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
	mediaAssetCleanupBatchSize   = 32
	cleanupStagePlanned          = "planned"
	cleanupStageStaged           = "staged"
	cleanupStageDBDeleted        = "db_deleted"
	cleanupStageQuarantined      = "quarantined"
	cleanupRootGlobalLibrary     = "global_library"
	cleanupRootGlobalPoster      = "global_poster"
	cleanupRootProjectLibrary    = "project_library"
	cleanupRootProjectPoster     = "project_poster"
	maxMediaAssetCleanupID       = 128
	maxMediaAssetCleanupAttempts = 30
	mediaAssetCleanupPassBudget  = 100 * time.Millisecond
)

var mediaAssetCleanupIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

type managedCleanupFS struct {
	rootFD   int
	trashFD  int
	renameat func(int, string, int, string, uint32) error
	unlinkat func(int, string, int) error
}

type openedCleanupFile struct {
	rootFD    int
	rootKind  string
	relPath   string
	tombstone string
	rootDev   int64
	rootIno   uint64
	fileDev   int64
	fileIno   uint64
}

type openedCleanupIntent struct {
	cleanup *managedCleanupFS
	files   []openedCleanupFile
}

func (opened *openedCleanupIntent) Close() {
	for index := range opened.files {
		if opened.files[index].rootFD >= 0 {
			_ = unix.Close(opened.files[index].rootFD)
			opened.files[index].rootFD = -1
		}
	}
}

type mediaCleanupWakeTimer interface {
	Stop() bool
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

func identityForFD(fd int) (int64, uint64, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return 0, 0, err
	}
	return int64(stat.Dev), uint64(stat.Ino), nil
}

func regularIdentityAt(rootFD int, relPath string) (int64, uint64, error) {
	parentFD, base, err := openRelativeParent(rootFD, relPath)
	if err != nil {
		return 0, 0, err
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(parentFD, base, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return 0, 0, err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return 0, 0, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return 0, 0, errors.New("managed media entry is not a regular file")
	}
	return int64(stat.Dev), uint64(stat.Ino), nil
}

func requireIdentityAt(rootFD int, relPath string, expectedDev int64, expectedIno uint64) error {
	dev, ino, err := regularIdentityAt(rootFD, relPath)
	if err != nil {
		return err
	}
	if dev != expectedDev || ino != expectedIno {
		return errors.New("managed media file identity changed")
	}
	return nil
}

func syncIdentityAt(rootFD int, relPath string, expectedDev int64, expectedIno uint64) error {
	parentFD, base, err := openRelativeParent(rootFD, relPath)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(parentFD, base, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	dev, ino, err := identityForFD(fd)
	if err != nil {
		return err
	}
	if dev != expectedDev || ino != expectedIno {
		return errors.New("managed media file identity changed")
	}
	return unix.Fsync(fd)
}

func (cleanup *managedCleanupFS) stage(rootFD int, relPath, tombstone string, expectedDev int64, expectedIno uint64) error {
	parentFD, base, err := openRelativeParent(rootFD, relPath)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	sourceErr := syncIdentityAt(parentFD, base, expectedDev, expectedIno)
	tombstoneErr := requireIdentityAt(cleanup.trashFD, tombstone, expectedDev, expectedIno)
	if sourceErr == nil && tombstoneErr == nil {
		return errors.New("live and staged media files both exist")
	}
	if errors.Is(sourceErr, unix.ENOENT) && tombstoneErr == nil {
		return nil
	}
	if errors.Is(sourceErr, unix.ENOENT) && errors.Is(tombstoneErr, unix.ENOENT) {
		return unix.ENOENT
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
	if err := requireIdentityAt(cleanup.trashFD, tombstone, expectedDev, expectedIno); err != nil {
		return err
	}
	return errors.Join(unix.Fsync(parentFD), unix.Fsync(cleanup.trashFD))
}

func (cleanup *managedCleanupFS) restore(rootFD int, relPath, tombstone string, expectedDev int64, expectedIno uint64) error {
	parentFD, base, err := openRelativeParent(rootFD, relPath)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	if err := requireIdentityAt(cleanup.trashFD, tombstone, expectedDev, expectedIno); errors.Is(err, unix.ENOENT) {
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
	if err := requireIdentityAt(parentFD, base, expectedDev, expectedIno); err != nil {
		return err
	}
	return errors.Join(unix.Fsync(parentFD), unix.Fsync(cleanup.trashFD))
}

func (cleanup *managedCleanupFS) remove(tombstone string, expectedDev int64, expectedIno uint64) error {
	if err := requireIdentityAt(cleanup.trashFD, tombstone, expectedDev, expectedIno); errors.Is(err, unix.ENOENT) {
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
	store.cleanupIntentCtx, store.cleanupIntentCancel = context.WithCancel(context.Background())
	store.cleanupNow = time.Now
	store.cleanupScheduleWake = func(delay time.Duration, fire func()) mediaCleanupWakeTimer {
		return time.AfterFunc(delay, fire)
	}
	store.openCleanupFS = func() (*managedCleanupFS, error) { return newManagedCleanupFS(store.dir) }
	store.createCleanupCandidate = store.repo.CreateMediaAssetWithCleanupIntent
	store.deleteCleanupAsset = func(id string) (bool, bool, error) {
		return store.repo.DeleteMediaAssetForCleanup(id, store.cleanupNow())
	}
	store.triggerMediaAssetCleanupMode(true)
}

// Close cancels and joins the finite cleanup worker and its retry timer.
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
		if store.cleanupWakeTimer != nil {
			store.cleanupWakeTimer.Stop()
			store.cleanupWakeTimer = nil
			store.cleanupWakeToken++
		}
		store.cleanupIntentWorkerMu.Unlock()
		store.cleanupIntentWG.Wait()
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
	if store.cleanupWakeTimer != nil {
		store.cleanupWakeTimer.Stop()
		store.cleanupWakeTimer = nil
		store.cleanupWakeToken++
	}
	store.cleanupIntentWorkerRun = true
	store.cleanupIntentDeferred = false
	done := make(chan struct{})
	store.cleanupIntentWorkerDone = done
	store.cleanupIntentWG.Add(1)
	store.cleanupIntentWorkerMu.Unlock()
	go func() {
		defer store.cleanupIntentWG.Done()
		_, runErr := store.runMediaAssetCleanupPass(store.cleanupIntentCtx, includeDeferred)
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			slog.Warn("media cleanup retry remains pending")
		}
		nextAt, nextErr := store.repo.NextMediaAssetCleanupAttempt(store.cleanupNow())
		if nextErr != nil {
			runErr = errors.Join(runErr, nextErr)
			slog.Warn("media cleanup scheduling retry remains pending")
		}
		store.cleanupIntentWorkerMu.Lock()
		store.cleanupIntentWorkerRun = false
		runDeferred := store.cleanupIntentDeferred
		store.cleanupIntentDeferred = false
		close(done)
		if !runDeferred && store.cleanupIntentCtx.Err() == nil {
			var delay time.Duration
			schedule := false
			if nextErr != nil {
				delay, schedule = time.Second, true
			} else if nextAt != nil {
				delay, schedule = max(nextAt.Sub(store.cleanupNow()), time.Millisecond), true
				if runErr != nil {
					delay = max(delay, time.Second)
				}
			}
			if schedule {
				store.scheduleMediaCleanupWakeLocked(delay)
			}
		}
		store.cleanupIntentWorkerMu.Unlock()
		if runDeferred {
			store.triggerMediaAssetCleanupMode(true)
		}
	}()
}

func (store *MediaAssets) scheduleMediaCleanupWakeLocked(delay time.Duration) {
	if store.cleanupScheduleWake == nil || store.cleanupIntentCtx == nil || store.cleanupIntentCtx.Err() != nil {
		return
	}
	store.cleanupWakeToken++
	token := store.cleanupWakeToken
	store.cleanupWakeTimer = store.cleanupScheduleWake(delay, func() {
		store.cleanupIntentWorkerMu.Lock()
		if token != store.cleanupWakeToken || store.cleanupIntentCtx.Err() != nil {
			store.cleanupIntentWorkerMu.Unlock()
			return
		}
		store.cleanupWakeTimer = nil
		store.cleanupIntentWorkerMu.Unlock()
		store.triggerMediaAssetCleanup()
	})
}

func (store *MediaAssets) runMediaAssetCleanupBatch(ctx context.Context) error {
	_, err := store.runMediaAssetCleanupPass(ctx, true)
	return err
}

func (store *MediaAssets) runMediaAssetCleanupPass(ctx context.Context, includeDeferred bool) (int, error) {
	if store == nil || store.repo == nil || store.openCleanupFS == nil {
		return 0, errors.New("media cleanup is not initialized")
	}
	now := store.cleanupNow()
	intents, err := store.repo.ListMediaAssetCleanupIntents(mediaAssetCleanupBatchSize, now, includeDeferred)
	if err != nil {
		return 0, err
	}
	var batchErr error
	processed := 0
	started := time.Now()
	for _, intent := range intents {
		if processed > 0 && time.Since(started) >= mediaAssetCleanupPassBudget {
			return processed, batchErr
		}
		if err := ctx.Err(); err != nil {
			return processed, errors.Join(batchErr, err)
		}
		if store.cleanupBeforeAttempt != nil {
			store.cleanupBeforeAttempt(ctx, intent.AssetID)
		}
		if err := store.processCleanupIntent(intent); err != nil {
			batchErr = errors.Join(batchErr, err)
		}
		processed++
	}
	return processed, batchErr
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
		return store.quarantineCleanupIntent(intent, err)
	}
	if err := store.validateCleanupIntentAssetBinding(intent); err != nil {
		return store.quarantineCleanupIntent(intent, err)
	}
	cleanup, err := store.openCleanupFS()
	if err != nil {
		return store.recordCleanupFailure(intent, err)
	}
	defer cleanup.Close()
	if store.cleanupAfterOpen != nil {
		store.cleanupAfterOpen()
	}
	opened, err := store.openCleanupIntent(cleanup, intent)
	if err != nil {
		return store.recordCleanupFailure(intent, err)
	}
	defer opened.Close()
	if state := store.generationClaims[intent.AssetID]; state.count > 0 {
		return store.deferCleanupIntent(intent)
	}
	if store.cleanupBeforeMutation != nil {
		store.cleanupBeforeMutation()
	}
	if err := store.verifyOpenedCleanupIntent(opened, intent); err != nil {
		return store.quarantineCleanupIntent(intent, err)
	}
	if intent.Stage == cleanupStagePlanned {
		if err := store.stageCleanupIntentFiles(opened); err != nil {
			return store.recordCleanupFailure(intent, err)
		}
		if err := store.repo.UpdateMediaAssetCleanupIntentStage(intent.AssetID, cleanupStageStaged, store.cleanupNow()); err != nil {
			return store.recordCleanupFailure(intent, err)
		}
		intent.Stage = cleanupStageStaged
	}
	if intent.Stage == cleanupStageStaged {
		if store.cleanupBeforeDelete != nil {
			store.cleanupBeforeDelete()
		}
		if err := store.verifyOpenedCleanupIntent(opened, intent); err != nil {
			return store.quarantineCleanupIntent(intent, err)
		}
		deleted, referenced, err := store.deleteCleanupAsset(intent.AssetID)
		if err != nil {
			return store.recordCleanupFailure(intent, err)
		}
		if referenced {
			if err := store.restoreCleanupIntentFiles(opened); err != nil {
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
		if store.cleanupBeforeRemove != nil {
			store.cleanupBeforeRemove()
		}
		if err := store.verifyOpenedCleanupIntent(opened, intent); err != nil {
			return store.quarantineCleanupIntent(intent, err)
		}
		if err := store.removeCleanupIntentFiles(opened); err != nil {
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

func (store *MediaAssets) quarantineCleanupIntent(intent domain.MediaAssetCleanupIntentModel, cause error) error {
	if err := store.repo.QuarantineMediaAssetCleanupIntent(intent.AssetID, store.cleanupNow()); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (store *MediaAssets) validateCleanupIntentAssetBinding(intent domain.MediaAssetCleanupIntentModel) error {
	if intent.Stage == cleanupStageDBDeleted {
		return nil
	}
	model, err := store.repo.GetMediaAsset(intent.AssetID)
	if err != nil {
		return errors.Join(errors.New("media cleanup asset binding is unavailable"), err)
	}
	if !model.CleanupPending ||
		domain.CleanProjectID(domain.StringValue(model.ProjectID)) != domain.CleanProjectID(intent.ProjectID) ||
		model.RelPath != intent.AssetRelPath || model.PosterRelPath != intent.AssetPosterPath {
		return errors.New("media cleanup asset binding changed")
	}
	return nil
}

func (store *MediaAssets) recordCleanupFailure(intent domain.MediaAssetCleanupIntentModel, cause error) error {
	if intent.Attempts < 0 || intent.Attempts > maxMediaAssetCleanupAttempts {
		return errors.Join(cause, errors.New("invalid media cleanup attempts"))
	}
	attempts := min(intent.Attempts+1, maxMediaAssetCleanupAttempts)
	if attempts >= maxMediaAssetCleanupAttempts {
		return errors.Join(cause, store.repo.QuarantineMediaAssetCleanupIntent(intent.AssetID, store.cleanupNow()))
	}
	shift := min(max(attempts-1, 0), 6)
	delay := time.Second << uint(shift)
	now := store.cleanupNow()
	if err := store.repo.RecordMediaAssetCleanupFailure(intent.AssetID, attempts, now.Add(delay), now); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (store *MediaAssets) deferCleanupIntent(intent domain.MediaAssetCleanupIntentModel) error {
	now := store.cleanupNow()
	return store.repo.DeferMediaAssetCleanupIntent(intent.AssetID, now.Add(time.Second), now)
}

func (store *MediaAssets) cleanupIntentForModel(model domain.AssetModel) (domain.MediaAssetCleanupIntentModel, error) {
	if err := validateMediaAssetCleanupID(model.ID); err != nil {
		return domain.MediaAssetCleanupIntentModel{}, err
	}
	asset := store.mediaAssetRecordFromModel(model)
	intent := domain.MediaAssetCleanupIntentModel{
		AssetID: model.ID, ProjectID: asset.ProjectID, AssetRelPath: model.RelPath, AssetPosterPath: model.PosterRelPath, Stage: cleanupStagePlanned,
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
	cleanup, err := store.openCleanupFS()
	if err != nil {
		return domain.MediaAssetCleanupIntentModel{}, err
	}
	defer cleanup.Close()
	if err := store.populateCleanupIntentIdentity(cleanup, &intent); err != nil {
		return domain.MediaAssetCleanupIntentModel{}, err
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
	if intent.Attempts < 0 || intent.Attempts > maxMediaAssetCleanupAttempts {
		return errors.New("invalid media cleanup attempts")
	}
	filePath := store.assetFilePath(intent.ProjectID, intent.AssetRelPath)
	expectedFileRoot, expectedFileRel, err := store.cleanupManagedIdentity(filePath, intent.ProjectID, false)
	if err != nil || intent.FileRoot != expectedFileRoot || intent.FileRelPath != expectedFileRel || intent.FileTombstone != cleanupTombstoneName(intent.AssetID, "file", filePath) {
		return errors.New("media cleanup file identity is not canonical")
	}
	if intent.FileRootDev == 0 || intent.FileRootIno == 0 || intent.FileDev == 0 || intent.FileIno == 0 || intent.TrashDev == 0 || intent.TrashIno == 0 {
		return errors.New("media cleanup filesystem identity is incomplete")
	}
	if intent.AssetPosterPath == "" {
		if intent.PosterRoot != "" || intent.PosterRelPath != "" || intent.PosterTombstone != "" || intent.PosterRootDev != 0 || intent.PosterRootIno != 0 || intent.PosterDev != 0 || intent.PosterIno != 0 {
			return errors.New("media cleanup poster identity is not canonical")
		}
	} else {
		posterPath := store.assetFilePath(intent.ProjectID, intent.AssetPosterPath)
		expectedPosterRoot, expectedPosterRel, posterErr := store.cleanupManagedIdentity(posterPath, intent.ProjectID, true)
		if posterErr != nil || intent.PosterRoot != expectedPosterRoot || intent.PosterRelPath != expectedPosterRel || intent.PosterTombstone != cleanupTombstoneName(intent.AssetID, "poster", posterPath) || intent.PosterRootDev == 0 || intent.PosterRootIno == 0 || intent.PosterDev == 0 || intent.PosterIno == 0 {
			return errors.New("media cleanup poster identity is not canonical")
		}
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

func (store *MediaAssets) cleanupRootFD(cleanup *managedCleanupFS, kind, projectID string) (int, error) {
	rootPath, err := store.cleanupRootPath(kind, projectID)
	if err != nil {
		return -1, err
	}
	if kind == cleanupRootGlobalLibrary {
		return unix.Dup(cleanup.rootFD)
	}
	return openManagedRoot(rootPath)
}

func (store *MediaAssets) cleanupCurrentRootFD(kind, projectID string) (int, error) {
	rootPath, err := store.cleanupRootPath(kind, projectID)
	if err != nil {
		return -1, err
	}
	return openManagedRoot(rootPath)
}

func (store *MediaAssets) populateCleanupIntentIdentity(cleanup *managedCleanupFS, intent *domain.MediaAssetCleanupIntentModel) error {
	trashDev, trashIno, err := identityForFD(cleanup.trashFD)
	if err != nil {
		return err
	}
	intent.TrashDev, intent.TrashIno = trashDev, trashIno
	for _, file := range []struct {
		root, rel string
		rootDev   *int64
		rootIno   *uint64
		fileDev   *int64
		fileIno   *uint64
	}{
		{intent.FileRoot, intent.FileRelPath, &intent.FileRootDev, &intent.FileRootIno, &intent.FileDev, &intent.FileIno},
		{intent.PosterRoot, intent.PosterRelPath, &intent.PosterRootDev, &intent.PosterRootIno, &intent.PosterDev, &intent.PosterIno},
	} {
		if file.rel == "" {
			continue
		}
		rootFD, err := store.cleanupRootFD(cleanup, file.root, intent.ProjectID)
		if err != nil {
			return err
		}
		*file.rootDev, *file.rootIno, err = identityForFD(rootFD)
		if err == nil {
			*file.fileDev, *file.fileIno, err = regularIdentityAt(rootFD, filepath.FromSlash(file.rel))
		}
		_ = unix.Close(rootFD)
		if err != nil {
			return err
		}
	}
	return nil
}

func (store *MediaAssets) openCleanupIntent(cleanup *managedCleanupFS, intent domain.MediaAssetCleanupIntentModel) (*openedCleanupIntent, error) {
	trashDev, trashIno, err := identityForFD(cleanup.trashFD)
	if err != nil || trashDev != intent.TrashDev || trashIno != intent.TrashIno {
		return nil, errors.New("media cleanup trash identity changed")
	}
	opened := &openedCleanupIntent{cleanup: cleanup}
	for _, file := range []struct {
		root, rel, tombstone string
		rootDev              int64
		rootIno              uint64
		fileDev              int64
		fileIno              uint64
	}{
		{intent.FileRoot, intent.FileRelPath, intent.FileTombstone, intent.FileRootDev, intent.FileRootIno, intent.FileDev, intent.FileIno},
		{intent.PosterRoot, intent.PosterRelPath, intent.PosterTombstone, intent.PosterRootDev, intent.PosterRootIno, intent.PosterDev, intent.PosterIno},
	} {
		if file.rel == "" {
			continue
		}
		rootFD, openErr := store.cleanupRootFD(cleanup, file.root, intent.ProjectID)
		if openErr != nil {
			opened.Close()
			return nil, openErr
		}
		rootDev, rootIno, statErr := identityForFD(rootFD)
		if statErr != nil || rootDev != file.rootDev || rootIno != file.rootIno {
			_ = unix.Close(rootFD)
			opened.Close()
			return nil, errors.New("media cleanup root identity changed")
		}
		opened.files = append(opened.files, openedCleanupFile{
			rootFD: rootFD, rootKind: file.root, relPath: filepath.FromSlash(file.rel), tombstone: file.tombstone,
			rootDev: file.rootDev, rootIno: file.rootIno, fileDev: file.fileDev, fileIno: file.fileIno,
		})
	}
	if err := store.verifyOpenedCleanupIntent(opened, intent); err != nil {
		opened.Close()
		return nil, err
	}
	return opened, nil
}

func (store *MediaAssets) verifyOpenedCleanupIntent(opened *openedCleanupIntent, intent domain.MediaAssetCleanupIntentModel) error {
	trashDev, trashIno, err := identityForFD(opened.cleanup.trashFD)
	if err != nil || trashDev != intent.TrashDev || trashIno != intent.TrashIno {
		return errors.New("media cleanup trash identity changed")
	}
	for _, file := range opened.files {
		rootDev, rootIno, err := identityForFD(file.rootFD)
		if err != nil || rootDev != file.rootDev || rootIno != file.rootIno {
			return errors.New("media cleanup root identity changed")
		}
		currentRootFD, err := store.cleanupCurrentRootFD(file.rootKind, intent.ProjectID)
		if err != nil {
			return err
		}
		currentDev, currentIno, currentErr := identityForFD(currentRootFD)
		_ = unix.Close(currentRootFD)
		if currentErr != nil || currentDev != file.rootDev || currentIno != file.rootIno {
			return errors.New("media cleanup root identity changed")
		}
		sourceErr := requireIdentityAt(file.rootFD, file.relPath, file.fileDev, file.fileIno)
		tombErr := requireIdentityAt(opened.cleanup.trashFD, file.tombstone, file.fileDev, file.fileIno)
		switch intent.Stage {
		case cleanupStagePlanned:
			if sourceErr != nil && tombErr != nil {
				return errors.New("media cleanup file identity changed")
			}
		case cleanupStageStaged:
			if tombErr != nil {
				return errors.New("staged media cleanup file identity changed")
			}
		case cleanupStageDBDeleted:
			if tombErr != nil && !(errors.Is(sourceErr, unix.ENOENT) && errors.Is(tombErr, unix.ENOENT)) {
				return errors.New("deleted media cleanup file identity changed")
			}
		}
	}
	return nil
}

func (store *MediaAssets) stageCleanupIntentFiles(opened *openedCleanupIntent) error {
	for _, file := range opened.files {
		if err := opened.cleanup.stage(file.rootFD, file.relPath, file.tombstone, file.fileDev, file.fileIno); err != nil {
			return err
		}
	}
	return nil
}

func (store *MediaAssets) restoreCleanupIntentFiles(opened *openedCleanupIntent) error {
	for _, file := range opened.files {
		if err := opened.cleanup.restore(file.rootFD, file.relPath, file.tombstone, file.fileDev, file.fileIno); err != nil {
			return err
		}
	}
	return nil
}

func (store *MediaAssets) removeCleanupIntentFiles(opened *openedCleanupIntent) error {
	for _, file := range opened.files {
		if err := opened.cleanup.remove(file.tombstone, file.fileDev, file.fileIno); err != nil {
			return err
		}
	}
	return nil
}
