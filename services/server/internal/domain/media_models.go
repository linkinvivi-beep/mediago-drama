package domain

import "time"

// AssetModel is the single GORM model for physical asset metadata.
type AssetModel struct {
	ID              string    `gorm:"column:id;primaryKey"`
	ProjectID       *string   `gorm:"column:project_id;index:assets_project_id_idx"`
	Kind            string    `gorm:"column:kind;not null;index:assets_kind_idx"`
	ContentHash     string    `gorm:"column:content_hash;not null;default:'';index:assets_content_hash_idx"`
	Filename        string    `gorm:"column:filename;not null"`
	MIMEType        string    `gorm:"column:mime_type;not null"`
	SizeBytes       int64     `gorm:"column:size_bytes;not null"`
	RelPath         string    `gorm:"column:rel_path;not null;default:''"`
	URL             string    `gorm:"column:url;not null;default:''"`
	PosterRelPath   string    `gorm:"column:poster_rel_path;not null;default:''"`
	PosterURL       string    `gorm:"column:poster_url;not null;default:''"`
	Width           int       `gorm:"column:width;not null;default:0"`
	Height          int       `gorm:"column:height;not null;default:0"`
	DurationSeconds float64   `gorm:"column:duration_seconds;not null;default:0"`
	Source          string    `gorm:"column:source;not null;default:'';index:assets_source_idx"`
	SourceURL       string    `gorm:"column:source_url;not null;default:'';index:assets_source_url_idx"`
	MetadataStatus  string    `gorm:"column:metadata_status;not null;default:''"`
	StorageStatus   string    `gorm:"column:storage_status;not null;default:'ready';index:assets_storage_status_idx"`
	CleanupPending  bool      `gorm:"column:cleanup_pending;not null;default:false;index:assets_cleanup_pending_idx"`
	CreatedAt       time.Time `gorm:"column:created_at;not null;autoCreateTime:nano"`
	UpdatedAt       time.Time `gorm:"column:updated_at;not null;autoUpdateTime:nano"`

	Project *WorkspaceProjectModel `gorm:"foreignKey:ProjectID;references:ID;constraint:OnDelete:CASCADE"`
}

// MediaAssetCleanupIntentModel persists recoverable cleanup state without
// storing absolute or user-controlled filesystem paths.
type MediaAssetCleanupIntentModel struct {
	AssetID         string     `gorm:"column:asset_id;primaryKey"`
	ProjectID       string     `gorm:"column:project_id;not null;default:''"`
	AssetRelPath    string     `gorm:"column:asset_rel_path;not null;default:''"`
	AssetPosterPath string     `gorm:"column:asset_poster_rel_path;not null;default:''"`
	FileRoot        string     `gorm:"column:file_root;not null;default:''"`
	FileRootDev     int64      `gorm:"column:file_root_dev;not null;default:0"`
	FileRootIno     uint64     `gorm:"column:file_root_ino;not null;default:0"`
	FileRelPath     string     `gorm:"column:file_rel_path;not null;default:''"`
	FileTombstone   string     `gorm:"column:file_tombstone;not null;default:''"`
	FileDev         int64      `gorm:"column:file_dev;not null;default:0"`
	FileIno         uint64     `gorm:"column:file_ino;not null;default:0"`
	PosterRoot      string     `gorm:"column:poster_root;not null;default:''"`
	PosterRootDev   int64      `gorm:"column:poster_root_dev;not null;default:0"`
	PosterRootIno   uint64     `gorm:"column:poster_root_ino;not null;default:0"`
	PosterRelPath   string     `gorm:"column:poster_rel_path;not null;default:''"`
	PosterTombstone string     `gorm:"column:poster_tombstone;not null;default:''"`
	PosterDev       int64      `gorm:"column:poster_dev;not null;default:0"`
	PosterIno       uint64     `gorm:"column:poster_ino;not null;default:0"`
	TrashDev        int64      `gorm:"column:trash_dev;not null;default:0"`
	TrashIno        uint64     `gorm:"column:trash_ino;not null;default:0"`
	Stage           string     `gorm:"column:stage;not null;index:media_cleanup_intents_stage_idx"`
	Attempts        int        `gorm:"column:attempts;not null;default:0"`
	NextAttemptAt   *time.Time `gorm:"column:next_attempt_at;index:media_cleanup_intents_next_attempt_idx"`
	CreatedAt       time.Time  `gorm:"column:created_at;not null;autoCreateTime:nano"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;not null;autoUpdateTime:nano"`
}

// TableName returns the cleanup intent table name.
func (MediaAssetCleanupIntentModel) TableName() string {
	return "media_asset_cleanup_intents"
}

// TableName returns the backing table name.
func (AssetModel) TableName() string {
	return "assets"
}
