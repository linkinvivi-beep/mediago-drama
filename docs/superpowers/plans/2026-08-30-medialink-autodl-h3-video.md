# MediaLink AutoDL H3 Video Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route all visible video generation through an SSH-forwarded AutoDL ComfyUI instance running MiniMax H3, with secure credentials, validated REF2VA/FL2VA workflow profiles, durable prompt IDs, reconnect recovery, and existing MediaLink asset import.

**Architecture:** The Go server owns macOS Keychain credential references, a fingerprint-pinned SSH forwarder, a loopback-only ComfyUI client, workflow-profile validation, and the H3 provider. Workflow JSON remains user-supplied API format plus a semantic manifest, so node IDs are data rather than source-code constants. Generation persists the ComfyUI `prompt_id` immediately and every later status check addresses that same ID.

**Tech Stack:** Go, `golang.org/x/crypto/ssh`, `github.com/coder/websocket`, macOS Keychain `security`, ComfyUI HTTP/WebSocket APIs, GORM task state, React/SWR, Go `testing`, Vitest.

## Global Constraints

- MediaLink does not start, stop, rent, bill, or manage AutoDL instances.
- Connect only through a local SSH tunnel to remote `127.0.0.1:6006`; do not expose or persist a public ComfyUI URL.
- Store passwords, private keys, and private-key passphrases only in macOS Keychain; persist opaque credential references elsewhere.
- Pin the SSH host-key fingerprint after explicit first-use confirmation; mismatch is a hard failure.
- Accept ComfyUI API-format workflow JSON only; reject UI workflow JSON containing top-level `nodes`/`links`.
- Support exactly `REF2VA` and `FL2VA` profiles in the first release.
- After `prompt_id` is persisted, reconnect and poll that same ID; never submit a replacement prompt automatically.
- Real H3 runs consume cloud GPU resources; automated acceptance uses fakes until separately authorized.

---

## File Map

**Create**

- `services/server/internal/platform/keychain/store.go`, `store_darwin.go`, `store_darwin_test.go` — opaque secret store and injected command runner.
- `services/server/internal/platform/sshtunnel/manager.go`, `manager_test.go` — fingerprint-pinned Go SSH forwarder and reconnect state.
- `services/server/internal/platform/comfyui/client.go`, `client_http.go`, `client_http_test.go` — typed loopback ComfyUI API client.
- `services/server/internal/service/generation/h3_workflow_profile.go`, `_test.go` — API workflow/manifest validation and instantiation.
- `services/server/internal/service/generation/autodl_h3_provider.go`, `_test.go` — upload/submit/monitor/download provider.
- `services/server/internal/service/settings/autodl.go`, `_test.go` — non-secret settings and Keychain-backed credential mutation.
- `services/server/internal/http/handlers/autodl_settings.go`, `_test.go` — settings, host scan, connection test, profile validation endpoints.
- `apps/workspace/src/domains/settings/components/AutoDLH3Panel.tsx`, `_test.tsx` — connection and profile UI.
- `apps/workspace/src/domains/settings/components/H3WorkflowProfilesPanel.tsx`, `_test.tsx` — REF2VA/FL2VA import/validation UI.

**Modify**

- `services/server/go.mod`, `go.sum` — promote `golang.org/x/crypto` to direct use and add the WebSocket client.
- `services/server/internal/app/app.go` and wiring files found by `rg -n 'NewSettings|NewGenerationService' services/server/internal/app services/server/cmd` — construct one tunnel manager and provider.
- `services/server/internal/service/settings/store.go` — install the AutoDL settings dependencies.
- `services/server/internal/http/routes/routes.go` — register AutoDL endpoints.
- `services/server/internal/service/generation/generation_medialink_catalog.go` — bind `autodl.minimax-h3`.
- `services/server/internal/service/generation/generation_runtime_tasks.go` and tests — persist `prompt_id` before monitoring and recover waiting tasks.
- `services/server/internal/service/generation/generation_runtime_assets.go` — reuse video cache/poster import.
- `apps/workspace/src/domains/settings/api/settings.ts`, `apps/workspace/src/pages/Settings.tsx`, tests — integrate the two AutoDL panels.

## Task 1: Add a macOS Keychain credential store

**Files:** `platform/keychain/store.go`, `store_darwin.go`, `store_darwin_test.go`

- [ ] Write command-runner tests proving secrets are sent on stdin, never command arguments, and reads return the decoded payload.
- [ ] Define the boundary:

```go
type Store interface {
	Set(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}

type Credential struct {
	Kind       string `json:"kind"`
	Secret     string `json:"secret"`
	Passphrase string `json:"passphrase,omitempty"`
}
```

- [ ] Use service `app.medialink.autodl`. Encode the JSON payload with base64 before invoking `/usr/bin/security add-generic-password -U -a <reference> -s app.medialink.autodl -w` with `-w` last so `security` reads the value from stdin; decode the `find-generic-password -w` result.
- [ ] Support credential kinds `password` and `privateKey`. For a private key, use `Passphrase` only with `ssh.ParsePrivateKeyWithPassphrase`; never write key material to disk.
- [ ] Normalize Keychain item-not-found to a typed `ErrNotFound`; redact command stderr before wrapping errors.
- [ ] Run `go test ./internal/platform/keychain -count=1`; expect PASS.
- [ ] Commit: `feat(security): store AutoDL credentials in Keychain`.

## Task 2: Implement fingerprint-pinned SSH local forwarding

**Files:** `platform/sshtunnel/manager.go`, `manager_test.go`, `services/server/go.mod`

- [ ] Write tests with injected dial/listen functions for password auth, encrypted private-key auth, fingerprint match, fingerprint mismatch, loopback bind, reconnect, and shutdown.
- [ ] Define stable inputs and states:

```go
type Config struct {
	Host            string
	Port            int
	User            string
	RemotePort      int
	CredentialRef   string
	HostFingerprint string
}

type State struct {
	Status    string
	LocalPort int
	ErrorCode string
}
```

- [ ] Load the secret from Keychain, build `ssh.ClientConfig`, and use a `HostKeyCallback` that computes `ssh.FingerprintSHA256(key)` and compares it with `HostFingerprint` using constant-time byte comparison.
- [ ] Bind `net.Listen("tcp", "127.0.0.1:0")`; for each accepted connection dial remote `127.0.0.1:<RemotePort>` through the SSH client and bridge with two `io.Copy` operations.
- [ ] Keep one managed tunnel per application. Reconnect with bounded exponential backoff while the app is running; expose `disconnected`, `connecting`, `ready`, `reconnecting`, and `host_key_mismatch` states.
- [ ] Implement a separate read-only host scan helper using `ssh.Dial` with a callback that captures the presented fingerprint and aborts before authentication. The UI must confirm this value before save.
- [ ] Promote `golang.org/x/crypto v0.50.0` from indirect to direct in `go.mod`.
- [ ] Run `go test -race ./internal/platform/sshtunnel -count=1`; expect PASS.
- [ ] Commit: `feat(network): add secure AutoDL SSH tunnel`.

## Task 3: Implement the loopback ComfyUI client

**Files:** `platform/comfyui/client.go`, `client_http.go`, `client_http_test.go`

- [ ] Use `httptest.Server` to test health, object info, queue, image upload, prompt submission, history lookup, and output download. Reject any configured host other than `127.0.0.1`.
- [ ] Define the client boundary:

```go
type Client interface {
	Health(context.Context) (Health, error)
	ObjectInfo(context.Context) (map[string]ObjectDefinition, error)
	UploadImage(context.Context, string, string) (UploadedImage, error)
	Submit(context.Context, map[string]any, string) (PromptSubmission, error)
	History(context.Context, string) (HistoryEntry, bool, error)
	Queue(context.Context) (Queue, error)
	Download(context.Context, OutputFile) (io.ReadCloser, error)
}
```

- [ ] Implement endpoints `/system_stats`, `/object_info`, `/upload/image`, `/prompt`, `/history/{prompt_id}`, `/queue`, and `/view`. Use URL escaping, response-size limits, request timeouts, and typed non-2xx errors.
- [ ] Add `github.com/coder/websocket` and implement `/ws?clientId=<client-id>` progress events behind an injected monitor interface. HTTP history/queue polling remains the required fallback and source of terminal truth when WebSocket setup or reads fail.
- [ ] Require `POST /prompt` response to contain a non-empty `prompt_id`; return validation errors from `node_errors` without manufacturing an ID.
- [ ] Validate downloaded content by size, content type, and `ffprobe` in the provider before import.
- [ ] Run `go test ./internal/platform/comfyui -count=1`; expect PASS.
- [ ] Commit: `feat(comfyui): add tunneled API client`.

## Task 4: Validate semantic H3 workflow profiles

**Files:** `service/generation/h3_workflow_profile.go`, `_test.go`

- [ ] Write fixtures in test code for one valid REF2VA workflow, one valid FL2VA workflow, UI-format JSON, missing node IDs, wrong input names, unsupported profile kind, and missing required model.
- [ ] Use these concrete profile types:

```go
type H3ProfileKind string

const (
	H3ProfileREF2VA H3ProfileKind = "ref2va"
	H3ProfileFL2VA  H3ProfileKind = "fl2va"
)

type H3NodeBinding struct {
	NodeID string `json:"nodeId"`
	Input  string `json:"input"`
}

type H3WorkflowManifest struct {
	Prompt    H3NodeBinding   `json:"prompt"`
	Duration  H3NodeBinding   `json:"duration"`
	Width     H3NodeBinding   `json:"width"`
	Height    H3NodeBinding   `json:"height"`
	Seed      H3NodeBinding   `json:"seed"`
	Output    H3NodeBinding   `json:"output"`
	References []H3NodeBinding `json:"references"`
}
```

- [ ] A profile contains `id`, `name`, `kind`, `version`, `workflow`, `manifest`, `requiredNodes`, and `requiredModels`. Validate IDs and every bound input against the API workflow map shaped as `<node-id> -> {class_type, inputs}`.
- [ ] Reject any workflow with top-level `nodes` or `links`; reject any kind outside the two constants; require one or more reference bindings for REF2VA and exactly first/last image bindings for FL2VA.
- [ ] Cross-check `requiredNodes` against `/object_info` class names and `requiredModels` against the object-info input choices. Return all validation issues in a deterministic array.
- [ ] Instantiate by deep-copying workflow JSON and setting only manifest-bound fields. Never hard-code node IDs in the provider.
- [ ] Run `go test ./internal/service/generation -run TestH3WorkflowProfile -count=1`; expect PASS.
- [ ] Commit: `feat(h3): validate semantic workflow profiles`.

## Task 5: Persist non-secret AutoDL settings and profiles

**Files:** `service/settings/autodl.go`, `_test.go`, `store.go`

- [ ] Add tests proving the app setting contains only host/port/user/remote port/fingerprint/credential reference/profiles, while the secret exists only in the fake Keychain.
- [ ] Define public settings:

```go
type AutoDLSettings struct {
	Host            string      `json:"host"`
	Port            int         `json:"port"`
	User            string      `json:"user"`
	RemotePort      int         `json:"remotePort"`
	HostFingerprint string      `json:"hostFingerprint"`
	CredentialRef   string      `json:"credentialRef,omitempty"`
	CredentialSet   bool        `json:"credentialSet"`
	Profiles        []H3Profile `json:"profiles"`
}
```

- [ ] Save non-secret JSON under `medialink.autodl`; generate credential references with `crypto/rand`; write the secret to Keychain before committing the reference to app settings.
- [ ] On credential replacement, save the new Keychain item, update settings, then delete the old item. If settings persistence fails, delete the newly created item to avoid an orphan.
- [ ] Validate host, port `1..65535`, user, confirmed fingerprint, remote port default `6006`, unique profile IDs, and exact profile kinds.
- [ ] Add an explicit credential-clear method; it deletes only the exact referenced Keychain item and leaves the rest of AutoDL settings intact.
- [ ] Run `go test ./internal/service/settings -run TestAutoDL -count=1`; expect PASS.
- [ ] Commit: `feat(settings): persist AutoDL H3 configuration`.

## Task 6: Expose settings, host confirmation, profile validation, and connection test APIs

**Files:** `handlers/autodl_settings.go`, `_test.go`, `routes/routes.go`, OpenAPI annotations

- [ ] Add handler tests for malformed settings, unconfirmed fingerprint, Keychain failure redaction, successful scan, fingerprint mismatch, invalid workflow, and healthy tunneled ComfyUI.
- [ ] Register these endpoints:

```text
GET    /api/v1/settings/autodl
PUT    /api/v1/settings/autodl
DELETE /api/v1/settings/autodl/credential
POST   /api/v1/settings/autodl/host-key/scan
POST   /api/v1/settings/autodl/test
POST   /api/v1/settings/autodl/profiles/validate
```

- [ ] `host-key/scan` returns the presented SHA256 fingerprint only. `PUT` rejects a fingerprint unless the request includes `confirmHostFingerprint` with the exact same value.
- [ ] The connection test runs tunnel ensure, `/system_stats`, `/object_info`, then validates both saved profiles; return stable status/reason codes and no secret-bearing errors.
- [ ] The server never accepts `comfyuiBaseURL` or a public ComfyUI host in the request DTO.
- [ ] Run `go test ./internal/http/handlers -run TestAutoDL -count=1`; expect PASS.
- [ ] Commit: `feat(api): expose AutoDL H3 readiness checks`.

## Task 7: Implement upload, submit, monitor, and download

**Files:** `service/generation/autodl_h3_provider.go`, `_test.go`, `generation_medialink_catalog.go`

- [ ] Build a fake tunnel and fake ComfyUI test for REF2VA success and FL2VA success. Assert calls occur in this order: ensure tunnel, health/object validation, upload ordered references, instantiate workflow, submit once, checkpoint prompt ID, history/queue checks, download, validate.
- [ ] Define a provider with injectable boundaries:

```go
type AutoDLH3Provider struct {
	tunnel   H3Tunnel
	clients  H3ClientFactory
	profiles H3ProfileStore
	probe    MediaProbe
}
```

- [ ] Map request `profileKind` to one enabled saved profile. Enforce duration `4..15`; map aspect ratio/resolution to explicit width/height; map references by ordered manifest bindings.
- [ ] Upload reference images to a task-specific ComfyUI subfolder. Treat returned `name`, `subfolder`, and `type` as the only valid image input values.
- [ ] Submit once. Immediately emit progress with status `submitted`, response ID `autodl.h3:<prompt_id>`, `RuntimeState.ComfyPromptID`, and `SubmittedAt` before any WebSocket or polling work.
- [ ] Monitor WebSocket when available, but reconcile terminal state through `/history/{prompt_id}`. Use `/queue` to distinguish queued/running from missing history.
- [ ] On tunnel loss, return `waiting_reconnect` with the same response ID. `Get` reconnects and calls history/queue for that prompt ID; it never calls submit.
- [ ] Download only outputs recorded under that history entry. Validate with `ffprobe`: readable video stream, duration greater than zero, and expected container MIME. Return the file to the existing video asset cache.
- [ ] Supply this provider as the `autodlH3` member of the foundation plan's `mediaLinkRouteProviders` value and supply tunnel/profile preflight through its readiness function.
- [ ] Run `go test ./internal/service/generation -run TestAutoDLH3Provider -count=1`; expect PASS.
- [ ] Commit: `feat(generation): run H3 through AutoDL ComfyUI`.

## Task 8: Prove prompt-ID durability and no duplicate submission

**Files:** `generation_runtime_tasks.go`, `generation_runtime_test.go`, `generation_tasks_service.go`

- [ ] Write a crash-window test where submit returns `prompt-123`, the progress checkpoint persists, and monitoring disconnects. Assert the stored provider task ID is `autodl.h3:prompt-123` and runtime state contains `prompt-123`.
- [ ] Reconstruct the service and provider, call pending-task recovery, return completed history for `prompt-123`, and assert fake submit count remains exactly one across both service instances.
- [ ] If a task is `submitting` but has no persisted prompt ID after a process crash, mark it failed with `submission_outcome_unknown`; do not guess whether ComfyUI accepted it and do not resubmit automatically.
- [ ] Add `waiting_reconnect` and `importing` to active task statuses. Cancellation after submission may call ComfyUI interrupt only when the API can target the same prompt safely; otherwise mark local cancellation and disclose that remote computation may continue.
- [ ] Run `go test ./internal/service/generation -run 'Test.*(PromptID|NoDuplicate|SubmissionOutcomeUnknown)' -count=1`; expect PASS.
- [ ] Commit: `fix(tasks): prevent duplicate H3 submissions`.

## Task 9: Build the AutoDL and workflow profile settings UI

**Files:** `settings/api/settings.ts`, `AutoDLH3Panel.tsx`, `_test.tsx`, `H3WorkflowProfilesPanel.tsx`, `_test.tsx`, `Settings.tsx`, `_test.tsx`

- [ ] Add API types matching the non-secret server DTOs. Credential mutations may send a secret but responses and SWR cache must never contain it.
- [ ] Build `AutoDLH3Panel` with host, SSH port, user, remote port fixed/defaulted to 6006, credential kind, secret entry, host-key scan/confirm, save, clear credential, and connection test.
- [ ] Build `H3WorkflowProfilesPanel` with two slots only: REF2VA and FL2VA. Import pasted/file JSON, edit semantic bindings, validate against the live instance, and show every validation issue.
- [ ] Reject UI-format workflow JSON client-side with the same message as the server; server validation remains authoritative.
- [ ] Show connection states `未配置`, `连接中`, `已就绪`, `重连中`, `主机指纹不匹配`, and `工作流不可用` using existing semantic token classes.
- [ ] Add tests for secret non-retention, host fingerprint confirmation, both profile slots, UI-format rejection, live validation errors, and successful connection test.
- [ ] Run `pnpm test -- --run src/domains/settings/components/AutoDLH3Panel.test.tsx src/domains/settings/components/H3WorkflowProfilesPanel.test.tsx src/pages/Settings.test.tsx` from `apps/workspace`; expect PASS.
- [ ] Commit: `feat(settings): configure AutoDL H3 workflows`.

## Plan Acceptance

- [ ] Unit tests demonstrate passwords/private keys/passphrases never appear in app settings, logs, command arguments, or task JSON.
- [ ] The tunnel listens only on `127.0.0.1` and rejects a changed host fingerprint.
- [ ] Both REF2VA and FL2VA API workflows validate and instantiate through manifests without source-code node IDs.
- [ ] Restart recovery uses the original ComfyUI `prompt_id`; fake submit count is exactly one.
- [ ] The only visible video route is `AutoDL · MiniMax H3`.
- [ ] No real SSH connection, AutoDL instance mutation, or GPU generation occurs during automated verification.
