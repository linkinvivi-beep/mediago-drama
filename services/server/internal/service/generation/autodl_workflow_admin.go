package generation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/autodl"
	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/comfyui"
	"github.com/mediago-dev/mediago-drama/services/server/internal/service/settings"
)

var ErrAutoDLWorkflowValidationInvalid = errors.New("AutoDL workflow is incompatible with the selected instance")

// AutoDLWorkflowStore is the narrow settings surface required by workflow
// administration. It intentionally contains no HTTP-facing password method.
type AutoDLWorkflowStore interface {
	GetAutoDLInstance(context.Context, string) (settings.AutoDLInstanceProfile, error)
	GetAutoDLWorkflowVersion(context.Context, string, string) (settings.ResolvedAutoDLWorkflow, error)
	CreateAutoDLWorkflow(context.Context, settings.AutoDLWorkflowCreateMutation) (settings.AutoDLWorkflowProfileResponse, error)
	ReplaceAutoDLWorkflow(context.Context, string, settings.AutoDLWorkflowVersionMutation) (settings.AutoDLWorkflowProfileResponse, error)
	SaveAutoDLWorkflowValidation(context.Context, string, settings.AutoDLWorkflowValidation) (settings.AutoDLInstanceResponse, error)
}

type AutoDLWorkflowValidationRequest struct {
	InstanceProfileID string
	WorkflowProfileID string
	VersionID         string
}

type AutoDLWorkflowPreviewRequest struct {
	InstanceProfileID string
	UIWorkflow        json.RawMessage
}

type AutoDLWorkflowPreview struct {
	Inspection       comfyui.WorkflowInspection `json:"inspection"`
	ObjectInfoDigest string                     `json:"objectInfoDigest"`
}

type AutoDLWorkflowCreateRequest struct {
	InstanceProfileID string
	ID                string
	Name              string
	Description       string
	MediaKind         string
	RouteID           string
	UIWorkflow        json.RawMessage
	Bindings          comfyui.WorkflowBindings
	References        settings.AutoDLReferenceContract
	PromptGuide       string
}

type AutoDLWorkflowReplaceRequest struct {
	InstanceProfileID        string
	ExpectedCurrentVersionID string
	UIWorkflow               json.RawMessage
	Bindings                 comfyui.WorkflowBindings
	References               settings.AutoDLReferenceContract
	PromptGuide              string
}

type AutoDLFingerprintResult struct {
	Fingerprint string `json:"fingerprint"`
}

// AutoDLInstanceCheck is deliberately redacted: the managed tunnel origin is
// never part of the response contract.
type AutoDLInstanceCheck struct {
	Connected      bool     `json:"connected"`
	LocalPort      int      `json:"localPort,omitempty"`
	ComfyUIVersion string   `json:"comfyuiVersion,omitempty"`
	Devices        []string `json:"devices,omitempty"`
	Reason         string   `json:"reason,omitempty"`
}

type AutoDLWorkflowAdmin struct {
	settings AutoDLWorkflowStore
	tunnels  autodl.TunnelManager
	scanner  autodl.HostKeyScanner
	client   func(string) (comfyui.Client, error)
	clock    func() time.Time
}

func NewAutoDLWorkflowAdmin(store AutoDLWorkflowStore, tunnels autodl.TunnelManager, scanner autodl.HostKeyScanner) *AutoDLWorkflowAdmin {
	return &AutoDLWorkflowAdmin{
		settings: store,
		tunnels:  tunnels,
		scanner:  scanner,
		client:   comfyui.NewClient,
		clock:    time.Now,
	}
}

// Readiness verifies one exact workflow version against the instance's live
// ComfyUI node schema. A previously ready validation fails closed when the
// host fingerprint, workflow digests, or /object_info digest has changed.
func (admin *AutoDLWorkflowAdmin) Readiness(
	ctx context.Context,
	instance settings.AutoDLInstanceProfile,
	workflowProfileID string,
	workflowVersionID string,
) (autodl.Tunnel, bool) {
	workflowProfileID = strings.TrimSpace(workflowProfileID)
	workflowVersionID = strings.TrimSpace(workflowVersionID)
	if admin == nil || admin.settings == nil || !instance.Enabled || workflowProfileID == "" || workflowVersionID == "" {
		return autodl.Tunnel{}, false
	}
	workflow, err := admin.settings.GetAutoDLWorkflowVersion(ctx, workflowProfileID, workflowVersionID)
	if err != nil || workflow.ProfileID != workflowProfileID || workflow.VersionID != workflowVersionID {
		return autodl.Tunnel{}, false
	}
	var ready *settings.AutoDLWorkflowValidation
	for index := range instance.WorkflowValidations {
		validation := &instance.WorkflowValidations[index]
		if validation.WorkflowProfileID == workflowProfileID && validation.VersionID == workflowVersionID {
			ready = validation
			break
		}
	}
	if ready == nil || ready.Status != settings.AutoDLWorkflowValidationReady ||
		ready.WorkflowDigest != workflow.WorkflowDigest || ready.APITemplateDigest != workflow.APITemplateDigest ||
		ready.ObjectInfoDigest == "" || (ready.InstanceFingerprint != "" && ready.InstanceFingerprint != instance.HostFingerprint) {
		return autodl.Tunnel{}, false
	}
	client, tunnel, err := admin.connectInstance(ctx, instance)
	if err != nil {
		return autodl.Tunnel{}, false
	}
	objectInfo, err := client.ObjectInfo(ctx)
	if err != nil {
		return autodl.Tunnel{}, false
	}
	objectInfoDigest, err := comfyui.DigestObjectInfo(objectInfo)
	if err != nil || objectInfoDigest != ready.ObjectInfoDigest {
		return autodl.Tunnel{}, false
	}
	return tunnel, true
}

func (admin *AutoDLWorkflowAdmin) ScanFingerprint(ctx context.Context, instanceID string) (AutoDLFingerprintResult, error) {
	instance, err := admin.instance(ctx, instanceID, false)
	if err != nil {
		return AutoDLFingerprintResult{}, err
	}
	if admin.scanner == nil {
		return AutoDLFingerprintResult{}, fmt.Errorf("AutoDL SSH host key scanner is unavailable")
	}
	fingerprint, err := admin.scanner.Scan(ctx, instance.Host, instance.SSHPort)
	if err != nil {
		return AutoDLFingerprintResult{}, err
	}
	return AutoDLFingerprintResult{Fingerprint: fingerprint}, nil
}

func (admin *AutoDLWorkflowAdmin) CheckInstance(ctx context.Context, instanceID string) (AutoDLInstanceCheck, error) {
	instance, client, tunnel, err := admin.connect(ctx, instanceID)
	_ = instance
	if err != nil {
		return AutoDLInstanceCheck{Reason: "connection_failed"}, err
	}
	stats, err := client.SystemStats(ctx)
	if err != nil {
		return AutoDLInstanceCheck{Reason: "comfyui_unavailable"}, err
	}
	localPort, err := tunnelLocalPort(tunnel)
	if err != nil {
		return AutoDLInstanceCheck{Reason: "tunnel_unavailable"}, err
	}
	result := AutoDLInstanceCheck{
		Connected: true, LocalPort: localPort, ComfyUIVersion: stats.System.ComfyUIVersion,
		Devices: make([]string, 0, len(stats.Devices)),
	}
	for _, device := range stats.Devices {
		if name := strings.TrimSpace(device.Name); name != "" {
			result.Devices = append(result.Devices, name)
		}
	}
	return result, nil
}

func (admin *AutoDLWorkflowAdmin) Preview(ctx context.Context, request AutoDLWorkflowPreviewRequest) (AutoDLWorkflowPreview, error) {
	_, client, _, err := admin.connect(ctx, request.InstanceProfileID)
	if err != nil {
		return AutoDLWorkflowPreview{}, err
	}
	objectInfo, err := client.ObjectInfo(ctx)
	if err != nil {
		return AutoDLWorkflowPreview{}, err
	}
	inspection, err := comfyui.InspectUIWorkflow(request.UIWorkflow, objectInfo)
	if err != nil {
		return AutoDLWorkflowPreview{}, err
	}
	digest, err := comfyui.DigestObjectInfo(objectInfo)
	if err != nil {
		return AutoDLWorkflowPreview{}, err
	}
	return AutoDLWorkflowPreview{Inspection: inspection, ObjectInfoDigest: digest}, nil
}

func (admin *AutoDLWorkflowAdmin) Create(ctx context.Context, request AutoDLWorkflowCreateRequest) (settings.AutoDLWorkflowProfileResponse, error) {
	compiled, err := admin.compileOnInstance(ctx, request.InstanceProfileID, request.UIWorkflow, request.Bindings)
	if err != nil {
		return settings.AutoDLWorkflowProfileResponse{}, err
	}
	return admin.settings.CreateAutoDLWorkflow(ctx, settings.AutoDLWorkflowCreateMutation{
		ID: request.ID, Name: request.Name, Description: request.Description, MediaKind: request.MediaKind, RouteID: request.RouteID,
		Compiled: compiled, References: request.References, PromptGuide: request.PromptGuide,
	})
}

func (admin *AutoDLWorkflowAdmin) Replace(ctx context.Context, profileID string, request AutoDLWorkflowReplaceRequest) (settings.AutoDLWorkflowProfileResponse, error) {
	compiled, err := admin.compileOnInstance(ctx, request.InstanceProfileID, request.UIWorkflow, request.Bindings)
	if err != nil {
		return settings.AutoDLWorkflowProfileResponse{}, err
	}
	return admin.settings.ReplaceAutoDLWorkflow(ctx, profileID, settings.AutoDLWorkflowVersionMutation{
		ExpectedCurrentVersionID: request.ExpectedCurrentVersionID,
		Compiled:                 compiled, References: request.References, PromptGuide: request.PromptGuide,
	})
}

func (admin *AutoDLWorkflowAdmin) Validate(ctx context.Context, request AutoDLWorkflowValidationRequest) (settings.AutoDLWorkflowValidation, error) {
	instance, err := admin.instance(ctx, request.InstanceProfileID, true)
	if err != nil {
		return settings.AutoDLWorkflowValidation{}, err
	}
	workflow, err := admin.settings.GetAutoDLWorkflowVersion(ctx, strings.TrimSpace(request.WorkflowProfileID), strings.TrimSpace(request.VersionID))
	if err != nil {
		return settings.AutoDLWorkflowValidation{}, err
	}
	if strings.TrimSpace(request.VersionID) == "" || workflow.ProfileID != strings.TrimSpace(request.WorkflowProfileID) || workflow.VersionID != strings.TrimSpace(request.VersionID) {
		return settings.AutoDLWorkflowValidation{}, settings.ErrAutoDLWorkflowVersionNotFound
	}
	client, _, err := admin.connectInstance(ctx, instance)
	if err != nil {
		return settings.AutoDLWorkflowValidation{}, err
	}
	objectInfo, err := client.ObjectInfo(ctx)
	if err != nil {
		return settings.AutoDLWorkflowValidation{}, err
	}
	objectInfoDigest, err := comfyui.DigestObjectInfo(objectInfo)
	if err != nil {
		return settings.AutoDLWorkflowValidation{}, err
	}
	validation := settings.AutoDLWorkflowValidation{
		WorkflowProfileID: workflow.ProfileID, VersionID: workflow.VersionID,
		WorkflowDigest: workflow.WorkflowDigest, APITemplateDigest: workflow.APITemplateDigest,
		ObjectInfoDigest: objectInfoDigest, InstanceFingerprint: instance.HostFingerprint,
		ValidatedAt: admin.now().UTC().Format(time.RFC3339Nano),
	}
	compiled, compileErr := comfyui.CompileUIWorkflow(workflow.UIWorkflow, objectInfo, workflow.Bindings)
	if compileErr != nil || compiled.WorkflowDigest != workflow.WorkflowDigest || compiled.APITemplateDigest != workflow.APITemplateDigest {
		validation.Status = settings.AutoDLWorkflowValidationInvalid
		validation.Reason = "workflow_incompatible"
		if _, saveErr := admin.settings.SaveAutoDLWorkflowValidation(ctx, instance.ID, validation); saveErr != nil {
			return validation, saveErr
		}
		return validation, ErrAutoDLWorkflowValidationInvalid
	}
	validation.Status = settings.AutoDLWorkflowValidationReady
	validation.Reason = "validated_read_only"
	if _, err := admin.settings.SaveAutoDLWorkflowValidation(ctx, instance.ID, validation); err != nil {
		return validation, err
	}
	return validation, nil
}

func (admin *AutoDLWorkflowAdmin) compileOnInstance(ctx context.Context, instanceID string, uiWorkflow json.RawMessage, bindings comfyui.WorkflowBindings) (comfyui.CompiledWorkflow, error) {
	_, client, _, err := admin.connect(ctx, instanceID)
	if err != nil {
		return comfyui.CompiledWorkflow{}, err
	}
	objectInfo, err := client.ObjectInfo(ctx)
	if err != nil {
		return comfyui.CompiledWorkflow{}, err
	}
	return comfyui.CompileUIWorkflow(uiWorkflow, objectInfo, bindings)
}

func (admin *AutoDLWorkflowAdmin) connect(ctx context.Context, instanceID string) (settings.AutoDLInstanceProfile, comfyui.Client, autodl.Tunnel, error) {
	instance, err := admin.instance(ctx, instanceID, true)
	if err != nil {
		return settings.AutoDLInstanceProfile{}, nil, autodl.Tunnel{}, err
	}
	client, tunnel, err := admin.connectInstance(ctx, instance)
	return instance, client, tunnel, err
}

func (admin *AutoDLWorkflowAdmin) connectInstance(ctx context.Context, instance settings.AutoDLInstanceProfile) (comfyui.Client, autodl.Tunnel, error) {
	if admin.tunnels == nil {
		return nil, autodl.Tunnel{}, fmt.Errorf("AutoDL tunnel manager is unavailable")
	}
	tunnel, err := admin.tunnels.Ensure(ctx, autodl.TunnelTarget{
		InstanceProfileID: instance.ID, Host: instance.Host, SSHPort: instance.SSHPort, SSHUser: instance.SSHUser,
		ComfyPort: instance.ComfyPort, HostFingerprint: instance.HostFingerprint, CredentialRef: instance.CredentialRef,
	})
	if err != nil {
		return nil, autodl.Tunnel{}, err
	}
	if tunnel.InstanceProfileID != instance.ID {
		return nil, autodl.Tunnel{}, fmt.Errorf("AutoDL managed tunnel instance mismatch")
	}
	clientFactory := admin.client
	if clientFactory == nil {
		clientFactory = comfyui.NewClient
	}
	client, err := clientFactory(tunnel.BaseURL)
	if err != nil {
		return nil, autodl.Tunnel{}, err
	}
	return client, tunnel, nil
}

func (admin *AutoDLWorkflowAdmin) instance(ctx context.Context, instanceID string, requireReady bool) (settings.AutoDLInstanceProfile, error) {
	if admin == nil || admin.settings == nil {
		return settings.AutoDLInstanceProfile{}, fmt.Errorf("AutoDL workflow settings are unavailable")
	}
	instance, err := admin.settings.GetAutoDLInstance(ctx, strings.TrimSpace(instanceID))
	if err != nil {
		return settings.AutoDLInstanceProfile{}, err
	}
	if requireReady && (!instance.Enabled || strings.TrimSpace(instance.HostFingerprint) == "" || strings.TrimSpace(instance.CredentialRef) == "") {
		return settings.AutoDLInstanceProfile{}, settings.ErrAutoDLWorkflowUnavailable
	}
	return instance, nil
}

func (admin *AutoDLWorkflowAdmin) now() time.Time {
	if admin.clock == nil {
		return time.Now()
	}
	return admin.clock()
}

func tunnelLocalPort(tunnel autodl.Tunnel) (int, error) {
	parsed, err := url.Parse(tunnel.BaseURL)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.Path != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return 0, fmt.Errorf("invalid AutoDL managed tunnel")
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil || (host != "127.0.0.1" && host != "::1" && !strings.EqualFold(host, "localhost")) {
		return 0, fmt.Errorf("invalid AutoDL managed tunnel")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid AutoDL managed tunnel")
	}
	return port, nil
}
