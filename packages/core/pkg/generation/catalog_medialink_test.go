package generation

import (
	"reflect"
	"testing"
)

func TestMediaLinkCatalogRoutes(t *testing.T) {
	imageRoute, ok := FindRoute(RouteCodexImage)
	if !ok || imageRoute.Kind != KindImage || imageRoute.Provider != ProviderCodex {
		t.Fatalf("Codex image route = %+v, %v", imageRoute, ok)
	}
	videoRoute, ok := FindRoute(RouteAutoDLH3)
	if !ok || videoRoute.Kind != KindVideo || videoRoute.Provider != ProviderAutoDL {
		t.Fatalf("AutoDL H3 route = %+v, %v", videoRoute, ok)
	}
}

func TestMediaLinkCatalogIncludesZImageWithOneReference(t *testing.T) {
	route, ok := FindRoute(RouteAutoDLImage)
	if !ok || route.Kind != KindImage || route.Provider != ProviderAutoDL ||
		route.Adapter != AdapterAutoDLComfyImage || !route.SupportsReferenceURLs ||
		route.MaxReferenceURLs != 1 || route.Async {
		t.Fatalf("route = %+v, found = %v", route, ok)
	}
}

func TestMediaLinkCatalogRouteContracts(t *testing.T) {
	tests := []struct {
		name              string
		routeID           string
		wantID            string
		wantFamilyID      string
		wantVersionID     string
		wantLabel         string
		wantKind          Kind
		wantProvider      string
		wantAdapter       string
		wantAsync         bool
		wantParams        []string
		wantReferenceURLs bool
	}{
		{
			name:              "Codex image",
			routeID:           RouteCodexImage,
			wantID:            "codex.imagegen",
			wantFamilyID:      FamilyCodexImage,
			wantVersionID:     VersionCodexImageV1,
			wantLabel:         "Codex 生图",
			wantKind:          KindImage,
			wantProvider:      ProviderCodex,
			wantAdapter:       AdapterCodexImage,
			wantParams:        []string{"aspectRatio"},
			wantReferenceURLs: true,
		},
		{
			name:              "AutoDL image",
			routeID:           RouteAutoDLImage,
			wantID:            "autodl.image",
			wantFamilyID:      FamilyZImage,
			wantVersionID:     VersionZImageV1,
			wantLabel:         "AutoDL · 云端生图",
			wantKind:          KindImage,
			wantProvider:      ProviderAutoDL,
			wantAdapter:       AdapterAutoDLComfyImage,
			wantParams:        []string{"aspectRatio", "resolution", "seed"},
			wantReferenceURLs: true,
		},
		{
			name:              "AutoDL H3 video",
			routeID:           RouteAutoDLH3,
			wantID:            "autodl.minimax-h3",
			wantFamilyID:      FamilyMiniMaxH3,
			wantVersionID:     VersionMiniMaxH3V1,
			wantLabel:         "AutoDL · MiniMax H3",
			wantKind:          KindVideo,
			wantProvider:      ProviderAutoDL,
			wantAdapter:       AdapterAutoDLComfyH3Video,
			wantAsync:         true,
			wantParams:        []string{"duration", "aspectRatio", "resolution", "seed", "profileKind"},
			wantReferenceURLs: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			route, ok := FindRoute(tc.routeID)
			if !ok {
				t.Fatalf("FindRoute(%q) is missing", tc.routeID)
			}
			if route.ID != tc.wantID || route.FamilyID != tc.wantFamilyID || route.VersionID != tc.wantVersionID ||
				route.Label != tc.wantLabel || route.Kind != tc.wantKind || route.Provider != tc.wantProvider ||
				route.Adapter != tc.wantAdapter || route.Async != tc.wantAsync ||
				route.SupportsReferenceURLs != tc.wantReferenceURLs {
				t.Fatalf("route = %+v", route)
			}
			if len(route.AuthKeys) != 0 {
				t.Fatalf("route auth keys = %v, want none", route.AuthKeys)
			}
			gotParams := make([]string, 0, len(route.Params))
			for _, param := range route.Params {
				gotParams = append(gotParams, param.Name)
				if param.Name == "referenceImages" || param.Name == "referenceUrls" {
					t.Fatalf("ordered references must not be a catalog parameter: %+v", param)
				}
			}
			if !reflect.DeepEqual(gotParams, tc.wantParams) {
				t.Fatalf("route params = %v, want %v", gotParams, tc.wantParams)
			}

			references := []string{"reference-1", "reference-2"}
			request := ApplyRoute(Request{ReferenceURLs: references}, route)
			if !reflect.DeepEqual(request.ReferenceURLs, references) {
				t.Fatalf("ordered references = %v, want %v", request.ReferenceURLs, references)
			}
		})
	}
}

func TestMediaLinkH3CanonicalOptions(t *testing.T) {
	route, ok := FindRoute(RouteAutoDLH3)
	if !ok {
		t.Fatal("AutoDL H3 route is missing")
	}

	duration := routeParamValues(mustParam(t, route, string(ParamDuration)))
	wantDuration := []string{"4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15"}
	if !reflect.DeepEqual(duration, wantDuration) {
		t.Fatalf("duration options = %v, want %v", duration, wantDuration)
	}
	profile := routeParamValues(mustParam(t, route, string(ParamProfileKind)))
	if want := []string{"ref2va", "fl2va"}; !reflect.DeepEqual(profile, want) {
		t.Fatalf("profile options = %v, want %v", profile, want)
	}
}

func TestMediaLinkProvidersAreDirectAndCredentialFree(t *testing.T) {
	want := map[string]ProviderInfo{
		ProviderCodex:  {ID: ProviderCodex, Label: "Codex", ProviderType: ProviderTypeLocal},
		ProviderAutoDL: {ID: ProviderAutoDL, Label: "AutoDL", ProviderType: ProviderTypeCustom},
	}
	for _, provider := range Providers() {
		if expected, ok := want[provider.ID]; ok {
			if provider != expected {
				t.Fatalf("provider %q = %+v, want %+v", provider.ID, provider, expected)
			}
			delete(want, provider.ID)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing providers: %v", want)
	}
	for _, providerID := range []string{ProviderCodex, ProviderAutoDL} {
		if spec, ok := FindCredentialSpec(providerID); ok {
			t.Fatalf("provider %q unexpectedly has credential spec %+v", providerID, spec)
		}
	}
}

func TestMediaLinkFamiliesAreAddedWithoutRemovingLegacyCatalog(t *testing.T) {
	want := map[string]struct {
		versionID string
		kind      Kind
	}{
		FamilyCodexImage: {versionID: VersionCodexImageV1, kind: KindImage},
		FamilyZImage:     {versionID: VersionZImageV1, kind: KindImage},
		FamilyMiniMaxH3:  {versionID: VersionMiniMaxH3V1, kind: KindVideo},
	}
	for _, group := range ModelFamilyGroups() {
		expected, ok := want[group.Family.ID]
		if !ok {
			continue
		}
		if group.Family.Kind != expected.kind || len(group.Versions) != 1 || group.Versions[0].ID != expected.versionID || len(group.Routes) != 1 {
			t.Fatalf("family group %q = %+v", group.Family.ID, group)
		}
		delete(want, group.Family.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing MediaLink families: %v", want)
	}
	if _, ok := FindRoute(RouteDMXSeedream5Lite); !ok {
		t.Fatal("legacy DMX Seedream route was removed")
	}
}

func routeParamValues(param ParamSpec) []string {
	values := make([]string, 0, len(param.Options))
	for _, option := range param.Options {
		values = append(values, option.Value)
	}
	return values
}
