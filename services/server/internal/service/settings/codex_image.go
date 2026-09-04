package settings

import (
	"context"
	"strings"

	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/codexapp"
)

const (
	CodexImageReasonNotLoggedIn           = "not_logged_in"
	CodexImageReasonCLIUnavailable        = "cli_unavailable"
	CodexImageReasonCapabilityUnavailable = "capability_unavailable"
	CodexImageReasonCapabilityDisabled    = "capability_disabled"
	CodexImageReasonReady                 = "ready"
)

// CodexImagePreflight is the non-secret readiness state for built-in Codex image generation.
type CodexImagePreflight struct {
	AccountStatus   string `json:"accountStatus"`
	ImageGeneration bool   `json:"imageGeneration"`
	Ready           bool   `json:"ready"`
	Reason          string `json:"reason,omitempty"`
}

// GetCodexImagePreflight checks the shared ChatGPT login and image capability without starting a turn.
func (service *Settings) GetCodexImagePreflight(ctx context.Context) (CodexImagePreflight, error) {
	unavailable := CodexImagePreflight{
		AccountStatus: "unavailable",
		Reason:        CodexImageReasonCLIUnavailable,
	}
	if service == nil || service.codexAccount == nil || strings.TrimSpace(service.codexAccount.binPath) == "" {
		return unavailable, nil
	}

	requestCtx, cancel := context.WithTimeout(ctx, codexAccountRequestTimeout)
	defer cancel()
	session, err := service.codexAccount.newSession(requestCtx, service.codexAccount.binPath)
	if err != nil {
		return unavailable, nil
	}
	defer session.Close()

	var accountResponse struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	if err := session.Call(requestCtx, "account/read", map[string]any{"refreshToken": false}, &accountResponse); err != nil {
		return unavailable, nil
	}
	if accountResponse.Account == nil || accountResponse.Account.Type != "chatgpt" {
		return CodexImagePreflight{
			AccountStatus: "notLoggedIn",
			Reason:        CodexImageReasonNotLoggedIn,
		}, nil
	}

	preflight := CodexImagePreflight{AccountStatus: "loggedIn"}
	capabilities, err := codexapp.ReadModelProviderCapabilities(requestCtx, session)
	if err != nil {
		preflight.Reason = CodexImageReasonCapabilityUnavailable
		return preflight, nil
	}
	preflight.ImageGeneration = capabilities.ImageGeneration
	if !capabilities.ImageGeneration {
		preflight.Reason = CodexImageReasonCapabilityDisabled
		return preflight, nil
	}
	preflight.Ready = true
	preflight.Reason = CodexImageReasonReady
	return preflight, nil
}
