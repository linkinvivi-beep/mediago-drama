package repository

import (
	"fmt"
	"strings"
	"time"

	"github.com/mediago-dev/mediago-drama/services/server/internal/domain"
	"gorm.io/gorm"
)

// MediaAssetRepository persists local media asset metadata.
type MediaAssetRepository struct {
	db *gorm.DB
}

// NewMediaAssetRepository opens the workspace database via the central workspace schema owner.
func NewMediaAssetRepository(dbPath string) (*MediaAssetRepository, error) {
	db, err := OpenWorkspaceDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening media asset repository database: %w", err)
	}
	return NewMediaAssetRepositoryFromDB(db), nil
}

// NewMediaAssetRepositoryFromDB creates a repository from an existing workspace DB.
func NewMediaAssetRepositoryFromDB(db *gorm.DB) *MediaAssetRepository {
	return &MediaAssetRepository{db: db}
}

// ListMediaAssets returns recently updated media assets visible to a project.
func (repo *MediaAssetRepository) ListMediaAssets(limit int, projectID string) ([]domain.AssetModel, error) {
	projectID = domain.CleanProjectID(projectID)
	models := []domain.AssetModel{}
	query := repo.db.Order("updated_at DESC").Limit(limit)
	if projectID == "" {
		query = query.Where("project_id IS NULL")
	} else {
		query = query.Where("project_id IS NULL OR project_id = ?", projectID)
	}
	if err := query.Find(&models).Error; err != nil {
		return nil, fmt.Errorf("listing media assets: %w", err)
	}
	return models, nil
}

// ListAllMediaAssets returns every media asset ordered by update time.
func (repo *MediaAssetRepository) ListAllMediaAssets() ([]domain.AssetModel, error) {
	models := []domain.AssetModel{}
	if err := repo.db.Order("updated_at DESC").Find(&models).Error; err != nil {
		return nil, fmt.Errorf("listing all media assets: %w", err)
	}
	return models, nil
}

// GetMediaAsset returns a media asset by ID.
func (repo *MediaAssetRepository) GetMediaAsset(id string) (domain.AssetModel, error) {
	var model domain.AssetModel
	err := repo.db.First(&model, "id = ?", strings.TrimSpace(id)).Error
	if IsRecordNotFound(err) {
		return domain.AssetModel{}, ErrRecordNotFound
	}
	if err != nil {
		return domain.AssetModel{}, fmt.Errorf("getting media asset: %w", err)
	}
	return model, nil
}

// FindMediaAssetBySourceURL returns the latest media asset for a source URL.
func (repo *MediaAssetRepository) FindMediaAssetBySourceURL(sourceURL string) (domain.AssetModel, error) {
	var model domain.AssetModel
	err := repo.db.Where("source_url = ?", strings.TrimSpace(sourceURL)).
		Order("updated_at DESC").
		First(&model).Error
	if IsRecordNotFound(err) {
		return domain.AssetModel{}, ErrRecordNotFound
	}
	if err != nil {
		return domain.AssetModel{}, fmt.Errorf("finding media asset by source URL: %w", err)
	}
	return model, nil
}

// FindMediaAssetBySourceURLAndScope returns the latest media asset for a source URL in one generation scope.
func (repo *MediaAssetRepository) FindMediaAssetBySourceURLAndScope(sourceURL string, projectID string, source string, conversationID string) (domain.AssetModel, error) {
	var model domain.AssetModel
	err := repo.db.Where(
		"source_url = ? AND COALESCE(project_id, '') = ? AND source = ?",
		strings.TrimSpace(sourceURL),
		domain.CleanProjectID(projectID),
		strings.TrimSpace(source),
	).
		Order("updated_at DESC").
		First(&model).Error
	if IsRecordNotFound(err) {
		return domain.AssetModel{}, ErrRecordNotFound
	}
	if err != nil {
		return domain.AssetModel{}, fmt.Errorf("finding media asset by source URL and scope: %w", err)
	}
	return model, nil
}

// FindMediaAssetByContentHashAndScope returns the latest generated asset with identical content in one generation scope.
func (repo *MediaAssetRepository) FindMediaAssetByContentHashAndScope(contentHash string, kind string, projectID string, source string, conversationID string) (domain.AssetModel, error) {
	var model domain.AssetModel
	err := repo.db.Where(
		"content_hash = ? AND kind = ? AND COALESCE(project_id, '') = ? AND source = ?",
		strings.TrimSpace(contentHash),
		strings.TrimSpace(kind),
		domain.CleanProjectID(projectID),
		strings.TrimSpace(source),
	).
		Order("updated_at DESC").
		First(&model).Error
	if IsRecordNotFound(err) {
		return domain.AssetModel{}, ErrRecordNotFound
	}
	if err != nil {
		return domain.AssetModel{}, fmt.Errorf("finding media asset by content hash and scope: %w", err)
	}
	return model, nil
}

// CreateMediaAsset inserts a media asset.
func (repo *MediaAssetRepository) CreateMediaAsset(model domain.AssetModel) error {
	model.ProjectID = domain.StringPtr(domain.CleanProjectID(domain.StringValue(model.ProjectID)))
	if err := repo.db.Create(&model).Error; err != nil {
		return fmt.Errorf("creating media asset: %w", err)
	}
	return nil
}

// DeleteMediaAsset deletes a media asset by ID.
func (repo *MediaAssetRepository) DeleteMediaAsset(id string) (bool, error) {
	result := repo.db.Delete(&domain.AssetModel{}, "id = ?", strings.TrimSpace(id))
	if result.Error != nil {
		return false, fmt.Errorf("deleting media asset: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

// DeleteMediaAssetIfUnreferenced atomically removes one asset row only when no
// workspace relation currently points at it.
func (repo *MediaAssetRepository) DeleteMediaAssetIfUnreferenced(id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	result := repo.db.Exec(`DELETE FROM assets
		WHERE id = ?
		  AND NOT EXISTS (SELECT 1 FROM generation_task_assets WHERE asset_id = ?)
		  AND NOT EXISTS (SELECT 1 FROM generation_task_references WHERE asset_id = ?)
		  AND NOT EXISTS (SELECT 1 FROM project_selected_assets WHERE asset_id = ?)
		  AND NOT EXISTS (SELECT 1 FROM project_reference_assets WHERE asset_id = ?)`, id, id, id, id, id)
	if result.Error != nil {
		return false, fmt.Errorf("deleting unreferenced media asset: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

// MarkMediaAssetCleanupPending makes a failed task-owned asset discoverable by
// the cleanup recovery scan even when intent creation is temporarily blocked.
func (repo *MediaAssetRepository) MarkMediaAssetCleanupPending(id string, pending bool) error {
	result := repo.db.Model(&domain.AssetModel{}).
		Where("id = ?", strings.TrimSpace(id)).
		Update("cleanup_pending", pending)
	if result.Error != nil {
		return fmt.Errorf("marking media asset cleanup pending: %w", result.Error)
	}
	return nil
}

// PrepareMediaAssetCleanupIntent atomically confirms that a cleanup-pending
// asset is unreferenced and persists its managed relative file identities.
func (repo *MediaAssetRepository) PrepareMediaAssetCleanupIntent(intent domain.MediaAssetCleanupIntentModel) (bool, error) {
	intent.AssetID = strings.TrimSpace(intent.AssetID)
	if intent.AssetID == "" {
		return false, nil
	}
	prepared := false
	err := repo.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&domain.AssetModel{}).
			Where(`id = ? AND cleanup_pending = ?
				AND NOT EXISTS (SELECT 1 FROM generation_task_assets WHERE asset_id = ?)
				AND NOT EXISTS (SELECT 1 FROM generation_task_references WHERE asset_id = ?)
				AND NOT EXISTS (SELECT 1 FROM project_selected_assets WHERE asset_id = ?)
				AND NOT EXISTS (SELECT 1 FROM project_reference_assets WHERE asset_id = ?)`,
				intent.AssetID, true, intent.AssetID, intent.AssetID, intent.AssetID, intent.AssetID).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		var existing int64
		if err := tx.Model(&domain.MediaAssetCleanupIntentModel{}).
			Where("asset_id = ?", intent.AssetID).Count(&existing).Error; err != nil {
			return err
		}
		if existing == 0 {
			if err := tx.Create(&intent).Error; err != nil {
				return err
			}
		}
		prepared = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("preparing media asset cleanup intent: %w", err)
	}
	return prepared, nil
}

// GetMediaAssetCleanupIntent returns one persisted cleanup intent.
func (repo *MediaAssetRepository) GetMediaAssetCleanupIntent(assetID string) (domain.MediaAssetCleanupIntentModel, error) {
	var intent domain.MediaAssetCleanupIntentModel
	err := repo.db.First(&intent, "asset_id = ?", strings.TrimSpace(assetID)).Error
	if IsRecordNotFound(err) {
		return domain.MediaAssetCleanupIntentModel{}, ErrRecordNotFound
	}
	if err != nil {
		return domain.MediaAssetCleanupIntentModel{}, fmt.Errorf("getting media asset cleanup intent: %w", err)
	}
	return intent, nil
}

// DeleteMediaAssetCleanupIntent removes one completed or cancelled intent.
func (repo *MediaAssetRepository) DeleteMediaAssetCleanupIntent(assetID string) error {
	if err := repo.db.Delete(&domain.MediaAssetCleanupIntentModel{}, "asset_id = ?", strings.TrimSpace(assetID)).Error; err != nil {
		return fmt.Errorf("deleting media asset cleanup intent: %w", err)
	}
	return nil
}

// ListMediaAssetsPendingCleanupWithoutIntent returns bounded recovery
// candidates whose intent insert did not complete.
func (repo *MediaAssetRepository) ListMediaAssetsPendingCleanupWithoutIntent(limit int) ([]domain.AssetModel, error) {
	if limit <= 0 {
		return nil, nil
	}
	assets := []domain.AssetModel{}
	err := repo.db.Model(&domain.AssetModel{}).
		Where(`cleanup_pending = ?
			AND NOT EXISTS (SELECT 1 FROM media_asset_cleanup_intents WHERE asset_id = assets.id)
			AND NOT EXISTS (SELECT 1 FROM generation_task_assets WHERE asset_id = assets.id)
			AND NOT EXISTS (SELECT 1 FROM generation_task_references WHERE asset_id = assets.id)
			AND NOT EXISTS (SELECT 1 FROM project_selected_assets WHERE asset_id = assets.id)
			AND NOT EXISTS (SELECT 1 FROM project_reference_assets WHERE asset_id = assets.id)`, true).
		Order("created_at ASC, id ASC").Limit(limit).Find(&assets).Error
	if err != nil {
		return nil, fmt.Errorf("listing media assets pending cleanup: %w", err)
	}
	return assets, nil
}

// ListMediaAssetCleanupIntents returns a bounded retry batch.
func (repo *MediaAssetRepository) ListMediaAssetCleanupIntents(limit int, now time.Time, includeDeferred bool) ([]domain.MediaAssetCleanupIntentModel, error) {
	if limit <= 0 {
		return nil, nil
	}
	intents := []domain.MediaAssetCleanupIntentModel{}
	query := repo.db.Model(&domain.MediaAssetCleanupIntentModel{})
	if !includeDeferred {
		query = query.Where("next_attempt_at IS NULL OR next_attempt_at <= ?", now)
	}
	if err := query.Order("updated_at ASC, asset_id ASC").Limit(limit).Find(&intents).Error; err != nil {
		return nil, fmt.Errorf("listing media asset cleanup intents: %w", err)
	}
	return intents, nil
}

// UpdateMediaAssetCleanupIntentStage advances recoverable file/DB state.
func (repo *MediaAssetRepository) UpdateMediaAssetCleanupIntentStage(assetID string, stage string, updatedAt time.Time) error {
	if err := repo.db.Model(&domain.MediaAssetCleanupIntentModel{}).
		Where("asset_id = ?", strings.TrimSpace(assetID)).
		Updates(map[string]any{"stage": strings.TrimSpace(stage), "updated_at": updatedAt}).Error; err != nil {
		return fmt.Errorf("updating media asset cleanup intent stage: %w", err)
	}
	return nil
}

// RecordMediaAssetCleanupFailure persists bounded retry responsibility.
func (repo *MediaAssetRepository) RecordMediaAssetCleanupFailure(assetID string, attempts int, nextAttemptAt time.Time, updatedAt time.Time) error {
	if err := repo.db.Model(&domain.MediaAssetCleanupIntentModel{}).
		Where("asset_id = ?", strings.TrimSpace(assetID)).
		Updates(map[string]any{
			"attempts":        attempts,
			"next_attempt_at": nextAttemptAt,
			"updated_at":      updatedAt,
		}).Error; err != nil {
		return fmt.Errorf("recording media asset cleanup failure: %w", err)
	}
	return nil
}

// DeleteMediaAssetForCleanup atomically deletes an unreferenced asset and
// advances its intent, or cancels cleanup when a durable reference appeared.
func (repo *MediaAssetRepository) DeleteMediaAssetForCleanup(assetID string, updatedAt time.Time) (bool, bool, error) {
	assetID = strings.TrimSpace(assetID)
	deleted := false
	referenced := false
	err := repo.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`DELETE FROM assets
			WHERE id = ?
			  AND cleanup_pending = ?
			  AND NOT EXISTS (SELECT 1 FROM generation_task_assets WHERE asset_id = ?)
			  AND NOT EXISTS (SELECT 1 FROM generation_task_references WHERE asset_id = ?)
			  AND NOT EXISTS (SELECT 1 FROM project_selected_assets WHERE asset_id = ?)
			  AND NOT EXISTS (SELECT 1 FROM project_reference_assets WHERE asset_id = ?)`,
			assetID, true, assetID, assetID, assetID, assetID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			deleted = true
			return tx.Model(&domain.MediaAssetCleanupIntentModel{}).
				Where("asset_id = ?", assetID).
				Updates(map[string]any{"stage": "db_deleted", "updated_at": updatedAt}).Error
		}
		var count int64
		if err := tx.Model(&domain.AssetModel{}).Where("id = ?", assetID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return tx.Model(&domain.MediaAssetCleanupIntentModel{}).
				Where("asset_id = ?", assetID).
				Updates(map[string]any{"stage": "db_deleted", "updated_at": updatedAt}).Error
		}
		referenced = true
		if err := tx.Model(&domain.AssetModel{}).Where("id = ?", assetID).Update("cleanup_pending", false).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return false, false, fmt.Errorf("deleting media asset for cleanup: %w", err)
	}
	return deleted, referenced, nil
}

// CompleteMediaAssetCleanup removes a finished intent after tombstones are gone.
func (repo *MediaAssetRepository) CompleteMediaAssetCleanup(assetID string) error {
	return repo.DeleteMediaAssetCleanupIntent(assetID)
}

// CancelMediaAssetCleanup restores a claimed/referenced asset to durable state.
func (repo *MediaAssetRepository) CancelMediaAssetCleanup(assetID string) error {
	return repo.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&domain.AssetModel{}).
			Where("id = ?", strings.TrimSpace(assetID)).Update("cleanup_pending", false).Error; err != nil {
			return err
		}
		return tx.Delete(&domain.MediaAssetCleanupIntentModel{}, "asset_id = ?", strings.TrimSpace(assetID)).Error
	})
}

// UpdateMediaAssetFilename updates the display filename and timestamp.
func (repo *MediaAssetRepository) UpdateMediaAssetFilename(id string, filename string, updatedAt string) error {
	err := repo.db.Model(&domain.AssetModel{}).
		Where("id = ?", strings.TrimSpace(id)).
		Updates(map[string]any{
			"filename":   strings.TrimSpace(filename),
			"updated_at": domain.TimeFromString(updatedAt),
		}).Error
	if err != nil {
		return fmt.Errorf("updating media asset filename: %w", err)
	}
	return nil
}

// UpdateMediaAssetMetadata updates derived media metadata and poster fields.
func (repo *MediaAssetRepository) UpdateMediaAssetMetadata(id string, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	err := repo.db.Model(&domain.AssetModel{}).
		Where("id = ?", strings.TrimSpace(id)).
		Updates(updates).Error
	if err != nil {
		return fmt.Errorf("updating media asset metadata: %w", err)
	}
	return nil
}

// UpdateMediaAssetStorage updates the physical storage metadata for an asset.
func (repo *MediaAssetRepository) UpdateMediaAssetStorage(id string, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	err := repo.db.Model(&domain.AssetModel{}).
		Where("id = ?", strings.TrimSpace(id)).
		Updates(updates).Error
	if err != nil {
		return fmt.Errorf("updating media asset storage: %w", err)
	}
	return nil
}
