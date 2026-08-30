# MediaLink Codex Image Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace every visible image-generation route with a globally sequential Codex `$imagegen` runner that uses the user's existing ChatGPT login, persists structured app-server state, and imports completed images into MediaLink assets.

**Architecture:** Extend the existing `codexapp` JSON-RPC session instead of invoking an image API or scraping chat text. A server-owned Codex provider consumes structured `imageGeneration` thread items, emits metadata-only progress checkpoints, and resumes interrupted work through persisted thread/turn/item IDs. Existing generation tasks, media caching, selection, and history remain authoritative.

**Tech Stack:** Go, Codex app-server JSON-RPC v2, GORM/SQLite, React/SWR, Go `testing`, Vitest.

## Global Constraints

- Use built-in Codex `$imagegen`; do not add OpenAI Image API URLs, API-key fields, or model billing settings.
- Use `modelProvider/capabilities/read` and require `imageGeneration=true` before submission.
- Parse only structured `imageGeneration` items; never scrape assistant prose or Markdown image links.
- Allow one active Codex image job globally; queued jobs remain visible and cancelable.
- Save and import only absolute image paths produced inside the job-specific workspace.
- A real image generation consumes the user's Codex/ChatGPT quota; run mocked tests only until separately authorized.

---

## File Map

**Create**

- `services/server/internal/platform/codexapp/capabilities.go` and `_test.go` — capability RPC DTOs and checks.
- `services/server/internal/platform/codexapp/image_generation.go` and `_test.go` — thread/turn/image item protocol.
- `services/server/internal/service/generation/codex_image_provider.go` and `_test.go` — generation provider, queue, checkpoints, resume.
- `services/server/internal/service/settings/codex_image.go` and `_test.go` — settings preflight result.

**Modify**

- `services/server/internal/platform/codexapp/session.go` — MediaLink client identity and testable session factory.
- `services/server/internal/domain/generation_models.go` — additive `runtime_state_json` task column.
- `services/server/internal/http/dto/generation.go` — sanitized runtime-state response.
- `services/server/internal/repository/generation_task_repo.go` and tests — persist the new column.
- `services/server/internal/service/generation/generation_tasks_service.go` and tests — encode/decode runtime state and recover active image tasks.
- `services/server/internal/service/generation/generation_helpers.go` and tests — copy runtime metadata and provider task IDs.
- `services/server/internal/service/generation/generation_runtime_tasks.go` and tests — persist metadata-only progress and resume.
- `services/server/internal/service/generation/generation_medialink_catalog.go` — bind `codex.imagegen` to the provider.
- `services/server/internal/http/handlers/settings.go`, `routes/routes.go`, handler tests — expose Codex image preflight.
- `apps/workspace/src/domains/settings/api/settings.ts` — preflight types and API call.
- `apps/workspace/src/domains/settings/components/CodexAccessPanel.tsx` and `_test.tsx` — login/capability/quota explanation.
- `apps/workspace/src/pages/Settings.tsx` and `_test.tsx` — remove relay/API-key image controls from the visible product.

## Task 1: Add typed capability and image-generation protocol support

**Files:** `codexapp/capabilities.go`, `capabilities_test.go`, `image_generation.go`, `image_generation_test.go`, `session.go`

- [ ] Build a fake `codexapp.Client` test that records calls and returns this exact capability payload:

```json
{"imageGeneration":true,"namespaceTools":true,"webSearch":true}
```

- [ ] Test that the first request method is `modelProvider/capabilities/read` with `{}` params and that `imageGeneration=false` becomes a typed unavailable result rather than a login error.
- [ ] Add protocol types matching the generated schema:

```go
type ModelProviderCapabilities struct {
	ImageGeneration bool `json:"imageGeneration"`
	NamespaceTools  bool `json:"namespaceTools"`
	WebSearch       bool `json:"webSearch"`
}

type ImageGenerationThreadItem struct {
	ID                    string                  `json:"id"`
	Result                string                  `json:"result"`
	RevisedPrompt         *string                 `json:"revisedPrompt"`
	SavedPath             *string                 `json:"savedPath"`
	Status                string                  `json:"status"`
	Failure               *ImageGenerationFailure `json:"failure"`
	TransparentBackground *bool                   `json:"transparentBackground"`
	Type                  string                  `json:"type"`
}
```

- [ ] Add a session method that starts a thread in a supplied job directory and starts a turn whose input contains one text item beginning with `$imagegen`. Add ordered `localImage` input items for each validated reference path.
- [ ] Set `approvalPolicy` to `never`, use the workspace-write sandbox, and set client info to name/title `medialink`/`MediaLink`.
- [ ] Parse `item/started`, `item/completed`, and `turn/completed`; accept an image only when `type=imageGeneration`, `status=completed`, `failure=null`, and `savedPath` is present.
- [ ] Run `go test ./internal/platform/codexapp -run 'Test.*(Capabilities|ImageGeneration)' -count=1` from `services/server`; expect PASS.
- [ ] Commit: `feat(codex): support structured image generation events`.

## Task 2: Persist resumable task runtime state

**Files:** `domain/generation_models.go`, `http/dto/generation.go`, `repository/generation_task_repo.go`, `generation_tasks_service.go`, corresponding tests

- [ ] Write a repository round-trip test that stores and reloads these fields:

```go
type GenerationTaskRuntimeState struct {
	CodexThreadID string `json:"codexThreadId,omitempty"`
	CodexTurnID   string `json:"codexTurnId,omitempty"`
	CodexItemID   string `json:"codexItemId,omitempty"`
	RevisedPrompt string `json:"revisedPrompt,omitempty"`
	SavedPath     string `json:"savedPath,omitempty"`
	ComfyPromptID string `json:"comfyPromptId,omitempty"`
	SubmittedAt   string `json:"submittedAt,omitempty"`
}
```

- [ ] Add `RuntimeStateJSON string` to `GenerationTaskModel` with `gorm:"column:runtime_state_json;not null;type:text;default:'{}'"`; rely on the existing AutoMigrate path for this additive local schema change.
- [ ] Add `RuntimeState GenerationTaskRuntimeState` to the service record and DTO. Never place credentials, private keys, tunnel endpoints, or raw Codex event payloads in it.
- [ ] Update repository upsert assignments and service encode/decode helpers. Invalid stored JSON must return a wrapped data error rather than silently dropping recovery state.
- [ ] Expand pending-image statuses to `preparing`, `queued`, `submitting`, `submitted`, `running`, `importing`, and `waiting_reconnect`.
- [ ] Run `go test ./internal/repository ./internal/service/generation -run 'Test.*RuntimeState|Test.*ListPending' -count=1`; expect PASS.
- [ ] Commit: `feat(tasks): persist generation recovery state`.

## Task 3: Implement the global sequential Codex image provider

**Files:** `service/generation/codex_image_provider.go`, `_test.go`, `generation_medialink_catalog.go`

- [ ] Write table-driven provider tests for: capability unavailable, FIFO ordering, canceled queued task, successful image item, failure item, saved path outside job root, interrupted turn returned as resumable, and resume from an existing thread.
- [ ] Define injected interfaces at the consumer boundary:

```go
type CodexImageSession interface {
	Capabilities(context.Context) (codexapp.ModelProviderCapabilities, error)
	GenerateImage(context.Context, codexapp.ImageGenerationRequest, func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error)
	ReadImageResult(context.Context, string) (codexapp.ImageGenerationResult, error)
}

type CodexImageProvider struct {
	session CodexImageSession
	root    string
	queue   chan struct{}
}
```

- [ ] Create one provider instance for the application so `queue := make(chan struct{}, 1)` serializes every image task. Acquire with a context-aware `select`; release with `defer`.
- [ ] Create each job directory below `<MediaLink user data>/generation/codex-image/<task-id>`. Build the text input as `$imagegen\n` plus the final image prompt; add references as `localImage` inputs in request order.
- [ ] Emit progress checkpoints after thread start, turn start, image item start, and completion. Put typed `GenerationTaskRuntimeState` in `coregeneration.Response.Metadata["runtime_state"]`.
- [ ] Set response ID to `codex.imagegen:<thread-id>`. If the app-server disconnects after a thread ID exists, return status `waiting_reconnect` and the response ID without starting a second turn.
- [ ] In `Get`, strip the prefix, call `ReadImageResult`, and return the same response ID until a terminal item exists.
- [ ] Validate `savedPath` with `filepath.Rel(jobRoot, savedPath)`: reject absolute escapes and `..`; require an allowed image MIME by content sniffing and a non-zero file size.
- [ ] Supply this provider as the `codexImage` member of the foundation plan's `mediaLinkRouteProviders` value and supply Codex preflight through its readiness function.
- [ ] Run `go test ./internal/service/generation -run TestCodexImageProvider -count=1`; expect PASS.
- [ ] Commit: `feat(generation): run Codex image jobs sequentially`.

## Task 4: Persist metadata-only progress and recover without duplicate turns

**Files:** `generation_helpers.go`, `generation_runtime_tasks.go`, `generation_runtime_test.go`, `generation_tasks_service.go`

- [ ] Write a failing runtime test where a provider emits a progress event with zero assets but non-empty `runtime_state`; assert the running task is updated before the provider disconnects.
- [ ] Extend `GenerationResponseFromCore` to map a typed `runtime_state` metadata value into `GenerationMessageResponse.RuntimeState`.
- [ ] Change `persistGenerationProgress` so metadata-only events update status, provider task ID, and runtime state even when there are no assets. Continue using the existing asset-cache path when assets exist.
- [ ] Make `generationProviderTaskIDForResponse` retain `codex.imagegen:<thread-id>` for Codex image tasks, not only async video tasks.
- [ ] On application recovery, route a Codex pending task with a provider task ID to `Provider.Get`; never call `Provider.Generate` when `CodexThreadID` or the Codex provider task ID exists.
- [ ] Add a restart test: persist `waiting_reconnect` + thread ID, construct a new service, recover the completed image item, import one asset, and assert the fake session recorded zero new `turn/start` calls.
- [ ] Run `go test ./internal/service/generation -run 'Test.*(ProgressRuntimeState|RecoverCodexImage)' -count=1`; expect PASS.
- [ ] Commit: `fix(generation): resume Codex image tasks safely`.

## Task 5: Import the generated file through the existing asset pipeline

**Files:** `generation_runtime_assets.go`, `generation_runtime_import.go`, `codex_image_provider_test.go`, `generation_runtime_test.go`

- [ ] Write an integration-style service test using a temporary PNG. The provider returns its absolute `savedPath`; the task must finish with one cached MediaLink asset and no external file URI in the public response.
- [ ] Security correction from code review: return the immutable bytes obtained by the same secure validation read as an internal-only `coregeneration.Asset` payload with image kind and detected MIME. Pass that payload through `cacheGenerationResponseAssetsForTask`, scrub the payload and its non-path marker on every success, failure, cancellation, nil-store, and progress-reuse branch, and never reopen `savedPath` by pathname. This does not change the user-visible specification.
- [ ] Preserve `revisedPrompt` in runtime state and preserve the user's original prompt in `GenerationTaskRecord.Prompt`.
- [ ] Let the existing asset import, selection, storyboard attachment, and project asset APIs operate unchanged.
- [ ] Delete no Codex output files. Treat the job directory as application-owned recovery evidence; later cleanup is a separate feature.
- [ ] Run `go test ./internal/service/generation -run 'Test.*Codex.*Import' -count=1`; expect PASS.
- [ ] Commit: `feat(assets): import Codex image outputs`.

## Task 6: Add image-specific prompt optimization routing

**Files:** `generation_runtime_prompt_optimize.go`, `_test.go`, frontend generation prompt hook files found by `rg -n 'optimizedPrompt|usePromptOptimize' apps/workspace/src`

- [ ] Add tests for both branches: optimization disabled sends the user's prompt directly after `$imagegen`; enabled first runs the existing Codex text optimizer and sends its returned prompt.
- [ ] Add an image-only system instruction that preserves character/scene/prop identity, composition, medium, lighting, aspect ratio, and ordered reference roles; require prompt-only output.
- [ ] Reuse the existing `optimizedPrompt` response/history fields. Store Codex image generation's `revisedPrompt` separately in runtime state because it is produced by `$imagegen`, not by the text optimizer.
- [ ] Ensure the text optimization route is an internal executor and is absent from the visible generation catalog.
- [ ] Run `go test ./internal/service/generation -run 'Test.*ImagePromptOptimi' -count=1`; expect PASS.
- [ ] Run the focused frontend prompt-hook test selected by `rg -l 'usePromptOptimize' apps/workspace/src --glob '*test*'`; expect PASS.
- [ ] Commit: `feat(prompts): optimize Codex image prompts`.

## Task 7: Expose Codex image preflight in Settings

**Files:** `settings/codex_image.go`, `_test.go`, `handlers/settings.go`, handler tests, `routes/routes.go`, `settings.ts`, `CodexAccessPanel.tsx`, `_test.tsx`, `Settings.tsx`

- [ ] Define a preflight response with non-secret fields only:

```go
type CodexImagePreflight struct {
	AccountStatus   string `json:"accountStatus"`
	ImageGeneration bool   `json:"imageGeneration"`
	Ready           bool   `json:"ready"`
	Reason          string `json:"reason,omitempty"`
}
```

- [ ] Add `GET /api/v1/settings/codex-image/preflight`; it reads the shared Codex login and calls `modelProvider/capabilities/read`. Map not logged in, CLI unavailable, capability disabled, and ready to stable reason codes.
- [ ] Update `CodexAccessPanel` to show shared ChatGPT login, capability state, refresh/test button, and the disclosure that generation uses Codex quota. Do not render a model picker, API key, relay URL, or Image API price field.
- [ ] Remove `CodexRelayPanel`, model-platform image controls, and API-key provider cards from the visible MediaLink Settings page; retain their source modules.
- [ ] Add component tests for ready, not logged in, capability unavailable, and refresh failure states.
- [ ] Run `go test ./internal/service/settings ./internal/http/handlers -run 'Test.*CodexImage' -count=1` from `services/server`; expect PASS.
- [ ] Run `pnpm test -- --run src/domains/settings/components/CodexAccessPanel.test.tsx src/pages/Settings.test.tsx` from `apps/workspace`; expect PASS.
- [ ] Commit: `feat(settings): add Codex image readiness check`.

## Plan Acceptance

- [ ] All image selectors expose only `Codex 生图` and route ID `codex.imagegen`.
- [ ] Two simultaneous mocked jobs complete in FIFO order with a maximum concurrency of one.
- [ ] Restart recovery completes from a persisted Codex thread without a second `turn/start`.
- [ ] Public task JSON contains no credential or raw app-server payload.
- [ ] `rg -n 'api.openai.com|images/generations|OPENAI_API_KEY' services/server/internal/service/generation/codex_image_provider.go apps/workspace/src/domains/settings` returns no matches.
- [ ] No real `$imagegen` turn is started during automated verification.
