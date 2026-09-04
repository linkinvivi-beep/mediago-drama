package generation

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/services/server/internal/domain"
	"github.com/mediago-dev/mediago-drama/services/server/internal/service/media"
)

func (workflow *GenerationService) resolveGenerationReferences(
	route coregeneration.ModelRoute,
	request generationMessageRequest,
) ([]string, error) {
	if !route.SupportsReferenceURLs {
		return []string{}, nil
	}

	ordered := orderedGenerationReferencesFromParams(request.Params)
	if len(ordered) == 0 {
		ordered = canonicalOrderedGenerationReferences(request)
	}
	if err := validateOrderedGenerationReferences(ordered); err != nil {
		return nil, err
	}
	references := make([]string, 0, len(ordered))
	for _, item := range ordered {
		if strings.HasPrefix(item.Source, "url:") {
			references = append(references, strings.TrimPrefix(item.Source, "url:"))
			continue
		}
		assetID := strings.TrimPrefix(item.Source, "asset:")
		asset, ok, err := workflow.mediaAssets.Get(assetID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("media asset %q was not found", assetID)
		}
		if asset.Kind == string(coregeneration.KindAudio) {
			if routeSupportsAudioReference(route) {
				reference, err := workflow.audioReferenceValue(asset)
				if err != nil {
					return nil, fmt.Errorf("reading media asset %q: %w", assetID, err)
				}
				references = append(references, reference)
			}
			if route.Kind == coregeneration.KindVideo {
				continue
			}
			return nil, fmt.Errorf("media asset %q is not a supported reference", assetID)
		}
		if asset.Kind != string(coregeneration.KindImage) && asset.Kind != string(coregeneration.KindVideo) {
			return nil, fmt.Errorf("media asset %q is not a supported reference", assetID)
		}
		if route.Kind == coregeneration.KindImage && asset.Kind != string(coregeneration.KindImage) {
			return nil, fmt.Errorf("media asset %q is not an image reference", assetID)
		}
		if asset.Kind == string(coregeneration.KindVideo) && route.Adapter != coregeneration.AdapterOpenRouterVideo {
			return nil, fmt.Errorf("media asset %q is not supported as a video reference for this route", assetID)
		}

		reference := asset.URL
		if route.Kind == coregeneration.KindImage && asset.Kind == string(coregeneration.KindImage) {
			reference, err = workflow.mediaAssets.CompressedImageDataURIValue(
				asset,
				media.DefaultReferenceImageCompressionOptions(),
			)
		} else {
			reference, err = workflow.mediaAssets.DataURIValue(asset)
		}
		if err != nil {
			return nil, fmt.Errorf("reading media asset %q: %w", assetID, err)
		}
		references = append(references, reference)
	}

	return references, nil
}

func routeSupportsAudioReference(route coregeneration.ModelRoute) bool {
	return route.Kind == coregeneration.KindVideo &&
		(route.Adapter == coregeneration.AdapterJimengCLIVideo ||
			route.Adapter == coregeneration.AdapterPippitCLIVideo)
}

func (workflow *GenerationService) audioReferenceValue(asset media.MediaAsset) (string, error) {
	if strings.TrimSpace(asset.FilePath) != "" {
		return workflow.mediaAssets.DataURIValue(asset)
	}

	if reference, ok, err := workflow.voicePreviewReferenceValue(asset); err != nil {
		return "", err
	} else if ok {
		return reference, nil
	}

	for _, value := range []string{asset.SourceURL, asset.URL} {
		value = strings.TrimSpace(value)
		if isProviderReadableReferenceValue(value) {
			return value, nil
		}
	}

	return "", fmt.Errorf("audio asset has no readable file or reference URL")
}

func (workflow *GenerationService) voicePreviewReferenceValue(asset media.MediaAsset) (string, bool, error) {
	for _, value := range []string{asset.URL, asset.SourceURL} {
		routeID, voiceID, ok := generationVoicePreviewRouteAndVoiceFromURL(value)
		if !ok {
			continue
		}

		preview, data, found, err := workflow.GenerationVoicePreviewContent(routeID, voiceID)
		if err != nil || !found {
			return "", found, err
		}

		mimeType := strings.TrimSpace(firstNonEmpty(preview.MIMEType, asset.MIMEType, "audio/mpeg"))
		return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), true, nil
	}
	return "", false, nil
}

func isProviderReadableReferenceValue(value string) bool {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "data:") ||
		strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "file://") ||
		filepath.IsAbs(value)
}

func splitReferenceURLs(values []string) ([]string, []string) {
	referenceURLs := []string{}
	assetIDs := []string{}
	seenReferenceURLs := map[string]struct{}{}
	seenAssetIDs := map[string]struct{}{}

	for _, value := range CompactStrings(values) {
		if assetID := libraryAssetIDFromGenerationAssetURL(value); assetID != "" {
			if _, exists := seenAssetIDs[assetID]; exists {
				continue
			}

			seenAssetIDs[assetID] = struct{}{}
			assetIDs = append(assetIDs, assetID)
			continue
		}

		if _, exists := seenReferenceURLs[value]; exists {
			continue
		}

		seenReferenceURLs[value] = struct{}{}
		referenceURLs = append(referenceURLs, value)
	}

	return referenceURLs, assetIDs
}

func uniqueCompactStrings(values []string) []string {
	result := []string{}
	seen := map[string]struct{}{}
	for _, value := range CompactStrings(values) {
		if _, exists := seen[value]; exists {
			continue
		}

		seen[value] = struct{}{}
		result = append(result, value)
	}

	return result
}

func (workflow *GenerationService) cacheGenerationResponseAssets(
	ctx context.Context,
	response coregeneration.Response,
	projectID string,
) coregeneration.Response {
	return workflow.cacheGenerationResponseAssetsWithOptions(ctx, response, generationMediaSaveOptions(projectID, "", ""))
}

func (workflow *GenerationService) cacheGenerationResponseAssetsForScope(
	ctx context.Context,
	response coregeneration.Response,
	projectID string,
	conversationID string,
) coregeneration.Response {
	return workflow.cacheGenerationResponseAssetsWithOptions(ctx, response, generationMediaSaveOptions(projectID, conversationID, ""))
}

func (workflow *GenerationService) cacheGenerationResponseAssetsForTask(
	ctx context.Context,
	response coregeneration.Response,
	task generationTaskRecord,
) coregeneration.Response {
	return workflow.cacheGenerationResponseAssetsWithOptions(ctx, response, generationMediaSaveOptionsWithTitle(
		workflow.projectIDForTask(task),
		task.ConversationID,
		task.SectionID,
		generationAssetTitleFromTask(task),
	))
}

func (workflow *GenerationService) cacheGenerationResponseAssetsWithOptions(
	ctx context.Context,
	response coregeneration.Response,
	options media.MediaAssetSaveOptions,
) coregeneration.Response {
	response, _ = workflow.cacheGenerationResponseAssetsWithOptionsTracked(ctx, response, options)
	return response
}

func (workflow *GenerationService) cacheGenerationResponseAssetsWithOptionsTracked(
	ctx context.Context,
	response coregeneration.Response,
	options media.MediaAssetSaveOptions,
) (coregeneration.Response, []string) {
	response, receipt := workflow.cacheGenerationResponseAssetsWithOptionsReceipt(ctx, response, options, false)
	return response, receipt.createdAssetIDs
}

func (workflow *GenerationService) cacheGenerationResponseAssetsWithOptionsClaimed(
	ctx context.Context,
	response coregeneration.Response,
	options media.MediaAssetSaveOptions,
) (coregeneration.Response, []media.MediaAssetClaim) {
	response, receipt := workflow.cacheGenerationResponseAssetsWithOptionsReceipt(ctx, response, options, true)
	return response, receipt.claims
}

type generationAssetCacheReceipt struct {
	createdAssetIDs []string
	claims          []media.MediaAssetClaim
}

func (workflow *GenerationService) cacheGenerationResponseAssetsWithOptionsReceipt(
	ctx context.Context,
	response coregeneration.Response,
	options media.MediaAssetSaveOptions,
	claimAssets bool,
) (coregeneration.Response, generationAssetCacheReceipt) {
	if len(response.Assets) == 0 {
		return response, generationAssetCacheReceipt{}
	}

	warnings := []string{}
	receipt := generationAssetCacheReceipt{}
	codexImportFailed := false
	for index, asset := range response.Assets {
		internalCodexPayload, _ := asset.Metadata[codexImageInternalPayloadKey].(bool)
		asset.Metadata = generationAssetMetadataWithoutInternalSources(asset.Metadata)
		response.Assets[index].Metadata = asset.Metadata
		var cached media.MediaAsset
		var created bool
		var claim media.MediaAssetClaim
		var err error
		switch {
		case workflow.mediaAssets == nil:
			if !internalCodexPayload {
				continue
			}
			err = fmt.Errorf("MediaLink media asset store is unavailable")
		case internalCodexPayload && ctx.Err() != nil:
			err = ctx.Err()
		default:
			if claimAssets {
				cached, claim, err = workflow.cacheGenerationAssetClaimed(ctx, asset, options)
				created = claim.Created
			} else {
				cached, created, err = workflow.cacheGenerationAssetTracked(ctx, asset, options)
			}
		}
		if internalCodexPayload && err == nil && cached.ID == "" {
			err = fmt.Errorf("Codex image output did not produce a MediaLink asset")
		}
		if internalCodexPayload {
			response.Assets[index].Base64 = ""
			if err != nil {
				response.Assets[index].URL = ""
				codexImportFailed = true
			}
		}
		if err != nil {
			warning := err.Error()
			if internalCodexPayload {
				warning = "Codex image output could not be imported into MediaLink assets"
			}
			warnings = append(warnings, warning)
			slog.Warn(
				"generation asset cache failed",
				"response_id", response.ID,
				"model", response.Model,
				"asset_kind", asset.Kind,
				"asset_url", sanitizedLogString(asset.URL),
				"error", err,
			)
			continue
		}
		if cached.ID == "" {
			continue
		}
		if created {
			receipt.createdAssetIDs = append(receipt.createdAssetIDs, cached.ID)
		}
		if claim.AssetID != "" {
			receipt.claims = append(receipt.claims, claim)
		}

		response.Assets[index].URL = cached.URL
		response.Assets[index].Base64 = ""
		response.Assets[index].MIMEType = cached.MIMEType
		if response.Assets[index].Metadata == nil {
			response.Assets[index].Metadata = map[string]any{}
		}
		response.Assets[index].Metadata["asset_id"] = cached.ID
		if cached.DownloadPath != "" {
			response.Assets[index].Metadata["download_path"] = cached.DownloadPath
		}
		if cached.PosterURL != "" {
			response.Assets[index].Metadata["poster_url"] = cached.PosterURL
		}
	}
	if codexImportFailed {
		if response.Metadata == nil {
			response.Metadata = map[string]any{}
		}
		response.Status = "failed"
		response.Metadata["error"] = "Codex image output could not be imported into MediaLink assets"
		response.Metadata["failure_message"] = "图像已生成，但导入 MediaLink 素材库失败。"
		response.Metadata["retryable"] = true
	}
	if len(warnings) > 0 {
		if response.Metadata == nil {
			response.Metadata = map[string]any{}
		}
		response.Metadata["asset_cache_warnings"] = warnings
	}

	return response, receipt
}

// CacheGenerationResponseAssets stores generated assets in the local media store when possible.
func (workflow *GenerationService) CacheGenerationResponseAssets(
	ctx context.Context,
	response coregeneration.Response,
) coregeneration.Response {
	return workflow.cacheGenerationResponseAssets(ctx, response, "")
}

func (workflow *GenerationService) cacheGenerationAsset(
	ctx context.Context,
	asset coregeneration.Asset,
	options media.MediaAssetSaveOptions,
) (media.MediaAsset, error) {
	cached, _, err := workflow.cacheGenerationAssetTracked(ctx, asset, options)
	return cached, err
}

func (workflow *GenerationService) cacheGenerationAssetTracked(
	ctx context.Context,
	asset coregeneration.Asset,
	options media.MediaAssetSaveOptions,
) (media.MediaAsset, bool, error) {
	kind := string(asset.Kind)
	if asset.Base64 != "" {
		cached, created, err := workflow.mediaAssets.SaveBase64WithOptionsTracked(kind, asset.MIMEType, asset.Base64, "", options)
		if err != nil {
			return media.MediaAsset{}, false, fmt.Errorf("saving base64 asset: %w", err)
		}

		return cached, created, nil
	}
	if asset.URL == "" || isLocalMediaAssetURL(asset.URL) {
		return media.MediaAsset{}, false, nil
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(asset.URL)), "data:") {
		cached, created, err := workflow.mediaAssets.SaveBase64WithOptionsTracked(kind, asset.MIMEType, asset.URL, "", options)
		if err != nil {
			return media.MediaAsset{}, false, fmt.Errorf("saving data uri asset: %w", err)
		}

		return cached, created, nil
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(asset.URL)), "http://") &&
		!strings.HasPrefix(strings.ToLower(strings.TrimSpace(asset.URL)), "https://") {
		return media.MediaAsset{}, false, fmt.Errorf("unsupported generated asset url scheme")
	}

	cached, created, err := workflow.mediaAssets.SaveRemoteAssetWithOptionsTracked(ctx, kind, asset.URL, options)
	if err != nil {
		return media.MediaAsset{}, false, fmt.Errorf("caching remote asset: %w", err)
	}

	cached = workflow.renameCachedGenerationAsset(cached, options, asset.URL)
	return cached, created, nil
}

func (workflow *GenerationService) cacheGenerationAssetClaimed(
	ctx context.Context,
	asset coregeneration.Asset,
	options media.MediaAssetSaveOptions,
) (media.MediaAsset, media.MediaAssetClaim, error) {
	kind := string(asset.Kind)
	if asset.Base64 != "" {
		cached, claim, err := workflow.mediaAssets.SaveBase64WithOptionsClaimed(kind, asset.MIMEType, asset.Base64, "", options)
		if err != nil {
			return media.MediaAsset{}, media.MediaAssetClaim{}, fmt.Errorf("saving base64 asset: %w", err)
		}
		return cached, claim, nil
	}
	if asset.URL == "" || isLocalMediaAssetURL(asset.URL) {
		return media.MediaAsset{}, media.MediaAssetClaim{}, nil
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(asset.URL)), "data:") {
		cached, claim, err := workflow.mediaAssets.SaveBase64WithOptionsClaimed(kind, asset.MIMEType, asset.URL, "", options)
		if err != nil {
			return media.MediaAsset{}, media.MediaAssetClaim{}, fmt.Errorf("saving data uri asset: %w", err)
		}
		return cached, claim, nil
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(asset.URL)), "http://") &&
		!strings.HasPrefix(strings.ToLower(strings.TrimSpace(asset.URL)), "https://") {
		return media.MediaAsset{}, media.MediaAssetClaim{}, fmt.Errorf("unsupported generated asset url scheme")
	}
	cached, claim, err := workflow.mediaAssets.SaveRemoteAssetWithOptionsClaimed(ctx, kind, asset.URL, options)
	if err != nil {
		return media.MediaAsset{}, media.MediaAssetClaim{}, fmt.Errorf("caching remote asset: %w", err)
	}
	cached = workflow.renameCachedGenerationAsset(cached, options, asset.URL)
	return cached, claim, nil
}

func (workflow *GenerationService) finalizeGenerationAssetClaims(claims []media.MediaAssetClaim, persisted bool) {
	if workflow == nil || workflow.mediaAssets == nil || len(claims) == 0 {
		return
	}
	if persisted {
		for _, err := range workflow.mediaAssets.CommitGenerationAssetClaims(claims) {
			slog.Warn("generation asset commit failed", "error", sanitizedLogString(err.Error()))
		}
		return
	}
	for _, err := range workflow.mediaAssets.CompensateGenerationAssetClaims(claims) {
		slog.Warn("generation asset compensation failed", "error", sanitizedLogString(err.Error()))
	}
}

func generationAssetMetadataWithoutInternalSources(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	cleaned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "_", "")) {
		case "savedpath", "localpath", "medialinkinternalcodeximagepayload":
			continue
		default:
			cleaned[key] = value
		}
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func (workflow *GenerationService) renameCachedGenerationAsset(
	cached media.MediaAsset,
	options media.MediaAssetSaveOptions,
	sourceURL string,
) media.MediaAsset {
	filename := strings.TrimSpace(options.Filename)
	if workflow == nil ||
		workflow.mediaAssets == nil ||
		filename == "" ||
		cached.ID == "" ||
		strings.TrimSpace(cached.SourceURL) != strings.TrimSpace(sourceURL) {
		return cached
	}
	if filepath.Ext(filename) == "" {
		filename += filepath.Ext(cached.Filename)
	}
	if cached.Filename == filename {
		return cached
	}
	updated, ok, err := workflow.mediaAssets.UpdateFilename(cached.ID, filename)
	if err != nil || !ok {
		return cached
	}
	return updated
}

func isLocalMediaAssetURL(value string) bool {
	return libraryAssetIDFromGenerationAssetURL(value) != ""
}

func generationMediaSaveOptions(projectID string, conversationID string, sectionID string) media.MediaAssetSaveOptions {
	source := media.MediaSourceGeneration
	if strings.TrimSpace(projectID) == "" && strings.TrimSpace(conversationID) != "" {
		source = media.MediaSourceToolbox
	}
	return media.MediaAssetSaveOptions{
		ProjectID:      projectID,
		Source:         source,
		ConversationID: conversationID,
		SectionID:      sectionID,
	}
}

func generationMediaSaveOptionsWithTitle(projectID string, conversationID string, sectionID string, assetTitle string) media.MediaAssetSaveOptions {
	options := generationMediaSaveOptions(projectID, conversationID, sectionID)
	options.Filename = strings.TrimSpace(assetTitle)
	return options
}

func (workflow *GenerationService) saveGenerationBase64Asset(kind string, mimeType string, value string, sourceURL string, projectID string, conversationID string) (media.MediaAsset, error) {
	return workflow.mediaAssets.SaveBase64WithOptions(kind, mimeType, value, sourceURL, generationMediaSaveOptions(projectID, conversationID, ""))
}

func (workflow *GenerationService) saveGenerationRemoteAsset(ctx context.Context, kind string, remoteURL string, projectID string, conversationID string) (media.MediaAsset, error) {
	return workflow.mediaAssets.SaveRemoteAssetWithOptions(ctx, kind, remoteURL, generationMediaSaveOptions(projectID, conversationID, ""))
}

func (workflow *GenerationService) projectIDForConversation(conversationID string) string {
	if workflow == nil || workflow.generationTasks == nil {
		return ""
	}
	conversation, ok, err := workflow.generationTasks.GetConversation(strings.TrimSpace(conversationID))
	if err != nil || !ok {
		return ""
	}
	return GenerationProjectIDFromScopeID(conversation.ScopeID)
}

func (workflow *GenerationService) projectIDForTask(task generationTaskRecord) string {
	if projectID := GenerationProjectIDForRequest(task.ProjectID, ""); projectID != "" {
		return projectID
	}
	return workflow.projectIDForConversation(task.ConversationID)
}

func (workflow *GenerationService) studioSessionIDForConversation(conversation GenerationConversationRecord, projectID string) string {
	if strings.TrimSpace(projectID) != "" || !isFileBackedGenerationConversation(conversation) {
		return ""
	}
	return domain.CleanProjectID(conversation.ID)
}

func (workflow *GenerationService) studioDirForSessionID(sessionID string) string {
	if workflow == nil || workflow.generationTasks == nil {
		return ""
	}
	conversation, ok, err := workflow.generationTasks.GetConversation(domain.CleanProjectID(sessionID))
	if err != nil || !ok {
		return ""
	}
	return workflow.ensureStudioSessionDir(conversation)
}

func (workflow *GenerationService) studioSessionIDForTask(task generationTaskRecord) string {
	if workflow == nil || workflow.generationTasks == nil {
		return ""
	}
	if workflow.projectIDForTask(task) != "" {
		return ""
	}
	conversation, ok, err := workflow.generationTasks.GetConversation(strings.TrimSpace(task.ConversationID))
	if err != nil || !ok {
		return ""
	}
	return workflow.studioSessionIDForConversation(conversation, "")
}
