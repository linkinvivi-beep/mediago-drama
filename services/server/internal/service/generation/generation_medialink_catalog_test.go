package generation

import (
	"context"
	"fmt"
	"testing"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/services/server/internal/service/settings"
)

func TestMediaLinkRouteProviders(t *testing.T) {
	codexProvider := &mediaLinkTestProvider{name: "codex-test"}
	h3Provider := &mediaLinkTestProvider{name: "autodl-h3-test"}
	providers := mediaLinkRouteProviders{
		codexImage: codexProvider,
		autodlH3:   h3Provider,
	}

	t.Run("selects the provider for each MediaLink route", func(t *testing.T) {
		for _, test := range []struct {
			routeID string
			want    coregeneration.Provider
		}{
			{routeID: coregeneration.RouteCodexImage, want: codexProvider},
			{routeID: coregeneration.RouteAutoDLH3, want: h3Provider},
		} {
			route, ok := coregeneration.FindRoute(test.routeID)
			if !ok {
				t.Fatalf("missing route %q", test.routeID)
			}
			got, err := providers.providerForRoute(route)
			if err != nil {
				t.Fatalf("providerForRoute(%q) error = %v", test.routeID, err)
			}
			if got != test.want {
				t.Fatalf("providerForRoute(%q) = %T, want injected provider %T", test.routeID, got, test.want)
			}
		}
	})

	t.Run("rejects a hidden legacy route with a stable error", func(t *testing.T) {
		route, ok := coregeneration.FindRoute(coregeneration.RouteOpenRouterGPT41MiniText)
		if !ok {
			t.Fatalf("missing route %q", coregeneration.RouteOpenRouterGPT41MiniText)
		}
		_, err := providers.providerForRoute(route)
		want := fmt.Sprintf("MediaLink route %q is not available", coregeneration.RouteOpenRouterGPT41MiniText)
		if err == nil || err.Error() != want {
			t.Fatalf("providerForRoute(%q) error = %v, want %q", route.ID, err, want)
		}
	})

	t.Run("uses readiness instead of legacy API key checks", func(t *testing.T) {
		workflow := NewGenerationService(nil, nil, nil)
		workflow.SetMediaLinkProviders(codexProvider, h3Provider, func(_ context.Context, routeID string) (bool, string) {
			if routeID == coregeneration.RouteCodexImage {
				return true, ""
			}
			return false, "AutoDL H3 is not ready"
		})

		codexRoute, _ := coregeneration.FindRoute(coregeneration.RouteCodexImage)
		if !workflow.generationRouteConfigured(codexRoute) {
			t.Fatal("Codex route should be configured when its readiness check succeeds")
		}
		got, err := workflow.newGenerationProvider(codexRoute)
		if err != nil || got != codexProvider {
			t.Fatalf("newGenerationProvider(Codex) = %T, %v; want injected provider", got, err)
		}

		h3Route, _ := coregeneration.FindRoute(coregeneration.RouteAutoDLH3)
		if workflow.generationRouteConfigured(h3Route) {
			t.Fatal("AutoDL route should be unconfigured when its readiness check fails")
		}
		if err := workflow.requireGenerationRouteConfigured(h3Route); err == nil || err.Error() != "AutoDL H3 is not ready" {
			t.Fatalf("requireGenerationRouteConfigured(AutoDL) error = %v, want injected readiness reason", err)
		}
	})

	t.Run("keeps hidden legacy routes on the runtime provider", func(t *testing.T) {
		settingsService := settings.NewSettings(&generationTestAPIKeyStore{
			values: map[string]string{coregeneration.ProviderOpenRouter: "sk-test"},
		})
		workflow := NewGenerationService(settingsService, nil, nil)
		workflow.generationProviderFactory = providers.providerForRoute

		legacyRoute, _ := coregeneration.FindRoute(coregeneration.RouteOpenRouterGPT41MiniText)
		got, err := workflow.newGenerationProvider(legacyRoute)
		if err != nil {
			t.Fatalf("newGenerationProvider(%q) error = %v", legacyRoute.ID, err)
		}
		if got == codexProvider || got == h3Provider || got.Name() != "generation-runtime" {
			t.Fatalf("newGenerationProvider(%q) = %T (%q), want legacy generation runtime", legacyRoute.ID, got, got.Name())
		}
	})
}

type mediaLinkTestProvider struct {
	name string
}

func (provider *mediaLinkTestProvider) Name() string {
	return provider.name
}

func (*mediaLinkTestProvider) Generate(context.Context, coregeneration.Request) (coregeneration.Response, error) {
	return coregeneration.Response{}, nil
}

func (*mediaLinkTestProvider) Get(context.Context, string) (coregeneration.Response, error) {
	return coregeneration.Response{}, nil
}
