# MediaLink Voice Library Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make “全部音色” the complete, shortest voice-selection entry by combining provider voices with tagged local voice samples, while keeping ordinary audio out and reserving “我的音色” for personal voices.

**Architecture:** Add one optional `voicePurpose` field to the existing media asset record and reuse the current media upload/update APIs. The audio picker will derive `voice-library`, `personal-voice`, and untagged collections client-side, so no separate voice database or generation-route changes are needed. The existing material import dialog remains the storage path and gains file-drop handling through the already tested `useReferenceFileDropTarget` hook.

**Tech Stack:** Go, Gin, GORM/SQLite, React 19, TypeScript, SWR, Vitest, Testing Library, pnpm.

## Global Constraints

- “全部音色”汇总供应商内置音色和本地角色样音。
- “我的音色”只展示明确标记为个人音色的素材。
- 音乐、环境音和音效不进入任何音色列表。
- 新音色永久保存到现有素材库，导入后立即刷新并选中。
- 不伪造供应商 `voiceId`，不修改生成路由协议，不建立独立音色数据库。
- 已有《崩坏：星穹铁道》样音只做定向标记，不移动、不复制、不重新编码。
- 只支持 macOS Apple Silicon，不增加 Windows 分支。

---

## File Map

- `services/server/internal/domain/media_models.go`: persist the optional `voice_purpose` asset column.
- `services/server/internal/repository/media_repo.go`: update one asset's voice purpose.
- `services/server/internal/service/media/store.go`: validate, save, expose, and update `voicePurpose` without changing ordinary asset behavior.
- `services/server/internal/http/handlers/media.go`: accept `voicePurpose` from multipart upload and JSON update requests.
- `services/server/internal/service/media/store_test.go`: service-level validation and persistence regression coverage.
- `services/server/internal/app/api_test.go`: HTTP contract coverage for upload and update.
- `apps/workspace/src/domains/workspace/api/media.ts`: expose the typed field and API helpers.
- `apps/workspace/src/domains/generation/components/MaterialLibraryImportDialog.tsx`: reuse the existing drop-target hook for file import.
- `apps/workspace/src/domains/generation/components/MediaGenerationDialogs.test.tsx`: drag/drop import regression coverage.
- `apps/workspace/src/shared/components/generation-dialogs/AudioGenerationDialog.tsx`: rename the fast entry to “添加音色”.
- `apps/workspace/src/shared/components/generation-dialogs/AudioReferenceSelectionPanel.tsx`: combine tagged local voices with provider voices and show explicit empty states.
- `apps/workspace/src/shared/components/generation-dialogs/AudioGenerationDialog.test.tsx`: voice-purpose filtering, no-provider fallback, import, and selection coverage.

### Task 1: Persist and expose media asset voice purpose

**Files:**

- Modify: `services/server/internal/domain/media_models.go`
- Modify: `services/server/internal/repository/media_repo.go`
- Modify: `services/server/internal/service/media/store.go`
- Test: `services/server/internal/service/media/store_test.go`

**Interfaces:**

- Produces: `VoicePurposeVoiceLibrary = "voice-library"` and `VoicePurposePersonalVoice = "personal-voice"`.
- Produces: `MediaAsset.VoicePurpose string` and `MediaAssetSaveOptions.VoicePurpose string`.
- Produces: `SaveMultipartFileWithVoicePurpose(header *multipart.FileHeader, projectID string, voicePurpose string) (MediaAsset, error)`.
- Produces: `UpdateVoicePurpose(id string, voicePurpose string) (MediaAsset, bool, error)`.

- [ ] **Step 1: Write failing service tests for valid, empty, and invalid purposes**

Add tests in `store_test.go` that upload an audio fixture with `voice-library`, reopen it through `Get`, and assert persistence:

```go
asset, err := store.SaveMultipartFileWithVoicePurpose(header, "alpha", VoicePurposeVoiceLibrary)
if err != nil {
    t.Fatalf("SaveMultipartFileWithVoicePurpose() error = %v", err)
}
if asset.VoicePurpose != VoicePurposeVoiceLibrary {
    t.Fatalf("VoicePurpose = %q, want %q", asset.VoicePurpose, VoicePurposeVoiceLibrary)
}
stored, ok, err := store.Get(asset.ID)
if err != nil || !ok || stored.VoicePurpose != VoicePurposeVoiceLibrary {
    t.Fatalf("Get() = %+v, %v, %v", stored, ok, err)
}
```

Also assert that ordinary `SaveMultipartFile` leaves the field empty, `personal-voice` is accepted only for audio, and `music` or a voice purpose on an image returns an error without creating an asset.

- [ ] **Step 2: Run the focused service test and verify it fails**

Run:

```bash
cd services/server
go test ./internal/service/media -run 'Test.*VoicePurpose' -count=1
```

Expected: FAIL because the constants, field, and methods do not exist.

- [ ] **Step 3: Add the minimal persisted field and validation**

Add to `domain.AssetModel`:

```go
VoicePurpose string `gorm:"column:voice_purpose;not null;default:'';index:assets_voice_purpose_idx"`
```

Add to `service/media/store.go`:

```go
const (
    VoicePurposeVoiceLibrary  = "voice-library"
    VoicePurposePersonalVoice = "personal-voice"
)

func normalizeVoicePurpose(kind string, value string) (string, error) {
    value = strings.ToLower(strings.TrimSpace(value))
    if value == "" {
        return "", nil
    }
    if strings.ToLower(strings.TrimSpace(kind)) != MediaKindAudio {
        return "", fmt.Errorf("voice purpose is only valid for audio assets")
    }
    switch value {
    case VoicePurposeVoiceLibrary, VoicePurposePersonalVoice:
        return value, nil
    default:
        return "", fmt.Errorf("unsupported voice purpose %q", value)
    }
}
```

Thread `VoicePurpose` through `MediaAsset`, `MediaAssetSaveOptions`, the new asset/model construction, and `mediaAssetRecordFromModel`. Keep the existing `SaveMultipartFile` signature as a compatibility wrapper:

```go
func (store *MediaAssets) SaveMultipartFile(header *multipart.FileHeader, projectID string) (MediaAsset, error) {
    return store.SaveMultipartFileWithVoicePurpose(header, projectID, "")
}
```

The new method validates the purpose before writing bytes and passes it through `MediaAssetSaveOptions`.

- [ ] **Step 4: Add repository and service update methods**

Add an exact repository update:

```go
func (repo *MediaAssetRepository) UpdateMediaAssetVoicePurpose(id string, purpose string, updatedAt string) error {
    return repo.db.Model(&domain.AssetModel{}).
        Where("id = ?", strings.TrimSpace(id)).
        Updates(map[string]any{
            "voice_purpose": purpose,
            "updated_at": domain.TimeFromString(updatedAt),
        }).Error
}
```

Add `UpdateVoicePurpose` in the service. It must load the asset, validate the value against the asset kind, update only `voice_purpose` and `updated_at`, and return the refreshed value. An empty string is allowed so a mistaken classification can be removed without deleting the asset.

- [ ] **Step 5: Run the focused and repository migration tests**

Run:

```bash
cd services/server
go test ./internal/service/media ./internal/repository -count=1
```

Expected: PASS, including GORM auto-migration of `voice_purpose`.

- [ ] **Step 6: Commit Task 1**

```bash
git add services/server/internal/domain/media_models.go services/server/internal/repository/media_repo.go services/server/internal/service/media/store.go services/server/internal/service/media/store_test.go
git commit -m "feat(media): persist audio voice purpose"
```

### Task 2: Extend the media HTTP and TypeScript contracts

**Files:**

- Modify: `services/server/internal/http/handlers/media.go`
- Test: `services/server/internal/app/api_test.go`
- Modify: `apps/workspace/src/domains/workspace/api/media.ts`

**Interfaces:**

- Consumes: Task 1 `SaveMultipartFileWithVoicePurpose` and `UpdateVoicePurpose`.
- Produces: multipart field `voicePurpose` on `POST /media-assets` and project-scoped equivalent.
- Produces: optional JSON field `voicePurpose` on existing asset `PUT` routes.
- Produces: `uploadMediaAsset(file, projectId, { voicePurpose })` and `updateMediaAssetVoicePurpose(id, voicePurpose, projectId)`.

- [ ] **Step 1: Write failing API tests**

Extend the media asset subtest in `api_test.go` with an audio multipart upload containing:

```go
if err := writer.WriteField("voicePurpose", servicemedia.VoicePurposeVoiceLibrary); err != nil {
    t.Fatalf("writing voice purpose: %v", err)
}
```

Assert the response and subsequent list contain `"voicePurpose":"voice-library"`. Then send:

```json
{ "voicePurpose": "personal-voice" }
```

to the existing `PUT /api/v1/media-assets/{id}` route and assert the response changes only that field. Add negative assertions for `"voicePurpose":"music"` and for attaching a voice purpose to an image.

- [ ] **Step 2: Run the focused API test and verify it fails**

Run:

```bash
cd services/server
go test ./internal/app -run 'TestAPIHandler/media_assets_voice_purpose' -count=1
```

Expected: FAIL because the handler ignores the new multipart/JSON fields.

- [ ] **Step 3: Implement the HTTP contract without adding routes**

Read `context.PostForm("voicePurpose")` in the upload handler and call `SaveMultipartFileWithVoicePurpose`. Change the update request to pointer fields so the existing filename update remains compatible:

```go
type MediaAssetUpdateRequest struct {
    Filename     *string `json:"filename,omitempty"`
    VoicePurpose *string `json:"voicePurpose,omitempty"`
}
```

Reject a request where both fields are absent. Apply filename first when present, then voice purpose, and return the final asset. Do not accept any other metadata.

- [ ] **Step 4: Add the typed client helpers**

In `media.ts` add:

```ts
export type MediaAssetVoicePurpose = "voice-library" | "personal-voice";

export interface MediaAsset {
  // existing fields
  voicePurpose?: MediaAssetVoicePurpose;
}

export interface MediaAssetUploadOptions {
  voicePurpose?: MediaAssetVoicePurpose;
}
```

Append `voicePurpose` to `FormData` only when supplied. Preserve all two-argument upload callers. Add:

```ts
export const updateMediaAssetVoicePurpose = async (
  id: string,
  voicePurpose: MediaAssetVoicePurpose | "",
  projectId?: string | null,
) => {
  const response = await httpClient.put<MediaAsset>(
    `${mediaAssetsPath(projectId)}/${encodeURIComponent(id)}`,
    { voicePurpose },
  );
  return response.data;
};
```

- [ ] **Step 5: Run server tests and the workspace type build**

Run:

```bash
cd services/server
go test ./internal/app ./internal/http/handlers -count=1
cd ../../apps/workspace
pnpm build
```

Expected: PASS.

- [ ] **Step 6: Commit Task 2**

```bash
git add services/server/internal/http/handlers/media.go services/server/internal/app/api_test.go apps/workspace/src/domains/workspace/api/media.ts
git commit -m "feat(media): expose audio voice purpose API"
```

### Task 3: Add drag/drop to the reused material import dialog

**Files:**

- Modify: `apps/workspace/src/domains/generation/components/MaterialLibraryImportDialog.tsx`
- Test: `apps/workspace/src/domains/generation/components/MediaGenerationDialogs.test.tsx`

**Interfaces:**

- Consumes: existing `useReferenceFileDropTarget({ disabled, isImporting, onImportFiles })`.
- Preserves: `onUploadAsset(file) => Promise<MediaAsset>` and the reducer's automatic selection behavior.

- [ ] **Step 1: Write a failing audio-drop test**

Render `MaterialLibraryImportDialog` with `assetKind="audio"`, drop one valid `.mp3` and one invalid `.txt` on the element named “拖拽音频到这里”, then assert only the MP3 is uploaded and selected:

```ts
fireEvent.drop(screen.getByLabelText("拖拽音频到这里"), {
  dataTransfer: { files: [audioFile, textFile], types: ["Files"] },
});
await waitFor(() => expect(onUploadAsset).toHaveBeenCalledTimes(1));
expect(onUploadAsset).toHaveBeenCalledWith(audioFile);
expect(screen.getByRole("checkbox", { name: /角色样音\.mp3/u })).toHaveAttribute(
  "aria-checked",
  "true",
);
```

Also verify a drop while `confirming` does not upload.

- [ ] **Step 2: Run the focused test and verify it fails**

Run:

```bash
cd apps/workspace
pnpm test -- MediaGenerationDialogs.test.tsx -t 'drops audio files'
```

Expected: FAIL because the dialog has no drop target.

- [ ] **Step 3: Refactor file-input upload into one shared function**

Extract `uploadFiles(files: File[])` from `uploadSelectedFiles`. It must filter with `isMaterialUploadFile`, dispatch the same reducer actions, call `Promise.all`, and keep the existing single-selection rule that selects the last uploaded asset. The file input and drop target both call this function.

- [ ] **Step 4: Attach the existing drop hook and visual state**

Use:

```ts
const { dropTargetProps, isDraggingFiles } = useReferenceFileDropTarget({
  disabled: confirming || !onUploadAsset,
  isImporting: isUploading,
  onImportFiles: uploadFiles,
});
```

Wrap the asset grid/empty state in a focus-neutral container with `aria-label={\`拖拽${kindCopy.label}到这里\`}` and `...dropTargetProps`. When `isDraggingFiles`, show a bordered overlay reading `松开即可添加${kindCopy.label}`. Do not add global drag listeners.

- [ ] **Step 5: Run the component test file**

Run:

```bash
cd apps/workspace
pnpm test -- MediaGenerationDialogs.test.tsx
```

Expected: PASS.

- [ ] **Step 6: Commit Task 3**

```bash
git add apps/workspace/src/domains/generation/components/MaterialLibraryImportDialog.tsx apps/workspace/src/domains/generation/components/MediaGenerationDialogs.test.tsx
git commit -m "feat(media): support dropping assets into import dialog"
```

### Task 4: Make “全部音色” the aggregate voice library

**Files:**

- Modify: `apps/workspace/src/shared/components/generation-dialogs/AudioGenerationDialog.tsx`
- Modify: `apps/workspace/src/shared/components/generation-dialogs/AudioReferenceSelectionPanel.tsx`
- Test: `apps/workspace/src/shared/components/generation-dialogs/AudioGenerationDialog.test.tsx`

**Interfaces:**

- Consumes: Task 2 `MediaAsset.voicePurpose`, upload options, and `updateMediaAssetVoicePurpose`.
- Produces: `voiceLibraryAssets` and `personalVoiceAssets` derived collections.
- Preserves: provider voice selection, filtering, preview, and favorites.

- [ ] **Step 1: Replace the fixture with purpose-specific assets and write failing visibility tests**

Use three audio assets in the test fixture:

```ts
const roleSample = mediaAudio({
  id: "role-sample",
  filename: "三月七.mp3",
  voicePurpose: "voice-library",
});
const personalVoice = mediaAudio({
  id: "personal",
  filename: "我的旁白.mp3",
  voicePurpose: "personal-voice",
});
const music = mediaAudio({ id: "music", filename: "背景音乐.mp3" });
```

Assert on the default “全部音色” page:

- provider voice, `三月七`, and `我的旁白` are visible;
- `背景音乐` is absent.

After clicking “我的音色”, assert only `我的旁白` remains. Then return a catalog with no audio routes and assert `三月七` is still visible.

- [ ] **Step 2: Write failing import and empty-state tests**

Assert the header button is named “添加音色”. Uploading calls:

```ts
expect(mocks.uploadMediaAsset).toHaveBeenCalledWith(file, "project-a", {
  voicePurpose: "voice-library",
});
```

Selecting an existing untagged audio asset calls:

```ts
expect(mocks.updateMediaAssetVoicePurpose).toHaveBeenCalledWith(
  "audio-1",
  "voice-library",
  "project-a",
);
```

and the returned tagged asset becomes the outer draft selection. When provider and tagged local lists are both empty, assert a visible “还没有可用音色” message and “添加音色” action instead of a blank area.

- [ ] **Step 3: Run the focused test file and verify it fails**

Run:

```bash
cd apps/workspace
pnpm test -- AudioGenerationDialog.test.tsx
```

Expected: FAIL on visibility, upload arguments, update call, and empty-state assertions.

- [ ] **Step 4: Derive the two local voice collections**

Replace the current `userAudioAssets` derivation with:

```ts
const audioAssets = useMemo(
  () => (mediaAssetsData?.assets ?? []).filter((asset) => asset.kind === "audio"),
  [mediaAssetsData?.assets],
);
const voiceLibraryAssets = useMemo(
  () =>
    audioAssets.filter(
      (asset) => asset.voicePurpose === "voice-library" || asset.voicePurpose === "personal-voice",
    ),
  [audioAssets],
);
const personalVoiceAssets = useMemo(
  () => audioAssets.filter((asset) => asset.voicePurpose === "personal-voice"),
  [audioAssets],
);
```

Use `voiceLibraryAssets` under “全部音色” and `personalVoiceAssets` under “我的音色”. Keep untagged audio available inside the “添加音色” material dialog so it can be deliberately promoted, but never show it directly in either voice grid.

- [ ] **Step 5: Combine provider and local grids without rewriting their cards**

Under “全部音色”, render the existing `SystemVoiceGrid` followed by `UserAudioGrid` for tagged local voices. Local voices remain visible when the current provider filters have no results; provider gender/age/language/trait filters must not silently exclude local files that have no such metadata. Under the provider “收藏” subfilter, keep the current system-only behavior.

Add a compact explicit empty state only when the currently selected outer tab has no visible entries and loading is finished. Preserve provider preview/favorite functions unchanged.

- [ ] **Step 6: Make the fast import path tag, refresh, and select**

Rename the audio dialog title action from “从素材库中选择” to “添加音色”. Upload with `{ voicePurpose: "voice-library" }`. When the user confirms an existing untagged asset in the material dialog, await `updateMediaAssetVoicePurpose(asset.id, "voice-library", projectId)`, refresh SWR, and pass the returned tagged asset into `selectMaterialAsset`.

The import remains single-select in this generation picker. The shared material dialog may accept multiple files, but its existing single-selection reducer selects the last successful upload for immediate use; all successfully uploaded files remain permanently tagged in the library.

- [ ] **Step 7: Run UI tests, formatting, and build**

Run:

```bash
cd apps/workspace
pnpm test -- AudioGenerationDialog.test.tsx MediaGenerationDialogs.test.tsx
pnpm check
pnpm build
```

Expected: PASS.

- [ ] **Step 8: Commit Task 4**

```bash
git add apps/workspace/src/shared/components/generation-dialogs/AudioGenerationDialog.tsx apps/workspace/src/shared/components/generation-dialogs/AudioReferenceSelectionPanel.tsx apps/workspace/src/shared/components/generation-dialogs/AudioGenerationDialog.test.tsx
git commit -m "feat(audio): aggregate tagged samples in all voices"
```

### Task 5: Build, install, migrate the known samples, and verify the real app

**Files:**

- No source files beyond Tasks 1–4.
- Read-only source folder: `/Users/jialiankun/Desktop/AIGC/参考素材/样音`
- Installed app: `/Applications/MediaLink.app`

**Interfaces:**

- Consumes: Task 2 update API and Task 4 installed UI.
- Produces: the existing matching《崩坏：星穹铁道》audio assets tagged `voice-library` in the user's live workspace.

- [ ] **Step 1: Run the complete relevant test suites**

Run:

```bash
cd services/server
go test ./internal/service/media ./internal/repository ./internal/http/handlers ./internal/app -count=1
cd ../../apps/workspace
pnpm test -- AudioGenerationDialog.test.tsx MediaGenerationDialogs.test.tsx
pnpm check
pnpm build
```

Expected: all commands PASS.

- [ ] **Step 2: Build the macOS Apple Silicon application**

Run from the repository root:

```bash
pnpm --dir apps/workspace electron:build:darwin-arm64
```

Expected: a signed-or-local ad-hoc Apple Silicon `.app`/DMG artifact is produced without a Windows target.

- [ ] **Step 3: Replace the installed app only after a successful build**

Quit MediaLink normally, preserve the previous app as a recoverable backup, install the new `/Applications/MediaLink.app`, and reopen it. Do not delete the backup until live verification succeeds.

- [ ] **Step 4: Promote only the already imported known sample files**

Read basenames from `/Users/jialiankun/Desktop/AIGC/参考素材/样音`, list live audio assets through MediaLink's local API, intersect by exact filename, and send this body to the existing asset update endpoint for each matching asset ID:

```json
{ "voicePurpose": "voice-library" }
```

Before writing, print the matched filenames and count. Stop if any filename maps to more than one asset or if the match set does not equal the intended imported《崩坏：星穹铁道》sample set. Do not alter unmatched audio assets and do not copy or delete files.

- [ ] **Step 5: Verify the installed UI and persistence**

Open an audio selection flow and confirm:

- “全部音色” opens by default and contains all promoted character samples;
- “我的音色” does not contain those role samples;
- existing untagged music is absent from both;
- “添加音色” accepts file selection and drag/drop;
- a newly added sample is immediately selected;
- closing and reopening MediaLink preserves the sample in “全部音色”.

## Self-Review

- Spec coverage: all requirements map to Tasks 1–5; provider voices, tagged local samples, personal-only tab, ordinary audio exclusion, fast import, drag/drop, persistence, migration, and installed-app verification are covered.
- Placeholder scan: every code-edit and verification step names its concrete files, interfaces, commands, and expected result.
- Type consistency: `voicePurpose`, `voice-library`, and `personal-voice` use the same spelling in Go, HTTP, TypeScript, tests, and migration steps.
- Scope check: no independent subsystem was introduced; storage, API, and UI changes are one vertical feature and remain independently testable by task.
