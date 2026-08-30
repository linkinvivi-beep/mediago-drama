package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	platformautodl "github.com/mediago-dev/mediago-drama/services/server/internal/platform/autodl"
	platformkeychain "github.com/mediago-dev/mediago-drama/services/server/internal/platform/keychain"
	serviceshared "github.com/mediago-dev/mediago-drama/services/server/internal/service/shared"
)

const (
	autoDLSettingsKey      = "medialink.autodl.instance-pool.v1"
	autoDLKeychainService  = "app.medialink.autodl"
	autoDLSettingsVersion  = 1
	defaultAutoDLComfyPort = 6006

	AutoDLWorkflowStatusReady             = "ready"
	AutoDLWorkflowStatusNeedsRevalidation = "needs_revalidation"
	AutoDLWorkflowStatusInvalid           = "invalid"
)

var (
	ErrAutoDLSettingsInvalid  = errors.New("AutoDL settings are invalid")
	ErrAutoDLSettingsCorrupt  = errors.New("stored AutoDL settings are corrupt")
	ErrAutoDLInstanceNotFound = errors.New("AutoDL instance was not found")
	ErrAutoDLWorkflowNotFound = errors.New("AutoDL workflow profile was not found")

	autoDLProfileIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

	allowedAutoDLWorkflowKinds = map[string]struct{}{
		"zimage-t2i":                 {},
		"zimage-i2i":                 {},
		"flux-fp8-t2i":               {},
		"flux-fp8-i2i":               {},
		"flux-lustly-adult-t2i":      {},
		"flux-lustly-adult-i2i":      {},
		"flux-lustly-adult-portrait": {},
		"flux-lustly-adult-fullbody": {},
		"zimage-flux-refine":         {},
		"h3-ref2va":                  {},
		"h3-fl2va":                   {},
	}

	// These seven digests came from the 2026-08-30 read-only inspection. The
	// user subsequently rejected those workflow files, so they can be stored
	// for diagnosis but can never satisfy readiness.
	rejectedAutoDLWorkflowDigests = map[string]struct{}{
		"9970e8c3d92c4661a744b046d9f1b96208d875ad557af407f0ba89d656bc8419": {},
		"1d84021c7f0530d13d914bc982ccf2e8e75200ea433331331f27264a01884462": {},
		"1ee1ab222cab32acfd6473708b15092356c7438b7fcbc6b58a2f9a903ba0bee8": {},
		"1f0cbb187d4bb66e4edaab33b42d90aebd342457621bff1c093a21a42db092aa": {},
		"80a5524712fe07dbc84d2c89558a66381a9bf03b8dba8380bf3dd84e9dcccc8c": {},
		"6c3a39a77b7a6a5a13e46c2e2788502be578982fb5727540dceb5e94faf9b4b0": {},
		"cc2ba571bc0bc6c1e7d68b9d4a3b8f1302f999d01c15745867f40e62f6f6f8b2": {},
	}
)

type autoDLPasswordStore interface {
	Set(context.Context, string, string, string) error
	Get(context.Context, string, string) (string, error)
	Delete(context.Context, string, string) error
}

// AutoDLWorkflowValidation records one instance/profile compatibility check.
// Live health remains in memory; only the workflow identity and validation
// outcome are durable.
type AutoDLWorkflowValidation struct {
	WorkflowProfileID string `json:"workflowProfileId"`
	Status            string `json:"status"`
	WorkflowDigest    string `json:"workflowDigest,omitempty"`
	ValidatedAt       string `json:"validatedAt,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

// AutoDLInstanceProfile is the durable, non-secret connection profile.
type AutoDLInstanceProfile struct {
	ID                  string                     `json:"id"`
	Name                string                     `json:"name"`
	Host                string                     `json:"host"`
	SSHPort             int                        `json:"sshPort"`
	SSHUser             string                     `json:"sshUser"`
	ComfyPort           int                        `json:"comfyPort"`
	HostFingerprint     string                     `json:"hostFingerprint,omitempty"`
	CredentialRef       string                     `json:"credentialRef"`
	Enabled             bool                       `json:"enabled"`
	WorkflowValidations []AutoDLWorkflowValidation `json:"workflowValidations,omitempty"`
}

// AutoDLInstanceResponse exposes only whether the Keychain item exists.
type AutoDLInstanceResponse struct {
	AutoDLInstanceProfile
	HasPassword bool `json:"hasPassword"`
}

// AutoDLInstanceMutation accepts an OpenSSH login command only at the input
// boundary. The command is parsed and never persisted or executed.
type AutoDLInstanceMutation struct {
	ID              string `json:"id,omitempty"`
	Name            string `json:"name"`
	SSHCommand      string `json:"sshCommand"`
	Password        string `json:"password,omitempty"`
	ComfyPort       int    `json:"comfyPort,omitempty"`
	HostFingerprint string `json:"hostFingerprint,omitempty"`
	Enabled         bool   `json:"enabled"`
}

// AutoDLWorkflowProfile stores an imported workflow and its semantic metadata.
type AutoDLWorkflowProfile struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Kind              string          `json:"kind"`
	Version           string          `json:"version"`
	Status            string          `json:"status"`
	Workflow          json.RawMessage `json:"workflow,omitempty"`
	APITemplate       json.RawMessage `json:"apiTemplate,omitempty"`
	Manifest          json.RawMessage `json:"manifest,omitempty"`
	RequiredNodes     []string        `json:"requiredNodes,omitempty"`
	RequiredModels    []string        `json:"requiredModels,omitempty"`
	WorkflowDigest    string          `json:"workflowDigest"`
	APITemplateDigest string          `json:"apiTemplateDigest,omitempty"`
}

// AutoDLWorkflowProfileResponse derives readiness from the durable status.
type AutoDLWorkflowProfileResponse struct {
	AutoDLWorkflowProfile
	Ready bool `json:"ready"`
}

// AutoDLWorkflowProfileMutation replaces one stable workflow profile.
type AutoDLWorkflowProfileMutation struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Kind              string          `json:"kind"`
	Version           string          `json:"version"`
	Status            string          `json:"status,omitempty"`
	Workflow          json.RawMessage `json:"workflow,omitempty"`
	APITemplate       json.RawMessage `json:"apiTemplate,omitempty"`
	Manifest          json.RawMessage `json:"manifest,omitempty"`
	RequiredNodes     []string        `json:"requiredNodes,omitempty"`
	RequiredModels    []string        `json:"requiredModels,omitempty"`
	WorkflowDigest    string          `json:"workflowDigest"`
	APITemplateDigest string          `json:"apiTemplateDigest,omitempty"`
}

// AutoDLSettingsResponse is the redacted settings view.
type AutoDLSettingsResponse struct {
	Instances        []AutoDLInstanceResponse        `json:"instances"`
	WorkflowProfiles []AutoDLWorkflowProfileResponse `json:"workflowProfiles"`
}

type autoDLSettingsDocument struct {
	Version          int                     `json:"version"`
	Instances        []AutoDLInstanceProfile `json:"instances"`
	WorkflowProfiles []AutoDLWorkflowProfile `json:"workflowProfiles"`
}

// SetAutoDLPasswordStore installs the password store used for AutoDL
// credentials. Production wiring supplies macOS Keychain; tests supply a fake.
func (service *Settings) SetAutoDLPasswordStore(store platformkeychain.GenericPasswordStore) {
	if service == nil {
		return
	}
	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	service.autoDLPasswords = store
}

// GetAutoDLSettings loads the non-secret document and adds redacted Keychain
// presence flags.
func (service *Settings) GetAutoDLSettings(ctx context.Context) (AutoDLSettingsResponse, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLSettingsResponse{}, err
	}
	service.autoDLSettingsMu.Lock()
	document, err := service.loadAutoDLDocumentLocked()
	passwordStore := service.autoDLPasswords
	service.autoDLSettingsMu.Unlock()
	if err != nil {
		return AutoDLSettingsResponse{}, err
	}
	return buildAutoDLSettingsResponse(ctx, passwordStore, document)
}

// SaveAutoDLInstance creates or replaces a named instance while preserving its
// stable identity, credential reference, and workflow validation records.
func (service *Settings) SaveAutoDLInstance(ctx context.Context, mutation AutoDLInstanceMutation) (AutoDLInstanceResponse, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLInstanceResponse{}, err
	}
	target, err := platformautodl.ParseSSHLoginCommand(mutation.SSHCommand)
	if err != nil {
		return AutoDLInstanceResponse{}, fmt.Errorf("%w: SSH command", ErrAutoDLSettingsInvalid)
	}
	if target.User == "" {
		return AutoDLInstanceResponse{}, fmt.Errorf("%w: SSH user", ErrAutoDLSettingsInvalid)
	}
	name := strings.TrimSpace(mutation.Name)
	if name == "" || len(name) > 128 {
		return AutoDLInstanceResponse{}, fmt.Errorf("%w: instance name", ErrAutoDLSettingsInvalid)
	}
	comfyPort := mutation.ComfyPort
	if comfyPort == 0 {
		comfyPort = defaultAutoDLComfyPort
	}
	if comfyPort < 1 || comfyPort > 65535 {
		return AutoDLInstanceResponse{}, fmt.Errorf("%w: ComfyUI port", ErrAutoDLSettingsInvalid)
	}
	fingerprint := strings.TrimSpace(mutation.HostFingerprint)
	if len(fingerprint) > 512 {
		return AutoDLInstanceResponse{}, fmt.Errorf("%w: host fingerprint", ErrAutoDLSettingsInvalid)
	}

	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return AutoDLInstanceResponse{}, err
	}
	index := findAutoDLInstance(document.Instances, strings.TrimSpace(mutation.ID))
	var profile AutoDLInstanceProfile
	if strings.TrimSpace(mutation.ID) == "" {
		idGenerator := service.autoDLIDGenerator
		if idGenerator == nil {
			idGenerator = serviceshared.RandomID
		}
		id, generateErr := idGenerator("autodl")
		if generateErr != nil {
			return AutoDLInstanceResponse{}, fmt.Errorf("generating AutoDL instance id: %w", generateErr)
		}
		if !autoDLProfileIDPattern.MatchString(id) || findAutoDLInstance(document.Instances, id) >= 0 {
			return AutoDLInstanceResponse{}, fmt.Errorf("%w: generated instance id", ErrAutoDLSettingsInvalid)
		}
		profile.ID = id
		profile.CredentialRef = id
	} else {
		if index < 0 {
			return AutoDLInstanceResponse{}, ErrAutoDLInstanceNotFound
		}
		profile = document.Instances[index]
	}
	connectionChanged := index >= 0 && autoDLConnectionChanged(profile, target, comfyPort, fingerprint)
	profile.Name = name
	profile.Host = target.Host
	profile.SSHPort = target.Port
	profile.SSHUser = target.User
	profile.ComfyPort = comfyPort
	profile.HostFingerprint = fingerprint
	profile.Enabled = mutation.Enabled
	if connectionChanged {
		markAutoDLValidationsForRevalidation(profile.WorkflowValidations, "connection_changed")
	}

	if index < 0 {
		document.Instances = append(document.Instances, profile)
	} else {
		document.Instances[index] = profile
	}
	if err := service.saveAutoDLDocumentWithPasswordLocked(ctx, document, profile, mutation.Password); err != nil {
		return AutoDLInstanceResponse{}, err
	}
	return service.autoDLInstanceResponseLocked(ctx, profile)
}

// ClearAutoDLInstancePassword removes only the exact credential reference for
// the selected instance and leaves all non-secret settings intact.
func (service *Settings) ClearAutoDLInstancePassword(ctx context.Context, instanceID string) (AutoDLInstanceResponse, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLInstanceResponse{}, err
	}
	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return AutoDLInstanceResponse{}, err
	}
	index := findAutoDLInstance(document.Instances, strings.TrimSpace(instanceID))
	if index < 0 {
		return AutoDLInstanceResponse{}, ErrAutoDLInstanceNotFound
	}
	if service.autoDLPasswords == nil {
		return AutoDLInstanceResponse{}, fmt.Errorf("AutoDL password store is unavailable")
	}
	profile := document.Instances[index]
	if err := service.autoDLPasswords.Delete(ctx, autoDLKeychainService, profile.CredentialRef); err != nil {
		return AutoDLInstanceResponse{}, fmt.Errorf("clearing AutoDL password: %w", err)
	}
	return AutoDLInstanceResponse{AutoDLInstanceProfile: profile, HasPassword: false}, nil
}

// SetAutoDLInstancePassword replaces only the selected instance's Keychain
// item. The non-secret settings document is not rewritten.
func (service *Settings) SetAutoDLInstancePassword(ctx context.Context, instanceID, password string) (AutoDLInstanceResponse, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLInstanceResponse{}, err
	}
	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return AutoDLInstanceResponse{}, err
	}
	index := findAutoDLInstance(document.Instances, strings.TrimSpace(instanceID))
	if index < 0 {
		return AutoDLInstanceResponse{}, ErrAutoDLInstanceNotFound
	}
	if service.autoDLPasswords == nil {
		return AutoDLInstanceResponse{}, fmt.Errorf("AutoDL password store is unavailable")
	}
	profile := document.Instances[index]
	if err := service.autoDLPasswords.Set(ctx, autoDLKeychainService, profile.CredentialRef, password); err != nil {
		return AutoDLInstanceResponse{}, fmt.Errorf("saving AutoDL password: %w", err)
	}
	return AutoDLInstanceResponse{AutoDLInstanceProfile: profile, HasPassword: true}, nil
}

// DeleteAutoDLInstance removes one exact profile and its exact Keychain
// account. If durable removal fails after Keychain deletion, the prior secret
// is restored when one existed.
func (service *Settings) DeleteAutoDLInstance(ctx context.Context, instanceID string) (AutoDLSettingsResponse, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLSettingsResponse{}, err
	}
	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return AutoDLSettingsResponse{}, err
	}
	index := findAutoDLInstance(document.Instances, strings.TrimSpace(instanceID))
	if index < 0 {
		return AutoDLSettingsResponse{}, ErrAutoDLInstanceNotFound
	}
	profile := document.Instances[index]
	var previous string
	hadPrevious := false
	if service.autoDLPasswords != nil {
		previous, err = service.autoDLPasswords.Get(ctx, autoDLKeychainService, profile.CredentialRef)
		if err != nil && !errors.Is(err, platformkeychain.ErrNotFound) {
			return AutoDLSettingsResponse{}, fmt.Errorf("reading AutoDL password before deletion: %w", err)
		}
		hadPrevious = err == nil
		if err := service.autoDLPasswords.Delete(ctx, autoDLKeychainService, profile.CredentialRef); err != nil {
			return AutoDLSettingsResponse{}, fmt.Errorf("deleting AutoDL password: %w", err)
		}
	}
	document.Instances = append(document.Instances[:index], document.Instances[index+1:]...)
	if err := service.persistAutoDLDocumentLocked(document); err != nil {
		if hadPrevious {
			_ = service.autoDLPasswords.Set(ctx, autoDLKeychainService, profile.CredentialRef, previous)
		}
		return AutoDLSettingsResponse{}, err
	}
	return buildAutoDLSettingsResponse(ctx, service.autoDLPasswords, document)
}

// SaveAutoDLWorkflowProfile creates or replaces one stable workflow profile.
func (service *Settings) SaveAutoDLWorkflowProfile(ctx context.Context, mutation AutoDLWorkflowProfileMutation) (AutoDLWorkflowProfileResponse, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	profile, err := normalizeAutoDLWorkflowProfile(mutation)
	if err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	index := findAutoDLWorkflowProfile(document.WorkflowProfiles, profile.ID)
	if index < 0 {
		document.WorkflowProfiles = append(document.WorkflowProfiles, profile)
	} else {
		if document.WorkflowProfiles[index].WorkflowDigest != profile.WorkflowDigest {
			for instanceIndex := range document.Instances {
				for validationIndex := range document.Instances[instanceIndex].WorkflowValidations {
					validation := &document.Instances[instanceIndex].WorkflowValidations[validationIndex]
					if validation.WorkflowProfileID == profile.ID {
						validation.Status = AutoDLWorkflowStatusNeedsRevalidation
						validation.Reason = "workflow_changed"
					}
				}
			}
		}
		document.WorkflowProfiles[index] = profile
	}
	if err := service.persistAutoDLDocumentLocked(document); err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	return autoDLWorkflowResponse(profile), nil
}

// SaveAutoDLWorkflowValidation records one instance/profile result. A profile
// that is itself awaiting revalidation can never gain a ready instance record.
func (service *Settings) SaveAutoDLWorkflowValidation(ctx context.Context, instanceID string, validation AutoDLWorkflowValidation) (AutoDLInstanceResponse, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLInstanceResponse{}, err
	}
	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return AutoDLInstanceResponse{}, err
	}
	instanceIndex := findAutoDLInstance(document.Instances, strings.TrimSpace(instanceID))
	if instanceIndex < 0 {
		return AutoDLInstanceResponse{}, ErrAutoDLInstanceNotFound
	}
	profileID := strings.TrimSpace(validation.WorkflowProfileID)
	profileIndex := findAutoDLWorkflowProfile(document.WorkflowProfiles, profileID)
	if profileIndex < 0 {
		return AutoDLInstanceResponse{}, ErrAutoDLWorkflowNotFound
	}
	profile := document.WorkflowProfiles[profileIndex]
	status := strings.TrimSpace(validation.Status)
	if !validAutoDLWorkflowStatus(status) || strings.TrimSpace(validation.WorkflowDigest) != profile.WorkflowDigest {
		return AutoDLInstanceResponse{}, fmt.Errorf("%w: workflow validation", ErrAutoDLSettingsInvalid)
	}
	validation.WorkflowProfileID = profileID
	validation.WorkflowDigest = profile.WorkflowDigest
	validation.Status = status
	validation.ValidatedAt = strings.TrimSpace(validation.ValidatedAt)
	validation.Reason = strings.TrimSpace(validation.Reason)
	if len(validation.ValidatedAt) > 64 || len(validation.Reason) > 512 {
		return AutoDLInstanceResponse{}, fmt.Errorf("%w: workflow validation metadata", ErrAutoDLSettingsInvalid)
	}
	if status == AutoDLWorkflowStatusReady && normalizeStoredAutoDLWorkflowStatus(profile) != AutoDLWorkflowStatusReady {
		validation.Status = AutoDLWorkflowStatusNeedsRevalidation
		validation.Reason = "profile_needs_revalidation"
	}
	instance := &document.Instances[instanceIndex]
	validationIndex := findAutoDLWorkflowValidation(instance.WorkflowValidations, profileID)
	if validationIndex < 0 {
		instance.WorkflowValidations = append(instance.WorkflowValidations, validation)
	} else {
		instance.WorkflowValidations[validationIndex] = validation
	}
	sort.Slice(instance.WorkflowValidations, func(left, right int) bool {
		return instance.WorkflowValidations[left].WorkflowProfileID < instance.WorkflowValidations[right].WorkflowProfileID
	})
	if err := service.persistAutoDLDocumentLocked(document); err != nil {
		return AutoDLInstanceResponse{}, err
	}
	return service.autoDLInstanceResponseLocked(ctx, *instance)
}

// DeleteAutoDLWorkflowProfile removes one workflow definition without
// deleting instances or their historical validation records.
func (service *Settings) DeleteAutoDLWorkflowProfile(ctx context.Context, profileID string) (AutoDLSettingsResponse, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLSettingsResponse{}, err
	}
	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return AutoDLSettingsResponse{}, err
	}
	index := findAutoDLWorkflowProfile(document.WorkflowProfiles, strings.TrimSpace(profileID))
	if index < 0 {
		return AutoDLSettingsResponse{}, ErrAutoDLWorkflowNotFound
	}
	document.WorkflowProfiles = append(document.WorkflowProfiles[:index], document.WorkflowProfiles[index+1:]...)
	if err := service.persistAutoDLDocumentLocked(document); err != nil {
		return AutoDLSettingsResponse{}, err
	}
	return buildAutoDLSettingsResponse(ctx, service.autoDLPasswords, document)
}

func (service *Settings) loadAutoDLDocumentLocked() (autoDLSettingsDocument, error) {
	if service == nil || service.appSettings == nil {
		return autoDLSettingsDocument{}, ErrAppSettingStoreMissing
	}
	raw, found, err := service.appSettings.GetAppSetting(autoDLSettingsKey)
	if err != nil {
		return autoDLSettingsDocument{}, err
	}
	if !found || strings.TrimSpace(raw) == "" {
		return autoDLSettingsDocument{Version: autoDLSettingsVersion, Instances: []AutoDLInstanceProfile{}, WorkflowProfiles: []AutoDLWorkflowProfile{}}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document autoDLSettingsDocument
	if err := decoder.Decode(&document); err != nil {
		return autoDLSettingsDocument{}, fmt.Errorf("%w: invalid JSON", ErrAutoDLSettingsCorrupt)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return autoDLSettingsDocument{}, fmt.Errorf("%w: trailing JSON", ErrAutoDLSettingsCorrupt)
	}
	if err := validateAutoDLDocument(document); err != nil {
		return autoDLSettingsDocument{}, fmt.Errorf("%w: invalid document", ErrAutoDLSettingsCorrupt)
	}
	return document, nil
}

func (service *Settings) persistAutoDLDocumentLocked(document autoDLSettingsDocument) error {
	document.Version = autoDLSettingsVersion
	if document.Instances == nil {
		document.Instances = []AutoDLInstanceProfile{}
	}
	if document.WorkflowProfiles == nil {
		document.WorkflowProfiles = []AutoDLWorkflowProfile{}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encoding AutoDL settings: %w", err)
	}
	if err := service.appSettings.SetAppSetting(autoDLSettingsKey, string(encoded)); err != nil {
		return fmt.Errorf("saving AutoDL settings: %w", err)
	}
	return nil
}

func (service *Settings) saveAutoDLDocumentWithPasswordLocked(ctx context.Context, document autoDLSettingsDocument, profile AutoDLInstanceProfile, password string) error {
	if password == "" {
		return service.persistAutoDLDocumentLocked(document)
	}
	if service.autoDLPasswords == nil {
		return fmt.Errorf("AutoDL password store is unavailable")
	}
	previous, getErr := service.autoDLPasswords.Get(ctx, autoDLKeychainService, profile.CredentialRef)
	if getErr != nil && !errors.Is(getErr, platformkeychain.ErrNotFound) {
		return fmt.Errorf("reading prior AutoDL password: %w", getErr)
	}
	hadPrevious := getErr == nil
	if err := service.autoDLPasswords.Set(ctx, autoDLKeychainService, profile.CredentialRef, password); err != nil {
		return fmt.Errorf("saving AutoDL password: %w", err)
	}
	if err := service.persistAutoDLDocumentLocked(document); err != nil {
		if hadPrevious {
			_ = service.autoDLPasswords.Set(ctx, autoDLKeychainService, profile.CredentialRef, previous)
		} else {
			_ = service.autoDLPasswords.Delete(ctx, autoDLKeychainService, profile.CredentialRef)
		}
		return err
	}
	return nil
}

func (service *Settings) autoDLInstanceResponseLocked(ctx context.Context, profile AutoDLInstanceProfile) (AutoDLInstanceResponse, error) {
	if service.autoDLPasswords == nil {
		return AutoDLInstanceResponse{AutoDLInstanceProfile: profile}, nil
	}
	_, err := service.autoDLPasswords.Get(ctx, autoDLKeychainService, profile.CredentialRef)
	if errors.Is(err, platformkeychain.ErrNotFound) {
		return AutoDLInstanceResponse{AutoDLInstanceProfile: profile}, nil
	}
	if err != nil {
		return AutoDLInstanceResponse{}, fmt.Errorf("checking AutoDL password: %w", err)
	}
	return AutoDLInstanceResponse{AutoDLInstanceProfile: profile, HasPassword: true}, nil
}

func buildAutoDLSettingsResponse(ctx context.Context, passwordStore autoDLPasswordStore, document autoDLSettingsDocument) (AutoDLSettingsResponse, error) {
	response := AutoDLSettingsResponse{
		Instances:        make([]AutoDLInstanceResponse, 0, len(document.Instances)),
		WorkflowProfiles: make([]AutoDLWorkflowProfileResponse, 0, len(document.WorkflowProfiles)),
	}
	for _, profile := range document.Instances {
		hasPassword := false
		if passwordStore != nil {
			_, err := passwordStore.Get(ctx, autoDLKeychainService, profile.CredentialRef)
			switch {
			case err == nil:
				hasPassword = true
			case errors.Is(err, platformkeychain.ErrNotFound):
			default:
				return AutoDLSettingsResponse{}, fmt.Errorf("checking AutoDL password: %w", err)
			}
		}
		response.Instances = append(response.Instances, AutoDLInstanceResponse{AutoDLInstanceProfile: profile, HasPassword: hasPassword})
	}
	for _, profile := range document.WorkflowProfiles {
		profile.Status = normalizeStoredAutoDLWorkflowStatus(profile)
		response.WorkflowProfiles = append(response.WorkflowProfiles, autoDLWorkflowResponse(profile))
	}
	return response, nil
}

func normalizeAutoDLWorkflowProfile(mutation AutoDLWorkflowProfileMutation) (AutoDLWorkflowProfile, error) {
	id := strings.TrimSpace(mutation.ID)
	kind := strings.TrimSpace(mutation.Kind)
	if !autoDLProfileIDPattern.MatchString(id) {
		return AutoDLWorkflowProfile{}, fmt.Errorf("%w: workflow profile id", ErrAutoDLSettingsInvalid)
	}
	if _, ok := allowedAutoDLWorkflowKinds[kind]; !ok {
		return AutoDLWorkflowProfile{}, fmt.Errorf("%w: workflow kind", ErrAutoDLSettingsInvalid)
	}
	name := strings.TrimSpace(mutation.Name)
	version := strings.TrimSpace(mutation.Version)
	digest := strings.TrimSpace(mutation.WorkflowDigest)
	if name == "" || len(name) > 128 || version == "" || len(version) > 128 || digest == "" || len(digest) > 128 {
		return AutoDLWorkflowProfile{}, fmt.Errorf("%w: workflow identity", ErrAutoDLSettingsInvalid)
	}
	status := strings.TrimSpace(mutation.Status)
	if status == "" {
		status = AutoDLWorkflowStatusNeedsRevalidation
	}
	if !validAutoDLWorkflowStatus(status) {
		return AutoDLWorkflowProfile{}, fmt.Errorf("%w: workflow status", ErrAutoDLSettingsInvalid)
	}
	for _, raw := range []json.RawMessage{mutation.Workflow, mutation.APITemplate, mutation.Manifest} {
		if len(raw) > 0 && !json.Valid(raw) {
			return AutoDLWorkflowProfile{}, fmt.Errorf("%w: workflow JSON", ErrAutoDLSettingsInvalid)
		}
	}
	profile := AutoDLWorkflowProfile{
		ID:                id,
		Name:              name,
		Kind:              kind,
		Version:           version,
		Status:            status,
		Workflow:          cloneRawMessage(mutation.Workflow),
		APITemplate:       cloneRawMessage(mutation.APITemplate),
		Manifest:          cloneRawMessage(mutation.Manifest),
		RequiredNodes:     normalizedStringSet(mutation.RequiredNodes),
		RequiredModels:    normalizedStringSet(mutation.RequiredModels),
		WorkflowDigest:    digest,
		APITemplateDigest: strings.TrimSpace(mutation.APITemplateDigest),
	}
	profile.Status = normalizeStoredAutoDLWorkflowStatus(profile)
	return profile, nil
}

func normalizeStoredAutoDLWorkflowStatus(profile AutoDLWorkflowProfile) string {
	digest := strings.ToLower(strings.TrimSpace(profile.WorkflowDigest))
	digest = strings.TrimPrefix(digest, "sha256:")
	if _, rejected := rejectedAutoDLWorkflowDigests[digest]; rejected {
		return AutoDLWorkflowStatusNeedsRevalidation
	}
	return profile.Status
}

func autoDLWorkflowResponse(profile AutoDLWorkflowProfile) AutoDLWorkflowProfileResponse {
	profile.Status = normalizeStoredAutoDLWorkflowStatus(profile)
	return AutoDLWorkflowProfileResponse{AutoDLWorkflowProfile: profile, Ready: profile.Status == AutoDLWorkflowStatusReady}
}

func validateAutoDLDocument(document autoDLSettingsDocument) error {
	if document.Version != autoDLSettingsVersion {
		return fmt.Errorf("unsupported version")
	}
	instanceIDs := make(map[string]struct{}, len(document.Instances))
	credentialRefs := make(map[string]struct{}, len(document.Instances))
	for _, instance := range document.Instances {
		if !autoDLProfileIDPattern.MatchString(instance.ID) || !autoDLProfileIDPattern.MatchString(instance.CredentialRef) || strings.TrimSpace(instance.Name) == "" || len(instance.Name) > 128 || instance.ComfyPort < 1 || instance.ComfyPort > 65535 {
			return fmt.Errorf("invalid instance")
		}
		parsed, err := platformautodl.ParseSSHLoginCommand(fmt.Sprintf("ssh -p %d -l %s %s", instance.SSHPort, instance.SSHUser, instance.Host))
		if err != nil || parsed.Host != instance.Host || parsed.Port != instance.SSHPort || parsed.User != instance.SSHUser {
			return fmt.Errorf("invalid instance")
		}
		if _, duplicate := instanceIDs[instance.ID]; duplicate {
			return fmt.Errorf("duplicate instance")
		}
		if _, duplicate := credentialRefs[instance.CredentialRef]; duplicate {
			return fmt.Errorf("duplicate credential reference")
		}
		instanceIDs[instance.ID] = struct{}{}
		credentialRefs[instance.CredentialRef] = struct{}{}
		validationIDs := make(map[string]struct{}, len(instance.WorkflowValidations))
		for _, validation := range instance.WorkflowValidations {
			if !autoDLProfileIDPattern.MatchString(validation.WorkflowProfileID) || !validAutoDLWorkflowStatus(validation.Status) {
				return fmt.Errorf("invalid workflow validation")
			}
			if _, duplicate := validationIDs[validation.WorkflowProfileID]; duplicate {
				return fmt.Errorf("duplicate workflow validation")
			}
			validationIDs[validation.WorkflowProfileID] = struct{}{}
		}
	}
	profileIDs := make(map[string]struct{}, len(document.WorkflowProfiles))
	for _, profile := range document.WorkflowProfiles {
		if !autoDLProfileIDPattern.MatchString(profile.ID) || profile.Name == "" || profile.Version == "" || profile.WorkflowDigest == "" || !validAutoDLWorkflowStatus(profile.Status) {
			return fmt.Errorf("invalid workflow profile")
		}
		if _, ok := allowedAutoDLWorkflowKinds[profile.Kind]; !ok {
			return fmt.Errorf("invalid workflow kind")
		}
		if _, duplicate := profileIDs[profile.ID]; duplicate {
			return fmt.Errorf("duplicate workflow profile")
		}
		profileIDs[profile.ID] = struct{}{}
		for _, raw := range []json.RawMessage{profile.Workflow, profile.APITemplate, profile.Manifest} {
			if len(raw) > 0 && !json.Valid(raw) {
				return fmt.Errorf("invalid workflow JSON")
			}
		}
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple JSON values")
	}
	return err
}

func findAutoDLInstance(instances []AutoDLInstanceProfile, id string) int {
	for index := range instances {
		if instances[index].ID == id {
			return index
		}
	}
	return -1
}

func findAutoDLWorkflowProfile(profiles []AutoDLWorkflowProfile, id string) int {
	for index := range profiles {
		if profiles[index].ID == id {
			return index
		}
	}
	return -1
}

func findAutoDLWorkflowValidation(validations []AutoDLWorkflowValidation, profileID string) int {
	for index := range validations {
		if validations[index].WorkflowProfileID == profileID {
			return index
		}
	}
	return -1
}

func autoDLConnectionChanged(profile AutoDLInstanceProfile, target platformautodl.SSHLoginTarget, comfyPort int, fingerprint string) bool {
	return profile.Host != target.Host ||
		profile.SSHPort != target.Port ||
		profile.SSHUser != target.User ||
		profile.ComfyPort != comfyPort ||
		profile.HostFingerprint != fingerprint
}

func markAutoDLValidationsForRevalidation(validations []AutoDLWorkflowValidation, reason string) {
	for index := range validations {
		validations[index].Status = AutoDLWorkflowStatusNeedsRevalidation
		validations[index].Reason = reason
	}
}

func validAutoDLWorkflowStatus(status string) bool {
	return status == AutoDLWorkflowStatusReady || status == AutoDLWorkflowStatusNeedsRevalidation || status == AutoDLWorkflowStatusInvalid
}

func normalizedStringSet(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	return bytes.Clone(raw)
}

func requireAutoDLContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("AutoDL settings context is required")
	}
	return ctx.Err()
}
