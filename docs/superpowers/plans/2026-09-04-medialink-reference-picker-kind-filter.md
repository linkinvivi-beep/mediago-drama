# MediaLink Reference Picker Kind Filter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ensure every reference picker displays only media kinds accepted by the current generation route, so an image-only picker never lists audio or video.

**Architecture:** Keep the fix inside the existing shared `ReferenceSelectionDialog`. Derive the visible kind filters and visible options from `selectableKinds`, while preserving any narrower explicit `visibleKindFilters` supplied by a caller. No API, database, asset, or route-schema changes are required.

**Tech Stack:** React 19, TypeScript, Vitest, Testing Library, pnpm, Electron Builder

## Global Constraints

- Do not delete, migrate, or reclassify any existing media asset.
- Preserve mixed image/video/audio reference selection for routes that explicitly support those kinds.
- Do not modify media APIs, database schemas, upload flows, or generation route configuration.
- Avoid unrelated refactors.

---

### Task 1: Filter the shared reference picker by supported media kind

**Files:**
- Modify: `apps/workspace/src/domains/generation/components/ReferenceSelectionDialog.tsx`
- Test: `apps/workspace/src/domains/generation/components/MediaGenerationDialogs.test.tsx`

**Interfaces:**
- Consumes: `ReferenceSelectionDialogProps.selectableKinds: Set<MediaAsset["kind"]>` and optional `visibleKindFilters?: ReferenceKindFilter[]`.
- Produces: a picker whose tabs, counts, ordinary options, and shortcut groups contain only kinds present in `selectableKinds`.

- [ ] **Step 1: Write the failing regression test**

Add a test that passes one image, one video, and one audio asset without an explicit `visibleKindFilters`, while limiting `selectableKinds` to `new Set(["image"])`:

```tsx
it("hides media kinds that the current route cannot use as references", () => {
  render(
    <ReferenceSelectionDialog
      disabled={false}
      entries={[]}
      inputId="reference-upload"
      isUploading={false}
      mediaAssets={[
        mediaAsset(),
        mediaAsset({
          id: "video-1",
          filename: "scene.mp4",
          kind: "video",
          mimeType: "video/mp4",
          url: "/api/v1/media-assets/video-1/content",
        }),
        mediaAsset({
          id: "audio-1",
          filename: "audio.mp3",
          kind: "audio",
          mimeType: "audio/mpeg",
          url: "/api/v1/media-assets/audio-1/content",
        }),
      ]}
      open
      references={[]}
      requiresReference={false}
      selectableKinds={new Set(["image"])}
      selectedAssetIds={[]}
      onOpenChange={vi.fn()}
      onImportFiles={vi.fn()}
      onRemoveReference={vi.fn()}
      onToggleReference={vi.fn()}
    />,
  );

  expect(screen.getByText("still.png")).toBeTruthy();
  expect(screen.queryByText("scene.mp4")).toBeNull();
  expect(screen.queryByText("audio.mp3")).toBeNull();
  expect(screen.queryByRole("tab", { name: /视频/ })).toBeNull();
  expect(screen.queryByRole("tab", { name: /音频/ })).toBeNull();
});
```

Update the existing audio playback test so it declares `selectableKinds={new Set(["audio"])}`; that test continues to verify the audio preview when audio is genuinely supported.

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
pnpm -C apps/workspace test -- MediaGenerationDialogs.test.tsx
```

Expected: the new test fails because `scene.mp4` and `audio.mp3` are still rendered by the default `all` filter.

- [ ] **Step 3: Implement the minimal shared filter**

In `ReferenceSelectionDialog.tsx`, derive the supported kind filters by intersecting the caller's optional visible filters with `selectableKinds`. Use the supported option list for counts and rendering, and apply the same supported-kind check to shortcut items:

```tsx
const kindFilters = useMemo(
  () => normalizeReferenceKindFilters(visibleKindFilters, selectableKinds),
  [selectableKinds, visibleKindFilters],
);
const options = useMemo(
  () =>
    buildGeneratedReferenceOptions(entries, mediaAssets).filter((option) =>
      selectableKinds.has(option.kind),
    ),
  [entries, mediaAssets, selectableKinds],
);
```

`normalizeReferenceKindFilters` must retain `all` only when more than one supported reference kind is visible, and must preserve a caller's narrower explicit list after intersection.

- [ ] **Step 4: Run the focused test and verify GREEN**

Run:

```bash
pnpm -C apps/workspace test -- MediaGenerationDialogs.test.tsx
```

Expected: all tests in the file pass, including mixed-kind tabs, image-only filtering, and audio playback.

- [ ] **Step 5: Run adjacent generation tests and type/build verification**

Run:

```bash
pnpm -C apps/workspace test -- GenerationSettingsForm.test.tsx MediaGenerationWorkspace.test.tsx GenerationWorkspace.imageSpec.test.tsx
pnpm -C apps/workspace build
git diff --check
```

Expected: all tests pass, the workspace production build succeeds, and `git diff --check` prints no errors.

- [ ] **Step 6: Commit the code fix**

```bash
git add apps/workspace/src/domains/generation/components/ReferenceSelectionDialog.tsx apps/workspace/src/domains/generation/components/MediaGenerationDialogs.test.tsx
git commit -m "fix(generation): hide unsupported reference media"
```

### Task 2: Package and install the verified macOS Apple Silicon build

**Files:**
- Build output: `apps/workspace/release/mac-arm64/MediaLink.app`
- Installed app: `/Applications/MediaLink.app`

**Interfaces:**
- Consumes: the verified workspace frontend and existing staged arm64 service binary.
- Produces: an ad-hoc-signed `/Applications/MediaLink.app` containing the fixed reference picker.

- [ ] **Step 1: Build the arm64 Electron application**

Run:

```bash
pnpm -C apps/workspace electron:build:darwin-arm64
```

Expected: Electron Builder produces `apps/workspace/release/mac-arm64/MediaLink.app` successfully.

- [ ] **Step 2: Verify architecture and staged service integrity**

Run:

```bash
file apps/workspace/release/mac-arm64/MediaLink.app/Contents/MacOS/MediaLink
shasum -a 256 bin/darwin-arm64/mediago-server apps/workspace/electron/resources/bin/mediago-server apps/workspace/release/mac-arm64/MediaLink.app/Contents/Resources/bin/mediago-server
```

Expected: the app executable is `arm64`, and all three service hashes match.

- [ ] **Step 3: Install recoverably and sign**

Close only the running MediaLink process, move the current `/Applications/MediaLink.app` to a uniquely named backup under `/private/tmp`, copy the new bundle into `/Applications`, and ad-hoc sign it with:

```bash
codesign --force --deep --sign - /Applications/MediaLink.app
```

- [ ] **Step 4: Launch and smoke-check**

Open `/Applications/MediaLink.app`, confirm its local service starts, and verify the installed bundle executable remains arm64 and passes `codesign --verify --deep --strict`.

- [ ] **Step 5: Push only to the user's fork**

```bash
git push fork feat/medialink-implementation
```

Expected: the new design and fix commits are present only on `linkinvivi-beep/mediago-drama` branch `feat/medialink-implementation`.
