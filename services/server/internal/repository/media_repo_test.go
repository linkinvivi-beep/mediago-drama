package repository

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/mediago-dev/mediago-drama/services/server/internal/domain"
)

func TestMediaAssetRepositoryLifecycle(t *testing.T) {
	repo, err := NewMediaAssetRepository(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("NewMediaAssetRepository() error = %v", err)
	}
	seedRepositoryProject(t, repo.db, "alpha")

	asset := domain.AssetModel{
		ID:          "asset-1",
		ProjectID:   domain.StringPtr("alpha"),
		Kind:        "image",
		Filename:    "draft.png",
		MIMEType:    "image/png",
		SizeBytes:   12,
		URL:         "/api/v1/media-assets/asset-1/content",
		SourceURL:   "https://example.test/draft.png",
		ContentHash: "hash-1",
		Source:      "generated",
		RelPath:     "library/2026-05-22/asset-1.png",
		CreatedAt:   domain.TimeFromString("2026-05-22T00:00:00Z"),
		UpdatedAt:   domain.TimeFromString("2026-05-22T00:00:00Z"),
	}
	if err := repo.CreateMediaAsset(asset); err != nil {
		t.Fatalf("CreateMediaAsset() error = %v", err)
	}

	got, err := repo.GetMediaAsset(asset.ID)
	if err != nil {
		t.Fatalf("GetMediaAsset() error = %v", err)
	}
	if got.Filename != asset.Filename || got.RelPath != asset.RelPath || got.Source != asset.Source {
		t.Fatalf("GetMediaAsset() = %#v, want persisted filename/source/relative path", got)
	}

	bySource, err := repo.FindMediaAssetBySourceURL(asset.SourceURL)
	if err != nil {
		t.Fatalf("FindMediaAssetBySourceURL() error = %v", err)
	}
	if bySource.ID != asset.ID {
		t.Fatalf("FindMediaAssetBySourceURL().ID = %q, want %q", bySource.ID, asset.ID)
	}
	byScopedSource, err := repo.FindMediaAssetBySourceURLAndScope(
		asset.SourceURL,
		domain.StringValue(asset.ProjectID),
		asset.Source,
		"",
	)
	if err != nil {
		t.Fatalf("FindMediaAssetBySourceURLAndScope() error = %v", err)
	}
	if byScopedSource.ID != asset.ID {
		t.Fatalf("FindMediaAssetBySourceURLAndScope().ID = %q, want %q", byScopedSource.ID, asset.ID)
	}
	byContentHash, err := repo.FindMediaAssetByContentHashAndScope(
		asset.ContentHash,
		asset.Kind,
		domain.StringValue(asset.ProjectID),
		asset.Source,
		"",
	)
	if err != nil {
		t.Fatalf("FindMediaAssetByContentHashAndScope() error = %v", err)
	}
	if byContentHash.ID != asset.ID {
		t.Fatalf("FindMediaAssetByContentHashAndScope().ID = %q, want %q", byContentHash.ID, asset.ID)
	}
	if _, err := repo.FindMediaAssetBySourceURLAndScope(
		asset.SourceURL,
		"beta",
		asset.Source,
		"other-conversation",
	); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("FindMediaAssetBySourceURLAndScope(other) error = %v, want ErrRecordNotFound", err)
	}
	if _, err := repo.FindMediaAssetByContentHashAndScope(
		asset.ContentHash,
		asset.Kind,
		"beta",
		asset.Source,
		"other-conversation",
	); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("FindMediaAssetByContentHashAndScope(other) error = %v, want ErrRecordNotFound", err)
	}

	assets, err := repo.ListMediaAssets(10, "alpha")
	if err != nil {
		t.Fatalf("ListMediaAssets() error = %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("ListMediaAssets() len = %d, want 1", len(assets))
	}

	if err := repo.UpdateMediaAssetFilename(asset.ID, "final.png", "2026-05-22T00:01:00Z"); err != nil {
		t.Fatalf("UpdateMediaAssetFilename() error = %v", err)
	}
	got, err = repo.GetMediaAsset(asset.ID)
	if err != nil {
		t.Fatalf("GetMediaAsset() after update error = %v", err)
	}
	if got.Filename != "final.png" {
		t.Fatalf("updated Filename = %q, want final.png", got.Filename)
	}

	if err := repo.UpdateMediaAssetMetadata(asset.ID, map[string]any{
		"duration_seconds": 3.5,
		"width":            640,
		"height":           360,
		"poster_rel_path":  "library/2026-05-22/asset-1.poster.jpg",
		"poster_url":       "/api/v1/media-assets/asset-1/poster",
		"metadata_status":  "ready",
	}); err != nil {
		t.Fatalf("UpdateMediaAssetMetadata() error = %v", err)
	}
	got, err = repo.GetMediaAsset(asset.ID)
	if err != nil {
		t.Fatalf("GetMediaAsset() after metadata update error = %v", err)
	}
	if got.DurationSeconds != 3.5 || got.Width != 640 || got.Height != 360 {
		t.Fatalf("metadata = duration %v dimensions %dx%d, want 3.5 640x360", got.DurationSeconds, got.Width, got.Height)
	}
	if got.PosterURL != "/api/v1/media-assets/asset-1/poster" || got.MetadataStatus != "ready" {
		t.Fatalf("poster/status = %q/%q, want poster URL and ready", got.PosterURL, got.MetadataStatus)
	}

	if err := repo.UpdateMediaAssetStorage(asset.ID, map[string]any{
		"source":   "upload",
		"rel_path": "library/2026-05-22/asset-1.png",
	}); err != nil {
		t.Fatalf("UpdateMediaAssetStorage() error = %v", err)
	}
	got, err = repo.GetMediaAsset(asset.ID)
	if err != nil {
		t.Fatalf("GetMediaAsset() after storage update error = %v", err)
	}
	if got.Source != "upload" || got.RelPath != "library/2026-05-22/asset-1.png" {
		t.Fatalf("storage fields = %#v", got)
	}

	deleted, err := repo.DeleteMediaAsset(asset.ID)
	if err != nil {
		t.Fatalf("DeleteMediaAsset() error = %v", err)
	}
	if !deleted {
		t.Fatal("DeleteMediaAsset() deleted = false, want true")
	}
	if _, err := repo.GetMediaAsset(asset.ID); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("GetMediaAsset() after delete error = %v, want ErrRecordNotFound", err)
	}
}

func TestMediaAssetRepositoryListMediaAssetsFiltersByProject(t *testing.T) {
	repo, err := NewMediaAssetRepository(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("NewMediaAssetRepository() error = %v", err)
	}
	seedRepositoryProject(t, repo.db, "alpha")
	seedRepositoryProject(t, repo.db, "beta")

	for _, asset := range []domain.AssetModel{
		testAssetModel("asset-global", "", "2026-05-22T00:00:00Z"),
		testAssetModel("asset-alpha", "alpha", "2026-05-22T00:01:00Z"),
		testAssetModel("asset-beta", "beta", "2026-05-22T00:02:00Z"),
	} {
		if err := repo.CreateMediaAsset(asset); err != nil {
			t.Fatalf("CreateMediaAsset(%q) error = %v", asset.ID, err)
		}
	}

	studioAssets, err := repo.ListMediaAssets(10, "")
	if err != nil {
		t.Fatalf("ListMediaAssets(studio) error = %v", err)
	}
	if got := mediaAssetIDs(studioAssets); !equalStringSlices(got, []string{"asset-global"}) {
		t.Fatalf("studio asset IDs = %#v, want global only", got)
	}

	alphaAssets, err := repo.ListMediaAssets(10, "alpha")
	if err != nil {
		t.Fatalf("ListMediaAssets(alpha) error = %v", err)
	}
	if got := mediaAssetIDs(alphaAssets); !equalStringSlices(got, []string{"asset-alpha", "asset-global"}) {
		t.Fatalf("alpha asset IDs = %#v, want alpha and global", got)
	}
}

func TestMediaAssetRepositoryPreparesCleanupIntentOnlyForPendingUnreferencedAsset(t *testing.T) {
	repo, err := NewMediaAssetRepository(filepath.Join(t.TempDir(), "media.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	now := domain.TimeFromString("2026-08-30T00:00:00Z")
	asset := domain.AssetModel{
		ID: "asset-cleanup", Kind: "image", Filename: "image.png", MIMEType: "image/png",
		RelPath: "2026-08-30/image.png", Source: "generation", CleanupPending: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repo.CreateMediaAsset(asset); err != nil {
		t.Fatal(err)
	}
	intent := domain.MediaAssetCleanupIntentModel{
		AssetID: asset.ID, FileRoot: "global_library", FileRelPath: "2026-08-30/image.png",
		FileTombstone: "asset-cleanup-file.png", Stage: "planned", CreatedAt: now, UpdatedAt: now,
	}
	prepared, err := repo.PrepareMediaAssetCleanupIntent(intent)
	if err != nil || !prepared {
		t.Fatalf("PrepareMediaAssetCleanupIntent() = prepared %v err %v", prepared, err)
	}
	got, err := repo.GetMediaAssetCleanupIntent(asset.ID)
	if err != nil || got != intent {
		t.Fatalf("GetMediaAssetCleanupIntent() = %#v err %v", got, err)
	}

	if err := repo.DeleteMediaAssetCleanupIntent(asset.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkMediaAssetCleanupPending(asset.ID, false); err != nil {
		t.Fatal(err)
	}
	prepared, err = repo.PrepareMediaAssetCleanupIntent(intent)
	if err != nil || prepared {
		t.Fatalf("non-pending prepare = prepared %v err %v", prepared, err)
	}
}

func TestMediaAssetRepositoryListsPendingAssetsWithoutIntentInBoundedOrder(t *testing.T) {
	repo, err := NewMediaAssetRepository(filepath.Join(t.TempDir(), "media.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"asset-3", "asset-1", "asset-2"} {
		if err := repo.CreateMediaAsset(domain.AssetModel{
			ID: id, Kind: "image", Filename: id + ".png", MIMEType: "image/png",
			RelPath: id + ".png", Source: "generation", CleanupPending: true,
			CreatedAt: domain.TimeFromString(fmt.Sprintf("2026-08-30T00:00:0%dZ", index)),
			UpdatedAt: domain.TimeFromString(fmt.Sprintf("2026-08-30T00:00:0%dZ", index)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if prepared, err := repo.PrepareMediaAssetCleanupIntent(domain.MediaAssetCleanupIntentModel{
		AssetID: "asset-3", FileRoot: "global_library", FileRelPath: "asset-3.png",
		FileTombstone: "asset-3-file.png", Stage: "planned",
		CreatedAt: domain.TimeFromString("2026-08-30T00:00:00Z"), UpdatedAt: domain.TimeFromString("2026-08-30T00:00:00Z"),
	}); err != nil || !prepared {
		t.Fatalf("PrepareMediaAssetCleanupIntent() = prepared %v err %v", prepared, err)
	}
	assets, err := repo.ListMediaAssetsPendingCleanupWithoutIntent(1)
	if err != nil || len(assets) != 1 || assets[0].ID != "asset-1" {
		t.Fatalf("ListMediaAssetsPendingCleanupWithoutIntent() = %#v err %v", assets, err)
	}
}

func testAssetModel(id string, projectID string, updatedAt string) domain.AssetModel {
	return domain.AssetModel{
		ID:        id,
		Kind:      "image",
		Filename:  id + ".png",
		MIMEType:  "image/png",
		SizeBytes: 12,
		URL:       "/api/v1/media-assets/" + id + "/content",
		ProjectID: domain.StringPtr(projectID),
		RelPath:   "library/2026-05-22/" + id + ".png",
		CreatedAt: domain.TimeFromString("2026-05-22T00:00:00Z"),
		UpdatedAt: domain.TimeFromString(updatedAt),
	}
}

func mediaAssetIDs(assets []domain.AssetModel) []string {
	ids := make([]string, 0, len(assets))
	for _, asset := range assets {
		ids = append(ids, asset.ID)
	}
	return ids
}

func equalStringSlices(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
