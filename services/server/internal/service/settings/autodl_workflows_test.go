package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/comfyui"
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

func TestResolveAutoDLWorkflowFailsClosedOnAmbiguousDefaults(t *testing.T) {
	service := seededGenericWorkflowSettingsWithRawDefaults(t, []AutoDLWorkflowDefault{
		{ID: "one-a", MinReferences: 1, MaxReferences: 1, WorkflowProfileID: "workflow-a"},
		{ID: "one-b", MinReferences: 1, MaxReferences: 1, WorkflowProfileID: "workflow-b"},
	})
	_, err := service.ResolveAutoDLWorkflow(context.Background(), AutoDLWorkflowResolveRequest{ReferenceCount: 1, ForNewTask: true})
	if !errors.Is(err, ErrAutoDLWorkflowDefaultAmbiguous) {
		t.Fatalf("error = %v, want ErrAutoDLWorkflowDefaultAmbiguous", err)
	}
}

func TestArchiveKeepsHistoricalVersionResolvableButBlocksNewSelection(t *testing.T) {
	service, identity := seededEnabledWorkflow(t, "portrait")
	if _, err := service.SetAutoDLWorkflowState(context.Background(), identity.ProfileID, AutoDLWorkflowStateMutation{Archived: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveAutoDLWorkflow(context.Background(), AutoDLWorkflowResolveRequest{WorkflowProfileID: identity.ProfileID, ForNewTask: true}); !errors.Is(err, ErrAutoDLWorkflowUnavailable) {
		t.Fatalf("new-task error = %v, want ErrAutoDLWorkflowUnavailable", err)
	}
	resolved, err := service.GetAutoDLWorkflowVersion(context.Background(), identity.ProfileID, identity.VersionID)
	if err != nil || resolved.VersionID != identity.VersionID {
		t.Fatalf("historical version = %#v, error = %v", resolved, err)
	}
}

func TestSetAutoDLWorkflowDefaultsRejectsOverlappingRanges(t *testing.T) {
	service, _ := seededEnabledWorkflow(t, "portrait")
	_, _ = seedEnabledWorkflowOnService(t, service, "landscape")
	_, err := service.SetAutoDLWorkflowDefaults(context.Background(), []AutoDLWorkflowDefault{
		{ID: "first", MinReferences: 0, MaxReferences: 1, WorkflowProfileID: "portrait"},
		{ID: "second", MinReferences: 1, MaxReferences: 1, WorkflowProfileID: "landscape"},
	})
	if !errors.Is(err, ErrAutoDLWorkflowDefaultOverlap) {
		t.Fatalf("error = %v, want ErrAutoDLWorkflowDefaultOverlap", err)
	}
}

func TestSetAutoDLWorkflowDefaultsRejectsRangeOutsideWorkflowContract(t *testing.T) {
	service, _ := seededEnabledWorkflow(t, "portrait")
	_, err := service.SetAutoDLWorkflowDefaults(context.Background(), []AutoDLWorkflowDefault{
		{ID: "too-many-references", MinReferences: 0, MaxReferences: 2, WorkflowProfileID: "portrait"},
	})
	if !errors.Is(err, ErrAutoDLSettingsInvalid) {
		t.Fatalf("error = %v, want ErrAutoDLSettingsInvalid", err)
	}
}

func TestDuplicateAutoDLWorkflowStartsDisabledWithIndependentVersion(t *testing.T) {
	service, source := seededEnabledWorkflow(t, "portrait")
	duplicate, err := service.DuplicateAutoDLWorkflow(context.Background(), source.ProfileID, AutoDLWorkflowDuplicateMutation{ID: "portrait-copy", Name: "Portrait copy"})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Enabled || duplicate.AutoSelectable || duplicate.Archived || duplicate.ID != "portrait-copy" || duplicate.CurrentVersionID == source.VersionID {
		t.Fatalf("duplicate = %#v", duplicate)
	}
	if len(duplicate.Versions) != 1 || duplicate.Versions[0].SourceVersionID != source.VersionID {
		t.Fatalf("duplicate versions = %#v", duplicate.Versions)
	}
}

func TestReplaceAutoDLWorkflowRejectsStaleExpectedVersion(t *testing.T) {
	service, identity := seededEnabledWorkflow(t, "portrait")
	_, err := service.ReplaceAutoDLWorkflow(context.Background(), identity.ProfileID, AutoDLWorkflowVersionMutation{
		ExpectedCurrentVersionID: "stale-version", Compiled: genericCompiledWorkflow(2), References: AutoDLReferenceContract{Min: 0, Max: 1},
	})
	if !errors.Is(err, ErrAutoDLWorkflowVersionConflict) {
		t.Fatalf("error = %v, want ErrAutoDLWorkflowVersionConflict", err)
	}
}

func TestReplaceAutoDLWorkflowAppendsVersionAndRequiresFreshValidation(t *testing.T) {
	service, identity := seededEnabledWorkflow(t, "portrait")
	profile, err := service.ReplaceAutoDLWorkflow(context.Background(), identity.ProfileID, AutoDLWorkflowVersionMutation{
		ExpectedCurrentVersionID: identity.VersionID, Compiled: genericCompiledWorkflow(2), References: AutoDLReferenceContract{Min: 0, Max: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Enabled || profile.AutoSelectable || len(profile.Versions) != 2 || profile.Versions[0].VersionID != identity.VersionID || profile.CurrentVersionID == identity.VersionID {
		t.Fatalf("replaced profile = %#v", profile)
	}
	if _, err := service.GetAutoDLWorkflowVersion(context.Background(), profile.ID, identity.VersionID); err != nil {
		t.Fatalf("historical version disappeared: %v", err)
	}
	if _, err := service.SetAutoDLWorkflowState(context.Background(), profile.ID, AutoDLWorkflowStateMutation{Enabled: boolPtr(true)}); !errors.Is(err, ErrAutoDLWorkflowUnavailable) {
		t.Fatalf("enable before validation error = %v", err)
	}
	settings, err := service.GetAutoDLSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	current := profile.Versions[1]
	if _, err := service.SaveAutoDLWorkflowValidation(context.Background(), settings.Instances[0].ID, AutoDLWorkflowValidation{
		WorkflowProfileID: profile.ID, VersionID: current.VersionID, Status: AutoDLWorkflowValidationReady,
		WorkflowDigest: current.WorkflowDigest, APITemplateDigest: current.APITemplateDigest,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetAutoDLWorkflowState(context.Background(), profile.ID, AutoDLWorkflowStateMutation{Enabled: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAutoDLWorkflowRejectsStaleInstanceValidation(t *testing.T) {
	service, identity := seededEnabledWorkflow(t, "portrait")
	settings, err := service.GetAutoDLSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	instance := settings.Instances[0]
	if _, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		ID: instance.ID, Name: instance.Name, SSHCommand: "ssh root@portrait-new.example.com", ComfyPort: 6006, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveAutoDLWorkflow(context.Background(), AutoDLWorkflowResolveRequest{WorkflowProfileID: identity.ProfileID, ForNewTask: true}); !errors.Is(err, ErrAutoDLWorkflowUnavailable) {
		t.Fatalf("error = %v, want ErrAutoDLWorkflowUnavailable", err)
	}
}

func TestResolveAutoDLWorkflowRejectsReferenceCountOutsideContract(t *testing.T) {
	service, identity := seededEnabledWorkflow(t, "portrait")
	_, err := service.ResolveAutoDLWorkflow(context.Background(), AutoDLWorkflowResolveRequest{
		WorkflowProfileID: identity.ProfileID, ReferenceCount: 2, ForNewTask: true,
	})
	if !errors.Is(err, ErrAutoDLWorkflowUnavailable) {
		t.Fatalf("error = %v, want ErrAutoDLWorkflowUnavailable", err)
	}
}

type testWorkflowIdentity struct {
	ProfileID string
	VersionID string
}

func seededEnabledWorkflow(t *testing.T, profileID string) (*Settings, testWorkflowIdentity) {
	t.Helper()
	service, _, _ := newAutoDLSettingsForTest()
	identity, _ := seedEnabledWorkflowOnService(t, service, profileID)
	return service, identity
}

func seedEnabledWorkflowOnService(t *testing.T, service *Settings, profileID string) (testWorkflowIdentity, AutoDLWorkflowProfileResponse) {
	t.Helper()
	profile, err := service.CreateAutoDLWorkflow(context.Background(), AutoDLWorkflowCreateMutation{
		ID: profileID, Name: profileID, Compiled: genericCompiledWorkflow(1), References: AutoDLReferenceContract{Min: 0, Max: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU " + profileID, SSHCommand: fmt.Sprintf("ssh root@%s.example.com", profileID), ComfyPort: 6006, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	version := profile.Versions[0]
	if _, err := service.SaveAutoDLWorkflowValidation(context.Background(), instance.ID, AutoDLWorkflowValidation{
		WorkflowProfileID: profile.ID, VersionID: version.VersionID, Status: AutoDLWorkflowValidationReady,
		WorkflowDigest: version.WorkflowDigest, APITemplateDigest: version.APITemplateDigest,
	}); err != nil {
		t.Fatal(err)
	}
	profile, err = service.SetAutoDLWorkflowState(context.Background(), profile.ID, AutoDLWorkflowStateMutation{Enabled: boolPtr(true), AutoSelectable: boolPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	return testWorkflowIdentity{ProfileID: profile.ID, VersionID: profile.CurrentVersionID}, profile
}

func seededGenericWorkflowSettingsWithRawDefaults(t *testing.T, defaults []AutoDLWorkflowDefault) *Settings {
	t.Helper()
	service, store, _ := newAutoDLSettingsForTest()
	_, _ = seedEnabledWorkflowOnService(t, service, "workflow-a")
	_, _ = seedEnabledWorkflowOnService(t, service, "workflow-b")
	var document autoDLSettingsDocument
	if err := json.Unmarshal([]byte(store.value(autoDLSettingsKey)), &document); err != nil {
		t.Fatal(err)
	}
	document.WorkflowDefaults = defaults
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	store.values[autoDLSettingsKey] = string(encoded)
	return service
}

func genericCompiledWorkflow(seed int) comfyui.CompiledWorkflow {
	ui := json.RawMessage(fmt.Sprintf(`{"nodes":[{"id":%d}],"links":[]}`, seed))
	api := json.RawMessage(fmt.Sprintf(`{"1":{"class_type":"Test","inputs":{"seed":%d,"text":"prompt"}}}`, seed))
	return comfyui.CompiledWorkflow{
		UIWorkflow: ui, APITemplate: api,
		Bindings: comfyui.WorkflowBindings{
			Confirmed: true, Prompts: []comfyui.WorkflowTarget{{NodeID: "1", InputName: "text"}}, Outputs: []comfyui.OutputBinding{{NodeID: "1"}},
		},
		WorkflowDigest: autoDLPayloadDigest(ui), APITemplateDigest: autoDLPayloadDigest(api), RequiredNodes: []string{"Test"},
	}
}

func boolPtr(value bool) *bool { return &value }

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
