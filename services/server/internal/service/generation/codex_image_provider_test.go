package generation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/codexapp"
)

type codexImageSessionStub struct {
	mu              sync.Mutex
	capabilities    codexapp.ModelProviderCapabilities
	capabilitiesErr error
	generate        func(context.Context, codexapp.ImageGenerationRequest, func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error)
	read            func(context.Context, string) (codexapp.ImageGenerationResult, error)
	requests        []codexapp.ImageGenerationRequest
	readThreadIDs   []string
}

func (stub *codexImageSessionStub) Capabilities(context.Context) (codexapp.ModelProviderCapabilities, error) {
	return stub.capabilities, stub.capabilitiesErr
}

func (stub *codexImageSessionStub) GenerateImage(ctx context.Context, request codexapp.ImageGenerationRequest, checkpoint func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
	stub.mu.Lock()
	stub.requests = append(stub.requests, request)
	stub.mu.Unlock()
	if stub.generate == nil {
		return codexapp.ImageGenerationResult{}, errors.New("unexpected GenerateImage call")
	}
	return stub.generate(ctx, request, checkpoint)
}

func (stub *codexImageSessionStub) ReadImageResult(ctx context.Context, threadID string) (codexapp.ImageGenerationResult, error) {
	stub.mu.Lock()
	stub.readThreadIDs = append(stub.readThreadIDs, threadID)
	stub.mu.Unlock()
	if stub.read == nil {
		return codexapp.ImageGenerationResult{}, errors.New("unexpected ReadImageResult call")
	}
	return stub.read(ctx, threadID)
}

func TestCodexImageProviderResultCases(t *testing.T) {
	tests := []struct {
		name       string
		capability bool
		generate   func(string) func(context.Context, codexapp.ImageGenerationRequest, func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error)
		wantStatus string
		wantID     string
		wantErr    string
	}{
		{
			name:       "capability unavailable",
			capability: false,
			wantErr:    "image generation capability is unavailable",
		},
		{
			name:       "successful structured item",
			capability: true,
			generate: func(_ string) func(context.Context, codexapp.ImageGenerationRequest, func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
				return func(_ context.Context, request codexapp.ImageGenerationRequest, checkpoint func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
					path := writeTestPNG(t, request.JobDir, "image.png")
					checkpoint(codexapp.ImageGenerationCheckpoint{Stage: codexapp.ImageGenerationStageThreadStarted, ThreadID: "thread-success"})
					checkpoint(codexapp.ImageGenerationCheckpoint{Stage: codexapp.ImageGenerationStageTurnStarted, ThreadID: "thread-success", TurnID: "turn-success"})
					return completedCodexImageResult("thread-success", "turn-success", "item-success", path), nil
				}
			},
			wantStatus: "completed",
			wantID:     "codex.imagegen:thread-success",
		},
		{
			name:       "failure item",
			capability: true,
			generate: func(_ string) func(context.Context, codexapp.ImageGenerationRequest, func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
				return func(_ context.Context, _ codexapp.ImageGenerationRequest, checkpoint func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
					item := codexapp.ImageGenerationThreadItem{
						ID: "item-failed", Type: "imageGeneration", Status: "failed", Failure: &codexapp.ImageGenerationFailure{Type: "usageLimitExceeded"},
					}
					checkpoint(codexapp.ImageGenerationCheckpoint{Stage: codexapp.ImageGenerationStageThreadStarted, ThreadID: "thread-failed"})
					checkpoint(codexapp.ImageGenerationCheckpoint{Stage: codexapp.ImageGenerationStageItemCompleted, ThreadID: "thread-failed", TurnID: "turn-failed", Item: &item})
					checkpoint(codexapp.ImageGenerationCheckpoint{Stage: codexapp.ImageGenerationStageTurnCompleted, ThreadID: "thread-failed", TurnID: "turn-failed"})
					return codexapp.ImageGenerationResult{}, errors.New("Codex image generation failed: usageLimitExceeded")
				}
			},
			wantErr: "Codex image generation failed: usageLimitExceeded",
		},
		{
			name:       "saved path outside job root",
			capability: true,
			generate: func(dataRoot string) func(context.Context, codexapp.ImageGenerationRequest, func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
				outside := writeTestPNG(t, filepath.Dir(dataRoot), "outside.png")
				return func(context.Context, codexapp.ImageGenerationRequest, func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
					return completedCodexImageResult("thread-outside", "turn-outside", "item-outside", outside), nil
				}
			},
			wantErr: "outside Codex image job directory",
		},
		{
			name:       "interrupted turn is resumable",
			capability: true,
			generate: func(_ string) func(context.Context, codexapp.ImageGenerationRequest, func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
				return func(_ context.Context, _ codexapp.ImageGenerationRequest, checkpoint func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
					checkpoint(codexapp.ImageGenerationCheckpoint{Stage: codexapp.ImageGenerationStageThreadStarted, ThreadID: "thread-resume"})
					checkpoint(codexapp.ImageGenerationCheckpoint{Stage: codexapp.ImageGenerationStageTurnStarted, ThreadID: "thread-resume", TurnID: "turn-resume"})
					return codexapp.ImageGenerationResult{}, errors.New("reading Codex image turn: EOF")
				}
			},
			wantStatus: "waiting_reconnect",
			wantID:     "codex.imagegen:thread-resume",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataRoot := t.TempDir()
			stub := &codexImageSessionStub{capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: test.capability}}
			if test.generate != nil {
				stub.generate = test.generate(dataRoot)
			}
			provider := NewCodexImageProvider(stub, dataRoot)
			response, err := provider.Generate(context.Background(), codexImageRequest("task-result"))
			if test.wantErr != "" {
				if err == nil || !contains(err.Error(), test.wantErr) {
					t.Fatalf("Generate() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if response.Status != test.wantStatus || response.ID != test.wantID {
				t.Fatalf("Generate() response = %#v, want status/id %q/%q", response, test.wantStatus, test.wantID)
			}
			state, ok := response.Metadata["runtime_state"].(GenerationTaskRuntimeState)
			if !ok || state.CodexThreadID == "" {
				t.Fatalf("runtime_state = %#v, want typed state with thread id", response.Metadata["runtime_state"])
			}
			if response.Status == "completed" {
				if len(response.Assets) != 1 || response.Assets[0].MIMEType != "image/png" || response.Assets[0].Base64 == "" {
					t.Fatalf("assets = %#v, want one imported PNG payload", response.Assets)
				}
			}
			if _, marshalErr := json.Marshal(response.Metadata); marshalErr != nil {
				t.Fatalf("response metadata is not serializable: %v", marshalErr)
			}
		})
	}
}

func TestCodexImageProviderMaterializesOrderedValidatedReferences(t *testing.T) {
	dataRoot := t.TempDir()
	local := writeTestPNG(t, filepath.Join(dataRoot, "assets"), "local.png")
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(testPNGBytes())
	var got []string
	stub := &codexImageSessionStub{
		capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: true},
		generate: func(_ context.Context, request codexapp.ImageGenerationRequest, _ func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
			got = append([]string(nil), request.ReferencePaths...)
			path := writeTestPNG(t, request.JobDir, "image.png")
			return completedCodexImageResult("thread-refs", "turn-refs", "item-refs", path), nil
		},
	}
	provider := NewCodexImageProvider(stub, dataRoot)
	request := codexImageRequest("task-refs")
	request.ReferenceURLs = []string{dataURI, local}
	if _, err := provider.Generate(context.Background(), request); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	canonicalLocal, err := filepath.EvalSymlinks(local)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	if len(got) != 2 || filepath.Base(got[0]) != "reference-001.png" || got[1] != canonicalLocal {
		t.Fatalf("ordered reference paths = %#v", got)
	}
}

func TestCodexImageProviderSerializesFIFOAndCancelsQueuedTask(t *testing.T) {
	started := make(chan string, 3)
	release := make(chan struct{})
	stub := &codexImageSessionStub{
		capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: true},
		generate: func(ctx context.Context, request codexapp.ImageGenerationRequest, _ func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
			started <- request.Prompt
			select {
			case <-ctx.Done():
				return codexapp.ImageGenerationResult{}, ctx.Err()
			case <-release:
			}
			path := writeTestPNG(t, request.JobDir, "image.png")
			return completedCodexImageResult("thread-"+request.Prompt, "turn", "item", path), nil
		},
	}
	provider := NewCodexImageProvider(stub, t.TempDir())
	type result struct{ err error }
	firstDone := make(chan result, 1)
	go func() {
		_, err := provider.Generate(context.Background(), codexImageRequestWithPrompt("task-first", "first"))
		firstDone <- result{err: err}
	}()
	if got := <-started; got != "first" {
		t.Fatalf("first started = %q", got)
	}

	secondDone := make(chan result, 1)
	go func() {
		_, err := provider.Generate(context.Background(), codexImageRequestWithPrompt("task-second", "second"))
		secondDone <- result{err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	cancelCtx, cancel := context.WithCancel(context.Background())
	canceledDone := make(chan result, 1)
	go func() {
		_, err := provider.Generate(cancelCtx, codexImageRequestWithPrompt("task-canceled", "canceled"))
		canceledDone <- result{err: err}
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if got := (<-canceledDone).err; !errors.Is(got, context.Canceled) {
		t.Fatalf("canceled queued Generate() error = %v, want context.Canceled", got)
	}

	release <- struct{}{}
	if got := (<-firstDone).err; got != nil {
		t.Fatalf("first Generate() error = %v", got)
	}
	if got := <-started; got != "second" {
		t.Fatalf("second started = %q, want FIFO second", got)
	}
	release <- struct{}{}
	if got := (<-secondDone).err; got != nil {
		t.Fatalf("second Generate() error = %v", got)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.requests) != 2 {
		t.Fatalf("GenerateImage calls = %d, want 2 (canceled queued task not submitted)", len(stub.requests))
	}
}

func TestCodexImageProviderResumesExistingThread(t *testing.T) {
	dataRoot := t.TempDir()
	jobDir := filepath.Join(dataRoot, "generation", "codex-image", "task-existing")
	path := writeTestPNG(t, jobDir, "resumed.png")
	stub := &codexImageSessionStub{read: func(_ context.Context, threadID string) (codexapp.ImageGenerationResult, error) {
		return completedCodexImageResult(threadID, "turn-existing", "item-existing", path), nil
	}}
	provider := NewCodexImageProvider(stub, dataRoot)
	response, err := provider.Get(context.Background(), "codex.imagegen:thread-existing")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if response.ID != "codex.imagegen:thread-existing" || response.Status != "completed" || len(response.Assets) != 1 {
		t.Fatalf("Get() response = %#v", response)
	}
	if !reflect.DeepEqual(stub.readThreadIDs, []string{"thread-existing"}) {
		t.Fatalf("ReadImageResult ids = %v", stub.readThreadIDs)
	}
}

func TestCodexImageProviderResumeJobRootBoundary(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(t *testing.T, dataRoot string, taskA string, taskB string, outside string) (string, string)
		wantErr string
	}{
		{
			name: "valid resumed output",
			arrange: func(t *testing.T, _ string, taskA string, _ string, _ string) (string, string) {
				return taskA, writeTestPNG(t, taskA, "result.png")
			},
		},
		{
			name: "sibling task output",
			arrange: func(t *testing.T, _ string, taskA string, taskB string, _ string) (string, string) {
				if err := os.MkdirAll(taskA, 0o700); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
				return taskA, writeTestPNG(t, taskB, "sibling.png")
			},
			wantErr: "outside Codex image job directory",
		},
		{
			name: "missing cwd",
			arrange: func(t *testing.T, _ string, taskA string, _ string, _ string) (string, string) {
				return "", writeTestPNG(t, taskA, "result.png")
			},
			wantErr: "job directory is missing",
		},
		{
			name: "cwd is provider root",
			arrange: func(t *testing.T, dataRoot string, taskA string, _ string, _ string) (string, string) {
				return filepath.Join(dataRoot, "generation", "codex-image"), writeTestPNG(t, taskA, "result.png")
			},
			wantErr: "immediate task directory",
		},
		{
			name: "cwd outside provider root",
			arrange: func(t *testing.T, _ string, taskA string, _ string, outside string) (string, string) {
				return outside, writeTestPNG(t, taskA, "result.png")
			},
			wantErr: "outside Codex image jobs directory",
		},
		{
			name: "symlinked cwd escape",
			arrange: func(t *testing.T, dataRoot string, taskA string, _ string, outside string) (string, string) {
				jobsRoot := filepath.Join(dataRoot, "generation", "codex-image")
				if err := os.MkdirAll(jobsRoot, 0o700); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
				link := filepath.Join(jobsRoot, "task-link")
				if err := os.Symlink(outside, link); err != nil {
					t.Fatalf("Symlink() error = %v", err)
				}
				return link, writeTestPNG(t, taskA, "result.png")
			},
			wantErr: "outside Codex image jobs directory",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataRoot := t.TempDir()
			jobsRoot := filepath.Join(dataRoot, "generation", "codex-image")
			taskA := filepath.Join(jobsRoot, "task-a")
			taskB := filepath.Join(jobsRoot, "task-b")
			outside := t.TempDir()
			jobDir, savedPath := test.arrange(t, dataRoot, taskA, taskB, outside)
			stub := &codexImageSessionStub{read: func(_ context.Context, threadID string) (codexapp.ImageGenerationResult, error) {
				result := completedCodexImageResult(threadID, "turn", "item", savedPath)
				result.JobDir = jobDir
				return result, nil
			}}
			provider := NewCodexImageProvider(stub, dataRoot)
			response, err := provider.Get(context.Background(), "codex.imagegen:thread-boundary")
			if test.wantErr != "" {
				if err == nil || !contains(err.Error(), test.wantErr) {
					t.Fatalf("Get() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if response.Status != "completed" || len(response.Assets) != 1 {
				t.Fatalf("Get() response = %#v", response)
			}
		})
	}
}

func TestCodexImageProviderGetKeepsSameIDForNonterminalThread(t *testing.T) {
	stub := &codexImageSessionStub{read: func(_ context.Context, threadID string) (codexapp.ImageGenerationResult, error) {
		return codexapp.ImageGenerationResult{ThreadID: threadID, TurnID: "turn-running", Item: codexapp.ImageGenerationThreadItem{ID: "item-running", Type: "imageGeneration", Status: "inProgress"}}, nil
	}}
	provider := NewCodexImageProvider(stub, t.TempDir())
	response, err := provider.Get(context.Background(), "codex.imagegen:thread-running")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if response.ID != "codex.imagegen:thread-running" || response.Status != "waiting_reconnect" || len(response.Assets) != 0 {
		t.Fatalf("Get() response = %#v", response)
	}
}

func TestCodexImageProviderRejectsCanonicalSymlinkEscape(t *testing.T) {
	dataRoot := t.TempDir()
	outsideRoot := t.TempDir()
	outside := writeTestPNG(t, outsideRoot, "outside.png")
	stub := &codexImageSessionStub{
		capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: true},
		generate: func(_ context.Context, request codexapp.ImageGenerationRequest, _ func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
			link := filepath.Join(request.JobDir, "linked.png")
			if err := os.Symlink(outside, link); err != nil {
				t.Fatalf("Symlink() error = %v", err)
			}
			return completedCodexImageResult("thread-link", "turn-link", "item-link", link), nil
		},
	}
	provider := NewCodexImageProvider(stub, dataRoot)
	_, err := provider.Generate(context.Background(), codexImageRequest("task-link"))
	if err == nil || !contains(err.Error(), "outside Codex image job directory") {
		t.Fatalf("Generate() error = %v, want canonical symlink escape rejection", err)
	}
}

func TestCodexImageProviderRejectsSymlinkedJobRootEscape(t *testing.T) {
	dataRoot := t.TempDir()
	outsideRoot := t.TempDir()
	if err := os.Symlink(outsideRoot, filepath.Join(dataRoot, "generation")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	stub := &codexImageSessionStub{capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: true}}
	provider := NewCodexImageProvider(stub, dataRoot)
	_, err := provider.Generate(context.Background(), codexImageRequest("task-root-link"))
	if err == nil || !contains(err.Error(), "outside MediaLink user data directory") {
		t.Fatalf("Generate() error = %v, want symlinked job-root escape rejection", err)
	}
}

func TestCodexImageProviderRejectsUnsafeTaskID(t *testing.T) {
	stub := &codexImageSessionStub{capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: true}}
	provider := NewCodexImageProvider(stub, t.TempDir())
	request := codexImageRequest("../collision")
	_, err := provider.Generate(context.Background(), request)
	if err == nil || !contains(err.Error(), "task id") {
		t.Fatalf("Generate() error = %v, want unsafe task id rejection", err)
	}
}

func codexImageRequest(taskID string) coregeneration.Request {
	return codexImageRequestWithPrompt(taskID, "cinematic portrait")
}

func codexImageRequestWithPrompt(taskID string, prompt string) coregeneration.Request {
	return coregeneration.Request{
		Kind:    coregeneration.KindImage,
		RouteID: coregeneration.RouteCodexImage,
		Prompt:  prompt,
		Options: map[string]any{codexImageTaskIDRequestOption: taskID},
	}
}

func completedCodexImageResult(threadID string, turnID string, itemID string, path string) codexapp.ImageGenerationResult {
	revised := "revised prompt"
	return codexapp.ImageGenerationResult{
		ThreadID: threadID,
		TurnID:   turnID,
		JobDir:   filepath.Dir(path),
		Item: codexapp.ImageGenerationThreadItem{
			ID: itemID, Type: "imageGeneration", Status: "completed", SavedPath: &path, RevisedPrompt: &revised,
		},
	}
}

func writeTestPNG(t *testing.T, dir string, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	path := filepath.Join(dir, name)
	// A complete 1x1 transparent PNG keeps MIME sniffing honest.
	data := testPNGBytes()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func testPNGBytes() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89}
}

func contains(value string, substring string) bool {
	return len(value) >= len(substring) && (value == substring || containsAt(value, substring))
}

func containsAt(value string, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
