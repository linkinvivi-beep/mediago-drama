package generation

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/packages/core/pkg/generation/runtime"
	configassets "github.com/mediago-dev/mediago-drama/services/server/configs"
	"github.com/mediago-dev/mediago-drama/services/server/internal/service/media"
	"github.com/mediago-dev/mediago-drama/services/server/internal/service/settings"
	"github.com/mediago-dev/mediago-drama/services/server/internal/service/textcompletion"
)

const generationRequestTimeout = 1000 * time.Second

// GenerationService owns generation request orchestration and task persistence.
type GenerationService struct {
	settings                      *settings.Settings
	generationPreferences         *GenerationPreferenceService
	generationNotifications       *GenerationNotificationService
	generationTasks               *GenerationTaskService
	mediaAssets                   *media.MediaAssets
	documents                     GenerationDocumentResolver
	generationProviderFactory     func(coregeneration.ModelRoute) (coregeneration.Provider, error)
	legacyProviderFactory         func(coregeneration.ModelRoute) (coregeneration.Provider, error)
	mediaLinkReadiness            func(context.Context, string) (bool, string)
	multimodalTextProviderFactory runtime.MultimodalTextProviderFactory
	voicePreviews                 *VoicePreviewStore
	stylePreviews                 *StylePreviewStore
	stylePrompts                  StylePromptSource
	contentUseAuthorizer          ContentUseAuthorizer
	textCompletion                *textcompletion.Service
	mediagoBaseURL                string
	mediagoModelCatalog           mediagoModelCatalogCache
	jimengBinPath                 string
	jimengBinDir                  string
	libTVBinPath                  string
	libTVBinDir                   string
	libTVProjectID                string
	pippitBinPath                 string
	pippitBinDir                  string
	jimengSeedanceQueueMu         sync.Mutex
	generationRootCtx             context.Context
	generationRootCancel          context.CancelFunc
	generationCancelMu            sync.Mutex
	generationCancels             map[string]map[*generationTaskCancellation]struct{}
	generationPreflightCancels    map[string]map[*generationTaskCancellation]struct{}
	generationDeleteMu            sync.Mutex
	generationDeleting            map[string]int
	// Test-only synchronization seams for the otherwise sub-millisecond
	// claim/delete transfer window. Production construction leaves both nil.
	generationRetryClaimedHook   func()
	generationDeleteStartingHook func()
	generationAssetsCachedHook   func(string)
	generationSubmitFinishedHook func(string)
}

type generationTaskCancellation struct {
	cancel context.CancelFunc
}

// SetTextCompletionService configures executor-neutral internal text completion.
func (workflow *GenerationService) SetTextCompletionService(service *textcompletion.Service) {
	workflow.textCompletion = service
}

type generationModelsResponse = GenerationModelsResponse
type generationMessageRequest = GenerationMessageRequest
type generationMessageResponse = GenerationMessageResponse
type generationTaskRecord = GenerationTaskRecord
type generationTasksResponse = GenerationTasksResponse

// NewGenerationService creates a generation workflow service.
func NewGenerationService(settings *settings.Settings, generationTasks *GenerationTaskService, mediaAssets *media.MediaAssets, generationPreferences ...*GenerationPreferenceService) *GenerationService {
	var preferences *GenerationPreferenceService
	if len(generationPreferences) > 0 {
		preferences = generationPreferences[0]
	}
	rootCtx, rootCancel := context.WithCancel(context.Background())
	return &GenerationService{
		settings:                      settings,
		generationPreferences:         preferences,
		generationTasks:               generationTasks,
		mediaAssets:                   mediaAssets,
		multimodalTextProviderFactory: defaultMultimodalTextProviderFactory,
		voicePreviews:                 NewVoicePreviewStore(configassets.VoicePreviews),
		stylePreviews:                 NewStylePreviewStore(configassets.StylePresets),
		generationRootCtx:             rootCtx,
		generationRootCancel:          rootCancel,
		generationCancels:             map[string]map[*generationTaskCancellation]struct{}{},
		generationPreflightCancels:    map[string]map[*generationTaskCancellation]struct{}{},
		generationDeleting:            map[string]int{},
	}
}

// SetGenerationRuntimeContext binds background generation jobs to app shutdown.
func (workflow *GenerationService) SetGenerationRuntimeContext(parent context.Context) {
	if parent == nil {
		parent = context.Background()
	}
	workflow.generationCancelMu.Lock()
	oldCancel := workflow.generationRootCancel
	workflow.generationRootCtx, workflow.generationRootCancel = context.WithCancel(parent)
	workflow.generationCancelMu.Unlock()
	if oldCancel != nil {
		oldCancel()
	}
}

func (workflow *GenerationService) launchSubmittedGeneration(task generationTaskRecord, provider coregeneration.Provider, request coregeneration.Request, action string, projectID string, conversationID string) {
	ctx, done := workflow.generationTaskContext(task.ID)
	workflow.launchClaimedSubmittedGeneration(ctx, done, task, provider, request, action, projectID, conversationID)
}

func (workflow *GenerationService) launchClaimedSubmittedGeneration(ctx context.Context, done func(), task generationTaskRecord, provider coregeneration.Provider, request coregeneration.Request, action string, projectID string, conversationID string) {
	go func() {
		defer done()
		workflow.completeSubmittedGeneration(ctx, task, provider, request, action, projectID, conversationID)
	}()
}

func (workflow *GenerationService) generationTaskContext(taskID string) (context.Context, func()) {
	workflow.generationCancelMu.Lock()
	ctx, entry := workflow.registerGenerationTaskContextLocked(taskID)
	workflow.generationCancelMu.Unlock()
	return ctx, workflow.generationTaskContextRelease(taskID, entry)
}

func (workflow *GenerationService) generationTaskLocallyOwned(taskID string) bool {
	workflow.generationCancelMu.Lock()
	owned := len(workflow.generationCancels[strings.TrimSpace(taskID)]) > 0
	workflow.generationCancelMu.Unlock()
	return owned
}

func (workflow *GenerationService) registerGenerationTaskContextLocked(taskID string) (context.Context, *generationTaskCancellation) {
	parent := workflow.generationRootCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	entry := &generationTaskCancellation{cancel: cancel}
	entries := workflow.generationCancels[taskID]
	if entries == nil {
		entries = map[*generationTaskCancellation]struct{}{}
		workflow.generationCancels[taskID] = entries
	}
	entries[entry] = struct{}{}
	return ctx, entry
}

func (workflow *GenerationService) generationTaskContextRelease(taskID string, entry *generationTaskCancellation) func() {
	return func() {
		if entry == nil {
			return
		}
		workflow.generationCancelMu.Lock()
		workflow.unregisterGenerationTaskContextLocked(taskID, entry)
		workflow.generationCancelMu.Unlock()
	}
}

func (workflow *GenerationService) unregisterGenerationTaskContextLocked(taskID string, entry *generationTaskCancellation) {
	entry.cancel()
	entries := workflow.generationCancels[taskID]
	delete(entries, entry)
	if len(entries) == 0 {
		delete(workflow.generationCancels, taskID)
	}
}

func (workflow *GenerationService) claimFailedCodexRetryContext(preflight context.Context, taskID string, message string) (GenerationTaskRuntimeState, context.Context, func(), bool, error) {
	workflow.generationCancelMu.Lock()
	defer workflow.generationCancelMu.Unlock()
	if err := preflight.Err(); err != nil {
		return GenerationTaskRuntimeState{}, nil, nil, false, err
	}
	if workflow.generationTaskDeletionRequested(taskID) {
		return GenerationTaskRuntimeState{}, nil, nil, false, nil
	}
	ctx, entry := workflow.registerGenerationTaskContextLocked(taskID)
	state, claimed, err := workflow.generationTasks.ClaimFailedCodexRetry(taskID, message)
	if err != nil || !claimed {
		workflow.unregisterGenerationTaskContextLocked(taskID, entry)
		return state, nil, nil, claimed, err
	}
	if workflow.generationRetryClaimedHook != nil {
		workflow.generationRetryClaimedHook()
	}
	return state, ctx, workflow.generationTaskContextRelease(taskID, entry), true, nil
}

// generationPreflightContext is caller-owned but also cancellable by task
// deletion and app shutdown. It is deliberately kept separate from the
// app-lifetime task registry used after a failed retry is atomically claimed.
func (workflow *GenerationService) generationPreflightContext(taskID string, caller context.Context) (context.Context, func()) {
	workflow.generationCancelMu.Lock()
	parent := workflow.generationRootCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	stopCaller := func() bool { return false }
	if caller != nil {
		stopCaller = context.AfterFunc(caller, cancel)
	}
	entry := &generationTaskCancellation{cancel: cancel}
	entries := workflow.generationPreflightCancels[taskID]
	if entries == nil {
		entries = map[*generationTaskCancellation]struct{}{}
		workflow.generationPreflightCancels[taskID] = entries
	}
	entries[entry] = struct{}{}
	workflow.generationCancelMu.Unlock()
	return ctx, func() {
		stopCaller()
		cancel()
		workflow.generationCancelMu.Lock()
		entries := workflow.generationPreflightCancels[taskID]
		delete(entries, entry)
		if len(entries) == 0 {
			delete(workflow.generationPreflightCancels, taskID)
		}
		workflow.generationCancelMu.Unlock()
	}
}

func (workflow *GenerationService) cancelGenerationTask(taskID string) {
	workflow.generationCancelMu.Lock()
	workflow.cancelGenerationTaskLocked(taskID)
	workflow.generationCancelMu.Unlock()
}

func (workflow *GenerationService) cancelGenerationTaskLocked(taskID string) {
	entries := workflow.generationCancels[strings.TrimSpace(taskID)]
	for entry := range entries {
		entry.cancel()
	}
	preflightEntries := workflow.generationPreflightCancels[strings.TrimSpace(taskID)]
	for entry := range preflightEntries {
		entry.cancel()
	}
}

func (workflow *GenerationService) markGenerationTaskDeleting(taskID string) func() {
	taskID = strings.TrimSpace(taskID)
	workflow.generationDeleteMu.Lock()
	workflow.generationDeleting[taskID]++
	workflow.generationDeleteMu.Unlock()
	return func() {
		workflow.generationDeleteMu.Lock()
		workflow.generationDeleting[taskID]--
		if workflow.generationDeleting[taskID] <= 0 {
			delete(workflow.generationDeleting, taskID)
		}
		workflow.generationDeleteMu.Unlock()
	}
}

func (workflow *GenerationService) generationTaskDeletionRequested(taskID string) bool {
	workflow.generationDeleteMu.Lock()
	deleting := workflow.generationDeleting[strings.TrimSpace(taskID)] > 0
	workflow.generationDeleteMu.Unlock()
	return deleting
}

// SetStylePromptLibrary wires the prompt library that owns style presets.
func (workflow *GenerationService) SetStylePromptLibrary(source StylePromptSource) {
	workflow.stylePrompts = source
}

// SetContentUseAuthorizer installs the edition-specific formal-content gate.
func (workflow *GenerationService) SetContentUseAuthorizer(authorizer ContentUseAuthorizer) {
	workflow.contentUseAuthorizer = authorizer
}

// SetJimengCLIPaths configures the local Jimeng CLI lookup paths.
func (workflow *GenerationService) SetJimengCLIPaths(binPath string, binDir string) {
	workflow.jimengBinPath = strings.TrimSpace(binPath)
	workflow.jimengBinDir = strings.TrimSpace(binDir)
}

// SetLibTVCLIConfig configures the local LibTV CLI lookup paths and optional project.
func (workflow *GenerationService) SetLibTVCLIConfig(binPath string, binDir string, projectID string) {
	workflow.libTVBinPath = strings.TrimSpace(binPath)
	workflow.libTVBinDir = strings.TrimSpace(binDir)
	workflow.libTVProjectID = strings.TrimSpace(projectID)
}

// SetPippitCLIPaths configures the local Pippit / Xiaoyunque CLI lookup paths.
func (workflow *GenerationService) SetPippitCLIPaths(binPath string, binDir string) {
	workflow.pippitBinPath = strings.TrimSpace(binPath)
	workflow.pippitBinDir = strings.TrimSpace(binDir)
}

// SetMediagoBaseURL configures the MediaGo OpenAI-compatible generation endpoint.
func (workflow *GenerationService) SetMediagoBaseURL(baseURL string) {
	workflow.mediagoBaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

// SetGenerationNotifications sets the notification service used by generation
// workflows and wires task start/completion transitions to connected clients.
func (workflow *GenerationService) SetGenerationNotifications(notifications *GenerationNotificationService) {
	workflow.generationNotifications = notifications
	if workflow.generationTasks != nil && notifications != nil {
		workflow.generationTasks.SetTaskStartedListener(notifications.AnnounceTaskStarted)
		workflow.generationTasks.SetTaskCompletionListener(notifications.AnnounceTaskCompletion)
	}
}

// SetDocumentResolver sets the workspace document reader used by document-backed generation.
func (workflow *GenerationService) SetDocumentResolver(documents GenerationDocumentResolver) {
	workflow.documents = documents
}

// ListGenerationModels returns the generation model catalog for HTTP handlers.
func (workflow *GenerationService) ListGenerationModels() generationModelsResponse {
	catalog := mediaLinkCatalog(coregeneration.Catalog())
	for index := range catalog.Routes {
		catalog.Routes[index].Configured = workflow.generationRouteConfigured(catalog.Routes[index])
	}

	return generationModelsResponse{
		Families:      catalog.Families,
		Versions:      catalog.Versions,
		Routes:        catalog.Routes,
		Models:        catalog.Models,
		Providers:     catalog.Providers,
		VoicePreviews: workflow.listVoicePreviewAssets(),
		StylePresets:  workflow.listStylePresets(),
	}
}

// CreateGenerationMessage creates a generation request for HTTP handlers.
func (workflow *GenerationService) CreateGenerationMessage(ctx context.Context, payload generationMessageRequest) (generationMessageResponse, int, error) {
	payload.Kind = strings.TrimSpace(payload.Kind)
	payload.ConversationID = strings.TrimSpace(payload.ConversationID)
	hasScopeFilter := strings.TrimSpace(payload.ScopeID) != ""
	payload.ScopeID = NormalizeGenerationConversationScopeID(payload.ScopeID)
	payload.ProjectID = GenerationProjectIDForRequest(payload.ProjectID, "")
	if payload.ProjectID == "" && payload.NotificationTarget != nil {
		payload.ProjectID = GenerationProjectIDForRequest(payload.NotificationTarget.ProjectID, "")
	}
	payload.Prompt = strings.TrimSpace(payload.Prompt)
	payload.RouteID = strings.TrimSpace(payload.RouteID)
	payload.FamilyID = strings.TrimSpace(payload.FamilyID)
	payload.VersionID = strings.TrimSpace(payload.VersionID)
	payload.Provider = strings.TrimSpace(payload.Provider)
	payload.ModelID = strings.TrimSpace(payload.ModelID)
	payload.Model = strings.TrimSpace(payload.Model)
	payload.AssetTitle = strings.TrimSpace(payload.AssetTitle)
	if status, err := workflow.resolveGenerationPromptReferences(ctx, &payload); err != nil {
		return generationMessageResponse{}, status, err
	}
	payload.PromptSupplements = NormalizeGenerationPromptSupplements(payload.PromptSupplements)
	payload.ReferenceURLs = CompactStrings(payload.ReferenceURLs)
	payload.ReferenceAssetIDs = CompactStrings(payload.ReferenceAssetIDs)
	payload.ReferenceBindings = normalizeGenerationReferenceBindings(payload.ReferenceBindings)
	var sourceRefsErr error
	payload.SourceRefs, sourceRefsErr = normalizeContentSourceRefs(payload.SourceRefs)
	if sourceRefsErr != nil {
		return generationMessageResponse{}, http.StatusForbidden, sourceRefsErr
	}
	// Prompt optimization is handled exclusively by CreatePromptOptimizedGenerationMessage
	// (the optimize-and-generate endpoint); plain generation ignores this field.
	payload.PromptOptimization = nil
	if err := workflow.applyGenerationDocumentContext(&payload); err != nil {
		return generationMessageResponse{}, http.StatusBadRequest, err
	}
	if payload.AssetTitle == "" {
		payload.AssetTitle = generationAssetTitleFromNotificationTarget(payload.NotificationTarget)
	}
	payload.Prompt = ApplyGenerationPromptSupplements(payload.Prompt, payload.PromptSupplements)
	payload.PromptSupplements = nil
	payload.ReferenceURLs = uniqueCompactStrings(payload.ReferenceURLs)
	payload.ReferenceAssetIDs = uniqueCompactStrings(payload.ReferenceAssetIDs)
	if payload.Kind == "" && payload.RouteID == "" && payload.ModelID == "" {
		payload.Kind = string(coregeneration.KindImage)
	}
	payload.Params = NormalizeGenerationParams(payload.Params)
	orderedReferences := canonicalOrderedGenerationReferences(payload)
	if err := validateOrderedGenerationReferences(orderedReferences); err != nil {
		return generationMessageResponse{}, http.StatusBadRequest, err
	}
	payload.Params = generationParamsWithOrderedReferences(payload.Params, orderedReferences)
	if payload.Prompt == "" {
		return generationMessageResponse{}, http.StatusBadRequest, fmt.Errorf("缺少 prompt")
	}
	if status, err := workflow.authorizeContentUse(ctx, "call", payload.SourceRefs); err != nil {
		return generationMessageResponse{}, status, err
	}
	route, err := ResolveGenerationRoute(payload)
	if err != nil {
		return generationMessageResponse{}, http.StatusBadRequest, err
	}
	payload.Kind = string(route.Kind)
	payload.RouteID = route.ID
	payload.FamilyID = route.FamilyID
	payload.VersionID = route.VersionID
	payload.Provider = route.Provider
	if payload.Model == "" {
		payload.Model = route.Model
	}
	if payload.ModelID == "" {
		payload.ModelID = route.LegacyModelID
	}
	if err := workflow.requireGenerationRouteConfiguredContext(ctx, route); err != nil {
		return generationMessageResponse{}, http.StatusServiceUnavailable, err
	}
	conversation, status, err := workflow.resolveGenerationConversationWithScopeFilter(payload.ConversationID, payload.ScopeID, payload.Kind, hasScopeFilter)
	if err != nil {
		return generationMessageResponse{}, status, err
	}
	payload.ConversationID = conversation.ID
	if payload.ProjectID == "" {
		payload.ProjectID = GenerationProjectIDFromScopeID(conversation.ScopeID)
	}
	projectID := payload.ProjectID
	if payload.ProjectName == "" {
		payload.ProjectName = workflow.generationProjectName(projectID)
	}
	workflow.appendStudioUserTranscript(conversation, payload)

	referenceURLs, err := workflow.resolveGenerationReferences(route, payload)
	if err != nil {
		return generationMessageResponse{}, http.StatusBadRequest, err
	}
	payload.Model = generationModelForReferences(route, payload.Model, referenceURLs)

	generationRequest := GenerationRequestFromMessage(payload, route, referenceURLs)
	generationRequest.Prompt = workflow.providerPromptForGeneration(route, payload)
	if err := coregeneration.ValidateRequestForRoute(generationRequest, route); err != nil {
		return generationMessageResponse{}, http.StatusBadRequest, err
	}
	provider, err := workflow.newGenerationProviderContext(ctx, route)
	if err != nil {
		return generationMessageResponse{}, http.StatusServiceUnavailable, err
	}
	if ShouldSubmitGenerationInBackground(route) {
		messageResponse := SubmittingGenerationResponse("", coregeneration.Kind(payload.Kind))
		shouldSubmit := true
		var task GenerationTaskRecord
		if shouldQueueJimengSeedanceSubmission(route) {
			workflow.jimengSeedanceQueueMu.Lock()
			queueBlocked, queueErr := workflow.jimengSeedanceSubmissionQueueBlocked("")
			if queueErr != nil {
				workflow.jimengSeedanceQueueMu.Unlock()
				return generationMessageResponse{}, http.StatusInternalServerError, queueErr
			}
			if queueBlocked {
				messageResponse = QueuedGenerationResponse("", coregeneration.Kind(payload.Kind))
				shouldSubmit = false
			}
			task = GenerationTaskFromMessage(payload, route, messageResponse)
			if err := workflow.generationTasks.Upsert(task); err != nil {
				workflow.jimengSeedanceQueueMu.Unlock()
				return generationMessageResponse{}, http.StatusInternalServerError, err
			}
			workflow.jimengSeedanceQueueMu.Unlock()
		} else {
			task = GenerationTaskFromMessage(payload, route, messageResponse)
			if err := workflow.generationTasks.Upsert(task); err != nil {
				return generationMessageResponse{}, http.StatusInternalServerError, err
			}
		}
		workflow.trackGenerationNotificationTarget(task, payload.NotificationTarget)
		workflow.syncGenerationNotificationTask(task)
		_ = workflow.generationTasks.RecordAttempt(task.ID, "create", messageResponse.Status, messageResponse.Message, nil)
		if shouldSubmit {
			go workflow.submitPendingGeneration(context.Background(), task, provider, generationRequest, "create", projectID, payload.ConversationID)
		}
		return messageResponse, http.StatusOK, nil
	}
	if ShouldRunGenerationInBackground(route) {
		messageResponse := SubmittedGenerationResponse("", coregeneration.Kind(payload.Kind))
		task := GenerationTaskFromMessage(payload, route, messageResponse)
		if err := workflow.generationTasks.Upsert(task); err != nil {
			return generationMessageResponse{}, http.StatusInternalServerError, err
		}
		workflow.trackGenerationNotificationTarget(task, payload.NotificationTarget)
		workflow.syncGenerationNotificationTask(task)
		_ = workflow.generationTasks.RecordAttempt(task.ID, "create", messageResponse.Status, messageResponse.Message, nil)
		workflow.launchSubmittedGeneration(task, provider, generationRequest, "create", projectID, payload.ConversationID)
		return messageResponse, http.StatusOK, nil
	}

	runCtx, cancel := context.WithTimeout(ctx, generationRequestTimeout)
	defer cancel()

	response, err := workflow.generateWithProvider(
		runCtx,
		provider,
		generationRequest,
		generationProviderLogContext{Action: "create"},
	)
	if err != nil {
		messageResponse := FailedGenerationResponse("", err)
		workflow.appendStudioAssistantTranscript(conversation, messageResponse)
		if ShouldPersistGenerationTask(route) {
			task := GenerationTaskFromMessage(payload, route, messageResponse)
			if saveErr := workflow.generationTasks.Upsert(task); saveErr != nil {
				messageResponse.Message = AppendStorageWarning(messageResponse.Message, saveErr)
			} else {
				workflow.trackGenerationNotificationTarget(task, payload.NotificationTarget)
				workflow.syncGenerationNotificationTask(task)
				_ = workflow.generationTasks.RecordAttempt(task.ID, "create", messageResponse.Status, messageResponse.Message, err)
			}
		}
		return messageResponse, http.StatusOK, nil
	}
	response, assetClaims := workflow.cacheGenerationResponseAssetsWithOptionsClaimed(ctx, response, generationMediaSaveOptionsWithTitle(projectID, payload.ConversationID, payload.SectionID, payload.AssetTitle))

	messageResponse := generationResponseWithAssetTitle(GenerationResponseFromCore(response, payload.Kind), payload.AssetTitle)
	persistedAssets := !ShouldPersistGenerationTask(route)
	if ShouldPersistGenerationTask(route) {
		task := GenerationTaskFromMessage(payload, route, messageResponse)
		// A synchronous completed task has no persisted notification yet. Suppress
		// the untracked-task listener when a target is about to be registered;
		// SyncTask below will publish the single richer completion event.
		var persistErr error
		if payload.NotificationTarget != nil && isCompletedGenerationTaskStatus(task.Status) {
			persistErr = workflow.generationTasks.UpsertWithoutCompletionListener(task)
		} else {
			persistErr = workflow.generationTasks.Upsert(task)
		}
		if persistErr != nil {
			messageResponse.Message = AppendStorageWarning(messageResponse.Message, persistErr)
		} else {
			persistedAssets = true
			messageResponse.Assets = generationAssetsWithTaskSlots(task.ID, task.Assets)
			workflow.trackGenerationNotificationTarget(task, payload.NotificationTarget)
			workflow.syncGenerationNotificationTask(task)
			_ = workflow.generationTasks.RecordAttempt(task.ID, "create", messageResponse.Status, messageResponse.Message, nil)
		}
	}
	workflow.finalizeGenerationAssetClaims(assetClaims, persistedAssets)
	workflow.appendStudioAssistantTranscript(conversation, messageResponse)

	return messageResponse, http.StatusOK, nil
}

func (workflow *GenerationService) trackGenerationNotificationTarget(task GenerationTaskRecord, target *GenerationNotificationTarget) {
	if workflow.generationNotifications == nil || target == nil {
		return
	}
	// Notification persistence must not make generation fail.
	// The task itself remains the source of truth for generated media.
	_ = workflow.generationNotifications.TrackTaskTarget(task, target)
}

func (workflow *GenerationService) syncGenerationNotificationTask(task GenerationTaskRecord) {
	if workflow.generationNotifications == nil {
		return
	}
	workflow.generationNotifications.SyncTask(task)
}
