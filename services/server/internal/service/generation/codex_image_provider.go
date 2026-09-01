package generation

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/codexapp"
	"golang.org/x/sys/unix"
)

const (
	codexImageTaskIDRequestOption = "_medialink_task_id"
	codexImageResponseIDPrefix    = coregeneration.RouteCodexImage + ":"
	codexImageInternalPayloadKey  = "_medialink_internal_codex_image_payload"

	// Bounds are intentionally generous for production Codex images while
	// preventing unbounded allocations from untrusted references/results.
	maxCodexImageReferences          = 10
	maxCodexImageReferenceBytes      = 20 << 20
	maxCodexImageTotalReferenceBytes = 80 << 20
	maxCodexImageOutputBytes         = 64 << 20
	maxCodexImageDimension           = 16384
	maxCodexImagePixels              = 40_000_000
)

var safeCodexImageTaskID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var safeCodexImageAttemptID = regexp.MustCompile(`^attempt-[a-f0-9]{32}$`)

// CodexImageSession is the typed app-server surface required by the image provider.
type CodexImageSession interface {
	Capabilities(context.Context) (codexapp.ModelProviderCapabilities, error)
	GenerateImage(context.Context, codexapp.ImageGenerationRequest, func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error)
	ReadImageResult(context.Context, string) (codexapp.ImageGenerationResult, error)
}

// CodexImageProvider executes all Codex image turns through one strict FIFO gate.
type CodexImageProvider struct {
	session CodexImageSession
	root    string
	queue   *codexImageFIFO
}

type managedCodexImageSession struct {
	parent  context.Context
	binPath string
	factory func(context.Context, context.Context, string) (codexapp.Client, error)
	gate    *codexImageFIFO
	stateMu sync.Mutex
	client  codexapp.Client
	typed   *codexapp.ImageGenerationSession
}

type codexImageWaiter struct {
	ready   chan struct{}
	granted bool
}

// codexImageFIFO is a context-aware, strict FIFO one-operation gate.
type codexImageFIFO struct {
	mu      sync.Mutex
	active  bool
	waiters []*codexImageWaiter
}

func newCodexImageFIFO() *codexImageFIFO { return &codexImageFIFO{} }

func (queue *codexImageFIFO) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	waiter := &codexImageWaiter{ready: make(chan struct{})}
	queue.mu.Lock()
	if !queue.active && len(queue.waiters) == 0 {
		if err := ctx.Err(); err != nil {
			queue.mu.Unlock()
			return err
		}
		queue.active = true
		queue.mu.Unlock()
		return nil
	}
	queue.waiters = append(queue.waiters, waiter)
	queue.mu.Unlock()

	select {
	case <-waiter.ready:
		return nil
	case <-ctx.Done():
		queue.mu.Lock()
		if waiter.granted {
			queue.mu.Unlock()
			queue.release()
			return ctx.Err()
		}
		for index, candidate := range queue.waiters {
			if candidate == waiter {
				queue.waiters = append(queue.waiters[:index], queue.waiters[index+1:]...)
				break
			}
		}
		queue.mu.Unlock()
		return ctx.Err()
	}
}

func (queue *codexImageFIFO) release() {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.waiters) == 0 {
		queue.active = false
		return
	}
	next := queue.waiters[0]
	queue.waiters = queue.waiters[1:]
	next.granted = true
	close(next.ready)
}

// NewCodexImageProvider creates one application-scoped Codex image provider.
// dataRoot is the MediaLink-owned user-data directory, not an arbitrary output path.
func NewCodexImageProvider(session CodexImageSession, dataRoot string) *CodexImageProvider {
	return &CodexImageProvider{
		session: session,
		root:    filepath.Join(filepath.Clean(strings.TrimSpace(dataRoot)), "generation", "codex-image"),
		queue:   newCodexImageFIFO(),
	}
}

// NewManagedCodexImageProvider creates the application singleton. Its app-server
// process starts lazily and is canceled with parent. A failed read reconnects once;
// GenerateImage is never retried because that could duplicate a turn.
func NewManagedCodexImageProvider(parent context.Context, binPath string, dataRoot string) *CodexImageProvider {
	if parent == nil {
		parent = context.Background()
	}
	managed := &managedCodexImageSession{
		parent:  parent,
		binPath: strings.TrimSpace(binPath),
		factory: func(parent context.Context, initCtx context.Context, path string) (codexapp.Client, error) {
			return codexapp.StartWithInitContext(parent, initCtx, path)
		},
		gate: newCodexImageFIFO(),
	}
	if parent.Done() != nil {
		go managed.closeOnShutdown()
	}
	return NewCodexImageProvider(managed, dataRoot)
}

// Ready checks the exact capability used by generation without submitting a turn.
func (provider *CodexImageProvider) Ready(ctx context.Context) (bool, string) {
	if provider == nil || provider.session == nil {
		return false, "Codex image provider is not configured"
	}
	capabilities, err := provider.session.Capabilities(ctx)
	if err != nil {
		return false, fmt.Sprintf("Codex image preflight failed: %v", err)
	}
	if !capabilities.ImageGeneration {
		return false, "Codex image generation capability is unavailable"
	}
	return true, ""
}

func (*CodexImageProvider) Name() string { return "Codex Image" }

func (session *managedCodexImageSession) Capabilities(ctx context.Context) (codexapp.ModelProviderCapabilities, error) {
	ctx, cancel := session.operationContext(ctx)
	defer cancel()
	client, err := session.factory(session.parent, ctx, session.binPath)
	if err != nil {
		return codexapp.ModelProviderCapabilities{}, err
	}
	defer client.Close()
	return codexapp.ReadModelProviderCapabilities(ctx, client)
}

func (session *managedCodexImageSession) GenerateImage(ctx context.Context, request codexapp.ImageGenerationRequest, checkpoint func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
	ctx, cancel := session.operationContext(ctx)
	defer cancel()
	if err := session.gate.acquire(ctx); err != nil {
		return codexapp.ImageGenerationResult{}, err
	}
	defer session.gate.release()
	typed, err := session.ensure(ctx)
	if err != nil {
		return codexapp.ImageGenerationResult{}, err
	}
	result, err := typed.GenerateImage(ctx, request, checkpoint)
	if err != nil {
		session.invalidate()
	}
	return result, err
}

func (session *managedCodexImageSession) ReadImageResult(ctx context.Context, threadID string) (codexapp.ImageGenerationResult, error) {
	ctx, cancel := session.operationContext(ctx)
	defer cancel()
	if err := session.gate.acquire(ctx); err != nil {
		return codexapp.ImageGenerationResult{}, err
	}
	defer session.gate.release()
	typed, err := session.ensure(ctx)
	if err != nil {
		return codexapp.ImageGenerationResult{}, err
	}
	result, err := typed.ReadImageResult(ctx, threadID)
	if err == nil || ctx.Err() != nil {
		return result, err
	}
	session.invalidate()
	typed, err = session.ensure(ctx)
	if err != nil {
		return codexapp.ImageGenerationResult{}, err
	}
	return typed.ReadImageResult(ctx, threadID)
}

func (session *managedCodexImageSession) ensure(initCtx context.Context) (*codexapp.ImageGenerationSession, error) {
	session.stateMu.Lock()
	if session.typed != nil {
		typed := session.typed
		session.stateMu.Unlock()
		return typed, nil
	}
	session.stateMu.Unlock()
	client, err := session.factory(session.parent, initCtx, session.binPath)
	if err != nil {
		return nil, err
	}
	session.stateMu.Lock()
	session.client = client
	session.typed = codexapp.NewImageGenerationSession(client)
	typed := session.typed
	session.stateMu.Unlock()
	return typed, nil
}

func (session *managedCodexImageSession) invalidate() {
	session.stateMu.Lock()
	client := session.client
	session.client = nil
	session.typed = nil
	session.stateMu.Unlock()
	if client != nil {
		client.Close()
	}
}

func (session *managedCodexImageSession) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	operationCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(session.parent, cancel)
	return operationCtx, func() { stop(); cancel() }
}

func (session *managedCodexImageSession) closeOnShutdown() {
	<-session.parent.Done()
	_ = session.gate.acquire(context.Background())
	session.invalidate()
	session.gate.release()
}

// Generate creates one isolated app-owned job directory and runs one structured
// built-in image generation turn.
func (provider *CodexImageProvider) Generate(ctx context.Context, request coregeneration.Request) (coregeneration.Response, error) {
	if provider == nil || provider.session == nil {
		return coregeneration.Response{}, fmt.Errorf("Codex image session is not configured")
	}
	if err := ctx.Err(); err != nil {
		return coregeneration.Response{}, err
	}
	if request.Kind != "" && request.Kind != coregeneration.KindImage {
		return coregeneration.Response{}, fmt.Errorf("Codex image provider only supports image generation")
	}
	if routeID := strings.TrimSpace(request.RouteID); routeID != "" && routeID != coregeneration.RouteCodexImage {
		return coregeneration.Response{}, fmt.Errorf("Codex image provider does not support route %q", routeID)
	}
	taskID, err := codexImageTaskID(request)
	if err != nil {
		return coregeneration.Response{}, err
	}

	if err := provider.queue.acquire(ctx); err != nil {
		return coregeneration.Response{}, err
	}
	defer provider.queue.release()

	capabilities, err := provider.session.Capabilities(ctx)
	if err != nil {
		return coregeneration.Response{}, fmt.Errorf("reading Codex image capabilities: %w", err)
	}
	if !capabilities.ImageGeneration {
		return coregeneration.Response{}, fmt.Errorf("Codex image generation capability is unavailable")
	}

	jobDir, err := provider.createJobDir(taskID)
	if err != nil {
		return coregeneration.Response{}, err
	}
	referencePaths, err := provider.materializeReferences(request.ReferenceURLs, jobDir)
	if err != nil {
		return coregeneration.Response{}, err
	}

	state := GenerationTaskRuntimeState{}
	var checkpointItem *codexapp.ImageGenerationThreadItem
	turnCompleted := false
	emit := func(status string) {
		callback, ok := coregeneration.ProgressCallbackFromOptions(request.Options)
		if !ok {
			return
		}
		callback(ctx, coregeneration.ProgressEvent{Response: codexImageProgressResponse(request.Model, status, state)})
	}
	result, generateErr := provider.session.GenerateImage(ctx, codexapp.ImageGenerationRequest{
		JobDir:         jobDir,
		Prompt:         strings.TrimSpace(request.Prompt),
		ReferencePaths: referencePaths,
	}, func(checkpoint codexapp.ImageGenerationCheckpoint) {
		applyCodexImageCheckpoint(&state, checkpoint)
		if checkpoint.Item != nil {
			item := *checkpoint.Item
			checkpointItem = &item
		}
		if checkpoint.Stage == codexapp.ImageGenerationStageTurnCompleted {
			turnCompleted = true
		}
		switch checkpoint.Stage {
		case codexapp.ImageGenerationStageItemCompleted, codexapp.ImageGenerationStageTurnCompleted:
			emit("importing")
		default:
			emit("running")
		}
	})
	if generateErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return coregeneration.Response{}, ctxErr
		}
		if checkpointItem != nil && (checkpointItem.Failure != nil || strings.EqualFold(checkpointItem.Status, "failed")) {
			return coregeneration.Response{}, codexImageFailure(*checkpointItem)
		}
		if turnCompleted {
			return coregeneration.Response{}, generateErr
		}
		if state.CodexThreadID != "" {
			return codexImageProgressResponse(request.Model, "waiting_reconnect", state), nil
		}
		return coregeneration.Response{}, generateErr
	}
	applyCodexImageResult(&state, result)
	response, err := provider.responseForResult(request.Model, result, jobDir, state)
	if err != nil {
		return coregeneration.Response{}, err
	}
	if callback, ok := coregeneration.ProgressCallbackFromOptions(request.Options); ok {
		callback(ctx, coregeneration.ProgressEvent{Response: response, Completed: 1, Total: 1})
	}
	return response, nil
}

// Get reads an existing thread and never starts another turn.
func (provider *CodexImageProvider) Get(ctx context.Context, id string) (coregeneration.Response, error) {
	if provider == nil || provider.session == nil {
		return coregeneration.Response{}, fmt.Errorf("Codex image session is not configured")
	}
	id = strings.TrimSpace(id)
	if !strings.HasPrefix(id, codexImageResponseIDPrefix) {
		return coregeneration.Response{}, fmt.Errorf("invalid Codex image response id")
	}
	threadID := strings.TrimSpace(strings.TrimPrefix(id, codexImageResponseIDPrefix))
	if threadID == "" || strings.ContainsAny(threadID, `/\\`) {
		return coregeneration.Response{}, fmt.Errorf("invalid Codex image thread id")
	}
	result, err := provider.session.ReadImageResult(ctx, threadID)
	if err != nil {
		return coregeneration.Response{}, err
	}
	state := GenerationTaskRuntimeState{}
	applyCodexImageResult(&state, result)
	if !codexImageItemCompleted(result.Item) {
		if result.Item.Failure != nil || strings.EqualFold(result.Item.Status, "failed") {
			return coregeneration.Response{}, codexImageFailure(result.Item)
		}
		return codexImageProgressResponse("", "waiting_reconnect", state), nil
	}
	jobDir, err := provider.recoveredJobDir(result.JobDir)
	if err != nil {
		return coregeneration.Response{}, err
	}
	return provider.responseForResult("", result, jobDir, state)
}

func codexImageTaskID(request coregeneration.Request) (string, error) {
	value, _ := request.Options[codexImageTaskIDRequestOption].(string)
	value = strings.TrimSpace(value)
	if !safeCodexImageTaskID.MatchString(value) || value == "." || value == ".." {
		return "", fmt.Errorf("Codex image task id is missing or unsafe")
	}
	return value, nil
}

func requestWithCodexImageTaskID(request coregeneration.Request, taskID string) coregeneration.Request {
	next := make(map[string]any, len(request.Options)+1)
	for key, value := range request.Options {
		next[key] = value
	}
	next[codexImageTaskIDRequestOption] = strings.TrimSpace(taskID)
	request.Options = next
	return request
}

func (provider *CodexImageProvider) createJobDir(taskID string) (string, error) {
	dataRoot := filepath.Dir(filepath.Dir(provider.root))
	if dataRoot == "." || !filepath.IsAbs(dataRoot) {
		return "", fmt.Errorf("MediaLink user data directory must be absolute")
	}
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return "", fmt.Errorf("creating MediaLink user data directory: %w", err)
	}
	canonicalDataRoot, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		return "", fmt.Errorf("canonicalizing MediaLink user data directory: %w", err)
	}
	if err := os.MkdirAll(provider.root, 0o700); err != nil {
		return "", fmt.Errorf("creating Codex image jobs directory: %w", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(provider.root)
	if err != nil {
		return "", fmt.Errorf("canonicalizing Codex image jobs directory: %w", err)
	}
	if err := requirePathWithin(canonicalDataRoot, canonicalRoot, "MediaLink user data directory"); err != nil {
		return "", err
	}
	taskDir := filepath.Join(canonicalRoot, taskID)
	if info, statErr := os.Lstat(taskDir); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("Codex image task directory collides with a non-directory")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("checking Codex image task directory: %w", statErr)
	} else if err := os.Mkdir(taskDir, 0o700); err != nil {
		return "", fmt.Errorf("creating Codex image task directory: %w", err)
	}
	canonicalTaskDir, err := filepath.EvalSymlinks(taskDir)
	if err != nil {
		return "", fmt.Errorf("canonicalizing Codex image task directory: %w", err)
	}
	if err := requirePathWithin(canonicalRoot, canonicalTaskDir, "Codex image jobs directory"); err != nil {
		return "", err
	}
	for attempt := 0; attempt < 4; attempt++ {
		suffix, randomErr := codexImageRandomHex(16)
		if randomErr != nil {
			return "", fmt.Errorf("creating Codex image attempt id: %w", randomErr)
		}
		jobDir := filepath.Join(canonicalTaskDir, "attempt-"+suffix)
		if mkdirErr := os.Mkdir(jobDir, 0o700); mkdirErr == nil {
			return jobDir, nil
		} else if !errors.Is(mkdirErr, os.ErrExist) {
			return "", fmt.Errorf("creating Codex image attempt directory: %w", mkdirErr)
		}
	}
	return "", fmt.Errorf("creating unique Codex image attempt directory")
}

func (provider *CodexImageProvider) recoveredJobDir(value string) (string, error) {
	value = filepath.Clean(strings.TrimSpace(value))
	if value == "." || !filepath.IsAbs(value) {
		return "", fmt.Errorf("Codex image thread job directory is missing or not absolute")
	}
	root := filepath.Clean(strings.TrimSpace(provider.root))
	if root == "." || !filepath.IsAbs(root) {
		return "", fmt.Errorf("Codex image jobs directory must be absolute")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("canonicalizing Codex image jobs directory: %w", err)
	}
	relativeInput, err := filepath.Rel(root, value)
	if err != nil || filepath.IsAbs(relativeInput) || relativeInput == ".." || strings.HasPrefix(relativeInput, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("Codex image path is outside Codex image jobs directory")
	}
	canonicalCandidate := filepath.Join(canonicalRoot, relativeInput)
	canonicalJobDir, err := filepath.EvalSymlinks(canonicalCandidate)
	if err != nil {
		return "", fmt.Errorf("canonicalizing Codex image thread job directory: %w", err)
	}
	if canonicalJobDir != canonicalCandidate {
		return "", fmt.Errorf("Codex image thread cwd must not contain symlinks")
	}
	if err := requirePathWithin(canonicalRoot, canonicalJobDir, "Codex image jobs directory"); err != nil {
		return "", err
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalJobDir)
	parts := strings.Split(relative, string(filepath.Separator))
	if err != nil || relative == "." || filepath.IsAbs(relative) || len(parts) != 2 || !safeCodexImageTaskID.MatchString(parts[0]) || !safeCodexImageAttemptID.MatchString(parts[1]) {
		return "", fmt.Errorf("Codex image thread cwd must be an exact task attempt directory")
	}
	info, err := os.Stat(canonicalJobDir)
	if err != nil {
		return "", fmt.Errorf("checking Codex image thread job directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("Codex image thread cwd is not a directory")
	}
	return canonicalJobDir, nil
}

func (provider *CodexImageProvider) materializeReferences(values []string, jobDir string) ([]string, error) {
	if len(values) > maxCodexImageReferences {
		return nil, fmt.Errorf("Codex image reference count exceeds %d", maxCodexImageReferences)
	}
	paths := make([]string, 0, len(values))
	totalBytes := int64(0)
	dataRoot := filepath.Dir(filepath.Dir(provider.root))
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return nil, fmt.Errorf("creating MediaLink user data directory: %w", err)
	}
	for index, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(value), "data:") {
			data, mimeType, materializeErr := decodeCodexImageDataReference(value, index, maxCodexImageReferenceBytes)
			if materializeErr != nil {
				return nil, materializeErr
			}
			totalBytes += int64(len(data))
			if totalBytes > maxCodexImageTotalReferenceBytes {
				return nil, fmt.Errorf("Codex image total reference bytes exceed %d", maxCodexImageTotalReferenceBytes)
			}
			path, materializeErr := materializeCodexImageReference(data, mimeType, jobDir, index)
			if materializeErr != nil {
				return nil, materializeErr
			}
			paths = append(paths, path)
			continue
		}
		if strings.HasPrefix(strings.ToLower(value), "file://") {
			parsed, parseErr := url.Parse(value)
			if parseErr != nil || parsed.Host != "" {
				return nil, fmt.Errorf("Codex image reference %d is not a valid local file", index+1)
			}
			value = parsed.Path
		}
		if !filepath.IsAbs(value) {
			return nil, fmt.Errorf("Codex image reference %d must be a validated local image", index+1)
		}
		_, mimeType, data, validateErr := readValidatedCodexImage(value, dataRoot, maxCodexImageReferenceBytes, "MediaLink user data directory")
		if validateErr != nil {
			return nil, fmt.Errorf("Codex image reference %d: %w", index+1, validateErr)
		}
		totalBytes += int64(len(data))
		if totalBytes > maxCodexImageTotalReferenceBytes {
			return nil, fmt.Errorf("Codex image total reference bytes exceed %d", maxCodexImageTotalReferenceBytes)
		}
		path, materializeErr := materializeCodexImageReference(data, mimeType, jobDir, index)
		if materializeErr != nil {
			return nil, materializeErr
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func decodeCodexImageDataReference(value string, index int, maximum int64) ([]byte, string, error) {
	header, payload, ok := strings.Cut(value, ",")
	if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return nil, "", fmt.Errorf("Codex image reference %d must be a base64 image data URI", index+1)
	}
	mimeType := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")))
	_, ok = codexImageExtension(mimeType)
	if !ok {
		return nil, "", fmt.Errorf("Codex image reference %d has unsupported MIME type %q", index+1, mimeType)
	}
	decodedLength := base64.StdEncoding.DecodedLen(len(payload))
	if strings.HasSuffix(payload, "==") {
		decodedLength -= 2
	} else if strings.HasSuffix(payload, "=") {
		decodedLength--
	}
	if int64(decodedLength) > maximum {
		return nil, "", fmt.Errorf("Codex image reference %d exceeds %d bytes", index+1, maximum)
	}
	reader := io.LimitReader(base64.NewDecoder(base64.StdEncoding, strings.NewReader(payload)), maximum+1)
	data, err := io.ReadAll(reader)
	if err != nil || len(data) == 0 {
		return nil, "", fmt.Errorf("Codex image reference %d has invalid base64 data", index+1)
	}
	if int64(len(data)) > maximum {
		return nil, "", fmt.Errorf("Codex image reference %d exceeds %d bytes", index+1, maximum)
	}
	detected, err := validateCodexImageBytes(data)
	if err != nil {
		return nil, "", fmt.Errorf("Codex image reference %d: %w", index+1, err)
	}
	if detected != mimeType {
		return nil, "", fmt.Errorf("Codex image reference %d MIME mismatch: detected %q", index+1, detected)
	}
	return data, mimeType, nil
}

func materializeCodexImageReference(data []byte, mimeType string, jobDir string, index int) (string, error) {
	extension, _ := codexImageExtension(mimeType)
	suffix, err := codexImageRandomHex(12)
	if err != nil {
		return "", fmt.Errorf("materializing Codex image reference %d: %w", index+1, err)
	}
	path := filepath.Join(jobDir, fmt.Sprintf("reference-%03d-%s%s", index+1, suffix, extension))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("materializing Codex image reference %d: %w", index+1, err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return "", fmt.Errorf("materializing Codex image reference %d: %w", index+1, writeErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("closing Codex image reference %d: %w", index+1, closeErr)
	}
	return path, nil
}

func (provider *CodexImageProvider) responseForResult(model string, result codexapp.ImageGenerationResult, allowedRoot string, state GenerationTaskRuntimeState) (coregeneration.Response, error) {
	if result.Item.Failure != nil || strings.EqualFold(result.Item.Status, "failed") {
		return coregeneration.Response{}, codexImageFailure(result.Item)
	}
	if !codexImageItemCompleted(result.Item) {
		return codexImageProgressResponse(model, "waiting_reconnect", state), nil
	}
	_, mimeType, data, err := readValidatedCodexImage(*result.Item.SavedPath, allowedRoot, maxCodexImageOutputBytes, "Codex image job directory")
	if err != nil {
		return coregeneration.Response{}, err
	}
	state.SavedPath = ""
	return coregeneration.Response{
		ID:     codexImageResponseIDPrefix + strings.TrimSpace(result.ThreadID),
		Status: "completed",
		Model:  model,
		Assets: []coregeneration.Asset{{
			Kind:     coregeneration.KindImage,
			Base64:   base64.StdEncoding.EncodeToString(data),
			MIMEType: mimeType,
			Metadata: map[string]any{codexImageInternalPayloadKey: true},
		}},
		Metadata: map[string]any{"runtime_state": state},
	}, nil
}

func codexImageProgressResponse(model string, status string, state GenerationTaskRuntimeState) coregeneration.Response {
	response := coregeneration.Response{Status: status, Model: model, Metadata: map[string]any{"runtime_state": state}}
	if state.CodexThreadID != "" {
		response.ID = codexImageResponseIDPrefix + state.CodexThreadID
	}
	return response
}

func applyCodexImageCheckpoint(state *GenerationTaskRuntimeState, checkpoint codexapp.ImageGenerationCheckpoint) {
	if value := strings.TrimSpace(checkpoint.ThreadID); value != "" {
		state.CodexThreadID = value
	}
	if value := strings.TrimSpace(checkpoint.TurnID); value != "" {
		state.CodexTurnID = value
	}
	if checkpoint.Item != nil {
		applyCodexImageItem(state, *checkpoint.Item)
	}
}

func applyCodexImageResult(state *GenerationTaskRuntimeState, result codexapp.ImageGenerationResult) {
	if value := strings.TrimSpace(result.ThreadID); value != "" {
		state.CodexThreadID = value
	}
	if value := strings.TrimSpace(result.TurnID); value != "" {
		state.CodexTurnID = value
	}
	applyCodexImageItem(state, result.Item)
}

func applyCodexImageItem(state *GenerationTaskRuntimeState, item codexapp.ImageGenerationThreadItem) {
	if value := strings.TrimSpace(item.ID); value != "" {
		state.CodexItemID = value
	}
	if item.RevisedPrompt != nil {
		state.RevisedPrompt = strings.TrimSpace(*item.RevisedPrompt)
	}
}

func codexImageItemCompleted(item codexapp.ImageGenerationThreadItem) bool {
	return item.Type == "imageGeneration" && item.Status == "completed" && item.Failure == nil && item.SavedPath != nil && strings.TrimSpace(*item.SavedPath) != ""
}

func codexImageFailure(item codexapp.ImageGenerationThreadItem) error {
	if item.Failure != nil {
		failureType := strings.TrimSpace(item.Failure.Type)
		if failureType == "" {
			failureType = "unknown"
		}
		return fmt.Errorf("Codex image generation failed: %s", failureType)
	}
	return fmt.Errorf("Codex image item status is %q", item.Status)
}

func readValidatedCodexImage(path string, allowedRoot string, maximum int64, rootLabel string) (string, string, []byte, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || !filepath.IsAbs(path) {
		return "", "", nil, fmt.Errorf("Codex image path must be absolute")
	}
	root := filepath.Clean(strings.TrimSpace(allowedRoot))
	if root == "." || !filepath.IsAbs(root) {
		return "", "", nil, fmt.Errorf("%s must be absolute", rootLabel)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", nil, fmt.Errorf("canonicalizing %s: %w", rootLabel, err)
	}
	relative, err := filepath.Rel(root, path)
	if err == nil && (filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))) && root != canonicalRoot {
		relative, err = filepath.Rel(canonicalRoot, path)
	}
	if err == nil && (filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		canonicalParent, parentErr := filepath.EvalSymlinks(filepath.Dir(path))
		if parentErr == nil {
			path = filepath.Join(canonicalParent, filepath.Base(path))
			relative, err = filepath.Rel(canonicalRoot, path)
		}
	}
	if err != nil || filepath.IsAbs(relative) || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", nil, fmt.Errorf("Codex image path is outside %s", rootLabel)
	}
	components := strings.Split(relative, string(filepath.Separator))
	rootFD, err := unix.Open(canonicalRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", "", nil, fmt.Errorf("opening %s: %w", rootLabel, err)
	}
	currentFD := rootFD
	defer func() {
		_ = unix.Close(currentFD)
		if currentFD != rootFD {
			_ = unix.Close(rootFD)
		}
	}()
	for _, component := range components[:len(components)-1] {
		if component == "" || component == "." || component == ".." {
			return "", "", nil, fmt.Errorf("Codex image path is outside %s", rootLabel)
		}
		nextFD, openErr := unix.Openat(currentFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return "", "", nil, fmt.Errorf("opening Codex image parent: %w", openErr)
		}
		if currentFD != rootFD {
			_ = unix.Close(currentFD)
		}
		currentFD = nextFD
	}
	name := components[len(components)-1]
	fd, err := unix.Openat(currentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", "", nil, fmt.Errorf("opening Codex image result: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return "", "", nil, fmt.Errorf("opening Codex image result")
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return "", "", nil, fmt.Errorf("checking Codex image file: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return "", "", nil, fmt.Errorf("Codex image path is not a regular file")
	}
	if stat.Size <= 0 {
		return "", "", nil, fmt.Errorf("Codex image file is empty")
	}
	if stat.Size > maximum {
		return "", "", nil, fmt.Errorf("Codex image file exceeds %d bytes", maximum)
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return "", "", nil, fmt.Errorf("reading Codex image result: %w", err)
	}
	if int64(len(data)) > maximum {
		return "", "", nil, fmt.Errorf("Codex image file exceeds %d bytes", maximum)
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return "", "", nil, fmt.Errorf("rechecking Codex image file: %w", err)
	}
	if after.Dev != stat.Dev || after.Ino != stat.Ino || after.Size != stat.Size {
		return "", "", nil, fmt.Errorf("Codex image file changed while reading")
	}
	mimeType, err := validateCodexImageBytes(data)
	if err != nil {
		return "", "", nil, err
	}
	return filepath.Join(canonicalRoot, relative), mimeType, data, nil
}

func validateCodexImageBytes(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("Codex image file is empty")
	}
	mimeType := http.DetectContentType(data)
	if _, ok := codexImageExtension(mimeType); !ok {
		return "", fmt.Errorf("Codex image result has unsupported MIME type %q", mimeType)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("Codex image result is an invalid image: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxCodexImageDimension || config.Height > maxCodexImageDimension {
		return "", fmt.Errorf("Codex image dimensions %dx%d exceed limits", config.Width, config.Height)
	}
	if int64(config.Width)*int64(config.Height) > maxCodexImagePixels {
		return "", fmt.Errorf("Codex image pixel count exceeds %d", maxCodexImagePixels)
	}
	wantFormat := map[string]string{"image/png": "png", "image/jpeg": "jpeg", "image/gif": "gif"}[mimeType]
	if format != wantFormat {
		return "", fmt.Errorf("Codex image MIME mismatch: decoded %q", format)
	}
	if _, decodedFormat, decodeErr := image.Decode(bytes.NewReader(data)); decodeErr != nil {
		return "", fmt.Errorf("Codex image result is an invalid image: %w", decodeErr)
	} else if decodedFormat != wantFormat {
		return "", fmt.Errorf("Codex image MIME mismatch: decoded %q", decodedFormat)
	}
	return mimeType, nil
}

func codexImageRandomHex(byteCount int) (string, error) {
	value := make([]byte, byteCount)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	const alphabet = "0123456789abcdef"
	encoded := make([]byte, byteCount*2)
	for index, item := range value {
		encoded[index*2] = alphabet[item>>4]
		encoded[index*2+1] = alphabet[item&0x0f]
	}
	return string(encoded), nil
}

func requirePathWithin(root string, path string, rootLabel string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Codex image path is outside %s", rootLabel)
	}
	return nil
}

func codexImageExtension(mimeType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png", true
	case "image/jpeg":
		return ".jpg", true
	case "image/gif":
		return ".gif", true
	default:
		return "", false
	}
}
