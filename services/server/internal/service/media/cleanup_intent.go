package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mediago-dev/mediago-drama/services/server/internal/domain"
	"github.com/mediago-dev/mediago-drama/services/server/internal/service/shared"
	"golang.org/x/sys/unix"
)

const (
	mediaAssetCleanupBatchSize = 32
	cleanupStagePlanned        = "planned"
	cleanupStageStaged         = "staged"
	cleanupStageDBDeleted      = "db_deleted"
	cleanupRootGlobalLibrary   = "global_library"
	cleanupRootGlobalPoster    = "global_poster"
	cleanupRootProjectLibrary  = "project_library"
	cleanupRootProjectPoster   = "project_poster"
	maxMediaAssetCleanupID     = 128
)

var mediaAssetCleanupIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

type managedCleanupFS struct {
	rootFD   int
	trashFD  int
	renameat func(int, string, int, string, uint32) error
	unlinkat func(int, string, int) error
}

func newManagedCleanupFS(root string) (*managedCleanupFS, error) {
	resolved, err := filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return nil, fmt.Errorf("resolving managed media root: %w", err)
	}
	rootFD, err := unix.Open(resolved, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("opening managed media root: %w", err)
	}
	cleanup := &managedCleanupFS{rootFD: rootFD, trashFD: -1, renameat: unix.RenameatxNp, unlinkat: unix.Unlinkat}
	if err := unix.Mkdirat(rootFD, ".trash", 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		cleanup.Close()
		return nil, fmt.Errorf("creating managed cleanup directory: %w", err)
	}
	trashFD, err := unix.Openat(rootFD, ".trash", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		cleanup.Close()
		return nil, fmt.Errorf("opening managed cleanup directory: %w", err)
	}
	cleanup.trashFD = trashFD
	if err := unix.Fchmod(trashFD, 0o700); err != nil {
		cleanup.Close()
		return nil, fmt.Errorf("securing managed cleanup directory: %w", err)
	}
	if err := unix.Fsync(rootFD); err != nil {
		cleanup.Close()
		return nil, fmt.Errorf("syncing managed media root: %w", err)
	}
	return cleanup, nil
}

func (cleanup *managedCleanupFS) Close() error {
	if cleanup == nil {
		return nil
	}
	var closeErr error
	if cleanup.trashFD >= 0 {
		closeErr = errors.Join(closeErr, unix.Close(cleanup.trashFD))
		cleanup.trashFD = -1
	}
	if cleanup.rootFD >= 0 {
		closeErr = errors.Join(closeErr, unix.Close(cleanup.rootFD))
		cleanup.rootFD = -1
	}
	return closeErr
}

func validateMediaAssetCleanupID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxMediaAssetCleanupID || !mediaAssetCleanupIDPattern.MatchString(id) {
		return errors.New("invalid media asset cleanup ID")
	}
	return nil
}

func validateCleanupRelativePath(value string) error {
	value = filepath.FromSlash(strings.TrimSpace(value))
	if value == "" || filepath.IsAbs(value) || filepath.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		return errors.New("invalid managed media relative path")
	}
	return nil
}

func validateCleanupTombstone(assetID, role, name string) error {
	if err := validateMediaAssetCleanupID(assetID); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name || strings.Contains(name, "..") {
		return errors.New("invalid media cleanup tombstone")
	}
	if !strings.HasPrefix(name, assetID+"-"+role+".") && name != assetID+"-"+role {
		return errors.New("media cleanup tombstone does not belong to asset")
	}
	return nil
}

func openManagedRoot(path string) (int, error) {
	resolved, err := filepath.EvalSymlinks(strings.TrimSpace(path))
	if err != nil {
		return -1, err
	}
	return unix.Open(resolved, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

func openRelativeParent(rootFD int, relPath string) (int, string, error) {
	if err := validateCleanupRelativePath(relPath); err != nil {
		return -1, "", err
	}
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	current, err := unix.Dup(rootFD)
	if err != nil {
		return -1, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(current, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return -1, "", openErr
		}
		current = next
	}
	return current, parts[len(parts)-1], nil
}

func requireRegularAt(dirFD int, name string) error {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("managed media entry is not a regular file")
	}
	return nil
}

func syncRegularAt(dirFD int, name string) error {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("managed media entry is not a regular file")
	}
	return unix.Fsync(fd)
}

func (cleanup *managedCleanupFS) stage(rootFD int, relPath, tombstone string) error {
	parentFD, base, err := openRelativeParent(rootFD, relPath)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	sourceErr := syncRegularAt(parentFD, base)
	tombstoneErr := requireRegularAt(cleanup.trashFD, tombstone)
	if sourceErr == nil && tombstoneErr == nil {
		return errors.New("live and staged media files both exist")
	}
	if errors.Is(sourceErr, unix.ENOENT) && tombstoneErr == nil {
		return nil
	}
	if errors.Is(sourceErr, unix.ENOENT) && errors.Is(tombstoneErr, unix.ENOENT) {
		return nil
	}
	if sourceErr != nil {
		return sourceErr
	}
	if tombstoneErr != nil && !errors.Is(tombstoneErr, unix.ENOENT) {
		return tombstoneErr
	}
	if err := cleanup.renameat(parentFD, base, cleanup.trashFD, tombstone, unix.RENAME_EXCL); err != nil {
		return err
	}
	return errors.Join(unix.Fsync(parentFD), unix.Fsync(cleanup.trashFD))
}

func (cleanup *managedCleanupFS) restore(rootFD int, relPath, tombstone string) error {
	parentFD, base, err := openRelativeParent(rootFD, relPath)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	if err := requireRegularAt(cleanup.trashFD, tombstone); errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil {
		return err
	}
	if err := requireRegularAt(parentFD, base); err == nil {
		return errors.New("restoring staged media would overwrite a live file")
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := cleanup.renameat(cleanup.trashFD, tombstone, parentFD, base, unix.RENAME_EXCL); err != nil {
		return err
	}
	return errors.Join(unix.Fsync(parentFD), unix.Fsync(cleanup.trashFD))
}

func (cleanup *managedCleanupFS) remove(tombstone string) error {
	if err := requireRegularAt(cleanup.trashFD, tombstone); errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil {
		return err
	}
	if err := cleanup.unlinkat(cleanup.trashFD, tombstone, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return unix.Fsync(cleanup.trashFD)
}

func (store *MediaAssets) initializeMediaAssetCleanup() {
	if store == nil || store.repo == nil || store.initErr != nil {
		return
	}
	cleanup, err := newManagedCleanupFS(store.dir)
	if err != nil {
		store.initErr = err
		return
	}
	store.cleanupFD = cleanup
	store.cleanupIntentCtx, store.cleanupIntentCancel = context.WithCancel(context.Background())
	store.cleanupNow = time.Now
	store.prepareCleanupIntent = store.repo.PrepareMediaAssetCleanupIntent
	store.deleteCleanupAsset = func(id string) (bool, bool, error) {
		return store.repo.DeleteMediaAssetForCleanup(id, store.cleanupNow())
	}
	store.triggerMediaAssetCleanupMode(true)
}

// Close cancels and joins the finite cleanup worker before releasing directory FDs.
func (store *MediaAssets) Close() error {
	if store == nil {
		return nil
	}
	var closeErr error
	store.cleanupIntentCloseOnce.Do(func() {
		if store.cleanupIntentCancel != nil {
			store.cleanupIntentCancel()
		}
		store.cleanupIntentWorkerMu.Lock()
		store.cleanupIntentWorkerMu.Unlock()
		store.cleanupIntentWG.Wait()
		if store.cleanupFD != nil {
			closeErr = store.cleanupFD.Close()
		}
	})
	return closeErr
}

func (store *MediaAssets) triggerMediaAssetCleanup() {
	store.triggerMediaAssetCleanupMode(false)
}

func (store *MediaAssets) triggerMediaAssetCleanupMode(includeDeferred bool) {
	if store == nil || store.cleanupIntentCtx == nil || store.cleanupIntentCtx.Err() != nil {
		return
	}
	store.cleanupIntentWorkerMu.Lock()
	if store.cleanupIntentCtx.Err() != nil {
		store.cleanupIntentWorkerMu.Unlock()
		return
	}
	if store.cleanupIntentWorkerRun {
		store.cleanupIntentDeferred = store.cleanupIntentDeferred || includeDeferred
		store.cleanupIntentWorkerMu.Unlock()
		return
	}
	store.cleanupIntentWorkerRun = true
	store.cleanupIntentDeferred = false
	done := make(chan struct{})
	store.cleanupIntentWorkerDone = done
	store.cleanupIntentWG.Add(1)
	store.cleanupIntentWorkerMu.Unlock()
	go func() {
		defer store.cleanupIntentWG.Done()
		if err := store.runMediaAssetCleanupBatchMode(store.cleanupIntentCtx, includeDeferred); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("media cleanup retry remains pending")
		}
		store.cleanupIntentWorkerMu.Lock()
		store.cleanupIntentWorkerRun = false
		runDeferred := store.cleanupIntentDeferred
		store.cleanupIntentDeferred = false
		close(done)
		store.cleanupIntentWorkerMu.Unlock()
		if runDeferred {
			store.triggerMediaAssetCleanupMode(true)
		}
	}()
}

func (store *MediaAssets) runMediaAssetCleanupBatch(ctx context.Context) error {
	return store.runMediaAssetCleanupBatchMode(ctx, true)
}

func (store *MediaAssets) runMediaAssetCleanupBatchMode(ctx context.Context, includeDeferred bool) error {
	if store == nil || store.repo == nil || store.cleanupFD == nil {
		return errors.New("media cleanup is not initialized")
	}
	now := store.cleanupNow()
	intents, err := store.repo.ListMediaAssetCleanupIntents(mediaAssetCleanupBatchSize, now, includeDeferred)
	if err != nil {
		return err
	}
	pendingLimit := mediaAssetCleanupBatchSize - len(intents)
	if pendingLimit < 0 {
		pendingLimit = 0
	}
	pending, err := store.repo.ListMediaAssetsPendingCleanupWithoutIntent(pendingLimit)
	if err != nil {
		return err
	}
	var batchErr error
	for _, model := range pending {
		if err := ctx.Err(); err != nil {
			return errors.Join(batchErr, err)
		}
		if store.cleanupBeforeAttempt != nil {
			store.cleanupBeforeAttempt(ctx, model.ID)
		}
		if err := store.prepareAndProcessCleanup(model); err != nil {
			batchErr = errors.Join(batchErr, err)
		}
	}
	for _, intent := range intents {
		if err := ctx.Err(); err != nil {
			return errors.Join(batchErr, err)
		}
		if store.cleanupBeforeAttempt != nil {
			store.cleanupBeforeAttempt(ctx, intent.AssetID)
		}
		if err := store.processCleanupIntent(intent); err != nil {
			batchErr = errors.Join(batchErr, err)
		}
	}
	return batchErr
}

func (store *MediaAssets) prepareAndProcessCleanup(model domain.AssetModel) error {
	store.generationClaimMu.Lock()
	defer store.generationClaimMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	if state := store.generationClaims[model.ID]; state.count > 0 {
		return store.repo.CancelMediaAssetCleanup(model.ID)
	}
	intent, err := store.cleanupIntentForModel(model)
	if err != nil {
		return err
	}
	prepared, err := store.prepareCleanupIntent(intent)
	if err != nil || !prepared {
		return err
	}
	persisted, err := store.repo.GetMediaAssetCleanupIntent(intent.AssetID)
	if err != nil {
		return err
	}
	return store.processCleanupIntentLocked(persisted)
}

func (store *MediaAssets) processCleanupIntent(intent domain.MediaAssetCleanupIntentModel) error {
	store.generationClaimMu.Lock()
	defer store.generationClaimMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.processCleanupIntentLocked(intent)
}

func (store *MediaAssets) processCleanupIntentLocked(intent domain.MediaAssetCleanupIntentModel) error {
	if err := store.validateCleanupIntent(intent); err != nil {
		return store.recordCleanupFailure(intent, err)
	}
	if state := store.generationClaims[intent.AssetID]; state.count > 0 {
		if err := store.restoreCleanupIntentFiles(intent); err != nil {
			return store.recordCleanupFailure(intent, err)
		}
		return store.repo.CancelMediaAssetCleanup(intent.AssetID)
	}
	if intent.Stage == cleanupStagePlanned {
		if err := store.stageCleanupIntentFiles(intent); err != nil {
			return store.recordCleanupFailure(intent, err)
		}
		if err := store.repo.UpdateMediaAssetCleanupIntentStage(intent.AssetID, cleanupStageStaged, store.cleanupNow()); err != nil {
			return store.recordCleanupFailure(intent, err)
		}
		intent.Stage = cleanupStageStaged
	}
	if intent.Stage == cleanupStageStaged {
		deleted, referenced, err := store.deleteCleanupAsset(intent.AssetID)
		if err != nil {
			return store.recordCleanupFailure(intent, err)
		}
		if referenced {
			if err := store.restoreCleanupIntentFiles(intent); err != nil {
				return store.recordCleanupFailure(intent, err)
			}
			return store.repo.CancelMediaAssetCleanup(intent.AssetID)
		}
		if !deleted {
			// The repository also advances an already-missing row to db_deleted.
		}
		intent.Stage = cleanupStageDBDeleted
	}
	if intent.Stage == cleanupStageDBDeleted {
		if err := store.removeCleanupIntentFiles(intent); err != nil {
			return store.recordCleanupFailure(intent, err)
		}
		if err := store.repo.CompleteMediaAssetCleanup(intent.AssetID); err != nil {
			return store.recordCleanupFailure(intent, err)
		}
		if state, ok := store.generationClaims[intent.AssetID]; ok && state.count == 0 && state.staged {
			delete(store.generationClaims, intent.AssetID)
		}
	}
	return nil
}

func (store *MediaAssets) recordCleanupFailure(intent domain.MediaAssetCleanupIntentModel, cause error) error {
	attempts := intent.Attempts + 1
	delay := time.Second << min(attempts-1, 6)
	now := store.cleanupNow()
	if err := store.repo.RecordMediaAssetCleanupFailure(intent.AssetID, attempts, now.Add(delay), now); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (store *MediaAssets) cleanupIntentForModel(model domain.AssetModel) (domain.MediaAssetCleanupIntentModel, error) {
	if err := validateMediaAssetCleanupID(model.ID); err != nil {
		return domain.MediaAssetCleanupIntentModel{}, err
	}
	asset := store.mediaAssetRecordFromModel(model)
	intent := domain.MediaAssetCleanupIntentModel{
		AssetID: model.ID, ProjectID: asset.ProjectID, Stage: cleanupStagePlanned,
		CreatedAt: store.cleanupNow(), UpdatedAt: store.cleanupNow(),
	}
	var err error
	intent.FileRoot, intent.FileRelPath, err = store.cleanupManagedIdentity(asset.FilePath, asset.ProjectID, false)
	if err != nil {
		return domain.MediaAssetCleanupIntentModel{}, err
	}
	intent.FileTombstone = cleanupTombstoneName(model.ID, "file", asset.FilePath)
	if strings.TrimSpace(asset.PosterPath) != "" {
		intent.PosterRoot, intent.PosterRelPath, err = store.cleanupManagedIdentity(asset.PosterPath, asset.ProjectID, true)
		if err != nil {
			return domain.MediaAssetCleanupIntentModel{}, err
		}
		intent.PosterTombstone = cleanupTombstoneName(model.ID, "poster", asset.PosterPath)
	}
	return intent, nil
}

func cleanupTombstoneName(assetID, role, original string) string {
	ext := strings.ToLower(filepath.Ext(original))
	if len(ext) > 16 || strings.ContainsAny(ext, `/\\`) {
		ext = ""
	}
	return assetID + "-" + role + ext
}

func (store *MediaAssets) cleanupManagedIdentity(path, projectID string, poster bool) (string, string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", errors.New("managed media path is empty")
	}
	rootKind := cleanupRootGlobalLibrary
	rootPath := store.dir
	if domain.CleanProjectID(projectID) != "" {
		projectDir, err := store.projectDir(projectID)
		if err != nil {
			return "", "", err
		}
		rootKind, rootPath = cleanupRootProjectLibrary, shared.ProjectLibraryAssetsDir(projectDir)
		if poster {
			rootKind, rootPath = cleanupRootProjectPoster, shared.ProjectMediaPosterCacheDir(projectDir)
		}
	} else if poster {
		if strings.TrimSpace(store.workspaceRoot) == "" {
			return "", "", errors.New("workspace root is required for poster cleanup")
		}
		rootKind, rootPath = cleanupRootGlobalPoster, shared.WorkspacePathsFor(store.workspaceRoot).MediaPosterCacheDir()
	}
	rel, ok := relativePathUnder(rootPath, path)
	if !ok || validateCleanupRelativePath(filepath.FromSlash(rel)) != nil {
		return "", "", errors.New("media cleanup path is outside its managed root")
	}
	return rootKind, filepath.ToSlash(rel), nil
}

func (store *MediaAssets) validateCleanupIntent(intent domain.MediaAssetCleanupIntentModel) error {
	if err := validateMediaAssetCleanupID(intent.AssetID); err != nil {
		return err
	}
	if intent.Stage != cleanupStagePlanned && intent.Stage != cleanupStageStaged && intent.Stage != cleanupStageDBDeleted {
		return errors.New("invalid media cleanup stage")
	}
	for _, file := range []struct{ root, rel, tombstone, role string }{
		{intent.FileRoot, intent.FileRelPath, intent.FileTombstone, "file"},
		{intent.PosterRoot, intent.PosterRelPath, intent.PosterTombstone, "poster"},
	} {
		if file.rel == "" && file.tombstone == "" && file.role == "poster" {
			continue
		}
		if _, err := store.cleanupRootPath(file.root, intent.ProjectID); err != nil {
			return err
		}
		if err := validateCleanupRelativePath(filepath.FromSlash(file.rel)); err != nil {
			return err
		}
		if err := validateCleanupTombstone(intent.AssetID, file.role, file.tombstone); err != nil {
			return err
		}
	}
	return nil
}

func (store *MediaAssets) cleanupRootPath(kind, projectID string) (string, error) {
	switch kind {
	case cleanupRootGlobalLibrary:
		if domain.CleanProjectID(projectID) != "" {
			return "", errors.New("global cleanup root has project identity")
		}
		return store.dir, nil
	case cleanupRootGlobalPoster:
		if domain.CleanProjectID(projectID) != "" || strings.TrimSpace(store.workspaceRoot) == "" {
			return "", errors.New("invalid global poster cleanup root")
		}
		return shared.WorkspacePathsFor(store.workspaceRoot).MediaPosterCacheDir(), nil
	case cleanupRootProjectLibrary, cleanupRootProjectPoster:
		projectDir, err := store.projectDir(projectID)
		if err != nil {
			return "", err
		}
		if kind == cleanupRootProjectLibrary {
			return shared.ProjectLibraryAssetsDir(projectDir), nil
		}
		return shared.ProjectMediaPosterCacheDir(projectDir), nil
	default:
		return "", errors.New("invalid managed media cleanup root")
	}
}

func (store *MediaAssets) withCleanupFiles(intent domain.MediaAssetCleanupIntentModel, operation func(int, string, string) error) error {
	files := []struct{ root, rel, tombstone string }{
		{intent.FileRoot, intent.FileRelPath, intent.FileTombstone},
		{intent.PosterRoot, intent.PosterRelPath, intent.PosterTombstone},
	}
	for _, file := range files {
		if file.rel == "" {
			continue
		}
		rootPath, err := store.cleanupRootPath(file.root, intent.ProjectID)
		if err != nil {
			return err
		}
		rootFD := -1
		if file.root == cleanupRootGlobalLibrary {
			rootFD, err = unix.Dup(store.cleanupFD.rootFD)
		} else {
			rootFD, err = openManagedRoot(rootPath)
		}
		if err != nil {
			return err
		}
		err = operation(rootFD, filepath.FromSlash(file.rel), file.tombstone)
		_ = unix.Close(rootFD)
		if err != nil {
			return err
		}
	}
	return nil
}

func (store *MediaAssets) stageCleanupIntentFiles(intent domain.MediaAssetCleanupIntentModel) error {
	return store.withCleanupFiles(intent, store.cleanupFD.stage)
}

func (store *MediaAssets) restoreCleanupIntentFiles(intent domain.MediaAssetCleanupIntentModel) error {
	return store.withCleanupFiles(intent, store.cleanupFD.restore)
}

func (store *MediaAssets) removeCleanupIntentFiles(intent domain.MediaAssetCleanupIntentModel) error {
	for _, tombstone := range []string{intent.FileTombstone, intent.PosterTombstone} {
		if tombstone != "" {
			if err := store.cleanupFD.remove(tombstone); err != nil {
				return err
			}
		}
	}
	return nil
}
