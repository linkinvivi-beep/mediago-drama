package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/autodl"
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
