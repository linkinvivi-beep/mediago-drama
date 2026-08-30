package generation

import (
	"encoding/json"
	"strings"
	"testing"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
)

func TestSanitizedGenerationResponseOmitsSavedPaths(t *testing.T) {
	path := "/private/tmp/medialink/codex/job/generated.png"
	value := sanitizedGenerationResponse(coregeneration.Response{
		Assets: []coregeneration.Asset{{
			Kind:     coregeneration.KindImage,
			MIMEType: "image/png",
			Metadata: map[string]any{"saved_path": path},
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
	for _, forbidden := range []string{path, "saved_path", "savedPath", "SavedPath"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("sanitized response log %s contains %q", encoded, forbidden)
		}
	}
}
