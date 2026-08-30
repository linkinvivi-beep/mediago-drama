package generation

import (
	"context"
	"encoding/base64"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/codexapp"
	"github.com/mediago-dev/mediago-drama/services/server/internal/service/media"
)

type quotaSafeCodexProvider struct {
	mu               sync.Mutex
	generate         int
	get              int
	started          chan coregeneration.Request
	generateRelease  chan struct{}
	generateResponse coregeneration.Response
	getResponse      coregeneration.Response
}

func (*quotaSafeCodexProvider) Name() string { return "quota-safe-codex" }
func (provider *quotaSafeCodexProvider) Generate(ctx context.Context, request coregeneration.Request) (coregeneration.Response, error) {
	provider.mu.Lock()
	provider.generate++
	provider.mu.Unlock()
	if provider.started != nil {
		provider.started <- request
	}
	if provider.generateRelease != nil {
		<-provider.generateRelease
		if err := ctx.Err(); err != nil {
			return coregeneration.Response{}, err
		}
	}
	if provider.generateResponse.Status != "" {
		return provider.generateResponse, nil
	}
	<-ctx.Done()
	return coregeneration.Response{}, ctx.Err()
}
func (provider *quotaSafeCodexProvider) Get(_ context.Context, id string) (coregeneration.Response, error) {
	provider.mu.Lock()
	provider.get++
	provider.mu.Unlock()
	if provider.getResponse.Status != "" {
		return provider.getResponse, nil
	}
	return coregeneration.Response{ID: id, Status: "waiting_reconnect", Metadata: map[string]any{"runtime_state": GenerationTaskRuntimeState{CodexThreadID: "thread-existing"}}}, nil
}

func (provider *quotaSafeCodexProvider) counts() (int, int) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.generate, provider.get
}

func TestGenerationServiceDeleteCancelsQueuedCodexTaskBeforeImageTurn(t *testing.T) {
	store := NewGenerationTaskService(filepath.Join(t.TempDir(), "settings.db"), nil)
	workflow := NewGenerationService(nil, store, nil)
	firstStarted := make(chan struct{})
	session := &codexImageSessionStub{
		capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: true},
		generate: func(ctx context.Context, _ codexapp.ImageGenerationRequest, _ func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
			select {
			case <-firstStarted:
			default:
				close(firstStarted)
			}
			<-ctx.Done()
			return codexapp.ImageGenerationResult{}, ctx.Err()
		},
	}
	provider := NewCodexImageProvider(session, t.TempDir())
	first := testCodexGenerationTask("task-first", "submitted")
	second := testCodexGenerationTask("task-second", "submitted")
	for _, task := range []GenerationTaskRecord{first, second} {
		if err := store.Upsert(task); err != nil {
			t.Fatal(err)
		}
	}
	workflow.launchSubmittedGeneration(first, provider, codexImageRequest(first.ID), "create", "", "")
	<-firstStarted
	workflow.launchSubmittedGeneration(second, provider, codexImageRequest(second.ID), "create", "", "")
	waitForCodexImageQueueDepth(t, provider.queue, 1)
	if _, deleted, err := workflow.DeleteGenerationTask(second.ID); err != nil || !deleted {
		t.Fatalf("DeleteGenerationTask() = deleted %v, err %v", deleted, err)
	}
	waitForGenerationCancelRegistry(t, workflow, second.ID, false)
	session.mu.Lock()
	requests := len(session.requests)
	session.mu.Unlock()
	if requests != 1 {
		t.Fatalf("GenerateImage calls = %d, want 1", requests)
	}
	if _, deleted, err := workflow.DeleteGenerationTask(first.ID); err != nil || !deleted {
		t.Fatalf("cleanup delete = %v, %v", deleted, err)
	}
	waitForGenerationCancelRegistry(t, workflow, first.ID, false)
}

func TestRetryCodexDisconnectedTaskUsesGetWithoutNewTurn(t *testing.T) {
	store := NewGenerationTaskService(filepath.Join(t.TempDir(), "settings.db"), nil)
	task := testCodexGenerationTask("task-retry", "waiting_reconnect")
	task.ProviderTaskID = codexImageResponseIDPrefix + "thread-existing"
	task.RuntimeState.CodexThreadID = "thread-existing"
	if err := store.Upsert(task); err != nil {
		t.Fatal(err)
	}
	provider := &quotaSafeCodexProvider{}
	workflow := NewGenerationService(nil, store, nil)
	workflow.SetMediaLinkProviders(provider, &mediaLinkTestProvider{name: "h3"}, func(context.Context, string) (bool, string) { return true, "" })
	response, status, err := workflow.RetryGenerationTask(context.Background(), task.ID)
	if err != nil || status != http.StatusOK {
		t.Fatalf("RetryGenerationTask() = %#v, %d, %v", response, status, err)
	}
	provider.mu.Lock()
	generate, get := provider.generate, provider.get
	provider.mu.Unlock()
	if generate != 0 || get != 1 {
		t.Fatalf("provider calls Generate/Get = %d/%d, want 0/1", generate, get)
	}
	runtimeOnly := testCodexGenerationTask("task-runtime-only", "waiting_reconnect")
	runtimeOnly.RuntimeState.CodexThreadID = "thread-runtime-only"
	if err := store.Upsert(runtimeOnly); err != nil {
		t.Fatal(err)
	}
	if _, status, err := workflow.RetryGenerationTask(context.Background(), runtimeOnly.ID); err != nil || status != http.StatusOK {
		t.Fatalf("runtime-only RetryGenerationTask() status/error = %d/%v", status, err)
	}
	provider.mu.Lock()
	generate, get = provider.generate, provider.get
	provider.mu.Unlock()
	if generate != 0 || get != 2 {
		t.Fatalf("provider calls after runtime-only recovery = %d/%d, want 0/2", generate, get)
	}
}

func TestRetryTerminalCodexFailureClearsRecoveryStateBeforeNewTurn(t *testing.T) {
	store := NewGenerationTaskService(filepath.Join(t.TempDir(), "settings.db"), nil)
	task := testCodexGenerationTask("task-terminal", "failed")
	task.ProviderTaskID = codexImageResponseIDPrefix + "stale-thread"
	task.RuntimeState = GenerationTaskRuntimeState{CodexThreadID: "stale-thread", SavedPath: "/stale/path"}
	if err := store.Upsert(task); err != nil {
		t.Fatal(err)
	}
	provider := &quotaSafeCodexProvider{started: make(chan coregeneration.Request, 1)}
	workflow := NewGenerationService(nil, store, nil)
	workflow.SetMediaLinkProviders(provider, &mediaLinkTestProvider{name: "h3"}, func(context.Context, string) (bool, string) { return true, "" })
	_, status, err := workflow.RetryGenerationTask(context.Background(), task.ID)
	if err != nil || status != http.StatusOK {
		t.Fatalf("RetryGenerationTask() status/error = %d/%v", status, err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("new terminal-failure turn did not start")
	}
	stored, ok, err := store.Get(task.ID)
	if err != nil || !ok {
		t.Fatalf("Get() = %v/%v", ok, err)
	}
	if stored.ProviderTaskID != "" || stored.RuntimeState != (GenerationTaskRuntimeState{}) {
		t.Fatalf("recovery state = %q/%+v, want cleared", stored.ProviderTaskID, stored.RuntimeState)
	}
	if _, deleted, deleteErr := workflow.DeleteGenerationTask(task.ID); deleteErr != nil || !deleted {
		t.Fatalf("cleanup delete = %v/%v", deleted, deleteErr)
	}
}

func TestGenerationServiceRuntimeContextCancelsAndCleansActiveTask(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	store := NewGenerationTaskService(filepath.Join(t.TempDir(), "settings.db"), nil)
	workflow := NewGenerationService(nil, store, nil)
	workflow.SetGenerationRuntimeContext(parent)
	task := testCodexGenerationTask("task-shutdown", "submitted")
	if err := store.Upsert(task); err != nil {
		t.Fatal(err)
	}
	provider := &quotaSafeCodexProvider{started: make(chan coregeneration.Request, 1)}
	workflow.launchSubmittedGeneration(task, provider, codexImageRequest(task.ID), "create", "", "")
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("generation did not start")
	}
	waitForGenerationCancelRegistry(t, workflow, task.ID, true)
	cancel()
	waitForGenerationCancelRegistry(t, workflow, task.ID, false)
}

func TestRetryCodexStatusGateNeverGeneratesNonfailedTask(t *testing.T) {
	tests := []struct {
		status     string
		identified bool
		wantGet    int
	}{
		{status: "preparing"}, {status: "queued"}, {status: "submitting"}, {status: "submitted"},
		{status: "running"}, {status: "importing"}, {status: "waiting_reconnect"},
		{status: "running", identified: true, wantGet: 1},
		{status: "completed", identified: true},
	}
	for _, test := range tests {
		t.Run(test.status+map[bool]string{true: "-identified", false: "-no-id"}[test.identified], func(t *testing.T) {
			store := NewGenerationTaskService(filepath.Join(t.TempDir(), "settings.db"), nil)
			task := testCodexGenerationTask("task", test.status)
			if test.identified {
				task.ProviderTaskID = codexImageResponseIDPrefix + "thread"
			}
			if err := store.Upsert(task); err != nil {
				t.Fatal(err)
			}
			provider := &quotaSafeCodexProvider{started: make(chan coregeneration.Request, 1)}
			workflow := NewGenerationService(nil, store, nil)
			workflow.SetMediaLinkProviders(provider, &mediaLinkTestProvider{name: "h3"}, func(context.Context, string) (bool, string) { return true, "" })
			_, _, _ = workflow.RetryGenerationTask(context.Background(), task.ID)
			time.Sleep(20 * time.Millisecond)
			generate, get := provider.counts()
			if generate != 0 || get != test.wantGet {
				t.Fatalf("Generate/Get = %d/%d, want 0/%d", generate, get, test.wantGet)
			}
		})
	}
}

func TestRetryCodexFailedTaskIsClaimedOnceConcurrently(t *testing.T) {
	store := NewGenerationTaskService(filepath.Join(t.TempDir(), "settings.db"), nil)
	task := testCodexGenerationTask("task-concurrent", "failed")
	task.ProviderTaskID = codexImageResponseIDPrefix + "stale"
	if err := store.Upsert(task); err != nil {
		t.Fatal(err)
	}
	provider := &quotaSafeCodexProvider{started: make(chan coregeneration.Request, 2)}
	workflow := NewGenerationService(nil, store, nil)
	workflow.SetMediaLinkProviders(provider, &mediaLinkTestProvider{name: "h3"}, func(context.Context, string) (bool, string) { return true, "" })
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	for index := 0; index < 2; index++ {
		go func() {
			<-start
			_, _, _ = workflow.RetryGenerationTask(context.Background(), task.ID)
			done <- struct{}{}
		}()
	}
	close(start)
	for index := 0; index < 2; index++ {
		<-done
	}
	time.Sleep(20 * time.Millisecond)
	generate, _ := provider.counts()
	if generate != 1 {
		t.Fatalf("Generate calls = %d, want 1", generate)
	}
	_, _, _ = workflow.DeleteGenerationTask(task.ID)
}

func TestRetryCodexCallerCancellationAfterClaimDoesNotCancelBackgroundTurn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "settings.db")
	store := NewGenerationTaskService(dbPath, nil)
	mediaAssets := media.NewMediaAssets(dbPath, t.TempDir())
	task := testCodexGenerationTask("task-caller-cancel", "failed")
	if err := store.Upsert(task); err != nil {
		t.Fatal(err)
	}
	provider := &quotaSafeCodexProvider{
		started:          make(chan coregeneration.Request, 1),
		generateRelease:  make(chan struct{}),
		generateResponse: completedCoreCodexResponse("completed after handoff"),
	}
	workflow := NewGenerationService(nil, store, mediaAssets)
	workflow.SetMediaLinkProviders(provider, &mediaLinkTestProvider{name: "h3"}, func(context.Context, string) (bool, string) { return true, "" })
	caller, cancel := context.WithCancel(context.Background())
	if _, status, err := workflow.RetryGenerationTask(caller, task.ID); err != nil || status != http.StatusOK {
		t.Fatalf("RetryGenerationTask() status/error = %d/%v", status, err)
	}
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("background generation did not start")
	}
	cancel()
	time.Sleep(10 * time.Millisecond)
	close(provider.generateRelease)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stored, found, err := store.Get(task.ID)
		if err == nil && found && stored.Status == "completed" {
			return
		}
		if err == nil && found && stored.Status == "failed" {
			t.Fatalf("background task inherited caller cancellation: %s", stored.Error)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("background task did not complete")
}

func TestRetryCodexDeleteDuringPreflightDoesNotResurrectOrGenerate(t *testing.T) {
	store := NewGenerationTaskService(filepath.Join(t.TempDir(), "settings.db"), nil)
	task := testCodexGenerationTask("task-delete-race", "failed")
	if err := store.Upsert(task); err != nil {
		t.Fatal(err)
	}
	provider := &quotaSafeCodexProvider{started: make(chan coregeneration.Request, 1)}
	preflight := make(chan struct{})
	workflow := NewGenerationService(nil, store, nil)
	workflow.SetMediaLinkProviders(provider, &mediaLinkTestProvider{name: "h3"}, func(ctx context.Context, _ string) (bool, string) {
		select {
		case <-preflight:
		default:
			close(preflight)
		}
		<-ctx.Done()
		return false, ctx.Err().Error()
	})
	retryDone := make(chan struct{})
	go func() { _, _, _ = workflow.RetryGenerationTask(context.Background(), task.ID); close(retryDone) }()
	<-preflight
	if _, deleted, err := workflow.DeleteGenerationTask(task.ID); err != nil || !deleted {
		t.Fatalf("Delete = %v/%v", deleted, err)
	}
	select {
	case <-retryDone:
	case <-time.After(time.Second):
		t.Fatal("retry did not stop after delete cancellation")
	}
	if _, found, err := store.Get(task.ID); err != nil || found {
		t.Fatalf("deleted task found=%v err=%v", found, err)
	}
	generate, _ := provider.counts()
	if generate != 0 {
		t.Fatalf("Generate calls = %d, want 0", generate)
	}
}

func TestCodexRecoveryCompletionClearsIdentityAndImportsOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "settings.db")
	store := NewGenerationTaskService(dbPath, nil)
	mediaAssets := media.NewMediaAssets(dbPath, t.TempDir())
	task := testCodexGenerationTask("task-recover", "waiting_reconnect")
	task.ProviderTaskID = codexImageResponseIDPrefix + "thread"
	task.RuntimeState = GenerationTaskRuntimeState{CodexThreadID: "thread", CodexTurnID: "turn", CodexItemID: "item", SavedPath: "/old", RevisedPrompt: "old", ComfyPromptID: "keep"}
	if err := store.Upsert(task); err != nil {
		t.Fatal(err)
	}
	provider := &quotaSafeCodexProvider{getResponse: completedCoreCodexResponse("new revised")}
	workflow := NewGenerationService(nil, store, mediaAssets)
	workflow.SetMediaLinkProviders(provider, &mediaLinkTestProvider{name: "h3"}, func(context.Context, string) (bool, string) { return true, "" })
	if _, status, err := workflow.GetGenerationVideo(context.Background(), task.ID); err != nil || status != http.StatusOK {
		t.Fatalf("first Get = %d/%v", status, err)
	}
	first, found, err := store.Get(task.ID)
	if err != nil || !found {
		t.Fatalf("stored = %v/%v", found, err)
	}
	assertTerminalCodexState(t, first, "new revised", "keep")
	assets := len(first.Assets)
	if _, status, err := workflow.GetGenerationVideo(context.Background(), task.ID); err != nil || status != http.StatusOK {
		t.Fatalf("second Get = %d/%v", status, err)
	}
	second, _, _ := store.Get(task.ID)
	_, get := provider.counts()
	if get != 1 || len(second.Assets) != assets {
		t.Fatalf("second Get provider/assets = %d/%d, want 1/%d", get, len(second.Assets), assets)
	}
}

func TestCodexBackgroundCompletionClearsRecoveryIdentity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "settings.db")
	store := NewGenerationTaskService(dbPath, nil)
	mediaAssets := media.NewMediaAssets(dbPath, t.TempDir())
	task := testCodexGenerationTask("task-background", "submitted")
	task.ProviderTaskID = codexImageResponseIDPrefix + "old"
	task.RuntimeState = GenerationTaskRuntimeState{CodexThreadID: "old", RevisedPrompt: "old", ComfyPromptID: "keep"}
	if err := store.Upsert(task); err != nil {
		t.Fatal(err)
	}
	provider := &quotaSafeCodexProvider{started: make(chan coregeneration.Request, 1), generateResponse: completedCoreCodexResponse("background revised")}
	workflow := NewGenerationService(nil, store, mediaAssets)
	workflow.launchSubmittedGeneration(task, provider, codexImageRequest(task.ID), "create", "", "")
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("Generate did not start")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stored, found, err := store.Get(task.ID)
		if err == nil && found && stored.Status == "completed" {
			assertTerminalCodexState(t, stored, "background revised", "keep")
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("background task did not complete")
}

func TestCodexWorkerSkipsStoredTerminalTask(t *testing.T) {
	provider := &quotaSafeCodexProvider{started: make(chan coregeneration.Request, 1)}
	workflow := NewGenerationService(nil, nil, nil)
	workflow.SetMediaLinkProviders(provider, &mediaLinkTestProvider{name: "h3"}, func(context.Context, string) (bool, string) { return true, "" })
	task := testCodexGenerationTask("task-terminal-worker", "completed")
	task.ProviderTaskID = codexImageResponseIDPrefix + "thread"
	workflow.PollGenerationTask(context.Background(), task)
	generate, get := provider.counts()
	if generate != 0 || get != 0 {
		t.Fatalf("terminal worker Generate/Get = %d/%d", generate, get)
	}
}

func completedCoreCodexResponse(revised string) coregeneration.Response {
	return coregeneration.Response{ID: codexImageResponseIDPrefix + "thread", Status: "completed", Assets: []coregeneration.Asset{{Kind: coregeneration.KindImage, MIMEType: "image/png", Base64: base64.StdEncoding.EncodeToString(testPNGBytes())}}, Metadata: map[string]any{"runtime_state": GenerationTaskRuntimeState{CodexThreadID: "thread", CodexTurnID: "turn", CodexItemID: "item", SavedPath: "/validated", RevisedPrompt: revised}}}
}

func assertTerminalCodexState(t *testing.T, task GenerationTaskRecord, revised string, comfy string) {
	t.Helper()
	if task.ProviderTaskID != "" || task.RuntimeState.CodexThreadID != "" || task.RuntimeState.CodexTurnID != "" || task.RuntimeState.CodexItemID != "" || task.RuntimeState.SavedPath != "" {
		t.Fatalf("terminal recovery identity not cleared: %q %+v", task.ProviderTaskID, task.RuntimeState)
	}
	if task.RuntimeState.RevisedPrompt != revised || task.RuntimeState.ComfyPromptID != comfy {
		t.Fatalf("preserved runtime = %+v, want revised/comfy %q/%q", task.RuntimeState, revised, comfy)
	}
}

func testCodexGenerationTask(id string, status string) GenerationTaskRecord {
	route, _ := coregeneration.FindRoute(coregeneration.RouteCodexImage)
	return GenerationTaskRecord{
		ID: id, Kind: string(route.Kind), RouteID: route.ID, FamilyID: route.FamilyID, VersionID: route.VersionID,
		Provider: route.Provider, Model: route.Model, Prompt: "portrait", Params: map[string]any{}, Status: status,
	}
}

func waitForGenerationCancelRegistry(t *testing.T, workflow *GenerationService, taskID string, want bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		workflow.generationCancelMu.Lock()
		_, got := workflow.generationCancels[taskID]
		workflow.generationCancelMu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("cancel registry for %q did not become %v", taskID, want)
}
