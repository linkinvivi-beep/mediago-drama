package generation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/services/server/internal/service/textcompletion"
)

// StreamGenerationText streams a text generation request and persists the final task.
func (workflow *GenerationService) StreamGenerationText(
	ctx context.Context,
	payload GenerationMessageRequest,
	emit func(GenerationTextStreamEvent) error,
) (int, error) {
	if emit == nil {
		return http.StatusInternalServerError, fmt.Errorf("stream emitter is required")
	}

	payload.Kind = string(coregeneration.KindText)
	payload.ConversationID = strings.TrimSpace(payload.ConversationID)
	hasScopeFilter := strings.TrimSpace(payload.ScopeID) != ""
	payload.ScopeID = NormalizeGenerationConversationScopeID(payload.ScopeID)
	payload.ProjectID = GenerationProjectIDForRequest(payload.ProjectID, "")
	payload.Prompt = strings.TrimSpace(payload.Prompt)
	payload.RouteID = strings.TrimSpace(payload.RouteID)
	payload.FamilyID = strings.TrimSpace(payload.FamilyID)
	payload.VersionID = strings.TrimSpace(payload.VersionID)
	payload.Provider = strings.TrimSpace(payload.Provider)
	payload.TextExecutor = strings.ToLower(strings.TrimSpace(payload.TextExecutor))
	payload.ModelID = strings.TrimSpace(payload.ModelID)
	payload.Model = strings.TrimSpace(payload.Model)
	switch textcompletion.ExecutorType(payload.TextExecutor) {
	case "", textcompletion.ExecutorAuto, textcompletion.ExecutorRoute, textcompletion.ExecutorCodex:
	default:
		return http.StatusBadRequest, fmt.Errorf("unknown text executor %q", payload.TextExecutor)
	}
	optimization, status, err := workflow.prepareTextPromptOptimization(ctx, &payload)
	if err != nil {
		return status, err
	}
	payload.ReferenceURLs = []string{}
	payload.ReferenceAssetIDs = []string{}
	var sourceRefsErr error
	payload.SourceRefs, sourceRefsErr = normalizeContentSourceRefs(payload.SourceRefs)
	if sourceRefsErr != nil {
		return http.StatusForbidden, sourceRefsErr
	}
	payload.Params = NormalizeGenerationParams(payload.Params)
	if payload.Prompt == "" {
		return http.StatusBadRequest, fmt.Errorf("缺少 prompt")
	}
	if status, err := workflow.authorizeContentUse(ctx, "call", payload.SourceRefs); err != nil {
		return status, err
	}
	if workflow.shouldUseExecutorText(payload) {
		return workflow.streamGenerationTextWithExecutor(ctx, payload, hasScopeFilter, optimization, emit)
	}

	route, err := ResolveGenerationRoute(payload)
	if err != nil {
		return http.StatusBadRequest, err
	}
	if route.Kind != coregeneration.KindText {
		return http.StatusBadRequest, fmt.Errorf("generation route is not a text route")
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
	if err := workflow.requireGenerationRouteConfigured(route); err != nil {
		return http.StatusServiceUnavailable, err
	}

	conversation, status, err := workflow.resolveGenerationConversationWithScopeFilter(payload.ConversationID, payload.ScopeID, payload.Kind, hasScopeFilter)
	if err != nil {
		return status, err
	}
	payload.ConversationID = conversation.ID
	if payload.ProjectID == "" {
		payload.ProjectID = GenerationProjectIDFromScopeID(conversation.ScopeID)
	}
	if payload.ProjectName == "" {
		payload.ProjectName = workflow.generationProjectName(payload.ProjectID)
	}
	workflow.appendStudioUserTranscript(conversation, payload)

	provider, err := workflow.newGenerationProvider(route)
	if err != nil {
		return http.StatusServiceUnavailable, err
	}

	taskID, err := workflow.generationTasks.idGenerator("generation")
	if err != nil {
		return http.StatusInternalServerError, err
	}
	initialResponse := GenerationMessageResponse{
		ID:      taskID,
		Role:    "assistant",
		Status:  "running",
		Message: "文本生成中...",
		Assets:  []GenerationAsset{},
		Usage:   GenerationUsage{},
	}
	task := GenerationTaskFromMessage(payload, route, initialResponse)
	if err := workflow.generationTasks.Upsert(task); err != nil {
		return http.StatusInternalServerError, err
	}
	_ = workflow.generationTasks.RecordAttempt(task.ID, "create", task.Status, task.Message, nil)

	if err := emit(GenerationTextStreamEvent{
		Type:           "start",
		TaskID:         task.ID,
		ConversationID: conversation.ID,
		Status:         task.Status,
		Message:        &initialResponse,
	}); err != nil {
		return http.StatusOK, err
	}

	runCtx, cancel := context.WithTimeout(ctx, generationRequestTimeout)
	defer cancel()

	generationRequest := GenerationRequestFromMessage(payload, route, nil)
	generationRequest.Prompt = optimization.Prompt
	streamProvider, ok := provider.(coregeneration.TextStreamProvider)
	if !ok {
		return workflow.generateGenerationTextWithoutStream(
			runCtx,
			provider,
			generationRequest,
			task,
			conversation,
			optimization,
			emit,
		)
	}
	stream, err := streamProvider.GenerateTextStream(runCtx, generationRequest)
	if err != nil {
		if errors.Is(err, coregeneration.ErrTextStreamingUnsupported) {
			return workflow.generateGenerationTextWithoutStream(
				runCtx,
				provider,
				generationRequest,
				task,
				conversation,
				optimization,
				emit,
			)
		}
		err = optimization.safeFailure(err)
		message := workflow.persistTextStreamFailure(task, "", err)
		workflow.appendStudioAssistantTranscript(conversation, message)
		return http.StatusOK, emit(GenerationTextStreamEvent{
			Type:           "error",
			TaskID:         task.ID,
			ConversationID: conversation.ID,
			Status:         "failed",
			Error:          err.Error(),
		})
	}
	defer stream.Close()

	var builder strings.Builder
	usage := GenerationUsage{}
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			err = optimization.safeFailure(err)
			partialText := builder.String()
			if optimization.Enabled {
				partialText = ""
			}
			message := workflow.persistTextStreamFailure(task, partialText, err)
			workflow.appendStudioAssistantTranscript(conversation, message)
			return http.StatusOK, emit(GenerationTextStreamEvent{
				Type:           "error",
				TaskID:         task.ID,
				ConversationID: conversation.ID,
				Status:         "failed",
				Error:          err.Error(),
			})
		}
		if event.Usage != nil {
			usage = GenerationUsage{
				InputTokens:     event.Usage.InputTokens,
				OutputTokens:    event.Usage.OutputTokens,
				TotalTokens:     event.Usage.TotalTokens,
				ReasoningTokens: event.Usage.ReasoningTokens,
				CachedTokens:    event.Usage.CachedTokens,
			}
		}
		if event.Delta != "" {
			if optimization.Enabled && len(event.Delta) > maxPromptOptimizationOutputBytes-builder.Len() {
				return workflow.finishPromptOptimizationFailure(task, conversation, emit)
			}
			builder.WriteString(event.Delta)
			if !optimization.Enabled {
				if err := emit(GenerationTextStreamEvent{
					Type:           "delta",
					TaskID:         task.ID,
					ConversationID: conversation.ID,
					Delta:          event.Delta,
					Status:         "running",
				}); err != nil {
					return http.StatusOK, err
				}
			}
		}
		if event.Done {
			break
		}
	}

	text := builder.String()
	text, err = optimization.validateOutput(text)
	if err != nil {
		return workflow.finishPromptOptimizationFailure(task, conversation, emit)
	}
	message := "文本生成已完成。"
	if strings.TrimSpace(text) == "" {
		message = "文本生成已完成，但未返回文本。"
	}
	completedResponse := GenerationMessageResponse{
		ID:      task.ID,
		Role:    "assistant",
		Status:  "completed",
		Message: message,
		Text:    text,
		Assets:  []GenerationAsset{},
		Usage:   usage,
	}
	return workflow.finishGenerationTextTask(task, conversation, completedResponse, emit)
}

func (workflow *GenerationService) shouldUseExecutorText(payload GenerationMessageRequest) bool {
	switch textcompletion.ExecutorType(payload.TextExecutor) {
	case textcompletion.ExecutorCodex:
		return true
	case textcompletion.ExecutorRoute:
		return false
	case "", textcompletion.ExecutorAuto:
		if workflow == nil || workflow.textCompletion == nil {
			return false
		}
		_, err := workflow.resolveConfiguredTextRoute(payload.RouteID)
		return err != nil
	default:
		return false
	}
}

func (workflow *GenerationService) streamGenerationTextWithExecutor(
	ctx context.Context,
	payload GenerationMessageRequest,
	hasScopeFilter bool,
	optimization promptOptimizationExecution,
	emit func(GenerationTextStreamEvent) error,
) (int, error) {
	executor := textcompletion.ExecutorType(payload.TextExecutor)
	if executor == "" {
		executor = textcompletion.ExecutorAuto
	}
	if executor != textcompletion.ExecutorAuto && executor != textcompletion.ExecutorCodex {
		return http.StatusBadRequest, fmt.Errorf("unknown text executor %q", payload.TextExecutor)
	}

	conversation, status, err := workflow.resolveGenerationConversationWithScopeFilter(
		payload.ConversationID,
		payload.ScopeID,
		string(coregeneration.KindText),
		hasScopeFilter,
	)
	if err != nil {
		return status, err
	}
	payload.ConversationID = conversation.ID
	if payload.ProjectID == "" {
		payload.ProjectID = GenerationProjectIDFromScopeID(conversation.ScopeID)
	}
	if payload.ProjectName == "" {
		payload.ProjectName = workflow.generationProjectName(payload.ProjectID)
	}
	workflow.appendStudioUserTranscript(conversation, payload)

	taskID, err := workflow.generationTasks.idGenerator("generation")
	if err != nil {
		return http.StatusInternalServerError, err
	}
	initialResponse := GenerationMessageResponse{
		ID:      taskID,
		Role:    "assistant",
		Status:  "running",
		Message: "Codex 文本生成中...",
		Assets:  []GenerationAsset{},
		Usage:   GenerationUsage{},
	}
	requestModel := strings.TrimSpace(payload.Model)
	displayModel := firstNonEmpty(requestModel, "codex")
	payload.Model = displayModel
	route := coregeneration.ModelRoute{
		ID:        "codex/text",
		FamilyID:  "codex-text",
		VersionID: "codex-text",
		Label:     "Codex",
		Kind:      coregeneration.KindText,
		Provider:  "codex",
		Model:     displayModel,
		Status:    coregeneration.RouteStatusAvailable,
	}
	task := GenerationTaskFromMessage(payload, route, initialResponse)
	if err := workflow.generationTasks.Upsert(task); err != nil {
		return http.StatusInternalServerError, err
	}
	_ = workflow.generationTasks.RecordAttempt(task.ID, "create", task.Status, task.Message, nil)
	if err := emit(GenerationTextStreamEvent{
		Type:           "start",
		TaskID:         task.ID,
		ConversationID: conversation.ID,
		Status:         task.Status,
		Message:        &initialResponse,
	}); err != nil {
		return http.StatusOK, err
	}

	text, err := workflow.CompleteText(ctx, TextCompletionRequest{
		Prompt:            optimization.Prompt,
		SystemInstruction: stringGenerationParam(payload.Params, "system_instruction"),
		Executor:          executor,
		Model:             requestModel,
		Params:            payload.Params,
	})
	if err != nil {
		err = optimization.safeFailure(err)
		message := workflow.persistTextStreamFailure(task, "", err)
		workflow.appendStudioAssistantTranscript(conversation, message)
		return http.StatusOK, emit(GenerationTextStreamEvent{
			Type:           "error",
			TaskID:         task.ID,
			ConversationID: conversation.ID,
			Status:         "failed",
			Error:          err.Error(),
		})
	}
	text, err = optimization.validateOutput(text)
	if err != nil {
		return workflow.finishPromptOptimizationFailure(task, conversation, emit)
	}
	completedResponse := GenerationMessageResponse{
		ID:      task.ID,
		Role:    "assistant",
		Status:  "completed",
		Message: "文本生成已完成。",
		Text:    text,
		Assets:  []GenerationAsset{},
		Usage:   GenerationUsage{},
	}
	return workflow.finishGenerationTextTask(task, conversation, completedResponse, emit)
}

func stringGenerationParam(params map[string]any, name string) string {
	if params == nil {
		return ""
	}
	value, _ := params[name].(string)
	return strings.TrimSpace(value)
}

func (workflow *GenerationService) prepareTextPromptOptimization(
	ctx context.Context,
	payload *GenerationMessageRequest,
) (promptOptimizationExecution, int, error) {
	if payload == nil || payload.PromptOptimization == nil {
		prompt := ""
		if payload != nil {
			prompt = payload.Prompt
		}
		return promptOptimizationExecution{Prompt: prompt}, http.StatusOK, nil
	}
	protectedBodies := []string{}
	if entry, resolved, err := workflow.resolvePromptReference(ctx, payload.PromptOptimization.ReferenceID, payload.PromptOptimization.ReferencePrompt); err != nil {
		return promptOptimizationExecution{}, http.StatusBadRequest, err
	} else if resolved {
		protectedBodies = append(protectedBodies, entry.Prompt)
	}
	if status, err := workflow.resolveGenerationPromptReferences(ctx, payload); err != nil {
		return promptOptimizationExecution{}, status, err
	}
	optimization := NormalizeGenerationPromptOptimizationRequest(payload.PromptOptimization)
	if err := ValidateGenerationPromptOptimizationRequest(optimization); err != nil {
		return promptOptimizationExecution{}, http.StatusBadRequest, err
	}
	ordered := orderedGenerationReferencesFromParams(payload.Params)
	if len(ordered) == 0 {
		ordered = canonicalOrderedGenerationReferences(*payload)
		if err := validateOrderedGenerationReferences(ordered); err != nil {
			return promptOptimizationExecution{}, http.StatusBadRequest, err
		}
		payload.Params = generationParamsWithOrderedReferences(payload.Params, ordered)
	}
	if err := validatePromptOptimizationInput(optimization, payload.Prompt, ordered, protectedBodies); err != nil {
		return promptOptimizationExecution{}, http.StatusBadRequest, err
	}
	contextDocuments, err := workflow.promptOptimizationContextDocuments(payload)
	if err != nil {
		return promptOptimizationExecution{}, http.StatusBadRequest, err
	}
	executionPrompt, err := promptOptimizationUserPrompt(optimization, payload.Prompt, ordered, contextDocuments)
	if err != nil {
		return promptOptimizationExecution{}, http.StatusBadRequest, err
	}
	execution := promptOptimizationExecution{
		Enabled:         true,
		Prompt:          executionPrompt,
		ProtectedBodies: promptOptimizationSensitiveBodies(protectedBodies, ordered),
	}
	targetKind := coregeneration.Kind(stringGenerationParam(payload.Params, promptOptimizationTargetKindParam))
	if targetKind == "" && strings.Contains(stringGenerationParam(payload.Params, "system_instruction"), "这是图片生成提示词") {
		targetKind = coregeneration.KindImage
	}
	targetRouteID := stringGenerationParam(payload.Params, promptOptimizationTargetRouteParam)
	promptGuide := boundedAutoDLPromptGuide(stringGenerationParam(payload.Params, promptOptimizationWorkflowGuideParam))
	if promptGuide == "" && isAutoDLGenerationRouteID(targetRouteID) {
		resolved, resolveErr := workflow.resolveAutoDLWorkflowForNewTask(ctx, coregeneration.Request{
			RouteID:       targetRouteID,
			ReferenceURLs: make([]string, len(ordered)),
		})
		if resolveErr != nil {
			return promptOptimizationExecution{}, http.StatusServiceUnavailable, resolveErr
		}
		promptGuide = boundedAutoDLPromptGuide(resolved.PromptGuide)
	}
	if targetKind == coregeneration.KindVideo && targetRouteID == coregeneration.RouteAutoDLH3 {
		execution.MaxOutputRunes = maxH3PromptOptimizationOutputRunes
	}
	instruction := promptOptimizationSystemInstructionForTarget(targetKind, targetRouteID, payload.Params, promptGuide)
	params := make(map[string]any, len(payload.Params)+1)
	for key, value := range payload.Params {
		params[key] = value
	}
	delete(params, promptOptimizationTargetKindParam)
	delete(params, promptOptimizationTargetRouteParam)
	delete(params, promptOptimizationWorkflowGuideParam)
	delete(params, promptOptimizationTargetDurationParam)
	delete(params, promptOptimizationTargetAspectRatioParam)
	delete(params, promptOptimizationTargetResolutionParam)
	params["system_instruction"] = instruction
	params[generationSensitivePromptParam] = true
	payload.Params = params
	payload.PromptOptimization = nil
	return execution, http.StatusOK, nil
}

func (workflow *GenerationService) finishPromptOptimizationFailure(
	task GenerationTaskRecord,
	conversation GenerationConversationRecord,
	emit func(GenerationTextStreamEvent) error,
) (int, error) {
	message := workflow.persistTextStreamFailure(task, "", errPromptOptimizationOutputRejected)
	workflow.appendStudioAssistantTranscript(conversation, message)
	return http.StatusOK, emit(GenerationTextStreamEvent{
		Type:           "error",
		TaskID:         task.ID,
		ConversationID: conversation.ID,
		Status:         "failed",
		Error:          errPromptOptimizationOutputRejected.Error(),
	})
}

func (workflow *GenerationService) persistTextStreamFailure(task GenerationTaskRecord, partialText string, failure error) GenerationMessageResponse {
	messageResponse := FailedGenerationResponse(task.ID, failure)
	messageResponse.Text = partialText
	failedTask := GenerationTaskWithMessage(task, messageResponse)
	if err := workflow.generationTasks.Upsert(failedTask); err != nil {
		slog.Error("generation text stream failure could not be saved", "task_id", task.ID, "error", err)
		return messageResponse
	}
	_ = workflow.generationTasks.RecordAttempt(task.ID, "create", messageResponse.Status, messageResponse.Message, failure)
	return messageResponse
}

func (workflow *GenerationService) generateGenerationTextWithoutStream(
	ctx context.Context,
	provider coregeneration.Provider,
	request coregeneration.Request,
	task GenerationTaskRecord,
	conversation GenerationConversationRecord,
	optimization promptOptimizationExecution,
	emit func(GenerationTextStreamEvent) error,
) (int, error) {
	response, err := workflow.generateWithProvider(
		ctx,
		provider,
		request,
		generationProviderLogContext{Action: "create", TaskID: task.ID},
	)
	if err != nil {
		err = optimization.safeFailure(err)
		message := workflow.persistTextStreamFailure(task, "", err)
		workflow.appendStudioAssistantTranscript(conversation, message)
		return http.StatusOK, emit(GenerationTextStreamEvent{
			Type:           "error",
			TaskID:         task.ID,
			ConversationID: conversation.ID,
			Status:         "failed",
			Error:          err.Error(),
		})
	}
	response.Text, err = optimization.validateOutput(response.Text)
	if err != nil {
		return workflow.finishPromptOptimizationFailure(task, conversation, emit)
	}

	response.ID = task.ID
	completedResponse := GenerationResponseFromCore(response, string(coregeneration.KindText))
	completedResponse.ID = task.ID
	return workflow.finishGenerationTextTask(task, conversation, completedResponse, emit)
}

func (workflow *GenerationService) finishGenerationTextTask(
	task GenerationTaskRecord,
	conversation GenerationConversationRecord,
	completedResponse GenerationMessageResponse,
	emit func(GenerationTextStreamEvent) error,
) (int, error) {
	completedTask := GenerationTaskWithMessage(task, completedResponse)
	if err := workflow.generationTasks.Upsert(completedTask); err != nil {
		completedResponse.Message = AppendStorageWarning(completedResponse.Message, err)
	} else {
		_ = workflow.generationTasks.RecordAttempt(task.ID, "create", completedResponse.Status, completedResponse.Message, nil)
	}
	workflow.appendStudioAssistantTranscript(conversation, completedResponse)

	return http.StatusOK, emit(GenerationTextStreamEvent{
		Type:           "done",
		TaskID:         task.ID,
		ConversationID: conversation.ID,
		Status:         completedResponse.Status,
		Message:        &completedResponse,
		Usage:          &completedResponse.Usage,
	})
}
