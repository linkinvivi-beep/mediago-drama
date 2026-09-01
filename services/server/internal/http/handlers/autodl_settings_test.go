package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/autodl"
	servicegeneration "github.com/mediago-dev/mediago-drama/services/server/internal/service/generation"
	servicesettings "github.com/mediago-dev/mediago-drama/services/server/internal/service/settings"
)

func TestWriteAutoDLSettingsErrorExplainsMissingPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/v1/settings/autodl/instances/test/check", nil)

	writeAutoDLSettingsError(context, autodl.ErrPasswordMissing)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
	body := response.Body.String()
	if !strings.Contains(body, "未保存有效 SSH 密码") || strings.Contains(body, "internal error") {
		t.Fatalf("response = %s, want actionable public message", body)
	}
}

func TestWriteAutoDLSettingsErrorExplainsReadinessFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		text   string
	}{
		{name: "host key", err: autodl.ErrHostKeyMismatch, status: http.StatusUnprocessableEntity, text: "主机指纹不匹配"},
		{name: "startup missing", err: servicegeneration.ErrAutoDLStartupCommandMissing, status: http.StatusUnprocessableEntity, text: "未配置启动命令"},
		{name: "startup failed", err: autodl.ErrRemoteCommandFailed, status: http.StatusBadGateway, text: "远程启动命令执行失败"},
		{name: "health timeout", err: servicegeneration.ErrAutoDLHealthTimeout, status: http.StatusGatewayTimeout, text: "等待远程服务健康超时"},
		{name: "port conflict", err: syscall.EADDRINUSE, status: http.StatusConflict, text: "本地端口已被占用"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			response := httptest.NewRecorder()
			requestContext, _ := gin.CreateTestContext(response)
			requestContext.Request = httptest.NewRequest(http.MethodPost, "/api/v1/settings/autodl/instances/test/check", nil)
			writeAutoDLSettingsError(requestContext, test.err)
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.text) || strings.Contains(response.Body.String(), "internal error") {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAutoDLReadinessReturnsLatestRedactedStage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	requestContext, _ := gin.CreateTestContext(response)
	requestContext.Request = httptest.NewRequest(http.MethodGet, "/api/v1/settings/autodl/instances/test/readiness", nil)
	requestContext.Params = gin.Params{{Key: "instanceId", Value: "test"}}
	handler := AutoDLSettings{admin: autoDLAdminStub{readiness: servicegeneration.AutoDLInstanceCheck{Stage: servicegeneration.AutoDLReadinessStageWaitingHealth, Reason: "waiting"}}}
	handler.HandleGetInstanceReadiness(requestContext)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"stage":"waiting_health"`) || strings.Contains(response.Body.String(), "baseUrl") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

type autoDLAdminStub struct {
	readiness servicegeneration.AutoDLInstanceCheck
}

func (stub autoDLAdminStub) ScanFingerprint(context.Context, string) (servicegeneration.AutoDLFingerprintResult, error) {
	return servicegeneration.AutoDLFingerprintResult{}, errors.New("unused")
}
func (stub autoDLAdminStub) CheckInstance(context.Context, string) (servicegeneration.AutoDLInstanceCheck, error) {
	return servicegeneration.AutoDLInstanceCheck{}, errors.New("unused")
}
func (stub autoDLAdminStub) ReadinessStatus(string) servicegeneration.AutoDLInstanceCheck {
	return stub.readiness
}
func (stub autoDLAdminStub) Preview(context.Context, servicegeneration.AutoDLWorkflowPreviewRequest) (servicegeneration.AutoDLWorkflowPreview, error) {
	return servicegeneration.AutoDLWorkflowPreview{}, errors.New("unused")
}
func (stub autoDLAdminStub) Create(context.Context, servicegeneration.AutoDLWorkflowCreateRequest) (servicesettings.AutoDLWorkflowProfileResponse, error) {
	return servicesettings.AutoDLWorkflowProfileResponse{}, errors.New("unused")
}
func (stub autoDLAdminStub) Replace(context.Context, string, servicegeneration.AutoDLWorkflowReplaceRequest) (servicesettings.AutoDLWorkflowProfileResponse, error) {
	return servicesettings.AutoDLWorkflowProfileResponse{}, errors.New("unused")
}
func (stub autoDLAdminStub) Validate(context.Context, servicegeneration.AutoDLWorkflowValidationRequest) (servicesettings.AutoDLWorkflowValidation, error) {
	return servicesettings.AutoDLWorkflowValidation{}, errors.New("unused")
}
