# MediaLink Configurable ComfyUI Workflows Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a generic, versioned “添加工作流” registry that lets MediaLink run user-confirmed ComfyUI image workflows through the existing AutoDL instance pool without hardcoding Z-Image, Qwen, or FLUX into provider logic.

**Architecture:** Preserve the existing AutoDL settings, Keychain, tunnel, loopback ComfyUI client, scheduler, generation task, and asset-import foundations. Add a bounded UI-workflow compiler, immutable workflow versions, per-instance read-only validation, administration APIs/UI, and one task-scoped AutoDL image provider that resolves a workflow snapshot before submitting exactly once.

**Tech Stack:** Go 1.25, GORM/SQLite app settings, macOS Keychain, OpenSSH tunnel manager, ComfyUI HTTP API, React 19, TypeScript 7, SWR, Vitest, Go `testing` and race detector.

## Global Constraints

- Target only macOS Apple Silicon (`darwin/arm64`); do not add Windows, Intel Mac, or Linux packaging.
- Preserve MediaGo Drama character, scene, prop, storyboard, asset, task-history, preview, Skills, prompt-template, and prompt-optimization workflows.
- Keep exactly three public visual routes: `Codex 生图`, `AutoDL · 云端生图`, and `AutoDL · MiniMax H3`.
- Use Codex built-in imagegen; never call OpenAI Images API or add an OpenAI image API-key field.
- Treat workflow/model names as profile data and UI labels; never branch provider or scheduler behavior on Z-Image, Qwen, FLUX, LoRA, or filename names.
- Reuse the completed Keychain, SSH tunnel, loopback ComfyUI client, and one-slot-per-instance scheduler; do not create competing connection, queue, download, or asset systems.
- Passwords remain write-only in macOS Keychain and never appear in SQLite, JSON responses, process arguments, runtime state, or logs.
- Imported content is JSON data only. Never execute uploaded JavaScript, `.mjs`, shell, Python, plugin installers, URLs, or commands.
- A workflow cannot supply or override a ComfyUI base URL; all ComfyUI traffic uses the managed loopback tunnel.
- Limit one workflow JSON to 8 MiB, 64 nesting levels, 2,048 nodes, 8,192 links, and 64 KiB per string field.
- Limit an image profile to eight ordered reference slots, each uploaded image to 32 MiB, and all references for one task to 128 MiB.
- Never silently accept suggested bindings; only a fully user-confirmed binding set can become an enabled workflow version.
- Never silently select between ambiguous defaults or incompatible profiles; fail closed before HTTP submission.
- Never resubmit when `/prompt` acceptance is unknown or a persisted `prompt_id` exists.
- Cancel only an exact known queued prompt; never call global `/interrupt`.
- Run fake/no-consumption tests by default. This plan grants no new real `/prompt`, cloud generation, model install, FLUX repair, or `ComfyUIPhotoSync` operation.
- Avoid unrelated refactors and broad renames.

## Relationship to Existing Work

The following commits are accepted foundations and must not be reimplemented:

- `a6bd0c8` through `828e894`: SSH parsing, Keychain-backed instance settings, and validation invalidation.
- `27d1bb3` through `19944a4`: multi-instance tunnel lifecycle.
- `1eff73f` and `f7a5fa2`: bounded loopback ComfyUI client.
- `c00a6ac` and `072b644`: shared AutoDL scheduler and reservation identity.

This plan supersedes Task 6 and Tasks 8–15 of `docs/superpowers/plans/2026-08-30-medialink-autodl-zimage.md`. It retains that plan's completed route/request identity and security foundations while removing its hardcoded nine-workflow matrix, one-reference ceiling, destructive workflow deletion, and model-specific prompt branches.

This plan does not supersede the H3-specific graph, provider, recovery, or MiniMax-H3 prompt-optimization work in `docs/superpowers/plans/2026-08-30-medialink-autodl-h3-video.md`; that plan must consume the same tunnel manager and scheduler after this image path is integrated.

The untracked `work/` directory is user-owned evidence. Do not stage, modify, copy, or delete it. Synthetic compiler fixtures belong under committed `testdata/`; real workflows are imported by the user through the finished UI.

---

## File Map

- `services/server/internal/service/settings/autodl_instances.go` remains responsible only for instance profiles, Keychain presence, and connection-change invalidation.
- `services/server/internal/platform/comfyui/workflow_types.go` defines the one shared semantic binding shape used by persistence, compilation, validation, and generation.
- `services/server/internal/service/settings/autodl_workflow_types.go` defines generic profile, immutable version, binding, default, and validation records.
- `services/server/internal/service/settings/autodl_workflows.go` owns versioned workflow persistence and v1-to-v2 migration.
- `services/server/internal/platform/comfyui/workflow_compile.go` parses bounded UI-format JSON, suggests bindings, compiles API prompt templates, and instantiates confirmed bindings.
- `services/server/internal/service/generation/autodl_workflow_admin.go` combines settings, tunnels, `/object_info`, compilation, and per-instance validation for administration only.
- `services/server/internal/service/generation/autodl_workflow_resolver.go` resolves explicit/default profiles and immutable versions for generation.
- `services/server/internal/service/generation/autodl_image_provider.go` owns upload, submit-once, history polling, exact cancellation, output download, and resume.
- `services/server/internal/service/generation/autodl_image_output.go` performs bounded image download and decode validation.
- `services/server/internal/http/handlers/autodl_settings.go` exposes thin administration endpoints.
- `apps/workspace/src/domains/settings/api/autodl.ts` mirrors the redacted Go administration contract.
- `apps/workspace/src/domains/settings/components/AutoDLSettingsPanel.tsx` owns the MediaLink instance/workflow settings surface.
- `apps/workspace/src/domains/settings/components/AutoDLWorkflowDialog.tsx` owns import, binding confirmation, replace-version, and duplicate flows.
- Existing generation settings files add only AutoDL workflow/instance selectors and declared scalar controls.

---

### Task 1: Replace hardcoded workflow kinds with a versioned generic settings document

**Files:**
- Create: `services/server/internal/platform/comfyui/workflow_types.go`
- Create: `services/server/internal/service/settings/autodl_workflow_types.go`
- Create: `services/server/internal/service/settings/autodl_workflows.go`
- Create: `services/server/internal/service/settings/autodl_workflows_test.go`
- Modify: `services/server/internal/service/settings/autodl_instances.go`
- Modify: `services/server/internal/service/settings/autodl_instances_test.go`

**Interfaces:**
- Produces: shared `comfyui.WorkflowBindings` plus settings-owned `AutoDLWorkflowProfile`, `AutoDLWorkflowVersion`, `AutoDLReferenceContract`, `AutoDLWorkflowDefault`, and four-state `AutoDLWorkflowValidation`.
- Produces: settings document version 2 with v1 migration that preserves instances and old workflow snapshots but marks migrated versions disabled, archived, and stale.
- Consumes: existing `AppSettingStore`, instance IDs, workflow digests, and instance validation invalidation.

- [ ] **Step 1: Write failing migration and immutability tests**

```go
func TestAutoDLSettingsMigratesV1WorkflowWithoutMakingItReady(t *testing.T) {
	service, store := newAutoDLSettingsWithRawDocument(t, legacyAutoDLV1Document())
	got, err := service.GetAutoDLSettings(context.Background())
	if err != nil { t.Fatal(err) }
	profile := got.WorkflowProfiles[0]
	if profile.Enabled || !profile.Archived || len(profile.Versions) != 1 {
		t.Fatalf("migrated profile = %#v", profile)
	}
	if profile.Versions[0].BindingStatus != AutoDLBindingStatusUnconfirmed {
		t.Fatalf("binding status = %q", profile.Versions[0].BindingStatus)
	}
	if !strings.Contains(store.value(autoDLSettingsKey), `"version":2`) {
		t.Fatal("migration was not persisted")
	}
}

func TestReplaceAutoDLWorkflowAppendsImmutableVersion(t *testing.T) {
	service := newAutoDLSettingsForTest(t)
	first := saveConfirmedWorkflowVersion(t, service, "portrait", `{"nodes":[]}`, "v1")
	second := replaceConfirmedWorkflowVersion(t, service, "portrait", `{"nodes":[{"id":1}]}`, first.VersionID)
	got := mustAutoDLWorkflow(t, service, "portrait")
	if len(got.Versions) != 2 || got.Versions[0].VersionID != first.VersionID || got.CurrentVersionID != second.VersionID {
		t.Fatalf("profile versions = %#v", got)
	}
}
```

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/service/settings -run 'TestAutoDLSettingsMigratesV1|TestReplaceAutoDLWorkflowAppends' -count=1
```

Expected: FAIL because settings version 1 replaces profiles in place and accepts only enumerated workflow kinds.

- [ ] **Step 3: Add the generic v2 types and migration**

```go
const autoDLSettingsVersion = 2

type AutoDLWorkflowProfile struct {
	ID               string                  `json:"id"`
	Name             string                  `json:"name"`
	Description      string                  `json:"description,omitempty"`
	MediaKind        string                  `json:"mediaKind"`
	RouteID          string                  `json:"routeId"`
	Enabled          bool                    `json:"enabled"`
	AutoSelectable   bool                    `json:"autoSelectable"`
	Archived         bool                    `json:"archived"`
	CurrentVersionID string                  `json:"currentVersionId"`
	Versions         []AutoDLWorkflowVersion `json:"versions"`
}

type AutoDLWorkflowVersion struct {
	VersionID        string                  `json:"versionId"`
	Sequence         int                     `json:"sequence"`
	SourceVersionID  string                  `json:"sourceVersionId,omitempty"`
	CreatedAt        string                  `json:"createdAt"`
	UIWorkflow       json.RawMessage         `json:"uiWorkflow"`
	APITemplate      json.RawMessage         `json:"apiTemplate"`
	WorkflowDigest   string                  `json:"workflowDigest"`
	APITemplateDigest string                 `json:"apiTemplateDigest"`
	BindingStatus    string                  `json:"bindingStatus"`
	Bindings         comfyui.WorkflowBindings `json:"bindings"`
	References       AutoDLReferenceContract `json:"references"`
	PromptGuide      string                  `json:"promptGuide,omitempty"`
}

const (
	AutoDLBindingStatusUnconfirmed = "unconfirmed"
	AutoDLBindingStatusConfirmed   = "confirmed"
	AutoDLWorkflowValidationReady  = "ready"
	AutoDLWorkflowValidationInvalid = "invalid"
	AutoDLWorkflowValidationUnknown = "unknown"
	AutoDLWorkflowValidationStale   = "stale"
)

type AutoDLReferenceContract struct {
	Min   int                       `json:"min"`
	Max   int                       `json:"max"`
	Slots []comfyui.ReferenceBinding `json:"slots,omitempty"`
}

type AutoDLWorkflowValidation struct {
	WorkflowProfileID string `json:"workflowProfileId"`
	VersionID string `json:"versionId"`
	Status string `json:"status"`
	WorkflowDigest string `json:"workflowDigest"`
	APITemplateDigest string `json:"apiTemplateDigest"`
	ObjectInfoDigest string `json:"objectInfoDigest,omitempty"`
	InstanceFingerprint string `json:"instanceFingerprint,omitempty"`
	ValidatedAt string `json:"validatedAt,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type AutoDLWorkflowVersionResponse struct {
	VersionID string `json:"versionId"`
	Sequence int `json:"sequence"`
	SourceVersionID string `json:"sourceVersionId,omitempty"`
	CreatedAt string `json:"createdAt"`
	WorkflowDigest string `json:"workflowDigest"`
	APITemplateDigest string `json:"apiTemplateDigest"`
	BindingStatus string `json:"bindingStatus"`
	Bindings comfyui.WorkflowBindings `json:"bindings"`
	References AutoDLReferenceContract `json:"references"`
	PromptGuide string `json:"promptGuide,omitempty"`
}

type AutoDLWorkflowProfileResponse struct {
	ID string `json:"id"`
	Name string `json:"name"`
	Description string `json:"description,omitempty"`
	MediaKind string `json:"mediaKind"`
	RouteID string `json:"routeId"`
	Enabled bool `json:"enabled"`
	AutoSelectable bool `json:"autoSelectable"`
	Archived bool `json:"archived"`
	CurrentVersionID string `json:"currentVersionId"`
	Versions []AutoDLWorkflowVersionResponse `json:"versions"`
}
```

Define `comfyui.WorkflowBindings`, `WorkflowTarget`, `ReferenceBinding`, `OutputBinding`, and `ParameterBinding` in `workflow_types.go`; Task 2 adds behavior around these shared data-only types. In `autodl_workflow_types.go`, add `type AutoDLWorkflowBindings = comfyui.WorkflowBindings` only as a compatibility alias for existing settings callers; do not create a second serialized binding shape.

Response versions deliberately omit `UIWorkflow` and `APITemplate`; those snapshots remain backend-only and are retrieved only through `GetAutoDLWorkflowVersion` for validation/execution. The UI receives bindings, reference contracts, prompt guidance, immutable IDs, and digest summaries.

The test file defines these local helpers: `newAutoDLSettingsWithRawDocument(t, raw) (*Settings, *fakeAppSettingStore)` seeds the exact raw app-setting value; `legacyAutoDLV1Document()` returns the minimal valid v1 JSON fixture; `saveConfirmedWorkflowVersion` and `replaceConfirmedWorkflowVersion` call the public registry mutations with fixed IDs and compiler-shaped bindings; `mustAutoDLWorkflow` fetches one profile or fails the test.

Move workflow fields out of `autodl_instances.go`. Delete `allowedAutoDLWorkflowKinds`, `blockedAutoDLReadyKinds`, rejected model digest tables, and `DeleteAutoDLWorkflowProfile`. Migrate each v1 workflow into one version whose original JSON/digests remain intact but whose `BindingStatus` is `unconfirmed`; set `Enabled=false`, `AutoSelectable=false`, and `Archived=true`. Convert all v1 instance validations to `stale` with reason `migrated_v1_without_confirmed_bindings`.

- [ ] **Step 4: Run settings normal and race tests**

Run:

```bash
cd services/server && go test ./internal/service/settings -count=1
cd services/server && go test -race ./internal/service/settings -run 'TestAutoDL' -count=5
```

Expected: PASS; serialized settings contain no password, executable command, model-specific kind enum, or destructive workflow deletion path.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/platform/comfyui/workflow_types.go services/server/internal/service/settings/autodl_instances.go services/server/internal/service/settings/autodl_instances_test.go services/server/internal/service/settings/autodl_workflow_types.go services/server/internal/service/settings/autodl_workflows.go services/server/internal/service/settings/autodl_workflows_test.go
git commit -m "feat(settings): version AutoDL workflow profiles"
```

---

### Task 2: Compile bounded ComfyUI UI workflows without executing code

**Files:**
- Create: `services/server/internal/platform/comfyui/workflow_compile.go`
- Create: `services/server/internal/platform/comfyui/workflow_compile_test.go`
- Modify: `services/server/internal/platform/comfyui/client.go`
- Modify: `services/server/internal/platform/comfyui/client_http_test.go`
- Create: `services/server/internal/platform/comfyui/testdata/ui_t2i.json`
- Create: `services/server/internal/platform/comfyui/testdata/ui_i2i.json`
- Create: `services/server/internal/platform/comfyui/testdata/object_info.json`

**Interfaces:**
- Produces: `InspectUIWorkflow`, `CompileUIWorkflow`, `InstantiateWorkflow`, `WorkflowBindings`, `WorkflowInspection`, and `CompiledWorkflow`.
- Consumes: UI-format `nodes`/`links` and the selected instance's read-only `ObjectInfo` snapshot.
- Guarantees: deterministic canonical digests, confirmed semantic targets only, no URL fetches, and no mutation of stored templates.

- [ ] **Step 1: Write failing bound, suggestion, and compile tests**

```go
func TestInspectUIWorkflowSuggestsButDoesNotConfirmBindings(t *testing.T) {
	inspection, err := InspectUIWorkflow(loadFixture(t, "ui_t2i.json"), loadObjectInfo(t))
	if err != nil { t.Fatal(err) }
	if inspection.Suggestions.Prompts[0].NodeID != "6" || inspection.Confirmed {
		t.Fatalf("inspection = %#v", inspection)
	}
}

func TestCompileUIWorkflowRequiresConfirmedOutputAndPrompt(t *testing.T) {
	bindings := WorkflowBindings{Confirmed: false}
	if _, err := CompileUIWorkflow(loadFixture(t, "ui_t2i.json"), loadObjectInfo(t), bindings); !errors.Is(err, ErrWorkflowBindingsUnconfirmed) {
		t.Fatalf("error = %v", err)
	}
}

func TestInstantiateWorkflowDoesNotMutateTemplate(t *testing.T) {
	compiled := mustCompileFixture(t, "ui_i2i.json")
	before := append([]byte(nil), compiled.APITemplate...)
	_, err := InstantiateWorkflow(compiled.APITemplate, compiled.Bindings, WorkflowInputs{
		Prompts: []string{"portrait"}, Seed: ptr(int64(7)), Width: ptr(1024), Height: ptr(1024),
		References: []UploadedReference{{Name: "ref.png", Type: "input"}},
	})
	if err != nil { t.Fatal(err) }
	if !bytes.Equal(before, compiled.APITemplate) { t.Fatal("stored template mutated") }
}
```

Add table cases that reject 8 MiB + 1 byte, depth 65, 2,049 nodes, 8,193 links, duplicate node IDs, dangling links, non-object roots, API-format-only payloads, `url`, `command`, `script`, and string fields longer than 64 KiB.

- [ ] **Step 2: Run compiler tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/platform/comfyui -run 'Test(Inspect|Compile|Instantiate|UIWorkflow)' -count=1
```

Expected: FAIL because no UI workflow compiler exists.

- [ ] **Step 3: Implement the strict parser and semantic compiler**

```go
type CompiledWorkflow struct {
	APITemplate       json.RawMessage
	Bindings          WorkflowBindings
	WorkflowDigest    string
	APITemplateDigest string
	RequiredNodes     []string
	RequiredModels    []string
}

func InspectUIWorkflow(raw json.RawMessage, objectInfo ObjectInfo) (WorkflowInspection, error)
func CompileUIWorkflow(raw json.RawMessage, objectInfo ObjectInfo, bindings WorkflowBindings) (CompiledWorkflow, error)
func InstantiateWorkflow(template json.RawMessage, bindings WorkflowBindings, inputs WorkflowInputs) (json.RawMessage, error)
```

Parse with a token-counting/depth-counting decoder before unmarshalling. Add a custom `UnmarshalJSON` on `ObjectInputs` that retains `RequiredOrder` and `OptionalOrder` while preserving the existing maps, so widget positions are derived from the actual `/object_info` key order. Suggestions use node class, input names, and graph connections but always return `Confirmed=false`. During compile, verify every target exists and is a writable constant input, translate links to `[originNodeID, originSlot]`, copy constant widgets from UI nodes, and reject attempts to bind `class_type` or a connected input. `InstantiateWorkflow` deep-copies the template and writes only targets declared by the stored binding set.

The compiler test file defines `loadFixture(t, name) json.RawMessage`, `loadObjectInfo(t) ObjectInfo`, `mustCompileFixture(t, name) CompiledWorkflow`, and `ptr[T any](value T) *T`. Add this concrete fuzz target:

```go
func FuzzUIWorkflowParser(f *testing.F) {
	f.Add([]byte(`{"nodes":[],"links":[]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = InspectUIWorkflow(raw, ObjectInfo{})
	})
}
```

- [ ] **Step 4: Run deterministic, fuzz-seed, and race checks**

Run:

```bash
cd services/server && go test ./internal/platform/comfyui -count=1
cd services/server && go test -race ./internal/platform/comfyui -run 'Test(Inspect|Compile|Instantiate)' -count=10
cd services/server && go test ./internal/platform/comfyui -run FuzzUIWorkflowParser -fuzztime=5s
```

Expected: PASS; repeated compilation produces identical template bytes and digests.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/platform/comfyui/client.go services/server/internal/platform/comfyui/client_http_test.go services/server/internal/platform/comfyui/workflow_compile.go services/server/internal/platform/comfyui/workflow_compile_test.go services/server/internal/platform/comfyui/testdata
git commit -m "feat(comfyui): compile bounded UI workflows"
```

---

### Task 3: Add immutable workflow registry operations and default resolution

**Files:**
- Modify: `services/server/internal/service/settings/autodl_workflows.go`
- Modify: `services/server/internal/service/settings/autodl_workflows_test.go`
- Modify: `services/server/internal/service/settings/autodl_workflow_types.go`

**Interfaces:**
- Produces: `CreateAutoDLWorkflow`, `ReplaceAutoDLWorkflow`, `DuplicateAutoDLWorkflow`, `SetAutoDLWorkflowState`, `SetAutoDLWorkflowDefaults`, and `ResolveAutoDLWorkflow`.
- Produces: profile state changes without physical deletion and unique default selection by reference count.
- Consumes: confirmed compiler output from Task 2.

- [ ] **Step 1: Write failing registry state/default tests**

```go
func TestResolveAutoDLWorkflowFailsClosedOnAmbiguousDefaults(t *testing.T) {
	service := seededGenericWorkflowSettingsWithRawDefaults(t, []AutoDLWorkflowDefault{
		{ID: "one-a", MinReferences: 1, MaxReferences: 1, WorkflowProfileID: "a"},
		{ID: "one-b", MinReferences: 1, MaxReferences: 1, WorkflowProfileID: "b"},
	})
	_, err := service.ResolveAutoDLWorkflow(context.Background(), AutoDLWorkflowResolveRequest{ReferenceCount: 1})
	if !errors.Is(err, ErrAutoDLWorkflowDefaultAmbiguous) { t.Fatalf("error = %v", err) }
}

func TestArchiveKeepsHistoricalVersionResolvableButBlocksNewSelection(t *testing.T) {
	service, version := seededEnabledWorkflow(t)
	if _, err := service.SetAutoDLWorkflowState(context.Background(), version.ProfileID, AutoDLWorkflowStateMutation{Archived: ptr(true)}); err != nil { t.Fatal(err) }
	if _, err := service.ResolveAutoDLWorkflow(context.Background(), AutoDLWorkflowResolveRequest{WorkflowProfileID: version.ProfileID, ForNewTask: true}); !errors.Is(err, ErrAutoDLWorkflowUnavailable) { t.Fatalf("error = %v", err) }
	if _, err := service.GetAutoDLWorkflowVersion(context.Background(), version.ProfileID, version.VersionID); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: Run registry tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/service/settings -run 'Test(ResolveAutoDLWorkflow|ArchiveKeeps|DuplicateAutoDL|SetAutoDLWorkflow)' -count=1
```

Expected: FAIL because only replace-in-place and delete operations exist.

- [ ] **Step 3: Implement exact registry mutations**

```go
type AutoDLWorkflowResolveRequest struct {
	WorkflowProfileID string
	VersionID         string
	ReferenceCount    int
	ForNewTask        bool
}

type AutoDLWorkflowCreateMutation struct {
	ID             string
	Name           string
	Description    string
	Compiled       comfyui.CompiledWorkflow
	References     AutoDLReferenceContract
	PromptGuide    string
}

type AutoDLWorkflowVersionMutation struct {
	ExpectedCurrentVersionID string
	Compiled                 comfyui.CompiledWorkflow
	References               AutoDLReferenceContract
	PromptGuide              string
}

type AutoDLWorkflowStateMutation struct {
	Enabled        *bool `json:"enabled,omitempty"`
	AutoSelectable *bool `json:"autoSelectable,omitempty"`
	Archived       *bool `json:"archived,omitempty"`
}

type AutoDLWorkflowDefault struct {
	ID                string `json:"id"`
	MinReferences     int    `json:"minReferences"`
	MaxReferences     int    `json:"maxReferences"`
	WorkflowProfileID string `json:"workflowProfileId"`
}

type ResolvedAutoDLWorkflow struct {
	ProfileID         string
	VersionID         string
	Name              string
	WorkflowDigest    string
	APITemplateDigest string
	APITemplate       json.RawMessage
	Bindings          AutoDLWorkflowBindings
	References        AutoDLReferenceContract
	PromptGuide       string
	AutoSelectable    bool
}
```

The test file defines `seededGenericWorkflowSettingsWithRawDefaults(t, defaults) *Settings`, `seededEnabledWorkflow(t) (*Settings, testWorkflowIdentity)`, and `boolPtr(value bool) *bool`. The raw-default helper simulates corrupt or legacy ambiguous storage so resolver fail-closed behavior remains independently tested even though the public default mutation rejects overlaps. `testWorkflowIdentity` contains only `ProfileID` and `VersionID`; helpers save confirmed compiler-shaped fixtures and fail immediately on setup errors.

Require unique stable IDs, monotonic version sequences, canonical digests from Task 2, `MediaKind=image`, `RouteID=autodl.image`, confirmed bindings, and reference bounds `0 <= min <= max <= 8`. A created or duplicated profile starts disabled and not auto-selectable until at least one instance validation passes and the user explicitly enables it. `ReplaceAutoDLWorkflow` appends and changes `CurrentVersionID`; it never mutates prior versions. `DuplicateAutoDLWorkflow` creates a new profile and first version with new IDs. `SetAutoDLWorkflowState` changes only `Enabled`, `AutoSelectable`, and `Archived`. Defaults point to stable profiles, public mutations reject overlapping ranges, and the resolver still fails closed if corrupt stored data contains ambiguity; new-task resolution selects only the current version.

- [ ] **Step 4: Run settings tests**

Run:

```bash
cd services/server && go test ./internal/service/settings -count=1
cd services/server && go test -race ./internal/service/settings -run 'TestAutoDLWorkflow' -count=5
```

Expected: PASS; no method physically deletes a workflow profile or version.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/service/settings/autodl_workflow_types.go services/server/internal/service/settings/autodl_workflows.go services/server/internal/service/settings/autodl_workflows_test.go
git commit -m "feat(settings): manage generic workflow versions"
```

---

### Task 4: Preview and validate workflows against exact AutoDL instances

**Files:**
- Create: `services/server/internal/platform/autodl/host_key_scan.go`
- Create: `services/server/internal/platform/autodl/host_key_scan_test.go`
- Create: `services/server/internal/service/generation/autodl_workflow_admin.go`
- Create: `services/server/internal/service/generation/autodl_workflow_admin_test.go`
- Modify: `services/server/internal/service/settings/autodl_instances.go`
- Modify: `services/server/internal/service/settings/autodl_instances_test.go`

**Interfaces:**
- Produces: `AutoDLWorkflowAdmin.ScanFingerprint`, `.CheckInstance`, `.Preview`, `.Create`, `.Replace`, and `.Validate`.
- Produces: instance connection lookup and a Keychain-backed `autodl.TunnelPasswordSource` adapter without exposing password bytes to handlers.
- Consumes: Task 2 compiler, Task 3 registry, existing `autodl.TunnelManager`, and existing `comfyui.Client`.

- [ ] **Step 1: Write failing read-only validation tests**

```go
func TestAutoDLWorkflowValidateUsesObjectInfoWithoutSubmittingPrompt(t *testing.T) {
	client := &fakeComfyClient{objectInfo: objectInfoFixture(t)}
	admin := newWorkflowAdminForTest(t, client)
	result, err := admin.Validate(context.Background(), AutoDLWorkflowValidationRequest{
		InstanceProfileID: "instance-a", WorkflowProfileID: "portrait", VersionID: "version-1",
	})
	if err != nil || result.Status != settings.AutoDLWorkflowValidationReady { t.Fatalf("result=%#v err=%v", result, err) }
	if client.submitCalls != 0 || client.uploadCalls != 0 { t.Fatalf("mutating calls = %d/%d", client.submitCalls, client.uploadCalls) }
}

func TestAutoDLWorkflowValidationBecomesStaleAfterFingerprintChange(t *testing.T) {
	admin, settingsService := seededValidatedAdmin(t)
	mustChangeInstanceFingerprint(t, settingsService, "instance-a")
	got := mustInstanceValidation(t, settingsService, "instance-a", "portrait", "version-1")
	if got.Status != settings.AutoDLWorkflowValidationStale { t.Fatalf("validation = %#v", got) }
}

func TestAutoDLInstanceCheckReturnsPortAndHealthWithoutBaseURL(t *testing.T) {
	admin := newWorkflowAdminForTest(t, &fakeComfyClient{stats: healthySystemStats()})
	got, err := admin.CheckInstance(context.Background(), "instance-a")
	encoded, _ := json.Marshal(got)
	if err != nil || !got.Connected || got.LocalPort < 1 || bytes.Contains(encoded, []byte("baseUrl")) {
		t.Fatalf("check=%#v err=%v", got, err)
	}
}
```

Add cases for missing class, missing bound input, unavailable model enum, absent output node, digest mismatch, wrong version, disabled instance, missing password/fingerprint, and canceled tunnel creation.

- [ ] **Step 2: Run admin tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/service/generation -run 'TestAutoDLWorkflow(Validate|Validation|Preview)' -count=1
```

Expected: FAIL because no workflow administration service exists.

- [ ] **Step 3: Implement read-only preview and validation**

```go
type AutoDLWorkflowValidationRequest struct {
	InstanceProfileID string
	WorkflowProfileID string
	VersionID         string
}

type AutoDLWorkflowAdmin struct {
	settings autoDLWorkflowStore
	tunnels  autodl.TunnelManager
	client   func(string) (comfyui.Client, error)
	clock    func() time.Time
}

type autoDLWorkflowStore interface {
	GetAutoDLInstance(context.Context, string) (settings.AutoDLInstanceProfile, error)
	GetAutoDLWorkflowVersion(context.Context, string, string) (settings.ResolvedAutoDLWorkflow, error)
	CreateAutoDLWorkflow(context.Context, settings.AutoDLWorkflowCreateMutation) (settings.AutoDLWorkflowProfileResponse, error)
	ReplaceAutoDLWorkflow(context.Context, string, settings.AutoDLWorkflowVersionMutation) (settings.AutoDLWorkflowProfileResponse, error)
	SaveAutoDLWorkflowValidation(context.Context, string, settings.AutoDLWorkflowValidation) (settings.AutoDLInstanceResponse, error)
}
```

`ScanFingerprint` accepts only a parsed instance SSH host/port and performs a bounded SSH key exchange without credentials or a remote command; it returns the observed `SHA256:` fingerprint for explicit user confirmation and does not persist it. `CheckInstance` requires the confirmed fingerprint and Keychain password, uses the managed tunnel, calls `/system_stats`, and returns only `connected`, `localPort`, ComfyUI version, device-name summary, and a redacted reason.

`Preview` resolves the selected instance, establishes the existing managed tunnel, reads `/object_info`, and returns suggestions without persisting or enabling a profile. `Create` and `Replace` repeat parsing/compilation server-side using the submitted UI JSON plus confirmed bindings; never trust a client-supplied API template or digest. `Validate` checks exact class/input/model/output requirements and stores `profileId + versionId + workflowDigest + apiTemplateDigest + objectInfoDigest + instanceFingerprint`. Connection changes, version replacement, or object-info digest changes mark older results `stale`.

Implement `Settings.Password(ctx, credentialRef) ([]byte, error)` by copying the Keychain string into a new byte slice and clearing intermediate byte buffers owned by the adapter after the tunnel handshake. Do not expose this method through HTTP.

The test file defines a complete `fakeComfyClient` implementing the existing `comfyui.Client`; every mutating method increments a counter and returns a fixed error unless the test explicitly configures it. `newWorkflowAdminForTest`, `seededValidatedAdmin`, `mustChangeInstanceFingerprint`, and `mustInstanceValidation` construct real in-memory settings documents with fake Keychain/tunnel dependencies, so tests exercise persistence rather than mocking validation state.

- [ ] **Step 4: Run admin, settings, tunnel, and client race tests**

Run:

```bash
cd services/server && go test ./internal/service/generation ./internal/service/settings ./internal/platform/autodl ./internal/platform/comfyui -count=1
cd services/server && go test -race ./internal/service/generation ./internal/service/settings -run 'TestAutoDLWorkflow' -count=5
```

Expected: PASS and fake clients record zero `/prompt` calls.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/platform/autodl/host_key_scan.go services/server/internal/platform/autodl/host_key_scan_test.go services/server/internal/service/generation/autodl_workflow_admin.go services/server/internal/service/generation/autodl_workflow_admin_test.go services/server/internal/service/settings/autodl_instances.go services/server/internal/service/settings/autodl_instances_test.go
git commit -m "feat(comfyui): validate workflow profiles per instance"
```

---

### Task 5: Expose redacted AutoDL instance and workflow administration APIs

**Files:**
- Create: `services/server/internal/http/handlers/autodl_settings.go`
- Create: `services/server/internal/http/handlers/autodl_settings_test.go`
- Modify: `services/server/internal/http/routes/routes.go`
- Modify: `services/server/internal/app/app.go`
- Modify: `services/server/internal/app/wire.go`
- Modify: `services/server/internal/app/api.go`

**Interfaces:**
- Produces: `/api/v1/settings/autodl` instance/workflow/default endpoints.
- Consumes: existing settings service and Task 4 `AutoDLWorkflowAdmin`.
- Guarantees: thin handlers, bounded JSON bodies, stable status mapping, and response redaction.

- [ ] **Step 1: Write failing handler contract tests**

```go
func TestAutoDLSettingsWorkflowCreateRejectsClientTemplateAndDigest(t *testing.T) {
	router, admin := autoDLSettingsRouter(t)
	body := `{"name":"Portrait","instanceProfileId":"instance-a","uiWorkflow":{"nodes":[],"links":[]},"apiTemplate":{"evil":true},"workflowDigest":"client-value","bindings":{"confirmed":true}}`
	response := performJSON(t, router, http.MethodPost, "/settings/autodl/workflows", body)
	if response.Code != http.StatusBadRequest || admin.createCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, admin.createCalls, response.Body.String())
	}
}

func TestAutoDLSettingsResponsesContainNoPasswordsOrBaseURLs(t *testing.T) {
	response := getSeededAutoDLSettings(t)
	for _, forbidden := range []string{"secret-value", "http://127.0.0.1", "/Users/example/private"} {
		if strings.Contains(response, forbidden) { t.Fatalf("response leaked %q", forbidden) }
	}
}
```

- [ ] **Step 2: Run handler tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/http/handlers -run TestAutoDLSettings -count=1
```

Expected: FAIL because routes and handlers do not exist.

- [ ] **Step 3: Add exact administration routes**

```text
GET    /api/v1/settings/autodl
POST   /api/v1/settings/autodl/instances
PUT    /api/v1/settings/autodl/instances/:instanceId
PUT    /api/v1/settings/autodl/instances/:instanceId/password
DELETE /api/v1/settings/autodl/instances/:instanceId/password
DELETE /api/v1/settings/autodl/instances/:instanceId
POST   /api/v1/settings/autodl/instances/:instanceId/scan-fingerprint
POST   /api/v1/settings/autodl/instances/:instanceId/check
POST   /api/v1/settings/autodl/workflows/preview
POST   /api/v1/settings/autodl/workflows
POST   /api/v1/settings/autodl/workflows/:profileId/versions
POST   /api/v1/settings/autodl/workflows/:profileId/duplicate
PATCH  /api/v1/settings/autodl/workflows/:profileId
PUT    /api/v1/settings/autodl/defaults
POST   /api/v1/settings/autodl/workflows/:profileId/versions/:versionId/validate/:instanceId
```

Use a 9 MiB handler body ceiling so the 8 MiB workflow plus metadata is accepted. Return `400` for malformed/bounded data, `404` for missing instance/profile/version, `409` for stale version or ambiguous default, `422` for invalid binding/model/output validation, and `503` for Keychain/tunnel/ComfyUI availability. Do not add a workflow DELETE route.

The handler test file defines `autoDLSettingsRouter(t) (*gin.Engine, *fakeAutoDLWorkflowAdmin)`, `performJSON(t, router, method, path, body) *httptest.ResponseRecorder`, and `getSeededAutoDLSettings(t) string`. The fake admin records exact call counts and returns redacted fixtures; request decoding uses `DisallowUnknownFields`, so server-derived templates, digests, status, and validation records are rejected at the HTTP boundary.

Wire one application-scoped tunnel manager into admin, scheduler, and provider. Store it on `apiHandler` and call `CloseAll` from `apiHandler.Close` after canceling generation contexts.

Construct the AutoDL handler with a narrow `NotifyInstancesChanged()` dependency. Call it after committed instance, password, workflow state/version/default, and validation mutations so blocked automatic/manual scheduler waiters re-evaluate readiness; never notify on rejected or rolled-back mutations.

- [ ] **Step 4: Run handlers, routes, app shutdown, and swagger checks**

Run:

```bash
cd services/server && go test ./internal/http/handlers ./internal/http/routes ./internal/app -run 'Test.*(AutoDL|Close)' -count=1
cd services/server && go test -race ./internal/http/handlers ./internal/app -run 'TestAutoDL' -count=3
```

Expected: PASS; shutdown closes tunnels once and no endpoint returns a password or tunnel URL.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/http/handlers/autodl_settings.go services/server/internal/http/handlers/autodl_settings_test.go services/server/internal/http/routes/routes.go services/server/internal/app/app.go services/server/internal/app/wire.go services/server/internal/app/api.go
git commit -m "feat(api): administer AutoDL workflows safely"
```

---

### Task 6: Build the MediaLink instance and workflow settings UI

**Files:**
- Create: `apps/workspace/src/domains/settings/api/autodl.ts`
- Create: `apps/workspace/src/domains/settings/api/autodl.test.ts`
- Create: `apps/workspace/src/domains/settings/components/AutoDLSettingsPanel.tsx`
- Create: `apps/workspace/src/domains/settings/components/AutoDLSettingsPanel.test.tsx`
- Create: `apps/workspace/src/domains/settings/components/AutoDLWorkflowDialog.tsx`
- Create: `apps/workspace/src/domains/settings/components/AutoDLWorkflowDialog.test.tsx`
- Modify: `apps/workspace/src/pages/Settings.tsx`
- Modify: `apps/workspace/src/pages/Settings.test.tsx`
- Modify: `apps/workspace/src/domains/workspace/components/ProjectNavigatorPanels.tsx`
- Modify: `apps/workspace/src/domains/workspace/components/ProjectNavigatorPanels.test.tsx`
- Modify: `apps/workspace/src/lib/stores/settings.ts`

**Interfaces:**
- Produces: a `MediaLink 配置` settings tab with instance cards and workflow registry.
- Consumes: Task 5 redacted API only.
- Guarantees: password inputs clear after save, binding suggestions require explicit confirmation, and no destructive workflow deletion action is rendered.

- [ ] **Step 1: Write failing API and component tests**

```tsx
it("requires every suggested binding to be confirmed before save", async () => {
	render(<AutoDLWorkflowDialog open mode="create" onOpenChange={() => {}} />);
	await userEvent.upload(screen.getByLabelText("ComfyUI 工作流 JSON"), workflowFile);
	await userEvent.click(screen.getByRole("button", { name: "分析工作流" }));
	expect(screen.getByRole("button", { name: "保存工作流" })).toBeDisabled();
	await confirmEveryMapping();
	expect(screen.getByRole("button", { name: "保存工作流" })).toBeEnabled();
});

it("shows replace duplicate disable and archive without delete", async () => {
	render(<AutoDLSettingsPanel />);
	expect(await screen.findByRole("button", { name: "添加工作流" })).toBeInTheDocument();
	expect(screen.getByRole("button", { name: "替换版本" })).toBeInTheDocument();
	expect(screen.getByRole("button", { name: "复制" })).toBeInTheDocument();
	expect(screen.queryByRole("button", { name: "删除工作流" })).not.toBeInTheDocument();
});
```

- [ ] **Step 2: Run focused frontend tests and verify RED**

Run:

```bash
cd apps/workspace && pnpm test -- src/domains/settings/api/autodl.test.ts src/domains/settings/components/AutoDLSettingsPanel.test.tsx src/domains/settings/components/AutoDLWorkflowDialog.test.tsx src/pages/Settings.test.tsx src/domains/workspace/components/ProjectNavigatorPanels.test.tsx
```

Expected: FAIL because the API module, tab, and dialogs do not exist.

- [ ] **Step 3: Implement the settings surface with existing primitives**

```ts
export interface AutoDLWorkflowProfile {
	id: string;
	name: string;
	description?: string;
	mediaKind: "image";
	routeId: "autodl.image";
	enabled: boolean;
	autoSelectable: boolean;
	archived: boolean;
	currentVersionId: string;
	versions: AutoDLWorkflowVersion[];
}

export const autoDLSettingsKey = "/settings/autodl";
export const getAutoDLSettings = async () =>
	(await httpClient.get<AutoDLSettingsResponse>(autoDLSettingsKey)).data;
```

Add `medialink-config` to settings navigation under “生成配置”. Instance cards edit name, standard SSH command, Comfy port, fingerprint, enabled state, and a write-only password field. “扫描指纹” shows the newly observed fingerprint and requires a second explicit confirmation before saving it. “检查连接” displays connection state, dynamic local port, ComfyUI version, and device summary from the redacted check response; it never displays or edits a base URL.

Workflow cards show reference range, current immutable version, digest prefixes, enabled/auto-selectable/archived state, and per-instance validation results. The dialog first uploads JSON and selects an instance for read-only analysis, then displays suggested targets for prompt, seed, dimensions, ordered references, denoise, output prefix, output roles, and bounded scalar parameters. Each mapping has an explicit confirmation checkbox. Replace and duplicate reuse this dialog; archive requires confirmation and never deletes history.

In `AutoDLWorkflowDialog.test.tsx`, define `workflowFile` as a `File` containing the committed synthetic `nodes`/`links` fixture and `confirmEveryMapping()` as a local helper that checks every button whose accessible name starts with `确认映射：`. Mock Service Worker is not required; mock the exported API functions from `autodl.ts` and assert their exact request objects.

- [ ] **Step 4: Run settings tests, lint, and format checks**

Run:

```bash
cd apps/workspace && pnpm test -- src/domains/settings src/pages/Settings.test.tsx src/domains/workspace/components/ProjectNavigatorPanels.test.tsx
cd apps/workspace && pnpm lint
cd apps/workspace && pnpm format
```

Expected: PASS with no hardcoded model workflow cards or password values in snapshots.

- [ ] **Step 5: Commit**

```bash
git add apps/workspace/src/domains/settings/api/autodl.ts apps/workspace/src/domains/settings/api/autodl.test.ts apps/workspace/src/domains/settings/components/AutoDLSettingsPanel.tsx apps/workspace/src/domains/settings/components/AutoDLSettingsPanel.test.tsx apps/workspace/src/domains/settings/components/AutoDLWorkflowDialog.tsx apps/workspace/src/domains/settings/components/AutoDLWorkflowDialog.test.tsx apps/workspace/src/pages/Settings.tsx apps/workspace/src/pages/Settings.test.tsx apps/workspace/src/domains/workspace/components/ProjectNavigatorPanels.tsx apps/workspace/src/domains/workspace/components/ProjectNavigatorPanels.test.tsx apps/workspace/src/lib/stores/settings.ts
git commit -m "feat(settings): add MediaLink workflow registry UI"
```

---

### Task 7: Resolve workflow snapshots and profile-aware prompt guidance

**Files:**
- Create: `services/server/internal/service/generation/autodl_workflow_resolver.go`
- Create: `services/server/internal/service/generation/autodl_workflow_resolver_test.go`
- Modify: `packages/core/pkg/generation/catalog_medialink.go`
- Modify: `packages/core/pkg/generation/catalog_medialink_test.go`
- Modify: `services/server/internal/http/dto/generation.go`
- Modify: `services/server/internal/service/generation/generation_helpers.go`
- Modify: `services/server/internal/service/generation/generation_helpers_test.go`
- Modify: `services/server/internal/service/generation/autodl_instance_scheduler.go`
- Modify: `services/server/internal/service/generation/autodl_instance_scheduler_test.go`
- Modify: `services/server/internal/service/generation/generation_runtime_prompt_optimize.go`
- Modify: `services/server/internal/service/generation/generation_runtime_prompt_optimize_test.go`
- Modify: `apps/workspace/src/api/types/generation.ts`

**Interfaces:**
- Produces: a generic `AutoDL 云端工作流` catalog family and eight-reference public ceiling.
- Produces: one immutable `ResolvedAutoDLWorkflow` snapshot before scheduling.
- Produces: scheduler reservation identity extended by exact workflow version without changing its one-slot-per-instance behavior.
- Consumes: explicit `workflowProfileId` or Task 3 default rules and optional profile `promptGuide`.

- [ ] **Step 1: Write failing route, default, and prompt-guide tests**

```go
func TestAutoDLImageCatalogIsModelNeutralAndAllowsEightReferences(t *testing.T) {
	route, ok := FindRoute(RouteAutoDLImage)
	if !ok || route.MaxReferenceURLs != 8 || route.FamilyID != FamilyAutoDLWorkflow || route.Model != "comfyui-workflow" {
		t.Fatalf("route = %#v", route)
	}
}

func TestAutoDLWorkflowResolverSnapshotsCurrentVersionBeforeScheduling(t *testing.T) {
	resolver := newResolverWithDefault(t, 1, "portrait", "version-2")
	got, err := resolver.Resolve(context.Background(), coregeneration.Request{RouteID: coregeneration.RouteAutoDLImage, ReferenceURLs: []string{"data:image/png;base64,AA=="}})
	if err != nil || got.ProfileID != "portrait" || got.VersionID != "version-2" { t.Fatalf("got=%#v err=%v", got, err) }
}

func TestAutoDLPromptOptimizationUsesStoredGuideWithoutModelSwitch(t *testing.T) {
	request := autoDLPromptRequest("portrait", "写实人物，简洁中文")
	got := buildPromptOptimizationInput(request, fakePromptGuide("Use concise English subject tags followed by a detailed natural-language scene."))
	if !strings.Contains(got.Instructions, "Use concise English subject tags") || strings.Contains(got.Instructions, "Z-Image") { t.Fatalf("instructions=%q", got.Instructions) }
}
```

- [ ] **Step 2: Run focused backend tests and verify RED**

Run:

```bash
cd packages/core && go test ./pkg/generation -run TestAutoDLImageCatalogIsModelNeutral -count=1
cd services/server && go test ./internal/service/generation -run 'TestAutoDL(WorkflowResolver|PromptOptimization)' -count=1
```

Expected: FAIL because the catalog remains Z-Image-specific and no generic resolver exists.

- [ ] **Step 3: Implement model-neutral resolution and checkpoint fields**

```go
const (
	FamilyAutoDLWorkflow  = "autodl-workflow"
	VersionAutoDLWorkflow = "autodl-workflow-v1"
)

type AutoDLWorkflowResolver interface {
	Resolve(context.Context, coregeneration.Request) (settings.ResolvedAutoDLWorkflow, error)
	ResolveVersion(context.Context, string, string) (settings.ResolvedAutoDLWorkflow, error)
}
```

Rename only the internal catalog family/version labels; keep `RouteAutoDLImage`, `AdapterAutoDLComfyImage`, persisted route IDs, and public route text stable. Set `MaxReferenceURLs=8`. Resolution checks exact reference count, profile enabled/archive state, confirmed bindings, and unique default. It returns a cloned template and bindings, then the provider persists `WorkflowProfileID`, `WorkflowProfileVersion`, `WorkflowDigest`, and `APITemplateDigest` before `/prompt`.

Add `APITemplateDigest` to `GenerationTaskRuntimeState` and its merge/conflict validators. For AutoDL image prompt optimization, append only the selected profile's bounded `PromptGuide`; an empty guide uses the existing generic image optimizer. Do not branch on profile name or model family.

Extend `InstanceRequest`, `PersistedInstanceReservation`, and internal reservations with `WorkflowVersionID`. Change `AutoDLInstanceReadiness` to receive both profile and version IDs. Require exact version identity in `AcquireNew`, `Resume`, restore, and conflict checks; keep FIFO/wake/round-robin and one shared slot behavior unchanged. The readiness callback re-reads `/object_info`, compares its digest with the stored ready validation, and fails closed if the instance changed since validation.

The resolver test file defines `newResolverWithDefault(t, referenceCount, profileID, versionID) *autoDLWorkflowResolver` using the real settings registry. The prompt test file defines `autoDLPromptRequest(profileID, prompt) GenerationMessageRequest` and `fakePromptGuide(value) autoDLPromptGuideResolver`; the fake returns the guide only for the exact requested profile and records that no model-family argument was supplied.

- [ ] **Step 4: Run core generation and runtime identity tests**

Run:

```bash
cd packages/core && go test ./pkg/generation -count=1
cd services/server && go test ./internal/service/generation -run 'Test.*(AutoDL|RuntimeState|PromptOptimi)' -count=1
cd services/server && go test -race ./internal/service/generation -run 'TestAutoDLWorkflowResolver' -count=5
```

Expected: PASS; ambiguous/default errors occur before scheduler acquisition and before any ComfyUI call.

- [ ] **Step 5: Commit**

```bash
git add packages/core/pkg/generation/catalog_medialink.go packages/core/pkg/generation/catalog_medialink_test.go services/server/internal/http/dto/generation.go services/server/internal/service/generation/autodl_workflow_resolver.go services/server/internal/service/generation/autodl_workflow_resolver_test.go services/server/internal/service/generation/autodl_instance_scheduler.go services/server/internal/service/generation/autodl_instance_scheduler_test.go services/server/internal/service/generation/generation_helpers.go services/server/internal/service/generation/generation_helpers_test.go services/server/internal/service/generation/generation_runtime_prompt_optimize.go services/server/internal/service/generation/generation_runtime_prompt_optimize_test.go apps/workspace/src/api/types/generation.ts
git commit -m "feat(generation): resolve configurable AutoDL workflows"
```

---

### Task 8: Implement submit-once AutoDL image execution and recovery

**Files:**
- Create: `services/server/internal/service/generation/autodl_image_provider.go`
- Create: `services/server/internal/service/generation/autodl_image_provider_test.go`
- Create: `services/server/internal/service/generation/autodl_image_output.go`
- Create: `services/server/internal/service/generation/autodl_image_output_test.go`
- Modify: `services/server/internal/service/generation/generation_medialink_catalog.go`
- Modify: `services/server/internal/service/generation/generation_medialink_catalog_test.go`
- Modify: `services/server/internal/service/generation/generation_runtime_provider.go`
- Modify: `services/server/internal/service/generation/generation_runtime_tasks.go`
- Modify: `services/server/internal/service/generation/generation_runtime_test.go`
- Modify: `services/server/internal/app/wire.go`

**Interfaces:**
- Produces: `AutoDLImageProvider.Generate`, task-scoped `.ForRuntimeState`, and `.Get`.
- Produces: exact lease handling for pre-submit cancellation, accepted prompt, unknown submit, queued cancel, running prompt, completion, and download retry.
- Consumes: Tasks 2/7 resolver and instantiator, existing scheduler, tunnel, ComfyUI client, and runtime checkpoint conflict gates.

- [ ] **Step 1: Write failing provider state-machine tests**

```go
func TestAutoDLImageProviderPersistsSnapshotBeforeSubmit(t *testing.T) {
	provider, fake := newAutoDLImageProviderTest(t)
	_, err := provider.Generate(context.Background(), autoDLImageRequest("task-a", "portrait"))
	if err != nil { t.Fatal(err) }
	if fake.submitCalls != 1 { t.Fatalf("submit calls = %d", fake.submitCalls) }
	if fake.checkpoints[0].WorkflowProfileVersion == "" || fake.checkpoints[0].APITemplateDigest == "" || fake.checkpoints[0].ComfyPromptID != "" {
		t.Fatalf("pre-submit checkpoint = %#v", fake.checkpoints[0])
	}
}

func TestAutoDLImageProviderUnknownSubmitQuarantinesWithoutRetry(t *testing.T) {
	provider, fake := newAutoDLImageProviderTest(t)
	fake.submitErr = comfyui.ErrSubmissionOutcomeUnknown
	_, err := provider.Generate(context.Background(), autoDLImageRequest("task-a", "portrait"))
	if !errors.Is(err, comfyui.ErrSubmissionOutcomeUnknown) || fake.submitCalls != 1 || !fake.lease.quarantined {
		t.Fatalf("err=%v calls=%d lease=%#v", err, fake.submitCalls, fake.lease)
	}
}

func TestAutoDLImageProviderResumeUsesPersistedVersionNotCurrentVersion(t *testing.T) {
	provider, fake := newAutoDLImageProviderTest(t)
	bound := provider.ForRuntimeState(runtimeState("portrait", "version-1", "prompt-1"))
	_, err := bound.Get(mustAutoDLImageProviderTaskID(t, "instance-a", "prompt-1"))
	if err != nil || fake.resolvedVersion != "version-1" { t.Fatalf("version=%q err=%v", fake.resolvedVersion, err) }
}
```

Add tests for zero/eight/nine references, ordered uploads, 32 MiB per-image and 128 MiB total limits, template non-mutation, node errors, exact output roles, queued cancel via `DeleteQueuedPrompt(promptID)`, running cancel quarantine, history loss, download resume, corrupt image, wrong MIME, oversized dimensions/pixels, and two instances running concurrently while one instance serializes image/H3 work.

- [ ] **Step 2: Run focused provider tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/service/generation -run 'TestAutoDLImageProvider|TestReadValidatedAutoDLImage' -count=1
```

Expected: FAIL because no AutoDL image provider exists.

- [ ] **Step 3: Implement the task-scoped provider**

```go
type AutoDLImageProvider struct {
	resolver  AutoDLWorkflowResolver
	scheduler InstanceScheduler
	client    func(string) (comfyui.Client, error)
	resumeState GenerationTaskRuntimeState
}

func (provider *AutoDLImageProvider) ForRuntimeState(state GenerationTaskRuntimeState) coregeneration.Provider {
	clone := *provider
	clone.resumeState = state
	return &clone
}
```

`Generate` resolves and checkpoints the immutable workflow snapshot through the existing `coregeneration.ProgressCallbackFromOptions` path, acquires one scheduler lease, validates that exact profile version is ready on the selected instance, uploads references in binding order, instantiates a deep copy, and calls `/prompt` once. On success it binds and checkpoints `prompt_id` through the same callback before polling. Unknown submission quarantines the lease. Pre-submit cancellation releases normally. After prompt acceptance, cancellation tries only `DeleteQueuedPrompt(promptID)`; a running or unknown prompt remains quarantined until explicit reconciliation proves terminal state.

`Get` is available only on a provider returned by `ForRuntimeState`; it resolves the persisted profile/version/digests, resumes the exact instance reservation, and reads exact history. It never resolves the current version or changes instance. Output bindings select node IDs and roles from the persisted version. Download via `View` uses a 64 MiB response ceiling, allowed image MIME types, `image.DecodeConfig`, maximum dimension 16,384, maximum 40 million pixels, and full decode before returning base64 assets to the existing atomic asset-import path.

Extend MediaLink provider wiring so `SetMediaLinkProviders(codexImage, autodlImage, autodlH3, readiness)` includes the image provider. Add a stored-task provider factory path that calls `ForRuntimeState` only for `RouteAutoDLImage`. Restore scheduler reservations from non-terminal persisted AutoDL image and H3 tasks at startup; reject duplicate task/instance records rather than dropping one.

The provider test file defines `newAutoDLImageProviderTest(t) (*AutoDLImageProvider, *autoDLProviderFixture)`, `autoDLImageRequest(taskID, profileID) coregeneration.Request`, `runtimeState(profileID, versionID, promptID) GenerationTaskRuntimeState`, and `mustAutoDLImageProviderTaskID(t, instanceID, promptID) string`. The fixture supplies real scheduler behavior with fake profiles/tunnels, a complete fake ComfyUI client, a resolver keyed by immutable version, and a `ProgressCallbackOption` recorder. `autoDLImageRequest` sets the existing internal task-ID and progress callback options and never places secrets or local paths in request options.

- [ ] **Step 4: Run provider, runtime, scheduler, and race suites**

Run:

```bash
cd services/server && go test ./internal/service/generation -count=1
cd services/server && go test -race ./internal/service/generation -run 'Test.*AutoDL' -count=10
cd services/server && go vet ./internal/service/generation ./internal/app
```

Expected: PASS; fake transport evidence reports no second `/prompt` for resume, retry, timeout, cancellation, or restart paths.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/service/generation/autodl_image_provider.go services/server/internal/service/generation/autodl_image_provider_test.go services/server/internal/service/generation/autodl_image_output.go services/server/internal/service/generation/autodl_image_output_test.go services/server/internal/service/generation/generation_medialink_catalog.go services/server/internal/service/generation/generation_medialink_catalog_test.go services/server/internal/service/generation/generation_runtime_provider.go services/server/internal/service/generation/generation_runtime_tasks.go services/server/internal/service/generation/generation_runtime_test.go services/server/internal/app/wire.go
git commit -m "feat(generation): run configurable AutoDL image workflows"
```

---

### Task 9: Add workflow, exposed-parameter, and advanced instance selectors to generation forms

**Files:**
- Create: `apps/workspace/src/domains/generation/hooks/useAutoDLWorkflowOptions.ts`
- Create: `apps/workspace/src/domains/generation/hooks/useAutoDLWorkflowOptions.test.tsx`
- Create: `apps/workspace/src/domains/generation/components/AutoDLWorkflowControls.tsx`
- Create: `apps/workspace/src/domains/generation/components/AutoDLWorkflowControls.test.tsx`
- Modify: `apps/workspace/src/domains/generation/components/generationSettingsValue.ts`
- Modify: `apps/workspace/src/domains/generation/components/generationSettingsValue.test.ts`
- Modify: `apps/workspace/src/domains/generation/hooks/useGenerationSettingsForm.ts`
- Modify: `apps/workspace/src/domains/generation/hooks/useGenerationSettingsForm.test.tsx`
- Modify: `apps/workspace/src/domains/generation/components/GenerationSettingsForm.tsx`
- Modify: `apps/workspace/src/domains/generation/components/GenerationSettingsForm.test.tsx`
- Modify: `apps/workspace/src/domains/generation/hooks/useGenerationSubmit.ts`
- Modify: `apps/workspace/src/domains/generation/hooks/useGenerationSubmit.test.ts`

**Interfaces:**
- Produces: compatible workflow selector, declared scalar controls, automatic instance mode, and advanced manual instance selection.
- Consumes: Task 5 redacted settings plus existing route/reference state.
- Guarantees: hidden for Codex/H3, no model-specific UI, and manual instance choice never falls back.

- [ ] **Step 1: Write failing compatibility and submit tests**

```tsx
it("offers only enabled validated workflows compatible with current references", async () => {
	render(<GenerationSettingsHarness routeId="autodl.image" referenceCount={2} />);
	await userEvent.click(await screen.findByLabelText("云端工作流"));
	expect(screen.getByRole("option", { name: "多图角色一致性" })).toBeInTheDocument();
	expect(screen.queryByRole("option", { name: "纯文生图" })).not.toBeInTheDocument();
});

it("submits explicit workflow and manual instance without silent fallback", async () => {
	const request = generationSettingsValueForSubmit({
		...autoDLValue(), workflowProfileId: "portrait", instanceProfileId: "instance-b",
	});
	expect(request.workflowProfileId).toBe("portrait");
	expect(request.instanceProfileId).toBe("instance-b");
});
```

Add cases for automatic default, missing default error, ambiguous default error, archived/disabled/stale profiles, 0/1/2/8 reference changes, parameter range clamping rejection, batch submission, and Codex/H3 forms remaining unchanged.

- [ ] **Step 2: Run focused generation UI tests and verify RED**

Run:

```bash
cd apps/workspace && pnpm test -- src/domains/generation/hooks/useAutoDLWorkflowOptions.test.tsx src/domains/generation/components/AutoDLWorkflowControls.test.tsx src/domains/generation/components/generationSettingsValue.test.ts src/domains/generation/hooks/useGenerationSettingsForm.test.tsx src/domains/generation/components/GenerationSettingsForm.test.tsx src/domains/generation/hooks/useGenerationSubmit.test.ts
```

Expected: FAIL because the generation form does not load workflow registry options.

- [ ] **Step 3: Implement route-scoped controls**

```ts
export interface GenerationSettingsValue {
	kind: GenerationSettingsKind;
	routeId: string;
	workflowProfileId?: string;
	instanceProfileId?: string;
	workflowParameters?: Record<string, string | number | boolean>;
	params: Record<string, unknown>;
}
```

For `autodl.image`, fetch redacted settings through SWR and filter profiles by enabled state, archive state, confirmed current version, reference contract, and at least one ready instance. The selector includes “自动选择兼容工作流”; the backend remains authoritative for unique default resolution. Render only the profile's declared scalar parameters and validate exact types/ranges. The instance selector defaults to automatic and appears under “高级设置”; explicit instances must be enabled, password-backed, fingerprint-confirmed, and ready for the selected version. Preserve an unavailable manual ID in form state with a visible waiting/error label instead of silently clearing it.

The component test file defines `GenerationSettingsHarness` with explicit `routeId` and `referenceCount` props and seeds SWR with a redacted registry fixture. The settings-value test file defines `autoDLValue() GenerationSettingsValue` with route `autodl.image`, empty automatic IDs, and no parameters. All option assertions use visible profile display names while submitted values assert stable IDs.

- [ ] **Step 4: Run generation frontend and static checks**

Run:

```bash
cd apps/workspace && pnpm test -- src/domains/generation
cd apps/workspace && pnpm lint
cd apps/workspace && pnpm format
cd apps/workspace && pnpm build
```

Expected: PASS; existing character, scene, prop, storyboard, batch, Codex image, and H3 route tests remain green.

- [ ] **Step 5: Commit**

```bash
git add apps/workspace/src/domains/generation/hooks/useAutoDLWorkflowOptions.ts apps/workspace/src/domains/generation/hooks/useAutoDLWorkflowOptions.test.tsx apps/workspace/src/domains/generation/components/AutoDLWorkflowControls.tsx apps/workspace/src/domains/generation/components/AutoDLWorkflowControls.test.tsx apps/workspace/src/domains/generation/components/generationSettingsValue.ts apps/workspace/src/domains/generation/components/generationSettingsValue.test.ts apps/workspace/src/domains/generation/hooks/useGenerationSettingsForm.ts apps/workspace/src/domains/generation/hooks/useGenerationSettingsForm.test.tsx apps/workspace/src/domains/generation/components/GenerationSettingsForm.tsx apps/workspace/src/domains/generation/components/GenerationSettingsForm.test.tsx apps/workspace/src/domains/generation/hooks/useGenerationSubmit.ts apps/workspace/src/domains/generation/hooks/useGenerationSubmit.test.ts
git commit -m "feat(workspace): select AutoDL workflow profiles"
```

---

### Task 10: Complete no-consumption integration and macOS Apple Silicon verification

**Files:**
- Create: `services/server/internal/service/generation/autodl_image_integration_test.go`
- Create: `apps/workspace/src/domains/settings/components/AutoDLWorkflowJourney.test.tsx`
- Modify: `docs/generation-route-protocol.md`
- Modify: `docs/superpowers/plans/2026-08-30-medialink-autodl-zimage.md`
- Modify: files from Tasks 1–9 only when a failing verification exposes an in-scope defect.

**Interfaces:**
- Produces: fake end-to-end proof for import → confirm → version → validate → select → submit once → resume → download → existing asset link.
- Consumes: all prior tasks and existing MediaGo Drama regression suites.

- [ ] **Step 1: Add fake end-to-end workflow and restart tests**

```go
func TestAutoDLImageEndToEndUsesImmutableWorkflowAcrossRestart(t *testing.T) {
	harness := newAutoDLIntegrationHarness(t)
	profile := harness.ImportConfirmAndValidate("ui_i2i.json", "instance-a")
	task := harness.SubmitImage(profile.ID, []string{validReferenceDataURI(t)})
	harness.AssertPromptSubmissions(1)
	harness.Restart()
	result := harness.ResumeAndComplete(task.ID)
	harness.AssertPromptSubmissions(1)
	harness.AssertAssetLinked(result, "character", "character-a")
}
```

Add integration cases for two instances parallel, one instance image/H3 serialization, manual instance waiting without fallback, unknown submission quarantine, stale validation before submit, replace-version preserving an old task, Codex fallback requiring a new user-confirmed attempt, and no workflow JS/URL execution.

`newAutoDLIntegrationHarness(t)` uses temporary settings/workspace databases, fake Keychain, fake tunnels, loopback `httptest.Server` ComfyUI endpoints, the real compiler/registry/scheduler/provider, and the existing generation/media services. Its methods are `ImportConfirmAndValidate(fixture, instanceID)`, `SubmitImage(profileID, references)`, `AssertPromptSubmissions(count)`, `Restart()`, `ResumeAndComplete(taskID)`, and `AssertAssetLinked(result, resourceType, resourceID)`. `validReferenceDataURI(t)` returns a generated 1×1 PNG data URI. The harness refuses any non-loopback request and records every endpoint call.

- [ ] **Step 2: Run full backend verification**

Run:

```bash
cd packages/core && go test ./... -count=1
cd services/server && go test ./... -count=1
cd services/server && go test -race ./internal/platform/autodl ./internal/platform/comfyui ./internal/service/settings ./internal/service/generation -count=3
cd services/server && go vet ./...
```

Expected: PASS with all HTTP servers fake/loopback and zero live `/prompt` calls.

- [ ] **Step 3: Run full frontend verification**

Run:

```bash
cd apps/workspace && pnpm test
cd apps/workspace && pnpm lint
cd apps/workspace && pnpm format
cd apps/workspace && pnpm build
```

Expected: PASS.

- [ ] **Step 4: Verify macOS Apple Silicon packaging without publishing**

Run:

```bash
pnpm build:server
cd apps/workspace && pnpm electron:build:darwin-arm64
file release/mac-arm64/MediaLink.app/Contents/MacOS/MediaLink
```

Expected: staging succeeds and `file` reports an arm64 Mach-O executable. Do not build Windows/Intel artifacts and do not publish.

- [ ] **Step 5: Prove scope and safety boundaries**

Run:

```bash
git status --short
git diff --check
git diff --name-only HEAD~10..HEAD
rg -n "OpenAI Images|gpt-image.*api|/interrupt|exec.Command.*workflow|node:child_process|DeleteAutoDLWorkflowProfile" services/server apps/workspace packages/core
```

Expected: only planned files are changed; `work/` remains untracked and untouched; scans find no OpenAI Images integration, global interrupt, workflow execution, or destructive workflow delete path.

- [ ] **Step 6: Update protocol documentation and retire superseded plan steps**

Document the generic profile/version/validation fields, submit-once checkpoint order, exact cancellation behavior, and user-confirmed Codex fallback in `docs/generation-route-protocol.md`. At the top of the older Z-Image plan, mark its Task 6 and Tasks 8–15 as superseded by this file; do not rewrite its historical evidence.

- [ ] **Step 7: Commit verification-only changes**

```bash
git add services/server/internal/service/generation/autodl_image_integration_test.go apps/workspace/src/domains/settings/components/AutoDLWorkflowJourney.test.tsx docs/generation-route-protocol.md docs/superpowers/plans/2026-08-30-medialink-autodl-zimage.md
git commit -m "test(medialink): verify configurable workflow routing"
```

---

## Completion Criteria

- A user can add an arbitrary compatible ComfyUI image workflow by importing UI JSON, reviewing every semantic mapping, and saving an immutable version.
- No model family is hardcoded into workflow persistence, validation, scheduling, provider execution, or selection UI.
- A workflow/version is schedulable only when enabled, binding-confirmed, reference-compatible, and ready on the selected instance.
- Replace creates a new version; old task snapshots still resume against their original version and instance.
- The AutoDL image provider submits once, checkpoints identity, resumes exact history, and never uses global interrupt.
- Codex imagegen remains an explicit user-selected fallback, not an automatic hidden retry.
- Existing character, scene, prop, storyboard, asset, history, prompt optimization, H3 pool, and macOS Apple Silicon behavior pass regression tests.
- No real cloud generation, model install, FLUX repair, or `ComfyUIPhotoSync` mutation occurs during default verification.
