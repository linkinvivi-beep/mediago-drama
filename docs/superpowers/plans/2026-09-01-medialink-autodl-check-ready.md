# MediaLink AutoDL Check-to-Ready Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make “检查连接” idempotently connect, start the configured remote ComfyUI/API when needed, wait for health, establish the tunnel, and report a directly usable instance.

**Architecture:** Extend the existing instance profile with a trusted startup command and optional local port. Reuse the tunnel manager's pinned SSH connection to execute the saved command once, orchestrate readiness in `AutoDLWorkflowAdmin`, and expose concrete stages/errors to the current settings panel without adding a second connection system.

**Tech Stack:** Go, `golang.org/x/crypto/ssh`, Gin, React/TypeScript, TanStack Query, Vitest.

## Global Constraints

- AutoDL power-on/off and billing remain manual.
- Startup commands are per-instance administrator configuration, never derived from prompts or workflow JSON.
- Passwords remain in macOS Keychain and never appear in API responses or logs.
- Local ports default to automatic allocation; advanced manual ports must fail clearly on conflict.
- Do not stop remote ComfyUI or ComfyUIPhotoSync when MediaLink exits.
- Keep H3 and image workflows route-neutral and avoid unrelated refactoring.

---

### Task 1: Persist startup and local-port configuration

**Files:**
- Modify: `services/server/internal/service/settings/autodl_instances.go`
- Modify: `services/server/internal/service/settings/autodl_instances_test.go`
- Modify: `apps/workspace/src/domains/settings/api/autodl.ts`

**Interfaces:**
- Produces: `AutoDLInstanceProfile.StartupCommand string`
- Produces: `AutoDLInstanceProfile.LocalPort int`
- Produces: matching `startupCommand?: string` and `localPort?: number` TypeScript fields

- [ ] **Step 1: Write failing settings migration and validation tests**

Cover v2 documents migrating with empty command/automatic port, a command over 4096 bytes being rejected, and a local port outside 1–65535 being rejected.

- [ ] **Step 2: Run the focused tests and confirm failure**

Run: `go test ./internal/service/settings -run 'TestAutoDL.*(Startup|LocalPort|Migrat)' -count=1`

Expected: FAIL because fields and validation do not exist.

- [ ] **Step 3: Add the schema fields and validation**

Use:

```go
StartupCommand string `json:"startupCommand,omitempty"`
LocalPort     int    `json:"localPort,omitempty"`
```

Normalize whitespace, allow an empty command, reject command length over 4096 bytes, and accept `LocalPort == 0` as automatic. Increment `autoDLSettingsVersion` from 2 to 3 while preserving v2 data.

- [ ] **Step 4: Update TypeScript API contracts and run tests**

Run: `go test ./internal/service/settings -count=1`

Run: `pnpm --filter @mediago/workspace test -- autodl`

Expected: PASS.

- [ ] **Step 5: Commit the configuration schema**

```bash
git add services/server/internal/service/settings/autodl_instances.go services/server/internal/service/settings/autodl_instances_test.go apps/workspace/src/domains/settings/api/autodl.ts
git commit -m "feat(autodl): store remote startup settings"
```

### Task 2: Execute one saved startup command over the pinned SSH client

**Files:**
- Modify: `services/server/internal/platform/autodl/tunnel_manager.go`
- Modify: `services/server/internal/platform/autodl/tunnel_manager_test.go`

**Interfaces:**
- Produces: `TunnelManager.Run(context.Context, TunnelTarget, string) error`
- Produces: `TunnelTarget.LocalPort int`, where `0` requests automatic allocation
- Consumes: existing pinned fingerprint, Keychain password source, and managed SSH client

- [ ] **Step 1: Add failing command execution tests**

Extend the fake SSH server to record exec requests. Test successful execution, non-zero exit, context cancellation, empty command rejection, automatic local-port allocation, exact manual-port binding, occupied manual-port failure, and that the password/command output is absent from returned errors.

- [ ] **Step 2: Run focused platform tests and confirm failure**

Run: `go test ./internal/platform/autodl -run 'TestTunnelManagerRun' -count=1`

Expected: build failure because `Run` is missing.

- [ ] **Step 3: Implement command execution on the matching managed tunnel**

Add `Run` to the interface. It must call `Ensure`, retrieve the same active target revision, create `ssh.NewSession()`, discard stdout/stderr, call `session.Start(command)`, and wait with context cancellation. Return stable wrapped errors such as `ErrRemoteCommandFailed`; never include command text or output. Include `LocalPort` in `TunnelTarget` and its target key; bind `127.0.0.1:0` when it is zero and the exact loopback port otherwise.

- [ ] **Step 4: Run tunnel tests including race detection**

Run: `go test -race ./internal/platform/autodl -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the SSH execution primitive**

```bash
git add services/server/internal/platform/autodl/tunnel_manager.go services/server/internal/platform/autodl/tunnel_manager_test.go
git commit -m "feat(autodl): run trusted startup commands"
```

### Task 3: Orchestrate idempotent instance readiness

**Files:**
- Modify: `services/server/internal/service/generation/autodl_workflow_admin.go`
- Modify: `services/server/internal/service/generation/autodl_workflow_admin_test.go`

**Interfaces:**
- Produces: `EnsureInstanceReady(context.Context, string) (AutoDLInstanceCheck, error)`
- Produces: readiness stages `connecting`, `probing`, `starting`, `waiting_health`, `tunneling`, `validating_api`, `ready`, `failed`
- Consumes: `TunnelManager.Run`

- [ ] **Step 1: Write failing orchestration tests**

Test: healthy service skips startup; unavailable service runs the command exactly once then becomes healthy; empty command reports `startup_command_missing`; command failure reports `startup_failed`; polling timeout reports `health_timeout`; two concurrent calls for the same instance share one operation; different instances run independently.

- [ ] **Step 2: Run focused service tests and confirm failure**

Run: `go test ./internal/service/generation -run 'TestAutoDL.*EnsureReady' -count=1`

Expected: FAIL because the readiness orchestration does not exist.

- [ ] **Step 3: Implement condition-based readiness**

Use a per-instance in-flight map. The owner performs `connect`, `SystemStats`, optional `Run`, then polls `SystemStats` with a 500 ms timer until a 90-second context deadline. Waiters receive the same final result. Do not run the startup command more than once per operation and do not kill the remote service on timeout.

- [ ] **Step 4: Make existing checks and generation readiness use the same path**

Have `CheckInstance` call `EnsureInstanceReady`. Update the scheduler readiness callback to invoke it before workflow digest validation. Do not submit `/prompt` during readiness.

- [ ] **Step 5: Run service tests**

Run: `go test ./internal/service/generation -count=1`

Expected: PASS.

- [ ] **Step 6: Commit readiness orchestration**

```bash
git add services/server/internal/service/generation/autodl_workflow_admin.go services/server/internal/service/generation/autodl_workflow_admin_test.go
git commit -m "feat(autodl): ensure ComfyUI is ready on check"
```

### Task 4: Expose actionable readiness status over HTTP

**Files:**
- Modify: `services/server/internal/http/handlers/autodl_settings.go`
- Modify: `services/server/internal/http/handlers/autodl_settings_test.go`
- Modify: `services/server/internal/http/routes/routes.go`
- Modify: `apps/workspace/src/domains/settings/api/autodl.ts`

**Interfaces:**
- Produces: `GET /api/v1/settings/autodl/instances/:instanceId/readiness`
- Produces: `{connected, stage, reason, localPort, comfyuiVersion, devices}`

- [ ] **Step 1: Write failing handler tests for known errors and status**

Assert missing password is 422 with the current explicit Chinese message; host-key mismatch, startup failure, health timeout, and manual port conflict return stable non-`internal error` messages; readiness GET returns the latest redacted stage.

- [ ] **Step 2: Run handler tests and confirm failure**

Run: `go test ./internal/http/handlers -run 'TestAutoDL.*(Readiness|Startup|Health)' -count=1`

Expected: FAIL because stage/status routing is absent.

- [ ] **Step 3: Add the redacted status endpoint and error mapping**

Extend `AutoDLInstanceCheck` with `Stage string`. Add explicit mappings for `ErrRemoteCommandFailed`, startup-command-missing, health-timeout, and `EADDRINUSE`. Responses may include stage/reason but never startup command, tunnel origin, password, or SSH output.

- [ ] **Step 4: Run handler and API integration tests**

Run: `go test ./internal/http/handlers ./internal/app -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the HTTP contract**

```bash
git add services/server/internal/http/handlers/autodl_settings.go services/server/internal/http/handlers/autodl_settings_test.go services/server/internal/http/routes/routes.go apps/workspace/src/domains/settings/api/autodl.ts
git commit -m "feat(autodl): expose readiness stages"
```

### Task 5: Add startup controls and live stages to the settings panel

**Files:**
- Modify: `apps/workspace/src/domains/settings/components/AutoDLSettingsPanel.tsx`
- Modify: `apps/workspace/src/domains/settings/components/AutoDLSettingsPanel.test.tsx`

**Interfaces:**
- Consumes: `startupCommand`, `localPort`, and `AutoDLInstanceCheck.stage`
- Produces: advanced instance fields and stage-specific UI copy

- [ ] **Step 1: Write failing UI tests**

Test that editing an instance preserves a saved startup command, automatic local port is represented by `0`, checking displays the returned stage, success says “可以生成”, and known server errors display their concrete description instead of `internal error`.

- [ ] **Step 2: Run the focused UI tests and confirm failure**

Run: `pnpm --filter @mediago/workspace test -- AutoDLSettingsPanel`

Expected: FAIL because fields/stages are not rendered.

- [ ] **Step 3: Implement the focused UI changes**

Add an “高级设置” disclosure containing a multiline remote startup command and optional local port. Keep the existing main card compact. While check is active, poll readiness once per second; map stages to `连接 SSH / 检查远程服务 / 启动服务 / 等待健康 / 建立隧道 / 验证 API / 可以生成`.

- [ ] **Step 4: Run UI tests, typecheck, and format**

Run: `pnpm --filter @mediago/workspace test -- AutoDLSettingsPanel`

Run: `pnpm --filter @mediago/workspace typecheck`

Run: `pnpm biome check apps/workspace/src/domains/settings`

Expected: PASS with no formatting changes pending.

- [ ] **Step 5: Commit the UI**

```bash
git add apps/workspace/src/domains/settings/components/AutoDLSettingsPanel.tsx apps/workspace/src/domains/settings/components/AutoDLSettingsPanel.test.tsx
git commit -m "feat(settings): show AutoDL startup readiness"
```

### Task 6: Full verification and macOS package gate

**Files:**
- Modify only when a failing regression proves a focused correction is required.

**Interfaces:**
- Consumes: all prior task interfaces
- Produces: verified arm64 MediaLink build

- [ ] **Step 1: Run all server tests and vet**

Run: `go test ./...`

Run: `go vet ./...`

Expected: PASS.

- [ ] **Step 2: Run workspace tests and typecheck**

Run: `pnpm --filter @mediago/workspace test`

Run: `pnpm --filter @mediago/workspace typecheck`

Expected: PASS.

- [ ] **Step 3: Build the macOS arm64 application using the repository release task**

Run from the repository root:

```bash
node scripts/build-server-target.mjs darwin-arm64
pnpm -C apps/workspace electron:build:darwin-arm64
```

Expected: `apps/workspace/release/MediaLink-0.1.0-beta.0-macos-arm64.dmg` and `.zip` are produced, and the staged server plus both MCP sidecars byte-match the newly built arm64 binaries.

- [ ] **Step 4: Perform one non-generating live readiness check**

With a user-selected powered-on instance, save its verified startup script, click “检查连接”, and confirm the final UI shows ComfyUI version, GPU, dynamic/manual local port, and “可以生成”. Do not submit `/prompt`.

- [ ] **Step 5: Commit only a focused correction if verification exposed one**

If verification did not change tracked files, do not create an empty commit.
