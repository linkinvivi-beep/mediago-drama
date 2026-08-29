package codexapp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type recordedCall struct {
	method string
	params any
}

type recordingClient struct {
	calls      []recordedCall
	callResult json.RawMessage
	call       func(string, any, any) error
	messages   []Message
}

func (client *recordingClient) Call(_ context.Context, method string, params any, output any) error {
	client.calls = append(client.calls, recordedCall{method: method, params: params})
	if client.call != nil {
		return client.call(method, params, output)
	}
	if output != nil && len(client.callResult) > 0 {
		return json.Unmarshal(client.callResult, output)
	}
	return nil
}

func (client *recordingClient) Next(context.Context) (Message, error) {
	if len(client.messages) == 0 {
		return Message{}, errors.New("no more messages")
	}
	message := client.messages[0]
	client.messages = client.messages[1:]
	return message, nil
}

func (*recordingClient) Close() {}

func TestImageGenerationStartsWorkspaceWriteTurnWithOrderedLocalImages(t *testing.T) {
	jobDir := t.TempDir()
	first := writeImageReference(t, jobDir, "first.png")
	second := writeImageReference(t, jobDir, "second.jpg")
	client := &recordingClient{}
	client.call = func(method string, _ any, output any) error {
		switch method {
		case "thread/start":
			return decodeTestResult(output, `{"thread":{"id":"thread-1"}}`)
		case "turn/start":
			return decodeTestResult(output, `{"turn":{"id":"turn-1"}}`)
		default:
			return errors.New("unexpected method: " + method)
		}
	}
	client.messages = successfulImageMessages("thread-1", "turn-1", "item-1", filepath.Join(jobDir, "result.png"))

	result, err := NewImageGenerationSession(client).GenerateImage(context.Background(), ImageGenerationRequest{
		JobDir:         jobDir,
		Prompt:         "a paper theatre",
		ReferencePaths: []string{first, second},
	}, nil)
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if result.ThreadID != "thread-1" || result.TurnID != "turn-1" || result.Item.ID != "item-1" {
		t.Fatalf("GenerateImage() = %#v", result)
	}
	if len(client.calls) != 2 {
		t.Fatalf("calls = %#v, want thread/start then turn/start", client.calls)
	}

	threadParams := client.calls[0].params.(map[string]any)
	if client.calls[0].method != "thread/start" || threadParams["cwd"] != jobDir || threadParams["sandbox"] != "workspace-write" || threadParams["approvalPolicy"] != "never" {
		t.Fatalf("thread/start = %#v", client.calls[0])
	}
	if _, exists := threadParams["runtimeWorkspaceRoots"]; exists {
		t.Fatalf("thread/start includes experimental runtimeWorkspaceRoots: %#v", client.calls[0])
	}
	turnParams := client.calls[1].params.(map[string]any)
	if client.calls[1].method != "turn/start" || turnParams["threadId"] != "thread-1" {
		t.Fatalf("turn/start = %#v", client.calls[1])
	}
	wantInput := []ImageGenerationInput{
		{Type: "text", Text: "$imagegen\na paper theatre"},
		{Type: "localImage", Path: first},
		{Type: "localImage", Path: second},
	}
	if !reflect.DeepEqual(turnParams["input"], wantInput) {
		t.Fatalf("turn input = %#v, want %#v", turnParams["input"], wantInput)
	}
}

func TestImageGenerationRejectsInvalidReferenceBeforeStartingThread(t *testing.T) {
	jobDir := t.TempDir()
	client := &recordingClient{}

	_, err := NewImageGenerationSession(client).GenerateImage(context.Background(), ImageGenerationRequest{
		JobDir:         jobDir,
		Prompt:         "test",
		ReferencePaths: []string{"relative.png"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("GenerateImage() error = %v, want absolute-path validation", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("calls = %#v, want no RPC requests", client.calls)
	}
}

func TestImageGenerationUsesOnlyCompletedStructuredImageItem(t *testing.T) {
	jobDir := t.TempDir()
	savedPath := filepath.Join(jobDir, "result.png")
	revised := "a refined paper theatre"
	client := imageGenerationClient("thread-1", "turn-1")
	client.messages = []Message{
		notification("item/completed", map[string]any{
			"threadId": "thread-1", "turnId": "turn-1",
			"item": map[string]any{"id": "prose-1", "type": "agentMessage", "text": "![not accepted](/tmp/fake.png)"},
		}),
		notification("item/started", map[string]any{
			"threadId": "thread-1", "turnId": "turn-1", "startedAtMs": 1,
			"item": map[string]any{"id": "item-1", "type": "imageGeneration", "result": "", "status": "inProgress", "failure": nil, "revisedPrompt": nil, "savedPath": nil, "transparentBackground": nil},
		}),
		notification("item/completed", map[string]any{
			"threadId": "other", "turnId": "turn-1", "completedAtMs": 2,
			"item": map[string]any{"id": "wrong-thread", "type": "imageGeneration", "result": "", "status": "completed", "failure": nil, "savedPath": "/tmp/wrong.png"},
		}),
		notification("item/completed", map[string]any{
			"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 3,
			"item": map[string]any{"id": "item-1", "type": "imageGeneration", "result": "success", "status": "completed", "failure": nil, "revisedPrompt": revised, "savedPath": savedPath, "transparentBackground": false},
		}),
		notification("turn/completed", map[string]any{
			"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "completed", "items": []any{}, "error": nil},
		}),
	}
	var checkpoints []ImageGenerationCheckpoint

	result, err := NewImageGenerationSession(client).GenerateImage(context.Background(), ImageGenerationRequest{JobDir: jobDir, Prompt: "test"}, func(checkpoint ImageGenerationCheckpoint) {
		checkpoints = append(checkpoints, checkpoint)
	})
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if result.Item.SavedPath == nil || *result.Item.SavedPath != savedPath || result.Item.RevisedPrompt == nil || *result.Item.RevisedPrompt != revised {
		t.Fatalf("GenerateImage() = %#v", result)
	}
	wantStages := []string{"thread_started", "turn_started", "item_started", "item_completed", "turn_completed"}
	gotStages := make([]string, len(checkpoints))
	for index := range checkpoints {
		gotStages[index] = checkpoints[index].Stage
	}
	if !reflect.DeepEqual(gotStages, wantStages) {
		t.Fatalf("checkpoint stages = %#v, want %#v", gotStages, wantStages)
	}
}

func TestImageGenerationDoesNotAcceptFailedOrIncompleteImageItem(t *testing.T) {
	tests := []struct {
		name string
		item map[string]any
	}{
		{name: "failed", item: map[string]any{"id": "item-1", "type": "imageGeneration", "result": "", "status": "failed", "failure": map[string]any{"type": "usageLimitExceeded", "limitId": "image_generation"}, "savedPath": nil}},
		{name: "missing saved path", item: map[string]any{"id": "item-1", "type": "imageGeneration", "result": "", "status": "completed", "failure": nil, "savedPath": nil}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			jobDir := t.TempDir()
			client := imageGenerationClient("thread-1", "turn-1")
			client.messages = []Message{
				notification("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 1, "item": test.item}),
				notification("turn/completed", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "completed", "items": []any{}, "error": nil}}),
			}

			_, err := NewImageGenerationSession(client).GenerateImage(context.Background(), ImageGenerationRequest{JobDir: jobDir, Prompt: "test"}, nil)
			if err == nil {
				t.Fatal("GenerateImage() error = nil, want invalid image item error")
			}
		})
	}
}

func TestImageGenerationThreadItemDecodesExactSchemaFields(t *testing.T) {
	raw := `{"id":"item-1","result":"success","revisedPrompt":"refined","savedPath":"/tmp/result.png","status":"completed","failure":null,"transparentBackground":true,"type":"imageGeneration"}`
	var item ImageGenerationThreadItem
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if item.ID != "item-1" || item.Result != "success" || item.RevisedPrompt == nil || *item.RevisedPrompt != "refined" || item.SavedPath == nil || *item.SavedPath != "/tmp/result.png" || item.Status != "completed" || item.Failure != nil || item.TransparentBackground == nil || !*item.TransparentBackground || item.Type != "imageGeneration" {
		t.Fatalf("decoded item = %#v", item)
	}
}

func imageGenerationClient(threadID string, turnID string) *recordingClient {
	client := &recordingClient{}
	client.call = func(method string, _ any, output any) error {
		switch method {
		case "thread/start":
			return decodeTestResult(output, `{"thread":{"id":"`+threadID+`"}}`)
		case "turn/start":
			return decodeTestResult(output, `{"turn":{"id":"`+turnID+`"}}`)
		default:
			return errors.New("unexpected method: " + method)
		}
	}
	return client
}

func successfulImageMessages(threadID string, turnID string, itemID string, savedPath string) []Message {
	return []Message{
		notification("item/completed", map[string]any{
			"threadId": threadID, "turnId": turnID, "completedAtMs": 1,
			"item": map[string]any{"id": itemID, "type": "imageGeneration", "result": "success", "status": "completed", "failure": nil, "savedPath": savedPath},
		}),
		notification("turn/completed", map[string]any{
			"threadId": threadID, "turn": map[string]any{"id": turnID, "status": "completed", "items": []any{}, "error": nil},
		}),
	}
}

func notification(method string, params any) Message {
	raw, err := json.Marshal(params)
	if err != nil {
		panic(err)
	}
	return Message{Method: method, Params: raw}
}

func decodeTestResult(output any, raw string) error {
	return json.Unmarshal([]byte(raw), output)
}

func writeImageReference(t *testing.T, dir string, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("reference"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
