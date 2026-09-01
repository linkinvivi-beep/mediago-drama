# MediaLink Reference Drag Import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users drag visual files into MediaLink generation surfaces so accepted files are permanently stored in the relevant material library and selected as ordered generation references.

**Architecture:** Add one pure batch-import core for file classification, route-aware preflight, two-worker upload ordering, progress, and per-file results. Existing generation controllers remain responsible for their own selection state and SWR cache, while a small shared drop-target hook gives the composer and reference dialog identical Finder-file behavior without a global drop provider.

**Tech Stack:** React 19, TypeScript 7, SWR 2, Vitest 4, Testing Library, Go catalog tests.

## Global Constraints

- macOS Apple Silicon is the only desktop target.
- Reuse `uploadMediaAsset`; do not add a backend endpoint or second storage path.
- Every accepted successful upload is permanent and selected after the batch settles; server-detected incompatible assets or assets imported while the user changes route remain stored but are not selected.
- Use at most 2 concurrent upload requests and preserve original file order.
- Register drop targets only on the composer card and reference dialog, never on the application window.
- Dropping files must never submit a generation request.
- Preserve existing route schema and generation-confirmation behavior; only correct the Codex catalog limit to the provider's existing maximum of 10.
- Do not modify or stage `work/`.
- Do not include Codex result import, prompt-optimization timeout, or MiniMax H3 prompt rules in this plan.

## File Structure

- Create `apps/workspace/src/domains/generation/lib/reference-file-import.ts`: pure file classification, bounded upload, ordered result, cache merge, ordered asset lookup, and summary helpers.
- Create `apps/workspace/src/domains/generation/lib/reference-file-import.test.ts`: deterministic unit tests for all import outcomes and concurrency.
- Create `apps/workspace/src/domains/generation/hooks/useReferenceFileDropTarget.ts`: scoped external-file drag state and event handlers.
- Create `apps/workspace/src/domains/generation/hooks/useReferenceFileDropTarget.test.tsx`: drag enter/leave/drop and internal-DnD tests.
- Modify `apps/workspace/src/domains/generation/hooks/useGenerationReferences.ts`: main workspace import state, optimistic cache update, route snapshot, and ordered selection.
- Modify `apps/workspace/src/domains/generation/hooks/useGenerationReferences.test.tsx`: workspace-controller import tests.
- Modify `apps/workspace/src/domains/generation/hooks/useGenerationSettingsForm.ts`: unified settings form import state and `commitValue` integration.
- Modify `apps/workspace/src/domains/generation/hooks/useGenerationSettingsForm.test.tsx`: settings-form import tests.
- Modify `apps/workspace/src/domains/generation/components/ReferenceSelectionDialog.tsx`: multiple-file input, scoped dialog drop target, and progress UI.
- Modify `apps/workspace/src/domains/generation/components/MediaGenerationDialogs.test.tsx`: dialog input/drop regression tests.
- Modify `apps/workspace/src/domains/generation/components/GenerationComposerPanel.tsx`: scoped composer drop target and overlay.
- Modify `apps/workspace/src/domains/generation/components/GenerationComposerPanel.test.tsx`: composer drop and non-file drag tests.
- Modify `apps/workspace/src/domains/generation/components/MediaGenerationInputPanel.tsx`: pass import props to the composer.
- Modify `apps/workspace/src/domains/generation/components/GenerationWorkspace.tsx`: wire the legacy generation workspace.
- Modify `apps/workspace/src/domains/generation/components/MediaGenerationWorkspace.tsx`: wire the current generation workspace.
- Modify `apps/workspace/src/domains/generation/components/MediaGenerationWorkspaceDialogs.tsx`: wire the shared reference dialog.
- Modify `apps/workspace/src/domains/generation/components/GenerationWorkspace.imageSpec.test.tsx`: legacy workspace wiring regression.
- Modify `apps/workspace/src/domains/generation/components/MediaGenerationWorkspace.test.tsx`: current workspace wiring regression.
- Modify `packages/core/pkg/generation/catalog_medialink.go`: declare the existing Codex provider limit.
- Modify `packages/core/pkg/generation/catalog_medialink_test.go`: lock the route/provider limit contract.

---

### Task 1: Align the Codex reference limit contract

**Files:**
- Modify: `packages/core/pkg/generation/catalog_medialink_test.go`
- Modify: `packages/core/pkg/generation/catalog_medialink.go`

**Interfaces:**
- Consumes: existing `withReferenceURLLimit(max int) routeOption`.
- Produces: `RouteCodexImage.MaxReferenceURLs == 10` in the public catalog.

- [ ] **Step 1: Write the failing catalog assertion**

Add the limit to `TestMediaLinkCatalogRoutes`:

```go
if !imageRoute.SupportsReferenceURLs || imageRoute.MaxReferenceURLs != 10 {
	t.Fatalf("Codex reference contract = supports:%v max:%d", imageRoute.SupportsReferenceURLs, imageRoute.MaxReferenceURLs)
}
```

- [ ] **Step 2: Run the focused test and verify failure**

Run:

```bash
cd packages/core && go test ./pkg/generation -run TestMediaLinkCatalogRoutes -count=1
```

Expected: FAIL because the catalog currently reports `MaxReferenceURLs == 0`.

- [ ] **Step 3: Apply the existing provider limit to the route**

Pass the existing route option after the final `false` argument in the Codex route declaration:

```go
mediaLinkRoute(
	RouteCodexImage,
	FamilyCodexImage,
	VersionCodexImageV1,
	"Codex 生图",
	KindImage,
	ProviderCodex,
	"imagegen",
	AdapterCodexImage,
	"https://developers.openai.com/codex/app-server",
	codexImageParams(),
	false,
	withReferenceURLLimit(10),
)
```

- [ ] **Step 4: Run the catalog tests**

Run:

```bash
cd packages/core && go test ./pkg/generation -run 'TestMediaLinkCatalog' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the route contract fix**

```bash
git add packages/core/pkg/generation/catalog_medialink.go packages/core/pkg/generation/catalog_medialink_test.go
git commit -m "fix(generation): declare Codex reference limit"
```

### Task 2: Build the ordered batch-import core

**Files:**
- Create: `apps/workspace/src/domains/generation/lib/reference-file-import.ts`
- Create: `apps/workspace/src/domains/generation/lib/reference-file-import.test.ts`

**Interfaces:**
- Consumes: `uploadMediaAsset(file, projectId)` and server-returned `MediaAsset`.
- Produces:

```ts
export type ReferenceImportStatus =
	| "uploaded"
	| "uploaded_incompatible"
	| "rejected"
	| "failed";

export interface ReferenceImportProgress {
	processed: number;
	total: number;
}

export interface ReferenceImportItemResult {
	asset?: MediaAsset;
	file: File;
	index: number;
	reason?: string;
	status: ReferenceImportStatus;
}

export interface ReferenceImportBatchResult {
	results: ReferenceImportItemResult[];
	selectableAssets: MediaAsset[];
	storedAssets: MediaAsset[];
}

export const importReferenceFiles: (options: {
	availableSlots?: number;
	files: readonly File[];
	isUploadedAssetCompatible: (asset: MediaAsset) => boolean;
	onProgress?: (progress: ReferenceImportProgress) => void;
	projectId?: string | null;
	selectableKinds: ReadonlySet<MediaAssetKind>;
	upload?: (file: File, projectId?: string | null) => Promise<MediaAsset>;
}) => Promise<ReferenceImportBatchResult>;

export const mergeMediaAssetsByID: (
	current: readonly MediaAsset[],
	incoming: readonly MediaAsset[],
) => MediaAsset[];

export const mediaAssetsInIDOrder: (
	ids: readonly string[],
	assets: readonly MediaAsset[],
) => MediaAsset[];

export const referenceImportIssueMessage: (
	batch: ReferenceImportBatchResult,
) => string | null;
```

- [ ] **Step 1: Write failing classification and preflight tests**

Cover MIME-first classification, the exact extension fallbacks, zero-byte rejection, unsupported kind rejection, and remaining-slot rejection:

```ts
it("rejects unsupported and overflow files before upload", async () => {
	const upload = vi.fn().mockRejectedValue(new Error("upload failed"));
	const result = await importReferenceFiles({
		availableSlots: 1,
		files: [
			new File(["png"], "first.png", { type: "image/png" }),
			new File(["mp4"], "clip.mp4", { type: "video/mp4" }),
			new File(["png"], "second.png", { type: "image/png" }),
		],
		isUploadedAssetCompatible: (asset) => asset.kind === "image",
		selectableKinds: new Set(["image"]),
		upload,
	});

	expect(upload).toHaveBeenCalledTimes(1);
	expect(result.results.map((item) => item.status)).toEqual([
		"failed",
		"rejected",
		"rejected",
	]);
});
```

The rejecting upload mock makes the accepted first item's expected `failed` status deterministic.

- [ ] **Step 2: Write failing ordering, concurrency, and compatibility tests**

Use deferred upload promises to finish indexes 1 then 0. Assert `selectableAssets` remains `[asset0, asset1]`, progress reaches `{processed: 2, total: 2}`, and observed active requests never exceed 2. Add a server-kind mismatch case expecting `uploaded_incompatible` with the asset present only in `storedAssets`.

Add a summary test that expects `null` when every item is `uploaded`, otherwise expects each failed or rejected filename and reason in the returned Chinese message.

- [ ] **Step 3: Implement the minimal pure core**

Implement `referenceFileKind(file)` with these rules:

```ts
const referenceExtensions = {
	image: new Set(["png", "jpg", "jpeg", "webp", "gif", "bmp", "avif"]),
	video: new Set(["mp4", "webm", "mov", "m4v"]),
	audio: new Set(["mp3", "wav", "m4a", "aac", "ogg", "flac"]),
} as const;
```

Pre-fill rejected results, reserve at most `availableSlots` accepted indexes in original order, and run exactly `Math.min(2, acceptedIndexes.length)` workers over a shared cursor. Store results by original index; derive `selectableAssets` and `storedAssets` from the sorted results after all workers settle.

Implement cache helpers with ID-based replacement and ordered lookup:

```ts
export const mediaAssetsInIDOrder = (ids, assets) => {
	const byID = new Map(assets.map((asset) => [asset.id, asset]));
	return ids.flatMap((id) => {
		const asset = byID.get(id);
		return asset ? [asset] : [];
	});
};
```

Implement `referenceImportIssueMessage` by joining every non-`uploaded` item as `文件名：原因`; return `null` when no issue exists.

- [ ] **Step 4: Run the core tests**

Run:

```bash
pnpm --dir apps/workspace test -- src/domains/generation/lib/reference-file-import.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit the import core**

```bash
git add apps/workspace/src/domains/generation/lib/reference-file-import.ts apps/workspace/src/domains/generation/lib/reference-file-import.test.ts
git commit -m "feat(generation): add ordered reference import core"
```

### Task 3: Integrate batch import with the generation workspace hook

**Files:**
- Modify: `apps/workspace/src/domains/generation/hooks/useGenerationReferences.ts`
- Modify: `apps/workspace/src/domains/generation/hooks/useGenerationReferences.test.tsx`

**Interfaces:**
- Consumes: `importReferenceFiles`, `mergeMediaAssetsByID`, `mediaAssetsInIDOrder`, and existing `canUseAssetAsReference`.
- Produces:

```ts
importReferenceFiles: (files: readonly File[]) => Promise<ReferenceImportBatchResult | null>;
isUploadingAsset: boolean;
referenceImportProgress: ReferenceImportProgress | null;
selectedReferenceAssets: MediaAsset[]; // ordered by selectedReferenceAssetIds
```

- [ ] **Step 1: Replace single-event tests with failing file-array tests**

Remove the synthetic `React.ChangeEvent` helper. Add tests that call:

```ts
await result.current.importReferenceFiles([
	new File(["first"], "first.png", { type: "image/png" }),
	new File(["second"], "second.png", { type: "image/png" }),
]);
```

Assert two uploads, one optimistic `mutateMediaAssets` cache update, IDs in input order, and assets in selected-ID order even when `mediaAssets` is reversed.

- [ ] **Step 2: Add failing guard and route-change tests**

Assert a second call while the first is pending returns `null` without another upload. Rerender with a different route before the upload settles and assert the asset is cached but not selected, with a readable route-change error.

- [ ] **Step 3: Implement controller integration**

Add an in-flight ref, progress state, and current-route ref. Calculate slots from `maxReferenceUrlsForRoute(selectedRoute) - referenceCount`. Run the shared core with the route snapshot and:

```ts
isUploadedAssetCompatible: (asset) =>
	canUseAssetAsReference(asset, selectedRoute, selectableReferenceKinds)
```

Optimistically merge `batch.storedAssets` before selecting:

```ts
await mutateMediaAssets(
	(current) => ({
		assets: mergeMediaAssetsByID(current?.assets ?? mediaAssets, batch.storedAssets),
	}),
	{ revalidate: false },
);
```

If the route ID is unchanged, append `batch.selectableAssets.map((asset) => asset.id)` through the existing limit and dedupe rules. Pass `referenceImportIssueMessage(batch)` to `setError` when the batch has issues. Start a background revalidation with a handled rejection. Derive `selectedReferenceAssets` through `mediaAssetsInIDOrder`.

- [ ] **Step 4: Run the hook tests**

Run:

```bash
pnpm --dir apps/workspace test -- src/domains/generation/hooks/useGenerationReferences.test.tsx
```

Expected: PASS.

- [ ] **Step 5: Commit the workspace hook**

```bash
git add apps/workspace/src/domains/generation/hooks/useGenerationReferences.ts apps/workspace/src/domains/generation/hooks/useGenerationReferences.test.tsx
git commit -m "feat(generation): import workspace references in batches"
```

### Task 4: Integrate batch import with unified generation settings

**Files:**
- Modify: `apps/workspace/src/domains/generation/hooks/useGenerationSettingsForm.ts`
- Modify: `apps/workspace/src/domains/generation/hooks/useGenerationSettingsForm.test.tsx`

**Interfaces:**
- Consumes: the Task 2 import core and existing `commitValue` normalization.
- Produces controller fields:

```ts
importReferenceFiles: (files: readonly File[]) => Promise<ReferenceImportBatchResult | null>;
referenceImportProgress: ReferenceImportProgress | null;
```

- [ ] **Step 1: Write failing settings-form import tests**

Mock `uploadMediaAsset`, invoke `result.current.importReferenceFiles([fileA, fileB])`, and assert:

```ts
expect(result.current.value.referenceAssetIds).toEqual(["asset-a", "asset-b"]);
expect(workspaceMock.current.mutateMediaAssets).toHaveBeenCalled();
```

Add cases for route limit, unsupported route, partial failure, and reversed workspace assets preserving ID order.

- [ ] **Step 2: Run the focused test and verify failure**

Run:

```bash
pnpm --dir apps/workspace test -- src/domains/generation/hooks/useGenerationSettingsForm.test.tsx
```

Expected: FAIL because the controller only exposes event-based single upload.

- [ ] **Step 3: Implement settings-form import**

Replace `uploadReferenceAsset(event)` with `importReferenceFiles(files)`. Use `new Set<MediaAssetKind>(["image"])`, current route support, and `maxReferenceImages - value.referenceAssetIds.length` for preflight. Optimistically merge returned assets through `workspace.mutateMediaAssets`, then call `commitValue` once with ordered IDs:

```ts
commitValue(
	normalizeGenerationSettingsValue(
		workspace.catalog,
		kind,
		{
			...currentValue,
			referenceAssetIds: Array.from(
				new Set([
					...currentValue.referenceAssetIds,
					...batch.selectableAssets.map((asset) => asset.id),
				]),
			),
		},
		promptItemsForNormalization,
	),
);
```

Use an in-flight ref and route-ID ref exactly as in Task 3, and surface `referenceImportIssueMessage(batch)` through the form's existing error state. Keep settings form references image-only.

- [ ] **Step 4: Run settings-form and value-normalization tests**

Run:

```bash
pnpm --dir apps/workspace test -- src/domains/generation/hooks/useGenerationSettingsForm.test.tsx src/domains/generation/components/generationSettingsValue.test.ts
```

Expected: PASS.

- [ ] **Step 5: Commit the settings integration**

```bash
git add apps/workspace/src/domains/generation/hooks/useGenerationSettingsForm.ts apps/workspace/src/domains/generation/hooks/useGenerationSettingsForm.test.tsx
git commit -m "feat(generation): import settings references in batches"
```

### Task 5: Add one scoped external-file drop hook

**Files:**
- Create: `apps/workspace/src/domains/generation/hooks/useReferenceFileDropTarget.ts`
- Create: `apps/workspace/src/domains/generation/hooks/useReferenceFileDropTarget.test.tsx`

**Interfaces:**
- Consumes: `disabled`, `isImporting`, and `onImportFiles(files)`.
- Produces:

```ts
export const useReferenceFileDropTarget: (options: {
	disabled: boolean;
	isImporting: boolean;
	onImportFiles: (files: File[]) => void | Promise<void>;
}) => {
	isDraggingFiles: boolean;
	dropTargetProps: Pick<
		React.HTMLAttributes<HTMLElement>,
		"onDragEnter" | "onDragLeave" | "onDragOver" | "onDrop"
	>;
};
```

- [ ] **Step 1: Write failing external-file drag tests**

Render a test component spreading `dropTargetProps`. Fire `dragEnter`, `dragOver`, and `drop` with `dataTransfer.types = ["Files"]` and two files. Assert highlight state appears, default is prevented, file order is preserved, and state clears after drop.

- [ ] **Step 2: Write failing non-file and nested-leave tests**

Use `dataTransfer.types = ["text/plain"]` and assert the hook neither prevents default nor imports. Use two nested `dragEnter` calls followed by one `dragLeave` and assert the highlight remains until the matching final leave.

- [ ] **Step 3: Implement the hook**

Track drag depth in a ref. Treat an event as external files only when:

```ts
Array.from(event.dataTransfer.types).includes("Files")
```

Call `preventDefault()` only for enabled external-file events. On drop, reset depth/state, convert `dataTransfer.files` to `File[]`, ignore empty lists, and call `onImportFiles` without awaiting inside the event handler.

- [ ] **Step 4: Run the hook tests**

Run:

```bash
pnpm --dir apps/workspace test -- src/domains/generation/hooks/useReferenceFileDropTarget.test.tsx
```

Expected: PASS.

- [ ] **Step 5: Commit the scoped drop hook**

```bash
git add apps/workspace/src/domains/generation/hooks/useReferenceFileDropTarget.ts apps/workspace/src/domains/generation/hooks/useReferenceFileDropTarget.test.tsx
git commit -m "feat(generation): add scoped reference drop target"
```

### Task 6: Enable multiple-file import in the reference dialog

**Files:**
- Modify: `apps/workspace/src/domains/generation/components/ReferenceSelectionDialog.tsx`
- Modify: `apps/workspace/src/domains/generation/components/MediaGenerationDialogs.test.tsx`
- Modify: `apps/workspace/src/domains/generation/components/GenerationSettingsForm.tsx`
- Modify: `apps/workspace/src/domains/generation/components/MediaGenerationWorkspaceDialogs.tsx`
- Modify: `apps/workspace/src/domains/generation/components/GenerationWorkspace.tsx`

**Interfaces:**
- Consumes: controller `importReferenceFiles`, `referenceImportProgress`, and Task 5 drop hook.
- Produces dialog props:

```ts
onImportFiles: (files: File[]) => Promise<unknown> | void;
importProgress?: ReferenceImportProgress | null;
```

- [ ] **Step 1: Write failing dialog input and drop tests**

Change test props from `onUpload` to `onImportFiles`. Assert the hidden input has `multiple`, and a change event with `[fileA, fileB]` calls `onImportFiles([fileA, fileB])` and clears `input.value`. Fire a Files drop on the dialog content and assert the same callback.

- [ ] **Step 2: Add failing dialog progress and disabled tests**

Render `isUploading={true}` with `importProgress={{processed: 2, total: 5}}` and assert “正在导入 2/5”. With `disabled` or `isUploading`, assert a file drop does not call the callback.

- [ ] **Step 3: Implement dialog behavior**

Replace event-shaped `onUpload` with `onImportFiles`. Add `multiple` and a local input adapter:

```tsx
onChange={(event) => {
	const files = Array.from(event.currentTarget.files ?? []);
	event.currentTarget.value = "";
	if (files.length > 0) void controller.onImportFiles(files);
}}
```

Apply Task 5 `dropTargetProps` to the dialog content wrapper. Show the drag copy over the content and progress beside the existing upload spinner and selection count.

- [ ] **Step 4: Wire all dialog callers and run tests**

Pass each controller's `importReferenceFiles` and progress fields from `GenerationSettingsForm`, `MediaGenerationWorkspaceDialogs`, and legacy `GenerationWorkspace`.

Run:

```bash
pnpm --dir apps/workspace test -- src/domains/generation/components/MediaGenerationDialogs.test.tsx src/domains/generation/hooks/useGenerationSettingsForm.test.tsx
```

Expected: PASS.

- [ ] **Step 5: Commit dialog support**

```bash
git add apps/workspace/src/domains/generation/components/ReferenceSelectionDialog.tsx apps/workspace/src/domains/generation/components/MediaGenerationDialogs.test.tsx apps/workspace/src/domains/generation/components/GenerationSettingsForm.tsx apps/workspace/src/domains/generation/components/MediaGenerationWorkspaceDialogs.tsx apps/workspace/src/domains/generation/components/GenerationWorkspace.tsx
git commit -m "feat(generation): import dropped references in picker"
```

### Task 7: Enable composer-card drops in both generation workspaces

**Files:**
- Modify: `apps/workspace/src/domains/generation/components/GenerationComposerPanel.tsx`
- Modify: `apps/workspace/src/domains/generation/components/GenerationComposerPanel.test.tsx`
- Modify: `apps/workspace/src/domains/generation/components/MediaGenerationInputPanel.tsx`
- Modify: `apps/workspace/src/domains/generation/components/GenerationWorkspace.tsx`
- Modify: `apps/workspace/src/domains/generation/components/MediaGenerationWorkspace.tsx`
- Modify: `apps/workspace/src/domains/generation/components/GenerationWorkspace.imageSpec.test.tsx`
- Modify: `apps/workspace/src/domains/generation/components/MediaGenerationWorkspace.test.tsx`

**Interfaces:**
- Consumes: Task 5 drop hook and controller import/progress fields.
- Produces composer props:

```ts
onImportReferenceFiles?: (files: File[]) => Promise<unknown> | void;
isImportingReferences?: boolean;
referenceImportProgress?: ReferenceImportProgress | null;
```

- [ ] **Step 1: Write failing composer behavior tests**

Render the composer with `onImportReferenceFiles`. Drop two files on the inner card and assert the callback receives both without the submit handler firing. Assert the overlay text is “松开以加入素材库并设为参考素材” during hover and “正在导入 2/5” while importing.

- [ ] **Step 2: Write failing capability and internal-DnD tests**

Render without an import callback or with `canSelectReference={false}` and assert Files drops are not intercepted. Fire a `text/plain` drag and assert no overlay and no callback.

- [ ] **Step 3: Implement the composer drop surface**

Use `useReferenceFileDropTarget` on the existing inner rounded card. Make the card `relative`, render an absolute token-colored overlay only when dragging or importing, and keep all existing prompt controls mounted beneath it. Do not add raw colors; use `bg-card`, `border-primary`, `text-foreground`, and `text-muted-foreground`.

- [ ] **Step 4: Thread props through both workspace call chains**

Pass the fields directly from `GenerationWorkspace` to `GenerationComposerPanel`. Add the same props to `MediaGenerationInputPanel`, then pass them from `MediaGenerationWorkspace`. Update the current-workspace component mocks to capture and invoke `onImportReferenceFiles`, and assert the workspace import callback is received by both the input panel and dialogs.

Run:

```bash
pnpm --dir apps/workspace test -- src/domains/generation/components/GenerationComposerPanel.test.tsx src/domains/generation/components/GenerationWorkspace.imageSpec.test.tsx src/domains/generation/components/MediaGenerationWorkspace.test.tsx
```

Expected: PASS and no generation submission calls from drop events.

- [ ] **Step 5: Commit composer support**

```bash
git add apps/workspace/src/domains/generation/components/GenerationComposerPanel.tsx apps/workspace/src/domains/generation/components/GenerationComposerPanel.test.tsx apps/workspace/src/domains/generation/components/MediaGenerationInputPanel.tsx apps/workspace/src/domains/generation/components/GenerationWorkspace.tsx apps/workspace/src/domains/generation/components/MediaGenerationWorkspace.tsx apps/workspace/src/domains/generation/components/GenerationWorkspace.imageSpec.test.tsx apps/workspace/src/domains/generation/components/MediaGenerationWorkspace.test.tsx
git commit -m "feat(generation): import references from composer drops"
```

### Task 8: Run regression gates and document evidence

**Files:**
- Modify only if a verification failure exposes a defect in files already listed above.

**Interfaces:**
- Consumes: all prior tasks.
- Produces: verified TypeScript, React, Go, formatting, and build evidence without a real generation request.

- [ ] **Step 1: Run all reference-import tests together**

```bash
pnpm --dir apps/workspace test -- \
  src/domains/generation/lib/reference-file-import.test.ts \
  src/domains/generation/hooks/useReferenceFileDropTarget.test.tsx \
  src/domains/generation/hooks/useGenerationReferences.test.tsx \
  src/domains/generation/hooks/useGenerationSettingsForm.test.tsx \
  src/domains/generation/components/MediaGenerationDialogs.test.tsx \
  src/domains/generation/components/GenerationComposerPanel.test.tsx \
  src/domains/generation/components/GenerationWorkspace.imageSpec.test.tsx \
  src/domains/generation/components/MediaGenerationWorkspace.test.tsx
```

Expected: PASS.

- [ ] **Step 2: Run workspace type, lint, and format gates**

```bash
pnpm --dir apps/workspace build
pnpm --dir apps/workspace lint
pnpm --dir apps/workspace format
```

Expected: all commands exit 0.

- [ ] **Step 3: Run the Go catalog package and diff checks**

```bash
cd packages/core && go test ./pkg/generation -count=1
cd ../.. && git diff --check
```

Expected: PASS and no whitespace errors.

- [ ] **Step 4: Run the full local regression suites**

```bash
pnpm --dir apps/workspace test
cd packages/core && go test ./...
```

Expected: PASS. Do not start ComfyUI, AutoDL, Codex image generation, or video generation.

- [ ] **Step 5: Verify scope and commit any final test-only adjustment**

Run:

```bash
git status --short
git diff --stat HEAD~7..HEAD
```

Expected: only the files named in this plan plus untouched untracked `work/`. If a test-only correction was required, stage its exact files and commit with:

```bash
git commit -m "test(generation): cover reference drag imports"
```

If no correction was required, do not create an empty commit.
