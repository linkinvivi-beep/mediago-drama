# MediaLink Integration and Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Integrate route-aware MiniMax H3 prompt optimization with MediaGo Drama's preserved character/scene/prop/storyboard workflow, present durable image/video task states, and produce a verified MediaLink Apple-Silicon release candidate.

**Architecture:** Extend the existing prompt-optimization request with target route context and assemble H3 context on the server from the full storyboard plus linked character, scene, prop, and ordered reference documents. Keep `optimizedPrompt` as the existing user-visible field. Finish with cross-route UI regression tests, mocked restart scenarios, and an unsigned/local macOS arm64 artifact; paid live generation remains an explicitly approved acceptance step.

**Tech Stack:** Go, React 19, TypeScript, Codex text completion, MiniMax H3 prompt contract, Vitest/Testing Library, Playwright-equivalent existing UI tests if present, Task, electron-builder, macOS `codesign`/`file`.

> **Superseded workflow assumptions (2026-09-01):** The approved configurable-workflow plan replaces fixed Z-Image/FLUX/Qwen and REF2VA/FL2VA source-code profiles with a generic workflow registry. MediaLink now exposes `Codex 生图`, `AutoDL · 云端生图`, and `AutoDL · MiniMax H3`. Every AutoDL workflow is selected by registry profile/version and semantic bindings. The remote ComfyUI loopback port is configurable per dynamic SSH instance; `6006` is only a possible value, never a required fixed port.

## Global Constraints

- Preserve the existing prompt-optimization toggle and history behavior.
- H3 optimization uses Codex text completion; it does not call MiniMax's text API.
- Context must include the full current storyboard and resolved character/scene/prop documents, not a truncated selection or only the current section.
- Ordered reference roles must remain explicit in the optimized prompt.
- Output is one complete H3 prompt of at most 7000 characters with duration `4..15` seconds.
- Validate structured completeness; do not silently shorten, invent absent character facts, or omit continuity constraints.
- Do not run paid Codex image or AutoDL GPU acceptance without a separate final authorization.

---

## Dependency Order

1. Complete `2026-08-30-medialink-foundation.md`.
2. Complete `2026-08-30-medialink-codex-image.md`.
3. Complete `2026-08-30-medialink-autodl-h3-video.md`.
4. Execute this plan.

## File Map

**Create**

- `services/server/internal/service/generation/generation_h3_prompt_context.go`, `_test.go` — full document/reference context assembly.
- `services/server/internal/service/generation/generation_h3_prompt.go`, `_test.go` — H3 instruction, output validation, duration/length contract.
- `apps/workspace/src/domains/generation/lib/medialink-routes.ts`, `_test.ts` — visible route labels and state presentation helpers.
- `apps/workspace/src/domains/generation/components/GenerationConnectionState.tsx`, `_test.tsx` — reconnect/recovery/error presentation.
- `docs/release/medialink-macos-arm64-checklist.md` — repeatable release evidence checklist.

**Modify**

- `services/server/internal/http/dto/generation.go` — target-route prompt-optimization context fields.
- `services/server/internal/service/generation/generation_runtime_prompt_optimize.go` and tests — select image or H3 instruction and persist result.
- `services/server/internal/service/generation/generation_document_context.go`, `generation_prompt_references.go`, `generation_reference_bindings.go` and tests — assemble complete ordered H3 context.
- Frontend files found by `rg -n 'promptOptimization|optimizedPrompt|usePromptOptimize' apps/workspace/src` — send target context through both optimize-only and optimize-and-generate flows.
- Frontend files found by `rg -n 'generation/models|routeId|Provider|生成视频|生成图片' apps/workspace/src/domains apps/workspace/src/features apps/workspace/src/pages` — restrict visible route choices and render durable states.
- Existing character, scene, prop, storyboard, asset, task, and episode-preview tests — add non-regression coverage without redesigning components.
- `README.md`, `docs/release/medialink-macos-arm64-checklist.md` — local build/install/configuration and acceptance sequence.

## Task 1: Extend prompt-optimization requests with target context

**Files:** `http/dto/generation.go`, frontend prompt optimization types/builders and tests found by the search command above

- [ ] Write Go JSON round-trip and TypeScript builder tests for an H3 optimize request carrying target route, document context, ordered asset IDs/bindings, and target video params.
- [ ] Append these fields to the existing optimization DTO instead of replacing its executor/model fields or adding a parallel endpoint:

```go
	TargetKind        string                       `json:"targetKind,omitempty"`
	TargetRouteID     string                       `json:"targetRouteId,omitempty"`
	DocumentContext   *GenerationDocumentContext   `json:"documentContext,omitempty"`
	ReferenceAssetIDs []string                     `json:"referenceAssetIds,omitempty"`
	ReferenceBindings []GenerationReferenceBinding `json:"referenceBindings,omitempty"`
	TargetParams      map[string]any               `json:"targetParams,omitempty"`
```

- [ ] For optimize-only and optimize-and-generate, populate those fields from the same current editor state used for final submission. Keep executor/model params separate from `TargetParams`.
- [ ] For old saved requests with no target fields, preserve the current generic optimizer behavior.
- [ ] Run `go test ./internal/http/dto ./internal/service/generation -run Test.*PromptOptimizationRequest -count=1` from `services/server`; expect PASS.
- [ ] Run the focused frontend test files returned by `rg -l 'promptOptimization' apps/workspace/src --glob '*test*'`; expect PASS.
- [ ] Commit: `feat(prompts): carry target generation context`.

## Task 2: Assemble full H3 storyboard and entity context

**Files:** `generation_h3_prompt_context.go`, `_test.go`, `generation_document_context.go`, `generation_prompt_references.go`, `generation_reference_bindings.go`

- [ ] Build a fixture project with one full storyboard document, two character documents, one scene document, one prop document, and ordered reference assets. Test exact context order and complete contents.
- [ ] Define context types:

```go
type H3ContextDocument struct {
	Kind    string
	Title   string
	Content string
}

type H3ReferenceContext struct {
	Index   int
	Role    string
	AssetID string
	Label   string
}

type H3PromptContext struct {
	Storyboard string
	Documents  []H3ContextDocument
	References []H3ReferenceContext
	Duration   int
	AspectRatio string
	Resolution string
	ProfileKind string
}
```

- [ ] Resolve the current document through the existing `GenerationDocumentResolver`. Use full `document.Content`, never selected section text, for the storyboard.
- [ ] Parse entity mentions with existing generation mention helpers, resolve the referenced character/scene/prop documents, deduplicate by document ID, and sort by first mention order.
- [ ] Resolve references from `ReferenceBindings` first and append unbound `ReferenceAssetIDs` in user order. Assign roles from bindings; never infer face/body/first/last roles when absent.
- [ ] Fail with stable errors when the storyboard is missing, a bound reference asset is unreadable, duration is outside `4..15`, or profile kind is not `ref2va|fl2va`.
- [ ] Run `go test ./internal/service/generation -run TestBuildH3PromptContext -count=1`; expect PASS.
- [ ] Commit: `feat(prompts): assemble H3 continuity context`.

## Task 3: Implement the MiniMax H3 optimization contract

**Files:** `generation_h3_prompt.go`, `_test.go`, `generation_runtime_prompt_optimize.go`

- [ ] Write table-driven tests asserting the system instruction requires all contract sections and that validation rejects empty output, output over 7000 characters, missing duration, missing reference mapping, and a missing end-state description.
- [ ] Select H3 instructions only when `TargetKind=video` and `TargetRouteID=autodl.minimax-h3`; use the Codex image instruction for `codex.imagegen`; retain generic instructions for legacy internal calls.
- [ ] Build the user prompt in deterministic XML-like blocks with escaped content:

```text
<generation duration="8" aspect_ratio="16:9" resolution="1080p" profile="ref2va">
<storyboard>FULL STORYBOARD</storyboard>
<characters>ORDERED CHARACTER DOCUMENTS</characters>
<scenes>ORDERED SCENE DOCUMENTS</scenes>
<props>ORDERED PROP DOCUMENTS</props>
<references>ORDERED ROLE MAPPINGS</references>
</generation>
```

- [ ] The system instruction must request: identity and wardrobe continuity; scene and prop continuity; ordered reference mapping; master timeline plus micro-timelines; camera/lens/movement; subject motion; lighting/color/style; negative constraints; and final frame/end state.
- [ ] Require one prompt only, duration `4..15`, maximum 7000 characters, and no commentary. State that unknown facts must remain unspecified rather than invented.
- [ ] Implement a validator that checks length, duration mention, reference indices when references exist, timeline markers, camera/motion, continuity, negative constraints, and end state. Return specific validation errors; do not truncate or rewrite the model output in code.
- [ ] Store the accepted value in the existing `optimizedPrompt` field and existing optimization history path.
- [ ] Run `go test ./internal/service/generation -run 'Test.*H3Prompt' -count=1`; expect PASS.
- [ ] Commit: `feat(prompts): optimize for MiniMax H3`.

## Task 4: Restrict visible routes across the existing workflow

**Files:** `medialink-routes.ts`, `_test.ts`, existing generation selectors/forms located by the File Map search

- [ ] Write selector tests for every generation entry point used by characters, scenes, props, storyboards, and general asset generation. Image choices must contain `Codex 生图` and `AutoDL · 云端生图`; video choices only `AutoDL · MiniMax H3`.
- [ ] Add centralized constants:

```ts
export const MEDIALINK_CODEX_IMAGE_ROUTE_ID = "codex.imagegen";
export const MEDIALINK_AUTODL_IMAGE_ROUTE_ID = "autodl.image";
export const MEDIALINK_VIDEO_ROUTE_ID = "autodl.minimax-h3";

export const medialinkRouteLabel = (routeId: string) =>
	routeId === MEDIALINK_CODEX_IMAGE_ROUTE_ID
		? "Codex 生图"
		: routeId === MEDIALINK_AUTODL_IMAGE_ROUTE_ID
			? "AutoDL · 云端生图"
			: "AutoDL · MiniMax H3";
```

- [ ] Update selectors to consume the server's filtered catalog and fall back only to the three constants while catalog data is loading. Do not expose raw provider IDs or old routes.
- [ ] Preserve reference pickers, character/scene/prop association, prompt optimization toggle, task history, regeneration, selection, and storyboard insertion behavior.
- [ ] Hide unsupported video controls; show duration, aspect ratio, resolution, seed, and compatible generic workflow/instance selection supplied by the H3 route and workflow registry.
- [ ] Run the focused tests returned by `rg -l 'generation/models|routeId' apps/workspace/src --glob '*test*'`; expect PASS.
- [ ] Commit: `feat(ui): route MediaLink image and video generation`.

## Task 5: Render durable queue, reconnect, and import states

**Files:** `GenerationConnectionState.tsx`, `_test.tsx`, task list/detail components found by `rg -n 'retryable|task.status|generation task' apps/workspace/src`

- [ ] Add state mapping tests for `preparing`, `queued`, `submitting`, `submitted`, `running`, `waiting_reconnect`, `importing`, `completed`, `failed`, and `canceled`.
- [ ] Show concise Chinese labels and remediation:

```ts
const stateLabels = {
	preparing: "准备中",
	queued: "排队中",
	submitting: "正在提交",
	submitted: "已提交",
	running: "生成中",
	waiting_reconnect: "等待重连",
	importing: "正在导入素材",
	completed: "已完成",
	failed: "失败",
	canceled: "已取消",
} as const;
```

- [ ] For Codex jobs, show revised prompt when present and a shared-quota disclosure. For AutoDL jobs, show workflow profile/version and prompt ID suffix for diagnostics, but not tunnel endpoint or secrets.
- [ ] Disable manual retry while a durable provider task ID exists and task status is active. Label `submission_outcome_unknown` as requiring the user to inspect ComfyUI before choosing a new submission.
- [ ] Keep the existing generated-asset card, selection, deletion, and storyboard attachment components.
- [ ] Run the focused tests returned by `rg -l 'task.status|retryable' apps/workspace/src --glob '*test*'`; expect PASS.
- [ ] Commit: `feat(ui): show resilient generation task states`.

## Task 6: Run preserved-workflow non-regression tests

**Files:** existing character, scene, prop, storyboard, asset, task, and episode-preview test files only

- [ ] Add or update tests for this full mocked sequence: create project, add character/scene/prop docs, create storyboard, attach ordered references, optimize an H3 prompt, generate image/video task records, import assets, select assets, attach to storyboard, and load episode preview.
- [ ] Assert no existing document/resource relationship is rewritten by either provider. Assert source content and entity documents remain byte-for-byte unchanged after generation.
- [ ] Assert the image and video providers receive the same ordered references displayed by the storyboard editor.
- [ ] Assert legacy route IDs cannot be selected through public catalog/UI, while their Go source packages still compile.
- [ ] Run `go test ./internal/service/generation ./internal/http/handlers -count=1` from `services/server`; expect PASS.
- [ ] Run `pnpm test` from `apps/workspace`; expect PASS.
- [ ] Commit: `test(workflow): protect MediaLink drama production flow`.

## Task 7: Execute repository quality gates

**Files:** only files required to fix failures caused by the preceding tasks

- [ ] Run `task -d packages/core check`; expect PASS.
- [ ] Run `task -d packages/core test`; expect PASS.
- [ ] Run `task -d services/server check`; expect PASS.
- [ ] Run `task -d services/server test`; expect PASS.
- [ ] Run `go build ./...` from `services/server`; expect PASS.
- [ ] Run `pnpm check` from `apps/workspace`; expect PASS.
- [ ] Run `pnpm test` from `apps/workspace`; expect PASS.
- [ ] Run `pnpm build` from `apps/workspace`; expect PASS.
- [ ] Inspect `git diff --check` and `git status --short`; fix only failures introduced by MediaLink changes and preserve unrelated user work.
- [ ] Commit quality-gate-only fixes, if any: `fix(build): satisfy MediaLink quality gates`.

## Task 8: Build and inspect the Apple-Silicon release candidate

**Files:** `README.md`, `docs/release/medialink-macos-arm64-checklist.md`

- [ ] Document prerequisites: macOS Apple Silicon, shared Codex login with image-generation capability, running AutoDL instance, ComfyUI on a configured remote loopback port, and at least one compatible imported workflow validated for the intended route and instance.
- [ ] Document that MediaLink uses a new data root and does not migrate or mutate MediaGo data.
- [ ] Build with `pnpm electron:build:darwin-arm64` from `apps/workspace`; expect MediaLink arm64 DMG and ZIP only.
- [ ] Mount/install the DMG to a temporary location without overwriting any installed app. Launch the temporary app, verify title/icon/settings/catalog, then quit it cleanly.
- [ ] Run `file` on the packaged executable; expect `arm64`. Run `codesign -dv --verbose=4`; expect identifier `app.medialink.desktop`. Run `spctl --assess --type execute`; record expected failure if the local build is unsigned rather than presenting it as notarized.
- [ ] Verify the new data directory is created only under `~/Library/Application Support/MediaLink`; verify the old MediaGo directory timestamp and contents are unchanged.
- [ ] Complete `docs/release/medialink-macos-arm64-checklist.md` with exact artifact names, command results, and unresolved signing/notarization status.
- [ ] Commit: `docs(release): add MediaLink arm64 verification`.

## Task 9: Optional paid end-to-end acceptance after separate approval

**Files:** `docs/release/medialink-macos-arm64-checklist.md`

- [ ] Stop here and ask for explicit authorization before consuming Codex image quota or AutoDL GPU time.
- [ ] After authorization, use one low-cost character image request and verify structured `imageGeneration` completion, revised prompt persistence, asset import, and UI attachment.
- [ ] Use one `4`-second H3 request with a user-approved test profile and reference set. Record the exact ComfyUI prompt ID, observe reconnect-safe polling, download, `ffprobe`, asset import, and episode-preview playback.
- [ ] Never replace an approved formal asset with these acceptance outputs; label them test assets.
- [ ] If either live run fails after submission, recover by its persisted thread/prompt ID; do not start another paid run without renewed approval.
- [ ] Update the checklist with pass/fail evidence but no credentials, private paths containing secrets, or account balances.
- [ ] Commit only the redacted checklist: `test(release): record MediaLink live acceptance`.

## Plan Acceptance

- [ ] Full storyboard and entity context reaches H3 optimization in deterministic order.
- [ ] Valid H3 optimized prompts meet the duration, length, timeline, camera, continuity, reference, negative, and end-state contract.
- [ ] All core drama-production workflows pass non-regression tests with the three visible MediaLink routes.
- [ ] Mocked Codex and H3 restart tests prove no duplicate paid submissions.
- [ ] The packaged app is MediaLink, bundle ID `app.medialink.desktop`, macOS arm64 only, with updater/publisher disabled.
- [ ] No real paid acceptance has run unless the checklist records separate user authorization.
