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
	MediaKind         string                                  `json:"mediaKind"`
	RouteID           string                                  `json:"routeId"`
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

// HandleGet godoc
// @Summary 获取 AutoDL 配置
// @Description 返回脱敏实例、工作流版本和默认工作流配置。
// @Tags Settings
// @Produce json
// @Success 200 {object} SwaggerEnvelope
// @Failure 500 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl [get]
func (handler AutoDLSettings) HandleGet(context *gin.Context) {
	result, err := handler.settings.GetAutoDLSettings(context.Request.Context())
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	httpresponse.OK(context, result)
}

// HandlePostInstance godoc
// @Summary 新增 AutoDL 实例
// @Description 保存 SSH 登录参数；密码仅写入系统凭据存储。
// @Tags Settings
// @Accept json
// @Produce json
// @Success 200 {object} SwaggerEnvelope
// @Failure 400 {object} SwaggerEnvelope
// @Failure 503 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl/instances [post]
func (handler AutoDLSettings) HandlePostInstance(context *gin.Context) {
	payload, ok := decodeAutoDLJSON[autoDLInstanceRequest](context)
	if !ok {
		return
	}
	handler.saveInstance(context, "", payload)
}

// HandlePutInstance godoc
// @Summary 更新 AutoDL 实例
// @Description 更新指定实例的 SSH、ComfyUI 端口和启用状态。
// @Tags Settings
// @Accept json
// @Produce json
// @Param instanceId path string true "Instance ID"
// @Success 200 {object} SwaggerEnvelope
// @Failure 400 {object} SwaggerEnvelope
// @Failure 404 {object} SwaggerEnvelope
// @Failure 503 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl/instances/{instanceId} [put]
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

// HandlePutPassword godoc
// @Summary 保存 AutoDL 实例密码
// @Description 将指定实例密码写入 macOS Keychain，不回传明文。
// @Tags Settings
// @Accept json
// @Produce json
// @Param instanceId path string true "Instance ID"
// @Success 200 {object} SwaggerEnvelope
// @Failure 400 {object} SwaggerEnvelope
// @Failure 404 {object} SwaggerEnvelope
// @Failure 503 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl/instances/{instanceId}/password [put]
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

// HandleDeletePassword godoc
// @Summary 清除 AutoDL 实例密码
// @Description 从 macOS Keychain 删除指定实例密码。
// @Tags Settings
// @Produce json
// @Param instanceId path string true "Instance ID"
// @Success 200 {object} SwaggerEnvelope
// @Failure 404 {object} SwaggerEnvelope
// @Failure 503 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl/instances/{instanceId}/password [delete]
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

// HandleDeleteInstance godoc
// @Summary 删除 AutoDL 实例
// @Description 删除指定实例配置及其凭据引用。
// @Tags Settings
// @Produce json
// @Param instanceId path string true "Instance ID"
// @Success 200 {object} SwaggerEnvelope
// @Failure 404 {object} SwaggerEnvelope
// @Failure 503 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl/instances/{instanceId} [delete]
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

// HandleScanFingerprint godoc
// @Summary 扫描 AutoDL 主机指纹
// @Description 通过 SSH 获取实例当前主机指纹，供用户核对后保存。
// @Tags Settings
// @Produce json
// @Param instanceId path string true "Instance ID"
// @Success 200 {object} SwaggerEnvelope
// @Failure 404 {object} SwaggerEnvelope
// @Failure 503 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl/instances/{instanceId}/scan-fingerprint [post]
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

// HandleCheckInstance godoc
// @Summary 检查 AutoDL 实例
// @Description 建立临时 SSH 隧道并检查 ComfyUI 可用性。
// @Tags Settings
// @Produce json
// @Param instanceId path string true "Instance ID"
// @Success 200 {object} SwaggerEnvelope
// @Failure 404 {object} SwaggerEnvelope
// @Failure 503 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl/instances/{instanceId}/check [post]
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

// HandlePreviewWorkflow godoc
// @Summary 预览 AutoDL 工作流语义
// @Description 解析 ComfyUI UI 工作流并返回可绑定节点，不提交生成任务。
// @Tags Settings
// @Accept json
// @Produce json
// @Success 200 {object} SwaggerEnvelope
// @Failure 400 {object} SwaggerEnvelope
// @Failure 503 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl/workflows/preview [post]
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

// HandleCreateWorkflow godoc
// @Summary 导入 AutoDL 工作流
// @Description 创建工作流配置及首个不可变版本。
// @Tags Settings
// @Accept json
// @Produce json
// @Success 200 {object} SwaggerEnvelope
// @Failure 400 {object} SwaggerEnvelope
// @Failure 409 {object} SwaggerEnvelope
// @Failure 422 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl/workflows [post]
func (handler AutoDLSettings) HandleCreateWorkflow(context *gin.Context) {
	payload, ok := decodeAutoDLJSON[autoDLWorkflowCreateRequest](context)
	if !ok {
		return
	}
	result, err := handler.admin.Create(context.Request.Context(), servicegeneration.AutoDLWorkflowCreateRequest{
		InstanceProfileID: payload.InstanceProfileID, ID: payload.ID, Name: payload.Name, Description: payload.Description, MediaKind: payload.MediaKind, RouteID: payload.RouteID,
		UIWorkflow: payload.UIWorkflow, Bindings: payload.Bindings, References: payload.References, PromptGuide: payload.PromptGuide,
	})
	if err != nil {
		writeAutoDLSettingsError(context, err)
		return
	}
	handler.notify()
	httpresponse.OK(context, result)
}

// HandleReplaceWorkflow godoc
// @Summary 新增 AutoDL 工作流版本
// @Description 以乐观锁方式为现有工作流新增不可变版本。
// @Tags Settings
// @Accept json
// @Produce json
// @Param profileId path string true "Workflow profile ID"
// @Success 200 {object} SwaggerEnvelope
// @Failure 404 {object} SwaggerEnvelope
// @Failure 409 {object} SwaggerEnvelope
// @Failure 422 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl/workflows/{profileId}/versions [post]
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

// HandleDuplicateWorkflow godoc
// @Summary 复制 AutoDL 工作流
// @Description 将现有工作流复制为新的独立配置。
// @Tags Settings
// @Accept json
// @Produce json
// @Param profileId path string true "Workflow profile ID"
// @Success 200 {object} SwaggerEnvelope
// @Failure 404 {object} SwaggerEnvelope
// @Failure 409 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl/workflows/{profileId}/duplicate [post]
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

// HandlePatchWorkflow godoc
// @Summary 更新 AutoDL 工作流状态
// @Description 更新工作流名称、说明或启用状态，不修改已有版本。
// @Tags Settings
// @Accept json
// @Produce json
// @Param profileId path string true "Workflow profile ID"
// @Success 200 {object} SwaggerEnvelope
// @Failure 404 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl/workflows/{profileId} [patch]
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

// HandlePutDefaults godoc
// @Summary 设置 AutoDL 默认工作流
// @Description 按媒体类型和参考图数量保存默认工作流映射。
// @Tags Settings
// @Accept json
// @Produce json
// @Success 200 {object} SwaggerEnvelope
// @Failure 400 {object} SwaggerEnvelope
// @Failure 409 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl/defaults [put]
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

// HandleValidateWorkflow godoc
// @Summary 验证 AutoDL 工作流版本
// @Description 在指定实例上验证节点、模型和绑定，不提交生成任务。
// @Tags Settings
// @Produce json
// @Param profileId path string true "Workflow profile ID"
// @Param versionId path string true "Workflow version ID"
// @Param instanceId path string true "Instance ID"
// @Success 200 {object} SwaggerEnvelope
// @Failure 404 {object} SwaggerEnvelope
// @Failure 422 {object} SwaggerEnvelope
// @Failure 503 {object} SwaggerEnvelope
// @Router /api/v1/settings/autodl/workflows/{profileId}/versions/{versionId}/validate/{instanceId} [post]
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
	if errors.Is(err, autodl.ErrPasswordMissing) {
		httpresponse.Fail(context, http.StatusUnprocessableEntity, "未保存有效 SSH 密码，请编辑实例后重新输入", err)
		return
	}
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
