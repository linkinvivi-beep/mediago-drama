package comfyui

// WorkflowTarget identifies one writable input on one ComfyUI node.
type WorkflowTarget struct {
	NodeID    string `json:"nodeId"`
	InputName string `json:"inputName"`
}

// ReferenceBinding maps one ordered MediaLink reference slot to a workflow input.
type ReferenceBinding struct {
	Index  int            `json:"index"`
	Target WorkflowTarget `json:"target"`
}

// OutputBinding identifies the node whose generated file is collected.
type OutputBinding struct {
	NodeID      string `json:"nodeId"`
	OutputIndex int    `json:"outputIndex,omitempty"`
}

// ParameterBinding maps a declared scalar control to a workflow input.
type ParameterBinding struct {
	Name   string         `json:"name"`
	Target WorkflowTarget `json:"target"`
}

// WorkflowBindings is the confirmed semantic contract between MediaLink and
// an imported workflow. Task-specific values are written only to these targets.
type WorkflowBindings struct {
	Confirmed  bool               `json:"confirmed"`
	Prompts    []WorkflowTarget   `json:"prompts"`
	Negative   *WorkflowTarget    `json:"negative,omitempty"`
	References []ReferenceBinding `json:"references,omitempty"`
	Outputs    []OutputBinding    `json:"outputs"`
	Parameters []ParameterBinding `json:"parameters,omitempty"`
}
