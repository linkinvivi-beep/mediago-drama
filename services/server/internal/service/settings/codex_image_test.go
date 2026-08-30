package settings

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/codexapp"
)

func TestCodexImagePreflightReadyUsesSharedAccountAndCapabilityRead(t *testing.T) {
	client := &codexImagePreflightClient{responses: map[string]json.RawMessage{
		"account/read":                    json.RawMessage(`{"account":{"type":"chatgpt","email":"user@example.com","planType":"plus"}}`),
		"modelProvider/capabilities/read": json.RawMessage(`{"imageGeneration":true,"namespaceTools":true,"webSearch":true}`),
	}}
	service := newCodexImagePreflightSettings(client)

	got, err := service.GetCodexImagePreflight(context.Background())
	if err != nil {
		t.Fatalf("GetCodexImagePreflight() error = %v", err)
	}
	want := CodexImagePreflight{
		AccountStatus:   "loggedIn",
		ImageGeneration: true,
		Ready:           true,
		Reason:          CodexImageReasonReady,
	}
	if got != want {
		t.Fatalf("GetCodexImagePreflight() = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(client.methods, []string{"account/read", "modelProvider/capabilities/read"}) {
		t.Fatalf("methods = %#v, want account then capability read", client.methods)
	}
}

func TestCodexImagePreflightStopsWhenSharedAccountIsNotLoggedIn(t *testing.T) {
	client := &codexImagePreflightClient{responses: map[string]json.RawMessage{
		"account/read": json.RawMessage(`{"account":null}`),
	}}
	service := newCodexImagePreflightSettings(client)

	got, err := service.GetCodexImagePreflight(context.Background())
	if err != nil {
		t.Fatalf("GetCodexImagePreflight() error = %v", err)
	}
	want := CodexImagePreflight{AccountStatus: "notLoggedIn", Reason: CodexImageReasonNotLoggedIn}
	if got != want {
		t.Fatalf("GetCodexImagePreflight() = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(client.methods, []string{"account/read"}) {
		t.Fatalf("methods = %#v, capability read must not run before login", client.methods)
	}
}

func TestCodexImagePreflightReportsUnavailableCLIWithoutReturningSecrets(t *testing.T) {
	service := NewSettings(nil)
	service.SetCodexCLIPath("")

	got, err := service.GetCodexImagePreflight(context.Background())
	if err != nil {
		t.Fatalf("GetCodexImagePreflight() error = %v", err)
	}
	want := CodexImagePreflight{AccountStatus: "unavailable", Reason: CodexImageReasonCLIUnavailable}
	if got != want {
		t.Fatalf("GetCodexImagePreflight() = %#v, want %#v", got, want)
	}
}

func TestCodexImagePreflightReportsCapabilityUnavailable(t *testing.T) {
	client := &codexImagePreflightClient{
		responses: map[string]json.RawMessage{
			"account/read": json.RawMessage(`{"account":{"type":"chatgpt","email":"user@example.com","planType":"plus"}}`),
		},
		errors: map[string]error{"modelProvider/capabilities/read": errors.New("transport stopped")},
	}
	service := newCodexImagePreflightSettings(client)

	got, err := service.GetCodexImagePreflight(context.Background())
	if err != nil {
		t.Fatalf("GetCodexImagePreflight() error = %v", err)
	}
	want := CodexImagePreflight{AccountStatus: "loggedIn", Reason: CodexImageReasonCapabilityUnavailable}
	if got != want {
		t.Fatalf("GetCodexImagePreflight() = %#v, want %#v", got, want)
	}
}

func TestCodexImagePreflightReportsDisabledImageCapability(t *testing.T) {
	client := &codexImagePreflightClient{responses: map[string]json.RawMessage{
		"account/read":                    json.RawMessage(`{"account":{"type":"chatgpt"}}`),
		"modelProvider/capabilities/read": json.RawMessage(`{"imageGeneration":false,"namespaceTools":true,"webSearch":true}`),
	}}
	service := newCodexImagePreflightSettings(client)

	got, err := service.GetCodexImagePreflight(context.Background())
	if err != nil {
		t.Fatalf("GetCodexImagePreflight() error = %v", err)
	}
	want := CodexImagePreflight{AccountStatus: "loggedIn", Reason: CodexImageReasonCapabilityDisabled}
	if got != want {
		t.Fatalf("GetCodexImagePreflight() = %#v, want %#v", got, want)
	}
}

func newCodexImagePreflightSettings(client codexapp.Client) *Settings {
	service := NewSettings(nil)
	service.SetCodexCLIPath("/mock/codex")
	service.codexAccount.newSession = func(context.Context, string) (codexapp.Client, error) {
		return client, nil
	}
	return service
}

type codexImagePreflightClient struct {
	responses map[string]json.RawMessage
	errors    map[string]error
	methods   []string
}

func (client *codexImagePreflightClient) Call(_ context.Context, method string, _ any, response any) error {
	client.methods = append(client.methods, method)
	if err := client.errors[method]; err != nil {
		return err
	}
	raw, ok := client.responses[method]
	if !ok {
		return errors.New("unexpected method: " + method)
	}
	return json.Unmarshal(raw, response)
}

func (*codexImagePreflightClient) Next(context.Context) (codexapp.Message, error) {
	return codexapp.Message{}, errors.New("unexpected Next call")
}

func (*codexImagePreflightClient) Close() {}
