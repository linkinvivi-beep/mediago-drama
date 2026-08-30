package generation

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
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
)

const (
	codexImageTaskIDRequestOption = "_medialink_task_id"
	codexImageResponseIDPrefix    = coregeneration.RouteCodexImage + ":"
)

var safeCodexImageTaskID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// CodexImageSession is the typed app-server surface required by the image provider.
type CodexImageSession interface {
	Capabilities(context.Context) (codexapp.ModelProviderCapabilities, error)
	GenerateImage(context.Context, codexapp.ImageGenerationRequest, func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error)
	ReadImageResult(context.Context, string) (codexapp.ImageGenerationResult, error)
}

// CodexImageProvider executes all Codex image turns through one global semaphore.
type CodexImageProvider struct {
	session CodexImageSession
	root    string
	queue   chan struct{}
}

type managedCodexImageSession struct {
	parent  context.Context
	binPath string
	mu      sync.Mutex
	client  codexapp.Client
	session *codexapp.ImageGenerationSession
}

// NewCodexImageProvider creates one application-scoped Codex image provider.
// dataRoot is the MediaLink-owned user-data directory, not an arbitrary output path.
func NewCodexImageProvider(session CodexImageSession, dataRoot string) *CodexImageProvider {
	return &CodexImageProvider{
		session: session,
		root:    filepath.Join(filepath.Clean(strings.TrimSpace(dataRoot)), "generation", "codex-image"),
		queue:   make(chan struct{}, 1),
	}
}

// NewManagedCodexImageProvider creates the application singleton. Its app-server
// process starts lazily and is canceled with parent. A failed read reconnects once;
// GenerateImage is never retried because that could duplicate a turn.
func NewManagedCodexImageProvider(parent context.Context, binPath string, dataRoot string) *CodexImageProvider {
	if parent == nil {
		parent = context.Background()
	}
	return NewCodexImageProvider(&managedCodexImageSession{parent: parent, binPath: strings.TrimSpace(binPath)}, dataRoot)
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
	session.mu.Lock()
	defer session.mu.Unlock()
	typed, err := session.ensureLocked()
	if err != nil {
		return codexapp.ModelProviderCapabilities{}, err
	}
	capabilities, err := typed.Capabilities(ctx)
	if err != nil {
		session.invalidateLocked()
	}
	return capabilities, err
}

func (session *managedCodexImageSession) GenerateImage(ctx context.Context, request codexapp.ImageGenerationRequest, checkpoint func(codexapp.ImageGenerationCheckpoint)) (codexapp.ImageGenerationResult, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	typed, err := session.ensureLocked()
	if err != nil {
		return codexapp.ImageGenerationResult{}, err
	}
	result, err := typed.GenerateImage(ctx, request, checkpoint)
	if err != nil {
		session.invalidateLocked()
	}
	return result, err
}

func (session *managedCodexImageSession) ReadImageResult(ctx context.Context, threadID string) (codexapp.ImageGenerationResult, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	typed, err := session.ensureLocked()
	if err != nil {
		return codexapp.ImageGenerationResult{}, err
	}
	result, err := typed.ReadImageResult(ctx, threadID)
	if err == nil || ctx.Err() != nil {
		return result, err
	}
	session.invalidateLocked()
	typed, err = session.ensureLocked()
	if err != nil {
		return codexapp.ImageGenerationResult{}, err
	}
	return typed.ReadImageResult(ctx, threadID)
}

func (session *managedCodexImageSession) ensureLocked() (*codexapp.ImageGenerationSession, error) {
	if session.session != nil {
		return session.session, nil
	}
	client, err := codexapp.Start(session.parent, session.binPath)
	if err != nil {
		return nil, err
	}
	session.client = client
	session.session = codexapp.NewImageGenerationSession(client)
	return session.session, nil
}

func (session *managedCodexImageSession) invalidateLocked() {
	if session.client != nil {
		session.client.Close()
	}
	session.client = nil
	session.session = nil
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

	select {
	case provider.queue <- struct{}{}:
		defer func() { <-provider.queue }()
	case <-ctx.Done():
		return coregeneration.Response{}, ctx.Err()
	}

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
	jobDir := filepath.Join(canonicalRoot, taskID)
	if info, statErr := os.Lstat(jobDir); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("Codex image task directory collides with a non-directory")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("checking Codex image task directory: %w", statErr)
	} else if err := os.Mkdir(jobDir, 0o700); err != nil {
		return "", fmt.Errorf("creating Codex image task directory: %w", err)
	}
	canonicalJobDir, err := filepath.EvalSymlinks(jobDir)
	if err != nil {
		return "", fmt.Errorf("canonicalizing Codex image task directory: %w", err)
	}
	if err := requirePathWithin(canonicalRoot, canonicalJobDir, "Codex image jobs directory"); err != nil {
		return "", err
	}
	return canonicalJobDir, nil
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
	canonicalJobDir, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", fmt.Errorf("canonicalizing Codex image thread job directory: %w", err)
	}
	if err := requirePathWithin(canonicalRoot, canonicalJobDir, "Codex image jobs directory"); err != nil {
		return "", err
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalJobDir)
	if err != nil || relative == "." || filepath.IsAbs(relative) || strings.Contains(relative, string(filepath.Separator)) || !safeCodexImageTaskID.MatchString(relative) {
		return "", fmt.Errorf("Codex image thread cwd must be an immediate task directory")
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
	paths := make([]string, 0, len(values))
	dataRoot := filepath.Dir(filepath.Dir(provider.root))
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return nil, fmt.Errorf("creating MediaLink user data directory: %w", err)
	}
	canonicalDataRoot, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("canonicalizing MediaLink user data directory: %w", err)
	}
	for index, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(value), "data:") {
			path, materializeErr := materializeCodexImageDataReference(value, jobDir, index)
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
		canonical, validateErr := validateCodexImageFile(value, canonicalDataRoot, "MediaLink user data directory")
		if validateErr != nil {
			return nil, fmt.Errorf("Codex image reference %d: %w", index+1, validateErr)
		}
		if _, validateErr = sniffCodexImageMIME(canonical); validateErr != nil {
			return nil, fmt.Errorf("Codex image reference %d: %w", index+1, validateErr)
		}
		paths = append(paths, canonical)
	}
	return paths, nil
}

func materializeCodexImageDataReference(value string, jobDir string, index int) (string, error) {
	header, payload, ok := strings.Cut(value, ",")
	if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return "", fmt.Errorf("Codex image reference %d must be a base64 image data URI", index+1)
	}
	mimeType := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")))
	extension, ok := codexImageExtension(mimeType)
	if !ok {
		return "", fmt.Errorf("Codex image reference %d has unsupported MIME type %q", index+1, mimeType)
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(data) == 0 {
		return "", fmt.Errorf("Codex image reference %d has invalid base64 data", index+1)
	}
	if detected := http.DetectContentType(data); detected != mimeType {
		return "", fmt.Errorf("Codex image reference %d MIME mismatch: detected %q", index+1, detected)
	}
	path := filepath.Join(jobDir, fmt.Sprintf("reference-%03d%s", index+1, extension))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("materializing Codex image reference %d: %w", index+1, err)
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
	path, mimeType, data, err := readValidatedCodexImage(*result.Item.SavedPath, allowedRoot)
	if err != nil {
		return coregeneration.Response{}, err
	}
	state.SavedPath = path
	return coregeneration.Response{
		ID:     codexImageResponseIDPrefix + strings.TrimSpace(result.ThreadID),
		Status: "completed",
		Model:  model,
		Assets: []coregeneration.Asset{{
			Kind:     coregeneration.KindImage,
			Base64:   base64.StdEncoding.EncodeToString(data),
			MIMEType: mimeType,
			Metadata: map[string]any{"saved_path": path},
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
	if item.SavedPath != nil {
		state.SavedPath = strings.TrimSpace(*item.SavedPath)
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

func readValidatedCodexImage(savedPath string, allowedRoot string) (string, string, []byte, error) {
	canonical, err := validateCodexImageFile(savedPath, allowedRoot, "Codex image job directory")
	if err != nil {
		return "", "", nil, err
	}
	file, err := os.Open(canonical)
	if err != nil {
		return "", "", nil, fmt.Errorf("opening Codex image result: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return "", "", nil, fmt.Errorf("reading Codex image result: %w", err)
	}
	if len(data) == 0 {
		return "", "", nil, fmt.Errorf("Codex image result is empty")
	}
	mimeType := http.DetectContentType(data)
	if _, ok := codexImageExtension(mimeType); !ok {
		return "", "", nil, fmt.Errorf("Codex image result has unsupported MIME type %q", mimeType)
	}
	return canonical, mimeType, data, nil
}

func sniffCodexImageMIME(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening image file: %w", err)
	}
	defer file.Close()
	header := make([]byte, 512)
	count, readErr := file.Read(header)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", fmt.Errorf("reading image file: %w", readErr)
	}
	if count == 0 {
		return "", fmt.Errorf("image file is empty")
	}
	mimeType := http.DetectContentType(header[:count])
	if _, ok := codexImageExtension(mimeType); !ok {
		return "", fmt.Errorf("image file has unsupported MIME type %q", mimeType)
	}
	return mimeType, nil
}

func validateCodexImageFile(path string, allowedRoot string, rootLabel string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || !filepath.IsAbs(path) {
		return "", fmt.Errorf("Codex image path must be absolute")
	}
	root := filepath.Clean(strings.TrimSpace(allowedRoot))
	if root == "." || !filepath.IsAbs(root) {
		return "", fmt.Errorf("%s must be absolute", rootLabel)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("canonicalizing %s: %w", rootLabel, err)
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("canonicalizing Codex image path: %w", err)
	}
	if err := requirePathWithin(canonicalRoot, canonicalPath, rootLabel); err != nil {
		return "", err
	}
	info, err := os.Stat(canonicalPath)
	if err != nil {
		return "", fmt.Errorf("checking Codex image file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Codex image path is not a regular file")
	}
	if info.Size() <= 0 {
		return "", fmt.Errorf("Codex image file is empty")
	}
	return canonicalPath, nil
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
	case "image/webp":
		return ".webp", true
	default:
		return "", false
	}
}
