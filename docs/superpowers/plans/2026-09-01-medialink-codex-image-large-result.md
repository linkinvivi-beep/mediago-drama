# MediaLink Codex Large Image Result Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `codex.imagegen` reliably consume large structured image events and keep capability checks responsive during generation.

**Architecture:** Keep the existing JSON-RPC and structured `imageGeneration` protocol. Replace the scanner's implicit 64 KiB token limit with an explicit bounded scanner, and run read-only capability probes through a transient app-server session so they do not wait behind the singleton generation session.

**Tech Stack:** Go, `bufio.Scanner`, Codex app-server JSON-RPC, Go tests.

## Global Constraints

- Continue using local Codex ChatGPT login and built-in `$imagegen`; do not add OpenAI Images API or API keys.
- Never log image base64, ChatGPT credentials, or complete app-server payloads.
- Never retry a submitted image turn; recovery uses the persisted thread ID.
- Keep changes limited to Codex app-server transport/provider code and tests.

---

### Task 1: Bound and enlarge app-server JSON-RPC messages

**Files:**
- Modify: `services/server/internal/platform/codexapp/session.go`
- Modify: `services/server/internal/platform/codexapp/session_test.go`

**Interfaces:**
- Produces: `newMessageScanner(io.Reader, int) *bufio.Scanner`
- Produces: `maxAppServerMessageBytes = 96 << 20`

- [ ] **Step 1: Write failing scanner tests**

Add tests that feed one valid JSON-RPC line larger than 64 KiB and a second line larger than a small injected limit:

```go
func TestMessageScannerAcceptsLargeBoundedJSONLine(t *testing.T) {
    payload := strings.Repeat("x", 128<<10)
    scanner := newMessageScanner(strings.NewReader(`{"method":"item/completed","params":"`+payload+`"}`+"\n"), 256<<10)
    if !scanner.Scan() { t.Fatalf("Scan() = false, error = %v", scanner.Err()) }
}

func TestMessageScannerRejectsLineOverExplicitLimit(t *testing.T) {
    scanner := newMessageScanner(strings.NewReader(strings.Repeat("x", 4097)), 4096)
    scanned := scanner.Scan()
    if scanned || scanner.Err() == nil || !strings.Contains(scanner.Err().Error(), "token too long") {
        t.Fatalf("Scan() = %v, error = %v", scanned, scanner.Err())
    }
}
```

- [ ] **Step 2: Run the focused tests and confirm failure**

Run: `go test ./internal/platform/codexapp -run 'TestMessageScanner' -count=1`

Expected: build failure because `newMessageScanner` does not exist.

- [ ] **Step 3: Implement the bounded scanner**

Add:

```go
const maxAppServerMessageBytes = 96 << 20

func newMessageScanner(reader io.Reader, maximum int) *bufio.Scanner {
    scanner := bufio.NewScanner(reader)
    scanner.Buffer(make([]byte, 64<<10), maximum)
    return scanner
}
```

Change `StartWithInitContext` to call `go session.readLoop(newMessageScanner(stdout, maxAppServerMessageBytes))`.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/platform/codexapp -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the transport fix**

```bash
git add services/server/internal/platform/codexapp/session.go services/server/internal/platform/codexapp/session_test.go
git commit -m "fix(codex): accept bounded large image events"
```

### Task 2: Decouple read-only preflight from image generation

**Files:**
- Modify: `services/server/internal/service/generation/codex_image_provider.go`
- Modify: `services/server/internal/service/generation/codex_image_provider_test.go`

**Interfaces:**
- Consumes: existing `factory(context.Context, context.Context, string) (codexapp.Client, error)`
- Produces: `probeCapabilities(context.Context) (codexapp.ModelProviderCapabilities, error)` on `managedCodexImageSession`

- [ ] **Step 1: Write a failing concurrency test**

Create a factory whose first client blocks in `GenerateImage` while a second transient client immediately returns `ImageGeneration: true`. Assert `provider.Ready` completes before the generation block is released and that the transient client is closed.

```go
select {
case result := <-readyResult:
    if !result.ready || result.reason != "" { t.Fatalf("Ready() = %#v", result) }
case <-time.After(time.Second):
    t.Fatal("Ready() waited behind active generation")
}
```

- [ ] **Step 2: Run the focused test and confirm timeout/failure**

Run: `go test ./internal/service/generation -run TestManagedCodexImagePreflightDoesNotWaitBehindGeneration -count=1`

Expected: FAIL because `Capabilities` uses the singleton session FIFO.

- [ ] **Step 3: Implement transient capability probing**

Change only `managedCodexImageSession.Capabilities`:

```go
func (session *managedCodexImageSession) Capabilities(ctx context.Context) (codexapp.ModelProviderCapabilities, error) {
    ctx, cancel := session.operationContext(ctx)
    defer cancel()
    client, err := session.factory(session.parent, ctx, session.binPath)
    if err != nil { return codexapp.ModelProviderCapabilities{}, err }
    defer client.Close()
    return codexapp.ReadModelProviderCapabilities(ctx, client)
}
```

Do not invalidate or replace the active generation client from this method.

- [ ] **Step 4: Run provider and platform tests**

Run: `go test ./internal/service/generation ./internal/platform/codexapp -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the preflight fix**

```bash
git add services/server/internal/service/generation/codex_image_provider.go services/server/internal/service/generation/codex_image_provider_test.go
git commit -m "fix(codex): isolate image capability probes"
```

### Task 3: Verify recovery and regression gates

**Files:**
- Modify only if a regression is exposed: `services/server/internal/service/generation/codex_image_provider_test.go`

**Interfaces:**
- Consumes: persisted `codex.imagegen:<threadID>` response identity
- Produces: verified large-event and reconnect behavior

- [ ] **Step 1: Run the full server test suite**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 2: Run static analysis**

Run: `go vet ./...`

Expected: no diagnostics.

- [ ] **Step 3: Verify the existing generated PNG without submitting a new turn**

Run:

```bash
file "/Users/jialiankun/.codex/generated_images/01a05aa2-8bc6-78e3-9be7-9e734c36f465/exec-76e82e21-5a98-4e9b-8991-ed3c34aa315e.png"
shasum -a 256 "/Users/jialiankun/.codex/generated_images/01a05aa2-8bc6-78e3-9be7-9e734c36f465/exec-76e82e21-5a98-4e9b-8991-ed3c34aa315e.png"
```

Expected: PNG 1672×941 and SHA-256 `7b9765bfcb54a8c190e00dcf0dccd0c0b37b0a427ce774362059ca07b33932d2`.

- [ ] **Step 4: Commit any test-only correction exposed by the full suite**

If no file changed, do not create an empty commit. If a focused regression test was required:

```bash
git add services/server/internal/service/generation/codex_image_provider_test.go
git commit -m "test(codex): cover image reconnect regression"
```
