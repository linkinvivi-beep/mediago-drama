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

func TestAutoDLSettingsTreatsEmptyKeychainItemAsMissing(t *testing.T) {
	service, _, passwords := newAutoDLSettingsForTest()
	instance, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", ComfyPort: 6006,
	})
	if err != nil {
		t.Fatal(err)
	}
	passwords.values[autoDLKeychainService+"\x00"+instance.CredentialRef] = ""

	response, err := service.GetAutoDLSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Instances) != 1 || response.Instances[0].HasPassword {
		t.Fatalf("GetAutoDLSettings() = %#v, want empty Keychain item reported as missing", response)
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

func TestAutoDLChangingOnlyFingerprintMarksValidationStale(t *testing.T) {
	service, _, _ := newAutoDLSettingsForTest()
	instance, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh -p 16109 root@gpu-a.example.com", ComfyPort: 6006,
		HostFingerprint: "SHA256:old", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := service.SaveValidatedAutoDLWorkflowProfile(context.Background(), AutoDLWorkflowProfileMutation{
		ID: "portrait", Name: "Portrait", Workflow: json.RawMessage(`{"nodes":[],"links":[]}`),
		APITemplate: json.RawMessage(`{"1":{"class_type":"Test","inputs":{}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveAutoDLWorkflowValidation(context.Background(), instance.ID, AutoDLWorkflowValidation{
		WorkflowProfileID: profile.ID, Status: AutoDLWorkflowValidationReady,
		WorkflowDigest: profile.WorkflowDigest, APITemplateDigest: profile.APITemplateDigest,
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		ID: instance.ID, Name: "GPU A", SSHCommand: "ssh -p 16109 root@gpu-a.example.com", ComfyPort: 6006,
		HostFingerprint: "SHA256:new", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.WorkflowValidations[0]; got.Status != AutoDLWorkflowValidationStale || got.Reason != "connection_changed" {
		t.Fatalf("validation = %#v, want stale connection_changed", got)
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

func TestAutoDLGetInstanceAndPasswordStayBackendOnly(t *testing.T) {
	service, _, _ := newAutoDLSettingsForTest()
	saved, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh -p 16109 root@gpu-a.example.com", Password: "keychain-only", ComfyPort: 6006, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	profile, err := service.GetAutoDLInstance(context.Background(), saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != saved.ID || profile.Host != "gpu-a.example.com" || profile.SSHPort != 16109 {
		t.Fatalf("GetAutoDLInstance() = %#v", profile)
	}
	password, err := service.Password(context.Background(), profile.CredentialRef)
	if err != nil {
		t.Fatal(err)
	}
	if string(password) != "keychain-only" {
		t.Fatalf("Password() = %q", password)
	}
	password[0] = 'X'
	again, err := service.Password(context.Background(), profile.CredentialRef)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != "keychain-only" {
		t.Fatalf("Password() did not return an owned copy: %q", again)
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

func TestAutoDLSavePasswordPersistsTraceableIdentityBeforeCredentialWrite(t *testing.T) {
	credentialErr := errors.New("credential write outcome unknown")
	service, appStore, passwords := newAutoDLSettingsForTest()
	identityWasDurable := false
	passwords.beforeSet = func(_ string, account string) {
		raw := appStore.value(autoDLSettingsKey)
		identityWasDurable = strings.Contains(raw, account) && strings.Contains(raw, `"host":"gpu-a.example.com"`)
	}
	passwords.setErr = credentialErr

	got, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", Password: "secret", ComfyPort: 6006,
	})
	var writeErr *AutoDLCredentialWriteError
	if !errors.As(err, &writeErr) || !errors.Is(err, credentialErr) {
		t.Fatalf("SaveAutoDLInstance() error = %v, want typed credential write error", err)
	}
	if got.ID == "" || got.CredentialRef == "" || writeErr.InstanceID != got.ID || writeErr.CredentialRef != got.CredentialRef {
		t.Fatalf("response=%#v error=%#v, want matching stable identity", got, writeErr)
	}
	if !identityWasDurable {
		t.Fatal("Keychain Set ran before the instance identity was durable")
	}

	passwords.setErr = nil
	response, getErr := service.GetAutoDLSettings(context.Background())
	if getErr != nil {
		t.Fatal(getErr)
	}
	if len(response.Instances) != 1 || response.Instances[0].ID != got.ID || response.Instances[0].CredentialRef != got.CredentialRef {
		t.Fatalf("GET response = %#v, want failed credential write identity to remain enumerable", response)
	}

	retried, retryErr := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		ID: got.ID, Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", Password: "secret", ComfyPort: 6006,
	})
	if retryErr != nil || retried.ID != got.ID || !retried.HasPassword {
		t.Fatalf("retry with durable id got=%#v error=%v, want same successful instance", retried, retryErr)
	}
	response, getErr = service.GetAutoDLSettings(context.Background())
	if getErr != nil || len(response.Instances) != 1 || response.Instances[0].ID != got.ID {
		t.Fatalf("GET after retry = %#v error=%v, want exactly one stable instance", response, getErr)
	}
}

func TestAutoDLSavePasswordFailureKeepsUpdatedExistingIdentityTraceable(t *testing.T) {
	credentialErr := errors.New("credential write outcome unknown")
	service, _, passwords := newAutoDLSettingsForTest()
	initial, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-old.example.com", ComfyPort: 6006,
	})
	if err != nil {
		t.Fatal(err)
	}
	passwords.setErr = credentialErr

	updated, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		ID: initial.ID, Name: "GPU A new route", SSHCommand: "ssh -p 16001 root@gpu-new.example.com", Password: "secret", ComfyPort: 6010,
	})
	var writeErr *AutoDLCredentialWriteError
	if !errors.As(err, &writeErr) || writeErr.InstanceID != initial.ID || updated.ID != initial.ID || updated.CredentialRef != initial.CredentialRef {
		t.Fatalf("updated=%#v error=%v, want same traceable instance identity", updated, err)
	}

	passwords.setErr = nil
	response, getErr := service.GetAutoDLSettings(context.Background())
	if getErr != nil {
		t.Fatal(getErr)
	}
	if len(response.Instances) != 1 || response.Instances[0].ID != initial.ID || response.Instances[0].Host != "gpu-new.example.com" || response.Instances[0].ComfyPort != 6010 {
		t.Fatalf("GET response = %#v, want durable updated profile after credential failure", response)
	}
}

func TestAutoDLSavePasswordDoesNotWriteCredentialBeforeProfileCommit(t *testing.T) {
	settingsErr := errors.New("settings write failed")
	baseStore := &memoryAppSettingStore{values: map[string]string{}}
	appStore := &controlledAppSettingStore{memoryAppSettingStore: baseStore, setErr: settingsErr}
	passwords := &fakeGenericPasswordStore{values: map[string]string{}}
	keychainSetCalled := false
	passwords.beforeSet = func(_, _ string) { keychainSetCalled = true }
	service := NewSettingsWithStores(&memoryAPIKeyStore{values: map[string]string{}}, nil, appStore)
	service.SetAutoDLPasswordStore(passwords)

	_, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", Password: "secret", ComfyPort: 6006,
	})
	if !errors.Is(err, settingsErr) {
		t.Fatalf("SaveAutoDLInstance() error = %v, want settings failure", err)
	}
	if keychainSetCalled {
		t.Fatal("Keychain Set was called before durable profile commit")
	}
}

func TestAutoDLDeleteInstanceReportsRestoreFailureWithIndependentContext(t *testing.T) {
	service, baseStore, passwords := newAutoDLSettingsForTest()
	instance, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", Password: "secret", ComfyPort: 6006,
	})
	if err != nil {
		t.Fatal(err)
	}
	primaryErr := errors.New("settings delete failed")
	rollbackErr := errors.New("credential restore failed")
	callerCtx, cancel := context.WithCancel(context.Background())
	controlled := &controlledAppSettingStore{memoryAppSettingStore: baseStore, setErr: primaryErr, beforeSet: cancel}
	service.appSettings = controlled
	passwords.setErr = rollbackErr

	_, err = service.DeleteAutoDLInstance(callerCtx, instance.ID)
	if !errors.Is(err, primaryErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("DeleteAutoDLInstance() error = %v, want joined primary and restore errors", err)
	}
	if passwords.lastSetContextErr != nil {
		t.Fatalf("rollback Set context error = %v, want independent live context", passwords.lastSetContextErr)
	}
}

func TestAutoDLDeleteInstanceRequiresPasswordStore(t *testing.T) {
	service, appStore, _ := newAutoDLSettingsForTest()
	instance, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", ComfyPort: 6006,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := appStore.value(autoDLSettingsKey)
	service.SetAutoDLPasswordStore(nil)

	if _, err := service.DeleteAutoDLInstance(context.Background(), instance.ID); !errors.Is(err, ErrAutoDLPasswordStoreUnavailable) {
		t.Fatalf("DeleteAutoDLInstance() error = %v, want ErrAutoDLPasswordStoreUnavailable", err)
	}
	if after := appStore.value(autoDLSettingsKey); after != before {
		t.Fatalf("instance document changed without password store: before=%s after=%s", before, after)
	}
}

func TestAutoDLCommittedMutationsDoNotFailDuringPasswordResponseEnrichment(t *testing.T) {
	probeErr := errors.New("Keychain probe unavailable")
	t.Run("save instance", func(t *testing.T) {
		service, baseStore, passwords := newAutoDLSettingsForTest()
		controlled := &controlledAppSettingStore{memoryAppSettingStore: baseStore, afterSet: func() { passwords.getErr = probeErr }}
		service.appSettings = controlled

		got, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
			Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", Password: "secret", ComfyPort: 6006,
		})
		if err != nil || !got.HasPassword {
			t.Fatalf("SaveAutoDLInstance() got=%#v error=%v, want committed success", got, err)
		}
	})

	t.Run("save validation", func(t *testing.T) {
		service, baseStore, passwords := newAutoDLSettingsForTest()
		instance, profile := seedReadyAutoDLInstanceAndProfile(t, service)
		controlled := &controlledAppSettingStore{memoryAppSettingStore: baseStore, afterSet: func() { passwords.getErr = probeErr }}
		service.appSettings = controlled

		got, err := service.SaveAutoDLWorkflowValidation(context.Background(), instance.ID, AutoDLWorkflowValidation{
			WorkflowProfileID: profile.ID, Status: AutoDLWorkflowStatusReady, WorkflowDigest: profile.WorkflowDigest, APITemplateDigest: profile.APITemplateDigest,
		})
		if err != nil || len(got.WorkflowValidations) != 1 {
			t.Fatalf("SaveAutoDLWorkflowValidation() got=%#v error=%v, want committed success", got, err)
		}
	})

	t.Run("delete instance", func(t *testing.T) {
		service, baseStore, passwords := newAutoDLSettingsForTest()
		first, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", ComfyPort: 6006})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{Name: "GPU B", SSHCommand: "ssh root@gpu-b.example.com", ComfyPort: 6006}); err != nil {
			t.Fatal(err)
		}
		controlled := &controlledAppSettingStore{memoryAppSettingStore: baseStore, afterSet: func() { passwords.getErr = probeErr }}
		service.appSettings = controlled

		got, err := service.DeleteAutoDLInstance(context.Background(), first.ID)
		if err != nil || len(got.Instances) != 1 {
			t.Fatalf("DeleteAutoDLInstance() got=%#v error=%v, want committed success", got, err)
		}
	})
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

func TestAutoDLPublicWorkflowSaveBindsDigestsAndCannotPromoteReady(t *testing.T) {
	service, appStore, _ := newAutoDLSettingsForTest()
	profile, err := service.SaveAutoDLWorkflowProfile(context.Background(), AutoDLWorkflowProfileMutation{
		ID:                "zimage-t2i",
		Name:              "Z T2I",
		Kind:              "zimage-t2i",
		Version:           "v1",
		Status:            AutoDLWorkflowStatusReady,
		Workflow:          json.RawMessage(`{"nodes":[],"links":[]}`),
		APITemplate:       json.RawMessage(`{"1":{"class_type":"Test","inputs":{}}}`),
		WorkflowDigest:    "9970e8c3d92c4661a744b046d9f1b96208d875ad557af407f0ba89d656bc8419",
		APITemplateDigest: "forged-api-digest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.WorkflowDigest != "d0c3706befa57baff8ca22222d9fdddf89599a195b70ac0d8c255eb6cbedb8b7" {
		t.Fatalf("WorkflowDigest = %q, want server hash of exact workflow bytes", profile.WorkflowDigest)
	}
	if profile.APITemplateDigest != "5d8757ca75e3554b5a58dae5fa0551f74de6d13b5a84f4e720c5f33cafc87e39" {
		t.Fatalf("APITemplateDigest = %q, want server hash of exact API bytes", profile.APITemplateDigest)
	}
	if profile.Status != AutoDLWorkflowStatusNeedsRevalidation || profile.Ready {
		t.Fatalf("publicly saved profile = %#v, want untrusted needs_revalidation", profile)
	}
	if raw := appStore.value(autoDLSettingsKey); strings.Contains(raw, "forged-api-digest") || strings.Contains(raw, "9970e8c3d92c") {
		t.Fatalf("stored workflow retained client-supplied digests: %s", raw)
	}
}

func TestAutoDLWorkflowDigestSurvivesJSONPersistenceFormatting(t *testing.T) {
	testCases := []struct {
		name     string
		workflow json.RawMessage
		api      json.RawMessage
	}{
		{name: "whitespace", workflow: json.RawMessage("{\n  \"nodes\": [],\n  \"links\": []\n}"), api: json.RawMessage("{\n  \"1\": {\"class_type\": \"Test\", \"inputs\": {}}\n}")},
		{name: "html escapes", workflow: json.RawMessage(`{"nodes":[],"links":[],"note":"<tag>&value>"}`), api: json.RawMessage(`{"1":{"class_type":"<Test>&","inputs":{}}}`)},
		{name: "unicode separators", workflow: json.RawMessage("{\"nodes\":[],\"links\":[],\"note\":\"left middle right\"}"), api: json.RawMessage("{\"1\":{\"class_type\":\"Test  \",\"inputs\":{}}}")},
		{name: "key order one", workflow: json.RawMessage(`{"nodes":[],"links":[],"alpha":1,"beta":2}`), api: json.RawMessage(`{"1":{"class_type":"Test","inputs":{"alpha":1,"beta":2}}}`)},
		{name: "key order two", workflow: json.RawMessage(`{"beta":2,"alpha":1,"links":[],"nodes":[]}`), api: json.RawMessage(`{"1":{"inputs":{"beta":2,"alpha":1},"class_type":"Test"}}`)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service, appStore, passwords := newAutoDLSettingsForTest()
			saved, err := service.SaveAutoDLWorkflowProfile(context.Background(), AutoDLWorkflowProfileMutation{
				ID: "zimage-t2i", Name: "Z T2I", Kind: "zimage-t2i", Version: "v1", Workflow: testCase.workflow, APITemplate: testCase.api,
			})
			if err != nil {
				t.Fatal(err)
			}

			restarted := NewSettingsWithStores(&memoryAPIKeyStore{values: map[string]string{}}, nil, appStore)
			restarted.SetAutoDLPasswordStore(passwords)
			response, err := restarted.GetAutoDLSettings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			loaded := response.WorkflowProfiles[0]
			if loaded.WorkflowDigest != saved.WorkflowDigest || loaded.APITemplateDigest != saved.APITemplateDigest {
				t.Fatalf("loaded digests = (%q, %q), saved = (%q, %q)", loaded.WorkflowDigest, loaded.APITemplateDigest, saved.WorkflowDigest, saved.APITemplateDigest)
			}
		})
	}
}

func TestAutoDLStoredWorkflowPayloadDigestMismatchFailsClosed(t *testing.T) {
	service, appStore, _ := newAutoDLSettingsForTest()
	document := autoDLSettingsDocument{
		Version:   autoDLSettingsVersion,
		Instances: []AutoDLInstanceProfile{},
		WorkflowProfiles: []AutoDLWorkflowProfile{{
			ID: "generic-image", Name: "Generic image", MediaKind: "image", RouteID: "autodl.image", CurrentVersionID: "generic-image-v1",
			Versions: []AutoDLWorkflowVersion{{
				VersionID: "generic-image-v1", Sequence: 1, CreatedAt: "2026-08-31T00:00:00Z",
				UIWorkflow: json.RawMessage(`{"nodes":[],"links":[]}`), WorkflowDigest: "forged",
				BindingStatus: AutoDLBindingStatusUnconfirmed, References: AutoDLReferenceContract{Min: 0, Max: 8},
			}},
		}},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	appStore.values[autoDLSettingsKey] = string(raw)

	if _, err := service.GetAutoDLSettings(context.Background()); !errors.Is(err, ErrAutoDLSettingsCorrupt) {
		t.Fatalf("GetAutoDLSettings() error = %v, want fail-closed digest mismatch", err)
	}
}

func TestAutoDLTrustedWorkflowSaveCanPromoteValidatedProfile(t *testing.T) {
	service, _, _ := newAutoDLSettingsForTest()
	profile, err := service.SaveValidatedAutoDLWorkflowProfile(context.Background(), AutoDLWorkflowProfileMutation{
		ID: "zimage-t2i", Name: "Z T2I", Kind: "zimage-t2i", Version: "v1",
		Workflow: json.RawMessage(`{"nodes":[],"links":[]}`), APITemplate: json.RawMessage(`{"1":{"class_type":"Test","inputs":{}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Status != AutoDLWorkflowStatusReady || !profile.Ready {
		t.Fatalf("trusted profile = %#v, want ready", profile)
	}
}

func TestAutoDLReadyWorkflowValidationRequiresBothServerDigests(t *testing.T) {
	testCases := []struct {
		name     string
		workflow string
		api      string
	}{
		{name: "missing workflow digest", api: "current"},
		{name: "missing API digest", workflow: "current"},
		{name: "wrong workflow digest", workflow: "wrong", api: "current"},
		{name: "wrong API digest", workflow: "current", api: "wrong"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service, _, _ := newAutoDLSettingsForTest()
			instance, profile := seedReadyAutoDLInstanceAndProfile(t, service)
			workflowDigest := testCase.workflow
			if workflowDigest == "current" {
				workflowDigest = profile.WorkflowDigest
			}
			apiDigest := testCase.api
			if apiDigest == "current" {
				apiDigest = profile.APITemplateDigest
			}

			_, err := service.SaveAutoDLWorkflowValidation(context.Background(), instance.ID, AutoDLWorkflowValidation{
				WorkflowProfileID: profile.ID, Status: AutoDLWorkflowStatusReady, WorkflowDigest: workflowDigest, APITemplateDigest: apiDigest,
			})
			if !errors.Is(err, ErrAutoDLSettingsInvalid) {
				t.Fatalf("SaveAutoDLWorkflowValidation() error = %v, want ErrAutoDLSettingsInvalid", err)
			}
		})
	}

	service, _, _ := newAutoDLSettingsForTest()
	instance, profile := seedReadyAutoDLInstanceAndProfile(t, service)
	updated, err := service.SaveAutoDLWorkflowValidation(context.Background(), instance.ID, AutoDLWorkflowValidation{
		WorkflowProfileID: profile.ID, Status: AutoDLWorkflowStatusInvalid, Reason: "offline_validation_failed",
	})
	if err != nil {
		t.Fatalf("non-ready validation with omitted digests error = %v", err)
	}
	if updated.WorkflowValidations[0].Status != AutoDLWorkflowStatusInvalid || updated.WorkflowValidations[0].WorkflowDigest != profile.WorkflowDigest || updated.WorkflowValidations[0].APITemplateDigest != profile.APITemplateDigest {
		t.Fatalf("non-ready validation = %#v, want server-bound current digests", updated.WorkflowValidations[0])
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
	profiles := make(map[string]AutoDLWorkflowProfileResponse)
	for _, profile := range []AutoDLWorkflowProfileMutation{
		{ID: "zimage-t2i", Name: "Z T2I", Kind: "zimage-t2i", Version: "v1", Workflow: json.RawMessage(`{"nodes":[{"id":1}],"links":[]}`), APITemplate: json.RawMessage(`{"1":{"class_type":"T2I","inputs":{}}}`)},
		{ID: "zimage-i2i", Name: "Z I2I", Kind: "zimage-i2i", Version: "v1", Workflow: json.RawMessage(`{"nodes":[{"id":2}],"links":[]}`), APITemplate: json.RawMessage(`{"2":{"class_type":"I2I","inputs":{}}}`)},
	} {
		saved, err := service.SaveValidatedAutoDLWorkflowProfile(context.Background(), profile)
		if err != nil {
			t.Fatal(err)
		}
		profiles[saved.ID] = saved
	}
	for _, validation := range []AutoDLWorkflowValidation{
		{WorkflowProfileID: "zimage-t2i", Status: AutoDLWorkflowStatusReady, WorkflowDigest: profiles["zimage-t2i"].WorkflowDigest, APITemplateDigest: profiles["zimage-t2i"].APITemplateDigest},
		{WorkflowProfileID: "zimage-i2i", Status: AutoDLWorkflowStatusReady, WorkflowDigest: profiles["zimage-i2i"].WorkflowDigest, APITemplateDigest: profiles["zimage-i2i"].APITemplateDigest},
		{WorkflowProfileID: "zimage-t2i", Status: AutoDLWorkflowStatusInvalid, WorkflowDigest: profiles["zimage-t2i"].WorkflowDigest, APITemplateDigest: profiles["zimage-t2i"].APITemplateDigest, Reason: "missing_model"},
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
		ID: "zimage-t2i", Name: "Z Image", Kind: "zimage-t2i", Version: "v1", Workflow: json.RawMessage(`{"nodes":[],"links":[]}`), APITemplate: json.RawMessage(`{"1":{"class_type":"First","inputs":{}}}`),
	}
	first, err := service.SaveValidatedAutoDLWorkflowProfile(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveAutoDLWorkflowValidation(context.Background(), instance.ID, AutoDLWorkflowValidation{
		WorkflowProfileID: "zimage-t2i", Status: AutoDLWorkflowStatusReady, WorkflowDigest: first.WorkflowDigest, APITemplateDigest: first.APITemplateDigest, Reason: "verified",
	}); err != nil {
		t.Fatal(err)
	}
	initial.APITemplate = json.RawMessage(`{"1":{"class_type":"Second","inputs":{}}}`)
	second, err := service.SaveValidatedAutoDLWorkflowProfile(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if second.WorkflowDigest != first.WorkflowDigest || second.APITemplateDigest == first.APITemplateDigest {
		t.Fatalf("digest transition first=%#v second=%#v, want API-only digest change", first, second)
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

func newAutoDLSettingsForTest() (*Settings, *memoryAppSettingStore, *fakeGenericPasswordStore) {
	appStore := &memoryAppSettingStore{values: map[string]string{}}
	passwords := &fakeGenericPasswordStore{values: map[string]string{}}
	service := NewSettingsWithStores(&memoryAPIKeyStore{values: map[string]string{}}, nil, appStore)
	service.SetAutoDLPasswordStore(passwords)
	return service, appStore, passwords
}

func seedReadyAutoDLInstanceAndProfile(t *testing.T, service *Settings) (AutoDLInstanceResponse, AutoDLWorkflowProfileResponse) {
	t.Helper()
	instance, err := service.SaveAutoDLInstance(context.Background(), AutoDLInstanceMutation{
		Name: "GPU A", SSHCommand: "ssh root@gpu-a.example.com", ComfyPort: 6006,
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := service.SaveValidatedAutoDLWorkflowProfile(context.Background(), AutoDLWorkflowProfileMutation{
		ID: "zimage-t2i", Name: "Z T2I", Kind: "zimage-t2i", Version: "v1",
		Workflow: json.RawMessage(`{"nodes":[],"links":[]}`), APITemplate: json.RawMessage(`{"1":{"class_type":"Test","inputs":{}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return instance, profile
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
	mu                   sync.Mutex
	values               map[string]string
	deleteCalls          []keychainCall
	getErr               error
	setErr               error
	deleteErr            error
	lastSetContextErr    error
	lastDeleteContextErr error
	beforeSet            func(service, account string)
}

func (store *fakeGenericPasswordStore) Set(ctx context.Context, service, account, secret string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.lastSetContextErr = ctx.Err()
	if store.lastSetContextErr != nil {
		return store.lastSetContextErr
	}
	if store.beforeSet != nil {
		store.beforeSet(service, account)
	}
	if store.setErr != nil {
		return store.setErr
	}
	store.values[service+"\x00"+account] = secret
	return nil
}

func (store *fakeGenericPasswordStore) Get(ctx context.Context, service, account string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if store.getErr != nil {
		return "", store.getErr
	}
	secret, ok := store.values[service+"\x00"+account]
	if !ok {
		return "", platformkeychain.ErrNotFound
	}
	return secret, nil
}

func (store *fakeGenericPasswordStore) Delete(ctx context.Context, service, account string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.lastDeleteContextErr = ctx.Err()
	if store.lastDeleteContextErr != nil {
		return store.lastDeleteContextErr
	}
	store.deleteCalls = append(store.deleteCalls, keychainCall{service: service, account: account})
	if store.deleteErr != nil {
		return store.deleteErr
	}
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

type controlledAppSettingStore struct {
	*memoryAppSettingStore
	beforeSet func()
	afterSet  func()
	setErr    error
}

func (store *controlledAppSettingStore) SetAppSetting(key string, value string) error {
	if store.beforeSet != nil {
		store.beforeSet()
	}
	if store.setErr != nil {
		return store.setErr
	}
	if err := store.memoryAppSettingStore.SetAppSetting(key, value); err != nil {
		return err
	}
	if store.afterSet != nil {
		store.afterSet()
	}
	return nil
}
