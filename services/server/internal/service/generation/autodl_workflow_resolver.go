package generation

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/comfyui"
	settingsservice "github.com/mediago-dev/mediago-drama/services/server/internal/service/settings"
)

const generationAutoDLWorkflowSnapshotOption = "_mediago_autodl_workflow_snapshot"

// AutoDLWorkflowResolverStore is the narrow registry surface needed by
// generation. It keeps workflow administration out of the execution path.
type AutoDLWorkflowResolverStore interface {
	ResolveAutoDLWorkflow(context.Context, settingsservice.AutoDLWorkflowResolveRequest) (settingsservice.ResolvedAutoDLWorkflow, error)
}

// AutoDLWorkflowResolver resolves one immutable workflow version before any
// instance is scheduled or any ComfyUI prompt is submitted.
type AutoDLWorkflowResolver interface {
	Resolve(context.Context, coregeneration.Request) (settingsservice.ResolvedAutoDLWorkflow, error)
	ResolveVersion(context.Context, string, string) (settingsservice.ResolvedAutoDLWorkflow, error)
}

type autoDLWorkflowResolver struct {
	store AutoDLWorkflowResolverStore
}

func NewAutoDLWorkflowResolver(store AutoDLWorkflowResolverStore) AutoDLWorkflowResolver {
	return &autoDLWorkflowResolver{store: store}
}

func (resolver *autoDLWorkflowResolver) Resolve(ctx context.Context, request coregeneration.Request) (settingsservice.ResolvedAutoDLWorkflow, error) {
	if resolver == nil || resolver.store == nil {
		return settingsservice.ResolvedAutoDLWorkflow{}, fmt.Errorf("AutoDL workflow registry is unavailable")
	}
	if ctx == nil || ctx.Err() != nil {
		return settingsservice.ResolvedAutoDLWorkflow{}, fmt.Errorf("AutoDL workflow resolution was canceled")
	}
	if strings.TrimSpace(request.RouteID) != coregeneration.RouteAutoDLImage {
		return settingsservice.ResolvedAutoDLWorkflow{}, fmt.Errorf("generation route %q does not use a configurable AutoDL image workflow", request.RouteID)
	}
	resolved, err := resolver.store.ResolveAutoDLWorkflow(ctx, settingsservice.AutoDLWorkflowResolveRequest{
		WorkflowProfileID: strings.TrimSpace(request.WorkflowProfileID),
		ReferenceCount:    len(request.ReferenceURLs),
		ForNewTask:        true,
	})
	if err != nil {
		return settingsservice.ResolvedAutoDLWorkflow{}, err
	}
	return cloneResolvedAutoDLWorkflow(resolved), nil
}

func (resolver *autoDLWorkflowResolver) ResolveVersion(ctx context.Context, profileID string, versionID string) (settingsservice.ResolvedAutoDLWorkflow, error) {
	if resolver == nil || resolver.store == nil {
		return settingsservice.ResolvedAutoDLWorkflow{}, fmt.Errorf("AutoDL workflow registry is unavailable")
	}
	if ctx == nil || ctx.Err() != nil || strings.TrimSpace(profileID) == "" || strings.TrimSpace(versionID) == "" {
		return settingsservice.ResolvedAutoDLWorkflow{}, fmt.Errorf("AutoDL workflow version identity is invalid")
	}
	resolved, err := resolver.store.ResolveAutoDLWorkflow(ctx, settingsservice.AutoDLWorkflowResolveRequest{
		WorkflowProfileID: strings.TrimSpace(profileID),
		VersionID:         strings.TrimSpace(versionID),
	})
	if err != nil {
		return settingsservice.ResolvedAutoDLWorkflow{}, err
	}
	return cloneResolvedAutoDLWorkflow(resolved), nil
}

func cloneResolvedAutoDLWorkflow(source settingsservice.ResolvedAutoDLWorkflow) settingsservice.ResolvedAutoDLWorkflow {
	cloned := source
	cloned.UIWorkflow = bytes.Clone(source.UIWorkflow)
	cloned.APITemplate = bytes.Clone(source.APITemplate)
	cloned.Bindings = cloneAutoDLWorkflowBindings(source.Bindings)
	cloned.References.Slots = append([]comfyui.ReferenceBinding(nil), source.References.Slots...)
	return cloned
}

func cloneAutoDLWorkflowBindings(source comfyui.WorkflowBindings) comfyui.WorkflowBindings {
	cloned := source
	cloned.Prompts = append([]comfyui.WorkflowTarget(nil), source.Prompts...)
	cloned.References = append([]comfyui.ReferenceBinding(nil), source.References...)
	cloned.Outputs = append([]comfyui.OutputBinding(nil), source.Outputs...)
	cloned.Parameters = append([]comfyui.ParameterBinding(nil), source.Parameters...)
	if source.Negative != nil {
		negative := *source.Negative
		cloned.Negative = &negative
	}
	return cloned
}

func autoDLWorkflowSnapshotFromOptions(options map[string]any) (settingsservice.ResolvedAutoDLWorkflow, bool) {
	resolved, ok := options[generationAutoDLWorkflowSnapshotOption].(settingsservice.ResolvedAutoDLWorkflow)
	if !ok || resolved.ProfileID == "" || resolved.VersionID == "" {
		return settingsservice.ResolvedAutoDLWorkflow{}, false
	}
	return cloneResolvedAutoDLWorkflow(resolved), true
}

func (workflow *GenerationService) resolveAutoDLWorkflowForNewTask(ctx context.Context, request coregeneration.Request) (settingsservice.ResolvedAutoDLWorkflow, error) {
	if workflow == nil || workflow.autoDLWorkflowResolver == nil {
		return settingsservice.ResolvedAutoDLWorkflow{}, fmt.Errorf("AutoDL workflow registry is unavailable")
	}
	return workflow.autoDLWorkflowResolver.Resolve(ctx, request)
}
