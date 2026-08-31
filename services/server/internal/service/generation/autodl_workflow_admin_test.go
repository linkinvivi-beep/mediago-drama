package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/autodl"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/comfyui"
	"github.com/mediago-dev/mediago-drama/services/server/internal/service/settings"
)

func TestAutoDLWorkflowValidateUsesObjectInfoWithoutSubmittingPrompt(t *testing.T) {
	store, workflow := workflowAdminStoreFixture(t)
	client := &workflowAdminFakeComfyClient{objectInfo: workflowAdminObjectInfo(t)}
	admin := workflowAdminForTest(store, client)

	result, err := admin.Validate(context.Background(), AutoDLWorkflowValidationRequest{
		InstanceProfileID: "instance-a", WorkflowProfileID: workflow.ProfileID, VersionID: workflow.VersionID,
	})
	if err != nil || result.Status != settings.AutoDLWorkflowValidationReady {
		t.Fatalf("Validate() result=%#v error=%v", result, err)
	}
	if client.submitCalls != 0 || client.uploadCalls != 0 {
		t.Fatalf("mutating calls = submit:%d upload:%d", client.submitCalls, client.uploadCalls)
	}
	if len(store.validations) != 1 || store.validations[0].ObjectInfoDigest == "" || store.validations[0].InstanceFingerprint != "SHA256:confirmed" {
		t.Fatalf("saved validations = %#v", store.validations)
	}
}

func TestAutoDLWorkflowPreviewIsReadOnlyAndDoesNotPersist(t *testing.T) {
	store, workflow := workflowAdminStoreFixture(t)
	client := &workflowAdminFakeComfyClient{objectInfo: workflowAdminObjectInfo(t)}
	admin := workflowAdminForTest(store, client)

	preview, err := admin.Preview(context.Background(), AutoDLWorkflowPreviewRequest{
		InstanceProfileID: "instance-a", UIWorkflow: workflow.UIWorkflow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.ObjectInfoDigest == "" || len(preview.Inspection.Suggestions.Prompts) == 0 {
		t.Fatalf("Preview() = %#v", preview)
	}
	if len(store.validations) != 0 || client.submitCalls != 0 || client.uploadCalls != 0 {
		t.Fatalf("preview mutated state: validations=%d submit=%d upload=%d", len(store.validations), client.submitCalls, client.uploadCalls)
	}
}

func TestAutoDLWorkflowCreateAndReplaceCompileServerSideReadOnly(t *testing.T) {
	store, workflow := workflowAdminStoreFixture(t)
	client := &workflowAdminFakeComfyClient{objectInfo: workflowAdminObjectInfo(t)}
	admin := workflowAdminForTest(store, client)

	created, err := admin.Create(context.Background(), AutoDLWorkflowCreateRequest{
		InstanceProfileID: "instance-a", ID: "new-profile", Name: "New Profile",
		UIWorkflow: workflow.UIWorkflow, Bindings: workflow.Bindings, References: settings.AutoDLReferenceContract{Min: 0, Max: 0},
	})
	if err != nil || created.ID != "new-profile" || store.created == nil {
		t.Fatalf("Create()=%#v error=%v mutation=%#v", created, err, store.created)
	}
	if store.created.Compiled.APITemplateDigest == "" || store.created.Compiled.WorkflowDigest == "" {
		t.Fatalf("server compiled mutation = %#v", store.created)
	}
	replaced, err := admin.Replace(context.Background(), "portrait", AutoDLWorkflowReplaceRequest{
		InstanceProfileID: "instance-a", ExpectedCurrentVersionID: "portrait-v1",
		UIWorkflow: workflow.UIWorkflow, Bindings: workflow.Bindings, References: settings.AutoDLReferenceContract{Min: 0, Max: 0},
	})
	if err != nil || replaced.ID != "portrait" || store.replaced == nil {
		t.Fatalf("Replace()=%#v error=%v mutation=%#v", replaced, err, store.replaced)
	}
	if client.submitCalls != 0 || client.uploadCalls != 0 {
		t.Fatalf("mutating calls = submit:%d upload:%d", client.submitCalls, client.uploadCalls)
	}
}

func TestAutoDLScanFingerprintDoesNotPersist(t *testing.T) {
	store, _ := workflowAdminStoreFixture(t)
	scanner := &workflowAdminFakeScanner{fingerprint: "SHA256:observed"}
	admin := workflowAdminForTest(store, &workflowAdminFakeComfyClient{})
	admin.scanner = scanner
	got, err := admin.ScanFingerprint(context.Background(), "instance-a")
	if err != nil || got.Fingerprint != "SHA256:observed" {
		t.Fatalf("ScanFingerprint()=%#v error=%v", got, err)
	}
	if scanner.host != "gpu.example.com" || scanner.port != 16109 || len(store.validations) != 0 {
		t.Fatalf("scanner=%#v validations=%#v", scanner, store.validations)
	}
}

func TestAutoDLInstanceCheckReturnsPortAndHealthWithoutBaseURL(t *testing.T) {
	store, _ := workflowAdminStoreFixture(t)
	client := &workflowAdminFakeComfyClient{stats: comfyui.SystemStats{
		System:  comfyui.SystemInfo{ComfyUIVersion: "0.3.60"},
		Devices: []comfyui.DeviceInfo{{Name: "GPU 0"}},
	}}
	admin := workflowAdminForTest(store, client)

	got, err := admin.CheckInstance(context.Background(), "instance-a")
	encoded, marshalErr := json.Marshal(got)
	if err != nil || marshalErr != nil || !got.Connected || got.LocalPort != 42123 || got.ComfyUIVersion != "0.3.60" {
		t.Fatalf("CheckInstance()=%#v error=%v marshal=%v", got, err, marshalErr)
	}
	if bytes.Contains(encoded, []byte("baseUrl")) || bytes.Contains(encoded, []byte("127.0.0.1")) {
		t.Fatalf("check response leaks tunnel origin: %s", encoded)
	}
}

func TestAutoDLWorkflowValidationRejectsDigestMismatchWithoutMutation(t *testing.T) {
	store, workflow := workflowAdminStoreFixture(t)
	workflow.WorkflowDigest = "wrong-digest"
	store.workflow = workflow
	client := &workflowAdminFakeComfyClient{objectInfo: workflowAdminObjectInfo(t)}
	admin := workflowAdminForTest(store, client)

	result, err := admin.Validate(context.Background(), AutoDLWorkflowValidationRequest{
		InstanceProfileID: "instance-a", WorkflowProfileID: workflow.ProfileID, VersionID: workflow.VersionID,
	})
	if !errors.Is(err, ErrAutoDLWorkflowValidationInvalid) || result.Status != settings.AutoDLWorkflowValidationInvalid {
		t.Fatalf("Validate() result=%#v error=%v", result, err)
	}
	if client.submitCalls != 0 || client.uploadCalls != 0 {
		t.Fatalf("mutating calls = submit:%d upload:%d", client.submitCalls, client.uploadCalls)
	}
}

func TestAutoDLWorkflowValidationRejectsIncompatibleObjectInfoReadOnly(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(comfyui.ObjectInfo)
	}{
		{name: "missing class", mutate: func(info comfyui.ObjectInfo) { delete(info, "CheckpointLoaderSimple") }},
		{name: "missing bound input", mutate: func(info comfyui.ObjectInfo) {
			definition := info["CLIPTextEncode"]
			delete(definition.Input.Required, "text")
			definition.Input.RequiredOrder = []string{"clip"}
			info["CLIPTextEncode"] = definition
		}},
		{name: "unavailable model enum", mutate: func(info comfyui.ObjectInfo) {
			definition := info["CheckpointLoaderSimple"]
			definition.Input.Required["ckpt_name"] = json.RawMessage(`[["missing.safetensors"]]`)
			info["CheckpointLoaderSimple"] = definition
		}},
		{name: "output node no longer output", mutate: func(info comfyui.ObjectInfo) {
			definition := info["SaveImage"]
			definition.OutputNode = false
			info["SaveImage"] = definition
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, workflow := workflowAdminStoreFixture(t)
			objectInfo := workflowAdminObjectInfo(t)
			test.mutate(objectInfo)
			client := &workflowAdminFakeComfyClient{objectInfo: objectInfo}
			admin := workflowAdminForTest(store, client)
			result, err := admin.Validate(context.Background(), AutoDLWorkflowValidationRequest{
				InstanceProfileID: "instance-a", WorkflowProfileID: workflow.ProfileID, VersionID: workflow.VersionID,
			})
			if !errors.Is(err, ErrAutoDLWorkflowValidationInvalid) || result.Status != settings.AutoDLWorkflowValidationInvalid {
				t.Fatalf("Validate() result=%#v error=%v", result, err)
			}
			if client.submitCalls != 0 || client.uploadCalls != 0 {
				t.Fatalf("mutating calls = submit:%d upload:%d", client.submitCalls, client.uploadCalls)
			}
		})
	}
}

func TestAutoDLWorkflowAdminFailsClosedBeforeComfyRead(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*workflowAdminFakeStore, *workflowAdminFakeTunnels)
		want   error
	}{
		{name: "wrong version", mutate: func(store *workflowAdminFakeStore, _ *workflowAdminFakeTunnels) {
			store.workflow.VersionID = "different-version"
		}, want: settings.ErrAutoDLWorkflowVersionNotFound},
		{name: "disabled instance", mutate: func(store *workflowAdminFakeStore, _ *workflowAdminFakeTunnels) {
			store.instance.Enabled = false
		}, want: settings.ErrAutoDLWorkflowUnavailable},
		{name: "missing fingerprint", mutate: func(store *workflowAdminFakeStore, _ *workflowAdminFakeTunnels) {
			store.instance.HostFingerprint = ""
		}, want: settings.ErrAutoDLWorkflowUnavailable},
		{name: "missing password", mutate: func(_ *workflowAdminFakeStore, tunnels *workflowAdminFakeTunnels) {
			tunnels.err = settings.ErrAutoDLPasswordStoreUnavailable
		}, want: settings.ErrAutoDLPasswordStoreUnavailable},
		{name: "canceled tunnel", mutate: func(_ *workflowAdminFakeStore, tunnels *workflowAdminFakeTunnels) {
			tunnels.err = context.Canceled
		}, want: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, workflow := workflowAdminStoreFixture(t)
			client := &workflowAdminFakeComfyClient{objectInfo: workflowAdminObjectInfo(t)}
			tunnels := &workflowAdminFakeTunnels{tunnel: autodl.Tunnel{InstanceProfileID: "instance-a", BaseURL: "http://127.0.0.1:42123"}}
			test.mutate(store, tunnels)
			admin := workflowAdminForTest(store, client)
			admin.tunnels = tunnels
			_, err := admin.Validate(context.Background(), AutoDLWorkflowValidationRequest{
				InstanceProfileID: "instance-a", WorkflowProfileID: workflow.ProfileID, VersionID: workflow.VersionID,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
			if client.submitCalls != 0 || client.uploadCalls != 0 {
				t.Fatalf("mutating calls = submit:%d upload:%d", client.submitCalls, client.uploadCalls)
			}
		})
	}
}

func workflowAdminForTest(store *workflowAdminFakeStore, client *workflowAdminFakeComfyClient) *AutoDLWorkflowAdmin {
	return &AutoDLWorkflowAdmin{
		settings: store,
		tunnels:  workflowAdminFakeTunnels{tunnel: autodl.Tunnel{InstanceProfileID: "instance-a", BaseURL: "http://127.0.0.1:42123"}},
		client:   func(string) (comfyui.Client, error) { return client, nil },
		clock:    func() time.Time { return time.Date(2026, 8, 31, 1, 2, 3, 0, time.UTC) },
	}
}

func workflowAdminStoreFixture(t *testing.T) (*workflowAdminFakeStore, settings.ResolvedAutoDLWorkflow) {
	t.Helper()
	ui := workflowAdminFixture(t, "ui_t2i.json")
	objectInfo := workflowAdminObjectInfo(t)
	inspection, err := comfyui.InspectUIWorkflow(ui, objectInfo)
	if err != nil {
		t.Fatal(err)
	}
	bindings := comfyui.WorkflowBindings{
		Confirmed: true, Prompts: inspection.Suggestions.Prompts, Outputs: inspection.Suggestions.Outputs, Parameters: inspection.Suggestions.Parameters,
	}
	compiled, err := comfyui.CompileUIWorkflow(ui, objectInfo, bindings)
	if err != nil {
		t.Fatal(err)
	}
	workflow := settings.ResolvedAutoDLWorkflow{
		ProfileID: "portrait", VersionID: "portrait-v1", Name: "Portrait",
		UIWorkflow: compiled.UIWorkflow, APITemplate: compiled.APITemplate, Bindings: bindings,
		WorkflowDigest: compiled.WorkflowDigest, APITemplateDigest: compiled.APITemplateDigest,
		References: settings.AutoDLReferenceContract{Min: 0, Max: 0},
	}
	return &workflowAdminFakeStore{
		instance: settings.AutoDLInstanceProfile{
			ID: "instance-a", Host: "gpu.example.com", SSHPort: 16109, SSHUser: "root", ComfyPort: 6006,
			CredentialRef: "instance-a", HostFingerprint: "SHA256:confirmed", Enabled: true,
		},
		workflow: workflow,
	}, workflow
}

func workflowAdminObjectInfo(t *testing.T) comfyui.ObjectInfo {
	t.Helper()
	var objectInfo comfyui.ObjectInfo
	if err := json.Unmarshal(workflowAdminFixture(t, "object_info.json"), &objectInfo); err != nil {
		t.Fatal(err)
	}
	return objectInfo
}

func workflowAdminFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "platform", "comfyui", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type workflowAdminFakeStore struct {
	instance    settings.AutoDLInstanceProfile
	workflow    settings.ResolvedAutoDLWorkflow
	validations []settings.AutoDLWorkflowValidation
	created     *settings.AutoDLWorkflowCreateMutation
	replaced    *settings.AutoDLWorkflowVersionMutation
}

func (store *workflowAdminFakeStore) GetAutoDLInstance(context.Context, string) (settings.AutoDLInstanceProfile, error) {
	return store.instance, nil
}

func (store *workflowAdminFakeStore) GetAutoDLWorkflowVersion(context.Context, string, string) (settings.ResolvedAutoDLWorkflow, error) {
	return store.workflow, nil
}

func (store *workflowAdminFakeStore) CreateAutoDLWorkflow(_ context.Context, mutation settings.AutoDLWorkflowCreateMutation) (settings.AutoDLWorkflowProfileResponse, error) {
	store.created = &mutation
	return settings.AutoDLWorkflowProfileResponse{ID: mutation.ID}, nil
}

func (store *workflowAdminFakeStore) ReplaceAutoDLWorkflow(_ context.Context, id string, mutation settings.AutoDLWorkflowVersionMutation) (settings.AutoDLWorkflowProfileResponse, error) {
	store.replaced = &mutation
	return settings.AutoDLWorkflowProfileResponse{ID: id}, nil
}

func (store *workflowAdminFakeStore) SaveAutoDLWorkflowValidation(_ context.Context, _ string, validation settings.AutoDLWorkflowValidation) (settings.AutoDLInstanceResponse, error) {
	store.validations = append(store.validations, validation)
	return settings.AutoDLInstanceResponse{}, nil
}

type workflowAdminFakeTunnels struct {
	tunnel autodl.Tunnel
	err    error
}

type workflowAdminFakeScanner struct {
	fingerprint string
	host        string
	port        int
}

func (scanner *workflowAdminFakeScanner) Scan(_ context.Context, host string, port int) (string, error) {
	scanner.host, scanner.port = host, port
	return scanner.fingerprint, nil
}

func (fake workflowAdminFakeTunnels) Ensure(context.Context, autodl.TunnelTarget) (autodl.Tunnel, error) {
	return fake.tunnel, fake.err
}
func (workflowAdminFakeTunnels) Close(string) error { return nil }
func (workflowAdminFakeTunnels) CloseAll() error    { return nil }

type workflowAdminFakeComfyClient struct {
	stats       comfyui.SystemStats
	objectInfo  comfyui.ObjectInfo
	uploadCalls int
	submitCalls int
}

func (fake *workflowAdminFakeComfyClient) SystemStats(context.Context) (comfyui.SystemStats, error) {
	return fake.stats, nil
}
func (fake *workflowAdminFakeComfyClient) ObjectInfo(context.Context) (comfyui.ObjectInfo, error) {
	return fake.objectInfo, nil
}
func (*workflowAdminFakeComfyClient) Queue(context.Context) (comfyui.QueueState, error) {
	return comfyui.QueueState{}, errors.New("unexpected queue call")
}
func (fake *workflowAdminFakeComfyClient) UploadImage(context.Context, comfyui.UploadImageRequest) (comfyui.UploadedImage, error) {
	fake.uploadCalls++
	return comfyui.UploadedImage{}, errors.New("unexpected upload call")
}
func (fake *workflowAdminFakeComfyClient) SubmitPrompt(context.Context, json.RawMessage, string) (comfyui.PromptSubmission, error) {
	fake.submitCalls++
	return comfyui.PromptSubmission{}, errors.New("unexpected prompt call")
}
func (*workflowAdminFakeComfyClient) History(context.Context, string) (comfyui.PromptHistory, error) {
	return comfyui.PromptHistory{}, errors.New("unexpected history call")
}
func (*workflowAdminFakeComfyClient) View(context.Context, comfyui.OutputFile) (io.ReadCloser, http.Header, error) {
	return nil, nil, errors.New("unexpected view call")
}
func (*workflowAdminFakeComfyClient) DeleteQueuedPrompt(context.Context, string) (bool, error) {
	return false, errors.New("unexpected delete call")
}
