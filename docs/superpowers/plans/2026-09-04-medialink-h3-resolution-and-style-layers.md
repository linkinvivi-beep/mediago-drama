# MediaLink H3 Resolution and Style Layers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the AutoDL MiniMax H3 video route expose its real 768p/1080p outputs, automatically apply the H3 prompt-writing contract, and let users add one compatible video photography style without mixing image-only presets into the video form.

**Architecture:** Keep the generation route as the source of truth for model-specific behavior. The server automatically layers the H3 instruction over the user-selected style reference; the prompt pack supplies only user-facing video photography styles. Reuse existing route schema normalization, prompt-pack storage, and generation settings controls rather than adding a new database or routing system.

**Tech Stack:** Go, React 19, TypeScript, Vitest, Go testing, pnpm, Electron Builder for macOS Apple Silicon

## Global Constraints

- Support macOS Apple Silicon only.
- Preserve the existing character, scene, prop, storyboard, generation, and AutoDL workflow flows.
- Do not add a second model selector or make users manually select the H3 adapter.
- Do not delete saved preferences; normalize them against the active route schema.
- Do not submit a paid cloud generation while verifying this change.
- Avoid unrelated refactors and do not touch the user-owned untracked `work/` directory.

---

### Task 1: Match the H3 resolution contract to the cloud workflow

**Files:**
- Modify: `packages/core/pkg/generation/catalog_medialink_test.go`
- Modify: `packages/core/pkg/generation/catalog_medialink.go`
- Create: `services/server/internal/service/generation/autodl_h3_provider_dimensions_test.go`
- Modify: `services/server/internal/service/generation/autodl_h3_provider.go`
- Modify: `services/server/internal/service/generation/generation_runtime_prompt_optimize_test.go`
- Modify: `services/server/internal/service/generation/generation_runtime_prompt_optimize.go`
- Modify: `docs/generation-route-protocol.md`

**Interfaces:**
- Consumes: canonical H3 `resolution` parameter and `aspectRatio` parameter.
- Produces: exact H3 choices `768p` and `1080p`; 16:9 dimensions `1344x768` and `1920x1080`; reversed dimensions for 9:16.

- [ ] **Step 1: Write failing Go tests**

Assert that the H3 route resolution values are exactly `768p` and `1080p`, labels include the concrete 16:9 size, the default remains `1080p`, and `autoDLH3Dimensions` maps 16:9 and 9:16 correctly while rejecting `2K`, `3K`, and legacy `720p`.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./pkg/generation -run 'TestMediaLinkH3CanonicalOptions' -count=1
go test ./internal/service/generation -run 'TestAutoDLH3Dimensions|TestH3PromptOptimizationInstructionExtendsGenericVideoContract' -count=1
```

Expected: failures show that the route and provider still use `720p`.

- [ ] **Step 3: Implement the minimal route and provider change**

Change only the AutoDL H3 catalog and provider validation from `720p` to `768p`. Map the 16:9 low tier to `1344x768`, 9:16 to `768x1344`, and 1:1 to `768x768`. Keep 1080p mappings unchanged. Update H3 optimizer resolution validation and document `768p` as a route-specific canonical value.

- [ ] **Step 4: Verify GREEN**

Run the same focused Go tests and expect all to pass.

### Task 2: Complete and expose the automatic H3 prompt adapter

**Files:**
- Modify: `services/server/internal/service/generation/generation_runtime_prompt_optimize_test.go`
- Modify: `services/server/internal/service/generation/generation_runtime_prompt_optimize.go`
- Modify: `apps/workspace/src/domains/generation/components/GenerationSettingsForm.test.tsx`
- Modify: `apps/workspace/src/domains/generation/components/GenerationSettingsForm.tsx`

**Interfaces:**
- Consumes: selected route ID `autodl.minimax-h3`, target duration/aspect ratio/resolution, reference order, and one selected style reference.
- Produces: an automatically applied H3 system instruction plus a visible, read-only `MiniMax H3 官方提示词规则（自动）` status in the optimization section.

- [ ] **Step 1: Write failing server and UI tests**

Extend the H3 instruction test to require ordered reference-role mapping, a complete target timeline without gaps or overlaps, micro action beats, previous-end/next-start continuity, and explicit dialogue/audio synchronization only when supplied. Add a video-form render test requiring the automatic H3 adapter label and requiring it not to appear for an image route.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./internal/service/generation -run 'TestH3PromptOptimizationInstructionExtendsGenericVideoContract' -count=1
pnpm -C apps/workspace test -- GenerationSettingsForm.test.tsx
```

Expected: the new H3 fragments and UI status are absent.

- [ ] **Step 3: Implement the H3-only additions**

Add only prompt-authoring rules from the installed official H3 skill; do not copy CLI authentication, submission, polling, or download instructions. In the form, derive the adapter status from `controller.selectedRoute.id === "autodl.minimax-h3"`; display it inside the prompt optimization section and explain that enabling optimization applies it together with the selected photography style.

- [ ] **Step 4: Verify GREEN**

Run the same focused Go and Vitest commands and expect all to pass.

### Task 3: Add media-compatible video photography styles

**Files:**
- Modify: `packages/instructions/pkg/pack/builtin/assets/pack.json`
- Create: `packages/instructions/pkg/pack/builtin/assets/prompts/video-film-realism.md`
- Create: `packages/instructions/pkg/pack/builtin/assets/prompts/video-hong-kong-neon-romance.md`
- Create: `packages/instructions/pkg/pack/builtin/assets/prompts/video-cool-crime.md`
- Create: `packages/instructions/pkg/pack/builtin/assets/prompts/video-naturalist-handheld.md`
- Create: `packages/instructions/pkg/pack/builtin/assets/prompts/video-neo-noir.md`
- Create: `packages/instructions/pkg/pack/builtin/assets/prompts/video-dream-soft-focus.md`
- Create: `packages/instructions/pkg/pack/builtin/assets/prompts/video-symmetrical-deadpan.md`
- Create: `packages/instructions/pkg/pack/builtin/assets/prompts/video-poetic-road-movie.md`
- Modify: `packages/instructions/pkg/pack/builtin/builtin_test.go`
- Modify: `apps/workspace/src/domains/generation/components/PromptSlashCommand.tsx`
- Modify: `apps/workspace/src/domains/generation/lib/prompt-insertions.ts`
- Modify: `apps/workspace/src/domains/generation/lib/prompt-insertions.test.ts`
- Modify: `apps/workspace/src/domains/generation/hooks/useGenerationWorkspace.ts`
- Modify: `apps/workspace/src/domains/generation/hooks/useGenerationSettingsForm.ts`
- Modify: `apps/workspace/src/domains/generation/hooks/useGenerationSettingsForm.test.tsx`

**Interfaces:**
- Consumes: existing optional prompt preset `type: image | video` metadata and the generation form `kind`.
- Produces: category `视频摄影风格`, eight video-only style presets, and prompt picker items filtered so video-only presets never appear in image forms and image-only presets never appear in video forms.

- [ ] **Step 1: Write failing pack and frontend tests**

Require eight `type: video` entries in category `video-style`, verify the Hong Kong neon preset expands concrete photographic traits instead of relying on a director name, propagate preset type into `PromptInsertItem`, and verify image/video form filtering preserves untyped shared styles.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./pkg/pack/builtin -run 'TestBuiltinVideoPhotographyStyles' -count=1
pnpm -C apps/workspace test -- prompt-insertions.test.ts useGenerationSettingsForm.test.tsx
```

Expected: the category, presets, type propagation, and filtering are missing.

- [ ] **Step 3: Implement the prompt-pack and compatibility filter**

Add the category and eight concise style files. Preserve `type` in `PromptInsertItem`, filter prompt references with `!item.type || item.type === kind`, and keep the existing single-select optimizer plus multi-select supplements unchanged.

- [ ] **Step 4: Verify GREEN**

Run the same focused Go and Vitest commands and expect all to pass.

### Task 4: Full verification and macOS Apple Silicon installation

**Files:**
- Build output: `apps/workspace/release/mac-arm64/MediaLink.app`
- Installed output: `/Applications/MediaLink.app`

**Interfaces:**
- Consumes: Tasks 1-3.
- Produces: a verified arm64 MediaLink application without launching any paid media generation.

- [ ] **Step 1: Run focused and adjacent verification**

```bash
go test ./pkg/generation ./pkg/pack/builtin -count=1
go test ./internal/service/generation -count=1
pnpm -C apps/workspace test -- GenerationSettingsForm.test.tsx useGenerationSettingsForm.test.tsx prompt-insertions.test.ts
pnpm -C apps/workspace build
git diff --check
```

- [ ] **Step 2: Build the arm64 application**

```bash
pnpm -C apps/workspace electron:build:darwin-arm64
file apps/workspace/release/mac-arm64/MediaLink.app/Contents/MacOS/MediaLink
```

Expected: the Electron application builds and its executable reports `arm64`.

- [ ] **Step 3: Install recoverably**

Close only MediaLink, move the current `/Applications/MediaLink.app` to a uniquely named backup under `/private/tmp`, copy the new application into `/Applications`, and apply an ad-hoc signature with `codesign --force --deep --sign - /Applications/MediaLink.app`.

- [ ] **Step 4: Launch and smoke-check without cloud generation**

Open MediaLink, confirm the process remains alive, confirm the packaged server is arm64, and inspect the generation catalog/tests rather than submitting a paid H3 task.

- [ ] **Step 5: Review the final diff**

Confirm no files outside this plan changed, `work/` remains untouched, and no credential, generated media, workflow JSON, or cloud instance state was modified.
