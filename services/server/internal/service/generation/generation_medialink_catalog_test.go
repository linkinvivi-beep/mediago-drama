package generation

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/services/server/internal/service/settings"
)

func TestMediaLinkCatalogVisibleListing(t *testing.T) {
	var mediagoRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mediagoRequests.Add(1)
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"data":[{"id":"gpt-image-2"}]}`))
	}))
	defer server.Close()

	settingsService := settings.NewSettings(&generationTestAPIKeyStore{
		values: map[string]string{coregeneration.ProviderMediago: "mgak-test"},
	})
	workflow := NewGenerationService(settingsService, nil, nil)
	workflow.SetMediagoBaseURL(server.URL)
	workflow.SetMediaLinkProviders(
		&mediaLinkTestProvider{name: "codex-test"},
		&mediaLinkTestProvider{name: "autodl-h3-test"},
		func(_ context.Context, routeID string) (bool, string) {
			return routeID == coregeneration.RouteCodexImage, "not ready"
		},
	)

	catalog := workflow.ListGenerationModels()

	if got := mediagoRequests.Load(); got != 0 {
		t.Fatalf("MediaGo catalog requests = %d, want 0", got)
	}
	if got, want := mediaLinkRouteIDs(catalog.Routes), []string{
		coregeneration.RouteCodexImage,
		coregeneration.RouteAutoDLH3,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("route IDs = %v, want %v", got, want)
	}
	if got, want := mediaLinkRouteKinds(catalog.Routes), []coregeneration.Kind{
		coregeneration.KindImage,
		coregeneration.KindVideo,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("route kinds = %v, want %v", got, want)
	}
	if got, want := mediaLinkFamilyIDs(catalog.Families), []string{
		coregeneration.FamilyCodexImage,
		coregeneration.FamilyMiniMaxH3,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("family IDs = %v, want %v", got, want)
	}
	if got, want := mediaLinkVersionIDs(catalog.Versions), []string{
		coregeneration.VersionCodexImageV1,
		coregeneration.VersionMiniMaxH3V1,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("version IDs = %v, want %v", got, want)
	}
	if got, want := mediaLinkProviderIDs(catalog.Providers), []string{
		coregeneration.ProviderCodex,
		coregeneration.ProviderAutoDL,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("provider IDs = %v, want %v", got, want)
	}
	if len(catalog.Models) != 0 {
		t.Fatalf("legacy models = %v, want empty", catalog.Models)
	}
	if !catalog.Routes[0].Configured || catalog.Routes[1].Configured {
		t.Fatalf(
			"route configured values = [%t %t], want readiness-derived [true false]",
			catalog.Routes[0].Configured,
			catalog.Routes[1].Configured,
		)
	}
	for _, family := range catalog.Families {
		if family.Kind != coregeneration.KindImage && family.Kind != coregeneration.KindVideo {
			t.Fatalf("family %q kind = %q, want image or video", family.ID, family.Kind)
		}
	}
	for _, version := range catalog.Versions {
		if version.FamilyID != coregeneration.FamilyCodexImage && version.FamilyID != coregeneration.FamilyMiniMaxH3 {
			t.Fatalf("version %q references hidden family %q", version.ID, version.FamilyID)
		}
	}
	for _, route := range catalog.Routes {
		if route.FamilyID != coregeneration.FamilyCodexImage && route.FamilyID != coregeneration.FamilyMiniMaxH3 {
			t.Fatalf("route %q references hidden family %q", route.ID, route.FamilyID)
		}
		if route.VersionID != coregeneration.VersionCodexImageV1 && route.VersionID != coregeneration.VersionMiniMaxH3V1 {
			t.Fatalf("route %q references hidden version %q", route.ID, route.VersionID)
		}
		if route.Provider != coregeneration.ProviderCodex && route.Provider != coregeneration.ProviderAutoDL {
			t.Fatalf("route %q references hidden provider %q", route.ID, route.Provider)
		}
	}
}

func mediaLinkRouteIDs(routes []coregeneration.ModelRoute) []string {
	ids := make([]string, 0, len(routes))
	for _, route := range routes {
		ids = append(ids, route.ID)
	}
	return ids
}

func mediaLinkRouteKinds(routes []coregeneration.ModelRoute) []coregeneration.Kind {
	kinds := make([]coregeneration.Kind, 0, len(routes))
	for _, route := range routes {
		kinds = append(kinds, route.Kind)
	}
	return kinds
}

func mediaLinkFamilyIDs(families []coregeneration.ModelFamily) []string {
	ids := make([]string, 0, len(families))
	for _, family := range families {
		ids = append(ids, family.ID)
	}
	return ids
}

func mediaLinkVersionIDs(versions []coregeneration.ModelVersion) []string {
	ids := make([]string, 0, len(versions))
	for _, version := range versions {
		ids = append(ids, version.ID)
	}
	return ids
}

func mediaLinkProviderIDs(providers []coregeneration.ProviderInfo) []string {
	ids := make([]string, 0, len(providers))
	for _, provider := range providers {
		ids = append(ids, provider.ID)
	}
	return ids
}

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
