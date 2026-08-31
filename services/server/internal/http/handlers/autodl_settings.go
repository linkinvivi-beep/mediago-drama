package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	httpresponse "github.com/mediago-dev/mediago-drama/services/server/internal/http/response"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/autodl"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/comfyui"
	servicegeneration "github.com/mediago-dev/mediago-drama/services/server/internal/service/generation"
	servicesettings "github.com/mediago-dev/mediago-drama/services/server/internal/service/settings"
)

const autoDLSettingsBodyLimit int64 = 9 << 20

type autoDLSettingsStore interface {
	GetAutoDLSettings(context.Context) (servicesettings.AutoDLSettingsResponse, error)
	SaveAutoDLInstance(context.Context, servicesettings.AutoDLInstanceMutation) (servicesettings.AutoDLInstanceResponse, error)
	SetAutoDLInstancePassword(context.Context, string, string) (servicesettings.AutoDLInstanceResponse, error)
	ClearAutoDLInstancePassword(context.Context, string) (servicesettings.AutoDLInstanceResponse, error)
	DeleteAutoDLInstance(context.Context, string) (servicesettings.AutoDLSettingsResponse, error)
	DuplicateAutoDLWorkflow(context.Context, string, servicesettings.AutoDLWorkflowDuplicateMutation) (servicesettings.AutoDLWorkflowProfileResponse, error)
	SetAutoDLWorkflowState(context.Context, string, servicesettings.AutoDLWorkflowStateMutation) (servicesettings.AutoDLWorkflowProfileResponse, error)
	SetAutoDLWorkflowDefaults(context.Context, []servicesettings.AutoDLWorkflowDefault) (servicesettings.AutoDLSettingsResponse, error)
}

type autoDLWorkflowAdministrator interface {
	ScanFingerprint(context.Context, string) (servicegeneration.AutoDLFingerprintResult, error)
	CheckInstance(context.Context, string) (servicegeneration.AutoDLInstanceCheck, error)
	Preview(context.Context, servicegeneration.AutoDLWorkflowPreviewRequest) (servicegeneration.AutoDLWorkflowPreview, error)
	Create(context.Context, servicegeneration.AutoDLWorkflowCreateRequest) (servicesettings.AutoDLWorkflowProfileResponse, error)
	Replace(context.Context, string, servicegeneration.AutoDLWorkflowReplaceRequest) (servicesettings.AutoDLWorkflowProfileResponse, error)
	Validate(context.Context, servicegeneration.AutoDLWorkflowValidationRequest) (servicesettings.AutoDLWorkflowValidation, error)
}

type autoDLInstancesNotifier interface {
	NotifyInstancesChanged()
}

// AutoDLSettings exposes only redacted instance and immutable workflow
// administration. It never receives a tunnel URL or password from a service
// response.
type AutoDLSettings struct {
	settings autoDLSettingsStore
	admin    autoDLWorkflowAdministrator
	notifier autoDLInstancesNotifier
}

func NewAutoDLSettings(settings autoDLSettingsStore, admin autoDLWorkflowAdministrator, notifier autoDLInstancesNotifier) AutoDLSettings {
	return AutoDLSettings{settings: settings, admin: admin, notifier: notifier}
}

type autoDLInstanceRequest struct {
	Name            string `json:"name"`
	SSHCommand      string `json:"sshCommand"`
	Password        string `json:"password,omitempty"`
	ComfyPort       int    `json:"comfyPort,omitempty"`
	HostFingerprint string `json:"hostFingerprint,omitempty"`
	Enabled         bool   `json:"enabled"`
}

type autoDLPasswordRequest struct {
	Password string `json:"password"`
}

type autoDLWorkflowPreviewRequest struct {
	InstanceProfileID string          `json:"instanceProfileId"`
	UIWorkflow        json.RawMessage `json:"uiWorkflow"`
}

type autoDLWorkflowCreateRequest struct {
	InstanceProfileID string                                  `json:"instanceProfileId"`
	ID                string                                  `json:"id"`
	Name              string                                  `json:"name"`
	Description       string                                  `json:"description,omitempty"`
	UIWorkflow        json.RawMessage                         `json:"uiWorkflow"`
	Bindings          comfyui.WorkflowBindings                `json:"bindings"`
	References        servicesettings.AutoDLReferenceContract `json:"references"`
	PromptGuide       string                                  `json:"promptGuide,omitempty"`
}

type autoDLWorkflowReplaceRequest struct {
	InstanceProfileID        string                                  `json:"instanceProfileId"`
	ExpectedCurrentVersionID string                                  `json:"expectedCurrentVersionId"`
	UIWorkflow               json.RawMessage                         `json:"uiWorkflow"`
	Bindings                 comfyui.WorkflowBindings                `json:"bindings"`
	References               servicesettings.AutoDLReferenceContract `json:"references"`
	PromptGuide              string                                  `json:"promptGuide,omitempty"`
}

type autoDLWorkflowDuplicateRequest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type autoDLWorkflowDefaultsRequest struct {
	Defaults []servicesettings.AutoDLWorkflowDefault `json:"defaults"`
}

func (handler AutoDLSettings) HandleGet(context *gin.Context) {
	result, err := handler.settings.GetAutoDLSettings(context.Request.Context())
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	httpresponse.OK(context, result)
}

func (handler AutoDLSettings) HandlePostInstance(context *gin.Context) {
	payload, ok := decodeAutoDLJSON[autoDLInstanceRequest](context)
	if !ok {
		return
	}
	handler.saveInstance(context, "", payload)
}

func (handler AutoDLSettings) HandlePutInstance(context *gin.Context) {
	instanceID, ok := requiredPathParam(context, "instanceId", "instanceId")
	if !ok {
		return
	}
	payload, ok := decodeAutoDLJSON[autoDLInstanceRequest](context)
	if !ok {
		return
	}
	handler.saveInstance(context, instanceID, payload)
}

func (handler AutoDLSettings) saveInstance(context *gin.Context, instanceID string, payload autoDLInstanceRequest) {
	result, err := handler.settings.SaveAutoDLInstance(context.Request.Context(), servicesettings.AutoDLInstanceMutation{
		ID: instanceID, Name: payload.Name, SSHCommand: payload.SSHCommand, Password: payload.Password,
		ComfyPort: payload.ComfyPort, HostFingerprint: payload.HostFingerprint, Enabled: payload.Enabled,
	})
	if err != nil {
		var credentialErr *servicesettings.AutoDLCredentialWriteError
		if errors.As(err, &credentialErr) {
			handler.notify()
		}
		writeAutoDLSettingsError(context, err)
		return
	}
	handler.notify()
	httpresponse.OK(context, result)
}

func (handler AutoDLSettings) HandlePutPassword(context *gin.Context) {
	instanceID, ok := requiredPathParam(context, "instanceId", "instanceId")
	if !ok {
		return
	}
	payload, ok := decodeAutoDLJSON[autoDLPasswordRequest](context)
	if !ok {
		return
	}
	if payload.Password == "" {
		httpresponse.Error(context, http.StatusBadRequest, "password is required")
		return
	}
	result, err := handler.settings.SetAutoDLInstancePassword(context.Request.Context(), instanceID, payload.Password)
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	handler.notify()
	httpresponse.OK(context, result)
}

func (handler AutoDLSettings) HandleDeletePassword(context *gin.Context) {
	instanceID, ok := requiredPathParam(context, "instanceId", "instanceId")
	if !ok {
		return
	}
	result, err := handler.settings.ClearAutoDLInstancePassword(context.Request.Context(), instanceID)
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	handler.notify()
	httpresponse.OK(context, result)
}

func (handler AutoDLSettings) HandleDeleteInstance(context *gin.Context) {
	instanceID, ok := requiredPathParam(context, "instanceId", "instanceId")
	if !ok {
		return
	}
	result, err := handler.settings.DeleteAutoDLInstance(context.Request.Context(), instanceID)
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	handler.notify()
	httpresponse.OK(context, result)
}

func (handler AutoDLSettings) HandleScanFingerprint(context *gin.Context) {
	instanceID, ok := requiredPathParam(context, "instanceId", "instanceId")
	if !ok {
		return
	}
	result, err := handler.admin.ScanFingerprint(context.Request.Context(), instanceID)
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	httpresponse.OK(context, result)
}

func (handler AutoDLSettings) HandleCheckInstance(context *gin.Context) {
	instanceID, ok := requiredPathParam(context, "instanceId", "instanceId")
	if !ok {
		return
	}
	result, err := handler.admin.CheckInstance(context.Request.Context(), instanceID)
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	httpresponse.OK(context, result)
}

func (handler AutoDLSettings) HandlePreviewWorkflow(context *gin.Context) {
	payload, ok := decodeAutoDLJSON[autoDLWorkflowPreviewRequest](context)
	if !ok {
		return
	}
	result, err := handler.admin.Preview(context.Request.Context(), servicegeneration.AutoDLWorkflowPreviewRequest{
		InstanceProfileID: payload.InstanceProfileID, UIWorkflow: payload.UIWorkflow,
	})
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	httpresponse.OK(context, result)
}

func (handler AutoDLSettings) HandleCreateWorkflow(context *gin.Context) {
	payload, ok := decodeAutoDLJSON[autoDLWorkflowCreateRequest](context)
	if !ok {
		return
	}
	result, err := handler.admin.Create(context.Request.Context(), servicegeneration.AutoDLWorkflowCreateRequest{
		InstanceProfileID: payload.InstanceProfileID, ID: payload.ID, Name: payload.Name, Description: payload.Description,
		UIWorkflow: payload.UIWorkflow, Bindings: payload.Bindings, References: payload.References, PromptGuide: payload.PromptGuide,
	})
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	handler.notify()
	httpresponse.OK(context, result)
}

func (handler AutoDLSettings) HandleReplaceWorkflow(context *gin.Context) {
	profileID, ok := requiredPathParam(context, "profileId", "profileId")
	if !ok {
		return
	}
	payload, ok := decodeAutoDLJSON[autoDLWorkflowReplaceRequest](context)
	if !ok {
		return
	}
	result, err := handler.admin.Replace(context.Request.Context(), profileID, servicegeneration.AutoDLWorkflowReplaceRequest{
		InstanceProfileID: payload.InstanceProfileID, ExpectedCurrentVersionID: payload.ExpectedCurrentVersionID,
		UIWorkflow: payload.UIWorkflow, Bindings: payload.Bindings, References: payload.References, PromptGuide: payload.PromptGuide,
	})
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	handler.notify()
	httpresponse.OK(context, result)
}

func (handler AutoDLSettings) HandleDuplicateWorkflow(context *gin.Context) {
	profileID, ok := requiredPathParam(context, "profileId", "profileId")
	if !ok {
		return
	}
	payload, ok := decodeAutoDLJSON[autoDLWorkflowDuplicateRequest](context)
	if !ok {
		return
	}
	result, err := handler.settings.DuplicateAutoDLWorkflow(context.Request.Context(), profileID, servicesettings.AutoDLWorkflowDuplicateMutation{
		ID: payload.ID, Name: payload.Name, Description: payload.Description,
	})
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	handler.notify()
	httpresponse.OK(context, result)
}

func (handler AutoDLSettings) HandlePatchWorkflow(context *gin.Context) {
	profileID, ok := requiredPathParam(context, "profileId", "profileId")
	if !ok {
		return
	}
	payload, ok := decodeAutoDLJSON[servicesettings.AutoDLWorkflowStateMutation](context)
	if !ok {
		return
	}
	result, err := handler.settings.SetAutoDLWorkflowState(context.Request.Context(), profileID, payload)
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	handler.notify()
	httpresponse.OK(context, result)
}

func (handler AutoDLSettings) HandlePutDefaults(context *gin.Context) {
	payload, ok := decodeAutoDLJSON[autoDLWorkflowDefaultsRequest](context)
	if !ok {
		return
	}
	result, err := handler.settings.SetAutoDLWorkflowDefaults(context.Request.Context(), payload.Defaults)
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	handler.notify()
	httpresponse.OK(context, result)
}

func (handler AutoDLSettings) HandleValidateWorkflow(context *gin.Context) {
	profileID, profileOK := requiredPathParam(context, "profileId", "profileId")
	versionID, versionOK := requiredPathParam(context, "versionId", "versionId")
	instanceID, instanceOK := requiredPathParam(context, "instanceId", "instanceId")
	if !profileOK || !versionOK || !instanceOK {
		return
	}
	result, err := handler.admin.Validate(context.Request.Context(), servicegeneration.AutoDLWorkflowValidationRequest{
		InstanceProfileID: instanceID, WorkflowProfileID: profileID, VersionID: versionID,
	})
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	handler.notify()
	httpresponse.OK(context, result)
}

func (handler AutoDLSettings) notify() {
	if handler.notifier != nil {
		handler.notifier.NotifyInstancesChanged()
	}
}

func decodeAutoDLJSON[T any](context *gin.Context) (T, bool) {
	var payload T
	context.Request.Body = http.MaxBytesReader(context.Writer, context.Request.Body, autoDLSettingsBodyLimit)
	decoder := json.NewDecoder(context.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		httpresponse.ErrorFromStatus(context, http.StatusBadRequest, fmt.Errorf("invalid json body: %w", err))
		return payload, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		httpresponse.Error(context, http.StatusBadRequest, "invalid json body: multiple JSON values")
		return payload, false
	}
	return payload, true
}

func writeAutoDLSettingsError(context *gin.Context, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, servicesettings.ErrAutoDLInstanceNotFound),
		errors.Is(err, servicesettings.ErrAutoDLWorkflowNotFound),
		errors.Is(err, servicesettings.ErrAutoDLWorkflowVersionNotFound):
		status = http.StatusNotFound
	case errors.Is(err, servicesettings.ErrAutoDLWorkflowVersionConflict),
		errors.Is(err, servicesettings.ErrAutoDLWorkflowDefaultAmbiguous),
		errors.Is(err, servicesettings.ErrAutoDLWorkflowDefaultOverlap),
		errors.Is(err, servicesettings.ErrAutoDLWorkflowAlreadyExists):
		status = http.StatusConflict
	case errors.Is(err, servicegeneration.ErrAutoDLWorkflowValidationInvalid),
		errors.Is(err, comfyui.ErrWorkflowBindingsUnconfirmed),
		errors.Is(err, comfyui.ErrWorkflowBindingInvalid):
		status = http.StatusUnprocessableEntity
	case errors.Is(err, servicesettings.ErrAutoDLSettingsInvalid),
		errors.Is(err, comfyui.ErrInvalidUIWorkflow),
		errors.Is(err, comfyui.ErrWorkflowInputsInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, servicesettings.ErrAutoDLPasswordStoreUnavailable),
		errors.Is(err, servicesettings.ErrAutoDLWorkflowUnavailable),
		errors.Is(err, autodl.ErrTunnelManagerClosed),
		errors.Is(err, autodl.ErrHostKeyMismatch),
		errors.Is(err, autodl.ErrTunnelSuperseded),
		errors.Is(err, autodl.ErrTunnelStale),
		errors.Is(err, comfyui.ErrInvalidBaseURL),
		errors.Is(err, comfyui.ErrResponseTooLarge):
		status = http.StatusServiceUnavailable
	default:
		var credentialErr *servicesettings.AutoDLCredentialWriteError
		var comfyError *comfyui.HTTPStatusError
		if errors.As(err, &credentialErr) || errors.As(err, &comfyError) || strings.Contains(err.Error(), "AutoDL tunnel") || strings.Contains(err.Error(), "AutoDL SSH") {
			status = http.StatusServiceUnavailable
		}
	}
	httpresponse.ErrorFromStatus(context, status, err)
}
