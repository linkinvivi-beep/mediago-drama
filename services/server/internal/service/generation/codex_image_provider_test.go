package generation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/codexapp"
	"github.com/mediago-dev/mediago-drama/services/server/internal/service/media"
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

type managedCodexClientStub struct {
	call       func(context.Context, string, any, any) error
	next       func(context.Context) (codexapp.Message, error)
	closeCount atomic.Int32
}

func (client *managedCodexClientStub) Call(ctx context.Context, method string, params any, result any) error {
	return client.call(ctx, method, params, result)
}
func (client *managedCodexClientStub) Next(ctx context.Context) (codexapp.Message, error) {
	if client.next == nil {
		return codexapp.Message{}, errors.New("unexpected Next call")
	}
	return client.next(ctx)
}
func (client *managedCodexClientStub) Close() { client.closeCount.Add(1) }

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
					t.Fatalf("assets = %#v, want one immutable in-memory PNG payload", response.Assets)
				}
				if _, exists := response.Assets[0].Metadata["saved_path"]; exists {
					t.Fatalf("asset metadata = %#v, want no saved path", response.Assets[0].Metadata)
				}
				if marked, _ := response.Assets[0].Metadata["_medialink_internal_codex_image_payload"].(bool); !marked {
					t.Fatalf("asset metadata = %#v, want internal Codex payload marker", response.Assets[0].Metadata)
				}
			}
			if _, marshalErr := json.Marshal(response.Metadata); marshalErr != nil {
				t.Fatalf("response metadata is not serializable: %v", marshalErr)
			}
		})
	}
}

func TestCodexImageProviderImportsExactOfficialGeneratedImage(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	savedPath := writeTestPNG(t, filepath.Join(codexHome, "generated_images", "thread-official"), "item-official.png")
	stub := &codexImageSessionStub{
		capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: true},
		generate: func(context.Context, codexapp.ImageGenerationRequest, func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
			return completedCodexImageResult("thread-official", "turn-official", "item-official", savedPath), nil
		},
	}

	response, err := NewCodexImageProvider(stub, t.TempDir()).Generate(context.Background(), codexImageRequest("task-official"))
	if err != nil || response.Status != "completed" || len(response.Assets) != 1 {
		t.Fatalf("Generate() = %#v, %v", response, err)
	}
}

func TestCodexImageProviderRejectsInvalidOfficialGeneratedImagePath(t *testing.T) {
	tests := []struct {
		name     string
		threadID string
		itemID   string
		arrange  func(t *testing.T, codexHome string) string
	}{
		{
			name: "wrong thread", threadID: "thread-good", itemID: "item-good",
			arrange: func(t *testing.T, codexHome string) string {
				return writeTestPNG(t, filepath.Join(codexHome, "generated_images", "thread-other"), "item-good.png")
			},
		},
		{
			name: "wrong item", threadID: "thread-good", itemID: "item-good",
			arrange: func(t *testing.T, codexHome string) string {
				return writeTestPNG(t, filepath.Join(codexHome, "generated_images", "thread-good"), "item-other.png")
			},
		},
		{
			name: "unsupported extension", threadID: "thread-good", itemID: "item-good",
			arrange: func(t *testing.T, codexHome string) string {
				return writeTestPNG(t, filepath.Join(codexHome, "generated_images", "thread-good"), "item-good.webp")
			},
		},
		{
			name: "outside root", threadID: "thread-good", itemID: "item-good",
			arrange: func(t *testing.T, codexHome string) string {
				return writeTestPNG(t, filepath.Join(codexHome, "outside"), "item-good.png")
			},
		},
		{
			name: "symlinked file", threadID: "thread-good", itemID: "item-good",
			arrange: func(t *testing.T, codexHome string) string {
				outside := writeTestPNG(t, t.TempDir(), "outside.png")
				dir := filepath.Join(codexHome, "generated_images", "thread-good")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "item-good.png")
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "symlinked parent", threadID: "thread-good", itemID: "item-good",
			arrange: func(t *testing.T, codexHome string) string {
				outsideDir := t.TempDir()
				_ = writeTestPNG(t, outsideDir, "item-good.png")
				generatedImages := filepath.Join(codexHome, "generated_images")
				if err := os.MkdirAll(generatedImages, 0o700); err != nil {
					t.Fatal(err)
				}
				threadDir := filepath.Join(generatedImages, "thread-good")
				if err := os.Symlink(outsideDir, threadDir); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(threadDir, "item-good.png")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			codexHome := t.TempDir()
			t.Setenv("CODEX_HOME", codexHome)
			savedPath := test.arrange(t, codexHome)
			stub := &codexImageSessionStub{
				capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: true},
				generate: func(context.Context, codexapp.ImageGenerationRequest, func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
					return completedCodexImageResult(test.threadID, "turn-good", test.itemID, savedPath), nil
				},
			}

			response, err := NewCodexImageProvider(stub, t.TempDir()).Generate(context.Background(), codexImageRequest("task-invalid"))
			if err == nil {
				t.Fatalf("Generate() = %#v, want invalid official path error", response)
			}
			if len(response.Assets) != 0 {
				t.Fatalf("Generate() assets = %#v, want none", response.Assets)
			}
		})
	}
}

func TestCodexImageProviderOutputCannotBeSwappedBeforeAssetImport(t *testing.T) {
	root := t.TempDir()
	jobDir := filepath.Join(root, "generation", "codex-image", "task-swap", "attempt-0123456789abcdef0123456789abcdef")
	savedPath := writeTestPNG(t, jobDir, "generated.png")
	want := append([]byte(nil), testPNGBytes()...)
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("outside-file-must-not-be-imported"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := NewCodexImageProvider(&codexImageSessionStub{read: func(_ context.Context, threadID string) (codexapp.ImageGenerationResult, error) {
		return completedCodexImageResult(threadID, "turn-swap", "item-swap", savedPath), nil
	}}, root)
	response, err := provider.Get(context.Background(), codexImageResponseIDPrefix+"thread-swap")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(savedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, savedPath); err != nil {
		t.Fatal(err)
	}

	mediaAssets := media.NewMediaAssets(filepath.Join(t.TempDir(), "settings.db"), t.TempDir())
	cached := NewGenerationService(nil, nil, mediaAssets).CacheGenerationResponseAssets(context.Background(), response)
	if len(cached.Assets) != 1 || !strings.HasPrefix(cached.Assets[0].URL, "/api/v1/media-assets/") {
		t.Fatalf("cached assets = %+v", cached.Assets)
	}
	assets, err := mediaAssets.List("")
	if err != nil || len(assets) != 1 {
		t.Fatalf("List() = %+v, err %v", assets, err)
	}
	got, err := os.ReadFile(assets[0].FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("imported bytes = %q, want originally validated PNG", got)
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
	if len(got) != 2 || filepath.Ext(got[0]) != ".png" || filepath.Ext(got[1]) != ".png" {
		t.Fatalf("ordered reference paths = %#v", got)
	}
	if filepath.Dir(got[0]) == filepath.Dir(local) || filepath.Dir(got[0]) != filepath.Dir(got[1]) || got[0] == got[1] {
		t.Fatalf("materialized reference paths = %#v, want distinct attempt-owned copies", got)
	}
}

func TestCodexImageProviderUsesFreshAttemptDirectoryForEveryTurn(t *testing.T) {
	var dirs []string
	stub := &codexImageSessionStub{
		capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: true},
		generate: func(_ context.Context, request codexapp.ImageGenerationRequest, _ func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
			dirs = append(dirs, request.JobDir)
			path := writeTestPNG(t, request.JobDir, "result.png")
			return completedCodexImageResult("thread-"+filepath.Base(request.JobDir), "turn", "item", path), nil
		},
	}
	provider := NewCodexImageProvider(stub, t.TempDir())
	for index := 0; index < 2; index++ {
		if _, err := provider.Generate(context.Background(), codexImageRequest("same-task")); err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	if len(dirs) != 2 || dirs[0] == dirs[1] {
		t.Fatalf("attempt dirs = %#v, want two distinct directories", dirs)
	}
	for _, dir := range dirs {
		if filepath.Base(filepath.Dir(dir)) != "same-task" || !contains(filepath.Base(dir), "attempt-") {
			t.Fatalf("attempt dir = %q, want root/task/attempt hierarchy", dir)
		}
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

func TestCodexImageProviderFIFOAllowsMiddleCancellationWithoutReordering(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		started := make(chan string, 8)
		release := make(chan struct{}, 8)
		stub := &codexImageSessionStub{
			capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: true},
			generate: func(ctx context.Context, request codexapp.ImageGenerationRequest, _ func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
				started <- request.Prompt
				select {
				case <-ctx.Done():
					return codexapp.ImageGenerationResult{}, ctx.Err()
				case <-release:
				}
				path := writeTestPNG(t, request.JobDir, "result.png")
				return completedCodexImageResult("thread-"+request.Prompt, "turn", "item", path), nil
			},
		}
		provider := NewCodexImageProvider(stub, t.TempDir())
		type result struct {
			prompt string
			err    error
		}
		done := make(chan result, 5)
		launch := func(ctx context.Context, prompt string) {
			go func() {
				_, err := provider.Generate(ctx, codexImageRequestWithPrompt("task-"+prompt, prompt))
				done <- result{prompt: prompt, err: err}
			}()
		}
		launch(context.Background(), "first")
		if got := <-started; got != "first" {
			t.Fatalf("first start = %q", got)
		}
		launch(context.Background(), "second")
		waitForCodexImageQueueDepth(t, provider.queue, 1)
		cancelCtx, cancel := context.WithCancel(context.Background())
		launch(cancelCtx, "canceled")
		waitForCodexImageQueueDepth(t, provider.queue, 2)
		launch(context.Background(), "third")
		waitForCodexImageQueueDepth(t, provider.queue, 3)
		launch(context.Background(), "fourth")
		waitForCodexImageQueueDepth(t, provider.queue, 4)
		cancel()
		release <- struct{}{}
		want := []string{"second", "third", "fourth"}
		for _, expected := range want {
			if got := <-started; got != expected {
				t.Fatalf("start order = %q, want %q (iteration %d)", got, expected, iteration)
			}
			release <- struct{}{}
		}
		for index := 0; index < 5; index++ {
			result := <-done
			if result.prompt == "canceled" && !errors.Is(result.err, context.Canceled) {
				t.Fatalf("canceled error = %v", result.err)
			}
		}
	}
}

func TestCodexImageProviderResumesExistingThread(t *testing.T) {
	dataRoot := t.TempDir()
	jobDir := filepath.Join(dataRoot, "generation", "codex-image", "task-existing", "attempt-0123456789abcdef0123456789abcdef")
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
			wantErr: "exact task attempt directory",
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
			wantErr: "must not contain symlinks",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataRoot := t.TempDir()
			jobsRoot := filepath.Join(dataRoot, "generation", "codex-image")
			taskA := filepath.Join(jobsRoot, "task-a", "attempt-0123456789abcdef0123456789abcdef")
			taskB := filepath.Join(jobsRoot, "task-b", "attempt-fedcba9876543210fedcba9876543210")
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

func TestCodexImageProviderDoesNotPersistUnvalidatedCheckpointPath(t *testing.T) {
	malicious := filepath.Join(t.TempDir(), "malicious.png")
	var progress []coregeneration.Response
	stub := &codexImageSessionStub{
		capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: true},
		generate: func(_ context.Context, _ codexapp.ImageGenerationRequest, checkpoint func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
			item := codexapp.ImageGenerationThreadItem{ID: "item", Type: "imageGeneration", Status: "completed", SavedPath: &malicious}
			checkpoint(codexapp.ImageGenerationCheckpoint{Stage: codexapp.ImageGenerationStageItemCompleted, ThreadID: "thread", TurnID: "turn", Item: &item})
			return codexapp.ImageGenerationResult{ThreadID: "thread", TurnID: "turn", Item: item}, nil
		},
	}
	provider := NewCodexImageProvider(stub, t.TempDir())
	request := codexImageRequest("task-malicious")
	request.Options[coregeneration.ProgressCallbackOption] = coregeneration.ProgressCallback(func(_ context.Context, event coregeneration.ProgressEvent) {
		progress = append(progress, event.Response)
	})
	_, err := provider.Generate(context.Background(), request)
	if err == nil {
		t.Fatal("Generate() error = nil, want invalid saved path")
	}
	if len(progress) == 0 {
		t.Fatal("progress callback was not invoked")
	}
	for _, response := range progress {
		state, _ := response.Metadata["runtime_state"].(GenerationTaskRuntimeState)
		if state.SavedPath != "" {
			t.Fatalf("progress SavedPath = %q, want empty", state.SavedPath)
		}
	}
}

func TestCodexImageProviderRejectsCorruptAndOversizedImages(t *testing.T) {
	t.Run("signature only output", func(t *testing.T) {
		dataRoot := t.TempDir()
		stub := &codexImageSessionStub{capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: true}, generate: func(_ context.Context, request codexapp.ImageGenerationRequest, _ func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
			path := filepath.Join(request.JobDir, "bad.png")
			valid := testPNGBytes()
			if err := os.WriteFile(path, valid[:len(valid)-12], 0o600); err != nil {
				t.Fatal(err)
			}
			return completedCodexImageResult("thread", "turn", "item", path), nil
		}}
		_, err := NewCodexImageProvider(stub, dataRoot).Generate(context.Background(), codexImageRequest("task"))
		if err == nil || !contains(err.Error(), "invalid image") {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("too many references", func(t *testing.T) {
		provider := NewCodexImageProvider(&codexImageSessionStub{capabilities: codexapp.ModelProviderCapabilities{ImageGeneration: true}}, t.TempDir())
		request := codexImageRequest("task")
		request.ReferenceURLs = make([]string, maxCodexImageReferences+1)
		for index := range request.ReferenceURLs {
			request.ReferenceURLs[index] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(testPNGBytes())
		}
		_, err := provider.Generate(context.Background(), request)
		if err == nil || !contains(err.Error(), "reference count") {
			t.Fatalf("Generate() error = %v", err)
		}
	})
	t.Run("base64 preflight", func(t *testing.T) {
		value := "data:image/png;base64," + base64.StdEncoding.EncodeToString(testPNGBytes())
		if _, _, err := decodeCodexImageDataReference(value, 0, 8); err == nil || !contains(err.Error(), "exceeds 8 bytes") {
			t.Fatalf("decodeCodexImageDataReference() error = %v", err)
		}
	})
	t.Run("oversized sparse output", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "huge.png")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxCodexImageOutputBytes + 1); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := readValidatedCodexImage(path, root, maxCodexImageOutputBytes, "test root"); err == nil || !contains(err.Error(), "exceeds") {
			t.Fatalf("readValidatedCodexImage() error = %v", err)
		}
	})
	t.Run("dimension limit", func(t *testing.T) {
		value := image.NewRGBA(image.Rect(0, 0, maxCodexImageDimension+1, 1))
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, value); err != nil {
			t.Fatal(err)
		}
		if _, err := validateCodexImageBytes(buffer.Bytes()); err == nil || !contains(err.Error(), "dimensions") {
			t.Fatalf("validateCodexImageBytes() error = %v", err)
		}
	})
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
	if err == nil || (!contains(err.Error(), "symbolic") && !contains(err.Error(), "too many levels")) {
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

func TestManagedCodexImageSessionCapabilitiesUseTransientClientWhileGenerationRuns(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	jobDir := t.TempDir()
	generationClient := &managedCodexClientStub{call: func(ctx context.Context, method string, _ any, _ any) error {
		if method != "thread/start" {
			return errors.New("unexpected method " + method)
		}
		close(started)
		select {
		case <-release:
			return errors.New("generation released")
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	capabilityClient := &managedCodexClientStub{call: func(_ context.Context, method string, _ any, result any) error {
		if method != "modelProvider/capabilities/read" {
			return errors.New("unexpected method " + method)
		}
		capabilities, ok := result.(*codexapp.ModelProviderCapabilities)
		if !ok {
			return errors.New("unexpected capabilities result")
		}
		*capabilities = codexapp.ModelProviderCapabilities{ImageGeneration: true}
		return nil
	}}
	var factoryCalls atomic.Int32
	managed := &managedCodexImageSession{
		parent: context.Background(), gate: newCodexImageFIFO(),
		factory: func(context.Context, context.Context, string) (codexapp.Client, error) {
			switch factoryCalls.Add(1) {
			case 1:
				return generationClient, nil
			case 2:
				return capabilityClient, nil
			default:
				return nil, errors.New("unexpected extra client")
			}
		},
	}
	generationDone := make(chan error, 1)
	go func() {
		_, err := managed.GenerateImage(context.Background(), codexapp.ImageGenerationRequest{JobDir: jobDir, Prompt: "prompt"}, nil)
		generationDone <- err
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	capabilities, err := managed.Capabilities(ctx)
	if err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if !capabilities.ImageGeneration {
		t.Fatalf("Capabilities() = %#v", capabilities)
	}
	if got := capabilityClient.closeCount.Load(); got != 1 {
		t.Fatalf("transient Close() count = %d, want 1", got)
	}
	close(release)
	if err := <-generationDone; err == nil || !strings.Contains(err.Error(), "generation released") {
		t.Fatalf("GenerateImage() error = %v", err)
	}
}

func TestManagedCodexImageSessionGateHonorsDeadlineAndShutdown(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	started := make(chan struct{})
	client := &managedCodexClientStub{call: func(ctx context.Context, method string, _ any, _ any) error {
		if method != "thread/start" {
			return errors.New("unexpected method " + method)
		}
		select {
		case <-started:
		default:
			close(started)
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	managed := &managedCodexImageSession{
		parent:  parent,
		factory: func(context.Context, context.Context, string) (codexapp.Client, error) { return client, nil },
		gate:    newCodexImageFIFO(),
	}
	go managed.closeOnShutdown()
	generateDone := make(chan error, 1)
	go func() {
		_, err := managed.GenerateImage(context.Background(), codexapp.ImageGenerationRequest{JobDir: t.TempDir(), Prompt: "prompt"}, nil)
		generateDone <- err
	}()
	<-started
	deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer deadlineCancel()
	if _, err := managed.GenerateImage(deadlineCtx, codexapp.ImageGenerationRequest{JobDir: t.TempDir(), Prompt: "queued"}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued GenerateImage() error = %v, want deadline exceeded", err)
	}
	cancelParent()
	if err := <-generateDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for client.closeCount.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := client.closeCount.Load(); got != 1 {
		t.Fatalf("Close() count = %d, want 1", got)
	}
}

func TestManagedCodexImageSessionReconnectsReadOnce(t *testing.T) {
	first := &managedCodexClientStub{call: func(context.Context, string, any, any) error { return errors.New("disconnected") }}
	second := &managedCodexClientStub{call: func(_ context.Context, method string, _ any, result any) error {
		if method != "thread/read" {
			return errors.New("unexpected method " + method)
		}
		payload := []byte(`{"thread":{"id":"thread","cwd":"/tmp/job","turns":[]}}`)
		return json.Unmarshal(payload, result)
	}}
	clients := []codexapp.Client{first, second}
	managed := &managedCodexImageSession{
		parent: context.Background(), gate: newCodexImageFIFO(),
		factory: func(context.Context, context.Context, string) (codexapp.Client, error) {
			client := clients[0]
			clients = clients[1:]
			return client, nil
		},
	}
	result, err := managed.ReadImageResult(context.Background(), "thread")
	if err != nil {
		t.Fatalf("ReadImageResult() error = %v", err)
	}
	if result.ThreadID != "thread" {
		t.Fatalf("thread id = %q", result.ThreadID)
	}
	if first.closeCount.Load() != 1 || second.closeCount.Load() != 0 {
		t.Fatalf("close counts = %d/%d", first.closeCount.Load(), second.closeCount.Load())
	}
}

func TestManagedCodexImageSessionColdStartUsesOperationDeadline(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	var parentCanceled bool
	managed := &managedCodexImageSession{
		parent: parent, gate: newCodexImageFIFO(),
		factory: func(processCtx context.Context, initCtx context.Context, _ string) (codexapp.Client, error) {
			<-initCtx.Done()
			parentCanceled = processCtx.Err() != nil
			return nil, initCtx.Err()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := managed.Capabilities(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if time.Since(started) > 250*time.Millisecond {
		t.Fatal("cold start ignored operation deadline")
	}
	if parentCanceled {
		t.Fatal("operation deadline canceled process lifetime parent")
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
	var buffer bytes.Buffer
	value := image.NewRGBA(image.Rect(0, 0, 1, 1))
	value.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	if err := png.Encode(&buffer, value); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

func contains(value string, substring string) bool {
	return len(value) >= len(substring) && (value == substring || containsAt(value, substring))
}

func waitForCodexImageQueueDepth(t *testing.T, queue *codexImageFIFO, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		queue.mu.Lock()
		got := len(queue.waiters)
		queue.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue depth did not reach %d", want)
}

func containsAt(value string, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
