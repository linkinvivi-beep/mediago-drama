package generation

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/comfyui"
)

const maxAutoDLVideoOutputBytes int64 = 200 << 20

func downloadAutoDLVideoOutputs(ctx context.Context, client comfyui.Client, history comfyui.PromptHistory, bindings comfyui.WorkflowBindings) ([]coregeneration.Asset, error) {
	assets := make([]coregeneration.Asset, 0, len(bindings.Outputs))
	for _, binding := range bindings.Outputs {
		output, ok := history.Outputs[binding.NodeID]
		if !ok || len(output.Videos) == 0 {
			return nil, fmt.Errorf("ComfyUI history is missing configured video output node %s", binding.NodeID)
		}
		index := binding.OutputIndex
		if index < 0 || index >= len(output.Videos) {
			return nil, fmt.Errorf("ComfyUI history video output index is invalid")
		}
		file := output.Videos[index]
		body, headers, err := client.View(ctx, file)
		if err != nil {
			return nil, err
		}
		encoded, mimeType, err := readValidatedAutoDLVideo(body, headers, file.Filename)
		if err != nil {
			return nil, err
		}
		assets = append(assets, coregeneration.Asset{Kind: coregeneration.KindVideo, Base64: encoded, MIMEType: mimeType})
	}
	if len(assets) == 0 {
		return nil, fmt.Errorf("ComfyUI H3 workflow returned no configured video outputs")
	}
	return assets, nil
}

func readValidatedAutoDLVideo(body io.ReadCloser, headers http.Header, filename string) (string, string, error) {
	if body == nil {
		return "", "", fmt.Errorf("ComfyUI video output is missing")
	}
	defer body.Close()
	payload, err := io.ReadAll(io.LimitReader(body, maxAutoDLVideoOutputBytes+1))
	if err != nil {
		return "", "", fmt.Errorf("reading ComfyUI video output: %w", err)
	}
	if len(payload) == 0 || int64(len(payload)) > maxAutoDLVideoOutputBytes {
		return "", "", fmt.Errorf("ComfyUI video output is empty or exceeds %d bytes", maxAutoDLVideoOutputBytes)
	}
	detected := detectAutoDLVideoMIMEType(payload)
	if detected == "" {
		return "", "", fmt.Errorf("ComfyUI video output has an invalid container signature")
	}
	headerType, _, _ := mime.ParseMediaType(strings.TrimSpace(headers.Get("Content-Type")))
	if headerType != "" && headerType != "application/octet-stream" && headerType != detected {
		return "", "", fmt.Errorf("ComfyUI video output MIME type does not match its content")
	}
	if extensionType := autoDLVideoMIMETypeForExtension(filename); extensionType != "" && extensionType != detected {
		return "", "", fmt.Errorf("ComfyUI video output extension does not match its content")
	}
	return base64.StdEncoding.EncodeToString(payload), detected, nil
}

func detectAutoDLVideoMIMEType(payload []byte) string {
	if len(payload) >= 12 && bytes.Equal(payload[4:8], []byte("ftyp")) {
		brand := string(payload[8:12])
		if brand == "qt  " {
			return "video/quicktime"
		}
		return "video/mp4"
	}
	if len(payload) >= 4 && bytes.Equal(payload[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
		return "video/webm"
	}
	return ""
}

func autoDLVideoMIMETypeForExtension(filename string) string {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(filename))) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	default:
		return ""
	}
}
