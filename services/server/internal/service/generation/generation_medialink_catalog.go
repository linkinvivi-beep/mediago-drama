package generation

import (
	"context"
	"fmt"
	"reflect"
	"time"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
)

const mediaLinkReadinessTimeout = 2 * time.Second

type mediaLinkRouteProviders struct {
	codexImage   coregeneration.Provider
	autodlZImage coregeneration.Provider
	autodlH3     coregeneration.Provider
}

func mediaLinkCatalog(source coregeneration.ModelCatalog) coregeneration.ModelCatalog {
	allowed := map[string]struct{}{
		coregeneration.RouteCodexImage:   {},
		coregeneration.RouteAutoDLZImage: {},
		coregeneration.RouteAutoDLH3:     {},
	}
	return filterCatalogRoutes(source, allowed)
}

func filterCatalogRoutes(source coregeneration.ModelCatalog, allowed map[string]struct{}) coregeneration.ModelCatalog {
	result := coregeneration.ModelCatalog{
		Families:  make([]coregeneration.ModelFamily, 0),
		Versions:  make([]coregeneration.ModelVersion, 0),
		Routes:    make([]coregeneration.ModelRoute, 0),
		Models:    make([]coregeneration.ModelSpec, 0),
		Providers: make([]coregeneration.ProviderInfo, 0),
	}
	referencedFamilies := make(map[string]struct{})
	referencedVersions := make(map[string]struct{})
	referencedProviders := make(map[string]struct{})

	for _, route := range source.Routes {
		if _, ok := allowed[route.ID]; !ok {
			continue
		}
		result.Routes = append(result.Routes, route)
		referencedFamilies[route.FamilyID] = struct{}{}
		referencedVersions[route.VersionID] = struct{}{}
		referencedProviders[route.Provider] = struct{}{}
	}
	for _, family := range source.Families {
		if _, ok := referencedFamilies[family.ID]; ok {
			result.Families = append(result.Families, family)
		}
	}
	for _, version := range source.Versions {
		if _, ok := referencedVersions[version.ID]; ok {
			result.Versions = append(result.Versions, version)
		}
	}
	for _, provider := range source.Providers {
		if _, ok := referencedProviders[provider.ID]; ok {
			result.Providers = append(result.Providers, provider)
		}
	}

	return result
}

func (providers mediaLinkRouteProviders) providerForRoute(route coregeneration.ModelRoute) (coregeneration.Provider, error) {
	var provider coregeneration.Provider
	switch route.ID {
	case coregeneration.RouteCodexImage:
		provider = providers.codexImage
	case coregeneration.RouteAutoDLZImage:
		provider = providers.autodlZImage
	case coregeneration.RouteAutoDLH3:
		provider = providers.autodlH3
	default:
		return nil, fmt.Errorf("MediaLink route %q is not available", route.ID)
	}
	if generationProviderIsNil(provider) {
		return nil, fmt.Errorf("MediaLink route %q provider is not configured", route.ID)
	}
	return provider, nil
}

func generationProviderIsNil(provider coregeneration.Provider) bool {
	if provider == nil {
		return true
	}
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func isMediaLinkRouteID(routeID string) bool {
	switch routeID {
	case coregeneration.RouteCodexImage, coregeneration.RouteAutoDLZImage, coregeneration.RouteAutoDLH3:
		return true
	default:
		return false
	}
}

func isAutoDLGenerationRouteID(routeID string) bool {
	switch routeID {
	case coregeneration.RouteAutoDLZImage, coregeneration.RouteAutoDLH3:
		return true
	default:
		return false
	}
}

// SetMediaLinkProviders installs the currently wired product providers and their readiness check.
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
}

func (workflow *GenerationService) mediaLinkRouteReady(routeID string) (bool, string) {
	return workflow.mediaLinkRouteReadyContext(context.Background(), routeID)
}

func (workflow *GenerationService) mediaLinkRouteReadyContext(ctx context.Context, routeID string) (bool, string) {
	if workflow == nil || workflow.mediaLinkReadiness == nil {
		return false, fmt.Sprintf("MediaLink route %q is not ready", routeID)
	}
	if route, ok := coregeneration.FindRoute(routeID); ok && workflow.generationProviderFactory != nil {
		if _, err := workflow.generationProviderFactory(route); err != nil {
			return false, err.Error()
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	readinessCtx, cancel := context.WithTimeout(ctx, mediaLinkReadinessTimeout)
	defer cancel()
	return workflow.mediaLinkReadiness(readinessCtx, routeID)
}
