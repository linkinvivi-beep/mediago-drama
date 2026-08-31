package comfyui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewClientAcceptsOnlyBareLoopbackHTTPOrigins(t *testing.T) {
	t.Parallel()

	valid := []struct {
		baseURL string
		want    string
	}{
		{baseURL: "http://127.0.0.1:8188", want: "http://127.0.0.1:8188"},
		{baseURL: "http://localhost:8188", want: "http://localhost:8188"},
		{baseURL: "http://LOCALHOST:8188", want: "http://localhost:8188"},
		{baseURL: "http://[::1]:8188", want: "http://[::1]:8188"},
	}
	for _, test := range valid {
		test := test
		t.Run("accept "+test.baseURL, func(t *testing.T) {
			t.Parallel()
			client, err := NewClient(test.baseURL)
			if err != nil {
				t.Fatalf("NewClient(%q) error = %v", test.baseURL, err)
			}
			if got := client.(*httpClient).baseURL; got != test.want {
				t.Fatalf("NewClient(%q) baseURL = %q, want canonical %q", test.baseURL, got, test.want)
			}
		})
	}

	invalid := []string{
		"",
		"https://127.0.0.1:8188",
		"http://127.0.0.2:8188",
		"http://0.0.0.0:8188",
		"http://[::]:8188",
		"http://localhost.example:8188",
		"http://127.0.0.1",
		"http://127.0.0.1:",
		"http://127.0.0.1:0",
		"http://127.0.0.1:65536",
		"http://127.0.0.1:http",
		"http://127.0.0.1:08188",
		"http://user:secret@127.0.0.1:8188",
		"http://127.0.0.1:8188/",
		"http://127.0.0.1:8188/comfy",
		"http://127.0.0.1:8188?target=public",
		"http://127.0.0.1:8188#fragment",
	}
	for _, baseURL := range invalid {
		baseURL := baseURL
		t.Run("reject "+baseURL, func(t *testing.T) {
			t.Parallel()
			if _, err := NewClient(baseURL); !errors.Is(err, ErrInvalidBaseURL) {
				t.Fatalf("NewClient(%q) error = %v, want ErrInvalidBaseURL", baseURL, err)
			}
		})
	}
}

func TestNewClientIgnoresProxyEnvironmentForLoopback(t *testing.T) {
	var proxyRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		http.Error(writer, "proxy must not receive loopback traffic", http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	t.Setenv("all_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(t, writer, `{"system":{"os":"linux"},"devices":[]}`)
	}))
	defer target.Close()

	client, err := NewClient(target.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	stats, err := client.SystemStats(context.Background())
	if err != nil || stats.System.OS != "linux" {
		t.Fatalf("SystemStats() = (%#v, %v)", stats, err)
	}
	if proxyRequests.Load() != 0 {
		t.Fatalf("proxy received %d requests, want 0", proxyRequests.Load())
	}
}

func TestHTTPClientTypedEndpointContracts(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/system_stats":
			assertMethod(t, request, http.MethodGet)
			writeJSON(t, writer, `{"system":{"os":"linux","comfyui_version":"0.3.75"},"devices":[{"name":"cuda:0","type":"cuda","index":0,"vram_total":24576,"vram_free":12288}]}`)
		case "/object_info":
			assertMethod(t, request, http.MethodGet)
			writeJSON(t, writer, `{"CheckpointLoaderSimple":{"input":{"required":{"ckpt_name":[["model.safetensors"]]}},"output":["MODEL"],"name":"CheckpointLoaderSimple","display_name":"Load Checkpoint","category":"loaders","output_node":false}}`)
		case "/queue":
			assertMethod(t, request, http.MethodGet)
			writeJSON(t, writer, `{"queue_running":[[7,"running-id",{"1":{"class_type":"KSampler"}},{"client_id":"client-1"},["9"]]],"queue_pending":[[8,"pending-id",{"2":{"class_type":"SaveImage"}},{},["2"]]]}`)
		case "/upload/image":
			assertMethod(t, request, http.MethodPost)
			if err := request.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("ParseMultipartForm() error = %v", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			file, header, err := request.FormFile("image")
			if err != nil {
				t.Errorf("FormFile(image) error = %v", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			defer file.Close()
			content, err := io.ReadAll(file)
			if err != nil {
				t.Errorf("ReadAll(image) error = %v", err)
			}
			if header.Filename != "reference.png" || string(content) != "png-data" {
				t.Errorf("uploaded file = (%q, %q)", header.Filename, content)
			}
			if request.FormValue("type") != "input" || request.FormValue("subfolder") != "references" || request.FormValue("overwrite") != "true" {
				t.Errorf("upload fields = %#v", request.MultipartForm.Value)
			}
			writeJSON(t, writer, `{"name":"reference.png","subfolder":"references","type":"input"}`)
		case "/prompt":
			assertMethod(t, request, http.MethodPost)
			var body struct {
				Prompt   json.RawMessage `json:"prompt"`
				ClientID string          `json:"client_id"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("Decode(prompt) error = %v", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			if string(body.Prompt) != `{"1":{"class_type":"KSampler"}}` || body.ClientID != "client-1" {
				t.Errorf("prompt request = %#v", body)
			}
			writeJSON(t, writer, `{"prompt_id":"prompt-1","number":9,"node_errors":{}}`)
		case "/history/prompt-1":
			assertMethod(t, request, http.MethodGet)
			writeJSON(t, writer, `{"prompt-1":{"prompt":[9,"prompt-1",{},{}],"outputs":{"9":{"images":[{"filename":"result.png","subfolder":"jobs","type":"output"}]}},"status":{"status_str":"success","completed":true,"messages":[]}}}`)
		case "/view":
			assertMethod(t, request, http.MethodGet)
			if got := request.URL.Query(); got.Get("filename") != "result image.png" || got.Get("subfolder") != "jobs/1" || got.Get("type") != "output" {
				t.Errorf("view query = %q", request.URL.RawQuery)
			}
			writer.Header().Set("Content-Type", "image/png")
			writer.Header().Set("X-Comfy-Meta", "safe")
			_, _ = io.WriteString(writer, "image-bytes")
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.RequestURI())
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	ctx := context.Background()

	stats, err := client.SystemStats(ctx)
	if err != nil || stats.System.OS != "linux" || stats.System.ComfyUIVersion != "0.3.75" || len(stats.Devices) != 1 || stats.Devices[0].VRAMFree != 12288 {
		t.Fatalf("SystemStats() = (%#v, %v)", stats, err)
	}
	objects, err := client.ObjectInfo(ctx)
	if err != nil || objects["CheckpointLoaderSimple"].DisplayName != "Load Checkpoint" || string(objects["CheckpointLoaderSimple"].Input.Required["ckpt_name"]) != `[["model.safetensors"]]` {
		t.Fatalf("ObjectInfo() = (%#v, %v)", objects, err)
	}
	queue, err := client.Queue(ctx)
	if err != nil || len(queue.Running) != 1 || queue.Running[0].PromptID != "running-id" || queue.Pending[0].PromptID != "pending-id" {
		t.Fatalf("Queue() = (%#v, %v)", queue, err)
	}
	uploaded, err := client.UploadImage(ctx, UploadImageRequest{
		Filename: "reference.png", Content: io.NopCloser(strings.NewReader("png-data")), Size: 8,
		Type: "input", Subfolder: "references", Overwrite: true,
	})
	if err != nil || uploaded.Name != "reference.png" || uploaded.Subfolder != "references" || uploaded.Type != "input" {
		t.Fatalf("UploadImage() = (%#v, %v)", uploaded, err)
	}
	submission, err := client.SubmitPrompt(ctx, json.RawMessage(`{"1":{"class_type":"KSampler"}}`), "client-1")
	if err != nil || submission.PromptID != "prompt-1" || submission.Number != 9 {
		t.Fatalf("SubmitPrompt() = (%#v, %v)", submission, err)
	}
	history, err := client.History(ctx, "prompt-1")
	if err != nil || history.PromptID != "prompt-1" || !history.Status.Completed || history.Outputs["9"].Images[0].Filename != "result.png" {
		t.Fatalf("History() = (%#v, %v)", history, err)
	}
	view, headers, err := client.View(ctx, OutputFile{Filename: "result image.png", Subfolder: "jobs/1", Type: "output"})
	if err != nil {
		t.Fatalf("View() error = %v", err)
	}
	defer view.Close()
	image, err := io.ReadAll(view)
	if err != nil || string(image) != "image-bytes" || headers.Get("Content-Type") != "image/png" || headers.Get("X-Comfy-Meta") != "safe" {
		t.Fatalf("View() = (%q, %#v, %v)", image, headers, err)
	}
}

func TestDeleteQueuedPromptSendsOnlyExactPromptID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assertMethod(t, request, http.MethodPost)
		if request.URL.Path != "/queue" {
			t.Errorf("path = %q, want /queue", request.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("Decode(queue delete) error = %v", err)
		}
		if len(body) != 1 || string(body["delete"]) != `["prompt-exact"]` {
			t.Errorf("queue delete body = %s", body)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	deleted, err := newTestClient(t, server).DeleteQueuedPrompt(context.Background(), "prompt-exact")
	if err != nil || !deleted {
		t.Fatalf("DeleteQueuedPrompt() = (%v, %v), want (true, nil)", deleted, err)
	}
}

func TestHTTPClientRejectsOversizedJSONResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.CopyN(writer, zeroReader{}, maxJSONResponseBytes+1)
	}))
	defer server.Close()

	_, err := newTestClient(t, server).SystemStats(context.Background())
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("SystemStats() error = %v, want ErrResponseTooLarge", err)
	}
}

func TestHTTPClientBoundsAndRedactsNon2xxBody(t *testing.T) {
	t.Parallel()

	t.Run("small secret body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, "credential=do-not-leak")
		}))
		defer server.Close()

		_, err := newTestClient(t, server).SystemStats(context.Background())
		var statusErr *HTTPStatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusBadGateway {
			t.Fatalf("SystemStats() error = %v, want status 502", err)
		}
		if strings.Contains(err.Error(), "credential") || strings.Contains(err.Error(), "do-not-leak") || strings.Contains(err.Error(), server.URL) {
			t.Fatalf("error leaked response or URL: %v", err)
		}
	})

	t.Run("oversized error body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = io.CopyN(writer, zeroReader{}, maxErrorResponseBytes+1)
		}))
		defer server.Close()

		_, err := newTestClient(t, server).SystemStats(context.Background())
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("SystemStats() error = %v, want ErrResponseTooLarge", err)
		}
	})
}

func TestHTTPClientRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(t, writer, `{"system":`)
	}))
	defer server.Close()

	_, err := newTestClient(t, server).SystemStats(context.Background())
	if err == nil || strings.Contains(err.Error(), server.URL) || !strings.Contains(err.Error(), "malformed JSON") {
		t.Fatalf("SystemStats() error = %v, want stable malformed JSON error", err)
	}
}

func TestHTTPClientDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/system_stats", http.StatusFound)
	}))
	defer origin.Close()

	_, err := newTestClient(t, origin).SystemStats(context.Background())
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusFound {
		t.Fatalf("SystemStats() error = %v, want redirect status error", err)
	}
	if redirected.Load() != 0 {
		t.Fatalf("redirect target received %d requests, want 0", redirected.Load())
	}
}

func TestHTTPClientPropagatesContextCancellation(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := newTestClient(t, server).SystemStats(ctx)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("SystemStats() error = %v, want context.Canceled", err)
	}
}

func TestUploadImageStreamsAndRejectsOversizedInput(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		reader, err := request.MultipartReader()
		if err != nil {
			t.Errorf("MultipartReader() error = %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		for {
			part, err := reader.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, part)
			_ = part.Close()
		}
		writeJSON(t, writer, `{"name":"too-large.png","subfolder":"","type":"input"}`)
	}))
	defer server.Close()

	_, err := newTestClient(t, server).UploadImage(context.Background(), UploadImageRequest{
		Filename: "too-large.png",
		Content:  io.NopCloser(io.LimitReader(zeroReader{}, maxUploadImageBytes+1)),
		Size:     -1,
		Type:     "input",
	})
	if !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("UploadImage() error = %v, want ErrUploadTooLarge", err)
	}
}

func TestUploadImageCancellationClosesOwnedBlockingContent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	content := newCloseBlockingReader()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := newTestClient(t, server).UploadImage(ctx, UploadImageRequest{
			Filename: "blocked.png",
			Content:  content,
			Size:     -1,
			Type:     "input",
		})
		done <- err
	}()
	waitForSignal(t, content.started, "upload reader to start")
	cancel()

	err := waitForUploadResult(t, done, content)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("UploadImage() error = %v, want context.Canceled", err)
	}
	if content.closeCalls.Load() != 1 {
		t.Fatalf("content Close calls = %d, want 1", content.closeCalls.Load())
	}
	waitForSignal(t, content.readExited, "owned reader Read to exit")
}

func TestUploadImageEarlyConnectionFailureClosesOwnedContent(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	baseURL := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}
	client, err := NewClient(baseURL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	content := newCloseBlockingReader()
	done := make(chan error, 1)
	go func() {
		_, err := client.UploadImage(context.Background(), UploadImageRequest{
			Filename: "blocked.png",
			Content:  content,
			Size:     -1,
			Type:     "input",
		})
		done <- err
	}()

	err = waitForUploadResult(t, done, content)
	if err == nil {
		t.Fatal("UploadImage() error = nil, want connection failure")
	}
	if content.closeCalls.Load() != 1 {
		t.Fatalf("content Close calls = %d, want 1", content.closeCalls.Load())
	}
}

func TestSubmitPromptFailsClosedWhenOutcomeIsUnknown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "success body has no prompt id",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writeJSON(t, writer, `{"number":3,"node_errors":{}}`)
			},
		},
		{
			name: "success body is malformed",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writeJSON(t, writer, `{"prompt_id":`)
			},
		},
		{
			name: "success body is oversized",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.CopyN(writer, zeroReader{}, maxJSONResponseBytes+1)
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(test.handler)
			defer server.Close()

			_, err := newTestClient(t, server).SubmitPrompt(context.Background(), json.RawMessage(`{"1":{"class_type":"KSampler"}}`), "client-1")
			if !errors.Is(err, ErrSubmissionOutcomeUnknown) {
				t.Fatalf("SubmitPrompt() error = %v, want ErrSubmissionOutcomeUnknown", err)
			}
			if strings.Contains(err.Error(), server.URL) {
				t.Fatalf("error leaked URL: %v", err)
			}
		})
	}
}

func TestSubmitPromptAmbiguousHTTPStatusesAreUnknown(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusInternalServerError,
		http.StatusServiceUnavailable,
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusFound,
	} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(status)
				writeJSON(t, writer, `{"error":"not accepted"}`)
			}))
			defer server.Close()

			_, err := submitTestPrompt(t, server.URL, context.Background())
			if !errors.Is(err, ErrSubmissionOutcomeUnknown) {
				t.Fatalf("SubmitPrompt() error = %v, want ErrSubmissionOutcomeUnknown", err)
			}
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) || statusErr.StatusCode != status {
				t.Fatalf("SubmitPrompt() error = %v, want preserved status %d", err, status)
			}
		})
	}
}

func TestSubmitPromptOnlyTreatsParseableExplicit4xxAsDefinite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		wantUnknown bool
	}{
		{name: "parseable", body: `{"error":"invalid prompt"}`, wantUnknown: false},
		{name: "malformed", body: `not-json`, wantUnknown: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()

			_, err := submitTestPrompt(t, server.URL, context.Background())
			if errors.Is(err, ErrSubmissionOutcomeUnknown) != test.wantUnknown {
				t.Fatalf("SubmitPrompt() error = %v, unknown = %v, want %v", err, errors.Is(err, ErrSubmissionOutcomeUnknown), test.wantUnknown)
			}
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusBadRequest {
				t.Fatalf("SubmitPrompt() error = %v, want preserved status 400", err)
			}
		})
	}
}

func TestSubmitPromptResponseFailuresAreUnknownAndPreserveCause(t *testing.T) {
	t.Parallel()

	t.Run("oversized error body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = io.CopyN(writer, zeroReader{}, maxErrorResponseBytes+1)
		}))
		defer server.Close()

		_, err := submitTestPrompt(t, server.URL, context.Background())
		if !errors.Is(err, ErrSubmissionOutcomeUnknown) || !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("SubmitPrompt() error = %v, want unknown and response-too-large", err)
		}
		var statusErr *HTTPStatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusInternalServerError {
			t.Fatalf("SubmitPrompt() error = %v, want preserved status 500", err)
		}
	})

	t.Run("truncated success body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Length", "100")
			writer.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(writer, `{"prompt_id":"truncated"`)
		}))
		defer server.Close()

		_, err := submitTestPrompt(t, server.URL, context.Background())
		if !errors.Is(err, ErrSubmissionOutcomeUnknown) || !strings.Contains(err.Error(), "could not be read") {
			t.Fatalf("SubmitPrompt() error = %v, want unknown with stable read failure", err)
		}
	})

	t.Run("malformed success body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writeJSON(t, writer, `{"prompt_id":`)
		}))
		defer server.Close()

		_, err := submitTestPrompt(t, server.URL, context.Background())
		if !errors.Is(err, ErrSubmissionOutcomeUnknown) || !strings.Contains(err.Error(), "malformed JSON") {
			t.Fatalf("SubmitPrompt() error = %v, want unknown with malformed JSON cause", err)
		}
	})

	t.Run("oversized success body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusOK)
			_, _ = io.CopyN(writer, zeroReader{}, maxJSONResponseBytes+1)
		}))
		defer server.Close()

		_, err := submitTestPrompt(t, server.URL, context.Background())
		if !errors.Is(err, ErrSubmissionOutcomeUnknown) || !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("SubmitPrompt() error = %v, want unknown and response-too-large", err)
		}
	})

	t.Run("stalled error body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusServiceUnavailable)
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := submitTestPrompt(t, server.URL, ctx)
		if !errors.Is(err, ErrSubmissionOutcomeUnknown) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("SubmitPrompt() error = %v, want unknown and context deadline", err)
		}
		var statusErr *HTTPStatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("SubmitPrompt() error = %v, want preserved status 503", err)
		}
	})
}

func TestSubmitPromptRedirectIsUnknownAndNotFollowed(t *testing.T) {
	t.Parallel()

	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/prompt", http.StatusFound)
	}))
	defer origin.Close()

	_, err := submitTestPrompt(t, origin.URL, context.Background())
	if !errors.Is(err, ErrSubmissionOutcomeUnknown) {
		t.Fatalf("SubmitPrompt() error = %v, want ErrSubmissionOutcomeUnknown", err)
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusFound {
		t.Fatalf("SubmitPrompt() error = %v, want preserved status 302", err)
	}
	if redirected.Load() != 0 {
		t.Fatalf("redirect target received %d requests, want 0", redirected.Load())
	}
}

func TestSubmitPromptCanceledBeforeRequestIsNotUnknown(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeJSON(t, writer, `{"prompt_id":"unexpected"}`)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newTestClient(t, server).SubmitPrompt(ctx, json.RawMessage(`{"1":{"class_type":"KSampler"}}`), "client-1")
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrSubmissionOutcomeUnknown) {
		t.Fatalf("SubmitPrompt() error = %v, want only context.Canceled", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("server received %d requests, want 0", requests.Load())
	}
}

func TestHistoryRequiresExactPromptEntry(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(t, writer, `{"different-id":{"outputs":{},"status":{"completed":true}}}`)
	}))
	defer server.Close()

	_, err := newTestClient(t, server).History(context.Background(), "wanted-id")
	if err == nil || !strings.Contains(err.Error(), "did not include requested prompt") {
		t.Fatalf("History() error = %v, want exact-entry error", err)
	}
}

func TestHistoryRejectsPromptIDPathTraversalBeforeRequest(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeJSON(t, writer, `{}`)
	}))
	defer server.Close()

	_, err := newTestClient(t, server).History(context.Background(), "..")
	if err == nil {
		t.Fatal("History(..) error = nil, want identifier validation error")
	}
	if requests.Load() != 0 {
		t.Fatalf("server received %d requests, want 0", requests.Load())
	}
}

func newTestClient(t *testing.T, server *httptest.Server) Client {
	t.Helper()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func submitTestPrompt(t *testing.T, baseURL string, ctx context.Context) (PromptSubmission, error) {
	t.Helper()
	client, err := NewClient(baseURL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client.SubmitPrompt(ctx, json.RawMessage(`{"1":{"class_type":"KSampler"}}`), "client-1")
}

func assertMethod(t *testing.T, request *http.Request, want string) {
	t.Helper()
	if request.Method != want {
		t.Errorf("method = %q, want %q", request.Method, want)
	}
}

func writeJSON(t *testing.T, writer http.ResponseWriter, raw string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(writer, raw); err != nil {
		t.Errorf("WriteString() error = %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 'x'
	}
	return len(buffer), nil
}

type closeBlockingReader struct {
	started    chan struct{}
	readExited chan struct{}
	closed     chan struct{}
	startOnce  sync.Once
	exitOnce   sync.Once
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

func newCloseBlockingReader() *closeBlockingReader {
	return &closeBlockingReader{
		started:    make(chan struct{}),
		readExited: make(chan struct{}),
		closed:     make(chan struct{}),
	}
}

func (reader *closeBlockingReader) Read([]byte) (int, error) {
	reader.startOnce.Do(func() { close(reader.started) })
	<-reader.closed
	reader.exitOnce.Do(func() { close(reader.readExited) })
	return 0, errors.New("reader closed")
}

func (reader *closeBlockingReader) Close() error {
	reader.closeCalls.Add(1)
	reader.closeOnce.Do(func() { close(reader.closed) })
	return nil
}

func waitForUploadResult(t *testing.T, done <-chan error, content *closeBlockingReader) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		_ = content.Close()
		err := <-done
		t.Fatalf("UploadImage() did not return after cancellation/failure; eventual error = %v", err)
		return nil
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
