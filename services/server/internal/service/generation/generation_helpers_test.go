package generation

import (
	"testing"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
)

func TestGenerationResponseFromCoreMapsTypedRuntimeState(t *testing.T) {
	want := GenerationTaskRuntimeState{
		CodexThreadID: "thread-progress",
		CodexTurnID:   "turn-progress",
		CodexItemID:   "item-progress",
		SubmittedAt:   "2026-08-30T12:00:00Z",
	}

	response := GenerationResponseFromCore(coregeneration.Response{
		ID:     codexImageResponseIDPrefix + "thread-progress",
		Status: "running",
		Metadata: map[string]any{
			"runtime_state": want,
		},
	}, string(coregeneration.KindImage))

	if response.RuntimeState != want {
		t.Fatalf("RuntimeState = %+v, want %+v", response.RuntimeState, want)
	}
}

func TestGenerationResponseFromTaskMapsRuntimeState(t *testing.T) {
	want := GenerationTaskRuntimeState{CodexThreadID: "thread-stored", CodexTurnID: "turn-stored"}
	response := GenerationResponseFromTask(GenerationTaskRecord{
		ID:           "task-stored",
		Status:       "waiting_reconnect",
		RuntimeState: want,
	})
	if response.RuntimeState != want {
		t.Fatalf("RuntimeState = %+v, want %+v", response.RuntimeState, want)
	}
}

func TestGenerationProjectIDFromScopeID(t *testing.T) {
	tests := []struct {
		name    string
		scopeID string
		want    string
	}{
		{name: "project scope", scopeID: "project-alpha", want: "alpha"},
		{name: "trims whitespace", scopeID: " project-alpha ", want: "alpha"},
		{name: "studio scope", scopeID: "studio", want: ""},
		{name: "empty scope", scopeID: "", want: ""},
		{name: "sanitizes project id", scopeID: "project-alpha/beta", want: "alpha-beta"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := GenerationProjectIDFromScopeID(test.scopeID); got != test.want {
				t.Fatalf("GenerationProjectIDFromScopeID(%q) = %q, want %q", test.scopeID, got, test.want)
			}
		})
	}
}

func TestGenerationProjectIDForRequestPrefersExplicitProjectID(t *testing.T) {
	if got := GenerationProjectIDForRequest("alpha", "episode-video:episode-1:clip-1"); got != "alpha" {
		t.Fatalf("GenerationProjectIDForRequest() = %q, want alpha", got)
	}
	if got := GenerationProjectIDForRequest("", "project-beta"); got != "beta" {
		t.Fatalf("GenerationProjectIDForRequest() fallback = %q, want beta", got)
	}
}

func TestGenerationBackgroundRouting(t *testing.T) {
	tests := []struct {
		name       string
		route      coregeneration.ModelRoute
		wantRun    bool
		wantSubmit bool
	}{
		{
			name:    "synchronous image runs in server background",
			route:   coregeneration.ModelRoute{Kind: coregeneration.KindImage},
			wantRun: true,
		},
		{
			name:    "synchronous video runs in server background",
			route:   coregeneration.ModelRoute{Kind: coregeneration.KindVideo},
			wantRun: true,
		},
		{
			name:       "asynchronous video submits in server background",
			route:      coregeneration.ModelRoute{Kind: coregeneration.KindVideo, Async: true},
			wantSubmit: true,
		},
		{
			name:  "audio keeps foreground behavior",
			route: coregeneration.ModelRoute{Kind: coregeneration.KindAudio},
		},
		{
			name:  "text keeps foreground behavior",
			route: coregeneration.ModelRoute{Kind: coregeneration.KindText},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ShouldRunGenerationInBackground(test.route); got != test.wantRun {
				t.Fatalf("ShouldRunGenerationInBackground() = %v, want %v", got, test.wantRun)
			}
			if got := ShouldSubmitGenerationInBackground(test.route); got != test.wantSubmit {
				t.Fatalf("ShouldSubmitGenerationInBackground() = %v, want %v", got, test.wantSubmit)
			}
		})
	}
}

func TestGenerationResponseFromCoreIncludesVideoPosterURL(t *testing.T) {
	response := GenerationResponseFromCore(coregeneration.Response{
		ID:     "generation-video-poster",
		Status: "completed",
		Assets: []coregeneration.Asset{
			{
				Kind: coregeneration.KindVideo,
				URL:  "/api/v1/media-assets/video-with-poster/content",
				Metadata: map[string]any{
					"poster_url": "/api/v1/media-assets/video-with-poster/poster",
				},
			},
		},
	}, string(coregeneration.KindVideo))

	if len(response.Assets) != 1 || response.Assets[0].PosterURL != "/api/v1/media-assets/video-with-poster/poster" {
		t.Fatalf("assets = %#v, want poster URL from metadata", response.Assets)
	}
}

func TestGenerationResponseFromCoreFailsCompletedImageWithoutAssets(t *testing.T) {
	response := GenerationResponseFromCore(coregeneration.Response{
		ID:     "generation-empty-image",
		Status: "completed",
		Model:  "gemini-3.1-flash-image",
	}, string(coregeneration.KindImage))

	if response.Status != "failed" {
		t.Fatalf("status = %q, want failed", response.Status)
	}
	if response.Message != "图像生成失败。" {
		t.Fatalf("message = %q, want image failure", response.Message)
	}
	if response.Error != "生成请求已完成，但未返回图片素材。" {
		t.Fatalf("error = %q, want empty image asset reason", response.Error)
	}
}

func TestGenerationRequestFromMessageSelectsMediagoHappyHorseModelFromReferences(t *testing.T) {
	route, ok := coregeneration.FindRoute(coregeneration.RouteMediagoHappyHorse11)
	if !ok {
		t.Fatalf("missing route %q", coregeneration.RouteMediagoHappyHorse11)
	}
	payload := GenerationMessageRequest{
		Kind:     string(route.Kind),
		RouteID:  route.ID,
		Provider: route.Provider,
		Model:    route.Model,
		Prompt:   "animate the character",
	}

	textRequest := GenerationRequestFromMessage(payload, route, nil)
	if textRequest.Model != coregeneration.ModelHappyHorse11T2V {
		t.Fatalf("text model = %q, want %q", textRequest.Model, coregeneration.ModelHappyHorse11T2V)
	}
	referenceRequest := GenerationRequestFromMessage(payload, route, []string{"data:image/png;base64,AAAA"})
	if referenceRequest.Model != coregeneration.ModelHappyHorse11R2V {
		t.Fatalf("reference model = %q, want %q", referenceRequest.Model, coregeneration.ModelHappyHorse11R2V)
	}
}

func TestGenerationRequestFromMessageCopiesAutoDLProfileSelection(t *testing.T) {
	route, ok := coregeneration.FindRoute(coregeneration.RouteAutoDLImage)
	if !ok {
		t.Fatalf("missing route %q", coregeneration.RouteAutoDLImage)
	}
	payload := GenerationMessageRequest{
		Kind:              string(route.Kind),
		RouteID:           route.ID,
		Model:             route.Model,
		Prompt:            "portrait",
		InstanceProfileID: " instance-a ",
		WorkflowProfileID: " zimage-i2i ",
	}

	request := GenerationRequestFromMessage(payload, route, nil)
	if request.InstanceProfileID != "instance-a" {
		t.Fatalf("InstanceProfileID = %q, want instance-a", request.InstanceProfileID)
	}
	if request.WorkflowProfileID != "zimage-i2i" {
		t.Fatalf("WorkflowProfileID = %q, want zimage-i2i", request.WorkflowProfileID)
	}
}

func TestGenerationRequestFromMessageDoesNotCopyInstanceProfileOutsideAutoDL(t *testing.T) {
	for _, routeID := range []string{coregeneration.RouteCodexImage, coregeneration.RouteDMXSeedream5Lite} {
		route, ok := coregeneration.FindRoute(routeID)
		if !ok {
			t.Fatalf("missing route %q", routeID)
		}
		request := GenerationRequestFromMessage(GenerationMessageRequest{
			Kind:              string(route.Kind),
			RouteID:           route.ID,
			Model:             route.Model,
			Prompt:            "portrait",
			InstanceProfileID: "instance-must-not-leak",
			WorkflowProfileID: "workflow-must-not-leak",
		}, route, nil)
		if request.InstanceProfileID != "" {
			t.Fatalf("route %q InstanceProfileID = %q, want empty", routeID, request.InstanceProfileID)
		}
		if request.WorkflowProfileID != "" {
			t.Fatalf("route %q WorkflowProfileID = %q, want empty", routeID, request.WorkflowProfileID)
		}
	}
}

func TestGenerationTaskFromMessageSnapshotsManualAutoDLProfiles(t *testing.T) {
	route, ok := coregeneration.FindRoute(coregeneration.RouteAutoDLImage)
	if !ok {
		t.Fatalf("missing route %q", coregeneration.RouteAutoDLImage)
	}
	task := GenerationTaskFromMessage(GenerationMessageRequest{
		Kind:              string(route.Kind),
		RouteID:           route.ID,
		Model:             route.Model,
		Prompt:            "portrait",
		InstanceProfileID: " instance-a ",
		WorkflowProfileID: " zimage-i2i ",
	}, route, GenerationMessageResponse{ID: "task-zimage", Status: "waiting_for_instance"})

	if task.RuntimeState.InstanceProfileID != "instance-a" {
		t.Fatalf("RuntimeState.InstanceProfileID = %q, want instance-a", task.RuntimeState.InstanceProfileID)
	}
	if task.RuntimeState.WorkflowProfileID != "zimage-i2i" {
		t.Fatalf("RuntimeState.WorkflowProfileID = %q, want zimage-i2i", task.RuntimeState.WorkflowProfileID)
	}
}

func TestGenerationTaskFromMessageDoesNotSnapshotManualInstanceOutsideAutoDL(t *testing.T) {
	route, ok := coregeneration.FindRoute(coregeneration.RouteCodexImage)
	if !ok {
		t.Fatalf("missing route %q", coregeneration.RouteCodexImage)
	}
	task := GenerationTaskFromMessage(GenerationMessageRequest{
		Kind:              string(route.Kind),
		RouteID:           route.ID,
		Model:             route.Model,
		Prompt:            "portrait",
		InstanceProfileID: "instance-must-not-leak",
		WorkflowProfileID: "workflow-must-not-leak",
	}, route, GenerationMessageResponse{ID: "task-codex", Status: "submitted"})
	if task.RuntimeState.InstanceProfileID != "" {
		t.Fatalf("RuntimeState.InstanceProfileID = %q, want empty", task.RuntimeState.InstanceProfileID)
	}
	if task.RuntimeState.WorkflowProfileID != "" {
		t.Fatalf("RuntimeState.WorkflowProfileID = %q, want empty", task.RuntimeState.WorkflowProfileID)
	}
}

func TestGenerationTaskFromMessageRejectsProviderIdentityThatConflictsWithManualInstance(t *testing.T) {
	route, ok := coregeneration.FindRoute(coregeneration.RouteAutoDLImage)
	if !ok {
		t.Fatalf("missing route %q", coregeneration.RouteAutoDLImage)
	}
	task := GenerationTaskFromMessage(GenerationMessageRequest{
		Kind:              string(route.Kind),
		RouteID:           route.ID,
		Model:             route.Model,
		Prompt:            "portrait",
		InstanceProfileID: "instance-a",
	}, route, GenerationMessageResponse{
		ID: "task-conflicting-create", Status: "running",
		RuntimeState: GenerationTaskRuntimeState{
			InstanceProfileID:      "instance-b",
			WorkflowProfileID:      "zimage-t2i",
			WorkflowProfileVersion: "v1",
			WorkflowDigest:         "sha256:one",
		},
	})
	if task.Status != "failed" || task.ErrorCode != generationRuntimeStateConflictCode {
		t.Fatalf("task status/error = %q/%q, want explicit identity conflict", task.Status, task.ErrorCode)
	}
	if task.RuntimeState.InstanceProfileID != "instance-a" || task.RuntimeState.WorkflowProfileID != "" {
		t.Fatalf("runtime state = %+v, want pinned instance without hybrid workflow", task.RuntimeState)
	}
}

func TestGenerationTaskWithMessageIgnoresAutoDLIdentityForCodex(t *testing.T) {
	task := GenerationTaskRecord{ID: "task-codex-update", Kind: "image", RouteID: coregeneration.RouteCodexImage}
	got := GenerationTaskWithMessage(task, GenerationMessageResponse{
		Status: "running",
		RuntimeState: GenerationTaskRuntimeState{
			CodexThreadID:     "thread-1",
			InstanceProfileID: "instance-must-not-leak",
			WorkflowProfileID: "zimage-t2i",
			WorkflowDigest:    "sha256:must-not-leak",
		},
	})
	if got.Status != "running" || got.RuntimeState.CodexThreadID != "thread-1" {
		t.Fatalf("Codex task = %+v, want normal Codex checkpoint", got)
	}
	if got.RuntimeState.InstanceProfileID != "" || got.RuntimeState.WorkflowProfileID != "" || got.RuntimeState.WorkflowDigest != "" {
		t.Fatalf("Codex runtime state contains AutoDL identity: %+v", got.RuntimeState)
	}
}

func TestGenerationTaskWithMessageRejectsConflictingAutoDLAttemptIdentity(t *testing.T) {
	current := GenerationTaskRuntimeState{
		InstanceProfileID:      "instance-a",
		WorkflowProfileID:      "zimage-t2i",
		WorkflowProfileVersion: "v1",
		WorkflowDigest:         "sha256:one",
		ComfyPromptID:          "prompt-1",
		SubmittedAt:            "2026-08-30T12:00:00Z",
	}
	task := GenerationTaskRecord{ID: "task-conflict", Kind: "image", RouteID: coregeneration.RouteAutoDLImage, RuntimeState: current}
	update := current
	update.WorkflowDigest = "sha256:two"

	got := GenerationTaskWithMessage(task, GenerationMessageResponse{Status: "running", RuntimeState: update})
	if got.Status != "failed" || got.ErrorCode != generationRuntimeStateConflictCode {
		t.Fatalf("task status/error = %q/%q, want explicit runtime conflict failure", got.Status, got.ErrorCode)
	}
	if got.RuntimeState != current {
		t.Fatalf("runtime state = %+v, want unchanged %+v", got.RuntimeState, current)
	}
}

func TestGenerationTaskWithMessageRejectsUnanchoredPartialAutoDLAttemptUpdate(t *testing.T) {
	current := GenerationTaskRuntimeState{InstanceProfileID: "instance-a", WorkflowProfileID: "zimage-t2i"}
	task := GenerationTaskRecord{ID: "task-partial", Kind: "image", RouteID: coregeneration.RouteAutoDLImage, RuntimeState: current}

	got := GenerationTaskWithMessage(task, GenerationMessageResponse{
		Status:       "running",
		RuntimeState: GenerationTaskRuntimeState{ComfyPromptID: "prompt-from-unknown-attempt"},
	})
	if got.Status != "failed" || got.ErrorCode != generationRuntimeStateConflictCode {
		t.Fatalf("task status/error = %q/%q, want partial update failure", got.Status, got.ErrorCode)
	}
	if got.RuntimeState != current {
		t.Fatalf("runtime state = %+v, want unchanged %+v", got.RuntimeState, current)
	}
}

func TestGenerationTaskWithMessageAcceptsAnchoredLateAutoDLAttemptUpdate(t *testing.T) {
	current := GenerationTaskRuntimeState{
		InstanceProfileID:      "instance-a",
		WorkflowProfileID:      "zimage-t2i",
		WorkflowProfileVersion: "v1",
		WorkflowDigest:         "sha256:one",
	}
	update := current
	update.ComfyPromptID = "prompt-1"
	update.SubmittedAt = "2026-08-30T12:00:00Z"
	task := GenerationTaskRecord{ID: "task-late", Kind: "image", RouteID: coregeneration.RouteAutoDLImage, RuntimeState: current}

	got := GenerationTaskWithMessage(task, GenerationMessageResponse{Status: "running", RuntimeState: update})
	if got.Status != "running" || got.RuntimeState != update {
		t.Fatalf("task = status %q state %+v, want running %+v", got.Status, got.RuntimeState, update)
	}
}

func TestGenerationTaskWithMessageAcceptsFirstCompleteAutoDLAttemptIdentity(t *testing.T) {
	want := GenerationTaskRuntimeState{
		InstanceProfileID:      "instance-a",
		WorkflowProfileID:      "zimage-t2i",
		WorkflowProfileVersion: "v1",
		WorkflowDigest:         "sha256:one",
		ComfyPromptID:          "prompt-1",
		SubmittedAt:            "2026-08-30T12:00:00Z",
	}
	task := GenerationTaskRecord{ID: "task-first-checkpoint", Kind: "image", RouteID: coregeneration.RouteAutoDLImage}
	got := GenerationTaskWithMessage(task, GenerationMessageResponse{Status: "running", RuntimeState: want})
	if got.Status != "running" || got.RuntimeState != want {
		t.Fatalf("task = status %q state %+v, want first complete identity %+v", got.Status, got.RuntimeState, want)
	}
}

func TestGenerationTaskWithMessageRejectsPromptIDWithoutSubmissionTime(t *testing.T) {
	task := GenerationTaskRecord{ID: "task-incomplete-prompt", Kind: "image", RouteID: coregeneration.RouteAutoDLImage}
	got := GenerationTaskWithMessage(task, GenerationMessageResponse{
		Status:       "running",
		RuntimeState: GenerationTaskRuntimeState{ComfyPromptID: "prompt-1"},
	})
	if got.Status != "failed" || got.ErrorCode != generationRuntimeStateConflictCode {
		t.Fatalf("task status/error = %q/%q, want incomplete prompt identity failure", got.Status, got.ErrorCode)
	}
	if got.RuntimeState != (GenerationTaskRuntimeState{}) {
		t.Fatalf("runtime state = %+v, want empty after rejected prompt identity", got.RuntimeState)
	}
}

func TestGenerationTaskWithMessageRejectsPromptPairWithoutAutoDLAnchors(t *testing.T) {
	task := GenerationTaskRecord{ID: "task-unanchored-prompt", Kind: "image", RouteID: coregeneration.RouteAutoDLImage}
	got := GenerationTaskWithMessage(task, GenerationMessageResponse{
		Status: "running",
		RuntimeState: GenerationTaskRuntimeState{
			ComfyPromptID: "prompt-1",
			SubmittedAt:   "2026-08-30T12:00:00Z",
		},
	})
	if got.Status != "failed" || got.ErrorCode != generationRuntimeStateConflictCode {
		t.Fatalf("task status/error = %q/%q, want unanchored prompt pair failure", got.Status, got.ErrorCode)
	}
}

func TestGenerationTaskWithMessageEmptyUpdateDetectsMalformedStoredAutoDLIdentity(t *testing.T) {
	malformed := GenerationTaskRuntimeState{
		ComfyPromptID: "prompt-1",
		SubmittedAt:   "2026-08-30T12:00:00Z",
	}
	task := GenerationTaskRecord{
		ID: "task-malformed-current", Kind: "image", RouteID: coregeneration.RouteAutoDLImage,
		Status: "running", RuntimeState: malformed,
	}
	got := GenerationTaskWithMessage(task, GenerationMessageResponse{Status: "running"})
	if got.Status != "failed" || got.ErrorCode != generationRuntimeStateConflictCode {
		t.Fatalf("task status/error = %q/%q, want malformed stored identity failure", got.Status, got.ErrorCode)
	}
	if got.RuntimeState != malformed {
		t.Fatalf("runtime state = %+v, want malformed checkpoint preserved for diagnosis", got.RuntimeState)
	}
}
