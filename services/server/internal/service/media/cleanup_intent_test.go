package media

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mediago-dev/mediago-drama/services/server/internal/domain"
	"github.com/mediago-dev/mediago-drama/services/server/internal/repository"
	"golang.org/x/sys/unix"
)

func TestCleanupIntentInsertFailureKeepsAssetAndUnreferencedScanRecovers(t *testing.T) {
	root := t.TempDir()
	store := newCleanupTestStore(t, filepath.Join(root, "workspace.db"), filepath.Join(root, "media"))
	t.Cleanup(func() { _ = store.Close() })
	asset, claim := saveClaimedCleanupTestAsset(t, store, "intent-insert-failure")
	realPrepare := store.repo.PrepareMediaAssetCleanupIntent
	store.prepareCleanupIntent = func(domain.MediaAssetCleanupIntentModel) (bool, error) {
		return false, errors.New("forced intent insert failure")
	}
	errorsFound := store.CompensateGenerationAssetClaims([]MediaAssetClaim{claim})
	if len(errorsFound) == 0 {
		t.Fatal("CompensateGenerationAssetClaims() error count = 0")
	}
	if _, err := os.Stat(asset.FilePath); err != nil {
		t.Fatalf("asset moved after intent insert failure: %v", err)
	}
	model, err := store.repo.GetMediaAsset(asset.ID)
	if err != nil || !model.CleanupPending {
		t.Fatalf("cleanup candidate = pending %v err %v", model.CleanupPending, err)
	}

	store.prepareCleanupIntent = realPrepare
	if err := store.runMediaAssetCleanupBatch(context.Background()); err != nil {
		t.Fatalf("runMediaAssetCleanupBatch() error = %v", err)
	}
	if _, err := os.Stat(asset.FilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered cleanup file remains: %v", err)
	}
	if _, err := store.repo.GetMediaAsset(asset.ID); !errors.Is(err, repository.ErrRecordNotFound) {
		t.Fatalf("recovered cleanup DB row remains: %v", err)
	}
}

func TestCleanupRetryRestoresAssetWhenANewClaimArrives(t *testing.T) {
	root := t.TempDir()
	store := newCleanupTestStore(t, filepath.Join(root, "workspace.db"), filepath.Join(root, "media"))
	t.Cleanup(func() { _ = store.Close() })
	asset, claim := saveClaimedCleanupTestAsset(t, store, "claim-retry-barrier")
	realDelete := store.deleteCleanupAsset
	store.deleteCleanupAsset = func(string) (bool, bool, error) {
		return false, false, errors.New("forced delete failure")
	}
	if errorsFound := store.CompensateGenerationAssetClaims([]MediaAssetClaim{claim}); len(errorsFound) == 0 {
		t.Fatal("CompensateGenerationAssetClaims() error count = 0")
	}
	if _, err := os.Stat(asset.FilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("asset was not staged before retry: %v", err)
	}

	store.deleteCleanupAsset = realDelete
	started := make(chan struct{})
	release := make(chan struct{})
	store.cleanupBeforeAttempt = func(context.Context, string) {
		close(started)
		<-release
	}
	store.triggerMediaAssetCleanupMode(true)
	<-started
	store.generationClaimMu.Lock()
	store.generationClaims[asset.ID] = mediaAssetClaimState{count: 1, staged: true}
	store.generationClaimMu.Unlock()
	newClaim := MediaAssetClaim{AssetID: asset.ID}
	close(release)
	waitForCleanupWorker(t, store)
	store.CommitGenerationAssetClaims([]MediaAssetClaim{newClaim})
	if _, err := os.Stat(asset.FilePath); err != nil {
		t.Fatalf("newly claimed asset was not restored: %v", err)
	}
	model, err := store.repo.GetMediaAsset(asset.ID)
	if err != nil || model.CleanupPending {
		t.Fatalf("new claim DB state = pending %v err %v", model.CleanupPending, err)
	}
}

func TestMediaAssetCleanupIgnoresLegacyFilesystemJournalWithoutReadingIt(t *testing.T) {
	root := t.TempDir()
	mediaDir := filepath.Join(root, "media")
	trashDir := filepath.Join(mediaDir, ".trash")
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyJournal := filepath.Join(trashDir, "legacy.json")
	if err := unix.Mkfifo(legacyJournal, 0o600); err != nil {
		t.Fatal(err)
	}
	oversizedJournal := filepath.Join(trashDir, "oversized.json")
	file, err := os.OpenFile(oversizedJournal, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("must-not-read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(trashDir, "malicious.json")); err != nil {
		t.Fatal(err)
	}
	store := newCleanupTestStore(t, filepath.Join(root, "workspace.db"), mediaDir)
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	info, err := os.Lstat(legacyJournal)
	if err != nil {
		t.Fatalf("legacy journal missing: %v", err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("legacy journal mode = %v", info.Mode())
	}
	if info, err := os.Stat(oversizedJournal); err != nil {
		t.Fatalf("oversized legacy journal missing: %v", err)
	} else if info.Size() != 64<<20 {
		t.Fatalf("oversized legacy journal size = %d", info.Size())
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "must-not-read" {
		t.Fatalf("malicious legacy target changed: data %q err %v", data, err)
	}
}

func TestMediaAssetsCloseCancelsAndWaitsForCleanupWorker(t *testing.T) {
	root := t.TempDir()
	store := newCleanupTestStore(t, filepath.Join(root, "workspace.db"), filepath.Join(root, "media"))
	asset, _ := saveClaimedCleanupTestAsset(t, store, "close-worker")
	if err := store.repo.MarkMediaAssetCleanupPending(asset.ID, true); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	store.cleanupBeforeAttempt = func(context.Context, string) {
		close(started)
		<-release
	}
	store.triggerMediaAssetCleanup()
	<-started
	closed := make(chan error, 1)
	go func() { closed <- store.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close() returned before worker exited: %v", err)
	default:
	}
	close(release)
	if err := <-closed; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestInitializeMediaAssetCleanupDoesNotWaitForWorkerBatch(t *testing.T) {
	root := t.TempDir()
	mediaDir := filepath.Join(root, "media")
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	repo, err := repository.NewMediaAssetRepository(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(mediaDir, "pending.png")
	if err := os.WriteFile(filePath, []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := repo.CreateMediaAsset(domain.AssetModel{
		ID: "asset-pending", Kind: MediaKindImage, Filename: "pending.png", MIMEType: "image/png",
		RelPath: "library/pending.png", Source: MediaSourceGeneration, CleanupPending: true,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	store := &MediaAssets{repo: repo, dir: mediaDir, cleanupBeforeAttempt: func(context.Context, string) {
		close(started)
		<-release
	}}
	returned := make(chan struct{})
	go func() {
		store.initializeMediaAssetCleanup()
		close(returned)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup worker did not start")
	}
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup initialization waited for the worker batch")
	}
	close(release)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupIntentRetriesDBDeleteAfterStoreRestart(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "workspace.db")
	mediaDir := filepath.Join(root, "media")
	store := newCleanupTestStore(t, dbPath, mediaDir)
	asset, claim := saveClaimedCleanupTestAsset(t, store, "db-delete-restart")
	store.deleteCleanupAsset = func(string) (bool, bool, error) {
		return false, false, errors.New("forced DB delete failure")
	}
	if errorsFound := store.CompensateGenerationAssetClaims([]MediaAssetClaim{claim}); len(errorsFound) == 0 {
		t.Fatal("CompensateGenerationAssetClaims() error count = 0")
	}
	intent, err := store.repo.GetMediaAssetCleanupIntent(asset.ID)
	if err != nil || intent.Stage != cleanupStageStaged || intent.Attempts != 1 {
		t.Fatalf("persisted retry = %#v err %v", intent, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := NewMediaAssets(dbPath, mediaDir)
	defer restarted.Close()
	waitForCleanupWorker(t, restarted)
	if _, err := restarted.repo.GetMediaAsset(asset.ID); !errors.Is(err, repository.ErrRecordNotFound) {
		t.Fatalf("asset remains after restart: %v", err)
	}
	if _, err := restarted.repo.GetMediaAssetCleanupIntent(asset.ID); !errors.Is(err, repository.ErrRecordNotFound) {
		t.Fatalf("intent remains after restart: %v", err)
	}
}

func TestCleanupIntentRetriesTombstoneRemoveAfterStoreRestart(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "workspace.db")
	mediaDir := filepath.Join(root, "media")
	store := newCleanupTestStore(t, dbPath, mediaDir)
	asset, claim := saveClaimedCleanupTestAsset(t, store, "remove-restart")
	realUnlink := store.cleanupFD.unlinkat
	store.cleanupFD.unlinkat = func(int, string, int) error { return unix.EPERM }
	if errorsFound := store.CompensateGenerationAssetClaims([]MediaAssetClaim{claim}); len(errorsFound) == 0 {
		t.Fatal("CompensateGenerationAssetClaims() error count = 0")
	}
	intent, err := store.repo.GetMediaAssetCleanupIntent(asset.ID)
	if err != nil || intent.Stage != cleanupStageDBDeleted {
		t.Fatalf("remove retry intent = %#v err %v", intent, err)
	}
	store.cleanupFD.unlinkat = realUnlink
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := NewMediaAssets(dbPath, mediaDir)
	defer restarted.Close()
	waitForCleanupWorker(t, restarted)
	if _, err := restarted.repo.GetMediaAssetCleanupIntent(asset.ID); !errors.Is(err, repository.ErrRecordNotFound) {
		t.Fatalf("remove intent remains after restart: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mediaDir, ".trash", intent.FileTombstone)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tombstone remains after restart: %v", err)
	}
}

func TestCleanupUsesFixedTrashFDWhenVisibleTrashIsReplacedBySymlink(t *testing.T) {
	root := t.TempDir()
	mediaDir := filepath.Join(root, "media")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	store := newCleanupTestStore(t, filepath.Join(root, "workspace.db"), mediaDir)
	defer store.Close()
	asset, claim := saveClaimedCleanupTestAsset(t, store, "trash-fd-swap")
	ownedTrash := filepath.Join(mediaDir, ".trash-owned")
	if err := os.Rename(filepath.Join(mediaDir, ".trash"), ownedTrash); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(mediaDir, ".trash")); err != nil {
		t.Fatal(err)
	}
	if errorsFound := store.CompensateGenerationAssetClaims([]MediaAssetClaim{claim}); len(errorsFound) != 0 {
		t.Fatalf("CompensateGenerationAssetClaims() errors = %v", errorsFound)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside directory touched: entries %v err %v", entries, err)
	}
	if _, err := store.repo.GetMediaAsset(asset.ID); !errors.Is(err, repository.ErrRecordNotFound) {
		t.Fatalf("asset remains: %v", err)
	}
}

func TestCleanupRefusesSymlinkTombstoneWithoutTouchingTarget(t *testing.T) {
	root := t.TempDir()
	mediaDir := filepath.Join(root, "media")
	store := newCleanupTestStore(t, filepath.Join(root, "workspace.db"), mediaDir)
	defer store.Close()
	asset, claim := saveClaimedCleanupTestAsset(t, store, "tombstone-symlink")
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	tombstone := cleanupTombstoneName(asset.ID, "file", asset.FilePath)
	if err := os.Symlink(target, filepath.Join(mediaDir, ".trash", tombstone)); err != nil {
		t.Fatal(err)
	}
	if errorsFound := store.CompensateGenerationAssetClaims([]MediaAssetClaim{claim}); len(errorsFound) == 0 {
		t.Fatal("CompensateGenerationAssetClaims() error count = 0")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "target" {
		t.Fatalf("symlink target changed: data %q err %v", data, err)
	}
	if _, err := os.Stat(asset.FilePath); err != nil {
		t.Fatalf("source file moved through malicious tombstone: %v", err)
	}
}

func TestCleanupRejectsSourceAncestorSymlinkSwap(t *testing.T) {
	root := t.TempDir()
	mediaDir := filepath.Join(root, "media")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	store := newCleanupTestStore(t, filepath.Join(root, "workspace.db"), mediaDir)
	defer store.Close()
	asset, claim := saveClaimedCleanupTestAsset(t, store, "source-ancestor-swap")
	parent := filepath.Dir(asset.FilePath)
	movedParent := parent + "-owned"
	if err := os.Rename(parent, movedParent); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	if errorsFound := store.CompensateGenerationAssetClaims([]MediaAssetClaim{claim}); len(errorsFound) == 0 {
		t.Fatal("CompensateGenerationAssetClaims() error count = 0")
	}
	if _, err := os.Stat(filepath.Join(movedParent, filepath.Base(asset.FilePath))); err != nil {
		t.Fatalf("managed file was lost: %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside directory touched: entries %v err %v", entries, err)
	}
}

func TestCleanupIntentRejectsTraversalAndMalformedIdentity(t *testing.T) {
	root := t.TempDir()
	store := newCleanupTestStore(t, filepath.Join(root, "workspace.db"), filepath.Join(root, "media"))
	defer store.Close()
	outside := filepath.Join(root, "outside.png")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, intent := range []domain.MediaAssetCleanupIntentModel{
		{AssetID: "../escape", FileRoot: cleanupRootGlobalLibrary, FileRelPath: "safe.png", FileTombstone: "escape-file.png", Stage: cleanupStagePlanned},
		{AssetID: "asset-traversal", FileRoot: cleanupRootGlobalLibrary, FileRelPath: "../outside.png", FileTombstone: "asset-traversal-file.png", Stage: cleanupStagePlanned},
		{AssetID: "asset-absolute", FileRoot: cleanupRootGlobalLibrary, FileRelPath: outside, FileTombstone: "asset-absolute-file.png", Stage: cleanupStagePlanned},
	} {
		if err := store.processCleanupIntent(intent); err == nil {
			t.Fatalf("processCleanupIntent(%#v) error = nil", intent)
		}
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "outside" {
		t.Fatalf("outside file changed: data %q err %v", data, err)
	}
}

func TestCleanupContinuousFailureRunsOneFiniteAttemptAndPersistsRetry(t *testing.T) {
	root := t.TempDir()
	store := newCleanupTestStore(t, filepath.Join(root, "workspace.db"), filepath.Join(root, "media"))
	defer store.Close()
	asset, claim := saveClaimedCleanupTestAsset(t, store, "finite-retry")
	store.deleteCleanupAsset = func(string) (bool, bool, error) {
		return false, false, errors.New("continuous failure")
	}
	if errorsFound := store.CompensateGenerationAssetClaims([]MediaAssetClaim{claim}); len(errorsFound) == 0 {
		t.Fatal("CompensateGenerationAssetClaims() error count = 0")
	}
	before, err := store.repo.GetMediaAssetCleanupIntent(asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	store.triggerMediaAssetCleanup()
	waitForCleanupWorker(t, store)
	after, err := store.repo.GetMediaAssetCleanupIntent(asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Attempts != 1 || after.Attempts != 1 || after.NextAttemptAt == nil {
		t.Fatalf("bounded retry state before=%#v after=%#v", before, after)
	}
	store.cleanupIntentWorkerMu.Lock()
	running := store.cleanupIntentWorkerRun
	store.cleanupIntentWorkerMu.Unlock()
	if running {
		t.Fatal("cleanup worker did not exit")
	}
}

func waitForCleanupWorker(t *testing.T, store *MediaAssets) {
	t.Helper()
	store.cleanupIntentWorkerMu.Lock()
	done := store.cleanupIntentWorkerDone
	store.cleanupIntentWorkerMu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup worker did not exit")
	}
}

func newCleanupTestStore(t *testing.T, dbPath, mediaDir string) *MediaAssets {
	t.Helper()
	store := NewMediaAssets(dbPath, mediaDir)
	waitForCleanupWorker(t, store)
	return store
}

func saveClaimedCleanupTestAsset(t *testing.T, store *MediaAssets, payload string) (MediaAsset, MediaAssetClaim) {
	t.Helper()
	asset, claim, err := store.claimSavedGenerationAsset(func() (MediaAsset, bool, error) {
		return store.SaveBase64WithOptionsTracked(
			MediaKindImage,
			"image/png",
			base64.StdEncoding.EncodeToString([]byte(payload)),
			"",
			MediaAssetSaveOptions{Source: MediaSourceGeneration},
		)
	})
	if err != nil {
		t.Fatal(err)
	}
	return asset, claim
}
