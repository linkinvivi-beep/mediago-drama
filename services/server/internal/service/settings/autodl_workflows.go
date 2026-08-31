package settings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/comfyui"
)

type autoDLSettingsDocumentV1 struct {
	Version          int                       `json:"version"`
	Instances        []autoDLInstanceProfileV1 `json:"instances"`
	WorkflowProfiles []autoDLWorkflowProfileV1 `json:"workflowProfiles"`
}

type autoDLInstanceProfileV1 struct {
	ID                  string                       `json:"id"`
	Name                string                       `json:"name"`
	Host                string                       `json:"host"`
	SSHPort             int                          `json:"sshPort"`
	SSHUser             string                       `json:"sshUser"`
	ComfyPort           int                          `json:"comfyPort"`
	HostFingerprint     string                       `json:"hostFingerprint,omitempty"`
	CredentialRef       string                       `json:"credentialRef"`
	Enabled             bool                         `json:"enabled"`
	WorkflowValidations []autoDLWorkflowValidationV1 `json:"workflowValidations,omitempty"`
}

type autoDLWorkflowValidationV1 struct {
	WorkflowProfileID string `json:"workflowProfileId"`
	Status            string `json:"status"`
	WorkflowDigest    string `json:"workflowDigest,omitempty"`
	APITemplateDigest string `json:"apiTemplateDigest,omitempty"`
	ValidatedAt       string `json:"validatedAt,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

type autoDLWorkflowProfileV1 struct {
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

// SaveAutoDLWorkflowProfile stores an unconfirmed compatibility snapshot.
// New HTTP administration will use the compiler-backed registry in Task 3.
func (service *Settings) SaveAutoDLWorkflowProfile(ctx context.Context, mutation AutoDLWorkflowProfileMutation) (AutoDLWorkflowProfileResponse, error) {
	return service.saveAutoDLWorkflowProfile(ctx, mutation, false)
}

// SaveValidatedAutoDLWorkflowProfile is restricted to trusted backend callers.
func (service *Settings) SaveValidatedAutoDLWorkflowProfile(ctx context.Context, mutation AutoDLWorkflowProfileMutation) (AutoDLWorkflowProfileResponse, error) {
	return service.saveAutoDLWorkflowProfile(ctx, mutation, true)
}

func (service *Settings) saveAutoDLWorkflowProfile(ctx context.Context, mutation AutoDLWorkflowProfileMutation, trusted bool) (AutoDLWorkflowProfileResponse, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	id := strings.TrimSpace(mutation.ID)
	name := strings.TrimSpace(mutation.Name)
	if !autoDLProfileIDPattern.MatchString(id) || name == "" || len(name) > 128 {
		return AutoDLWorkflowProfileResponse{}, fmt.Errorf("%w: workflow identity", ErrAutoDLSettingsInvalid)
	}
	if len(mutation.Workflow) > 0 && !json.Valid(mutation.Workflow) {
		return AutoDLWorkflowProfileResponse{}, fmt.Errorf("%w: workflow JSON", ErrAutoDLSettingsInvalid)
	}
	if len(mutation.APITemplate) > 0 && !json.Valid(mutation.APITemplate) {
		return AutoDLWorkflowProfileResponse{}, fmt.Errorf("%w: API template JSON", ErrAutoDLSettingsInvalid)
	}
	workflowDigest := strings.TrimSpace(mutation.WorkflowDigest)
	if len(mutation.Workflow) > 0 {
		workflowDigest = autoDLPayloadDigest(mutation.Workflow)
	}
	apiDigest := strings.TrimSpace(mutation.APITemplateDigest)
	if len(mutation.APITemplate) > 0 {
		apiDigest = autoDLPayloadDigest(mutation.APITemplate)
	}
	if workflowDigest == "" {
		return AutoDLWorkflowProfileResponse{}, fmt.Errorf("%w: workflow digest", ErrAutoDLSettingsInvalid)
	}
	confirmed := trusted && len(mutation.Workflow) > 0 && len(mutation.APITemplate) > 0

	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	profileIndex := findAutoDLWorkflowProfile(document.WorkflowProfiles, id)
	if profileIndex < 0 {
		document.WorkflowProfiles = append(document.WorkflowProfiles, AutoDLWorkflowProfile{
			ID: id, Name: name, MediaKind: "image", RouteID: "autodl.image",
			Enabled: confirmed, AutoSelectable: confirmed, Versions: []AutoDLWorkflowVersion{},
		})
		profileIndex = len(document.WorkflowProfiles) - 1
	}
	profile := &document.WorkflowProfiles[profileIndex]
	profile.Name = name
	if current, ok := currentAutoDLWorkflowVersion(*profile); ok &&
		current.WorkflowDigest == workflowDigest && current.APITemplateDigest == apiDigest {
		return autoDLWorkflowResponse(*profile), nil
	}
	sequence := len(profile.Versions) + 1
	bindingStatus := AutoDLBindingStatusUnconfirmed
	bindings := comfyui.WorkflowBindings{}
	if confirmed {
		bindingStatus = AutoDLBindingStatusConfirmed
		bindings.Confirmed = true
	}
	version := AutoDLWorkflowVersion{
		VersionID: fmt.Sprintf("%s-v%d", id, sequence), Sequence: sequence,
		SourceVersionID: strings.TrimSpace(mutation.Version), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		UIWorkflow: bytes.Clone(mutation.Workflow), APITemplate: bytes.Clone(mutation.APITemplate),
		WorkflowDigest: workflowDigest, APITemplateDigest: apiDigest,
		BindingStatus: bindingStatus, Bindings: bindings, References: AutoDLReferenceContract{Min: 0, Max: 8},
	}
	profile.Versions = append(profile.Versions, version)
	profile.CurrentVersionID = version.VersionID
	if confirmed {
		profile.Enabled = true
		profile.AutoSelectable = true
	}
	for instanceIndex := range document.Instances {
		for validationIndex := range document.Instances[instanceIndex].WorkflowValidations {
			validation := &document.Instances[instanceIndex].WorkflowValidations[validationIndex]
			if validation.WorkflowProfileID == id {
				validation.Status = AutoDLWorkflowValidationStale
				validation.Reason = "workflow_changed"
			}
		}
	}
	if err := service.persistAutoDLDocumentLocked(document); err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	return autoDLWorkflowResponse(*profile), nil
}

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
	version, ok := currentAutoDLWorkflowVersion(document.WorkflowProfiles[profileIndex])
	if !ok {
		return AutoDLInstanceResponse{}, ErrAutoDLWorkflowNotFound
	}
	status := strings.TrimSpace(validation.Status)
	if !validAutoDLWorkflowStatus(status) {
		return AutoDLInstanceResponse{}, fmt.Errorf("%w: workflow validation", ErrAutoDLSettingsInvalid)
	}
	if validation.VersionID != "" && strings.TrimSpace(validation.VersionID) != version.VersionID {
		return AutoDLInstanceResponse{}, fmt.Errorf("%w: workflow version", ErrAutoDLSettingsInvalid)
	}
	if status == AutoDLWorkflowValidationReady &&
		(version.BindingStatus != AutoDLBindingStatusConfirmed || validation.WorkflowDigest != version.WorkflowDigest || validation.APITemplateDigest != version.APITemplateDigest) {
		return AutoDLInstanceResponse{}, fmt.Errorf("%w: ready workflow validation digests", ErrAutoDLSettingsInvalid)
	}
	validation.WorkflowProfileID = profileID
	validation.VersionID = version.VersionID
	validation.WorkflowDigest = version.WorkflowDigest
	validation.APITemplateDigest = version.APITemplateDigest
	validation.Status = status
	validation.ValidatedAt = strings.TrimSpace(validation.ValidatedAt)
	validation.Reason = strings.TrimSpace(validation.Reason)
	if len(validation.ValidatedAt) > 64 || len(validation.Reason) > 512 {
		return AutoDLInstanceResponse{}, fmt.Errorf("%w: workflow validation metadata", ErrAutoDLSettingsInvalid)
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
	hasPassword, err := probeAutoDLPassword(ctx, service.autoDLPasswords, instance.CredentialRef)
	if err != nil {
		return AutoDLInstanceResponse{}, err
	}
	if err := service.persistAutoDLDocumentLocked(document); err != nil {
		return AutoDLInstanceResponse{}, err
	}
	return AutoDLInstanceResponse{AutoDLInstanceProfile: *instance, HasPassword: hasPassword}, nil
}

func migrateAutoDLDocumentV1(raw string) (autoDLSettingsDocument, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var legacy autoDLSettingsDocumentV1
	if err := decoder.Decode(&legacy); err != nil {
		return autoDLSettingsDocument{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil || legacy.Version != 1 {
		return autoDLSettingsDocument{}, fmt.Errorf("invalid v1 document")
	}
	document := autoDLSettingsDocument{
		Version: autoDLSettingsVersion, Instances: make([]AutoDLInstanceProfile, 0, len(legacy.Instances)),
		WorkflowProfiles: make([]AutoDLWorkflowProfile, 0, len(legacy.WorkflowProfiles)), WorkflowDefaults: []AutoDLWorkflowDefault{},
	}
	versionIDs := make(map[string]string, len(legacy.WorkflowProfiles))
	for _, old := range legacy.WorkflowProfiles {
		versionID := old.ID + "-v1"
		versionIDs[old.ID] = versionID
		document.WorkflowProfiles = append(document.WorkflowProfiles, AutoDLWorkflowProfile{
			ID: old.ID, Name: old.Name, Description: "Migrated from MediaLink settings v1",
			MediaKind: "legacy", RouteID: "autodl.legacy", Archived: true,
			CurrentVersionID: versionID,
			Versions: []AutoDLWorkflowVersion{{
				VersionID: versionID, Sequence: 1, SourceVersionID: old.Version,
				CreatedAt: "1970-01-01T00:00:00Z", UIWorkflow: bytes.Clone(old.Workflow), APITemplate: bytes.Clone(old.APITemplate),
				WorkflowDigest: old.WorkflowDigest, APITemplateDigest: old.APITemplateDigest,
				BindingStatus: AutoDLBindingStatusUnconfirmed, References: AutoDLReferenceContract{Min: 0, Max: 8},
			}},
		})
	}
	for _, old := range legacy.Instances {
		instance := AutoDLInstanceProfile{
			ID: old.ID, Name: old.Name, Host: old.Host, SSHPort: old.SSHPort, SSHUser: old.SSHUser,
			ComfyPort: old.ComfyPort, HostFingerprint: old.HostFingerprint, CredentialRef: old.CredentialRef, Enabled: old.Enabled,
			WorkflowValidations: make([]AutoDLWorkflowValidation, 0, len(old.WorkflowValidations)),
		}
		for _, validation := range old.WorkflowValidations {
			instance.WorkflowValidations = append(instance.WorkflowValidations, AutoDLWorkflowValidation{
				WorkflowProfileID: validation.WorkflowProfileID, VersionID: versionIDs[validation.WorkflowProfileID],
				Status: AutoDLWorkflowValidationStale, WorkflowDigest: validation.WorkflowDigest,
				APITemplateDigest: validation.APITemplateDigest, ValidatedAt: validation.ValidatedAt,
				Reason: "migrated_v1_without_confirmed_bindings",
			})
		}
		document.Instances = append(document.Instances, instance)
	}
	return document, nil
}

func currentAutoDLWorkflowVersion(profile AutoDLWorkflowProfile) (AutoDLWorkflowVersion, bool) {
	for _, version := range profile.Versions {
		if version.VersionID == profile.CurrentVersionID {
			return version, true
		}
	}
	return AutoDLWorkflowVersion{}, false
}

func autoDLWorkflowResponse(profile AutoDLWorkflowProfile) AutoDLWorkflowProfileResponse {
	response := AutoDLWorkflowProfileResponse{
		ID: profile.ID, Name: profile.Name, Description: profile.Description, MediaKind: profile.MediaKind,
		RouteID: profile.RouteID, Enabled: profile.Enabled, AutoSelectable: profile.AutoSelectable,
		Archived: profile.Archived, CurrentVersionID: profile.CurrentVersionID,
		Versions: make([]AutoDLWorkflowVersionResponse, 0, len(profile.Versions)),
	}
	for _, version := range profile.Versions {
		response.Versions = append(response.Versions, AutoDLWorkflowVersionResponse{
			VersionID: version.VersionID, Sequence: version.Sequence, SourceVersionID: version.SourceVersionID,
			CreatedAt: version.CreatedAt, WorkflowDigest: version.WorkflowDigest, APITemplateDigest: version.APITemplateDigest,
			BindingStatus: version.BindingStatus, Bindings: version.Bindings, References: version.References, PromptGuide: version.PromptGuide,
		})
	}
	if current, ok := currentAutoDLWorkflowVersion(profile); ok {
		response.WorkflowDigest = current.WorkflowDigest
		response.APITemplateDigest = current.APITemplateDigest
		response.Ready = current.BindingStatus == AutoDLBindingStatusConfirmed
		response.Status = AutoDLWorkflowValidationStale
		if response.Ready {
			response.Status = AutoDLWorkflowValidationReady
		}
	}
	return response
}

func autoDLPayloadDigest(raw json.RawMessage) string {
	canonical, err := json.Marshal(json.RawMessage(raw))
	if err != nil {
		canonical = raw
	}
	digest := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", digest[:])
}

func autoDLRawPresent(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (service *Settings) CreateAutoDLWorkflow(ctx context.Context, mutation AutoDLWorkflowCreateMutation) (AutoDLWorkflowProfileResponse, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	id, name, description, promptGuide, err := normalizeWorkflowIdentity(mutation.ID, mutation.Name, mutation.Description, mutation.PromptGuide)
	if err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	if err := validateCompiledWorkflow(mutation.Compiled, mutation.References); err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	if findAutoDLWorkflowProfile(document.WorkflowProfiles, id) >= 0 {
		return AutoDLWorkflowProfileResponse{}, ErrAutoDLWorkflowAlreadyExists
	}
	version := autoDLVersionFromCompiled(id, 1, "", mutation.Compiled, mutation.References, promptGuide)
	profile := AutoDLWorkflowProfile{
		ID: id, Name: name, Description: description, MediaKind: "image", RouteID: "autodl.image",
		CurrentVersionID: version.VersionID, Versions: []AutoDLWorkflowVersion{version},
	}
	document.WorkflowProfiles = append(document.WorkflowProfiles, profile)
	if err := service.persistAutoDLDocumentLocked(document); err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	return autoDLWorkflowResponse(profile), nil
}

func (service *Settings) ReplaceAutoDLWorkflow(ctx context.Context, profileID string, mutation AutoDLWorkflowVersionMutation) (AutoDLWorkflowProfileResponse, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	promptGuide := strings.TrimSpace(mutation.PromptGuide)
	if len(promptGuide) > 16*1024 {
		return AutoDLWorkflowProfileResponse{}, fmt.Errorf("%w: prompt guide", ErrAutoDLSettingsInvalid)
	}
	if err := validateCompiledWorkflow(mutation.Compiled, mutation.References); err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	index := findAutoDLWorkflowProfile(document.WorkflowProfiles, strings.TrimSpace(profileID))
	if index < 0 {
		return AutoDLWorkflowProfileResponse{}, ErrAutoDLWorkflowNotFound
	}
	profile := &document.WorkflowProfiles[index]
	if strings.TrimSpace(mutation.ExpectedCurrentVersionID) != profile.CurrentVersionID {
		return AutoDLWorkflowProfileResponse{}, ErrAutoDLWorkflowVersionConflict
	}
	version := autoDLVersionFromCompiled(profile.ID, len(profile.Versions)+1, profile.CurrentVersionID, mutation.Compiled, mutation.References, promptGuide)
	profile.Versions = append(profile.Versions, version)
	profile.CurrentVersionID = version.VersionID
	profile.Enabled = false
	profile.AutoSelectable = false
	markWorkflowProfileValidationsStale(document.Instances, profile.ID, "workflow_changed")
	if err := service.persistAutoDLDocumentLocked(document); err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	return autoDLWorkflowResponse(*profile), nil
}

func (service *Settings) DuplicateAutoDLWorkflow(ctx context.Context, sourceProfileID string, mutation AutoDLWorkflowDuplicateMutation) (AutoDLWorkflowProfileResponse, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	id, name, description, _, err := normalizeWorkflowIdentity(mutation.ID, mutation.Name, mutation.Description, "")
	if err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	if findAutoDLWorkflowProfile(document.WorkflowProfiles, id) >= 0 {
		return AutoDLWorkflowProfileResponse{}, ErrAutoDLWorkflowAlreadyExists
	}
	sourceIndex := findAutoDLWorkflowProfile(document.WorkflowProfiles, strings.TrimSpace(sourceProfileID))
	if sourceIndex < 0 {
		return AutoDLWorkflowProfileResponse{}, ErrAutoDLWorkflowNotFound
	}
	sourceVersion, ok := currentAutoDLWorkflowVersion(document.WorkflowProfiles[sourceIndex])
	if !ok {
		return AutoDLWorkflowProfileResponse{}, ErrAutoDLWorkflowVersionNotFound
	}
	version := sourceVersion
	version.VersionID = id + "-v1"
	version.Sequence = 1
	version.SourceVersionID = sourceVersion.VersionID
	version.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	version.UIWorkflow = bytes.Clone(sourceVersion.UIWorkflow)
	version.APITemplate = bytes.Clone(sourceVersion.APITemplate)
	profile := AutoDLWorkflowProfile{
		ID: id, Name: name, Description: description, MediaKind: "image", RouteID: "autodl.image",
		CurrentVersionID: version.VersionID, Versions: []AutoDLWorkflowVersion{version},
	}
	document.WorkflowProfiles = append(document.WorkflowProfiles, profile)
	if err := service.persistAutoDLDocumentLocked(document); err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	return autoDLWorkflowResponse(profile), nil
}

func (service *Settings) SetAutoDLWorkflowState(ctx context.Context, profileID string, mutation AutoDLWorkflowStateMutation) (AutoDLWorkflowProfileResponse, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	index := findAutoDLWorkflowProfile(document.WorkflowProfiles, strings.TrimSpace(profileID))
	if index < 0 {
		return AutoDLWorkflowProfileResponse{}, ErrAutoDLWorkflowNotFound
	}
	profile := &document.WorkflowProfiles[index]
	archived, enabled, autoSelectable := profile.Archived, profile.Enabled, profile.AutoSelectable
	if mutation.Archived != nil {
		archived = *mutation.Archived
	}
	if mutation.Enabled != nil {
		enabled = *mutation.Enabled
	}
	if mutation.AutoSelectable != nil {
		autoSelectable = *mutation.AutoSelectable
	}
	if archived {
		enabled, autoSelectable = false, false
	}
	if autoSelectable && !enabled {
		return AutoDLWorkflowProfileResponse{}, fmt.Errorf("%w: auto-selectable workflow must be enabled", ErrAutoDLSettingsInvalid)
	}
	if enabled {
		version, ok := currentAutoDLWorkflowVersion(*profile)
		if !ok || version.BindingStatus != AutoDLBindingStatusConfirmed || !hasReadyWorkflowValidation(document.Instances, profile.ID, version) {
			return AutoDLWorkflowProfileResponse{}, ErrAutoDLWorkflowUnavailable
		}
	}
	profile.Archived, profile.Enabled, profile.AutoSelectable = archived, enabled, autoSelectable
	if err := service.persistAutoDLDocumentLocked(document); err != nil {
		return AutoDLWorkflowProfileResponse{}, err
	}
	return autoDLWorkflowResponse(*profile), nil
}

func (service *Settings) SetAutoDLWorkflowDefaults(ctx context.Context, defaults []AutoDLWorkflowDefault) (AutoDLSettingsResponse, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return AutoDLSettingsResponse{}, err
	}
	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return AutoDLSettingsResponse{}, err
	}
	normalized := append([]AutoDLWorkflowDefault(nil), defaults...)
	seenIDs := make(map[string]struct{}, len(normalized))
	for index := range normalized {
		item := &normalized[index]
		item.ID = strings.TrimSpace(item.ID)
		item.WorkflowProfileID = strings.TrimSpace(item.WorkflowProfileID)
		profileIndex := findAutoDLWorkflowProfile(document.WorkflowProfiles, item.WorkflowProfileID)
		if !autoDLProfileIDPattern.MatchString(item.ID) || item.MinReferences < 0 || item.MaxReferences < item.MinReferences || item.MaxReferences > 8 || profileIndex < 0 {
			return AutoDLSettingsResponse{}, fmt.Errorf("%w: workflow default", ErrAutoDLSettingsInvalid)
		}
		version, found := currentAutoDLWorkflowVersion(document.WorkflowProfiles[profileIndex])
		if !found || item.MinReferences < version.References.Min || item.MaxReferences > version.References.Max {
			return AutoDLSettingsResponse{}, fmt.Errorf("%w: workflow default reference range", ErrAutoDLSettingsInvalid)
		}
		if _, duplicate := seenIDs[item.ID]; duplicate {
			return AutoDLSettingsResponse{}, fmt.Errorf("%w: duplicate workflow default", ErrAutoDLSettingsInvalid)
		}
		seenIDs[item.ID] = struct{}{}
		for previous := 0; previous < index; previous++ {
			if item.MinReferences <= normalized[previous].MaxReferences && normalized[previous].MinReferences <= item.MaxReferences {
				return AutoDLSettingsResponse{}, ErrAutoDLWorkflowDefaultOverlap
			}
		}
	}
	sort.Slice(normalized, func(left, right int) bool {
		if normalized[left].MinReferences == normalized[right].MinReferences {
			return normalized[left].ID < normalized[right].ID
		}
		return normalized[left].MinReferences < normalized[right].MinReferences
	})
	document.WorkflowDefaults = normalized
	passwordStates, err := probeAutoDLPasswordStates(ctx, service.autoDLPasswords, document.Instances)
	if err != nil {
		return AutoDLSettingsResponse{}, err
	}
	if err := service.persistAutoDLDocumentLocked(document); err != nil {
		return AutoDLSettingsResponse{}, err
	}
	return buildAutoDLSettingsResponseFromStates(document, passwordStates), nil
}

func (service *Settings) ResolveAutoDLWorkflow(ctx context.Context, request AutoDLWorkflowResolveRequest) (ResolvedAutoDLWorkflow, error) {
	if err := requireAutoDLContext(ctx); err != nil {
		return ResolvedAutoDLWorkflow{}, err
	}
	if request.ReferenceCount < 0 || request.ReferenceCount > 8 {
		return ResolvedAutoDLWorkflow{}, fmt.Errorf("%w: reference count", ErrAutoDLSettingsInvalid)
	}
	service.autoDLSettingsMu.Lock()
	defer service.autoDLSettingsMu.Unlock()
	document, err := service.loadAutoDLDocumentLocked()
	if err != nil {
		return ResolvedAutoDLWorkflow{}, err
	}
	profileID := strings.TrimSpace(request.WorkflowProfileID)
	selectedByDefault := profileID == ""
	if selectedByDefault {
		matches := make([]AutoDLWorkflowDefault, 0, 1)
		for _, candidate := range document.WorkflowDefaults {
			if request.ReferenceCount >= candidate.MinReferences && request.ReferenceCount <= candidate.MaxReferences {
				matches = append(matches, candidate)
			}
		}
		if len(matches) > 1 {
			return ResolvedAutoDLWorkflow{}, ErrAutoDLWorkflowDefaultAmbiguous
		}
		if len(matches) == 0 {
			return ResolvedAutoDLWorkflow{}, ErrAutoDLWorkflowUnavailable
		}
		profileID = matches[0].WorkflowProfileID
	}
	index := findAutoDLWorkflowProfile(document.WorkflowProfiles, profileID)
	if index < 0 {
		return ResolvedAutoDLWorkflow{}, ErrAutoDLWorkflowNotFound
	}
	profile := document.WorkflowProfiles[index]
	versionID := strings.TrimSpace(request.VersionID)
	if versionID == "" {
		versionID = profile.CurrentVersionID
	}
	if request.ForNewTask && versionID != profile.CurrentVersionID {
		return ResolvedAutoDLWorkflow{}, ErrAutoDLWorkflowUnavailable
	}
	version, found := findAutoDLWorkflowVersion(profile.Versions, versionID)
	if !found {
		return ResolvedAutoDLWorkflow{}, ErrAutoDLWorkflowVersionNotFound
	}
	if request.ForNewTask && (profile.Archived || !profile.Enabled || version.BindingStatus != AutoDLBindingStatusConfirmed ||
		request.ReferenceCount < version.References.Min || request.ReferenceCount > version.References.Max ||
		!hasReadyWorkflowValidation(document.Instances, profile.ID, version) || (selectedByDefault && !profile.AutoSelectable)) {
		return ResolvedAutoDLWorkflow{}, ErrAutoDLWorkflowUnavailable
	}
	return resolvedAutoDLWorkflow(profile, version), nil
}

func (service *Settings) GetAutoDLWorkflowVersion(ctx context.Context, profileID, versionID string) (ResolvedAutoDLWorkflow, error) {
	return service.ResolveAutoDLWorkflow(ctx, AutoDLWorkflowResolveRequest{WorkflowProfileID: profileID, VersionID: versionID})
}

func normalizeWorkflowIdentity(id, name, description, promptGuide string) (string, string, string, string, error) {
	id, name = strings.TrimSpace(id), strings.TrimSpace(name)
	description, promptGuide = strings.TrimSpace(description), strings.TrimSpace(promptGuide)
	if !autoDLProfileIDPattern.MatchString(id) || name == "" || len(name) > 128 || len(description) > 2048 || len(promptGuide) > 16*1024 {
		return "", "", "", "", fmt.Errorf("%w: workflow identity", ErrAutoDLSettingsInvalid)
	}
	return id, name, description, promptGuide, nil
}

func validateCompiledWorkflow(compiled comfyui.CompiledWorkflow, references AutoDLReferenceContract) error {
	if !compiled.Bindings.Confirmed || len(compiled.Bindings.Prompts) == 0 || len(compiled.Bindings.Outputs) == 0 ||
		!autoDLRawPresent(compiled.UIWorkflow) || !autoDLRawPresent(compiled.APITemplate) ||
		compiled.WorkflowDigest != autoDLPayloadDigest(compiled.UIWorkflow) || compiled.APITemplateDigest != autoDLPayloadDigest(compiled.APITemplate) ||
		references.Min < 0 || references.Max < references.Min || references.Max > 8 {
		return fmt.Errorf("%w: compiled workflow", ErrAutoDLSettingsInvalid)
	}
	seenSlots := make(map[int]struct{}, len(references.Slots))
	for _, slot := range references.Slots {
		if slot.Index < 0 || slot.Index >= references.Max {
			return fmt.Errorf("%w: reference slot", ErrAutoDLSettingsInvalid)
		}
		if _, duplicate := seenSlots[slot.Index]; duplicate {
			return fmt.Errorf("%w: duplicate reference slot", ErrAutoDLSettingsInvalid)
		}
		seenSlots[slot.Index] = struct{}{}
	}
	return nil
}

func autoDLVersionFromCompiled(profileID string, sequence int, sourceVersionID string, compiled comfyui.CompiledWorkflow, references AutoDLReferenceContract, promptGuide string) AutoDLWorkflowVersion {
	return AutoDLWorkflowVersion{
		VersionID: fmt.Sprintf("%s-v%d", profileID, sequence), Sequence: sequence, SourceVersionID: sourceVersionID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), UIWorkflow: bytes.Clone(compiled.UIWorkflow), APITemplate: bytes.Clone(compiled.APITemplate),
		WorkflowDigest: compiled.WorkflowDigest, APITemplateDigest: compiled.APITemplateDigest,
		BindingStatus: AutoDLBindingStatusConfirmed, Bindings: compiled.Bindings, References: references, PromptGuide: promptGuide,
	}
}

func hasReadyWorkflowValidation(instances []AutoDLInstanceProfile, profileID string, version AutoDLWorkflowVersion) bool {
	for _, instance := range instances {
		if !instance.Enabled {
			continue
		}
		for _, validation := range instance.WorkflowValidations {
			if validation.WorkflowProfileID == profileID && validation.VersionID == version.VersionID &&
				validation.Status == AutoDLWorkflowValidationReady && validation.WorkflowDigest == version.WorkflowDigest && validation.APITemplateDigest == version.APITemplateDigest {
				return true
			}
		}
	}
	return false
}

func markWorkflowProfileValidationsStale(instances []AutoDLInstanceProfile, profileID, reason string) {
	for instanceIndex := range instances {
		for validationIndex := range instances[instanceIndex].WorkflowValidations {
			validation := &instances[instanceIndex].WorkflowValidations[validationIndex]
			if validation.WorkflowProfileID == profileID {
				validation.Status = AutoDLWorkflowValidationStale
				validation.Reason = reason
			}
		}
	}
}

func findAutoDLWorkflowVersion(versions []AutoDLWorkflowVersion, versionID string) (AutoDLWorkflowVersion, bool) {
	for _, version := range versions {
		if version.VersionID == versionID {
			return version, true
		}
	}
	return AutoDLWorkflowVersion{}, false
}

func resolvedAutoDLWorkflow(profile AutoDLWorkflowProfile, version AutoDLWorkflowVersion) ResolvedAutoDLWorkflow {
	return ResolvedAutoDLWorkflow{
		ProfileID: profile.ID, VersionID: version.VersionID, Name: profile.Name,
		WorkflowDigest: version.WorkflowDigest, APITemplateDigest: version.APITemplateDigest,
		UIWorkflow: bytes.Clone(version.UIWorkflow), APITemplate: bytes.Clone(version.APITemplate), Bindings: version.Bindings,
		References: version.References, PromptGuide: version.PromptGuide, AutoSelectable: profile.AutoSelectable,
	}
}
