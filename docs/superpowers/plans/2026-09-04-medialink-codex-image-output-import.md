# MediaLink Codex Image Output Import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Import structured Codex `$imagegen` results from the official `generated_images` directory without weakening MediaLink path validation, and stop a newer terminal attempt from being masked by an older pending attempt.

**Architecture:** `CodexImageProvider` keeps the existing task-attempt directory as its primary output root and adds one narrowly derived fallback root from `CODEX_HOME` (or the current user's `~/.codex`). The fallback accepts only the exact `<thread-id>/<item-id>.<supported-extension>` path returned by the structured Codex result, then reuses the existing descriptor-based file, size, MIME, and image-decoding validation. The resource status reducer displays the newest attempt for a section, so an older orphaned pending task cannot override a newer completed or failed attempt.

**Tech Stack:** Go 1.25, `golang.org/x/sys/unix`, React, TypeScript, Vitest, pnpm, Electron Builder, macOS Apple Silicon.

## Global Constraints

- Do not call Codex image generation while implementing or verifying this fix.
- Do not call the OpenAI Image API.
- Do not modify AutoDL, ComfyUI, or video generation behavior.
- Preserve all existing symlink, regular-file, byte-size, dimension, pixel-count, MIME, and decode checks.
- Accept only a structured Codex result whose saved path matches its exact thread ID and item ID below the effective Codex home.
- Do not move or delete the Codex-generated source file.
- Avoid unrelated refactoring.

---

### Task 1: Accept the exact structured Codex output path

**Files:**
- Modify: `services/server/internal/service/generation/codex_image_provider.go`
- Test: `services/server/internal/service/generation/codex_image_provider_test.go`

**Interfaces:**
- Consumes: `codexapp.ImageGenerationResult{ThreadID, Item.ID, Item.SavedPath}` and the existing `readValidatedCodexImage(path, allowedRoot, maximum, rootLabel)` validator.
- Produces: `(*CodexImageProvider).readResult(result, jobDir) (mimeType string, data []byte, err error)` and `effectiveCodexGeneratedImagesRoot() (string, error)`.

- [ ] **Step 1: Write the failing success-path test**

Add a provider test that sets an isolated `CODEX_HOME`, creates
`generated_images/thread-official/item-official.png`, returns that exact structured result from the session stub, and expects one completed in-memory PNG asset:

```go
func TestCodexImageProviderImportsExactOfficialGeneratedImage(t *testing.T) {
    codexHome := t.TempDir()
    t.Setenv("CODEX_HOME", codexHome)
    savedPath := writeTestPNG(t, filepath.Join(codexHome, "generated_images", "thread-official"), "item-official.png")
    stub := &codexImageSessionStub{
        capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: true},
        generate: func(context.Context, codexapp.ImageGenerationRequest, func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
            return completedCodexImageResult("thread-official", "turn-official", "item-official", savedPath), nil
        },
    }

    response, err := NewCodexImageProvider(stub, t.TempDir()).Generate(context.Background(), codexImageRequest("task-official"))
    if err != nil || response.Status != "completed" || len(response.Assets) != 1 {
        t.Fatalf("Generate() = %#v, %v", response, err)
    }
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run from `services/server` with Go 1.25 and an isolated writable cache:

```bash
GOENV=off GOCACHE=/private/tmp/medialink-go-cache go test ./internal/service/generation -run TestCodexImageProviderImportsExactOfficialGeneratedImage -count=1
```

Expected: FAIL with `Codex image path is outside Codex image job directory`.

- [ ] **Step 3: Add failing boundary cases**

Add table cases proving that an incorrect thread directory, an incorrect item filename, an unsupported extension, a symlinked parent/file, and a path outside `generated_images` are rejected. Each case must return an error and no asset bytes.

```go
tests := []struct {
    name     string
    threadID string
    itemID   string
    arrange  func(codexHome string) string
}{
    {
        name: "wrong thread", threadID: "thread-good", itemID: "item-good",
        arrange: func(codexHome string) string {
            return writeTestPNG(t, filepath.Join(codexHome, "generated_images", "thread-other"), "item-good.png")
        },
    },
    {
        name: "wrong item", threadID: "thread-good", itemID: "item-good",
        arrange: func(codexHome string) string {
            return writeTestPNG(t, filepath.Join(codexHome, "generated_images", "thread-good"), "item-other.png")
        },
    },
    {
        name: "outside root", threadID: "thread-good", itemID: "item-good",
        arrange: func(codexHome string) string {
            return writeTestPNG(t, filepath.Join(codexHome, "outside"), "item-good.png")
        },
    },
}
```

- [ ] **Step 4: Run the boundary test and verify RED**

```bash
GOENV=off GOCACHE=/private/tmp/medialink-go-cache go test ./internal/service/generation -run 'TestCodexImageProvider(ImportsExactOfficialGeneratedImage|RejectsInvalidOfficialGeneratedImagePath)' -count=1
```

Expected: the valid official-output case still fails because fallback support is absent; the existing arbitrary outside-path case remains rejected.

- [ ] **Step 5: Implement the minimal trusted-root fallback**

Add a `codexOutputRoot` field initialized from `CODEX_HOME` or `os.UserHomeDir()`, then implement a narrow selector before calling the existing validator:

```go
func (provider *CodexImageProvider) readResult(result codexapp.ImageGenerationResult, jobDir string) (string, []byte, error) {
    savedPath := strings.TrimSpace(*result.Item.SavedPath)
    _, mimeType, data, err := readValidatedCodexImage(savedPath, jobDir, maxCodexImageOutputBytes, "Codex image job directory")
    if err == nil {
        return mimeType, data, nil
    }
    if officialErr := validateOfficialCodexImagePath(savedPath, provider.codexOutputRoot, result.ThreadID, result.Item.ID); officialErr != nil {
        return "", nil, err
    }
    _, mimeType, data, err = readValidatedCodexImage(savedPath, filepath.Join(provider.codexOutputRoot, result.ThreadID), maxCodexImageOutputBytes, "Codex generated image directory")
    return mimeType, data, err
}
```

`validateOfficialCodexImagePath` must reject unsafe/empty path segments, require the exact canonical thread parent, require a supported lowercase-normalized extension, and require the filename stem to equal the item ID. Do not catch errors by comparing error strings and do not add a general second arbitrary root.

- [ ] **Step 6: Run focused and package tests and verify GREEN**

```bash
GOENV=off GOCACHE=/private/tmp/medialink-go-cache go test ./internal/service/generation -run TestCodexImageProvider -count=1
GOENV=off GOCACHE=/private/tmp/medialink-go-cache go test ./internal/service/generation -count=1
```

Expected: PASS, including the existing outside-root and swap-resistance tests.

- [ ] **Step 7: Commit Task 1**

```bash
git add services/server/internal/service/generation/codex_image_provider.go services/server/internal/service/generation/codex_image_provider_test.go
git commit -m "fix(generation): import structured Codex image outputs"
```

---

### Task 2: Let the newest section attempt determine the visible status

**Files:**
- Modify: `apps/workspace/src/domains/generation/lib/resource-generation-status.ts`
- Test: `apps/workspace/src/domains/generation/lib/resource-generation-status.test.ts`

**Interfaces:**
- Consumes: `GenerationTask.createdAt`, `GenerationTask.updatedAt`, and `resourceGenerationStatusFromTask(task)`.
- Produces: unchanged `generationStatusForSection(tasks, documentId, sectionId)` API with newest-attempt semantics.

- [ ] **Step 1: Change the regression test to the required terminal behavior**

Replace the test that says an older in-progress task wins with:

```ts
it("lets a newer terminal attempt supersede an older pending attempt", () => {
  const status = generationStatusForSection([
    makeTask({ id: "old-pending", documentId: "doc-1", sectionId: "sec-1", status: "running", createdAt: "2026-01-01T00:00:00.000Z" }),
    makeTask({ id: "new-failed", documentId: "doc-1", sectionId: "sec-1", status: "failed", error: "boom", createdAt: "2026-01-02T00:00:00.000Z" }),
  ], "doc-1", "sec-1");
  expect(status).toMatchObject({ taskId: "new-failed", kind: "failed", message: "boom" });
});
```

- [ ] **Step 2: Run the focused test and verify RED**

```bash
pnpm -C apps/workspace test -- --run src/domains/generation/lib/resource-generation-status.test.ts
```

Expected: FAIL because `latestActiveOrRecentGenerationTask` currently always prefers any pending task.

- [ ] **Step 3: Implement newest-attempt selection**

Make `generationStatusForSection` select the newest matching task instead of globally prioritizing pending statuses. Order attempts by `createdAt`; use `updatedAt` only as a deterministic tie-breaker. Keep status normalization and visible completed-status behavior unchanged.

```ts
const latestGenerationAttempt = (tasks: GenerationTask[]) =>
  tasks.reduce<GenerationTask | undefined>((latest, task) => {
    if (!latest) return task;
    const createdDifference = Date.parse(task.createdAt || "") - Date.parse(latest.createdAt || "");
    if (createdDifference !== 0) return createdDifference > 0 ? task : latest;
    return generationTaskTime(task) >= generationTaskTime(latest) ? task : latest;
  }, undefined);
```

- [ ] **Step 4: Run focused workspace tests and verify GREEN**

```bash
pnpm -C apps/workspace test -- --run src/domains/generation/lib/resource-generation-status.test.ts src/domains/generation/hooks/useResourceGenerationStatuses.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit Task 2**

```bash
git add apps/workspace/src/domains/generation/lib/resource-generation-status.ts apps/workspace/src/domains/generation/lib/resource-generation-status.test.ts
git commit -m "fix(generation): converge resource status on newest attempt"
```

---

### Task 3: Verify, package, install, and recover the existing image

**Files:**
- No production source changes expected.
- Build outputs: `apps/workspace/release/mac-arm64/MediaLink.app`, `apps/workspace/release/MediaLink-0.1.0-beta.0-macos-arm64.dmg`, and ZIP artifact.

**Interfaces:**
- Consumes: the completed Task 1 and Task 2 commits, the existing Electron build pipeline, and the already-created Codex PNG.
- Produces: `/Applications/MediaLink.app` running the fixed arm64 build; the existing PNG remains intact and is available for MediaLink asset import without a new image turn.

- [ ] **Step 1: Run fresh verification**

```bash
GOENV=off GOCACHE=/private/tmp/medialink-go-cache go test ./internal/service/generation -count=1
pnpm -C apps/workspace test -- --run src/domains/generation/lib/resource-generation-status.test.ts src/domains/generation/hooks/useResourceGenerationStatuses.test.ts
pnpm -C apps/workspace build
git diff --check
```

Expected: all commands PASS and `git status --short` contains only the user-owned `work/` directory.

- [ ] **Step 2: Build the Apple Silicon desktop package**

Prepare a temporary Go 1.25 toolchain when `go` is unavailable, then run the repository's existing `darwin-arm64` stages without adding Windows targets or changing version metadata:

```bash
mkdir -p /private/tmp/medialink-go-1.25.0
curl -fL https://go.dev/dl/go1.25.0.darwin-arm64.tar.gz -o /private/tmp/medialink-go-1.25.0/go1.25.0.darwin-arm64.tar.gz
tar -C /private/tmp/medialink-go-1.25.0 -xzf /private/tmp/medialink-go-1.25.0/go1.25.0.darwin-arm64.tar.gz
pnpm -C apps/workspace build
node scripts/sync-workspace-dist.mjs
PATH="/private/tmp/medialink-go-1.25.0/go/bin:$PATH" GOENV=off GOCACHE=/private/tmp/medialink-go-cache node scripts/build-server-target.mjs darwin-arm64
node apps/workspace/scripts/stage-electron.ts codex darwin-arm64 mediago,openrouter,dmxapi http://127.0.0.1:3001/api/v1 dreamina,libtv,pippit
pnpm -C apps/workspace electron:build:darwin-arm64
```

- [ ] **Step 3: Install recoverably**

Move the current `/Applications/MediaLink.app` to a timestamped path under `/private/tmp`, copy the new app into `/Applications`, and ad-hoc sign the installed app if the unsigned development build requires it. Do not delete the backup.

- [ ] **Step 4: Verify the installed app**

```bash
codesign --verify --deep --strict /Applications/MediaLink.app
file /Applications/MediaLink.app/Contents/MacOS/MediaLink
file /Applications/MediaLink.app/Contents/Resources/bin/mediago-server
```

Launch once and confirm the app process is running and `mediago-server` listens on `127.0.0.1:48273`. A `401` from an unauthenticated local health request proves the authenticated listener is present; it is not a health failure.

- [ ] **Step 5: Validate the existing generated PNG without regenerating**

Confirm the exact file still exists, is a regular file below the expected thread/item path, and passes the same MIME/dimension limits. Then expose it through the existing MediaLink asset-import path or, if no safe existing history-recovery API exists, leave the original failed task unchanged and report the clickable file for drag-import. Do not call `GenerateImage`, retry the failed task, or create a new Codex turn.

- [ ] **Step 6: Final repository check**

```bash
git status --short
git log -3 --oneline
```

Expected: implementation commits are present; no unrelated tracked changes exist; user-owned `work/` remains untouched.
