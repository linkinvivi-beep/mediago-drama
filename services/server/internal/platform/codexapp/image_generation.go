package codexapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ImageGenerationStageThreadStarted = "thread_started"
	ImageGenerationStageTurnStarted   = "turn_started"
	ImageGenerationStageItemStarted   = "item_started"
	ImageGenerationStageItemCompleted = "item_completed"
	ImageGenerationStageTurnCompleted = "turn_completed"
)

// ImageGenerationFailure is the failure payload currently exposed by the generated app-server schema.
type ImageGenerationFailure struct {
	Type     string `json:"type"`
	LimitID  string `json:"limitId"`
	ResetsAt *int64 `json:"resetsAt"`
}

// ImageGenerationThreadItem is the structured image result emitted by Codex app-server.
type ImageGenerationThreadItem struct {
	ID                    string                  `json:"id"`
	Result                string                  `json:"result"`
	RevisedPrompt         *string                 `json:"revisedPrompt"`
	SavedPath             *string                 `json:"savedPath"`
	Status                string                  `json:"status"`
	Failure               *ImageGenerationFailure `json:"failure"`
	TransparentBackground *bool                   `json:"transparentBackground"`
	Type                  string                  `json:"type"`
}

// ImageGenerationInput is one typed turn input sent to app-server.
type ImageGenerationInput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Path string `json:"path,omitempty"`
}

// ImageGenerationRequest describes one isolated built-in $imagegen turn.
type ImageGenerationRequest struct {
	JobDir         string
	Prompt         string
	ReferencePaths []string
}

// ImageGenerationCheckpoint identifies durable points in the structured item lifecycle.
type ImageGenerationCheckpoint struct {
	Stage    string
	ThreadID string
	TurnID   string
	Item     *ImageGenerationThreadItem
}

// ImageGenerationResult is a completed, structured app-server image result.
type ImageGenerationResult struct {
	ThreadID string
	TurnID   string
	Item     ImageGenerationThreadItem
}

// ImageGenerationSession adds typed image-generation behavior to a JSON-RPC client.
type ImageGenerationSession struct {
	client Client
}

// NewImageGenerationSession wraps a client without taking ownership of its lifecycle.
func NewImageGenerationSession(client Client) *ImageGenerationSession {
	return &ImageGenerationSession{client: client}
}

// Capabilities reads model-provider features without starting a generation turn.
func (session *ImageGenerationSession) Capabilities(ctx context.Context) (ModelProviderCapabilities, error) {
	if session == nil {
		return ModelProviderCapabilities{}, fmt.Errorf("Codex image session is required")
	}
	return ReadModelProviderCapabilities(ctx, session.client)
}

// GenerateImage starts a workspace-scoped $imagegen turn and consumes only structured lifecycle items.
func (session *ImageGenerationSession) GenerateImage(
	ctx context.Context,
	request ImageGenerationRequest,
	checkpoint func(ImageGenerationCheckpoint),
) (ImageGenerationResult, error) {
	if session == nil || session.client == nil {
		return ImageGenerationResult{}, fmt.Errorf("Codex image session client is required")
	}
	jobDir, inputs, err := validateImageGenerationRequest(request)
	if err != nil {
		return ImageGenerationResult{}, err
	}

	var threadResponse struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := session.client.Call(ctx, "thread/start", map[string]any{
		"approvalPolicy":        "never",
		"cwd":                   jobDir,
		"runtimeWorkspaceRoots": []string{jobDir},
		"sandbox":               "workspace-write",
	}, &threadResponse); err != nil {
		return ImageGenerationResult{}, fmt.Errorf("starting Codex image thread: %w", err)
	}
	threadID := strings.TrimSpace(threadResponse.Thread.ID)
	if threadID == "" {
		return ImageGenerationResult{}, fmt.Errorf("Codex image session returned an empty thread id")
	}
	emitImageGenerationCheckpoint(checkpoint, ImageGenerationCheckpoint{Stage: ImageGenerationStageThreadStarted, ThreadID: threadID})

	var turnResponse struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := session.client.Call(ctx, "turn/start", map[string]any{
		"threadId": threadID,
		"input":    inputs,
	}, &turnResponse); err != nil {
		return ImageGenerationResult{}, fmt.Errorf("starting Codex image turn: %w", err)
	}
	turnID := strings.TrimSpace(turnResponse.Turn.ID)
	if turnID == "" {
		return ImageGenerationResult{}, fmt.Errorf("Codex image session returned an empty turn id")
	}
	emitImageGenerationCheckpoint(checkpoint, ImageGenerationCheckpoint{Stage: ImageGenerationStageTurnStarted, ThreadID: threadID, TurnID: turnID})

	var completed *ImageGenerationThreadItem
	var rejected *ImageGenerationThreadItem
	for {
		message, err := session.client.Next(ctx)
		if err != nil {
			return ImageGenerationResult{}, fmt.Errorf("reading Codex image turn: %w", err)
		}
		switch message.Method {
		case "item/started", "item/completed":
			event, ok := decodeImageGenerationItemEvent(message.Params, threadID, turnID)
			if !ok || event.Item.Type != "imageGeneration" {
				continue
			}
			stage := ImageGenerationStageItemStarted
			if message.Method == "item/completed" {
				stage = ImageGenerationStageItemCompleted
				if validCompletedImageGenerationItem(event.Item) {
					item := event.Item
					completed = &item
				} else {
					item := event.Item
					rejected = &item
				}
			}
			item := event.Item
			emitImageGenerationCheckpoint(checkpoint, ImageGenerationCheckpoint{Stage: stage, ThreadID: threadID, TurnID: turnID, Item: &item})
		case "turn/completed":
			turn, ok := decodeImageGenerationTurnCompleted(message.Params, threadID, turnID)
			if !ok {
				continue
			}
			emitImageGenerationCheckpoint(checkpoint, ImageGenerationCheckpoint{Stage: ImageGenerationStageTurnCompleted, ThreadID: threadID, TurnID: turnID})
			if turn.Status != "completed" {
				message := strings.TrimSpace(turn.ErrorMessage)
				if message == "" {
					message = "turn did not complete"
				}
				return ImageGenerationResult{}, fmt.Errorf("Codex image turn %s: %s", turn.Status, message)
			}
			if completed == nil {
				return ImageGenerationResult{}, invalidImageGenerationItemError(rejected)
			}
			return ImageGenerationResult{ThreadID: threadID, TurnID: turnID, Item: *completed}, nil
		}
	}
}

func validateImageGenerationRequest(request ImageGenerationRequest) (string, []ImageGenerationInput, error) {
	jobDir := filepath.Clean(strings.TrimSpace(request.JobDir))
	if jobDir == "." || !filepath.IsAbs(jobDir) {
		return "", nil, fmt.Errorf("Codex image job directory must be absolute")
	}
	info, err := os.Stat(jobDir)
	if err != nil {
		return "", nil, fmt.Errorf("checking Codex image job directory: %w", err)
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("Codex image job directory is not a directory")
	}
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		return "", nil, fmt.Errorf("Codex image prompt is required")
	}
	inputs := []ImageGenerationInput{{Type: "text", Text: "$imagegen\n" + prompt}}
	for _, value := range request.ReferencePaths {
		path := filepath.Clean(strings.TrimSpace(value))
		if path == "." || !filepath.IsAbs(path) {
			return "", nil, fmt.Errorf("Codex image reference path must be absolute")
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", nil, fmt.Errorf("checking Codex image reference %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return "", nil, fmt.Errorf("Codex image reference %q is not a regular file", path)
		}
		inputs = append(inputs, ImageGenerationInput{Type: "localImage", Path: path})
	}
	return jobDir, inputs, nil
}

type imageGenerationItemEvent struct {
	ThreadID string                    `json:"threadId"`
	TurnID   string                    `json:"turnId"`
	Item     ImageGenerationThreadItem `json:"item"`
}

func decodeImageGenerationItemEvent(raw json.RawMessage, threadID string, turnID string) (imageGenerationItemEvent, bool) {
	var event imageGenerationItemEvent
	if json.Unmarshal(raw, &event) != nil || event.ThreadID != threadID || event.TurnID != turnID {
		return imageGenerationItemEvent{}, false
	}
	return event, true
}

type imageGenerationTurn struct {
	Status       string
	ErrorMessage string
}

func decodeImageGenerationTurnCompleted(raw json.RawMessage, threadID string, turnID string) (imageGenerationTurn, bool) {
	var event struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &event) != nil || event.ThreadID != threadID || event.Turn.ID != turnID {
		return imageGenerationTurn{}, false
	}
	turn := imageGenerationTurn{Status: event.Turn.Status}
	if event.Turn.Error != nil {
		turn.ErrorMessage = event.Turn.Error.Message
	}
	return turn, true
}

func validCompletedImageGenerationItem(item ImageGenerationThreadItem) bool {
	return item.Type == "imageGeneration" && item.Status == "completed" && item.Failure == nil && item.SavedPath != nil && strings.TrimSpace(*item.SavedPath) != ""
}

func invalidImageGenerationItemError(item *ImageGenerationThreadItem) error {
	if item == nil {
		return fmt.Errorf("Codex image turn completed without a structured imageGeneration item")
	}
	if item.Failure != nil {
		return fmt.Errorf("Codex image generation failed: %s", strings.TrimSpace(item.Failure.Type))
	}
	if item.Status != "completed" {
		return fmt.Errorf("Codex image item status is %q", item.Status)
	}
	return fmt.Errorf("Codex image item completed without a saved path")
}

func emitImageGenerationCheckpoint(callback func(ImageGenerationCheckpoint), checkpoint ImageGenerationCheckpoint) {
	if callback != nil {
		callback(checkpoint)
	}
}
