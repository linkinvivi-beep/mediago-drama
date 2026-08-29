package codexapp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestModelProviderCapabilitiesUsesTypedReadRequest(t *testing.T) {
	client := &recordingClient{
		callResult: json.RawMessage(`{"imageGeneration":true,"namespaceTools":true,"webSearch":true}`),
	}

	capabilities, err := ReadModelProviderCapabilities(context.Background(), client)
	if err != nil {
		t.Fatalf("ReadModelProviderCapabilities() error = %v", err)
	}
	if capabilities != (ModelProviderCapabilities{ImageGeneration: true, NamespaceTools: true, WebSearch: true}) {
		t.Fatalf("ReadModelProviderCapabilities() = %#v", capabilities)
	}
	if len(client.calls) != 1 {
		t.Fatalf("calls = %#v, want one capability request", client.calls)
	}
	if client.calls[0].method != "modelProvider/capabilities/read" {
		t.Fatalf("first method = %q", client.calls[0].method)
	}
	if !reflect.DeepEqual(client.calls[0].params, struct{}{}) {
		t.Fatalf("capability params = %#v, want empty object", client.calls[0].params)
	}
}

func TestModelProviderCapabilitiesReportsImageGenerationUnavailableWithoutLoginError(t *testing.T) {
	client := &recordingClient{
		callResult: json.RawMessage(`{"imageGeneration":false,"namespaceTools":true,"webSearch":true}`),
	}

	capabilities, err := ReadModelProviderCapabilities(context.Background(), client)
	if err != nil {
		t.Fatalf("ReadModelProviderCapabilities() error = %v, want typed unavailable result", err)
	}
	if capabilities.ImageGeneration {
		t.Fatalf("ImageGeneration = true, want false")
	}
}
