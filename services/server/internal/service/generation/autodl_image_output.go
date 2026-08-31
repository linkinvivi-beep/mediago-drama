package generation

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"strings"
)

const (
	maxAutoDLImageOutputBytes int64 = 64 << 20
	maxAutoDLImageDimension        = 16384
	maxAutoDLImagePixels           = 40_000_000
)

func readValidatedAutoDLImage(body io.ReadCloser, headers http.Header) (string, string, error) {
	if body == nil {
		return "", "", fmt.Errorf("ComfyUI image output is missing")
	}
	defer body.Close()
	mimeType, _, err := mime.ParseMediaType(strings.TrimSpace(headers.Get("Content-Type")))
	if err != nil || !allowedAutoDLImageMIMEType(mimeType) {
		return "", "", fmt.Errorf("ComfyUI image output has an unsupported MIME type")
	}
	payload, err := io.ReadAll(io.LimitReader(body, maxAutoDLImageOutputBytes+1))
	if err != nil {
		return "", "", fmt.Errorf("reading ComfyUI image output: %w", err)
	}
	if int64(len(payload)) > maxAutoDLImageOutputBytes {
		return "", "", fmt.Errorf("ComfyUI image output exceeds %d bytes", maxAutoDLImageOutputBytes)
	}
	configuration, detectedFormat, err := image.DecodeConfig(bytes.NewReader(payload))
	if err != nil || configuration.Width <= 0 || configuration.Height <= 0 ||
		configuration.Width > maxAutoDLImageDimension || configuration.Height > maxAutoDLImageDimension ||
		int64(configuration.Width)*int64(configuration.Height) > maxAutoDLImagePixels {
		return "", "", fmt.Errorf("ComfyUI image output dimensions are invalid")
	}
	if !autoDLImageFormatMatchesMIMEType(detectedFormat, mimeType) {
		return "", "", fmt.Errorf("ComfyUI image output MIME type does not match its content")
	}
	if _, _, err := image.Decode(bytes.NewReader(payload)); err != nil {
		return "", "", fmt.Errorf("ComfyUI image output is corrupt")
	}
	return base64.StdEncoding.EncodeToString(payload), mimeType, nil
}

func allowedAutoDLImageMIMEType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image/png", "image/jpeg", "image/gif":
		return true
	default:
		return false
	}
}

func autoDLImageFormatMatchesMIMEType(format string, mimeType string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "png":
		return mimeType == "image/png"
	case "jpeg":
		return mimeType == "image/jpeg"
	case "gif":
		return mimeType == "image/gif"
	default:
		return false
	}
}
