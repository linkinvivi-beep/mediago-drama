package codexapp

import (
	"context"
	"fmt"
)

// ModelProviderCapabilities is the typed capability response returned by Codex app-server.
type ModelProviderCapabilities struct {
	ImageGeneration bool `json:"imageGeneration"`
	NamespaceTools  bool `json:"namespaceTools"`
	WebSearch       bool `json:"webSearch"`
}

// ReadModelProviderCapabilities reads capabilities without starting a thread or turn.
func ReadModelProviderCapabilities(ctx context.Context, client Client) (ModelProviderCapabilities, error) {
	if client == nil {
		return ModelProviderCapabilities{}, fmt.Errorf("Codex app-server client is required")
	}
	var capabilities ModelProviderCapabilities
	if err := client.Call(ctx, "modelProvider/capabilities/read", struct{}{}, &capabilities); err != nil {
		return ModelProviderCapabilities{}, fmt.Errorf("reading Codex model-provider capabilities: %w", err)
	}
	return capabilities, nil
}
