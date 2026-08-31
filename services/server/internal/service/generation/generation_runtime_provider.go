package generation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/packages/core/pkg/generation/runtime"
)

func (workflow *GenerationService) newGenerationProvider(route coregeneration.ModelRoute) (coregeneration.Provider, error) {
	return workflow.newGenerationProviderContext(context.Background(), route)
}

func (workflow *GenerationService) newGenerationProviderContext(ctx context.Context, route coregeneration.ModelRoute) (coregeneration.Provider, error) {
	if route.Status != coregeneration.RouteStatusAvailable {
		return nil, errors.New(route.StatusReason)
	}
	if err := workflow.requireGenerationRouteConfiguredContext(ctx, route); err != nil {
		return nil, err
	}
	if isMediaLinkRouteID(route.ID) && workflow.generationProviderFactory != nil {
		return workflow.generationProviderFactory(route)
	}

	return workflow.newLegacyGenerationProvider(route)
}

func (workflow *GenerationService) newLegacyGenerationProvider(route coregeneration.ModelRoute) (coregeneration.Provider, error) {
	if workflow.legacyProviderFactory != nil {
		return workflow.legacyProviderFactory(route)
	}
	return runtime.NewProvider(runtime.Config{
		Credentials:                   workflow.generationCredentialResolver(),
		MultimodalTextProviderFactory: workflow.multimodalTextProviderFactory,
		OpenRouterAppName:             "mediago-drama",
		MediagoBaseURL:                workflow.mediagoBaseURL,
		JimengBinPath:                 workflow.jimengBinPath,
		JimengBinDir:                  workflow.jimengBinDir,
		LibTVBinPath:                  workflow.libTVBinPath,
		LibTVBinDir:                   workflow.libTVBinDir,
		LibTVProjectID:                workflow.libTVProjectID,
		LibTVProjectStore:             workflow.settings,
		PippitBinPath:                 workflow.pippitBinPath,
		PippitBinDir:                  workflow.pippitBinDir,
	})
}

func (workflow *GenerationService) newGenerationProviderForTask(id string) (coregeneration.Provider, error) {
	route, err := RouteForGenerationTaskID(id)
	if err != nil {
		return nil, err
	}
	return workflow.newGenerationProvider(route)
}

func (workflow *GenerationService) generationCredentialResolver() runtime.CredentialResolver {
	return runtime.CredentialResolverFunc(func(_ context.Context, key string) (string, error) {
		value, _, err := workflow.settings.GetAPIKey(context.Background(), key)
		return value, err
	})
}

func (workflow *GenerationService) newGenerationProviderForStoredTask(
	id string,
	task generationTaskRecord,
	found bool,
) (coregeneration.Provider, error) {
	return workflow.newGenerationProviderForStoredTaskContext(context.Background(), id, task, found)
}

func (workflow *GenerationService) newGenerationProviderForStoredTaskContext(
	ctx context.Context,
	id string,
	task generationTaskRecord,
	found bool,
) (coregeneration.Provider, error) {
	route, err := RouteForStoredGenerationTask(id, task, found)
	if err != nil {
		return nil, err
	}
	var provider coregeneration.Provider
	if route.ID == coregeneration.RouteAutoDLImage && workflow.generationProviderFactory != nil {
		provider, err = workflow.generationProviderFactory(route)
	} else {
		provider, err = workflow.newGenerationProviderContext(ctx, route)
	}
	if err != nil {
		return nil, err
	}
	if route.ID == coregeneration.RouteAutoDLImage {
		bound, ok := provider.(interface {
			ForRuntimeState(GenerationTaskRuntimeState) coregeneration.Provider
		})
		if !ok {
			return nil, fmt.Errorf("AutoDL image provider does not support persisted runtime state")
		}
		state := task.RuntimeState
		state.LocalTaskID = task.ID
		return bound.ForRuntimeState(state), nil
	}
	return provider, nil
}

func (workflow *GenerationService) requireGenerationRouteConfigured(route coregeneration.ModelRoute) error {
	return workflow.requireGenerationRouteConfiguredContext(context.Background(), route)
}

func (workflow *GenerationService) requireGenerationRouteConfiguredContext(ctx context.Context, route coregeneration.ModelRoute) error {
	if isMediaLinkRouteID(route.ID) {
		ready, reason := workflow.mediaLinkRouteReadyContext(ctx, route.ID)
		if ready {
			return nil
		}
		return errors.New(reason)
	}
	if route.Provider == coregeneration.ProviderMediago &&
		strings.TrimSpace(workflow.mediagoBaseURL) != "" &&
		workflow.generationRouteCredentialsConfigured(route) {
		// Only a successfully fetched catalog may veto the route: a fetch failure
		// means the enablement state is unknown, and failing open lets the real
		// generation request surface the actual error instead of a misleading
		// "model disabled" message.
		if models, err := workflow.mediagoAvailableModels(context.Background()); err == nil {
			missingModels := mediagoMissingRouteModels(models, route)
			if len(missingModels) > 0 {
				return fmt.Errorf("MediaGo 聚合平台当前未启用模型 %s", strings.Join(missingModels, ", "))
			}
		}
	}
	return RequireGenerationRouteConfigured(
		route,
		workflow.generationRouteConfigured(route),
		workflow.generationRouteCredentialLabel(route),
	)
}

func (workflow *GenerationService) generationRouteConfigured(route coregeneration.ModelRoute) bool {
	if isMediaLinkRouteID(route.ID) {
		ready, _ := workflow.mediaLinkRouteReady(route.ID)
		return ready
	}
	return workflow.generationRouteConfiguredWithMediagoModels(route, nil, false)
}

func (workflow *GenerationService) generationRouteConfiguredWithMediagoModels(
	route coregeneration.ModelRoute,
	mediagoModels map[string]struct{},
	hasMediagoModels bool,
) bool {
	if workflow == nil || workflow.settings == nil {
		return false
	}
	if route.Status != coregeneration.RouteStatusAvailable {
		return false
	}
	if !workflow.settings.GenerationProviderEnabled(route.Provider) {
		return false
	}
	if route.Provider == coregeneration.ProviderMediago && strings.TrimSpace(workflow.mediagoBaseURL) == "" {
		return false
	}
	configured := workflow.generationRouteCredentialsConfigured(route)
	if !configured {
		return false
	}
	if route.Provider == coregeneration.ProviderMediago {
		if !hasMediagoModels {
			models, err := workflow.mediagoAvailableModels(context.Background())
			if err != nil {
				// Enablement is unknown when the catalog cannot be fetched; fail
				// open so a transient catalog outage does not report enabled
				// models as unconfigured.
				return true
			}
			mediagoModels = models
		}
		return mediagoModelSetHasRoute(mediagoModels, route)
	}
	return true
}

func (workflow *GenerationService) generationRouteCredentialsConfigured(route coregeneration.ModelRoute) bool {
	return GenerationRouteConfigured(route, func(authKey string) bool {
		value, _, err := workflow.settings.GetAPIKey(context.Background(), authKey)
		if err != nil {
			return false
		}
		return strings.TrimSpace(value) != ""
	})
}

// RouteConfigured reports whether routeID resolves and all credentials are present.
func (workflow *GenerationService) RouteConfigured(routeID string) bool {
	route, ok := coregeneration.FindRoute(routeID)
	if !ok {
		return false
	}
	return workflow.generationRouteConfigured(route)
}

func (workflow *GenerationService) generationRouteCredentialLabel(route coregeneration.ModelRoute) string {
	if workflow == nil || workflow.settings == nil {
		return GenerationRouteCredentialLabel(route, nil)
	}
	return GenerationRouteCredentialLabel(route, workflow.settings.ProviderLabel)
}
