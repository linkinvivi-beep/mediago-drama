package generation

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"regexp"
	"strconv"
	"strings"
	"time"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/comfyui"
)

const (
	autoDLImageTaskIDRequestOption       = "_medialink_autodl_task_id"
	maxAutoDLImageReferences            = 8
	maxAutoDLImageReferenceBytes  int64 = 32 << 20
	maxAutoDLImageReferenceTotal  int64 = 128 << 20
)

var safeAutoDLImageTaskID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// AutoDLImageProvider runs immutable, user-registered ComfyUI image workflows.
// The base provider starts new tasks; Get is enabled only on a task-scoped copy
// returned by ForRuntimeState.
type AutoDLImageProvider struct {
	resolver    AutoDLWorkflowResolver
	scheduler   InstanceScheduler
	client      func(string) (comfyui.Client, error)
	resumeState GenerationTaskRuntimeState
}

func NewAutoDLImageProvider(
	resolver AutoDLWorkflowResolver,
	scheduler InstanceScheduler,
	clientFactory func(string) (comfyui.Client, error),
) *AutoDLImageProvider {
	return &AutoDLImageProvider{resolver: resolver, scheduler: scheduler, client: clientFactory}
}

func (provider *AutoDLImageProvider) Name() string { return coregeneration.ProviderAutoDL }

func (provider *AutoDLImageProvider) ForRuntimeState(state GenerationTaskRuntimeState) coregeneration.Provider {
	if provider == nil {
		return (*AutoDLImageProvider)(nil)
	}
	cloned := *provider
	cloned.resumeState = state
	return &cloned
}

func (provider *AutoDLImageProvider) Generate(ctx context.Context, request coregeneration.Request) (coregeneration.Response, error) {
	if err := provider.validate(); err != nil {
		return coregeneration.Response{}, err
	}
	if request.RouteID != coregeneration.RouteAutoDLImage || len(request.ReferenceURLs) > maxAutoDLImageReferences {
		return coregeneration.Response{}, fmt.Errorf("invalid AutoDL image generation request")
	}
	taskID, err := autoDLImageTaskID(request)
	if err != nil {
		return coregeneration.Response{}, err
	}
	resolved, ok := autoDLWorkflowSnapshotFromOptions(request.Options)
	if !ok {
		resolved, err = provider.resolver.Resolve(ctx, request)
		if err != nil {
			return coregeneration.Response{}, err
		}
	}
	lease, err := provider.scheduler.AcquireNew(ctx, InstanceRequest{
		TaskID: taskID, WorkflowProfileID: resolved.ProfileID, WorkflowVersionID: resolved.VersionID,
		SelectedInstanceProfileID: strings.TrimSpace(request.InstanceProfileID),
	})
	if err != nil {
		return coregeneration.Response{}, err
	}
	releaseBeforeSubmit := true
	defer func() {
		if releaseBeforeSubmit {
			lease.ReleaseBeforeSubmit()
		}
	}()
	state := GenerationTaskRuntimeState{
		InstanceProfileID: lease.InstanceProfileID(), WorkflowProfileID: resolved.ProfileID,
		WorkflowProfileVersion: resolved.VersionID, WorkflowDigest: resolved.WorkflowDigest,
		APITemplateDigest: resolved.APITemplateDigest, AutoDLSubmissionState: "pre_submit",
	}
	emitAutoDLImageProgress(ctx, request, "running", "", state)
	client, err := provider.client(lease.Tunnel().BaseURL)
	if err != nil {
		return coregeneration.Response{}, err
	}
	uploaded, err := uploadAutoDLImageReferences(ctx, client, taskID, request.ReferenceURLs)
	if err != nil {
		return coregeneration.Response{}, err
	}
	instantiated, err := comfyui.InstantiateWorkflow(resolved.APITemplate, resolved.Bindings, autoDLWorkflowInputs(request, uploaded))
	if err != nil {
		return coregeneration.Response{}, err
	}
	submission, err := client.SubmitPrompt(ctx, instantiated, taskID)
	if err != nil {
		if errors.Is(err, comfyui.ErrSubmissionOutcomeUnknown) {
			state.AutoDLSubmissionState = "outcome_unknown"
			emitAutoDLImageProgress(ctx, request, "running", "", state)
			releaseBeforeSubmit = false
			_ = lease.Quarantine("submission_outcome_unknown")
		}
		return coregeneration.Response{}, err
	}
	if len(submission.NodeErrors) > 0 || strings.TrimSpace(submission.PromptID) == "" {
		return coregeneration.Response{}, fmt.Errorf("ComfyUI rejected the workflow inputs")
	}
	if err := lease.BindPrompt(submission.PromptID); err != nil {
		releaseBeforeSubmit = false
		_ = lease.Quarantine("prompt_checkpoint_failed")
		return coregeneration.Response{}, err
	}
	releaseBeforeSubmit = false
	state.ComfyPromptID = submission.PromptID
	state.SubmittedAt = time.Now().UTC().Format(time.RFC3339Nano)
	state.AutoDLSubmissionState = "accepted"
	providerTaskID, err := encodeAutoDLImageProviderTaskID(state.InstanceProfileID, state.ComfyPromptID)
	if err != nil {
		_ = lease.Quarantine("provider_task_id_failed")
		return coregeneration.Response{}, err
	}
	response := autoDLImageProgressResponse(request.Model, providerTaskID, "submitted", state)
	emitAutoDLImageProgress(ctx, request, response.Status, response.ID, state)
	return response, nil
}

func (provider *AutoDLImageProvider) Get(ctx context.Context, id string) (coregeneration.Response, error) {
	if err := provider.validate(); err != nil {
		return coregeneration.Response{}, err
	}
	state := provider.resumeState
	if state.InstanceProfileID == "" || state.WorkflowProfileID == "" || state.WorkflowProfileVersion == "" || state.ComfyPromptID == "" {
		return coregeneration.Response{}, fmt.Errorf("AutoDL image provider is not bound to a persisted task")
	}
	instanceID, promptID, err := parseAutoDLImageProviderTaskID(id)
	if err != nil || instanceID != state.InstanceProfileID || promptID != state.ComfyPromptID {
		return coregeneration.Response{}, fmt.Errorf("AutoDL image task identity does not match its checkpoint")
	}
	resolved, err := provider.resolver.ResolveVersion(ctx, state.WorkflowProfileID, state.WorkflowProfileVersion)
	if err != nil {
		return coregeneration.Response{}, err
	}
	if resolved.WorkflowDigest != state.WorkflowDigest || resolved.APITemplateDigest != state.APITemplateDigest {
		return coregeneration.Response{}, fmt.Errorf("AutoDL image workflow snapshot digest mismatch")
	}
	lease, err := provider.scheduler.Resume(ctx, taskIDForAutoDLResume(state), state.InstanceProfileID, state.ComfyPromptID)
	if err != nil {
		return coregeneration.Response{}, err
	}
	client, err := provider.client(lease.Tunnel().BaseURL)
	if err != nil {
		return coregeneration.Response{}, err
	}
	history, err := client.History(ctx, state.ComfyPromptID)
	if err != nil {
		return coregeneration.Response{}, err
	}
	if autoDLHistoryFailed(history.Status.StatusString) {
		lease.ReleaseTerminal()
		return coregeneration.Response{}, fmt.Errorf("ComfyUI image workflow failed")
	}
	if !history.Status.Completed {
		return autoDLImageProgressResponse("", id, "submitted", state), nil
	}
	assets, err := downloadAutoDLImageOutputs(ctx, client, history, resolved.Bindings)
	if err != nil {
		return coregeneration.Response{}, err
	}
	lease.ReleaseTerminal()
	return coregeneration.Response{
		ID: id, Status: "completed", Assets: assets,
		Metadata: map[string]any{"runtime_state": state},
	}, nil
}

func (provider *AutoDLImageProvider) validate() error {
	if provider == nil || provider.resolver == nil || provider.scheduler == nil || provider.client == nil {
		return fmt.Errorf("AutoDL image provider is not configured")
	}
	return nil
}

func requestWithAutoDLImageTaskID(request coregeneration.Request, taskID string) coregeneration.Request {
	next := make(map[string]any, len(request.Options)+1)
	for key, value := range request.Options {
		next[key] = value
	}
	next[autoDLImageTaskIDRequestOption] = strings.TrimSpace(taskID)
	request.Options = next
	return request
}

func autoDLImageTaskID(request coregeneration.Request) (string, error) {
	value, _ := request.Options[autoDLImageTaskIDRequestOption].(string)
	value = strings.TrimSpace(value)
	if !safeAutoDLImageTaskID.MatchString(value) || value == "." || value == ".." {
		return "", fmt.Errorf("AutoDL image task ID is missing or unsafe")
	}
	return value, nil
}

func emitAutoDLImageProgress(ctx context.Context, request coregeneration.Request, status string, id string, state GenerationTaskRuntimeState) {
	callback, ok := coregeneration.ProgressCallbackFromOptions(request.Options)
	if !ok {
		return
	}
	callback(ctx, coregeneration.ProgressEvent{Response: autoDLImageProgressResponse(request.Model, id, status, state)})
}

func autoDLImageProgressResponse(model string, id string, status string, state GenerationTaskRuntimeState) coregeneration.Response {
	return coregeneration.Response{
		ID: id, Model: model, Status: status,
		Metadata: map[string]any{"runtime_state": state},
	}
}

func uploadAutoDLImageReferences(ctx context.Context, client comfyui.Client, taskID string, references []string) ([]comfyui.UploadedReference, error) {
	uploaded := make([]comfyui.UploadedReference, 0, len(references))
	total := int64(0)
	for index, value := range references {
		payload, extension, err := decodeAutoDLImageReference(value, index)
		if err != nil {
			return nil, err
		}
		total += int64(len(payload))
		if total > maxAutoDLImageReferenceTotal {
			return nil, fmt.Errorf("AutoDL image references exceed %d bytes total", maxAutoDLImageReferenceTotal)
		}
		result, err := client.UploadImage(ctx, comfyui.UploadImageRequest{
			Filename: fmt.Sprintf("reference-%02d.%s", index+1, extension),
			Content: io.NopCloser(bytes.NewReader(payload)), Size: int64(len(payload)),
			Type: "input", Subfolder: "medialink/" + taskID, Overwrite: false,
		})
		if err != nil {
			return nil, err
		}
		uploaded = append(uploaded, comfyui.UploadedReference{Name: result.Name, Subfolder: result.Subfolder, Type: result.Type})
	}
	return uploaded, nil
}

func decodeAutoDLImageReference(value string, index int) ([]byte, string, error) {
	prefix, encoded, ok := strings.Cut(strings.TrimSpace(value), ",")
	if !ok || !strings.HasPrefix(strings.ToLower(prefix), "data:image/") || !strings.HasSuffix(strings.ToLower(prefix), ";base64") {
		return nil, "", fmt.Errorf("AutoDL image reference %d must be an inline image", index+1)
	}
	mediaTypeValue := prefix[len("data:") : len(prefix)-len(";base64")]
	mimeType, _, err := mime.ParseMediaType(mediaTypeValue)
	if err != nil || !allowedAutoDLImageMIMEType(mimeType) {
		return nil, "", fmt.Errorf("AutoDL image reference %d has an unsupported MIME type", index+1)
	}
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
	payload, err := io.ReadAll(io.LimitReader(decoder, maxAutoDLImageReferenceBytes+1))
	if err != nil || int64(len(payload)) > maxAutoDLImageReferenceBytes {
		return nil, "", fmt.Errorf("AutoDL image reference %d exceeds %d bytes or is invalid", index+1, maxAutoDLImageReferenceBytes)
	}
	configuration, detectedFormat, err := image.DecodeConfig(bytes.NewReader(payload))
	if err != nil || configuration.Width <= 0 || configuration.Height <= 0 ||
		configuration.Width > maxAutoDLImageDimension || configuration.Height > maxAutoDLImageDimension ||
		int64(configuration.Width)*int64(configuration.Height) > maxAutoDLImagePixels ||
		!autoDLImageFormatMatchesMIMEType(detectedFormat, mimeType) {
		return nil, "", fmt.Errorf("AutoDL image reference %d is corrupt", index+1)
	}
	if _, _, err := image.Decode(bytes.NewReader(payload)); err != nil {
		return nil, "", fmt.Errorf("AutoDL image reference %d is corrupt", index+1)
	}
	extension := "png"
	if mimeType == "image/jpeg" {
		extension = "jpg"
	} else if mimeType == "image/gif" {
		extension = "gif"
	}
	return payload, extension, nil
}

func autoDLWorkflowInputs(request coregeneration.Request, references []comfyui.UploadedReference) comfyui.WorkflowInputs {
	inputs := comfyui.WorkflowInputs{Prompts: []string{request.Prompt}, References: references, Parameters: make(map[string]any)}
	for key, value := range request.Params {
		inputs.Parameters[key] = value
	}
	if value, ok := autoDLInt64Param(request.Params, "seed"); ok {
		inputs.Seed = &value
	}
	if value, ok := autoDLIntParam(request.Params, "width"); ok {
		inputs.Width = &value
	}
	if value, ok := autoDLIntParam(request.Params, "height"); ok {
		inputs.Height = &value
	}
	return inputs
}

func autoDLInt64Param(params map[string]any, key string) (int64, bool) {
	value, ok := params[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		return int64(typed), typed == float64(int64(typed))
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func autoDLIntParam(params map[string]any, key string) (int, bool) {
	value, ok := autoDLInt64Param(params, key)
	if !ok || value <= 0 || value > maxAutoDLImageDimension {
		return 0, false
	}
	return int(value), true
}

func taskIDForAutoDLResume(state GenerationTaskRuntimeState) string {
	// The scheduler reservation is restored under the local generation task ID.
	// The stored-task provider factory adds it transiently; it is never serialized
	// into runtime state or exposed to the client.
	return strings.TrimSpace(state.LocalTaskID)
}

func autoDLHistoryFailed(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "error", "failed", "failure":
		return true
	default:
		return false
	}
}

func downloadAutoDLImageOutputs(ctx context.Context, client comfyui.Client, history comfyui.PromptHistory, bindings comfyui.WorkflowBindings) ([]coregeneration.Asset, error) {
	assets := make([]coregeneration.Asset, 0, len(bindings.Outputs))
	for _, binding := range bindings.Outputs {
		output, ok := history.Outputs[binding.NodeID]
		if !ok || len(output.Images) == 0 {
			return nil, fmt.Errorf("ComfyUI history is missing configured image output node %s", binding.NodeID)
		}
		index := binding.OutputIndex
		if index < 0 || index >= len(output.Images) {
			return nil, fmt.Errorf("ComfyUI history image output index is invalid")
		}
		body, headers, err := client.View(ctx, output.Images[index])
		if err != nil {
			return nil, err
		}
		encoded, mimeType, err := readValidatedAutoDLImage(body, headers)
		if err != nil {
			return nil, err
		}
		assets = append(assets, coregeneration.Asset{Kind: coregeneration.KindImage, Base64: encoded, MIMEType: mimeType})
	}
	if len(assets) == 0 {
		return nil, fmt.Errorf("ComfyUI image workflow returned no configured outputs")
	}
	return assets, nil
}
