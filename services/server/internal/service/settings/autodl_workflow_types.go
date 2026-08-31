package settings

import (
	"encoding/json"
	"errors"

	"github.com/mediago-dev/mediago-drama/services/server/internal/platform/comfyui"
)

var (
	ErrAutoDLWorkflowAlreadyExists    = errors.New("AutoDL workflow profile already exists")
	ErrAutoDLWorkflowVersionNotFound  = errors.New("AutoDL workflow version was not found")
	ErrAutoDLWorkflowVersionConflict  = errors.New("AutoDL workflow current version changed")
	ErrAutoDLWorkflowUnavailable      = errors.New("AutoDL workflow is unavailable for a new task")
	ErrAutoDLWorkflowDefaultAmbiguous = errors.New("AutoDL workflow default is ambiguous")
	ErrAutoDLWorkflowDefaultOverlap   = errors.New("AutoDL workflow default ranges overlap")
)

const (
	AutoDLWorkflowMediaImage = "image"
	AutoDLWorkflowMediaVideo = "video"

	AutoDLWorkflowRouteImage = "autodl.image"
	AutoDLWorkflowRouteH3    = "autodl.minimax-h3"

	AutoDLBindingStatusUnconfirmed = "unconfirmed"
	AutoDLBindingStatusConfirmed   = "confirmed"

	AutoDLWorkflowValidationReady   = "ready"
	AutoDLWorkflowValidationInvalid = "invalid"
	AutoDLWorkflowValidationUnknown = "unknown"
	AutoDLWorkflowValidationStale   = "stale"
)

// Deprecated status aliases keep instance callers source-compatible while
// the workflow registry moves from three states to the four-state contract.
const (
	AutoDLWorkflowStatusReady             = AutoDLWorkflowValidationReady
	AutoDLWorkflowStatusNeedsRevalidation = AutoDLWorkflowValidationStale
	AutoDLWorkflowStatusInvalid           = AutoDLWorkflowValidationInvalid
)

type AutoDLWorkflowBindings = comfyui.WorkflowBindings

type AutoDLReferenceContract struct {
	Min   int                        `json:"min"`
	Max   int                        `json:"max"`
	Slots []comfyui.ReferenceBinding `json:"slots,omitempty"`
}

// AutoDLWorkflowValidation records compatibility with one exact immutable
// workflow version on one exact AutoDL instance.
type AutoDLWorkflowValidation struct {
	WorkflowProfileID   string `json:"workflowProfileId"`
	VersionID           string `json:"versionId"`
	Status              string `json:"status"`
	WorkflowDigest      string `json:"workflowDigest"`
	APITemplateDigest   string `json:"apiTemplateDigest"`
	ObjectInfoDigest    string `json:"objectInfoDigest,omitempty"`
	InstanceFingerprint string `json:"instanceFingerprint,omitempty"`
	ValidatedAt         string `json:"validatedAt,omitempty"`
	Reason              string `json:"reason,omitempty"`
}

type AutoDLWorkflowVersion struct {
	VersionID         string                   `json:"versionId"`
	Sequence          int                      `json:"sequence"`
	SourceVersionID   string                   `json:"sourceVersionId,omitempty"`
	CreatedAt         string                   `json:"createdAt"`
	UIWorkflow        json.RawMessage          `json:"uiWorkflow"`
	APITemplate       json.RawMessage          `json:"apiTemplate"`
	WorkflowDigest    string                   `json:"workflowDigest"`
	APITemplateDigest string                   `json:"apiTemplateDigest"`
	BindingStatus     string                   `json:"bindingStatus"`
	Bindings          comfyui.WorkflowBindings `json:"bindings"`
	References        AutoDLReferenceContract  `json:"references"`
	PromptGuide       string                   `json:"promptGuide,omitempty"`
}

type AutoDLWorkflowProfile struct {
	ID               string                  `json:"id"`
	Name             string                  `json:"name"`
	Description      string                  `json:"description,omitempty"`
	MediaKind        string                  `json:"mediaKind"`
	RouteID          string                  `json:"routeId"`
	Enabled          bool                    `json:"enabled"`
	AutoSelectable   bool                    `json:"autoSelectable"`
	Archived         bool                    `json:"archived"`
	CurrentVersionID string                  `json:"currentVersionId"`
	Versions         []AutoDLWorkflowVersion `json:"versions"`
}

type AutoDLWorkflowVersionResponse struct {
	VersionID         string                   `json:"versionId"`
	Sequence          int                      `json:"sequence"`
	SourceVersionID   string                   `json:"sourceVersionId,omitempty"`
	CreatedAt         string                   `json:"createdAt"`
	WorkflowDigest    string                   `json:"workflowDigest"`
	APITemplateDigest string                   `json:"apiTemplateDigest"`
	BindingStatus     string                   `json:"bindingStatus"`
	Bindings          comfyui.WorkflowBindings `json:"bindings"`
	References        AutoDLReferenceContract  `json:"references"`
	PromptGuide       string                   `json:"promptGuide,omitempty"`
}

type AutoDLWorkflowProfileResponse struct {
	ID               string                          `json:"id"`
	Name             string                          `json:"name"`
	Description      string                          `json:"description,omitempty"`
	MediaKind        string                          `json:"mediaKind"`
	RouteID          string                          `json:"routeId"`
	Enabled          bool                            `json:"enabled"`
	AutoSelectable   bool                            `json:"autoSelectable"`
	Archived         bool                            `json:"archived"`
	CurrentVersionID string                          `json:"currentVersionId"`
	Versions         []AutoDLWorkflowVersionResponse `json:"versions"`
	// Transitional in-process fields keep existing service callers compiling;
	// they are deliberately excluded from the administration JSON contract.
	Status            string `json:"-"`
	Ready             bool   `json:"-"`
	WorkflowDigest    string `json:"-"`
	APITemplateDigest string `json:"-"`
}

type AutoDLWorkflowDefault struct {
	ID                string `json:"id"`
	RouteID           string `json:"routeId,omitempty"`
	MinReferences     int    `json:"minReferences"`
	MaxReferences     int    `json:"maxReferences"`
	WorkflowProfileID string `json:"workflowProfileId"`
}

type AutoDLWorkflowResolveRequest struct {
	WorkflowProfileID string
	VersionID         string
	RouteID           string
	ReferenceCount    int
	ForNewTask        bool
}

type AutoDLWorkflowCreateMutation struct {
	ID          string
	Name        string
	Description string
	MediaKind   string
	RouteID     string
	Compiled    comfyui.CompiledWorkflow
	References  AutoDLReferenceContract
	PromptGuide string
}

type AutoDLWorkflowVersionMutation struct {
	ExpectedCurrentVersionID string
	Compiled                 comfyui.CompiledWorkflow
	References               AutoDLReferenceContract
	PromptGuide              string
}

type AutoDLWorkflowDuplicateMutation struct {
	ID          string
	Name        string
	Description string
}

type AutoDLWorkflowStateMutation struct {
	Enabled        *bool `json:"enabled,omitempty"`
	AutoSelectable *bool `json:"autoSelectable,omitempty"`
	Archived       *bool `json:"archived,omitempty"`
}

type ResolvedAutoDLWorkflow struct {
	ProfileID         string
	VersionID         string
	Name              string
	MediaKind         string
	RouteID           string
	WorkflowDigest    string
	APITemplateDigest string
	UIWorkflow        json.RawMessage
	APITemplate       json.RawMessage
	Bindings          AutoDLWorkflowBindings
	References        AutoDLReferenceContract
	PromptGuide       string
	AutoSelectable    bool
}

// AutoDLWorkflowProfileMutation is retained as a narrow compatibility input
// until the compiler-backed registry operations replace it in Task 3.
type AutoDLWorkflowProfileMutation struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Kind              string          `json:"kind,omitempty"`
	Version           string          `json:"version,omitempty"`
	Status            string          `json:"status,omitempty"`
	Workflow          json.RawMessage `json:"workflow,omitempty"`
	APITemplate       json.RawMessage `json:"apiTemplate,omitempty"`
	Manifest          json.RawMessage `json:"manifest,omitempty"`
	RequiredNodes     []string        `json:"requiredNodes,omitempty"`
	RequiredModels    []string        `json:"requiredModels,omitempty"`
	WorkflowDigest    string          `json:"workflowDigest,omitempty"`
	APITemplateDigest string          `json:"apiTemplateDigest,omitempty"`
}
