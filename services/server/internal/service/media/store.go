package media

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mediago-dev/mediago-drama/services/server/internal/domain"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/timestamp"
	"github.com/mediago-dev/mediago-drama/services/server/internal/repository"
	"github.com/mediago-dev/mediago-drama/services/server/internal/service/shared"
)

const (
	mediaAssetListLimit     = 200
	MaxMediaAssetUploadSize = 200 << 20
	MediaKindImage          = shared.AssetKindImage
	MediaKindVideo          = shared.AssetKindVideo
	MediaKindAudio          = shared.AssetKindAudio
	MediaKindText           = shared.AssetKindText
	MetadataStatusReady     = "ready"
	MetadataStatusFailed    = "failed"
	StorageStatusReady      = "ready"
	StorageStatusMissing    = "missing"
	MediaSourceUpload       = "upload"
	MediaSourceGeneration   = "generation"
	MediaSourceToolbox      = "toolbox"
	MediaSourcePreview      = "preview"
)

var mediaAssetHTTPClient = &http.Client{Timeout: 2 * time.Minute}

type MediaAssets struct {
	mu                       sync.RWMutex
	generationClaimMu        sync.Mutex
	generationClaims         map[string]mediaAssetClaimState
	repo                     *repository.MediaAssetRepository
	workspaceRepo            *repository.WorkspaceRepository
	dir                      string
	workspaceRoot            string
	ffmpegPath               string
	ffmpegBinDir             string
	metadataBackfillAttempts map[string]struct{}
	renameFile               func(string, string) error
	removeFile               func(string) error
	deleteIfUnreferenced     func(string) (bool, error)
	initErr                  error
}

type MediaAsset struct {
	ID                string  `json:"id"`
	Kind              string  `json:"kind"`
	Filename          string  `json:"filename"`
	MIMEType          string  `json:"mimeType"`
	SizeBytes         int64   `json:"sizeBytes"`
	URL               string  `json:"url"`
	SourceURL         string  `json:"sourceUrl,omitempty"`
	ContentHash       string  `json:"-"`
	ProjectID         string  `json:"projectId,omitempty"`
	Source            string  `json:"source,omitempty"`
	ConversationID    string  `json:"conversationId,omitempty"`
	SectionID         string  `json:"sectionId,omitempty"`
	RelativePath      string  `json:"relativePath,omitempty"`
	DownloadPath      string  `json:"downloadPath,omitempty"`
	DurationSeconds   float64 `json:"durationSeconds,omitempty"`
	Width             int     `json:"width,omitempty"`
	Height            int     `json:"height,omitempty"`
	PosterURL         string  `json:"posterUrl,omitempty"`
	MetadataStatus    string  `json:"metadataStatus,omitempty"`
	MetadataError     string  `json:"metadataError,omitempty"`
	MetadataUpdatedAt string  `json:"metadataUpdatedAt,omitempty"`
	StorageStatus     string  `json:"storageStatus,omitempty"`
	StorageError      string  `json:"storageError,omitempty"`
	CreatedAt         string  `json:"createdAt"`
	UpdatedAt         string  `json:"updatedAt"`
	FilePath          string  `json:"-"`
	PosterPath        string  `json:"-"`
}

type MediaAssetsResponse struct {
	Assets []MediaAsset `json:"assets"`
}

type MediaAssetUpdateRequest struct {
	Filename string `json:"filename"`
}

// MediaAssetSaveOptions describes where a new media asset should live.
type MediaAssetSaveOptions struct {
	ProjectID      string
	Source         string
	ConversationID string
	SectionID      string
	Filename       string
}

// MediaAssetClaim keeps a cached generated asset staged until its task update
// either commits the relation or loses active ownership.
type MediaAssetClaim struct {
	AssetID string
	Created bool
}

type mediaAssetClaimState struct {
	count  int
	staged bool
}

type mediaAssetModel = domain.AssetModel

func NewMediaAssets(dbPath string, mediaDir string) *MediaAssets {
	if dbPath == "" {
		dbPath = shared.WorkspacePathsFor("").DatabasePath()
	}
	if mediaDir == "" {
		mediaDir = defaultMediaDir()
	}

	store := &MediaAssets{dir: mediaDir, renameFile: os.Rename, removeFile: os.Remove}
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		store.initErr = fmt.Errorf("creating media asset directory: %w", err)
		return store
	}

	repo, err := repository.NewMediaAssetRepository(dbPath)
	if err != nil {
		store.initErr = err
		return store
	}

	store.repo = repo
	store.deleteIfUnreferenced = repo.DeleteMediaAssetIfUnreferenced
	store.reconcilePendingMediaAssetCleanup()
	return store
}

// NewMediaAssetsFromRepository returns a media asset service backed by an
// already constructed repository.
func NewMediaAssetsFromRepository(repo *repository.MediaAssetRepository, mediaDir string, workspaceRoot string, workspaceRepo *repository.WorkspaceRepository, initErr error) *MediaAssets {
	if mediaDir == "" {
		if strings.TrimSpace(workspaceRoot) != "" {
			mediaDir = shared.WorkspacePathsFor(workspaceRoot).LibraryAssetsDir()
		} else {
			mediaDir = defaultMediaDir()
		}
	}

	store := &MediaAssets{
		repo:          repo,
		workspaceRepo: workspaceRepo,
		dir:           mediaDir,
		workspaceRoot: strings.TrimSpace(workspaceRoot),
		renameFile:    os.Rename,
		removeFile:    os.Remove,
		initErr:       initErr,
	}
	if store.initErr != nil {
		return store
	}
	if store.repo == nil {
		store.initErr = errors.New("media asset repository is nil")
		return store
	}
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		store.initErr = fmt.Errorf("creating media asset directory: %w", err)
		return store
	}
	store.deleteIfUnreferenced = store.repo.DeleteMediaAssetIfUnreferenced
	store.reconcilePendingMediaAssetCleanup()
	return store
}

// SetMediaToolPaths configures ffmpeg/ffprobe lookup paths for metadata extraction.
func (store *MediaAssets) SetMediaToolPaths(ffmpegPath string, ffmpegBinDir string) {
	if store == nil {
		return
	}
	store.ffmpegPath = strings.TrimSpace(ffmpegPath)
	store.ffmpegBinDir = strings.TrimSpace(ffmpegBinDir)
}

// WorkspaceRoot returns the global workspace root used for project-scoped media.
func (store *MediaAssets) WorkspaceRoot() string {
	if store == nil {
		return ""
	}
	return strings.TrimSpace(store.workspaceRoot)
}

// ServeFilePath returns a sanitized on-disk path for serving a media asset.
func (store *MediaAssets) ServeFilePath(asset MediaAsset) (string, error) {
	if store == nil {
		return "", errors.New("media asset store is nil")
	}
	filePath := strings.TrimSpace(asset.FilePath)
	if filePath == "" {
		return "", fmt.Errorf("media asset %s has no file path", asset.ID)
	}
	absolutePath, err := filepath.Abs(filePath)
	if err != nil {
		return "", fmt.Errorf("resolving media asset path: %w", err)
	}
	for _, root := range store.allowedRootsForAsset(asset) {
		ok, err := pathWithinRoot(absolutePath, root)
		if err != nil {
			return "", err
		}
		if ok {
			return absolutePath, nil
		}
	}
	return "", fmt.Errorf("media asset %s path is outside allowed roots", asset.ID)
}

// ServePosterFilePath returns a sanitized on-disk path for serving a media asset poster.
func (store *MediaAssets) ServePosterFilePath(asset MediaAsset) (string, error) {
	if store == nil {
		return "", errors.New("media asset store is nil")
	}
	posterPath := strings.TrimSpace(asset.PosterPath)
	if posterPath == "" {
		return "", fmt.Errorf("media asset %s has no poster path", asset.ID)
	}
	absolutePath, err := filepath.Abs(posterPath)
	if err != nil {
		return "", fmt.Errorf("resolving media asset poster path: %w", err)
	}
	for _, root := range store.allowedRootsForAsset(asset) {
		ok, err := pathWithinRoot(absolutePath, root)
		if err != nil {
			return "", err
		}
		if ok {
			return absolutePath, nil
		}
	}
	return "", fmt.Errorf("media asset %s poster path is outside allowed roots", asset.ID)
}

func pathWithinRoot(absolutePath string, root string) (bool, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return false, nil
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return false, fmt.Errorf("resolving media asset root: %w", err)
	}
	relative, err := filepath.Rel(absoluteRoot, absolutePath)
	if err != nil {
		return false, fmt.Errorf("checking media asset path containment: %w", err)
	}
	return relative == "." ||
		(!filepath.IsAbs(relative) &&
			relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func (store *MediaAssets) List(projectID string) ([]MediaAsset, error) {
	if store.initErr != nil {
		return nil, store.initErr
	}

	store.mu.RLock()
	models, err := store.repo.ListMediaAssets(mediaAssetListLimit, projectID)
	store.mu.RUnlock()
	if err != nil {
		return nil, err
	}

	assets := store.mediaAssetRecordsFromModels(models)
	return store.backfillListedVideoMetadata(assets)
}

func (store *MediaAssets) Get(id string) (MediaAsset, bool, error) {
	if store.initErr != nil {
		return MediaAsset{}, false, store.initErr
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	model, err := store.repo.GetMediaAsset(id)
	if repository.IsRecordNotFound(err) {
		return MediaAsset{}, false, nil
	}
	if err != nil {
		return MediaAsset{}, false, err
	}

	return store.mediaAssetRecordFromModel(model), true, nil
}

func (store *MediaAssets) FindBySourceURL(sourceURL string) (MediaAsset, bool, error) {
	if store.initErr != nil {
		return MediaAsset{}, false, store.initErr
	}
	if strings.TrimSpace(sourceURL) == "" {
		return MediaAsset{}, false, nil
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	model, err := store.repo.FindMediaAssetBySourceURL(sourceURL)
	if repository.IsRecordNotFound(err) {
		return MediaAsset{}, false, nil
	}
	if err != nil {
		return MediaAsset{}, false, err
	}

	return store.mediaAssetRecordFromModel(model), true, nil
}

func (store *MediaAssets) FindBySourceURLAndScope(sourceURL string, options MediaAssetSaveOptions) (MediaAsset, bool, error) {
	if store.initErr != nil {
		return MediaAsset{}, false, store.initErr
	}
	if strings.TrimSpace(sourceURL) == "" {
		return MediaAsset{}, false, nil
	}
	options = normalizeMediaAssetSaveOptions(options)

	store.mu.RLock()
	defer store.mu.RUnlock()

	model, err := store.repo.FindMediaAssetBySourceURLAndScope(
		sourceURL,
		options.ProjectID,
		options.Source,
		options.ConversationID,
	)
	if repository.IsRecordNotFound(err) {
		return MediaAsset{}, false, nil
	}
	if err != nil {
		return MediaAsset{}, false, err
	}

	return store.mediaAssetRecordFromModel(model), true, nil
}

func (store *MediaAssets) FindByContentHashAndScope(contentHash string, kind string, options MediaAssetSaveOptions) (MediaAsset, bool, error) {
	if store.initErr != nil {
		return MediaAsset{}, false, store.initErr
	}
	contentHash = strings.TrimSpace(contentHash)
	if contentHash == "" {
		return MediaAsset{}, false, nil
	}
	options = normalizeMediaAssetSaveOptions(options)

	store.mu.RLock()
	defer store.mu.RUnlock()

	model, err := store.repo.FindMediaAssetByContentHashAndScope(
		contentHash,
		kind,
		options.ProjectID,
		options.Source,
		options.ConversationID,
	)
	if repository.IsRecordNotFound(err) {
		return MediaAsset{}, false, nil
	}
	if err != nil {
		return MediaAsset{}, false, err
	}

	asset := store.mediaAssetRecordFromModel(model)
	if _, err := store.ServeFilePath(asset); err != nil {
		return MediaAsset{}, false, nil
	}
	if _, err := os.Stat(asset.FilePath); err != nil {
		return MediaAsset{}, false, nil
	}
	return asset, true, nil
}

func (store *MediaAssets) SaveMultipartFile(header *multipart.FileHeader, projectID string) (MediaAsset, error) {
	if store.initErr != nil {
		return MediaAsset{}, store.initErr
	}

	file, err := header.Open()
	if err != nil {
		return MediaAsset{}, err
	}
	defer file.Close()

	data, err := shared.ReadLimited(file, MaxMediaAssetUploadSize)
	if err != nil {
		return MediaAsset{}, err
	}

	return store.saveBytesForProject(data, header.Filename, header.Header.Get("Content-Type"), "", projectID, MediaSourceUpload)
}

func (store *MediaAssets) SaveReader(ctx context.Context, reader io.Reader, filename string, contentType string, sourceURL string) (MediaAsset, error) {
	_ = ctx
	data, err := shared.ReadLimited(reader, MaxMediaAssetUploadSize)
	if err != nil {
		return MediaAsset{}, err
	}

	return store.saveBytesForProject(data, filename, contentType, sourceURL, "", MediaSourceUpload)
}

func (store *MediaAssets) SaveBase64(kind string, mimeType string, value string, sourceURL string, projectID string) (MediaAsset, error) {
	return store.SaveBase64WithOptions(kind, mimeType, value, sourceURL, MediaAssetSaveOptions{
		ProjectID: projectID,
		Source:    MediaSourceGeneration,
	})
}

// SaveBase64WithOptions stores a base64 media asset using explicit placement metadata.
func (store *MediaAssets) SaveBase64WithOptions(kind string, mimeType string, value string, sourceURL string, options MediaAssetSaveOptions) (MediaAsset, error) {
	asset, _, err := store.SaveBase64WithOptionsTracked(kind, mimeType, value, sourceURL, options)
	return asset, err
}

// SaveBase64WithOptionsTracked stores a base64 asset and reports whether this call created it.
func (store *MediaAssets) SaveBase64WithOptionsTracked(kind string, mimeType string, value string, sourceURL string, options MediaAssetSaveOptions) (MediaAsset, bool, error) {
	return store.saveBase64WithOptionsTracked(kind, mimeType, value, sourceURL, options)
}

// SaveBase64WithOptionsClaimed stores a generated asset under a temporary
// in-process claim that the generation task writer must commit or compensate.
func (store *MediaAssets) SaveBase64WithOptionsClaimed(kind string, mimeType string, value string, sourceURL string, options MediaAssetSaveOptions) (MediaAsset, MediaAssetClaim, error) {
	return store.claimSavedGenerationAsset(func() (MediaAsset, bool, error) {
		return store.saveBase64WithOptionsTracked(kind, mimeType, value, sourceURL, options)
	})
}

// SaveTextWithOptions stores a text asset using explicit placement metadata.
func (store *MediaAssets) SaveTextWithOptions(content string, filename string, sourceURL string, options MediaAssetSaveOptions) (MediaAsset, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = defaultAssetFilename(MediaKindText, "text/plain")
	}
	return store.saveBytesWithKind([]byte(content), MediaKindText, filename, "text/plain", sourceURL, options)
}

// SaveBase64ForStudioSession is a legacy wrapper for toolbox conversation assets.
func (store *MediaAssets) SaveBase64ForStudioSession(kind string, mimeType string, value string, sourceURL string, sessionID string) (MediaAsset, error) {
	return store.SaveBase64WithOptions(kind, mimeType, value, sourceURL, MediaAssetSaveOptions{
		Source:         MediaSourceToolbox,
		ConversationID: sessionID,
	})
}

// SaveBase64ForStudioDir is a legacy wrapper for toolbox conversation assets.
func (store *MediaAssets) SaveBase64ForStudioDir(kind string, mimeType string, value string, sourceURL string, studioDir string) (MediaAsset, error) {
	return store.SaveBase64WithOptions(kind, mimeType, value, sourceURL, MediaAssetSaveOptions{
		Source:         MediaSourceToolbox,
		ConversationID: filepath.Base(strings.TrimSpace(studioDir)),
	})
}

func (store *MediaAssets) saveBase64WithOptions(kind string, mimeType string, value string, sourceURL string, options MediaAssetSaveOptions) (MediaAsset, error) {
	asset, _, err := store.saveBase64WithOptionsTracked(kind, mimeType, value, sourceURL, options)
	return asset, err
}

func (store *MediaAssets) saveBase64WithOptionsTracked(kind string, mimeType string, value string, sourceURL string, options MediaAssetSaveOptions) (MediaAsset, bool, error) {
	if store.initErr != nil {
		return MediaAsset{}, false, store.initErr
	}
	encoded := stripDataURI(value)
	if encoded == "" {
		return MediaAsset{}, false, fmt.Errorf("base64 asset is empty")
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil {
		return MediaAsset{}, false, fmt.Errorf("decoding base64 asset: %w", err)
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	if kind == "" {
		kind = shared.KindFromMIMEType(mimeType)
	}

	filename := strings.TrimSpace(options.Filename)
	if filename == "" {
		filename = defaultAssetFilename(kind, mimeType)
	}

	return store.saveBytesWithKindTracked(data, kind, filename, mimeType, sourceURL, options)
}

func (store *MediaAssets) SaveRemoteAsset(ctx context.Context, kind string, remoteURL string, projectID string) (MediaAsset, error) {
	return store.SaveRemoteAssetWithOptions(ctx, kind, remoteURL, MediaAssetSaveOptions{
		ProjectID: projectID,
		Source:    MediaSourceGeneration,
	})
}

// SaveRemoteAssetWithOptions downloads and stores a remote media asset using explicit placement metadata.
func (store *MediaAssets) SaveRemoteAssetWithOptions(ctx context.Context, kind string, remoteURL string, options MediaAssetSaveOptions) (MediaAsset, error) {
	asset, _, err := store.SaveRemoteAssetWithOptionsTracked(ctx, kind, remoteURL, options)
	return asset, err
}

// SaveRemoteAssetWithOptionsTracked downloads an asset and reports whether this call created it.
func (store *MediaAssets) SaveRemoteAssetWithOptionsTracked(ctx context.Context, kind string, remoteURL string, options MediaAssetSaveOptions) (MediaAsset, bool, error) {
	return store.saveRemoteAssetWithOptionsTracked(ctx, kind, remoteURL, options)
}

// SaveRemoteAssetWithOptionsClaimed stores a generated remote asset under a
// temporary claim that the generation task writer must commit or compensate.
func (store *MediaAssets) SaveRemoteAssetWithOptionsClaimed(ctx context.Context, kind string, remoteURL string, options MediaAssetSaveOptions) (MediaAsset, MediaAssetClaim, error) {
	return store.claimSavedGenerationAsset(func() (MediaAsset, bool, error) {
		return store.saveRemoteAssetWithOptionsTracked(ctx, kind, remoteURL, options)
	})
}

func (store *MediaAssets) claimSavedGenerationAsset(save func() (MediaAsset, bool, error)) (MediaAsset, MediaAssetClaim, error) {
	for attempt := 0; attempt < 2; attempt++ {
		asset, created, err := save()
		if err != nil || asset.ID == "" {
			return asset, MediaAssetClaim{}, err
		}
		store.generationClaimMu.Lock()
		store.mu.RLock()
		_, existsErr := store.repo.GetMediaAsset(asset.ID)
		store.mu.RUnlock()
		if existsErr == nil {
			if store.generationClaims == nil {
				store.generationClaims = map[string]mediaAssetClaimState{}
			}
			state := store.generationClaims[asset.ID]
			state.count++
			state.staged = state.staged || created
			store.generationClaims[asset.ID] = state
			store.generationClaimMu.Unlock()
			return asset, MediaAssetClaim{AssetID: asset.ID, Created: created}, nil
		}
		store.generationClaimMu.Unlock()
		if !repository.IsRecordNotFound(existsErr) {
			return MediaAsset{}, MediaAssetClaim{}, existsErr
		}
	}
	return MediaAsset{}, MediaAssetClaim{}, fmt.Errorf("claiming generated media asset: asset disappeared before it could be staged")
}

// CommitGenerationAssetClaims makes task-associated generated assets durable.
func (store *MediaAssets) CommitGenerationAssetClaims(claims []MediaAssetClaim) {
	store.finishGenerationAssetClaims(claims, true)
}

// CompensateGenerationAssetClaims releases failed task writes and removes only
// staged assets that remain unreferenced after every concurrent claim ends.
func (store *MediaAssets) CompensateGenerationAssetClaims(claims []MediaAssetClaim) []error {
	return store.finishGenerationAssetClaims(claims, false)
}

func (store *MediaAssets) finishGenerationAssetClaims(claims []MediaAssetClaim, committed bool) []error {
	if store == nil || len(claims) == 0 {
		return nil
	}
	store.generationClaimMu.Lock()
	defer store.generationClaimMu.Unlock()
	errorsFound := []error{}
	for _, claim := range claims {
		assetID := strings.TrimSpace(claim.AssetID)
		state, ok := store.generationClaims[assetID]
		if !ok {
			continue
		}
		if committed {
			state.staged = false
		}
		if state.count > 0 {
			state.count--
		}
		if state.count > 0 {
			store.generationClaims[assetID] = state
			continue
		}
		delete(store.generationClaims, assetID)
		if !committed && state.staged {
			if _, err := store.DeleteIfUnreferenced(assetID); err != nil {
				errorsFound = append(errorsFound, err)
			}
		}
	}
	return errorsFound
}

// SaveRemoteAssetForStudioSession is a legacy wrapper for toolbox conversation assets.
func (store *MediaAssets) SaveRemoteAssetForStudioSession(ctx context.Context, kind string, remoteURL string, sessionID string) (MediaAsset, error) {
	return store.SaveRemoteAssetWithOptions(ctx, kind, remoteURL, MediaAssetSaveOptions{
		Source:         MediaSourceToolbox,
		ConversationID: sessionID,
	})
}

// SaveRemoteAssetForStudioDir is a legacy wrapper for toolbox conversation assets.
func (store *MediaAssets) SaveRemoteAssetForStudioDir(ctx context.Context, kind string, remoteURL string, studioDir string) (MediaAsset, error) {
	return store.SaveRemoteAssetWithOptions(ctx, kind, remoteURL, MediaAssetSaveOptions{
		Source:         MediaSourceToolbox,
		ConversationID: filepath.Base(strings.TrimSpace(studioDir)),
	})
}

func (store *MediaAssets) saveRemoteAssetWithOptions(ctx context.Context, kind string, remoteURL string, options MediaAssetSaveOptions) (MediaAsset, error) {
	asset, _, err := store.saveRemoteAssetWithOptionsTracked(ctx, kind, remoteURL, options)
	return asset, err
}

func (store *MediaAssets) saveRemoteAssetWithOptionsTracked(ctx context.Context, kind string, remoteURL string, options MediaAssetSaveOptions) (MediaAsset, bool, error) {
	if store.initErr != nil {
		return MediaAsset{}, false, store.initErr
	}
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return MediaAsset{}, false, fmt.Errorf("remote asset url is empty")
	}
	options = normalizeMediaAssetSaveOptions(options)
	if existing, ok, err := store.FindBySourceURLAndScope(remoteURL, options); err != nil {
		return MediaAsset{}, false, err
	} else if ok {
		return existing, false, nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, remoteURL, nil)
	if err != nil {
		return MediaAsset{}, false, err
	}
	response, err := mediaAssetHTTPClient.Do(request)
	if err != nil {
		return MediaAsset{}, false, err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return MediaAsset{}, false, fmt.Errorf("downloading asset failed with status %d", response.StatusCode)
	}
	if response.ContentLength > MaxMediaAssetUploadSize {
		return MediaAsset{}, false, fmt.Errorf("asset is larger than %d bytes", MaxMediaAssetUploadSize)
	}

	data, err := shared.ReadLimited(response.Body, MaxMediaAssetUploadSize)
	if err != nil {
		return MediaAsset{}, false, err
	}

	mimeType := response.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	if kind == "" {
		kind = shared.KindFromMIMEType(mimeType)
	}

	filename := filenameFromURL(remoteURL)
	if strings.TrimSpace(options.Filename) != "" {
		filename = strings.TrimSpace(options.Filename)
	}
	if filename == "" {
		filename = defaultAssetFilename(kind, mimeType)
	}

	return store.saveBytesWithKindTracked(data, kind, filename, mimeType, remoteURL, options)
}

// SaveLinkedAssetWithOptions stores metadata for an already-served asset URL.
// The file is not copied locally; callers must pass a URL that remains playable
// by clients, such as an application-owned preview endpoint.
func (store *MediaAssets) SaveLinkedAssetWithOptions(kind string, assetURL string, filename string, mimeType string, options MediaAssetSaveOptions) (MediaAsset, error) {
	if store.initErr != nil {
		return MediaAsset{}, store.initErr
	}
	assetURL = strings.TrimSpace(assetURL)
	if assetURL == "" {
		return MediaAsset{}, fmt.Errorf("linked asset url is empty")
	}
	options = normalizeMediaAssetSaveOptions(options)
	if existing, ok, err := store.FindBySourceURLAndScope(assetURL, options); err != nil {
		return MediaAsset{}, err
	} else if ok {
		return existing, nil
	}

	kind = strings.ToLower(strings.TrimSpace(kind))
	mimeType = shared.NormalizeMIMEType(mimeType)
	if mimeType == "" {
		mimeType = defaultAssetMIMEType(kind)
	}
	if !isSupportedMediaAssetKind(kind) {
		return MediaAsset{}, unsupportedMediaAssetKindError()
	}

	id, err := shared.RandomID("asset")
	if err != nil {
		return MediaAsset{}, err
	}
	extension := shared.ExtensionForMIMEType(mimeType)
	filename = shared.SafeFilename(strings.TrimSpace(filename))
	if filename == "" {
		filename = id + extension
	}
	if filepath.Ext(filename) == "" {
		filename += extension
	}
	now := timestamp.NowRFC3339Nano()
	asset := MediaAsset{
		ID:             id,
		Kind:           kind,
		Filename:       filename,
		MIMEType:       mimeType,
		URL:            assetURL,
		SourceURL:      assetURL,
		ProjectID:      options.ProjectID,
		Source:         options.Source,
		ConversationID: options.ConversationID,
		SectionID:      options.SectionID,
		StorageStatus:  StorageStatusReady,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.repo.CreateMediaAsset(mediaAssetModel{
		ID:            asset.ID,
		Kind:          asset.Kind,
		Filename:      asset.Filename,
		MIMEType:      asset.MIMEType,
		URL:           asset.URL,
		SourceURL:     asset.SourceURL,
		ProjectID:     domain.StringPtr(asset.ProjectID),
		Source:        asset.Source,
		StorageStatus: asset.StorageStatus,
		CreatedAt:     domain.TimeFromString(asset.CreatedAt),
		UpdatedAt:     domain.TimeFromString(asset.UpdatedAt),
	}); err != nil {
		return MediaAsset{}, err
	}

	return asset, nil
}

func defaultAssetMIMEType(kind string) string {
	if kind == MediaKindVideo {
		return "video/mp4"
	}
	if kind == MediaKindAudio {
		return "audio/mpeg"
	}
	if kind == MediaKindText {
		return "text/plain"
	}
	return "image/png"
}

func (store *MediaAssets) Base64Value(asset MediaAsset) (string, error) {
	if store.initErr != nil {
		return "", store.initErr
	}
	data, err := os.ReadFile(asset.FilePath)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(data), nil
}

func (store *MediaAssets) DataURIValue(asset MediaAsset) (string, error) {
	encoded, err := store.Base64Value(asset)
	if err != nil {
		return "", err
	}

	return "data:" + asset.MIMEType + ";base64," + encoded, nil
}

func (store *MediaAssets) saveBytes(data []byte, filename string, contentType string, sourceURL string) (MediaAsset, error) {
	return store.saveBytesForProject(data, filename, contentType, sourceURL, "", MediaSourceUpload)
}

func (store *MediaAssets) saveBytesForProject(data []byte, filename string, contentType string, sourceURL string, projectID string, source string) (MediaAsset, error) {
	mimeType := strings.TrimSpace(strings.Split(contentType, ";")[0])
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = http.DetectContentType(data)
	}
	kind := shared.KindFromMIMEType(mimeType)
	if !isSupportedMediaAssetKind(kind) {
		return MediaAsset{}, unsupportedMediaAssetKindError()
	}

	return store.saveBytesWithKind(data, kind, filename, mimeType, sourceURL, MediaAssetSaveOptions{
		ProjectID: projectID,
		Source:    source,
	})
}

func (store *MediaAssets) saveBytesWithKind(data []byte, kind string, filename string, mimeType string, sourceURL string, options MediaAssetSaveOptions) (MediaAsset, error) {
	asset, _, err := store.saveBytesWithKindTracked(data, kind, filename, mimeType, sourceURL, options)
	return asset, err
}

func (store *MediaAssets) saveBytesWithKindTracked(data []byte, kind string, filename string, mimeType string, sourceURL string, options MediaAssetSaveOptions) (MediaAsset, bool, error) {
	if store.initErr != nil {
		return MediaAsset{}, false, store.initErr
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	mimeType = shared.NormalizeMIMEType(mimeType)
	if mimeType == "" {
		mimeType = defaultAssetMIMEType(kind)
	}
	if len(data) == 0 {
		return MediaAsset{}, false, fmt.Errorf("asset file is empty")
	}
	if len(data) > MaxMediaAssetUploadSize {
		return MediaAsset{}, false, fmt.Errorf("asset is larger than %d bytes", MaxMediaAssetUploadSize)
	}
	if !isSupportedMediaAssetKind(kind) {
		return MediaAsset{}, false, unsupportedMediaAssetKindError()
	}

	nowTime := time.Now()
	now := timestamp.FormatRFC3339Nano(nowTime)
	options = normalizeMediaAssetSaveOptions(options)
	contentHash := mediaAssetContentHash(data)
	if shouldReuseMediaAssetContent(options.Source) {
		if existing, ok, err := store.FindByContentHashAndScope(contentHash, kind, options); err != nil {
			return MediaAsset{}, false, err
		} else if ok {
			return existing, false, nil
		}
	}

	id, err := shared.RandomID("asset")
	if err != nil {
		return MediaAsset{}, false, err
	}

	filename = shared.SafeFilename(filename)
	ext := filepath.Ext(filename)
	if ext == "" {
		ext = shared.ExtensionForMIMEType(mimeType)
	}
	if filename == "" {
		filename = id + ext
	}
	if filepath.Ext(filename) == "" {
		filename += ext
	}

	target, err := store.targetLocation(options, mediaAssetDateDirForTime(nowTime))
	if err != nil {
		return MediaAsset{}, false, err
	}
	if err := os.MkdirAll(target.Directory, 0o755); err != nil {
		return MediaAsset{}, false, fmt.Errorf("creating media asset directory: %w", err)
	}

	filePath := filepath.Join(target.Directory, id+filepath.Ext(filename))
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		return MediaAsset{}, false, err
	}
	relativePath := joinAssetRelativePath(target.RelativeDir, filepath.Base(filePath))

	asset := MediaAsset{
		ID:             id,
		Kind:           kind,
		Filename:       filename,
		MIMEType:       mimeType,
		SizeBytes:      int64(len(data)),
		URL:            "/api/v1/media-assets/" + url.PathEscape(id) + "/content",
		SourceURL:      strings.TrimSpace(sourceURL),
		ContentHash:    contentHash,
		ProjectID:      options.ProjectID,
		Source:         options.Source,
		ConversationID: options.ConversationID,
		SectionID:      options.SectionID,
		RelativePath:   relativePath,
		DownloadPath:   filePath,
		MetadataStatus: "",
		StorageStatus:  StorageStatusReady,
		CreatedAt:      now,
		UpdatedAt:      now,
		FilePath:       filePath,
	}
	if kind == MediaKindVideo {
		asset = store.enrichVideoMetadata(asset, now)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.repo.CreateMediaAsset(mediaAssetModel{
		ID:              asset.ID,
		Kind:            asset.Kind,
		Filename:        asset.Filename,
		MIMEType:        asset.MIMEType,
		SizeBytes:       asset.SizeBytes,
		RelPath:         asset.RelativePath,
		URL:             asset.URL,
		SourceURL:       asset.SourceURL,
		ContentHash:     asset.ContentHash,
		ProjectID:       domain.StringPtr(asset.ProjectID),
		Source:          asset.Source,
		DurationSeconds: asset.DurationSeconds,
		Width:           asset.Width,
		Height:          asset.Height,
		PosterRelPath:   store.mediaAssetDBRelPath(asset.ProjectID, asset.PosterPath),
		PosterURL:       asset.PosterURL,
		MetadataStatus:  asset.MetadataStatus,
		StorageStatus:   asset.StorageStatus,
		CreatedAt:       domain.TimeFromString(asset.CreatedAt),
		UpdatedAt:       domain.TimeFromString(asset.UpdatedAt),
	}); err != nil {
		_ = os.Remove(filePath)
		if asset.PosterPath != "" {
			_ = os.Remove(asset.PosterPath)
		}
		return MediaAsset{}, false, err
	}

	return asset, true, nil
}

func isSupportedMediaAssetKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case MediaKindImage, MediaKindVideo, MediaKindAudio, MediaKindText:
		return true
	default:
		return false
	}
}

func shouldReuseMediaAssetContent(source string) bool {
	switch normalizeMediaAssetSource(source) {
	case MediaSourceGeneration, MediaSourceToolbox, MediaSourcePreview:
		return true
	default:
		return false
	}
}

func mediaAssetContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func unsupportedMediaAssetKindError() error {
	return fmt.Errorf("only image, video, audio, and text assets are supported")
}

func (store *MediaAssets) Delete(id string) (bool, error) {
	if store.initErr != nil {
		return false, store.initErr
	}

	asset, ok, err := store.Get(id)
	if err != nil || !ok {
		return ok, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	deleted, err := store.repo.DeleteMediaAsset(id)
	if err != nil {
		return false, err
	}
	if deleted {
		_ = os.Remove(asset.FilePath)
		if asset.PosterPath != "" {
			_ = os.Remove(asset.PosterPath)
		}
	}

	return deleted, nil
}

type stagedMediaAssetFile struct {
	Original string `json:"original"`
	Staged   string `json:"staged"`
}

type mediaAssetCleanupJournal struct {
	AssetID string                 `json:"assetId"`
	Files   []stagedMediaAssetFile `json:"files"`
}

// DeleteIfUnreferenced removes an uncommitted generated asset without
// cascading through an asset relation established by another task.
func (store *MediaAssets) DeleteIfUnreferenced(id string) (bool, error) {
	if store.initErr != nil {
		return false, store.initErr
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if journal, found, err := store.readMediaAssetCleanupJournalLocked(id); err != nil {
		return false, err
	} else if found {
		return store.reconcileMediaAssetCleanupJournalLocked(journal)
	}
	model, err := store.repo.GetMediaAsset(id)
	if repository.IsRecordNotFound(err) {
		return false, store.removeStagedMediaAssetFilesLocked(id)
	}
	if err != nil {
		return false, err
	}
	asset := store.mediaAssetRecordFromModel(model)
	journal, err := store.writeMediaAssetCleanupJournalLocked(asset)
	if err != nil {
		return false, err
	}
	return store.reconcileMediaAssetCleanupJournalLocked(journal)
}

func (store *MediaAssets) mediaAssetCleanupJournalForAsset(asset MediaAsset) mediaAssetCleanupJournal {
	trashDir := filepath.Join(store.dir, ".trash")
	files := []struct {
		path string
		role string
	}{{path: asset.FilePath, role: "file"}, {path: asset.PosterPath, role: "poster"}}
	staged := make([]stagedMediaAssetFile, 0, len(files))
	for _, file := range files {
		if strings.TrimSpace(file.path) == "" {
			continue
		}
		ext := filepath.Ext(file.path)
		target := filepath.Join(trashDir, asset.ID+"-"+file.role+ext)
		staged = append(staged, stagedMediaAssetFile{Original: file.path, Staged: target})
	}
	return mediaAssetCleanupJournal{AssetID: asset.ID, Files: staged}
}

func (store *MediaAssets) writeMediaAssetCleanupJournalLocked(asset MediaAsset) (mediaAssetCleanupJournal, error) {
	journal := store.mediaAssetCleanupJournalForAsset(asset)
	trashDir := filepath.Join(store.dir, ".trash")
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		return mediaAssetCleanupJournal{}, fmt.Errorf("creating media asset cleanup directory")
	}
	data, err := json.Marshal(journal)
	if err != nil {
		return mediaAssetCleanupJournal{}, fmt.Errorf("encoding media asset cleanup journal: %w", err)
	}
	journalPath := store.mediaAssetCleanupJournalPath(asset.ID)
	temporaryPath := journalPath + ".tmp"
	if err := os.WriteFile(temporaryPath, data, 0o600); err != nil {
		return mediaAssetCleanupJournal{}, fmt.Errorf("writing media asset cleanup journal %q", filepath.Base(temporaryPath))
	}
	if err := os.Rename(temporaryPath, journalPath); err != nil {
		_ = os.Remove(temporaryPath)
		return mediaAssetCleanupJournal{}, fmt.Errorf("committing media asset cleanup journal %q", filepath.Base(journalPath))
	}
	return journal, nil
}

func (store *MediaAssets) readMediaAssetCleanupJournalLocked(id string) (mediaAssetCleanupJournal, bool, error) {
	journalPath := store.mediaAssetCleanupJournalPath(id)
	data, err := os.ReadFile(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return mediaAssetCleanupJournal{}, false, nil
	}
	if err != nil {
		return mediaAssetCleanupJournal{}, false, fmt.Errorf("reading media asset cleanup journal %q", filepath.Base(journalPath))
	}
	journal := mediaAssetCleanupJournal{}
	if err := json.Unmarshal(data, &journal); err != nil || strings.TrimSpace(journal.AssetID) != strings.TrimSpace(id) {
		return mediaAssetCleanupJournal{}, false, fmt.Errorf("invalid media asset cleanup journal %q", filepath.Base(journalPath))
	}
	trashDir := filepath.Join(store.dir, ".trash")
	for _, file := range journal.Files {
		withinTrash, pathErr := pathWithinRoot(file.Staged, trashDir)
		if pathErr != nil || !withinTrash || strings.TrimSpace(file.Original) == "" {
			return mediaAssetCleanupJournal{}, false, fmt.Errorf("unsafe media asset cleanup journal %q", filepath.Base(journalPath))
		}
	}
	return journal, true, nil
}

func (store *MediaAssets) reconcileMediaAssetCleanupJournalLocked(journal mediaAssetCleanupJournal) (bool, error) {
	model, err := store.repo.GetMediaAsset(journal.AssetID)
	if repository.IsRecordNotFound(err) {
		if removeErr := store.removeStagedFilesLocked(journal.Files); removeErr != nil {
			return false, removeErr
		}
		return false, store.removeMediaAssetCleanupJournalLocked(journal.AssetID)
	}
	if err != nil {
		return false, err
	}
	expected := store.mediaAssetCleanupJournalForAsset(store.mediaAssetRecordFromModel(model))
	if !sameMediaAssetCleanupFiles(journal.Files, expected.Files) {
		return false, fmt.Errorf("media asset cleanup journal does not match persisted storage")
	}
	if err := store.ensureMediaAssetFilesStagedLocked(journal.Files); err != nil {
		return false, err
	}
	deleteIfUnreferenced := store.deleteIfUnreferenced
	if deleteIfUnreferenced == nil {
		deleteIfUnreferenced = store.repo.DeleteMediaAssetIfUnreferenced
	}
	deleted, deleteErr := deleteIfUnreferenced(journal.AssetID)
	if deleteErr != nil || !deleted {
		restoreErr := store.restoreStagedMediaAssetFilesLocked(journal.Files)
		if restoreErr == nil {
			if journalErr := store.removeMediaAssetCleanupJournalLocked(journal.AssetID); journalErr != nil {
				restoreErr = journalErr
			}
		}
		if deleteErr != nil {
			if restoreErr != nil {
				return false, fmt.Errorf("conditional media asset delete failed; restoring staged media asset: %w", restoreErr)
			}
			return false, fmt.Errorf("conditional media asset delete failed: %w", deleteErr)
		}
		if restoreErr != nil {
			return false, fmt.Errorf("restoring referenced media asset: %w", restoreErr)
		}
		return false, nil
	}
	if err := store.removeStagedFilesLocked(journal.Files); err != nil {
		return true, err
	}
	if err := store.removeMediaAssetCleanupJournalLocked(journal.AssetID); err != nil {
		return true, err
	}
	return true, nil
}

func sameMediaAssetCleanupFiles(left []stagedMediaAssetFile, right []stagedMediaAssetFile) bool {
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

func (store *MediaAssets) ensureMediaAssetFilesStagedLocked(files []stagedMediaAssetFile) error {
	for _, file := range files {
		_, originalErr := os.Stat(file.Original)
		_, stagedErr := os.Stat(file.Staged)
		if originalErr != nil && !errors.Is(originalErr, os.ErrNotExist) {
			return fmt.Errorf("checking media asset file %q: %w", filepath.Base(file.Original), originalErr)
		}
		if stagedErr != nil && !errors.Is(stagedErr, os.ErrNotExist) {
			return fmt.Errorf("checking staged media asset file %q: %w", filepath.Base(file.Staged), stagedErr)
		}
		originalExists := originalErr == nil
		stagedExists := stagedErr == nil
		if originalExists && stagedExists {
			return fmt.Errorf("media asset cleanup has both live and staged files")
		}
		if stagedExists || !originalExists {
			continue
		}
		if err := store.renameMediaAssetFile(file.Original, file.Staged); err != nil {
			return err
		}
	}
	return nil
}

func (store *MediaAssets) renameMediaAssetFile(source string, target string) error {
	renameFile := store.renameFile
	if renameFile == nil {
		renameFile = os.Rename
	}
	if err := renameFile(source, target); err != nil {
		return fmt.Errorf("staging media asset file %q: %w", filepath.Base(source), err)
	}
	return nil
}

func (store *MediaAssets) restoreStagedMediaAssetFilesLocked(files []stagedMediaAssetFile) error {
	for index := len(files) - 1; index >= 0; index-- {
		file := files[index]
		if _, err := os.Stat(file.Staged); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("checking staged media asset file %q: %w", filepath.Base(file.Staged), err)
		}
		if _, err := os.Stat(file.Original); err == nil {
			return fmt.Errorf("restoring staged media asset would overwrite a live file")
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checking live media asset file %q: %w", filepath.Base(file.Original), err)
		}
		if err := store.renameMediaAssetFile(file.Staged, file.Original); err != nil {
			return err
		}
	}
	return nil
}

func (store *MediaAssets) removeStagedFilesLocked(files []stagedMediaAssetFile) error {
	removeFile := store.removeFile
	if removeFile == nil {
		removeFile = os.Remove
	}
	var removeErr error
	for _, file := range files {
		if err := removeFile(file.Staged); err != nil && !errors.Is(err, os.ErrNotExist) && removeErr == nil {
			removeErr = fmt.Errorf("removing staged media asset file %q: %w", filepath.Base(file.Staged), err)
		}
	}
	return removeErr
}

func (store *MediaAssets) removeStagedMediaAssetFilesLocked(id string) error {
	paths, err := filepath.Glob(filepath.Join(store.dir, ".trash", strings.TrimSpace(id)+"-*"))
	if err != nil {
		return fmt.Errorf("finding staged media asset files: %w", err)
	}
	files := make([]stagedMediaAssetFile, 0, len(paths))
	for _, path := range paths {
		files = append(files, stagedMediaAssetFile{Staged: path})
	}
	return store.removeStagedFilesLocked(files)
}

func (store *MediaAssets) mediaAssetCleanupJournalPath(id string) string {
	return filepath.Join(store.dir, ".trash", strings.TrimSpace(id)+".json")
}

func (store *MediaAssets) removeMediaAssetCleanupJournalLocked(id string) error {
	removeFile := store.removeFile
	if removeFile == nil {
		removeFile = os.Remove
	}
	path := store.mediaAssetCleanupJournalPath(id)
	if err := removeFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing media asset cleanup journal %q: %w", filepath.Base(path), err)
	}
	return nil
}

func (store *MediaAssets) reconcilePendingMediaAssetCleanup() {
	if store == nil || store.repo == nil || store.initErr != nil {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	paths, err := filepath.Glob(filepath.Join(store.dir, ".trash", "*.json"))
	if err != nil {
		slog.Warn("media asset cleanup scan remains pending")
		return
	}
	for _, path := range paths {
		id := strings.TrimSuffix(filepath.Base(path), ".json")
		journal, found, readErr := store.readMediaAssetCleanupJournalLocked(id)
		if readErr != nil || !found {
			slog.Warn("media asset cleanup journal remains pending", "asset_id", id)
			continue
		}
		if _, reconcileErr := store.reconcileMediaAssetCleanupJournalLocked(journal); reconcileErr != nil {
			slog.Warn("media asset cleanup journal remains pending", "asset_id", id)
		}
	}
}

func (store *MediaAssets) UpdateFilename(id string, filename string) (MediaAsset, bool, error) {
	if store.initErr != nil {
		return MediaAsset{}, false, store.initErr
	}
	filename = shared.SafeFilename(filename)
	if filename == "" {
		return MediaAsset{}, false, fmt.Errorf("filename is required")
	}

	asset, ok, err := store.Get(id)
	if err != nil {
		return MediaAsset{}, ok, err
	}
	if !ok {
		return MediaAsset{}, false, nil
	}
	if filepath.Ext(filename) == "" {
		filename += filepath.Ext(asset.Filename)
	}
	asset.Filename = filename
	asset.UpdatedAt = timestamp.NowRFC3339Nano()

	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.repo.UpdateMediaAssetFilename(asset.ID, asset.Filename, asset.UpdatedAt); err != nil {
		return MediaAsset{}, false, err
	}

	return asset, true, nil
}

func FilterMediaAssets(assets []MediaAsset, kind string, query string) []MediaAsset {
	kind = strings.ToLower(strings.TrimSpace(kind))
	query = strings.ToLower(strings.TrimSpace(query))
	if kind == "" && query == "" {
		return assets
	}

	filtered := make([]MediaAsset, 0, len(assets))
	for _, asset := range assets {
		if kind != "" && kind != "all" && strings.ToLower(asset.Kind) != kind {
			continue
		}
		if query != "" &&
			!strings.Contains(strings.ToLower(asset.Filename), query) &&
			!strings.Contains(strings.ToLower(asset.SourceURL), query) &&
			!strings.Contains(strings.ToLower(asset.RelativePath), query) &&
			!strings.Contains(strings.ToLower(asset.MIMEType), query) {
			continue
		}
		filtered = append(filtered, asset)
	}

	return filtered
}

func (store *MediaAssets) backfillListedVideoMetadata(assets []MediaAsset) ([]MediaAsset, error) {
	for index := range assets {
		asset := assets[index]
		if !store.shouldAttemptVideoMetadataBackfill(asset) {
			continue
		}

		updated := store.enrichVideoMetadata(asset, timestamp.NowRFC3339Nano())
		store.mu.Lock()
		err := store.repo.UpdateMediaAssetMetadata(updated.ID, store.mediaAssetMetadataUpdates(updated))
		store.mu.Unlock()
		if err != nil {
			return nil, err
		}
		assets[index] = updated
	}
	return assets, nil
}

func (store *MediaAssets) shouldAttemptVideoMetadataBackfill(asset MediaAsset) bool {
	if !store.needsVideoMetadataBackfill(asset) {
		return false
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.metadataBackfillAttempts == nil {
		store.metadataBackfillAttempts = map[string]struct{}{}
	}
	if _, ok := store.metadataBackfillAttempts[asset.ID]; ok {
		return false
	}
	store.metadataBackfillAttempts[asset.ID] = struct{}{}
	return true
}

func (store *MediaAssets) needsVideoMetadataBackfill(asset MediaAsset) bool {
	if asset.Kind != MediaKindVideo || strings.TrimSpace(asset.FilePath) == "" {
		return false
	}
	if asset.DurationSeconds <= 0 || asset.Width <= 0 || asset.Height <= 0 {
		return true
	}
	if strings.TrimSpace(asset.MetadataStatus) != MetadataStatusReady {
		return true
	}
	if strings.TrimSpace(asset.PosterURL) == "" || strings.TrimSpace(asset.PosterPath) == "" {
		return true
	}
	if expectedPosterPath, err := store.expectedVideoPosterPath(asset); err != nil || !sameMediaAssetPath(asset.PosterPath, expectedPosterPath) {
		return true
	}
	posterPath, err := store.ServePosterFilePath(asset)
	if err != nil {
		return true
	}
	info, err := os.Stat(posterPath)
	return err != nil || info.Size() == 0
}

func (store *MediaAssets) mediaAssetMetadataUpdates(asset MediaAsset) map[string]any {
	return map[string]any{
		"duration_seconds": asset.DurationSeconds,
		"width":            asset.Width,
		"height":           asset.Height,
		"poster_rel_path":  store.mediaAssetDBRelPath(asset.ProjectID, asset.PosterPath),
		"poster_url":       asset.PosterURL,
		"metadata_status":  asset.MetadataStatus,
	}
}

func (store *MediaAssets) mediaAssetRecordsFromModels(models []mediaAssetModel) []MediaAsset {
	assets := make([]MediaAsset, 0, len(models))
	for _, model := range models {
		assets = append(assets, store.mediaAssetRecordFromModel(model))
	}
	return assets
}

func (store *MediaAssets) mediaAssetRecordFromModel(model mediaAssetModel) MediaAsset {
	return MediaAsset{
		ID:              model.ID,
		Kind:            model.Kind,
		Filename:        model.Filename,
		MIMEType:        model.MIMEType,
		SizeBytes:       model.SizeBytes,
		URL:             model.URL,
		SourceURL:       model.SourceURL,
		ContentHash:     model.ContentHash,
		ProjectID:       domain.StringValue(model.ProjectID),
		Source:          model.Source,
		RelativePath:    model.RelPath,
		DownloadPath:    store.assetFilePath(domain.StringValue(model.ProjectID), model.RelPath),
		DurationSeconds: model.DurationSeconds,
		Width:           model.Width,
		Height:          model.Height,
		PosterURL:       model.PosterURL,
		MetadataStatus:  model.MetadataStatus,
		StorageStatus:   model.StorageStatus,
		CreatedAt:       domain.StringFromTime(model.CreatedAt),
		UpdatedAt:       domain.StringFromTime(model.UpdatedAt),
		FilePath:        store.assetFilePath(domain.StringValue(model.ProjectID), model.RelPath),
		PosterPath:      store.assetFilePath(domain.StringValue(model.ProjectID), model.PosterRelPath),
	}
}

func (store *MediaAssets) assetFilePath(projectID string, relPath string) string {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" || filepath.IsAbs(relPath) {
		return relPath
	}
	projectID = domain.CleanProjectID(projectID)
	if projectID != "" {
		if projectDir, err := store.projectDir(projectID); err == nil && projectDir != "" {
			return filepath.Join(projectDir, filepath.FromSlash(relPath))
		}
	}
	baseDir := strings.TrimSpace(store.dir)
	if baseDir == "" && strings.TrimSpace(store.workspaceRoot) != "" {
		baseDir = shared.WorkspacePathsFor(store.workspaceRoot).LibraryAssetsDir()
	}
	if strings.HasPrefix(filepath.ToSlash(relPath), ".mediago-drama/") && strings.TrimSpace(store.workspaceRoot) != "" {
		return filepath.Join(store.workspaceRoot, filepath.FromSlash(relPath))
	}
	if baseDir == "" {
		baseDir = defaultMediaDir()
	}
	rel := strings.TrimPrefix(filepath.ToSlash(relPath), "library/")
	return filepath.Join(baseDir, filepath.FromSlash(rel))
}

func (store *MediaAssets) mediaAssetDBRelPath(projectID string, filePath string) string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" || !filepath.IsAbs(filePath) {
		return filepath.ToSlash(filePath)
	}
	projectID = domain.CleanProjectID(projectID)
	if projectID != "" {
		if projectDir, err := store.projectDir(projectID); err == nil && projectDir != "" {
			if rel, ok := relativePathUnder(projectDir, filePath); ok {
				return rel
			}
		}
	}
	if strings.TrimSpace(store.workspaceRoot) != "" {
		if rel, ok := relativePathUnder(store.workspaceRoot, filePath); ok {
			return rel
		}
	}
	if strings.TrimSpace(store.dir) != "" {
		if rel, ok := relativePathUnder(store.dir, filePath); ok {
			return rel
		}
	}
	return filePath
}

func relativePathUnder(root string, path string) (string, bool) {
	root = strings.TrimSpace(root)
	path = strings.TrimSpace(path)
	if root == "" || path == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func defaultMediaDir() string {
	return filepath.Join(shared.DefaultUserDataDir(), "assets")
}

func stripDataURI(value string) string {
	value = strings.TrimSpace(value)
	if _, encoded, ok := strings.Cut(value, ","); ok && strings.HasPrefix(strings.ToLower(value), "data:") {
		return strings.TrimSpace(encoded)
	}

	return value
}

func defaultAssetFilename(kind string, mimeType string) string {
	prefix := "asset"
	if kind == MediaKindImage {
		prefix = "image"
	}
	if kind == MediaKindVideo {
		prefix = "video"
	}
	if kind == MediaKindAudio {
		prefix = "audio"
	}
	if kind == MediaKindText {
		prefix = "text"
	}

	return prefix + shared.ExtensionForMIMEType(mimeType)
}

func filenameFromURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}

	return shared.SafeFilename(filepath.Base(parsed.Path))
}
