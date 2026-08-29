package generation

import (
	"context"
	"fmt"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
)

type mediaLinkRouteProviders struct {
	codexImage coregeneration.Provider
	autodlH3   coregeneration.Provider
}

func (providers mediaLinkRouteProviders) providerForRoute(route coregeneration.ModelRoute) (coregeneration.Provider, error) {
	switch route.ID {
	case coregeneration.RouteCodexImage:
		return providers.codexImage, nil
	case coregeneration.RouteAutoDLH3:
		return providers.autodlH3, nil
	default:
		return nil, fmt.Errorf("MediaLink route %q is not available", route.ID)
	}
}

func isMediaLinkRouteID(routeID string) bool {
	switch routeID {
	case coregeneration.RouteCodexImage, coregeneration.RouteAutoDLH3:
		return true
	default:
		return false
	}
}

// SetMediaLinkProviders installs the two product providers and their readiness check.
func (workflow *GenerationService) SetMediaLinkProviders(
	codexImage coregeneration.Provider,
	autodlH3 coregeneration.Provider,
	readiness func(context.Context, string) (bool, string),
) {
	providers := mediaLinkRouteProviders{
		codexImage: codexImage,
		autodlH3:   autodlH3,
	}
	workflow.generationProviderFactory = providers.providerForRoute
	workflow.mediaLinkReadiness = readiness
	workflow.mediaLinkProvidersInstalled = true
}

func (workflow *GenerationService) mediaLinkRouteReady(routeID string) (bool, string) {
	if workflow == nil || workflow.mediaLinkReadiness == nil {
		return false, fmt.Sprintf("MediaLink route %q is not ready", routeID)
	}
	return workflow.mediaLinkReadiness(context.Background(), routeID)
}
