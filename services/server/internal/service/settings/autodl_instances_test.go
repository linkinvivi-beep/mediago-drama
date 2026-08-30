package settings

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	platformkeychain "github.com/mediago-dev/mediago-drama/services/server/internal/platform/keychain"
)

func TestAutoDLSaveInstancePersistsNoPasswordOrRawCommand(t *testing.T) {
	service, appStore, passwords := newAutoDLSettingsForTest()

	got, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name:       "图像一号",
		SSHCommand: "ssh -p 23456 root@gpu.example.com",
		Password:   "secret-value",
		ComfyPort:  6007,
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("SaveAutoDLInstance() error = %v", err)
	}
	if got.ID == "" || got.CredentialRef == "" || !got.HasPassword {
		t.Fatalf("saved instance = %#v, want stable id, credential ref, and redacted password state", got)
	}
	if got.Host != "gpu.example.com" || got.SSHPort != 23456 || got.SSHUser != "root" || got.ComfyPort != 6007 {
		t.Fatalf("saved instance = %#v, want parsed SSH target and configured ComfyUI port", got)
	}

	raw := appStore.value(autoDLSettingsKey)
	if strings.Contains(raw, "secret-value") || strings.Contains(raw, "ssh -p") {
		t.Fatalf("stored settings leak secret or raw SSH command: %s", raw)
	}
	if secret := passwords.secret(autoDLKeychainService, got.CredentialRef); secret != "secret-value" {
		t.Fatalf("Keychain secret = %q, want secret-value", secret)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-value") || strings.Contains(string(encoded), "password") {
		t.Fatalf("response exposes password field: %s", encoded)
	}
}

func TestAutoDLSaveInstanceRequiresExplicitSSHUser(t *testing.T) {
	service, appStore, _ := newAutoDLSettingsForTest()

	_, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh gpu-a.example.com", ComfyPort: 6006,
	})
	if !errors.Is(err, ErrAutoDLSettingsInvalid) {
		t.Fatalf("SaveAutoDLInstance() error = %v, want ErrAutoDLSettingsInvalid", err)
	}
	if raw := appStore.value(autoDLSettingsKey); raw != "" {
		t.Fatalf("invalid instance persisted settings: %s", raw)
	}
}

func TestAutoDLReplacingInstanceKeepsStableIdentityAndWorkflowValidation(t *testing.T) {
	service, appStore, _ := newAutoDLSettingsForTest()
	initial, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name:       "GPU A",
		SSHCommand: "ssh -p 21001 root@gpu-a.example.com",
		ComfyPort:  6006,
		Enabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	seedAutoDLValidation(t, appStore, initial.ID, AutoDLWorkflowValidation{
		WorkflowProfileID: "zimage-t2i",
		Status:            AutoDLWorkflowStatusReady,
		WorkflowDigest:    "sha256:z-valid",
		ValidatedAt:       "2026-08-30T01:02:03Z",
	})

	replaced, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		ID:              initial.ID,
		Name:            "GPU A 新地址",
		SSHCommand:      "ssh -p 22002 root@gpu-new.example.com",
		ComfyPort:       6012,
		HostFingerprint: "SHA256:new-host-key",
		Enabled:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.ID != initial.ID || replaced.CredentialRef != initial.CredentialRef {
		t.Fatalf("replaced instance identity = %#v, want id=%q credentialRef=%q", replaced, initial.ID, initial.CredentialRef)
	}
	if len(replaced.WorkflowValidations) != 1 || replaced.WorkflowValidations[0].WorkflowProfileID != "zimage-t2i" {
		t.Fatalf("workflow validations = %#v, want prior validation record preserved", replaced.WorkflowValidations)
	}
	if replaced.WorkflowValidations[0].Status != AutoDLWorkflowStatusNeedsRevalidation {
		t.Fatalf("workflow validation status = %q, want connection replacement to require revalidation", replaced.WorkflowValidations[0].Status)
	}
	if replaced.Host != "gpu-new.example.com" || replaced.SSHPort != 22002 || replaced.ComfyPort != 6012 {
		t.Fatalf("replaced instance = %#v, want updated connection", replaced)
	}
}

func TestAutoDLListResponseRedactsPasswords(t *testing.T) {
	service, _, _ := newAutoDLSettingsForTest()
	saved, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name:       "GPU A",
		SSHCommand: "ssh root@gpu-a.example.com",
		Password:   "only-in-keychain",
		ComfyPort:  6006,
		Enabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	response, err := service.GetAutoDLSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Instances) != 1 || response.Instances[0].ID != saved.ID || !response.Instances[0].HasPassword {
		t.Fatalf("response = %#v, want one instance with hasPassword", response)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "only-in-keychain") || strings.Contains(string(encoded), `"password"`) || strings.Contains(string(encoded), `"sshCommand"`) {
		t.Fatalf("redacted response leaked a secret input: %s", encoded)
	}
}

func TestAutoDLClearPasswordDeletesOnlyExactKeychainAccount(t *testing.T) {
	service, _, passwords := newAutoDLSettingsForTest()
	first, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", Password: "first-secret", ComfyPort: 6006,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU B", SSHCommand: "ssh root@gpu-b.example.com", Password: "second-secret", ComfyPort: 6006,
	})
	if err != nil {
		t.Fatal(err)
	}

	cleared, err := service.ClearAutoDLInstancePassword(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.HasPassword {
		t.Fatalf("cleared response = %#v, want hasPassword false", cleared)
	}
	if passwords.has(autoDLKeychainService, first.CredentialRef) {
		t.Fatalf("credential %q still exists", first.CredentialRef)
	}
	if secret := passwords.secret(autoDLKeychainService, second.CredentialRef); secret != "second-secret" {
		t.Fatalf("unrelated credential = %q, want untouched second-secret", secret)
	}
	if !reflect.DeepEqual(passwords.deleteCalls, []keychainCall{{service: autoDLKeychainService, account: first.CredentialRef}}) {
		t.Fatalf("delete calls = %#v, want exact first account only", passwords.deleteCalls)
	}
}

func TestAutoDLSetPasswordDoesNotRewriteNonSecretProfile(t *testing.T) {
	service, appStore, passwords := newAutoDLSettingsForTest()
	instance, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", ComfyPort: 6006, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := appStore.value(autoDLSettingsKey)

	updated, err := service.SetAutoDLInstancePassword(context.Background(), instance.ID, "separate-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !updated.HasPassword || passwords.secret(autoDLKeychainService, instance.CredentialRef) != "separate-secret" {
		t.Fatalf("updated instance = %#v, want password stored only in fake Keychain", updated)
	}
	if after := appStore.value(autoDLSettingsKey); after != before || strings.Contains(after, "separate-secret") {
		t.Fatalf("non-secret document changed while setting password: before=%s after=%s", before, after)
	}
}

func TestAutoDLDeleteInstanceDeletesOnlyItsCredential(t *testing.T) {
	service, _, passwords := newAutoDLSettingsForTest()
	first, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", Password: "first-secret", ComfyPort: 6006,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU B", SSHCommand: "ssh root@gpu-b.example.com", Password: "second-secret", ComfyPort: 6006,
	})
	if err != nil {
		t.Fatal(err)
	}

	response, err := service.DeleteAutoDLInstance(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Instances) != 1 || response.Instances[0].ID != second.ID {
		t.Fatalf("response instances = %#v, want only second instance", response.Instances)
	}
	if passwords.has(autoDLKeychainService, first.CredentialRef) {
		t.Fatalf("deleted instance credential %q remains", first.CredentialRef)
	}
	if passwords.secret(autoDLKeychainService, second.CredentialRef) != "second-secret" {
		t.Fatal("deleting first instance altered second credential")
	}
	lastCall := passwords.deleteCalls[len(passwords.deleteCalls)-1]
	if lastCall != (keychainCall{service: autoDLKeychainService, account: first.CredentialRef}) {
		t.Fatalf("last delete call = %#v, want exact deleted instance account", lastCall)
	}
}

func TestAutoDLStoredMalformedJSONFailsClosed(t *testing.T) {
	service, appStore, _ := newAutoDLSettingsForTest()
	appStore.values[autoDLSettingsKey] = `{"version":1,"instances":[`

	if _, err := service.GetAutoDLSettings(context.Background()); !errors.Is(err, ErrAutoDLSettingsCorrupt) {
		t.Fatalf("GetAutoDLSettings() error = %v, want ErrAutoDLSettingsCorrupt", err)
	}
	if _, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", ComfyPort: 6006,
	}); !errors.Is(err, ErrAutoDLSettingsCorrupt) {
		t.Fatalf("SaveAutoDLInstance() error = %v, want ErrAutoDLSettingsCorrupt", err)
	}
	if got := appStore.value(autoDLSettingsKey); got != `{"version":1,"instances":[` {
		t.Fatalf("malformed durable value was overwritten: %q", got)
	}
}

func TestAutoDLStoredInvalidCredentialReferenceFailsClosed(t *testing.T) {
	service, appStore, _ := newAutoDLSettingsForTest()
	appStore.values[autoDLSettingsKey] = `{"version":1,"instances":[{"id":"autodl-safe","name":"GPU A","host":"gpu-a.example.com","sshPort":22,"sshUser":"root","comfyPort":6006,"credentialRef":"../../wrong","enabled":true}],"workflowProfiles":[]}`

	if _, err := service.GetAutoDLSettings(context.Background()); !errors.Is(err, ErrAutoDLSettingsCorrupt) {
		t.Fatalf("GetAutoDLSettings() error = %v, want ErrAutoDLSettingsCorrupt", err)
	}
}

func TestAutoDLWorkflowProfileRejectedFluxDigestNeedsRevalidation(t *testing.T) {
	service, _, _ := newAutoDLSettingsForTest()
	profile, err := service.SaveAutoDLWorkflowProfile(context.Background(), AutoDLWorkflowProfileMutation{
		ID:             "flux-fp8-t2i",
		Name:           "FLUX FP8 普通文生图",
		Kind:           "flux-fp8-t2i",
		Version:        "precision-v2",
		WorkflowDigest: "9970e8c3d92c4661a744b046d9f1b96208d875ad557af407f0ba89d656bc8419",
		Status:         AutoDLWorkflowStatusReady,
		Workflow:       json.RawMessage(`{"nodes":[],"links":[]}`),
		Manifest:       json.RawMessage(`{"prompt":{"nodeId":"6","input":"t5xxl"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Status != AutoDLWorkflowStatusNeedsRevalidation || profile.Ready {
		t.Fatalf("profile = %#v, defective observed FLUX v2 must not be ready", profile)
	}

	response, err := service.GetAutoDLSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(response.WorkflowProfiles) != 1 || response.WorkflowProfiles[0].Status != AutoDLWorkflowStatusNeedsRevalidation || response.WorkflowProfiles[0].Ready {
		t.Fatalf("stored profiles = %#v, want needs_revalidation and not ready", response.WorkflowProfiles)
	}
}

func TestAutoDLStoredRejectedFluxValidationCannotRemainReady(t *testing.T) {
	testCases := []struct {
		kind   string
		digest string
	}{
		{kind: "flux-fp8-t2i", digest: "9970e8c3d92c4661a744b046d9f1b96208d875ad557af407f0ba89d656bc8419"},
		{kind: "flux-fp8-i2i", digest: "1d84021c7f0530d13d914bc982ccf2e8e75200ea433331331f27264a01884462"},
		{kind: "flux-lustly-adult-t2i", digest: "1ee1ab222cab32acfd6473708b15092356c7438b7fcbc6b58a2f9a903ba0bee8"},
		{kind: "flux-lustly-adult-i2i", digest: "1f0cbb187d4bb66e4edaab33b42d90aebd342457621bff1c093a21a42db092aa"},
		{kind: "flux-lustly-adult-portrait", digest: "80a5524712fe07dbc84d2c89558a66381a9bf03b8dba8380bf3dd84e9dcccc8c"},
		{kind: "flux-lustly-adult-fullbody", digest: "6c3a39a77b7a6a5a13e46c2e2788502be578982fb5727540dceb5e94faf9b4b0"},
		{kind: "zimage-flux-refine", digest: "cc2ba571bc0bc6c1e7d68b9d4a3b8f1302f999d01c15745867f40e62f6f6f8b2"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.kind, func(t *testing.T) {
			service, appStore, _ := newAutoDLSettingsForTest()
			document := autoDLSettingsDocument{
				Version: autoDLSettingsVersion,
				Instances: []AutoDLInstanceProfile{{
					ID: "autodl-safe", Name: "GPU A", Host: "gpu-a.example.com", SSHPort: 22, SSHUser: "root", ComfyPort: 6006, CredentialRef: "autodl-safe", Enabled: true,
					WorkflowValidations: []AutoDLWorkflowValidation{
						{WorkflowProfileID: testCase.kind, Status: AutoDLWorkflowStatusReady, WorkflowDigest: testCase.digest, Reason: "previous_success"},
						{WorkflowProfileID: "zimage-t2i", Status: AutoDLWorkflowStatusReady, WorkflowDigest: "sha256:z-valid", Reason: "verified"},
					},
				}},
				WorkflowProfiles: []AutoDLWorkflowProfile{
					{ID: testCase.kind, Name: "Rejected candidate", Kind: testCase.kind, Version: "precision-v2", Status: AutoDLWorkflowStatusReady, WorkflowDigest: testCase.digest},
					{ID: "zimage-t2i", Name: "Z T2I", Kind: "zimage-t2i", Version: "v1", Status: AutoDLWorkflowStatusReady, WorkflowDigest: "sha256:z-valid"},
				},
			}
			raw, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			appStore.values[autoDLSettingsKey] = string(raw)

			response, err := service.GetAutoDLSettings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			validations := response.Instances[0].WorkflowValidations
			if len(validations) != 2 {
				t.Fatalf("validations = %#v, want rejected and Z-Image records", validations)
			}
			if validations[0].WorkflowProfileID != testCase.kind || validations[0].Status != AutoDLWorkflowStatusNeedsRevalidation || validations[0].Reason != "profile_needs_revalidation" {
				t.Fatalf("rejected validation = %#v, want forced needs_revalidation", validations[0])
			}
			if validations[1].WorkflowProfileID != "zimage-t2i" || validations[1].Status != AutoDLWorkflowStatusReady || validations[1].Reason != "verified" {
				t.Fatalf("unrelated validation = %#v, want unchanged ready Z-Image", validations[1])
			}
		})
	}
}

func TestAutoDLSaveWorkflowValidationCannotReadyRejectedProfile(t *testing.T) {
	service, _, _ := newAutoDLSettingsForTest()
	instance, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", ComfyPort: 6006,
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := service.SaveAutoDLWorkflowProfile(context.Background(), AutoDLWorkflowProfileMutation{
		ID:             "flux-fp8-t2i",
		Name:           "FLUX FP8 普通文生图",
		Kind:           "flux-fp8-t2i",
		Version:        "precision-v2",
		WorkflowDigest: "9970e8c3d92c4661a744b046d9f1b96208d875ad557af407f0ba89d656bc8419",
		Status:         AutoDLWorkflowStatusReady,
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := service.SaveAutoDLWorkflowValidation(context.Background(), instance.ID, AutoDLWorkflowValidation{
		WorkflowProfileID: profile.ID,
		Status:            AutoDLWorkflowStatusReady,
		WorkflowDigest:    profile.WorkflowDigest,
		ValidatedAt:       "2026-08-30T02:03:04Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.WorkflowValidations) != 1 || updated.WorkflowValidations[0].Status != AutoDLWorkflowStatusNeedsRevalidation {
		t.Fatalf("validations = %#v, rejected profile must remain needs_revalidation", updated.WorkflowValidations)
	}
}

func TestAutoDLSaveWorkflowValidationReplacesOnlySameProfile(t *testing.T) {
	service, _, _ := newAutoDLSettingsForTest()
	instance, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", ComfyPort: 6006,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []AutoDLWorkflowProfileMutation{
		{ID: "zimage-t2i", Name: "Z T2I", Kind: "zimage-t2i", Version: "v1", WorkflowDigest: "sha256:t2i", Status: AutoDLWorkflowStatusReady},
		{ID: "zimage-i2i", Name: "Z I2I", Kind: "zimage-i2i", Version: "v1", WorkflowDigest: "sha256:i2i", Status: AutoDLWorkflowStatusReady},
	} {
		if _, err := service.SaveAutoDLWorkflowProfile(context.Background(), profile); err != nil {
			t.Fatal(err)
		}
	}
	for _, validation := range []AutoDLWorkflowValidation{
		{WorkflowProfileID: "zimage-t2i", Status: AutoDLWorkflowStatusReady, WorkflowDigest: "sha256:t2i"},
		{WorkflowProfileID: "zimage-i2i", Status: AutoDLWorkflowStatusReady, WorkflowDigest: "sha256:i2i"},
		{WorkflowProfileID: "zimage-t2i", Status: AutoDLWorkflowStatusInvalid, WorkflowDigest: "sha256:t2i", Reason: "missing_model"},
	} {
		if _, err := service.SaveAutoDLWorkflowValidation(context.Background(), instance.ID, validation); err != nil {
			t.Fatal(err)
		}
	}
	response, err := service.GetAutoDLSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	validations := response.Instances[0].WorkflowValidations
	if len(validations) != 2 {
		t.Fatalf("validations = %#v, want one record per workflow profile", validations)
	}
	if validations[0].WorkflowProfileID != "zimage-i2i" || validations[1].WorkflowProfileID != "zimage-t2i" || validations[1].Status != AutoDLWorkflowStatusInvalid {
		t.Fatalf("validations = %#v, want stable sorted replacement", validations)
	}
}

func TestAutoDLWorkflowProfileReplacementKeepsStableIDAndInstanceValidations(t *testing.T) {
	service, appStore, _ := newAutoDLSettingsForTest()
	instance, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", ComfyPort: 6006,
	})
	if err != nil {
		t.Fatal(err)
	}
	seedAutoDLValidation(t, appStore, instance.ID, AutoDLWorkflowValidation{
		WorkflowProfileID: "zimage-t2i", Status: AutoDLWorkflowStatusReady, WorkflowDigest: "sha256:first",
	})
	if _, err := service.SaveAutoDLWorkflowProfile(context.Background(), AutoDLWorkflowProfileMutation{
		ID: "zimage-t2i", Name: "Z Image", Kind: "zimage-t2i", Version: "v1", WorkflowDigest: "sha256:first", Status: AutoDLWorkflowStatusReady,
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := service.SaveAutoDLWorkflowProfile(context.Background(), AutoDLWorkflowProfileMutation{
		ID: "zimage-t2i", Name: "Z Image Updated", Kind: "zimage-t2i", Version: "v2", WorkflowDigest: "sha256:second", Status: AutoDLWorkflowStatusNeedsRevalidation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != "zimage-t2i" || updated.Status != AutoDLWorkflowStatusNeedsRevalidation || updated.Ready {
		t.Fatalf("updated profile = %#v", updated)
	}
	response, err := service.GetAutoDLSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Instances) != 1 || len(response.Instances[0].WorkflowValidations) != 1 {
		t.Fatalf("instance validation records were lost: %#v", response.Instances)
	}
	if response.Instances[0].WorkflowValidations[0].Status != AutoDLWorkflowStatusNeedsRevalidation {
		t.Fatalf("validation = %#v, want changed workflow digest to require revalidation", response.Instances[0].WorkflowValidations[0])
	}
}

func TestAutoDLWorkflowAPITemplateDigestChangeRequiresInstanceRevalidation(t *testing.T) {
	service, _, _ := newAutoDLSettingsForTest()
	instance, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", ComfyPort: 6006,
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := AutoDLWorkflowProfileMutation{
		ID: "zimage-t2i", Name: "Z Image", Kind: "zimage-t2i", Version: "v1", WorkflowDigest: "sha256:same-workflow", APITemplateDigest: "sha256:api-v1", Status: AutoDLWorkflowStatusReady,
	}
	if _, err := service.SaveAutoDLWorkflowProfile(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveAutoDLWorkflowValidation(context.Background(), instance.ID, AutoDLWorkflowValidation{
		WorkflowProfileID: "zimage-t2i", Status: AutoDLWorkflowStatusReady, WorkflowDigest: "sha256:same-workflow", Reason: "verified",
	}); err != nil {
		t.Fatal(err)
	}
	initial.APITemplateDigest = "sha256:api-v2"
	if _, err := service.SaveAutoDLWorkflowProfile(context.Background(), initial); err != nil {
		t.Fatal(err)
	}

	response, err := service.GetAutoDLSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	validation := response.Instances[0].WorkflowValidations[0]
	if validation.Status != AutoDLWorkflowStatusNeedsRevalidation || validation.Reason != "workflow_changed" {
		t.Fatalf("validation = %#v, want API template digest change to require revalidation", validation)
	}
}

func TestAutoDLDeleteWorkflowProfileLeavesInstancesAndOtherProfiles(t *testing.T) {
	service, _, _ := newAutoDLSettingsForTest()
	instance, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", ComfyPort: 6006,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []AutoDLWorkflowProfileMutation{
		{ID: "zimage-t2i", Name: "Z T2I", Kind: "zimage-t2i", Version: "v1", WorkflowDigest: "sha256:t2i", Status: AutoDLWorkflowStatusReady},
		{ID: "zimage-i2i", Name: "Z I2I", Kind: "zimage-i2i", Version: "v1", WorkflowDigest: "sha256:i2i", Status: AutoDLWorkflowStatusReady},
	} {
		if _, err := service.SaveAutoDLWorkflowProfile(context.Background(), profile); err != nil {
			t.Fatal(err)
		}
	}

	response, err := service.DeleteAutoDLWorkflowProfile(context.Background(), "zimage-t2i")
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Instances) != 1 || response.Instances[0].ID != instance.ID {
		t.Fatalf("instances = %#v, want unchanged instance", response.Instances)
	}
	if len(response.WorkflowProfiles) != 1 || response.WorkflowProfiles[0].ID != "zimage-i2i" {
		t.Fatalf("workflow profiles = %#v, want only zimage-i2i", response.WorkflowProfiles)
	}
}

func newAutoDLSettingsForTest() (*Settings, *memoryAppSettingStore, *fakeGenericPasswordStore) {
	appStore := &memoryAppSettingStore{values: map[string]string{}}
	passwords := &fakeGenericPasswordStore{values: map[string]string{}}
	service := NewSettingsWithStores(&memoryAPIKeyStore{values: map[string]string{}}, nil, appStore)
	service.SetAutoDLPasswordStore(passwords)
	return service, appStore, passwords
}

func seedAutoDLValidation(t *testing.T, store *memoryAppSettingStore, instanceID string, validation AutoDLWorkflowValidation) {
	t.Helper()
	raw := store.value(autoDLSettingsKey)
	var document autoDLSettingsDocument
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		t.Fatal(err)
	}
	for index := range document.Instances {
		if document.Instances[index].ID == instanceID {
			document.Instances[index].WorkflowValidations = []AutoDLWorkflowValidation{validation}
		}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	store.values[autoDLSettingsKey] = string(encoded)
}

func (store *memoryAppSettingStore) value(key string) string {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.values[key]
}

type keychainCall struct {
	service string
	account string
}

type fakeGenericPasswordStore struct {
	mu          sync.Mutex
	values      map[string]string
	deleteCalls []keychainCall
}

func (store *fakeGenericPasswordStore) Set(_ context.Context, service, account, secret string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.values[service+"\x00"+account] = secret
	return nil
}

func (store *fakeGenericPasswordStore) Get(_ context.Context, service, account string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	secret, ok := store.values[service+"\x00"+account]
	if !ok {
		return "", platformkeychain.ErrNotFound
	}
	return secret, nil
}

func (store *fakeGenericPasswordStore) Delete(_ context.Context, service, account string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.deleteCalls = append(store.deleteCalls, keychainCall{service: service, account: account})
	delete(store.values, service+"\x00"+account)
	return nil
}

func (store *fakeGenericPasswordStore) secret(service, account string) string {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.values[service+"\x00"+account]
}

func (store *fakeGenericPasswordStore) has(service, account string) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	_, ok := store.values[service+"\x00"+account]
	return ok
}
