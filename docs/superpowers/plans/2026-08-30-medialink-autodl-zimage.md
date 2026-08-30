# MediaLink AutoDL Z-Image and Instance Pool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `AutoDL · Z-Image` to every existing MediaLink image-generation entry and run Z-Image or MiniMax H3 jobs through a shared, secure, multi-instance AutoDL pool with automatic scheduling and optional manual instance selection.

**Architecture:** Keep the existing generation request, task, asset-import, document-link, and history flows authoritative. Add a settings-owned AutoDL instance/profile registry, one tunnel and one execution slot per instance, a loopback-only ComfyUI client, a manifest-driven workflow instantiator, and a Z-Image provider that checkpoints `instanceProfileId + prompt_id` before polling. The future H3 provider consumes the same pool and scheduler rather than introducing a second connection stack.

**Tech Stack:** Go 1.25, GORM/SQLite app settings, macOS Keychain through `/usr/bin/security`, `golang.org/x/crypto/ssh`, ComfyUI HTTP API, React 19, TypeScript, SWR, Go `testing`, Vitest.

## Global Constraints

- Target only macOS Apple Silicon (`darwin/arm64`); do not add Windows, Intel Mac, or Linux packaging.
- Preserve the original MediaGo Drama character, scene, prop, storyboard, asset, task-history, preview, Skills, and prompt-template workflows.
- Expose exactly three visual routes: `Codex 生图`, `AutoDL · Z-Image`, and `AutoDL · MiniMax H3`; do not delete legacy provider source modules.
- Never call OpenAI Images API or add an OpenAI API-key field.
- Accept a standard OpenSSH login command plus a separate password; store the password only in macOS Keychain and never in SQLite, JSON responses, process arguments, runtime state, or logs.
- Support multiple dynamic AutoDL instances and configurable remote ComfyUI ports; use one execution slot per instance and allow different instances to run concurrently.
- Default to automatic compatible-instance selection; allow advanced manual selection and never silently override a manual selection.
- Accept ComfyUI API-format workflows only; node IDs live in profile manifests, not business code.
- Z-Image uses the text-to-image workflow with zero references and the image-to-image workflow with exactly one reference; reject more than one reference in the first release.
- Never resubmit when `/prompt` acceptance is unknown or when a persisted `prompt_id` exists.
- Run fake/no-consumption tests only. Real SSH inspection is read-only; real `/prompt` submission requires a separate explicit user approval.
- Avoid unrelated refactors and broad renames.

**Plan relationship:** Execute this plan's shared AutoDL Tasks 2-8 before resuming `2026-08-30-medialink-autodl-h3-video.md`. They supersede that older plan's single-instance Keychain, tunnel, ComfyUI client, settings, API, and connection-UI work. After the pool exists, retain only H3-specific workflow, provider, recovery, prompt, and UI tasks and adapt them to `AutoDLInstanceScheduler`; do not build a second H3-only connection stack.

---

## File Map

- `packages/core/pkg/generation/catalog_*` owns the public Z-Image route contract.
- `services/server/internal/service/settings/autodl_instances.go` owns non-secret instance/profile settings and response redaction.
- `services/server/internal/platform/keychain/generic_password.go` owns Keychain password storage.
- `services/server/internal/platform/autodl/ssh_command.go` parses the restricted login command.
- `services/server/internal/platform/autodl/tunnel_manager.go` owns per-instance SSH sessions and loopback listeners.
- `services/server/internal/platform/comfyui/` owns typed HTTP transport only.
- `services/server/internal/service/generation/comfy_workflow_profile.go` validates and instantiates semantic workflow profiles.
- `services/server/internal/service/generation/autodl_instance_scheduler.go` owns reservations and per-instance serialization.
- `services/server/internal/service/generation/autodl_zimage_provider.go` owns Z-Image upload, submit, poll, download, and recovery.
- Existing generation runtime files persist provider checkpoints and import validated assets.
- Settings React components manage instances and workflows; the existing generation form adds only the advanced instance selector.

---

### Task 1: Add the Z-Image route and durable request identity

**Files:**
- Modify: `packages/core/pkg/generation/catalog_adapters.go`
- Modify: `packages/core/pkg/generation/catalog_routes.go`
- Modify: `packages/core/pkg/generation/catalog_medialink.go`
- Modify: `packages/core/pkg/generation/catalog_medialink_test.go`
- Modify: `packages/core/pkg/generation/types.go`
- Modify: `services/server/internal/http/dto/generation.go`
- Modify: `services/server/internal/service/generation/generation_medialink_catalog.go`
- Modify: `services/server/internal/service/generation/generation_helpers.go`
- Modify: `services/server/internal/service/generation/generation_helpers_test.go`
- Modify: `services/server/internal/service/generation/generation_tasks_service_test.go`
- Modify: `apps/workspace/src/api/types/generation.ts`

**Interfaces:**
- Produces: `RouteAutoDLZImage = "autodl.zimage"`, `AdapterAutoDLComfyZImage = "autodl.comfyui.z-image"`.
- Produces: `coregeneration.Request.InstanceProfileID string` and `GenerationMessageRequest.InstanceProfileID string` where empty means automatic scheduling; `GenerationRequestFromMessage` copies it to the provider request.
- Produces: new runtime fields `InstanceProfileID`, `WorkflowProfileID`, `WorkflowProfileVersion`, and `WorkflowDigest`; preserves the existing `ComfyPromptID` and `SubmittedAt` fields.
- Produces: `GenerationTaskFromMessage` snapshots a non-empty manually selected instance into initial runtime state so a background task remains visibly pinned while waiting.

- [ ] **Step 1: Write failing catalog and persistence tests**

```go
func TestMediaLinkCatalogIncludesZImageWithOneReference(t *testing.T) {
	route, ok := FindRoute(RouteAutoDLZImage)
	if !ok || route.Kind != KindImage || route.Provider != ProviderAutoDL || route.MaxReferenceURLs != 1 {
		t.Fatalf("route = %+v, found = %v", route, ok)
	}
}

func TestGenerationTaskRuntimeStateRoundTripsAutoDLIdentity(t *testing.T) {
	service := NewGenerationTaskService(filepath.Join(t.TempDir(), "settings.db"), nil)
	want := GenerationTaskRuntimeState{
		InstanceProfileID: "instance-a", WorkflowProfileID: "zimage-t2i",
		WorkflowProfileVersion: "v1", WorkflowDigest: "sha256:abc",
		ComfyPromptID: "prompt-123", SubmittedAt: "2026-08-30T12:00:00Z",
	}
	if err := service.Upsert(GenerationTaskRecord{ID: "task-a", Kind: "image", RuntimeState: want}); err != nil { t.Fatal(err) }
	got, ok, err := service.Get("task-a")
	if err != nil || !ok || got.RuntimeState != want { t.Fatalf("state = %+v, ok = %v, err = %v", got.RuntimeState, ok, err) }
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
cd packages/core && go test ./pkg/generation -run 'TestMediaLinkCatalog.*ZImage' -count=1
cd services/server && go test ./internal/service/generation -run 'TestGenerationTaskRuntimeStateRoundTripsAutoDLIdentity' -count=1
```

Expected: FAIL because the route and runtime fields do not exist.

- [ ] **Step 3: Add the minimal route and DTO contract**

```go
const RouteAutoDLZImage = "autodl.zimage"
const AdapterAutoDLComfyZImage = "autodl.comfyui.z-image"

InstanceProfileID       string `json:"instanceProfileId,omitempty"`
WorkflowProfileID       string `json:"workflowProfileId,omitempty"`
WorkflowProfileVersion  string `json:"workflowProfileVersion,omitempty"`
WorkflowDigest          string `json:"workflowDigest,omitempty"`
```

Add a `FamilyZImage`/`VersionZImageV1` image route with aspect ratio, width/height or resolution, and seed parameters already representable by the catalog. Set `SupportsReferenceURLs=true`, `MaxReferenceURLs=1`, and keep application-owned background execution so waiting-for-instance progress can be persisted without blocking the HTTP request. Extend `mediaLinkRouteProviders` with `autodlZImage` but preserve nil-provider readiness until Task 9 wires it.

- [ ] **Step 4: Run affected tests**

Run:

```bash
cd packages/core && go test ./pkg/generation -count=1
cd services/server && go test ./internal/service/generation -run 'Test.*(MediaLink|RuntimeState)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/core/pkg/generation services/server/internal/http/dto/generation.go services/server/internal/service/generation apps/workspace/src/api/types/generation.ts
git commit -m "feat(generation): add AutoDL Z-Image route contract"
```

---

### Task 2: Parse AutoDL login commands and store passwords in Keychain

**Files:**
- Create: `services/server/internal/platform/autodl/ssh_command.go`
- Create: `services/server/internal/platform/autodl/ssh_command_test.go`
- Create: `services/server/internal/platform/keychain/generic_password.go`
- Create: `services/server/internal/platform/keychain/generic_password_test.go`

**Interfaces:**
- Produces: `autodl.ParseSSHLoginCommand(input string) (SSHLoginTarget, error)`.
- Produces: `keychain.GenericPasswordStore` implementing `Set(ctx, service, account, secret)`, `Get`, and `Delete`.

- [ ] **Step 1: Write parser and Keychain runner tests**

```go
func TestParseSSHLoginCommand(t *testing.T) {
	got, err := ParseSSHLoginCommand("ssh -p 23456 root@gpu.example.com")
	if err != nil || got.Host != "gpu.example.com" || got.Port != 23456 || got.User != "root" {
		t.Fatalf("target = %+v, err = %v", got, err)
	}
}

func TestParseSSHLoginCommandRejectsExecutableOptions(t *testing.T) {
	for _, input := range []string{
		"ssh -o ProxyCommand=evil root@gpu.example.com",
		"ssh root@gpu.example.com | sh",
		"ssh root@gpu.example.com uptime",
	} {
		if _, err := ParseSSHLoginCommand(input); err == nil { t.Fatalf("accepted %q", input) }
	}
}
```

Use a fake command runner to assert `security add-generic-password ... -w` receives the password on stdin and not in `Args`.

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/platform/autodl ./internal/platform/keychain -count=1
```

Expected: FAIL because both packages are missing.

- [ ] **Step 3: Implement a strict parser and Keychain adapter**

```go
type SSHLoginTarget struct {
	Host string
	Port int
	User string
}

type GenericPasswordStore interface {
	Set(context.Context, string, string, string) error
	Get(context.Context, string, string) (string, error)
	Delete(context.Context, string, string) error
}
```

Allow only `ssh`, one destination, `-p <1..65535>`, and optional `-l <user>`. Reject metacharacters and every unrecognized option. Call `/usr/bin/security add-generic-password -U -s <service> -a <account> -w` with `-w` last and the password on stdin; capture `find-generic-password -w` output without logging it.

- [ ] **Step 4: Run normal and race tests**

Run:

```bash
cd services/server && go test ./internal/platform/autodl ./internal/platform/keychain -count=1
cd services/server && go test -race ./internal/platform/autodl ./internal/platform/keychain -count=3
```

Expected: PASS and no secret appears in failure messages.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/platform/autodl services/server/internal/platform/keychain
git commit -m "feat(security): accept AutoDL SSH commands safely"
```

---

### Task 3: Persist the non-secret instance pool and workflow profiles

**Files:**
- Create: `services/server/internal/service/settings/autodl_instances.go`
- Create: `services/server/internal/service/settings/autodl_instances_test.go`
- Modify: `services/server/internal/service/settings/store.go`
- Modify: `services/server/internal/app/wire.go`

**Interfaces:**
- Produces: `AutoDLInstanceProfile`, `AutoDLWorkflowProfile`, and `AutoDLSettingsResponse`.
- Produces: CRUD methods plus `SetAutoDLInstancePassword` and `ClearAutoDLInstancePassword`.
- Consumes: Task 2 parser and `GenericPasswordStore`.

- [ ] **Step 1: Write failing settings tests**

```go
func TestSaveAutoDLInstancePersistsNoPasswordOrRawCommand(t *testing.T) {
	service := newAutoDLSettingsForTest(t)
	_, err := service.SaveAutoDLInstance(ctx, AutoDLInstanceMutation{
		Name: "图像一号", SSHCommand: "ssh -p 23456 root@gpu.example.com",
		Password: "secret-value", ComfyPort: 6006, Enabled: true,
	})
	if err != nil { t.Fatal(err) }
	raw := storedAppSetting(t)
	if strings.Contains(raw, "secret-value") || strings.Contains(raw, "ssh -p") { t.Fatal(raw) }
}
```

Add tests for stable IDs, replacement without losing workflow validation records, password clear, redacted responses, malformed stored JSON, and deletion of only the exact Keychain account.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/service/settings -run TestAutoDL -count=1
```

Expected: FAIL because AutoDL settings do not exist.

- [ ] **Step 3: Implement versioned JSON settings**

```go
const autoDLSettingsKey = "medialink.autodl.instance-pool.v1"
const autoDLKeychainService = "app.medialink.autodl"

type AutoDLInstanceProfile struct {
	ID string `json:"id"`
	Name string `json:"name"`
	Host string `json:"host"`
	SSHPort int `json:"sshPort"`
	SSHUser string `json:"sshUser"`
	ComfyPort int `json:"comfyPort"`
	HostFingerprint string `json:"hostFingerprint,omitempty"`
	CredentialRef string `json:"credentialRef"`
	Enabled bool `json:"enabled"`
}
```

Store workflow JSON and manifests in the same versioned app-setting document or a second versioned app-setting key, but never store health state or secrets as durable truth. Return `hasPassword` instead of a password value.

- [ ] **Step 4: Run settings tests and vet**

Run:

```bash
cd services/server && go test ./internal/service/settings -run TestAutoDL -count=1
cd services/server && go vet ./internal/service/settings ./internal/platform/autodl ./internal/platform/keychain
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/service/settings services/server/internal/app/wire.go
git commit -m "feat(settings): persist AutoDL instance profiles"
```

---

### Task 4: Build the multi-instance SSH tunnel manager

**Files:**
- Create: `services/server/internal/platform/autodl/tunnel_manager.go`
- Create: `services/server/internal/platform/autodl/tunnel_manager_test.go`
- Modify: `services/server/go.mod`
- Modify: `services/server/go.sum`

**Interfaces:**
- Consumes: `AutoDLInstanceProfile`-equivalent connection data and Keychain password retrieval.
- Produces: `TunnelManager.Ensure(ctx, target) (Tunnel, error)`, `Close(instanceID)`, and `CloseAll()`.

- [ ] **Step 1: Write fake-SSH tunnel tests**

```go
type Tunnel struct {
	InstanceProfileID string
	BaseURL string
}

type TunnelManager interface {
	Ensure(context.Context, TunnelTarget) (Tunnel, error)
	Close(string) error
	CloseAll() error
}
```

Test two instances opening different local ports, configurable remote ports, exact fingerprint acceptance, fingerprint mismatch rejection, concurrent `Ensure` deduplication, reconnect, context cancellation, and `CloseAll`.

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/platform/autodl -run TestTunnel -count=1
```

Expected: FAIL because the manager does not exist.

- [ ] **Step 3: Implement one session and loopback listener per instance**

Use `golang.org/x/crypto/ssh` with password auth and an exact SHA-256 host-key callback. Bind local listeners to `127.0.0.1:0`; dial only remote `127.0.0.1:<ComfyPort>`. Keep the password in memory only for SSH authentication and zero the owned byte slice after client construction where practical.

- [ ] **Step 4: Run normal and race tests**

Run:

```bash
cd services/server && go test ./internal/platform/autodl -run TestTunnel -count=1
cd services/server && go test -race ./internal/platform/autodl -run TestTunnel -count=5
```

Expected: PASS with no leaked listener after shutdown.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/platform/autodl services/server/go.mod services/server/go.sum
git commit -m "feat(network): manage multiple AutoDL tunnels"
```

---

### Task 5: Add a loopback-only ComfyUI HTTP client

**Files:**
- Create: `services/server/internal/platform/comfyui/client.go`
- Create: `services/server/internal/platform/comfyui/client_http.go`
- Create: `services/server/internal/platform/comfyui/client_http_test.go`

**Interfaces:**
- Produces: typed `SystemStats`, `ObjectInfo`, `Queue`, `UploadImage`, `SubmitPrompt`, `History`, `View`, and `DeleteQueuedPrompt` methods.
- Consumes: the `Tunnel.BaseURL` from Task 4.

- [ ] **Step 1: Write fake-server contract tests**

```go
type Client interface {
	SystemStats(context.Context) (SystemStats, error)
	ObjectInfo(context.Context) (ObjectInfo, error)
	Queue(context.Context) (QueueState, error)
	UploadImage(context.Context, UploadImageRequest) (UploadedImage, error)
	SubmitPrompt(context.Context, json.RawMessage, string) (PromptSubmission, error)
	History(context.Context, string) (PromptHistory, error)
	View(context.Context, OutputFile) (io.ReadCloser, http.Header, error)
	DeleteQueuedPrompt(context.Context, string) (bool, error)
}
```

Test response-size limits, non-2xx responses, malformed JSON, unknown submission outcome, exact prompt deletion, and rejection of non-loopback base URLs.

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/platform/comfyui -count=1
```

Expected: FAIL because the package is missing.

- [ ] **Step 3: Implement bounded HTTP calls**

Use context-aware requests, `io.LimitReader`, stable errors, multipart upload to `/upload/image`, POST to `/prompt`, GET `/history/{prompt_id}`, GET `/view`, and exact queue deletion only. Do not expose a generic arbitrary-URL request method.

- [ ] **Step 4: Run normal and race tests**

Run:

```bash
cd services/server && go test ./internal/platform/comfyui -count=1
cd services/server && go test -race ./internal/platform/comfyui -count=3
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/platform/comfyui
git commit -m "feat(comfyui): add bounded loopback client"
```

---

### Task 6: Validate and instantiate Z-Image workflow profiles

**Files:**
- Create: `services/server/internal/service/generation/comfy_workflow_profile.go`
- Create: `services/server/internal/service/generation/comfy_workflow_profile_test.go`
- Create: `services/server/internal/service/generation/testdata/zimage_t2i_api.json`
- Create: `services/server/internal/service/generation/testdata/zimage_i2i_api.json`

**Interfaces:**
- Produces: `ValidateWorkflowProfile`, `ValidateWorkflowOnInstance`, and `InstantiateWorkflow`.
- Produces kinds `zimage-t2i`, `zimage-i2i`, `h3-ref2va`, and `h3-fl2va`; this task fully implements the two Z-Image rules and reserves the H3 kind names for its existing plan.

- [ ] **Step 1: Write profile validation tests**

```go
type WorkflowBindings struct {
	Prompt InputBinding `json:"prompt"`
	Seed *InputBinding `json:"seed,omitempty"`
	Width *InputBinding `json:"width,omitempty"`
	Height *InputBinding `json:"height,omitempty"`
	ReferenceImage *InputBinding `json:"referenceImage,omitempty"`
	OutputPrefix InputBinding `json:"outputPrefix"`
}
```

Test valid T2I and I2I fixtures, UI-format JSON rejection, missing node/input, T2I incorrectly requiring a reference, I2I missing a reference binding, workflow immutability after instantiation, digest stability, and `/object_info` missing-node/model failures.

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/service/generation -run TestWorkflowProfile -count=1
```

Expected: FAIL because workflow profiles are not implemented.

- [ ] **Step 3: Implement manifest-driven deep-copy instantiation**

```go
type WorkflowInputValues struct {
	Prompt string
	Seed int64
	Width int
	Height int
	ReferenceImage *comfyui.UploadedImage
	OutputPrefix string
}
```

Reject top-level `nodes`/`links`, require an API map shaped as node ID to `{class_type, inputs}`, calculate SHA-256 over canonical stored workflow bytes, deep copy before mutation, and set only declared bindings. Enforce exactly zero references for T2I and one for I2I.

- [ ] **Step 4: Run focused and full generation tests**

Run:

```bash
cd services/server && go test ./internal/service/generation -run TestWorkflowProfile -count=1
cd services/server && go test ./internal/service/generation -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/service/generation/comfy_workflow_profile.go services/server/internal/service/generation/comfy_workflow_profile_test.go services/server/internal/service/generation/testdata
git commit -m "feat(comfyui): validate Z-Image workflows"
```

---

### Task 7: Implement the shared instance scheduler

**Files:**
- Create: `services/server/internal/service/generation/autodl_instance_scheduler.go`
- Create: `services/server/internal/service/generation/autodl_instance_scheduler_test.go`

**Interfaces:**
- Consumes: instance settings, tunnel readiness, and per-instance workflow validation.
- Produces: `AcquireNew`, `Resume`, `RestoreReservations`, and terminal/safe-pre-submit release operations. A reservation survives a transient provider return after `prompt_id` exists.

- [ ] **Step 1: Write deterministic scheduling tests**

```go
type InstanceRequest struct {
	TaskID string
	WorkflowProfileID string
	SelectedInstanceProfileID string
}

type InstanceLease interface {
	InstanceProfileID() string
	Tunnel() autodl.Tunnel
	BindPrompt(promptID string) error
	ReleaseBeforeSubmit()
	ReleaseTerminal()
}

type InstanceScheduler interface {
	AcquireNew(context.Context, InstanceRequest) (InstanceLease, error)
	Resume(context.Context, taskID, instanceProfileID, promptID string) (InstanceLease, error)
	RestoreReservations([]PersistedInstanceReservation) error
	Quarantine(instanceProfileID, reason string)
}
```

Test round-robin selection of two compatible idle instances, actual cross-instance concurrency, single-instance serialization across image and video requests, disabled/incompatible exclusion, automatic waiting, manual-instance waiting without fallback, cancellation while waiting, safe pre-submit release, prompt-bound reservation surviving disconnect, restart reservation restoration, exact-task resume, and terminal release. Test that an unknown submission quarantines the instance until explicit reconciliation instead of freeing its slot.

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/service/generation -run TestAutoDLInstanceScheduler -count=1
```

Expected: FAIL because the scheduler is missing.

- [ ] **Step 3: Implement reservations with one slot per instance**

Keep scheduler health state in memory and reload durable profile definitions from settings. Use a condition/channel wake-up rather than polling sleeps. Selection order must be stable round-robin among currently compatible profiles; a manual ID filters to exactly one candidate. Before dispatch starts, restore reservations from active generation tasks. A lease may be released immediately only while submission is provably impossible; after `BindPrompt`, keep the reservation until terminal history/cancellation is confirmed. Quarantine an instance after `submission_outcome_unknown` so another task cannot overlap a possibly accepted remote job.

- [ ] **Step 4: Run focused race tests**

Run:

```bash
cd services/server && go test -race ./internal/service/generation -run TestAutoDLInstanceScheduler -count=10
```

Expected: PASS with at most one active lease per instance and concurrent leases on distinct instances.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/service/generation/autodl_instance_scheduler.go services/server/internal/service/generation/autodl_instance_scheduler_test.go
git commit -m "feat(generation): schedule AutoDL instance jobs"
```

---

### Task 8: Expose instance and workflow administration APIs

**Files:**
- Modify: `services/server/internal/http/handlers/settings.go`
- Modify: `services/server/internal/http/handlers/settings_test.go`
- Modify: `services/server/internal/http/routes/routes.go`
- Modify: `services/server/internal/app/app.go`

**Interfaces:**
- Consumes: Tasks 3-7 settings, tunnel, ComfyUI, and workflow validation services.
- Produces: `/api/v1/settings/autodl/instances` CRUD, password, fingerprint scan/confirm, check, workflow CRUD, and workflow validation endpoints.

- [ ] **Step 1: Write handler tests before routes**

Test malformed SSH commands, password response redaction, unconfirmed fingerprint, fingerprint mismatch, configurable ComfyUI port, two independently healthy instances, UI-format workflow rejection, missing node/model, and successful no-generation workflow validation.

```go
func TestAutoDLInstanceResponseNeverReturnsPassword(t *testing.T) {
	response := requestJSON(t, http.MethodPost, "/api/v1/settings/autodl/instances", map[string]any{
		"name": "GPU A", "sshCommand": "ssh -p 23456 root@gpu.example.com",
		"password": "secret", "comfyPort": 6007,
	})
	if bytes.Contains(response.Body, []byte("secret")) { t.Fatal("password leaked") }
}
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/http/handlers -run TestAutoDL -count=1
```

Expected: FAIL with route not found or missing handler methods.

- [ ] **Step 3: Add narrow DTOs and routes**

Never accept a public `comfyuiBaseURL`. A check endpoint must build the URL from the manager-owned loopback tunnel. Fingerprint confirmation accepts the scanned fingerprint value and profile revision so a stale browser response cannot confirm a changed target.

- [ ] **Step 4: Run handler and route tests**

Run:

```bash
cd services/server && go test ./internal/http/handlers ./internal/http/routes -run TestAutoDL -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/http/handlers services/server/internal/http/routes services/server/internal/app/app.go
git commit -m "feat(api): manage AutoDL instance pool"
```

---

### Task 9: Implement the Z-Image provider and provider wiring

**Files:**
- Create: `services/server/internal/service/generation/autodl_zimage_provider.go`
- Create: `services/server/internal/service/generation/autodl_zimage_provider_test.go`
- Modify: `services/server/internal/service/generation/generation_medialink_catalog.go`
- Modify: `services/server/internal/service/generation/generation_runtime_provider.go`
- Modify: `services/server/internal/app/wire.go`

**Interfaces:**
- Consumes: scheduler, profile instantiator, ComfyUI client, asset limits, and progress callback.
- Produces: `AutoDLZImageProvider` implementing `coregeneration.Provider`.
- Provider task IDs use `autodl.zimage:<base64url(instanceProfileId)>:<base64url(promptId)>` and are parsed only by one tested helper.
- Test helpers in `autodl_zimage_provider_test.go`: `newZImageProviderHarness(t) (*AutoDLZImageProvider, *[]string)` and `validT2IRequest() coregeneration.Request` construct deterministic fakes used by the call-order test.

- [ ] **Step 1: Write provider tests with fake dependencies**

Cover T2I success, I2I upload success, more than one reference rejection before lease acquisition, automatic and manual selection, checkpoint ordering, reconnect `Get`, unknown submission outcome, missing history, exact queue cancellation, invalid output, oversized output, and no asset URL leakage.

```go
func TestAutoDLZImageCheckpointsBeforePolling(t *testing.T) {
	provider, calls := newZImageProviderHarness(t)
	_, err := provider.Generate(context.Background(), validT2IRequest())
	if err != nil { t.Fatal(err) }
	want := []string{"acquire", "preflight", "instantiate", "submit", "checkpoint:instance-a:prompt-123", "history", "view", "validate"}
	if !reflect.DeepEqual(*calls, want) { t.Fatalf("calls = %v, want %v", *calls, want) }
}
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/service/generation -run TestAutoDLZImageProvider -count=1
```

Expected: FAIL because the provider does not exist.

- [ ] **Step 3: Implement submit-once generation**

Before `/prompt`, emit waiting/preparing progress without a prompt ID. Immediately after an accepted response, bind the lease to the prompt and emit metadata containing the selected instance, workflow ID/version/digest, prompt ID, and submission timestamp. `Get` decodes the provider ID, resumes the same task reservation, and queries only that instance. A disconnect after binding keeps the reservation; terminal completion, confirmed exact cancellation, or confirmed remote loss releases it. An unknown submission quarantines the instance. Validate downloaded PNG/JPEG/GIF according to the existing image resource limits before returning a local/internal asset for the existing import path.

- [ ] **Step 4: Wire the third MediaLink provider**

```go
func (workflow *GenerationService) SetMediaLinkProviders(
	codexImage coregeneration.Provider,
	autodlZImage coregeneration.Provider,
	autodlH3 coregeneration.Provider,
	readiness func(context.Context, string) (bool, string),
)
```

Readiness for `autodl.zimage` is true only when at least one enabled instance has the required profile validated. Preserve the two-second caller-aware readiness bound.

- [ ] **Step 5: Run focused, full, and race tests**

Run:

```bash
cd services/server && go test ./internal/service/generation -run TestAutoDLZImageProvider -count=1
cd services/server && go test ./internal/service/generation -count=1
cd services/server && go test -race ./internal/service/generation -run 'TestAutoDL(ZImageProvider|InstanceScheduler)' -count=5
```

Expected: PASS without a real SSH or `/prompt` call.

- [ ] **Step 6: Commit**

```bash
git add services/server/internal/service/generation services/server/internal/app/wire.go
git commit -m "feat(generation): run Z-Image on AutoDL"
```

---

### Task 10: Make runtime recovery, retry, and cancellation instance-safe

**Files:**
- Modify: `services/server/internal/service/generation/generation_runtime_tasks.go`
- Modify: `services/server/internal/app/generation_worker.go`
- Modify: `services/server/internal/service/generation/generation_helpers.go`
- Modify: `services/server/internal/service/generation/generation_runtime_test.go`
- Modify: `services/server/internal/repository/generation_task_repo.go`
- Modify: `services/server/internal/repository/generation_task_repo_test.go`

**Interfaces:**
- Consumes: Task 9 provider checkpoints.
- Produces: statuses `waiting_for_instance`, `waiting_for_selected_instance`, `waiting_reconnect`, `submission_outcome_unknown`, and `remote_task_lost`.
- Test helper in `generation_runtime_test.go`: `newRecoveryHarness(t, state)` persists one active task and returns counters for `GetCalls(instanceID, promptID)` and `SubmitCalls()` plus `RestartAndRecover(t)`.

- [ ] **Step 1: Write crash-window and cancellation tests**

Test restart after persisted prompt ID restores the scheduler reservation before accepting new work and calls `Get` with submit count zero; restart with `submitting` and no prompt ID becomes `submission_outcome_unknown` and quarantines that instance; address change preserves instance identity; deleted queued task never acquires a lease; canceled ComfyUI queue item deletes only its exact prompt; completed tasks release the reservation and clear polling IDs while preserving workflow audit fields; concurrent retry is failed-only and cannot resurrect a deleted task.

```go
func TestRecoverZImageUsesOriginalInstanceAndPrompt(t *testing.T) {
	harness := newRecoveryHarness(t, GenerationTaskRuntimeState{InstanceProfileID: "instance-a", ComfyPromptID: "prompt-123"})
	harness.RestartAndRecover(t)
	if harness.GetCalls("instance-a", "prompt-123") != 1 { t.Fatal("original task was not polled once") }
	if harness.SubmitCalls() != 0 { t.Fatal("recovery resubmitted the prompt") }
}
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/service/generation ./internal/repository -run 'Test.*(RecoverZImage|AutoDLRetry|AutoDLCancel)' -count=1
```

Expected: FAIL because the generic runtime does not yet recognize AutoDL identity and statuses.

- [ ] **Step 3: Add provider-aware recovery gates**

Treat any Z-Image task with `InstanceProfileID` and `ComfyPromptID` as identified and poll through `Get`; never call `Generate`. A task at `submitting` without a prompt ID is not safe to retry automatically. Reuse the existing atomic retry/delete ownership pattern proven by Codex Task 3.

- [ ] **Step 4: Run focused concurrency and full package tests**

Run:

```bash
cd services/server && go test -race ./internal/service/generation ./internal/repository -run 'Test.*(RecoverZImage|AutoDLRetry|AutoDLCancel)' -count=10
cd services/server && go test ./internal/service/generation ./internal/repository -count=1
```

Expected: PASS and submit count remains exactly one across restart.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/service/generation services/server/internal/repository
git commit -m "fix(generation): recover AutoDL tasks without resubmission"
```

---

### Task 11: Add route-specific Z-Image prompt optimization

**Files:**
- Modify: `services/server/internal/service/generation/generation_runtime_prompt_optimize.go`
- Create: `services/server/internal/service/generation/generation_runtime_prompt_optimize_test.go`
- Modify: `services/server/internal/service/generation/generation_prompt_supplements.go`
- Modify: `packages/mcp/pkg/mcp/generation_prompt_supplements_test.go`

**Interfaces:**
- Consumes: `RouteAutoDLZImage` and the existing Codex text-completion service.
- Produces: a Z-Image-specific optimizer instruction while preserving the existing `optimizedPrompt` field.
- Test helper in the new test file: `captureOptimizerInstruction(t, routeID) string` installs the existing fake text-completion provider and returns its captured system instruction.

- [ ] **Step 1: Write enabled/disabled optimizer tests**

```go
func TestZImagePromptOptimizationUsesRouteSpecificInstruction(t *testing.T) {
	instruction := captureOptimizerInstruction(t, coregeneration.RouteAutoDLZImage)
	for _, phrase := range []string{"Z-Image", "参考图", "构图", "光线", "负面约束", "保留用户意图"} {
		if !strings.Contains(instruction, phrase) { t.Fatalf("missing %q in %q", phrase, instruction) }
	}
	if strings.Contains(instruction, "镜头时间线") { t.Fatal("Z-Image received H3 video instructions") }
}
```

Also assert disabled optimization sends the original prompt unchanged and Codex/H3 routes keep their own instructions.

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
cd services/server && go test ./internal/service/generation -run 'Test.*ZImagePromptOptimization' -count=1
```

Expected: FAIL because Z-Image has no optimizer branch.

- [ ] **Step 3: Add the narrow route switch**

Select optimizer instructions by route ID. Do not add a new text provider, model picker, API key, or automatic prompt optimization when the existing switch is off.

- [ ] **Step 4: Run generation and MCP prompt tests**

Run:

```bash
cd services/server && go test ./internal/service/generation -run 'Test.*Prompt' -count=1
cd packages/mcp && go test ./pkg/mcp -run 'Test.*Prompt' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/server/internal/service/generation packages/mcp/pkg/mcp
git commit -m "feat(prompts): optimize Z-Image requests"
```

---

### Task 12: Build the AutoDL instance and workflow settings UI

**Files:**
- Modify: `apps/workspace/src/domains/settings/api/settings.ts`
- Create: `apps/workspace/src/domains/settings/components/AutoDLInstancesPanel.tsx`
- Create: `apps/workspace/src/domains/settings/components/AutoDLInstancesPanel.test.tsx`
- Create: `apps/workspace/src/domains/settings/components/AutoDLWorkflowProfilesPanel.tsx`
- Create: `apps/workspace/src/domains/settings/components/AutoDLWorkflowProfilesPanel.test.tsx`
- Modify: `apps/workspace/src/pages/Settings.tsx`
- Modify: `apps/workspace/src/pages/Settings.test.tsx`

**Interfaces:**
- Consumes: Task 8 JSON APIs.
- Produces: instance CRUD, SSH command/password entry, fingerprint confirmation, per-instance health, and Z-Image workflow import/validation.

- [ ] **Step 1: Write UI tests first**

Test adding two instances, no password echo, changed address requiring fingerprint confirmation, custom ComfyUI port, independent workflow capability badges, disabling one instance, importing API JSON, rejecting UI JSON, and displaying validation errors without exposing secrets.

```tsx
it("adds two independently configurable AutoDL instances", async () => {
  render(<AutoDLInstancesPanel />);
  await addInstance("GPU A", "ssh -p 21001 root@gpu-a.example", "6006");
  await addInstance("GPU B", "ssh -p 21002 root@gpu-b.example", "6007");
  expect(screen.getByText("GPU A")).toBeTruthy();
  expect(screen.getByText("GPU B")).toBeTruthy();
});
```

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
cd apps/workspace && pnpm test -- --run src/domains/settings/components/AutoDLInstancesPanel.test.tsx src/domains/settings/components/AutoDLWorkflowProfilesPanel.test.tsx src/pages/Settings.test.tsx
```

Expected: FAIL because the panels do not exist.

- [ ] **Step 3: Implement the panels using existing settings layout primitives**

Show only parsed host/user/ports, `已保存密码` or `未保存密码`, fingerprint state, connection state, current task, and workflow capabilities. Keep workflow node mapping in an advanced disclosure; do not create a second settings application.

- [ ] **Step 4: Run focused and settings tests**

Run:

```bash
cd apps/workspace && pnpm test -- --run src/domains/settings/components/AutoDLInstancesPanel.test.tsx src/domains/settings/components/AutoDLWorkflowProfilesPanel.test.tsx src/pages/Settings.test.tsx
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/workspace/src/domains/settings apps/workspace/src/pages/Settings.tsx apps/workspace/src/pages/Settings.test.tsx
git commit -m "feat(settings): configure AutoDL instance pool"
```

---

### Task 13: Add automatic/manual instance selection to the existing generation UI

**Files:**
- Modify: `apps/workspace/src/domains/generation/hooks/useGenerationSettingsForm.ts`
- Modify: `apps/workspace/src/domains/generation/hooks/useGenerationSettingsForm.test.tsx`
- Modify: `apps/workspace/src/domains/generation/components/GenerationSettingsForm.tsx`
- Modify: `apps/workspace/src/domains/generation/components/GenerationSettingsForm.test.tsx`
- Modify: `apps/workspace/src/domains/generation/hooks/useGenerationSubmit.ts`
- Modify: `apps/workspace/src/domains/generation/components/generationSettingsValue.ts`
- Modify: `apps/workspace/src/domains/generation/components/generationSettingsValue.test.ts`

**Interfaces:**
- Consumes: Z-Image route metadata, instance summary API, and `GenerationMessageRequest.instanceProfileId`.
- Produces: default automatic selection and an advanced manual instance selector only for AutoDL routes.

- [ ] **Step 1: Write generation-form tests**

Test route selection across every shared image entry through the common form, default `自动分配`, manual instance serialization, manual instance unavailable copy, 0 references selecting T2I, 1 selecting I2I, and 2 references rejected before HTTP submission.

```tsx
it("rejects multiple Z-Image references before submit", async () => {
  const request = buildImageRequest({ routeId: "autodl.zimage", referenceAssetIds: ["a", "b"] });
  await expect(submit(request)).rejects.toThrow("AutoDL · Z-Image 首版最多支持 1 张参考图");
  expect(api.generate).not.toHaveBeenCalled();
});
```

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```bash
cd apps/workspace && pnpm test -- --run src/domains/generation/hooks/useGenerationSettingsForm.test.tsx src/domains/generation/components/GenerationSettingsForm.test.tsx src/domains/generation/components/generationSettingsValue.test.ts
```

Expected: FAIL because instance selection is not represented.

- [ ] **Step 3: Add the advanced selector without changing core entry layouts**

Add `instanceProfileId?: string` to the normalized settings value and outgoing request. Empty remains automatic. Display the selector only for AutoDL routes and keep all character/scene/prop/storyboard entry components unchanged.

- [ ] **Step 4: Run the complete generation frontend suite**

Run:

```bash
cd apps/workspace && pnpm test -- --run src/domains/generation
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add apps/workspace/src/domains/generation
git commit -m "feat(generation): select AutoDL instances"
```

---

### Task 14: Complete no-consumption integration and macOS arm64 verification

**Files:**
- Modify: `services/server/internal/service/generation/generation_medialink_catalog_test.go`
- Modify: `services/server/internal/http/handlers/generation_tasks_test.go`
- Modify: `apps/workspace/src/domains/generation/components/MediaGenerationDialogs.test.tsx`
- Modify: `docs/generation-route-protocol.md`
- Modify only if required by verified failures: `apps/workspace/Taskfile.yml`, root `Taskfile.yml`

**Interfaces:**
- Consumes: all earlier tasks.
- Produces: a passing three-route MediaLink build with no real generation.

- [ ] **Step 1: Add end-to-end fake tests**

Exercise character, scene, prop, and storyboard image requests through `autodl.zimage`; verify existing asset/document association, two-instance parallelism, one-instance image/video serialization, restart recovery with one submit, and no hidden provider route exposure.

- [ ] **Step 2: Run backend verification**

Run:

```bash
cd services/server && go test ./internal/platform/autodl ./internal/platform/comfyui ./internal/service/settings ./internal/service/generation ./internal/http/handlers ./internal/http/routes ./internal/repository -count=1
cd services/server && go test -race ./internal/platform/autodl ./internal/platform/comfyui ./internal/service/generation -count=1
cd services/server && go vet ./internal/platform/autodl ./internal/platform/comfyui ./internal/service/settings ./internal/service/generation ./internal/http/handlers ./internal/repository
```

Expected: PASS.

- [ ] **Step 3: Run frontend verification**

Run:

```bash
cd apps/workspace && pnpm test -- --run
cd apps/workspace && pnpm typecheck
```

Expected: PASS.

- [ ] **Step 4: Verify release boundaries without publishing**

Run the existing MediaLink macOS arm64 package task with `--publish never`; inspect the executable architectures with `file`, the bundle identifier with `plutil`, and confirm there are no Windows artifacts or publish-enabled leaves.

- [ ] **Step 5: Prove no real provider call occurred**

```bash
git diff --check
git status --short
rg -n 'api\.openai\.com|images/generations|OPENAI_API_KEY' services/server/internal/service/generation/autodl_zimage_provider.go apps/workspace/src/domains/settings
```

Expected: no OpenAI Images API integration, clean diff checks, and test logs containing only fake ComfyUI URLs.

- [ ] **Step 6: Commit integration fixes**

```bash
git add services/server apps/workspace docs/generation-route-protocol.md Taskfile.yml
git commit -m "test: verify MediaLink AutoDL image routing"
```

---

### Task 15: Inspect real workflows, then stop at the paid-test gate

**Files:**
- Modify only after inspection: the two user-imported workflow profile records through the application API/UI; do not commit credentials or instance-specific addresses.
- Update: `docs/superpowers/specs/2026-08-30-medialink-autodl-zimage-design.md` only if inspection proves a previously approved capability assumption false.

**Interfaces:**
- Consumes: current user-provided SSH login command, password entered through MediaLink, and the two workflow files on AutoDL.
- Produces: verified profile manifests and a read-only readiness report.

- [ ] **Step 1: Obtain current connection data safely**

Ask for the current non-secret SSH command. Have the user enter the password through MediaLink; do not request or accept it in chat.

- [ ] **Step 2: Perform read-only inspection**

Connect through the app-managed tunnel, call `/system_stats`, `/object_info`, and `/queue`, retrieve or have the user import both workflow JSON files, and validate their prompt, seed, size, reference, and output bindings. Do not call `/prompt`.

- [ ] **Step 3: Report readiness and stop**

Report instance IDs/names, configurable ComfyUI ports, validated profile versions/digests, missing nodes/models, and whether T2I/I2I instantiation succeeds without submission.

- [ ] **Step 4: Request separate paid-test approval**

Offer exactly these proposed real tests: one T2I image, one I2I image using one existing asset, optional two-instance parallel generation, and one restart/reconnect recovery. State that they consume GPU time. Do not proceed until the user explicitly approves the listed tests.
