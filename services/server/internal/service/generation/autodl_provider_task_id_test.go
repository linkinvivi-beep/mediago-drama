package generation

import (
	"testing"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
)

func TestAutoDLImageProviderTaskIDRoundTripUsesStrictEncodedSegments(t *testing.T) {
	const want = "autodl.image:aW5zdGFuY2UtYQ:cHJvbXB0OjEv5rWL6K-V"
	got, err := encodeAutoDLImageProviderTaskID("instance-a", "prompt:1/测试")
	if err != nil || got != want {
		t.Fatalf("encodeAutoDLImageProviderTaskID() = %q, %v; want %q", got, err, want)
	}
	instanceID, promptID, err := parseAutoDLImageProviderTaskID(want)
	if err != nil || instanceID != "instance-a" || promptID != "prompt:1/测试" {
		t.Fatalf("parseAutoDLImageProviderTaskID() = %q, %q, %v", instanceID, promptID, err)
	}
}

func TestParseAutoDLImageProviderTaskIDRejectsMalformedValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "wrong route", value: "autodl.minimax-h3:aW5zdGFuY2UtYQ:cHJvbXB0LTE"},
		{name: "empty instance", value: "autodl.image::cHJvbXB0LTE"},
		{name: "empty prompt", value: "autodl.image:aW5zdGFuY2UtYQ:"},
		{name: "invalid encoding", value: "autodl.image:***:cHJvbXB0LTE"},
		{name: "padded encoding", value: "autodl.image:aW5zdGFuY2UtYQ==:cHJvbXB0LTE"},
		{name: "extra segment", value: "autodl.image:aW5zdGFuY2UtYQ:cHJvbXB0LTE:extra"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := parseAutoDLImageProviderTaskID(test.value); err == nil {
				t.Fatalf("parseAutoDLImageProviderTaskID(%q) accepted malformed value", test.value)
			}
		})
	}
}

func TestValidateAutoDLProviderCheckpointRequiresExactZImageInstanceAndPrompt(t *testing.T) {
	state := completeAutoDLRuntimeIdentity("zimage-t2i")
	valid, err := encodeAutoDLImageProviderTaskID("instance-a", "prompt-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAutoDLProviderCheckpoint(coregeneration.RouteAutoDLImage, state, valid); err != nil {
		t.Fatalf("valid checkpoint rejected: %v", err)
	}
	wrongInstance, err := encodeAutoDLImageProviderTaskID("instance-b", "prompt-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAutoDLProviderCheckpoint(coregeneration.RouteAutoDLImage, state, wrongInstance); err == nil {
		t.Fatal("wrong instance checkpoint accepted")
	}
	encodedPrompt, err := encodeAutoDLImageProviderTaskID("instance-a", "prompt:1/测试")
	if err != nil {
		t.Fatal(err)
	}
	state.ComfyPromptID = "prompt:1/测试"
	if err := validateAutoDLProviderCheckpoint(coregeneration.RouteAutoDLImage, state, encodedPrompt); err != nil {
		t.Fatalf("encoded prompt checkpoint rejected: %v", err)
	}
}

func TestValidateAutoDLProviderCheckpointKeepsH3FormatRouteSpecific(t *testing.T) {
	state := completeAutoDLRuntimeIdentity("h3-ref2va")
	if err := validateAutoDLProviderCheckpoint(coregeneration.RouteAutoDLH3, state, coregeneration.RouteAutoDLH3+":prompt-1"); err != nil {
		t.Fatalf("valid H3 checkpoint rejected: %v", err)
	}
	for _, value := range []string{
		coregeneration.RouteAutoDLH3 + ":wrong-prompt",
		coregeneration.RouteAutoDLH3 + ":prompt-1:extra",
		coregeneration.RouteAutoDLImage + ":prompt-1",
	} {
		if err := validateAutoDLProviderCheckpoint(coregeneration.RouteAutoDLH3, state, value); err == nil {
			t.Fatalf("invalid H3 provider task ID %q accepted", value)
		}
	}
}
