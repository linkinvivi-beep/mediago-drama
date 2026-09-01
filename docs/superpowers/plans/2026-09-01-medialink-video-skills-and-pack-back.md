# MediaLink Video Skills and Pack Back Button Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make MediaLink's storyboard/video skills route-neutral and single-shot oriented, preserve route-aware video prompt optimization, and add a reliable return button to the dedicated prompt-pack editor window.

**Architecture:** Keep the existing generation form, confirmation, route, background task, workflow, and asset pipelines unchanged. Refine only the built-in instruction assets and generic video optimizer instruction, then add a renderer-level close-with-settings-fallback action to the existing standalone prompt-pack editor.

**Tech Stack:** Go 1.25, embedded Markdown instruction assets, React 19, TypeScript, React Router, Vitest, Testing Library, Electron, pnpm.

## Global Constraints

- macOS Apple Silicon only.
- Do not run real image, video, AutoDL, ComfyUI, or cloud GPU generation.
- Do not hardcode MiniMax H3, Seedance, ComfyUI nodes, or provider parameters into the route-neutral storyboard/video skills.
- Preserve `generation_settings`, confirmation selection, `generate_media`, `generate_media_batch`, background execution, notification, and asset persistence contracts.
- Do not modify workflow import, SSH, Keychain, scheduler, recovery, or media association code.
- Do not modify or stage the existing untracked `work/` directory.
- Do not create or update an upstream `mediago-dev` pull request.

---

### Task 1: Route-neutral storyboard and concise video-generation skills

**Files:**
- Modify: `packages/instructions/pkg/pack/builtin/assets/skills/storyboard-writer.skill.md`
- Modify: `packages/instructions/pkg/pack/builtin/assets/skills/video-generation.skill.md`
- Modify: `packages/instructions/pkg/pack/builtin/assets/prompts/video-cinematic-shot.md`
- Modify: `packages/instructions/pkg/pack/builtin/assets/prompts/video-product-orbit.md`
- Test: `packages/instructions/pkg/pack/builtin/builtin_test.go`

**Interfaces:**
- Consumes: existing embedded `pack.Builtin(context.Context)` asset loader.
- Produces: unchanged skill slugs `storyboard-writer` and `video-generation`, and unchanged prompt IDs `video-cinematic-shot` and `video-product-orbit`.

- [ ] **Step 1: Write failing semantic tests**

Add expectations to `builtin_test.go` that the storyboard and video skills contain the new single-shot contract and do not contain model/quality hardcoding:

```go
for _, fragment := range []string{
    "一镜一条",
    "一个主要叙事动作",
    "一个主要运镜",
    "明确的结束状态",
    "所选 route",
} {
    if !strings.Contains(body, fragment) {
        t.Fatalf("storyboard-writer missing route-neutral rule %q", fragment)
    }
}
for _, fragment := range []string{"Seedance 2.0", "15.00", "8K", "HDR10+", "120fps", "伦勃朗光为主"} {
    if strings.Contains(body, fragment) {
        t.Fatalf("storyboard-writer contains fixed model or rendering rule %q", fragment)
    }
}
```

Add video skill/prompt checks for `MediaLink MCP`, initial state, one action, one camera move, end state, continuity, and the explicit batch order. Reject `MediaGo Drama MCP` and conflicting `环绕或推近` wording in the orbit preset.

- [ ] **Step 2: Run the focused Go test and verify RED**

Run:

```bash
/private/tmp/medialink-go-1.25.0/go/bin/go test ./pkg/pack/builtin -run 'Test(Storyboard|Video)' -count=1
```

Expected: FAIL because the current assets still contain Seedance/15-second/8K/HDR/120fps defaults and lack the complete route-neutral single-shot wording.

- [ ] **Step 3: Rewrite only the affected instruction assets**

Replace `storyboard-writer.skill.md` with a compact route-neutral contract:

```markdown
- 默认一镜一条；一个 section 对应一个可独立生成的镜头。
- 每镜只承载一个主要叙事动作、一个主要运镜和一个明确的结束状态。
- 时长、比例、分辨率、音频和模型专属参数在生成时由所选 route 的统一设置表单确认。
```

Condense `video-generation.skill.md` while retaining confirmation, exact settings snapshot, one-call batch, async handoff, document context, and notification rules. Define this prompt shape:

```markdown
初始状态 → 主体动作及时间推进 → 一个主要运镜及速度 → 明确结束状态 → 连续性锚点 → 台词/声音 → 必要禁止项
```

Change branding to `MediaLink MCP`. Update the two prompt assets so the cinematic preset enforces one coherent shot and the orbit preset uses one slow orbit without a second competing push-in.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run the same focused Go command. Expected: PASS.

- [ ] **Step 5: Commit Task 1**

```bash
git add packages/instructions/pkg/pack/builtin/assets/skills/storyboard-writer.skill.md packages/instructions/pkg/pack/builtin/assets/skills/video-generation.skill.md packages/instructions/pkg/pack/builtin/assets/prompts/video-cinematic-shot.md packages/instructions/pkg/pack/builtin/assets/prompts/video-product-orbit.md packages/instructions/pkg/pack/builtin/builtin_test.go
git commit -m "fix(skills): make video prompts route neutral"
```

### Task 2: Preserve a strong generic video optimizer instruction

**Files:**
- Modify: `services/server/internal/service/generation/generation_runtime_prompt_optimize.go`
- Test: `services/server/internal/service/generation/generation_runtime_prompt_optimize_test.go`

**Interfaces:**
- Consumes: existing `promptOptimizationSystemInstruction(kind coregeneration.Kind, promptGuide ...string) string`, route-aware `promptOptimizationSystemInstructionForTarget`, H3 duration/aspect/resolution context, and ordered-reference manifest.
- Produces: a new private constant `videoPromptOptimizationSystemInstructionText` plus a private bounded target-model hint; no public API or request schema changes.

- [ ] **Step 1: Write failing instruction-selection tests**

Add a unit test that calls the real private helper from the same Go package:

```go
func TestPromptOptimizationSystemInstructionUsesSingleShotVideoContract(t *testing.T) {
    instruction := promptOptimizationSystemInstruction(coregeneration.KindVideo)
    for _, required := range []string{"单个连贯镜头", "一个主要动作", "一个主要运镜", "起始状态", "结束状态", "连续性"} {
        if !strings.Contains(instruction, required) {
            t.Fatalf("video instruction missing %q: %q", required, instruction)
        }
    }
    if strings.Contains(instruction, "MiniMax H3") {
        t.Fatalf("generic video instruction must remain route-neutral: %q", instruction)
    }
}
```

Extend the H3 assertion to prove the H3 instruction still contains its route-specific guidance and also inherits the generic single-shot contract.

Add a helper-level assertion that a non-empty target model such as `custom-video-model` appears only in the optimizer system instruction, while internal document/asset IDs and reference URLs remain absent. Keep the existing ordered-reference manifest test as the reference-count proof.

- [ ] **Step 2: Run the focused service test and verify RED**

Run:

```bash
/private/tmp/medialink-go-1.25.0/go/bin/go test ./internal/service/generation -run 'TestPromptOptimizationSystemInstructionUsesSingleShotVideoContract' -count=1
```

Expected: FAIL because non-H3 video currently receives only the generic all-media instruction.

- [ ] **Step 3: Add the minimal generic video instruction**

Define `videoPromptOptimizationSystemInstructionText` by extending the existing protected base instruction with route-neutral video rules. Make `h3PromptOptimizationSystemInstructionText` extend this video instruction, and update `promptOptimizationSystemInstruction`:

```go
if kind == coregeneration.KindVideo {
    return videoPromptOptimizationSystemInstructionText
}
```

Add a bounded private `_mediago_prompt_optimization_target_model` parameter populated from `generationPayload.Model`, append its value to the video optimizer instruction when non-empty, and delete it before the text provider receives public params. Do not serialize document IDs, section IDs, asset IDs, reference URLs, notification targets, workflow JSON, or confirmation IDs.

Do not otherwise change the existing target kind/route/settings envelope, H3 confirmed duration/aspect/resolution summary, ordered-reference manifest, protected-body validation, route resolution, or generation provider payload.

- [ ] **Step 4: Run focused and package tests and verify GREEN**

Run:

```bash
/private/tmp/medialink-go-1.25.0/go/bin/go test ./internal/service/generation -run 'Test(PromptOptimizationSystemInstruction|CodexImagePromptOptimization)' -count=1
```

Expected: PASS, with existing image optimization behavior unchanged.

- [ ] **Step 5: Commit Task 2**

```bash
git add services/server/internal/service/generation/generation_runtime_prompt_optimize.go services/server/internal/service/generation/generation_runtime_prompt_optimize_test.go
git commit -m "fix(prompts): strengthen generic video optimization"
```

### Task 3: Add a return button to the standalone prompt-pack editor

**Files:**
- Modify: `apps/workspace/src/pages/PromptPackEditor.tsx`
- Test: `apps/workspace/src/pages/PromptPackEditor.test.tsx`

**Interfaces:**
- Consumes: Electron's existing `window.close()` close-request path and React Router's `useNavigate()`.
- Produces: renderer callback `returnToSettings(): void`; no new IPC.

- [ ] **Step 1: Write failing UI tests**

In the Electron test fixture, spy on the real window close function and assert the new button closes the dedicated editor:

```tsx
it("returns from the dedicated editor by closing its Electron window", async () => {
    const close = vi.spyOn(window, "close").mockImplementation(() => undefined);
    renderEditor();
    fireEvent.click(await screen.findByRole("button", { name: "返回设置" }));
    expect(close).toHaveBeenCalledTimes(1);
});
```

Add a browser fallback test with `window.mediagoDesktop` removed and no opener. Render a location probe beside the editor, click `返回设置`, and assert the router location becomes `/settings`.

- [ ] **Step 2: Run the focused Vitest file and verify RED**

Run:

```bash
pnpm test src/pages/PromptPackEditor.test.tsx
```

Expected: FAIL because no `返回设置` button exists.

- [ ] **Step 3: Add the minimal close/fallback implementation**

Import `ChevronLeft` and `useNavigate`. Add:

```tsx
const navigate = useNavigate();
const returnToSettings = () => {
    if (window.mediagoDesktop?.isElectron || window.opener) {
        window.close();
        return;
    }
    navigate("/settings");
};
```

Render a visible outline/ghost button labeled `返回设置` immediately before the existing header title. Preserve `data-desktop-no-drag` so clicking it never starts a window drag.

- [ ] **Step 4: Run the focused UI test and verify GREEN**

Run the same focused Vitest command. Expected: PASS.

- [ ] **Step 5: Commit Task 3**

```bash
git add apps/workspace/src/pages/PromptPackEditor.tsx apps/workspace/src/pages/PromptPackEditor.test.tsx
git commit -m "fix(workspace): add prompt pack return action"
```

### Task 4: Full verification, packaging, installation, and fork-only delivery

**Files:**
- Modify only if final values changed: `docs/release/medialink-macos-arm64-checklist.md`

**Interfaces:**
- Consumes: completed Tasks 1-3.
- Produces: verified arm64 DMG/ZIP and installed `/Applications/MediaLink.app`.

- [ ] **Step 1: Run full source quality gates**

```bash
cd packages/core && /private/tmp/medialink-go-1.25.0/go/bin/go test ./... -count=1 -timeout=5m
cd services/server && /private/tmp/medialink-go-1.25.0/go/bin/go vet ./... && /private/tmp/medialink-go-1.25.0/go/bin/go test ./... -count=1 -timeout=5m
cd apps/workspace && pnpm check && pnpm test
git diff --check
```

Expected: all commands exit 0; no generated workflow is submitted.

- [ ] **Step 2: Rebuild and package current arm64 services**

```bash
env PATH=/private/tmp/medialink-go-1.25.0/go/bin:/usr/bin:/bin:/usr/sbin:/sbin /Users/jialiankun/.local/bin/node scripts/build-server-target.mjs darwin-arm64
/Users/jialiankun/.local/bin/node apps/workspace/scripts/stage-electron.ts codex darwin-arm64 'mediago,openrouter,dmxapi' '' dreamina
pnpm electron:build:darwin-arm64
```

Expected: the staged-binary guard passes and the arm64 DMG/ZIP are produced.

- [ ] **Step 3: Verify artifacts before installation**

Run `hdiutil verify`, `shasum -a 256`, and `file` on the application plus all three Go sidecars. Update the release checklist with the exact new hashes and final test count, then run `git diff --check`.

- [ ] **Step 4: Install and verify the UI without generation**

Quit the existing MediaLink process, move the previous app to a timestamped `/private/tmp` backup, install the new app with `ditto`, and launch `/Applications/MediaLink.app`. In the real app, verify:

- `技能包管理` shows `返回设置` and clicking it closes the editor window back to Settings.
- `Codex 接入` still reports `Codex 生图已就绪` after `刷新并测试`.
- No image/video generation task is created.

- [ ] **Step 5: Commit release record and push only the user fork**

```bash
git add docs/release/medialink-macos-arm64-checklist.md
git commit -m "docs(release): record video skill build"
git push fork feat/medialink-implementation
```

Confirm `git status --short` contains only the pre-existing `?? work/`. Do not push `origin` and do not create a pull request.
