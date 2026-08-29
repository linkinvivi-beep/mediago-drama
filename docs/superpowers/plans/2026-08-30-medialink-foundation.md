# MediaLink Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish the MediaLink product identity, its isolated macOS data root, the two-route generation catalog, and an Apple-Silicon-only build boundary without restructuring MediaGo Drama internals.

**Architecture:** Keep Go module names, import paths, sidecar names, database tables, and hidden legacy provider implementations intact. Add a narrow MediaLink catalog overlay and reuse the server's existing per-route provider factory, then change only user-facing brand, storage, packaging, update, and settings surfaces.

**Tech Stack:** Go 1.25, React 19, TypeScript 7, Electron 42, electron-builder, Vitest, Go `testing`, Task.

## Global Constraints

- Preserve source/script/character/scene/prop/storyboard/asset/task/episode-preview workflows.
- Do not rename `github.com/mediago-dev/mediago-drama` imports, internal sidecar binaries, database tables, or `.mediago-drama` project metadata.
- Do not delete legacy generation providers; hide them from MediaLink product surfaces.
- Do not read, move, delete, or mutate the old MediaGo user-data directory.
- Ship only macOS `arm64`; retain Windows-specific source only where removing it would cause unrelated churn.
- Never point MediaLink's updater or publisher at the upstream MediaGo repository.

---

## File Map

**Create**

- `packages/core/pkg/generation/catalog_medialink.go` — MediaLink image/video families and route specifications.
- `packages/core/pkg/generation/catalog_medialink_test.go` — route and catalog conformance tests.
- `services/server/internal/service/generation/generation_medialink_catalog.go` — visible two-route catalog filter and provider factory composition.
- `services/server/internal/service/generation/generation_medialink_catalog_test.go` — visible catalog tests.
- `apps/workspace/design/icons/medialink/icon.svg` — original MediaLink source icon.
- `apps/workspace/scripts/build-medialink-icons.sh` — deterministic macOS iconset/ICNS builder.

**Modify**

- `packages/core/pkg/generation/providers.go` — add `codex` and `autodl` provider identifiers.
- `packages/core/pkg/generation/catalog_adapters.go` — add the two adapter identifiers.
- `packages/core/pkg/generation/catalog_routes.go` — add the two route identifiers.
- `packages/core/pkg/generation/catalog_data.go` — register the two MediaLink families in the global catalog.
- `services/server/internal/service/generation/generation_runtime.go` — expose the MediaLink catalog and install the existing route-provider hook.
- `services/server/internal/service/shared/workspace_paths.go` and `_test.go` — use the isolated MediaLink user-data root.
- `apps/workspace/electron/src/main.ts` — set visible app/window/error text to MediaLink.
- `apps/workspace/electron/src/updater.ts` and its tests — report unavailable until a MediaLink release feed exists.
- `apps/workspace/scripts/stage-electron-app.ts` — set `productName`, `appId`, artifact name, icon, and macOS-only targets.
- `apps/workspace/package.json`, root `package.json`, `Taskfile.yml` — expose only Apple-Silicon build/release scripts.
- `.github/workflows/electron-release.yml` — build only macOS arm64 and never publish upstream.
- `apps/workspace/src/pages/Projects.tsx`, `ProjectSettings.tsx`, `apps/workspace/src/domains/workspace/components/ProjectNavigator.tsx`, `ProjectNavigatorPanels.tsx`, and visible tests — replace product-facing names only.
- `apps/workspace/src/pages/Settings.tsx` and `_test.tsx` — show the MediaLink settings shell while later plans add Codex and AutoDL panels.
- `README.md`, `LICENSE`, `NOTICE` — MediaLink identity, Apache-2.0 license, and upstream attribution.

## Task 1: Add the MediaLink catalog and route identifiers

**Files:** `packages/core/pkg/generation/providers.go`, `catalog_adapters.go`, `catalog_routes.go`, `catalog_medialink.go`, `catalog_data.go`, `catalog_medialink_test.go`

- [ ] Write failing tests asserting exactly these route identities and kinds:

```go
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
```

- [ ] Run `go test ./pkg/generation -run TestMediaLinkCatalogRoutes -count=1` from `packages/core`; expect failure because the constants do not exist.
- [ ] Add constants with the exact values below:

```go
const (
	ProviderCodex  = "codex"
	ProviderAutoDL = "autodl"

	AdapterCodexImage          = "codex.imagegen"
	AdapterAutoDLComfyH3Video  = "autodl.comfyui.minimax-h3"

	RouteCodexImage = "codex.imagegen"
	RouteAutoDLH3   = "autodl.minimax-h3"
)
```

- [ ] Define families `codex-image` and `minimax-h3`, versions `codex-image-v1` and `minimax-h3-v1`, and only the parameters supported by the approved flows: image aspect ratio/reference bindings; video duration `4..15`, aspect ratio, resolution, seed, and profile kind `ref2va|fl2va`.
- [ ] Register both family specs in `catalog_data.go`; leave all old family specs registered internally.
- [ ] Run `go test ./pkg/generation -run 'TestMediaLinkCatalogRoutes|TestCatalog' -count=1`; expect PASS.
- [ ] Commit: `feat(generation): add MediaLink generation routes`.

## Task 2: Reuse the existing server route-provider factory

**Files:** `services/server/internal/service/generation/generation_medialink_catalog.go`, `_test.go`, `generation_runtime.go`, `generation_runtime_provider.go`

- [ ] Write a failing service test proving the MediaLink factory returns the injected Codex provider for `codex.imagegen`, the injected H3 provider for `autodl.minimax-h3`, and a stable unsupported-route error for a hidden legacy route.
- [ ] Implement a small adapter around the existing `GenerationService.generationProviderFactory` hook:

```go
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
```

- [ ] Add `SetMediaLinkProviders(codexImage, autodlH3 coregeneration.Provider, readiness func(context.Context, string) (bool, string))`. It installs `providers.providerForRoute` into the existing hook and saves the readiness function. The app wiring will pass actual providers after plans 2 and 3; tests use fakes.
- [ ] For the two MediaLink route IDs, call the injected readiness function from `generationRouteConfigured` and `requireGenerationRouteConfigured` before the legacy provider/API-key checks. Return its reason when false; leave old routes on the existing configuration path.
- [ ] Do not change `packages/core/pkg/generation/runtime/provider.go`; internal text completion may continue to build the legacy runtime provider when it needs an existing text route.
- [ ] Run `go test ./internal/service/generation -run TestMediaLinkRouteProviders -count=1` from `services/server`; expect PASS.
- [ ] Commit: `feat(generation): route MediaLink providers`.

## Task 3: Filter MediaLink's visible generation catalog

**Files:** `services/server/internal/service/generation/generation_medialink_catalog.go`, `_test.go`, `generation_runtime.go`, `generation_runtime_provider.go`

- [ ] Write a failing service test that calls the generation-model listing and asserts the visible route ID set is exactly `{codex.imagegen, autodl.minimax-h3}` and the kind set is exactly `{image, video}`.
- [ ] Implement a pure filter so it can be tested without settings or network access:

```go
func mediaLinkCatalog(source coregeneration.ModelCatalog) coregeneration.ModelCatalog {
	allowed := map[string]struct{}{
		coregeneration.RouteCodexImage: {},
		coregeneration.RouteAutoDLH3:   {},
	}
	return filterCatalogRoutes(source, allowed)
}
```

- [ ] Preserve family/version/route references while removing orphaned entries. Mark configuration status through Codex and AutoDL preflight services, not API-key presence.
- [ ] Feed readiness into the filtered catalog through injected Codex and AutoDL preflight functions; do not make API-key presence the configuration signal.
- [ ] Run `go test ./internal/service/generation -run TestMediaLinkCatalog -count=1` from `services/server`; expect PASS.
- [ ] Commit: `feat(generation): expose MediaLink route catalog`.

## Task 4: Isolate MediaLink data and external branding

**Files:** `services/server/internal/service/shared/workspace_paths.go`, `_test.go`, `apps/workspace/electron/src/main.ts`, visible React files and tests, `README.md`, `NOTICE`

- [ ] Change the path test first to expect `~/Library/Application Support/MediaLink` on Darwin while continuing to expect `.mediago-drama` inside project workspaces.
- [ ] Run `go test ./internal/service/shared -run Test.*WorkspacePaths -count=1` from `services/server`; expect failure on the old directory name.
- [ ] Change only `userDataDirName` to `MediaLink`; do not add migration or fallback reads from the old directory.
- [ ] Update window title, startup error copy, About text, project-page copy, settings header, document title, and README to `MediaLink`. Keep internal marker strings and import paths unchanged.
- [ ] Add `NOTICE` with the upstream repository URL, Apache-2.0 attribution, and a statement that MediaLink is a derivative work with independent branding.
- [ ] Add or update UI tests to assert `MediaLink` is rendered and product-facing `MediaGo Drama` is absent.
- [ ] Run `go test ./internal/service/shared -count=1` and `pnpm test -- --run src/pages/Projects.test.tsx src/pages/Settings.test.tsx` from `apps/workspace`; expect PASS.
- [ ] Commit: `feat(app): establish MediaLink identity and data root`.

## Task 5: Add original icon assets

**Files:** `apps/workspace/design/icons/medialink/icon.svg`, `apps/workspace/scripts/build-medialink-icons.sh`, `apps/workspace/build/icons/icon.icns`

- [ ] Create a 1024×1024 SVG using only MediaLink-owned geometry: two rounded interlocking link strokes forming an abstract `M`, with no copied MediaGo shapes or colors.
- [ ] Make the icon script generate the standard `icon.iconset` sizes with `/usr/bin/sips`, then run `/usr/bin/iconutil -c icns`; the script must use paths rooted from its own directory and `set -euo pipefail`.
- [ ] Run `bash apps/workspace/scripts/build-medialink-icons.sh`; expect `apps/workspace/build/icons/icon.icns` to exist and `file` to identify an Apple icon image.
- [ ] Run `git diff --check`; expect no whitespace errors.
- [ ] Commit: `feat(brand): add MediaLink application icon`.

## Task 6: Enforce macOS Apple-Silicon packaging and disable publishing

**Files:** `apps/workspace/scripts/stage-electron-app.ts`, `apps/workspace/package.json`, root `package.json`, `Taskfile.yml`, `.github/workflows/electron-release.yml`, `apps/workspace/electron/src/updater.ts`, related tests

- [ ] Add a staging-script test or exported-config test asserting:

```ts
expect(config.productName).toBe("MediaLink");
expect(config.appId).toBe("app.medialink.desktop");
expect(config.artifactName).toContain("MediaLink");
expect(config.win).toBeUndefined();
expect(config.publish).toBeUndefined();
```

- [ ] Make the staging script reject any target other than `darwin-arm64` with a clear error. Set the artifact pattern to `MediaLink-${version}-macos-arm64.${ext}` and the icon to the new ICNS file.
- [ ] Remove public Windows build/release/publish scripts from package manifests and Task targets. Keep platform-specific runtime source where Electron compilation still references it.
- [ ] Reduce the release workflow to `macos-14`/`arm64`, upload only the DMG/ZIP, and remove release-publish steps and upstream repository identifiers.
- [ ] Change updater behavior to a deterministic unavailable state: `available=false`, `reason="releaseFeedNotConfigured"`; no request or browser link may point at `mediago-dev/mediago-drama`.
- [ ] Run `pnpm test -- --run electron` from `apps/workspace`; expect PASS.
- [ ] Run `pnpm electron:build:darwin-arm64` from `apps/workspace`; expect one arm64 DMG and ZIP with MediaLink names.
- [ ] Validate with `file <path-to-app-executable>` and `codesign -dv --verbose=4 <path-to-app>`; expect arm64 executable and bundle identifier `app.medialink.desktop`.
- [ ] Commit: `build(macos): package MediaLink for Apple Silicon only`.

## Plan Acceptance

- [ ] `go test ./pkg/generation/... -count=1` passes from `packages/core`.
- [ ] `go test ./internal/service/shared ./internal/service/generation -count=1` passes from `services/server`.
- [ ] `pnpm test -- --run src/pages/Projects.test.tsx src/pages/Settings.test.tsx` passes from `apps/workspace`.
- [ ] `rg -n 'mediago-dev/mediago-drama' apps/workspace/electron/src/updater.ts .github/workflows/electron-release.yml` returns no matches.
- [ ] `git status --short` contains only intended implementation files before each explicit commit.
