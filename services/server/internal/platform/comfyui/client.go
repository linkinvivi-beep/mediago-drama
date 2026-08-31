package comfyui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

var (
	ErrInvalidBaseURL           = errors.New("comfyui base URL must be a bare loopback HTTP origin")
	ErrResponseTooLarge         = errors.New("comfyui response body exceeds limit")
	ErrUploadTooLarge           = errors.New("comfyui image upload exceeds limit")
	ErrSubmissionOutcomeUnknown = errors.New("comfyui prompt submission outcome is unknown")
)

// Client exposes only the ComfyUI operations required by MediaLink.
type Client interface {
	SystemStats(context.Context) (SystemStats, error)
	ObjectInfo(context.Context) (ObjectInfo, error)
	Queue(context.Context) (QueueState, error)
	UploadImage(context.Context, UploadImageRequest) (UploadedImage, error)
	SubmitPrompt(context.Context, json.RawMessage, string) (PromptSubmission, error)
	History(context.Context, string) (PromptHistory, error)
	View(context.Context, OutputFile) (io.ReadCloser, http.Header, error)
	DeleteQueuedPrompt(context.Context, string) (bool, error)
}

type SystemStats struct {
	System  SystemInfo   `json:"system"`
	Devices []DeviceInfo `json:"devices"`
}

type SystemInfo struct {
	OS                        string   `json:"os"`
	RAMTotal                  int64    `json:"ram_total"`
	RAMFree                   int64    `json:"ram_free"`
	ComfyUIVersion            string   `json:"comfyui_version"`
	RequiredFrontendVersion   string   `json:"required_frontend_version"`
	InstalledTemplatesVersion string   `json:"installed_templates_version"`
	RequiredTemplatesVersion  string   `json:"required_templates_version"`
	PythonVersion             string   `json:"python_version"`
	PyTorchVersion            string   `json:"pytorch_version"`
	EmbeddedPython            bool     `json:"embedded_python"`
	Argv                      []string `json:"argv"`
}

type DeviceInfo struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	Index          int    `json:"index"`
	VRAMTotal      int64  `json:"vram_total"`
	VRAMFree       int64  `json:"vram_free"`
	TorchVRAMTotal int64  `json:"torch_vram_total"`
	TorchVRAMFree  int64  `json:"torch_vram_free"`
}

type ObjectInfo map[string]ObjectDefinition

type ObjectDefinition struct {
	Input       ObjectInputs `json:"input"`
	Output      []string     `json:"output"`
	OutputName  []string     `json:"output_name"`
	Name        string       `json:"name"`
	DisplayName string       `json:"display_name"`
	Description string       `json:"description"`
	Category    string       `json:"category"`
	OutputNode  bool         `json:"output_node"`
}

type ObjectInputs struct {
	Required map[string]json.RawMessage `json:"required"`
	Optional map[string]json.RawMessage `json:"optional"`
	Hidden   map[string]json.RawMessage `json:"hidden"`
}

type QueueState struct {
	Running []QueueItem `json:"queue_running"`
	Pending []QueueItem `json:"queue_pending"`
}

type QueueItem struct {
	Number           int64
	PromptID         string
	Prompt           json.RawMessage
	ExtraData        json.RawMessage
	OutputsToExecute json.RawMessage
}

type UploadImageRequest struct {
	Filename string
	// Content ownership transfers to UploadImage. Close must unblock any
	// concurrent Read so cancellation and connection failure can terminate.
	Content   io.ReadCloser
	Size      int64
	Type      string
	Subfolder string
	Overwrite bool
}

type UploadedImage struct {
	Name      string `json:"name"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

type PromptSubmission struct {
	PromptID   string                     `json:"prompt_id"`
	Number     int64                      `json:"number"`
	NodeErrors map[string]json.RawMessage `json:"node_errors"`
}

type PromptHistory struct {
	PromptID string
	Prompt   json.RawMessage       `json:"prompt"`
	Outputs  map[string]NodeOutput `json:"outputs"`
	Status   HistoryStatus         `json:"status"`
}

type NodeOutput struct {
	Images []OutputFile `json:"images"`
	Gifs   []OutputFile `json:"gifs"`
	Videos []OutputFile `json:"videos"`
	Audio  []OutputFile `json:"audio"`
}

type HistoryStatus struct {
	StatusString string            `json:"status_str"`
	Completed    bool              `json:"completed"`
	Messages     []json.RawMessage `json:"messages"`
}

type OutputFile struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

// HTTPStatusError reports only stable operation and status metadata. ComfyUI
// response bodies can contain workflow inputs and are never included.
type HTTPStatusError struct {
	Operation  string
	StatusCode int
}

func (err *HTTPStatusError) Error() string {
	return fmt.Sprintf("comfyui %s failed with HTTP status %d", err.Operation, err.StatusCode)
}
