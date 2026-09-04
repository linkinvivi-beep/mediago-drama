package generation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
)

type sensitivePromptErrorProvider struct {
	failure error
}

func (provider sensitivePromptErrorProvider) Name() string { return "sensitive-prompt-error" }

func (provider sensitivePromptErrorProvider) Generate(context.Context, coregeneration.Request) (coregeneration.Response, error) {
	return coregeneration.Response{}, provider.failure
}

func (provider sensitivePromptErrorProvider) Get(context.Context, string) (coregeneration.Response, error) {
	return coregeneration.Response{}, provider.failure
}

func TestGenerateWithProviderRedactsSensitivePromptAndProviderError(t *testing.T) {
	const protectedBody = "PROTECTED-REFERENCE-BODY-DO-NOT-LOG"
	const signedToken = "SIGNED-REFERENCE-TOKEN-DO-NOT-LOG"
	encodedReference := base64.StdEncoding.EncodeToString([]byte("INLINE-REFERENCE-BYTES-DO-NOT-LOG"))
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	workflow := NewGenerationService(nil, nil, nil)
	_, err := workflow.generateWithProvider(
		context.Background(),
		sensitivePromptErrorProvider{failure: errors.New("provider echoed " + protectedBody)},
		coregeneration.Request{
			Kind:   coregeneration.KindText,
			Prompt: "envelope contains " + protectedBody,
			ReferenceURLs: []string{
				"https://example.test/reference.png?token=" + signedToken,
				"data:image/png;base64," + encodedReference,
			},
			Options: map[string]any{generationSensitivePromptRequestOption: true},
		},
		generationProviderLogContext{Action: "create", TaskID: "task-sensitive"},
	)
	if err == nil || strings.Contains(err.Error(), protectedBody) {
		t.Fatalf("generateWithProvider() error = %q, want generic redacted error", err)
	}
	for _, forbidden := range []string{protectedBody, signedToken, encodedReference} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("provider logs leaked sensitive value %q: %s", forbidden, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "<protected-prompt-omitted>") {
		t.Fatalf("provider logs = %q, want explicit prompt omission", logs.String())
	}
}

func TestSanitizedGenerationResponseOmitsSavedPaths(t *testing.T) {
	path := "/private/tmp/medialink/codex/job/generated.png"
	value := sanitizedGenerationResponse(coregeneration.Response{
		Assets: []coregeneration.Asset{{
			Kind:     coregeneration.KindImage,
			MIMEType: "image/png",
			Metadata: map[string]any{"saved_path": path, "_medialink_internal_codex_image_payload": true},
		}},
		Metadata: map[string]any{
			"runtime_state": GenerationTaskRuntimeState{CodexThreadID: "thread", SavedPath: path},
			"savedPath":     path,
		},
	})
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{path, "saved_path", "savedPath", "SavedPath", "_medialink_internal_codex_image_payload"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("sanitized response log %s contains %q", encoded, forbidden)
		}
	}
}
