package generation

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/codexapp"
)

type quotaSafeCodexProvider struct {
	mu       sync.Mutex
	generate int
	get      int
	started  chan coregeneration.Request
}

func (*quotaSafeCodexProvider) Name() string { return "quota-safe-codex" }
func (provider *quotaSafeCodexProvider) Generate(ctx context.Context, request coregeneration.Request) (coregeneration.Response, error) {
	provider.mu.Lock()
	provider.generate++
	provider.mu.Unlock()
	if provider.started != nil {
		provider.started <- request
	}
	<-ctx.Done()
	return coregeneration.Response{}, ctx.Err()
}
func (provider *quotaSafeCodexProvider) Get(_ context.Context, id string) (coregeneration.Response, error) {
	provider.mu.Lock()
	provider.get++
	provider.mu.Unlock()
	return coregeneration.Response{ID: id, Status: "waiting_reconnect", Metadata: map[string]any{"runtime_state": GenerationTaskRuntimeState{CodexThreadID: "thread-existing"}}}, nil
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
