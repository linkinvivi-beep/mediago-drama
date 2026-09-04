package generation

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/comfyui"
)

const autoDLH3FramesPerSecond = 24

// AutoDLH3Provider runs user-registered MiniMax H3 ComfyUI workflows without
// depending on a fixed graph, node ID, model filename, or remote address.
type AutoDLH3Provider struct {
	resolver    AutoDLWorkflowResolver
	scheduler   InstanceScheduler
	client      func(string) (comfyui.Client, error)
	resumeState GenerationTaskRuntimeState
}

func NewAutoDLH3Provider(
	resolver AutoDLWorkflowResolver,
	scheduler InstanceScheduler,
	clientFactory func(string) (comfyui.Client, error),
) *AutoDLH3Provider {
	return &AutoDLH3Provider{resolver: resolver, scheduler: scheduler, client: clientFactory}
}

func (provider *AutoDLH3Provider) Name() string { return coregeneration.ProviderAutoDL }

func (provider *AutoDLH3Provider) ForRuntimeState(state GenerationTaskRuntimeState) coregeneration.Provider {
	if provider == nil {
		return (*AutoDLH3Provider)(nil)
	}
	cloned := *provider
	cloned.resumeState = state
	return &cloned
}

func (provider *AutoDLH3Provider) Generate(ctx context.Context, request coregeneration.Request) (coregeneration.Response, error) {
	if err := provider.validate(); err != nil {
		return coregeneration.Response{}, err
	}
	if request.RouteID != coregeneration.RouteAutoDLH3 || request.Kind != coregeneration.KindVideo || len(request.ReferenceURLs) > maxAutoDLImageReferences {
		return coregeneration.Response{}, fmt.Errorf("invalid AutoDL H3 generation request")
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
	if resolved.RouteID != coregeneration.RouteAutoDLH3 || resolved.MediaKind != string(coregeneration.KindVideo) {
		return coregeneration.Response{}, fmt.Errorf("selected workflow is not an AutoDL H3 video workflow")
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
	instantiated, err := comfyui.InstantiateWorkflow(resolved.APITemplate, resolved.Bindings, autoDLH3WorkflowInputs(request, uploaded))
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
		return coregeneration.Response{}, fmt.Errorf("ComfyUI rejected the H3 workflow inputs")
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
	providerTaskID, err := encodeAutoDLH3ProviderTaskID(state.ComfyPromptID)
	if err != nil {
		_ = lease.Quarantine("provider_task_id_failed")
		return coregeneration.Response{}, err
	}
	response := autoDLImageProgressResponse(request.Model, providerTaskID, "submitted", state)
	emitAutoDLImageProgress(ctx, request, response.Status, response.ID, state)
	return response, nil
}

func (provider *AutoDLH3Provider) Get(ctx context.Context, id string) (coregeneration.Response, error) {
	if err := provider.validate(); err != nil {
		return coregeneration.Response{}, err
	}
	state := provider.resumeState
	if state.InstanceProfileID == "" || state.WorkflowProfileID == "" || state.WorkflowProfileVersion == "" || state.ComfyPromptID == "" {
		return coregeneration.Response{}, fmt.Errorf("AutoDL H3 provider is not bound to a persisted task")
	}
	if err := validateAutoDLProviderCheckpoint(coregeneration.RouteAutoDLH3, state, id); err != nil {
		return coregeneration.Response{}, err
	}
	resolved, err := provider.resolver.ResolveVersion(ctx, state.WorkflowProfileID, state.WorkflowProfileVersion)
	if err != nil {
		return coregeneration.Response{}, err
	}
	if resolved.RouteID != coregeneration.RouteAutoDLH3 || resolved.WorkflowDigest != state.WorkflowDigest || resolved.APITemplateDigest != state.APITemplateDigest {
		return coregeneration.Response{}, fmt.Errorf("AutoDL H3 workflow snapshot does not match its checkpoint")
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
		return coregeneration.Response{}, fmt.Errorf("ComfyUI H3 workflow failed")
	}
	if !history.Status.Completed {
		return autoDLImageProgressResponse("", id, "submitted", state), nil
	}
	assets, err := downloadAutoDLVideoOutputs(ctx, client, history, resolved.Bindings)
	if err != nil {
		return coregeneration.Response{}, err
	}
	lease.ReleaseTerminal()
	return coregeneration.Response{ID: id, Status: "completed", Assets: assets, Metadata: map[string]any{"runtime_state": state}}, nil
}

func (provider *AutoDLH3Provider) CancelTask(ctx context.Context, task GenerationTaskRecord) error {
	if err := provider.validate(); err != nil {
		return err
	}
	return cancelAutoDLTask(ctx, provider.scheduler, provider.client, task)
}

func (provider *AutoDLH3Provider) validate() error {
	if provider == nil || provider.resolver == nil || provider.scheduler == nil || provider.client == nil {
		return fmt.Errorf("AutoDL H3 provider is not configured")
	}
	return nil
}

func autoDLH3WorkflowInputs(request coregeneration.Request, references []comfyui.UploadedReference) comfyui.WorkflowInputs {
	inputs := autoDLWorkflowInputs(request, references)
	duration, durationOK := autoDLInt64Param(request.Params, string(coregeneration.ParamDuration))
	if durationOK && duration >= 4 && duration <= 15 {
		inputs.Parameters["duration"] = duration
		inputs.Parameters["duration_seconds"] = duration
		frames := autoDLH3FrameCount(int(duration))
		inputs.Parameters["length"] = frames
		inputs.Parameters["frames"] = frames
	}
	width, height, sizeOK := autoDLH3Dimensions(request.Params)
	if sizeOK {
		inputs.Width = &width
		inputs.Height = &height
	}
	inputs.Parameters["fps"] = autoDLH3FramesPerSecond
	return inputs
}

func autoDLH3FrameCount(durationSeconds int) int {
	// MiniMax H3 accepts a 17k+5 frame grid. Ceil keeps the requested duration
	// from being shortened at the workflow's 24 fps output rate.
	steps := int(math.Ceil(float64(durationSeconds*autoDLH3FramesPerSecond-5) / 17.0))
	return steps*17 + 5
}

func autoDLH3Dimensions(params map[string]any) (int, int, bool) {
	ratio := strings.TrimSpace(fmt.Sprint(params[string(coregeneration.ParamAspectRatio)]))
	resolution := strings.ToLower(strings.TrimSpace(fmt.Sprint(params[string(coregeneration.ParamResolution)])))
	switch resolution {
	case "768p":
		switch ratio {
		case "16:9":
			return 1344, 768, true
		case "9:16":
			return 768, 1344, true
		case "1:1":
			return 768, 768, true
		default:
			return 0, 0, false
		}
	case "1080p":
		switch ratio {
		case "16:9":
			return 1920, 1080, true
		case "9:16":
			return 1080, 1920, true
		case "1:1":
			return 1080, 1080, true
		default:
			return 0, 0, false
		}
	default:
		return 0, 0, false
	}
}

func encodeAutoDLH3ProviderTaskID(promptID string) (string, error) {
	promptID = strings.TrimSpace(promptID)
	if promptID == "" || strings.Contains(promptID, ":") {
		return "", fmt.Errorf("AutoDL H3 provider task ID requires a valid prompt ID")
	}
	return coregeneration.RouteAutoDLH3 + ":" + promptID, nil
}
