package settings

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAutoDLSettingsMigratesV1WorkflowWithoutMakingItReady(t *testing.T) {
	service, store := newAutoDLSettingsWithRawDocument(t, legacyAutoDLV1Document(t))

	got, err := service.GetAutoDLSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.WorkflowProfiles) != 1 {
		t.Fatalf("workflow profile count = %d, want 1", len(got.WorkflowProfiles))
	}
	profile := got.WorkflowProfiles[0]
	if profile.Enabled || !profile.Archived || profile.AutoSelectable || len(profile.Versions) != 1 {
		t.Fatalf("migrated profile = %#v", profile)
	}
	if profile.Versions[0].BindingStatus != AutoDLBindingStatusUnconfirmed {
		t.Fatalf("binding status = %q, want %q", profile.Versions[0].BindingStatus, AutoDLBindingStatusUnconfirmed)
	}
	if len(got.Instances) != 1 || len(got.Instances[0].WorkflowValidations) != 1 {
		t.Fatalf("migrated instances = %#v", got.Instances)
	}
	validation := got.Instances[0].WorkflowValidations[0]
	if validation.Status != AutoDLWorkflowValidationStale || validation.Reason != "migrated_v1_without_confirmed_bindings" {
		t.Fatalf("migrated validation = %#v", validation)
	}
	if !strings.Contains(store.value(autoDLSettingsKey), `"version":2`) {
		t.Fatalf("migration was not persisted: %s", store.value(autoDLSettingsKey))
	}
}

func TestReplaceAutoDLWorkflowAppendsImmutableVersion(t *testing.T) {
	service, store, _ := newAutoDLSettingsForTest()
	first, err := service.SaveValidatedAutoDLWorkflowProfile(context.Background(), AutoDLWorkflowProfileMutation{
		ID:          "portrait",
		Name:        "Portrait",
		Kind:        "image",
		Version:     "v1",
		Workflow:    json.RawMessage(`{"nodes":[]}`),
		APITemplate: json.RawMessage(`{"1":{"class_type":"Test","inputs":{}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	firstVersionID := first.CurrentVersionID
	if firstVersionID == "" || len(first.Versions) != 1 {
		t.Fatalf("first profile = %#v", first)
	}

	second, err := service.SaveValidatedAutoDLWorkflowProfile(context.Background(), AutoDLWorkflowProfileMutation{
		ID:          "portrait",
		Name:        "Portrait",
		Kind:        "image",
		Version:     "v2",
		Workflow:    json.RawMessage(`{"nodes":[{"id":1}]}`),
		APITemplate: json.RawMessage(`{"1":{"class_type":"Test","inputs":{"seed":1}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Versions) != 2 || second.Versions[0].VersionID != firstVersionID || second.CurrentVersionID == firstVersionID {
		t.Fatalf("profile versions = %#v", second)
	}
	var persisted autoDLSettingsDocument
	if err := json.Unmarshal([]byte(store.value(autoDLSettingsKey)), &persisted); err != nil {
		t.Fatal(err)
	}
	if string(persisted.WorkflowProfiles[0].Versions[0].UIWorkflow) != `{"nodes":[]}` {
		t.Fatalf("first immutable workflow = %s", persisted.WorkflowProfiles[0].Versions[0].UIWorkflow)
	}
}

func TestAutoDLWorkflowResponseOmitsRawSnapshots(t *testing.T) {
	service, _, _ := newAutoDLSettingsForTest()
	profile, err := service.SaveValidatedAutoDLWorkflowProfile(context.Background(), AutoDLWorkflowProfileMutation{
		ID: "redacted", Name: "Redacted", Version: "v1",
		Workflow:    json.RawMessage(`{"nodes":[{"id":7}]}`),
		APITemplate: json.RawMessage(`{"7":{"class_type":"SecretNode","inputs":{}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"uiWorkflow":`, `"apiTemplate":`, "SecretNode", `"nodes"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("workflow response leaked %q: %s", forbidden, encoded)
		}
	}
}

func newAutoDLSettingsWithRawDocument(t *testing.T, raw string) (*Settings, *memoryAppSettingStore) {
	t.Helper()
	store := &memoryAppSettingStore{values: map[string]string{autoDLSettingsKey: raw}}
	service := NewSettingsWithStores(&memoryAPIKeyStore{values: map[string]string{}}, nil, store)
	service.SetAutoDLPasswordStore(&fakeGenericPasswordStore{values: map[string]string{}})
	return service, store
}

func legacyAutoDLV1Document(t *testing.T) string {
	t.Helper()
	workflow := json.RawMessage(`{"nodes":[]}`)
	apiTemplate := json.RawMessage(`{"1":{"class_type":"Test","inputs":{}}}`)
	document := map[string]any{
		"version": 1,
		"instances": []any{map[string]any{
			"id": "legacy-gpu", "name": "Legacy GPU", "host": "gpu.example.com",
			"sshPort": 22, "sshUser": "root", "comfyPort": 6006,
			"credentialRef": "legacy-gpu", "enabled": true,
			"workflowValidations": []any{map[string]any{
				"workflowProfileId": "legacy-profile", "status": "ready",
				"workflowDigest": autoDLPayloadDigest(workflow), "apiTemplateDigest": autoDLPayloadDigest(apiTemplate),
			}},
		}},
		"workflowProfiles": []any{map[string]any{
			"id":                "legacy-profile",
			"name":              "Legacy profile",
			"kind":              "zimage-t2i",
			"version":           "legacy-v1",
			"status":            "ready",
			"workflow":          workflow,
			"apiTemplate":       apiTemplate,
			"workflowDigest":    autoDLPayloadDigest(workflow),
			"apiTemplateDigest": autoDLPayloadDigest(apiTemplate),
		}},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
