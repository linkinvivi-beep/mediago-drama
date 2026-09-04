package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	platformautodl "github.com/mediago-dev/mediago-drama/services/server/internal/platform/autodl"
	platformkeychain "github.com/mediago-dev/mediago-drama/services/server/internal/platform/keychain"
	serviceshared "github.com/mediago-dev/mediago-drama/services/server/internal/service/shared"
)

const (
	autoDLSettingsKey         = "medialink.autodl.instance-pool.v1"
	autoDLKeychainService     = "app.medialink.autodl"
	autoDLSettingsVersion     = 3
	defaultAutoDLComfyPort    = 6006
	autoDLCompensationTimeout = 5 * time.Second
	maxAutoDLStartupCommand   = 4096
)

var (
	ErrAutoDLSettingsInvalid          = errors.New("AutoDL settings are invalid")
	ErrAutoDLSettingsCorrupt          = errors.New("stored AutoDL settings are corrupt")
	ErrAutoDLInstanceNotFound         = errors.New("AutoDL instance was not found")
	ErrAutoDLWorkflowNotFound         = errors.New("AutoDL workflow profile was not found")
	ErrAutoDLPasswordStoreUnavailable = errors.New("AutoDL password store is unavailable")

	autoDLProfileIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

type autoDLPasswordStore interface {
	Set(context.Context, string, string, string) error
	Get(context.Context, string, string) (string, error)
	Exists(context.Context, string, string) (bool, error)
	Delete(context.Context, string, string) error
}

// AutoDLCredentialWriteError reports a Keychain write whose instance identity
// is already durable. Callers must reuse InstanceID instead of creating a new
// profile; the Keychain outcome may be unknown.
type AutoDLCredentialWriteError struct {
	InstanceID    string
	CredentialRef string
	Cause         error
}

func (err *AutoDLCredentialWriteError) Error() string {
	return fmt.Sprintf("saving AutoDL credential for instance %s failed; outcome may be unknown", err.InstanceID)
}

func (err *AutoDLCredentialWriteError) Unwrap() error {
	return err.Cause
}

// AutoDLInstanceProfile is the durable, non-secret connection profile.
type AutoDLInstanceProfile struct {
	ID                  string                     `json:"id"`
	Name                string                     `json:"name"`
	Host                string                     `json:"host"`
	SSHPort             int                        `json:"sshPort"`
	SSHUser             string                     `json:"sshUser"`
	ComfyPort           int                        `json:"comfyPort"`
	StartupCommand      string                     `json:"startupCommand,omitempty"`
	LocalPort           int                        `json:"localPort,omitempty"`
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
	StartupCommand  string `json:"startupCommand,omitempty"`
	LocalPort       int    `json:"localPort,omitempty"`
	HostFingerprint string `json:"hostFingerprint,omitempty"`
	Enabled         bool   `json:"enabled"`
}

// AutoDLSettingsResponse is the redacted settings view.
type AutoDLSettingsResponse struct {
	Instances        []AutoDLInstanceResponse        `json:"instances"`
	WorkflowProfiles []AutoDLWorkflowProfileResponse `json:"workflowProfiles"`
	WorkflowDefaults []AutoDLWorkflowDefault         `json:"workflowDefaults"`
}

type autoDLSettingsDocument struct {
	Version          int                     `json:"version"`
	Instances        []AutoDLInstanceProfile `json:"instances"`
	WorkflowProfiles []AutoDLWorkflowProfile `json:"workflowProfiles"`
	WorkflowDefaults []AutoDLWorkflowDefault `json:"workflowDefaults,omitempty"`
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

// GetAutoDLInstance returns one non-secret connection profile for trusted
// backend orchestration. Password material remains in the Keychain.
func (service *Settings) GetAutoDLInstance(ctx context.Context, instanceID string) (AutoDLInstanceProfile, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLInstanceProfile{}, err
	}
	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return AutoDLInstanceProfile{}, err
	}
	index := findAutoDLInstance(document.Instances, strings.TrimSpace(instanceID))
	if index < 0 {
		return AutoDLInstanceProfile{}, ErrAutoDLInstanceNotFound
	}
	return document.Instances[index], nil
}

// Password implements autodl.TunnelPasswordSource for backend-only tunnel
// construction. The returned byte slice is newly owned by the caller.
func (service *Settings) Password(ctx context.Context, credentialRef string) ([]byte, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return nil, err
	}
	service.autoDLSettingsMu.Lock()
	passwordStore := service.autoDLPasswords
	service.autoDLSettingsMu.Unlock()
	if passwordStore == nil {
		return nil, ErrAutoDLPasswordStoreUnavailable
	}
	secret, err := passwordStore.Get(ctx, autoDLKeychainService, strings.TrimSpace(credentialRef))
	if err != nil {
		return nil, fmt.Errorf("reading AutoDL password: %w", err)
	}
	password := make([]byte, len(secret))
	copy(password, secret)
	return password, nil
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
	startupCommand := strings.TrimSpace(mutation.StartupCommand)
	if len(startupCommand) > maxAutoDLStartupCommand {
		return AutoDLInstanceResponse{}, fmt.Errorf("%w: startup command", ErrAutoDLSettingsInvalid)
	}
	localPort := mutation.LocalPort
	if localPort < 0 || localPort > 65535 {
		return AutoDLInstanceResponse{}, fmt.Errorf("%w: local port", ErrAutoDLSettingsInvalid)
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
	connectionChanged := index >= 0 && autoDLConnectionChanged(profile, target, comfyPort, startupCommand, localPort, fingerprint)
	profile.Name = name
	profile.Host = target.Host
	profile.SSHPort = target.Port
	profile.SSHUser = target.User
	profile.ComfyPort = comfyPort
	profile.StartupCommand = startupCommand
	profile.LocalPort = localPort
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
	hasPassword := false
	if mutation.Password == "" && index >= 0 {
		hasPassword, err = probeAutoDLPassword(ctx, service.autoDLPasswords, profile.CredentialRef)
		if err != nil {
			return AutoDLInstanceResponse{}, err
		}
	}
	if err := service.persistAutoDLDocumentLocked(document); err != nil {
		return AutoDLInstanceResponse{}, err
	}
	response := AutoDLInstanceResponse{AutoDLInstanceProfile: profile, HasPassword: hasPassword}
	if mutation.Password == "" {
		return response, nil
	}
	if service.autoDLPasswords == nil {
		return response, &AutoDLCredentialWriteError{InstanceID: profile.ID, CredentialRef: profile.CredentialRef, Cause: ErrAutoDLPasswordStoreUnavailable}
	}
	if err := service.autoDLPasswords.Set(ctx, autoDLKeychainService, profile.CredentialRef, mutation.Password); err != nil {
		return response, &AutoDLCredentialWriteError{InstanceID: profile.ID, CredentialRef: profile.CredentialRef, Cause: err}
	}
	response.HasPassword = true
	return response, nil
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
		return AutoDLInstanceResponse{}, ErrAutoDLPasswordStoreUnavailable
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
		return AutoDLInstanceResponse{}, ErrAutoDLPasswordStoreUnavailable
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
	if service.autoDLPasswords == nil {
		return AutoDLSettingsResponse{}, ErrAutoDLPasswordStoreUnavailable
	}
	passwordStates, err := probeAutoDLPasswordStates(ctx, service.autoDLPasswords, document.Instances)
	if err != nil {
		return AutoDLSettingsResponse{}, err
	}
	profile := document.Instances[index]
	var previous string
	hadPrevious := false
	previous, err = service.autoDLPasswords.Get(ctx, autoDLKeychainService, profile.CredentialRef)
	if err != nil && !errors.Is(err, platformkeychain.ErrNotFound) {
		return AutoDLSettingsResponse{}, fmt.Errorf("reading AutoDL password before deletion: %w", err)
	}
	hadPrevious = err == nil
	if err := service.autoDLPasswords.Delete(ctx, autoDLKeychainService, profile.CredentialRef); err != nil {
		return AutoDLSettingsResponse{}, fmt.Errorf("deleting AutoDL password: %w", err)
	}
	document.Instances = append(document.Instances[:index], document.Instances[index+1:]...)
	if err := service.persistAutoDLDocumentLocked(document); err != nil {
		if hadPrevious {
			compensationCtx, cancel := autoDLCompensationContext(ctx)
			rollbackErr := service.autoDLPasswords.Set(compensationCtx, autoDLKeychainService, profile.CredentialRef, previous)
			cancel()
			if rollbackErr != nil {
				return AutoDLSettingsResponse{}, errors.Join(err, fmt.Errorf("restoring AutoDL password: %w", rollbackErr))
			}
		}
		return AutoDLSettingsResponse{}, err
	}
	return buildAutoDLSettingsResponseFromStates(document, passwordStates), nil
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
		return autoDLSettingsDocument{Version: autoDLSettingsVersion, Instances: []AutoDLInstanceProfile{}, WorkflowProfiles: []AutoDLWorkflowProfile{}, WorkflowDefaults: []AutoDLWorkflowDefault{}}, nil
	}
	var envelope struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return autoDLSettingsDocument{}, fmt.Errorf("%w: invalid JSON", ErrAutoDLSettingsCorrupt)
	}
	if envelope.Version == 1 {
		document, err := migrateAutoDLDocumentV1(raw)
		if err != nil {
			return autoDLSettingsDocument{}, fmt.Errorf("%w: invalid v1 document", ErrAutoDLSettingsCorrupt)
		}
		if err := validateAutoDLDocument(document); err != nil {
			return autoDLSettingsDocument{}, fmt.Errorf("%w: invalid migrated document", ErrAutoDLSettingsCorrupt)
		}
		if err := service.persistAutoDLDocumentLocked(document); err != nil {
			return autoDLSettingsDocument{}, err
		}
		return document, nil
	}
	if envelope.Version == 2 {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		var document autoDLSettingsDocument
		if err := decoder.Decode(&document); err != nil {
			return autoDLSettingsDocument{}, fmt.Errorf("%w: invalid v2 document", ErrAutoDLSettingsCorrupt)
		}
		if err := ensureJSONEOF(decoder); err != nil {
			return autoDLSettingsDocument{}, fmt.Errorf("%w: trailing v2 JSON", ErrAutoDLSettingsCorrupt)
		}
		document.Version = autoDLSettingsVersion
		if err := validateAutoDLDocument(document); err != nil {
			return autoDLSettingsDocument{}, fmt.Errorf("%w: invalid migrated v2 document", ErrAutoDLSettingsCorrupt)
		}
		if err := service.persistAutoDLDocumentLocked(document); err != nil {
			return autoDLSettingsDocument{}, err
		}
		return document, nil
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
	normalizeAutoDLDocumentValidations(&document)
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
	if document.WorkflowDefaults == nil {
		document.WorkflowDefaults = []AutoDLWorkflowDefault{}
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

func buildAutoDLSettingsResponse(ctx context.Context, passwordStore autoDLPasswordStore, document autoDLSettingsDocument) (AutoDLSettingsResponse, error) {
	passwordStates, err := probeAutoDLPasswordStates(ctx, passwordStore, document.Instances)
	if err != nil {
		return AutoDLSettingsResponse{}, err
	}
	return buildAutoDLSettingsResponseFromStates(document, passwordStates), nil
}

func buildAutoDLSettingsResponseFromStates(document autoDLSettingsDocument, passwordStates map[string]bool) AutoDLSettingsResponse {
	normalizeAutoDLDocumentValidations(&document)
	response := AutoDLSettingsResponse{
		Instances:        make([]AutoDLInstanceResponse, 0, len(document.Instances)),
		WorkflowProfiles: make([]AutoDLWorkflowProfileResponse, 0, len(document.WorkflowProfiles)),
		WorkflowDefaults: make([]AutoDLWorkflowDefault, len(document.WorkflowDefaults)),
	}
	copy(response.WorkflowDefaults, document.WorkflowDefaults)
	for _, profile := range document.Instances {
		response.Instances = append(response.Instances, AutoDLInstanceResponse{AutoDLInstanceProfile: profile, HasPassword: passwordStates[profile.CredentialRef]})
	}
	for _, profile := range document.WorkflowProfiles {
		response.WorkflowProfiles = append(response.WorkflowProfiles, autoDLWorkflowResponse(profile))
	}
	return response
}

func normalizeAutoDLDocumentValidations(document *autoDLSettingsDocument) {
	versionsByProfileID := make(map[string]AutoDLWorkflowVersion, len(document.WorkflowProfiles))
	routesByProfileID := make(map[string]string, len(document.WorkflowProfiles))
	for _, profile := range document.WorkflowProfiles {
		routesByProfileID[profile.ID] = profile.RouteID
		if version, ok := currentAutoDLWorkflowVersion(profile); ok {
			versionsByProfileID[profile.ID] = version
		}
	}
	for index := range document.WorkflowDefaults {
		if document.WorkflowDefaults[index].RouteID == "" {
			document.WorkflowDefaults[index].RouteID = routesByProfileID[document.WorkflowDefaults[index].WorkflowProfileID]
		}
	}
	for instanceIndex := range document.Instances {
		for validationIndex := range document.Instances[instanceIndex].WorkflowValidations {
			validation := &document.Instances[instanceIndex].WorkflowValidations[validationIndex]
			current, found := versionsByProfileID[validation.WorkflowProfileID]
			if !found || current.BindingStatus != AutoDLBindingStatusConfirmed ||
				validation.VersionID != current.VersionID || validation.WorkflowDigest != current.WorkflowDigest ||
				validation.APITemplateDigest != current.APITemplateDigest {
				validation.Status = AutoDLWorkflowValidationStale
				if validation.Reason != "workflow_changed" && validation.Reason != "migrated_v1_without_confirmed_bindings" {
					validation.Reason = "profile_needs_revalidation"
				}
			}
		}
	}
}

func probeAutoDLPasswordStates(ctx context.Context, passwordStore autoDLPasswordStore, instances []AutoDLInstanceProfile) (map[string]bool, error) {
	states := make(map[string]bool, len(instances))
	for _, profile := range instances {
		hasPassword, err := probeAutoDLPassword(ctx, passwordStore, profile.CredentialRef)
		if err != nil {
			return nil, err
		}
		states[profile.CredentialRef] = hasPassword
	}
	return states, nil
}

func probeAutoDLPassword(ctx context.Context, passwordStore autoDLPasswordStore, credentialRef string) (bool, error) {
	if passwordStore == nil {
		return false, nil
	}
	hasPassword, err := passwordStore.Exists(ctx, autoDLKeychainService, credentialRef)
	if err != nil {
		return false, fmt.Errorf("checking AutoDL password: %w", err)
	}
	return hasPassword, nil
}

func autoDLCompensationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), autoDLCompensationTimeout)
}

func validateAutoDLDocument(document autoDLSettingsDocument) error {
	if document.Version != autoDLSettingsVersion {
		return fmt.Errorf("unsupported version")
	}
	instanceIDs := make(map[string]struct{}, len(document.Instances))
	credentialRefs := make(map[string]struct{}, len(document.Instances))
	for _, instance := range document.Instances {
		if !autoDLProfileIDPattern.MatchString(instance.ID) || !autoDLProfileIDPattern.MatchString(instance.CredentialRef) || strings.TrimSpace(instance.Name) == "" || len(instance.Name) > 128 || instance.ComfyPort < 1 || instance.ComfyPort > 65535 || len(instance.StartupCommand) > maxAutoDLStartupCommand || instance.LocalPort < 0 || instance.LocalPort > 65535 {
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
		if !autoDLProfileIDPattern.MatchString(profile.ID) || strings.TrimSpace(profile.Name) == "" || len(profile.Name) > 128 ||
			strings.TrimSpace(profile.MediaKind) == "" || strings.TrimSpace(profile.RouteID) == "" ||
			profile.CurrentVersionID == "" || len(profile.Versions) == 0 {
			return fmt.Errorf("invalid workflow profile")
		}
		if profile.RouteID != "autodl.legacy" {
			if _, _, err := normalizeAutoDLWorkflowRoute(profile.MediaKind, profile.RouteID); err != nil {
				return fmt.Errorf("invalid workflow profile route")
			}
		}
		if _, duplicate := profileIDs[profile.ID]; duplicate {
			return fmt.Errorf("duplicate workflow profile")
		}
		profileIDs[profile.ID] = struct{}{}
		versionIDs := make(map[string]struct{}, len(profile.Versions))
		currentFound := false
		for index, version := range profile.Versions {
			if version.VersionID == "" || version.Sequence != index+1 || version.WorkflowDigest == "" ||
				(version.BindingStatus != AutoDLBindingStatusConfirmed && version.BindingStatus != AutoDLBindingStatusUnconfirmed) ||
				version.References.Min < 0 || version.References.Max < version.References.Min || version.References.Max > 8 {
				return fmt.Errorf("invalid workflow version")
			}
			if _, duplicate := versionIDs[version.VersionID]; duplicate {
				return fmt.Errorf("duplicate workflow version")
			}
			versionIDs[version.VersionID] = struct{}{}
			currentFound = currentFound || version.VersionID == profile.CurrentVersionID
			for _, raw := range []json.RawMessage{version.UIWorkflow, version.APITemplate} {
				if autoDLRawPresent(raw) && !json.Valid(raw) {
					return fmt.Errorf("invalid workflow JSON")
				}
			}
			if autoDLRawPresent(version.UIWorkflow) && version.WorkflowDigest != autoDLPayloadDigest(version.UIWorkflow) {
				return fmt.Errorf("workflow digest mismatch")
			}
			if autoDLRawPresent(version.APITemplate) && version.APITemplateDigest != autoDLPayloadDigest(version.APITemplate) {
				return fmt.Errorf("API template digest mismatch")
			}
		}
		if !currentFound {
			return fmt.Errorf("current workflow version not found")
		}
	}
	defaultIDs := make(map[string]struct{}, len(document.WorkflowDefaults))
	for _, workflowDefault := range document.WorkflowDefaults {
		if !autoDLProfileIDPattern.MatchString(workflowDefault.ID) || workflowDefault.MinReferences < 0 ||
			workflowDefault.MaxReferences < workflowDefault.MinReferences || workflowDefault.MaxReferences > 8 {
			return fmt.Errorf("invalid workflow default")
		}
		if _, found := profileIDs[workflowDefault.WorkflowProfileID]; !found {
			return fmt.Errorf("workflow default profile not found")
		}
		if _, duplicate := defaultIDs[workflowDefault.ID]; duplicate {
			return fmt.Errorf("duplicate workflow default")
		}
		defaultIDs[workflowDefault.ID] = struct{}{}
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

func autoDLConnectionChanged(profile AutoDLInstanceProfile, target platformautodl.SSHLoginTarget, comfyPort int, startupCommand string, localPort int, fingerprint string) bool {
	return profile.Host != target.Host ||
		profile.SSHPort != target.Port ||
		profile.SSHUser != target.User ||
		profile.ComfyPort != comfyPort ||
		profile.StartupCommand != startupCommand ||
		profile.LocalPort != localPort ||
		profile.HostFingerprint != fingerprint
}

func markAutoDLValidationsForRevalidation(validations []AutoDLWorkflowValidation, reason string) {
	for index := range validations {
		validations[index].Status = AutoDLWorkflowStatusNeedsRevalidation
		validations[index].Reason = reason
	}
}

func validAutoDLWorkflowStatus(status string) bool {
	return status == AutoDLWorkflowValidationReady || status == AutoDLWorkflowValidationInvalid ||
		status == AutoDLWorkflowValidationUnknown || status == AutoDLWorkflowValidationStale
}

func requireAutoDLContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("AutoDL settings context is required")
	}
	return ctx.Err()
}
