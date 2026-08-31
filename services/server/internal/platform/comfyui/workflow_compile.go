package comfyui

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

const (
	maxUIWorkflowBytes  = 8 * 1024 * 1024
	maxUIWorkflowDepth  = 64
	maxUIWorkflowNodes  = 2048
	maxUIWorkflowLinks  = 8192
	maxUIWorkflowString = 64 * 1024
)

var (
	ErrInvalidUIWorkflow           = errors.New("invalid or unsafe ComfyUI UI workflow")
	ErrWorkflowBindingsUnconfirmed = errors.New("ComfyUI workflow bindings are not confirmed")
	ErrWorkflowBindingInvalid      = errors.New("ComfyUI workflow binding is invalid")
	ErrWorkflowInputsInvalid       = errors.New("ComfyUI workflow inputs are invalid")
)

type WorkflowSuggestions struct {
	Prompts    []WorkflowTarget   `json:"prompts"`
	References []ReferenceBinding `json:"references,omitempty"`
	Outputs    []OutputBinding    `json:"outputs"`
	Parameters []ParameterBinding `json:"parameters,omitempty"`
}

type WorkflowInspection struct {
	Confirmed     bool                `json:"confirmed"`
	NodeCount     int                 `json:"nodeCount"`
	LinkCount     int                 `json:"linkCount"`
	RequiredNodes []string            `json:"requiredNodes"`
	Suggestions   WorkflowSuggestions `json:"suggestions"`
}

type CompiledWorkflow struct {
	APITemplate       json.RawMessage  `json:"apiTemplate"`
	Bindings          WorkflowBindings `json:"bindings"`
	WorkflowDigest    string           `json:"workflowDigest"`
	APITemplateDigest string           `json:"apiTemplateDigest"`
	RequiredNodes     []string         `json:"requiredNodes"`
	RequiredModels    []string         `json:"requiredModels"`
}

type UploadedReference struct {
	Name      string `json:"name"`
	Subfolder string `json:"subfolder,omitempty"`
	Type      string `json:"type,omitempty"`
}

type WorkflowInputs struct {
	Prompts        []string
	NegativePrompt *string
	Seed           *int64
	Width          *int
	Height         *int
	References     []UploadedReference
	Parameters     map[string]any
}

type uiWorkflow struct {
	Nodes []uiWorkflowNode  `json:"nodes"`
	Links []json.RawMessage `json:"links"`
}

type uiWorkflowNode struct {
	ID            json.RawMessage   `json:"id"`
	Type          string            `json:"type"`
	Inputs        []uiWorkflowInput `json:"inputs"`
	WidgetsValues []json.RawMessage `json:"widgets_values"`
}

type uiWorkflowInput struct {
	Name string          `json:"name"`
	Link json.RawMessage `json:"link"`
}

type parsedUIWorkflow struct {
	RawCanonical json.RawMessage
	Nodes        map[string]uiWorkflowNode
	NodeOrder    []string
	Links        []parsedWorkflowLink
}

type parsedWorkflowLink struct {
	ID         string
	OriginID   string
	OriginSlot int
	TargetID   string
	TargetSlot int
}

type apiWorkflowNode struct {
	ClassType string         `json:"class_type"`
	Inputs    map[string]any `json:"inputs"`
}

func InspectUIWorkflow(raw json.RawMessage, objectInfo ObjectInfo) (WorkflowInspection, error) {
	parsed, err := parseUIWorkflow(raw)
	if err != nil {
		return WorkflowInspection{}, err
	}
	template, requiredNodes, _, err := compileAPITemplate(parsed, objectInfo)
	if err != nil {
		return WorkflowInspection{}, err
	}
	return WorkflowInspection{
		Confirmed: false, NodeCount: len(parsed.Nodes), LinkCount: len(parsed.Links), RequiredNodes: requiredNodes,
		Suggestions: suggestWorkflowBindings(parsed, objectInfo, template),
	}, nil
}

func CompileUIWorkflow(raw json.RawMessage, objectInfo ObjectInfo, bindings WorkflowBindings) (CompiledWorkflow, error) {
	if !bindings.Confirmed {
		return CompiledWorkflow{}, ErrWorkflowBindingsUnconfirmed
	}
	parsed, err := parseUIWorkflow(raw)
	if err != nil {
		return CompiledWorkflow{}, err
	}
	template, requiredNodes, requiredModels, err := compileAPITemplate(parsed, objectInfo)
	if err != nil {
		return CompiledWorkflow{}, err
	}
	if err := validateWorkflowBindings(bindings, parsed, objectInfo, template); err != nil {
		return CompiledWorkflow{}, err
	}
	apiRaw, err := json.Marshal(template)
	if err != nil {
		return CompiledWorkflow{}, fmt.Errorf("%w: encode API template", ErrInvalidUIWorkflow)
	}
	return CompiledWorkflow{
		APITemplate: apiRaw, Bindings: bindings,
		WorkflowDigest: digestJSON(parsed.RawCanonical), APITemplateDigest: digestJSON(apiRaw),
		RequiredNodes: requiredNodes, RequiredModels: requiredModels,
	}, nil
}

func InstantiateWorkflow(template json.RawMessage, bindings WorkflowBindings, inputs WorkflowInputs) (json.RawMessage, error) {
	if !bindings.Confirmed {
		return nil, ErrWorkflowBindingsUnconfirmed
	}
	var nodes map[string]apiWorkflowNode
	if err := json.Unmarshal(template, &nodes); err != nil {
		return nil, fmt.Errorf("%w: API template", ErrWorkflowInputsInvalid)
	}
	if len(inputs.Prompts) == 0 || (len(inputs.Prompts) != 1 && len(inputs.Prompts) != len(bindings.Prompts)) {
		return nil, fmt.Errorf("%w: prompt count", ErrWorkflowInputsInvalid)
	}
	for index, target := range bindings.Prompts {
		value := inputs.Prompts[0]
		if len(inputs.Prompts) > 1 {
			value = inputs.Prompts[index]
		}
		if err := writeWorkflowInput(nodes, target, value); err != nil {
			return nil, err
		}
	}
	if bindings.Negative != nil && inputs.NegativePrompt != nil {
		if err := writeWorkflowInput(nodes, *bindings.Negative, *inputs.NegativePrompt); err != nil {
			return nil, err
		}
	}
	if len(inputs.References) > len(bindings.References) {
		return nil, fmt.Errorf("%w: reference count", ErrWorkflowInputsInvalid)
	}
	for _, binding := range bindings.References {
		if binding.Index < 0 || binding.Index >= len(inputs.References) {
			continue
		}
		reference := inputs.References[binding.Index]
		filename := strings.TrimSpace(reference.Name)
		if strings.TrimSpace(reference.Subfolder) != "" {
			filename = path.Join(reference.Subfolder, filename)
		}
		if filename == "" {
			return nil, fmt.Errorf("%w: reference filename", ErrWorkflowInputsInvalid)
		}
		if err := writeWorkflowInput(nodes, binding.Target, filename); err != nil {
			return nil, err
		}
	}
	for _, binding := range bindings.Parameters {
		var value any
		var present bool
		switch strings.ToLower(binding.Name) {
		case "seed":
			if inputs.Seed != nil {
				value, present = *inputs.Seed, true
			}
		case "width":
			if inputs.Width != nil {
				value, present = *inputs.Width, true
			}
		case "height":
			if inputs.Height != nil {
				value, present = *inputs.Height, true
			}
		default:
			value, present = inputs.Parameters[binding.Name]
		}
		if present {
			if err := writeWorkflowInput(nodes, binding.Target, value); err != nil {
				return nil, err
			}
		}
	}
	instantiated, err := json.Marshal(nodes)
	if err != nil {
		return nil, fmt.Errorf("%w: encode instantiated workflow", ErrWorkflowInputsInvalid)
	}
	return instantiated, nil
}

func parseUIWorkflow(raw json.RawMessage) (parsedUIWorkflow, error) {
	if len(raw) == 0 || len(raw) > maxUIWorkflowBytes {
		return parsedUIWorkflow{}, ErrInvalidUIWorkflow
	}
	value, canonical, err := decodeBoundedJSON(raw)
	if err != nil {
		return parsedUIWorkflow{}, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return parsedUIWorkflow{}, ErrInvalidUIWorkflow
	}
	if _, ok := root["nodes"].([]any); !ok {
		return parsedUIWorkflow{}, ErrInvalidUIWorkflow
	}
	if _, ok := root["links"].([]any); !ok {
		return parsedUIWorkflow{}, ErrInvalidUIWorkflow
	}
	var workflow uiWorkflow
	if err := json.Unmarshal(raw, &workflow); err != nil {
		return parsedUIWorkflow{}, ErrInvalidUIWorkflow
	}
	if len(workflow.Nodes) > maxUIWorkflowNodes || len(workflow.Links) > maxUIWorkflowLinks {
		return parsedUIWorkflow{}, ErrInvalidUIWorkflow
	}
	parsed := parsedUIWorkflow{RawCanonical: canonical, Nodes: make(map[string]uiWorkflowNode, len(workflow.Nodes)), NodeOrder: make([]string, 0, len(workflow.Nodes))}
	for _, node := range workflow.Nodes {
		id, err := normalizedJSONID(node.ID)
		if err != nil || strings.TrimSpace(node.Type) == "" {
			return parsedUIWorkflow{}, ErrInvalidUIWorkflow
		}
		if _, duplicate := parsed.Nodes[id]; duplicate {
			return parsedUIWorkflow{}, ErrInvalidUIWorkflow
		}
		inputNames := make(map[string]struct{}, len(node.Inputs))
		for _, input := range node.Inputs {
			if input.Name == "" {
				return parsedUIWorkflow{}, ErrInvalidUIWorkflow
			}
			if _, duplicate := inputNames[input.Name]; duplicate {
				return parsedUIWorkflow{}, ErrInvalidUIWorkflow
			}
			inputNames[input.Name] = struct{}{}
		}
		parsed.Nodes[id] = node
		parsed.NodeOrder = append(parsed.NodeOrder, id)
	}
	linkIDs := make(map[string]struct{}, len(workflow.Links))
	for _, rawLink := range workflow.Links {
		link, err := parseWorkflowLink(rawLink)
		if err != nil {
			return parsedUIWorkflow{}, ErrInvalidUIWorkflow
		}
		if _, duplicate := linkIDs[link.ID]; duplicate {
			return parsedUIWorkflow{}, ErrInvalidUIWorkflow
		}
		origin, originFound := parsed.Nodes[link.OriginID]
		target, targetFound := parsed.Nodes[link.TargetID]
		if !originFound || !targetFound || link.OriginSlot < 0 || link.TargetSlot < 0 || link.TargetSlot >= len(target.Inputs) {
			return parsedUIWorkflow{}, ErrInvalidUIWorkflow
		}
		_ = origin
		linkIDs[link.ID] = struct{}{}
		parsed.Links = append(parsed.Links, link)
	}
	return parsed, nil
}

func decodeBoundedJSON(raw json.RawMessage) (any, json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, nil, ErrInvalidUIWorkflow
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, nil, ErrInvalidUIWorkflow
	}
	if err := validateBoundedJSONValue(value, 1); err != nil {
		return nil, nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, nil, ErrInvalidUIWorkflow
	}
	return value, canonical, nil
}

func validateBoundedJSONValue(value any, depth int) error {
	if depth > maxUIWorkflowDepth {
		return ErrInvalidUIWorkflow
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if len(key) > maxUIWorkflowString {
				return ErrInvalidUIWorkflow
			}
			switch strings.ToLower(key) {
			case "url", "command", "script":
				return ErrInvalidUIWorkflow
			}
			if err := validateBoundedJSONValue(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateBoundedJSONValue(child, depth+1); err != nil {
				return err
			}
		}
	case string:
		if len(typed) > maxUIWorkflowString {
			return ErrInvalidUIWorkflow
		}
	}
	return nil
}

func parseWorkflowLink(raw json.RawMessage) (parsedWorkflowLink, error) {
	var fields []json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) < 6 {
		return parsedWorkflowLink{}, ErrInvalidUIWorkflow
	}
	id, err := normalizedJSONID(fields[0])
	if err != nil {
		return parsedWorkflowLink{}, err
	}
	originID, err := normalizedJSONID(fields[1])
	if err != nil {
		return parsedWorkflowLink{}, err
	}
	targetID, err := normalizedJSONID(fields[3])
	if err != nil {
		return parsedWorkflowLink{}, err
	}
	originSlot, err := jsonInteger(fields[2])
	if err != nil {
		return parsedWorkflowLink{}, err
	}
	targetSlot, err := jsonInteger(fields[4])
	if err != nil {
		return parsedWorkflowLink{}, err
	}
	return parsedWorkflowLink{ID: id, OriginID: originID, OriginSlot: originSlot, TargetID: targetID, TargetSlot: targetSlot}, nil
}

func normalizedJSONID(raw json.RawMessage) (string, error) {
	var stringID string
	if err := json.Unmarshal(raw, &stringID); err == nil {
		if strings.TrimSpace(stringID) == "" || len(stringID) > 128 {
			return "", ErrInvalidUIWorkflow
		}
		return stringID, nil
	}
	integer, err := jsonInteger(raw)
	if err != nil || integer < 0 {
		return "", ErrInvalidUIWorkflow
	}
	return strconv.Itoa(integer), nil
}

func jsonInteger(raw json.RawMessage) (int, error) {
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, ErrInvalidUIWorkflow
	}
	value, err := strconv.ParseInt(number.String(), 10, 32)
	if err != nil {
		return 0, ErrInvalidUIWorkflow
	}
	return int(value), nil
}

func compileAPITemplate(parsed parsedUIWorkflow, objectInfo ObjectInfo) (map[string]apiWorkflowNode, []string, []string, error) {
	connected := make(map[string]map[string]any)
	for _, link := range parsed.Links {
		target := parsed.Nodes[link.TargetID]
		inputName := target.Inputs[link.TargetSlot].Name
		if connected[link.TargetID] == nil {
			connected[link.TargetID] = make(map[string]any)
		}
		if _, duplicate := connected[link.TargetID][inputName]; duplicate {
			return nil, nil, nil, ErrInvalidUIWorkflow
		}
		connected[link.TargetID][inputName] = []any{link.OriginID, link.OriginSlot}
	}
	template := make(map[string]apiWorkflowNode, len(parsed.Nodes))
	requiredNodeSet := make(map[string]struct{}, len(parsed.Nodes))
	requiredModelSet := make(map[string]struct{})
	for _, nodeID := range parsed.NodeOrder {
		node := parsed.Nodes[nodeID]
		definition, found := objectInfo[node.Type]
		if !found {
			return nil, nil, nil, fmt.Errorf("%w: unknown node type %q", ErrInvalidUIWorkflow, node.Type)
		}
		inputs := make(map[string]any)
		for name, value := range connected[nodeID] {
			inputs[name] = value
		}
		widgetIndex := 0
		for _, name := range append(append([]string{}, definition.Input.RequiredOrder...), definition.Input.OptionalOrder...) {
			if _, isConnected := inputs[name]; isConnected {
				continue
			}
			rawDefinition, exists := definition.Input.Required[name]
			if !exists {
				rawDefinition, exists = definition.Input.Optional[name]
			}
			if !exists || !isWidgetInput(rawDefinition) {
				continue
			}
			if widgetIndex >= len(node.WidgetsValues) {
				return nil, nil, nil, fmt.Errorf("%w: missing widget value", ErrInvalidUIWorkflow)
			}
			var value any
			decoder := json.NewDecoder(bytes.NewReader(node.WidgetsValues[widgetIndex]))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				return nil, nil, nil, ErrInvalidUIWorkflow
			}
			inputs[name] = value
			widgetIndex++
			lowerName := strings.ToLower(name)
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" &&
				(strings.Contains(lowerName, "model") || strings.Contains(lowerName, "ckpt") || strings.Contains(lowerName, "lora") || strings.Contains(lowerName, "vae")) {
				requiredModelSet[text] = struct{}{}
			}
		}
		if widgetIndex != len(node.WidgetsValues) {
			return nil, nil, nil, fmt.Errorf("%w: extra widget values", ErrInvalidUIWorkflow)
		}
		template[nodeID] = apiWorkflowNode{ClassType: node.Type, Inputs: inputs}
		requiredNodeSet[node.Type] = struct{}{}
	}
	return template, sortedSet(requiredNodeSet), sortedSet(requiredModelSet), nil
}

func isWidgetInput(raw json.RawMessage) bool {
	var definition []any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&definition); err != nil || len(definition) == 0 {
		return false
	}
	switch typed := definition[0].(type) {
	case []any:
		return true
	case string:
		switch strings.ToUpper(typed) {
		case "STRING", "INT", "FLOAT", "BOOLEAN", "COMBO", "IMAGEUPLOAD":
			return true
		}
	}
	return false
}

func suggestWorkflowBindings(parsed parsedUIWorkflow, objectInfo ObjectInfo, template map[string]apiWorkflowNode) WorkflowSuggestions {
	suggestions := WorkflowSuggestions{Prompts: []WorkflowTarget{}, References: []ReferenceBinding{}, Outputs: []OutputBinding{}, Parameters: []ParameterBinding{}}
	for _, nodeID := range parsed.NodeOrder {
		node := parsed.Nodes[nodeID]
		definition := objectInfo[node.Type]
		for name, value := range template[nodeID].Inputs {
			if _, connected := value.([]any); connected {
				continue
			}
			lowerName := strings.ToLower(name)
			target := WorkflowTarget{NodeID: nodeID, InputName: name}
			if lowerName == "text" && strings.Contains(strings.ToLower(node.Type), "textencode") {
				suggestions.Prompts = append(suggestions.Prompts, target)
			}
			if lowerName == "image" && strings.Contains(strings.ToLower(node.Type), "loadimage") {
				suggestions.References = append(suggestions.References, ReferenceBinding{Index: len(suggestions.References), Target: target})
			}
			switch lowerName {
			case "seed", "width", "height", "denoise", "cfg", "steps":
				suggestions.Parameters = append(suggestions.Parameters, ParameterBinding{Name: lowerName, Target: target})
			}
		}
		if definition.OutputNode {
			suggestions.Outputs = append(suggestions.Outputs, OutputBinding{NodeID: nodeID})
		}
	}
	return suggestions
}

func validateWorkflowBindings(bindings WorkflowBindings, parsed parsedUIWorkflow, objectInfo ObjectInfo, template map[string]apiWorkflowNode) error {
	if len(bindings.Prompts) == 0 || len(bindings.Outputs) == 0 {
		return ErrWorkflowBindingInvalid
	}
	seenTargets := make(map[string]struct{})
	checkTarget := func(target WorkflowTarget) error {
		key := target.NodeID + "\x00" + target.InputName
		if _, duplicate := seenTargets[key]; duplicate {
			return ErrWorkflowBindingInvalid
		}
		seenTargets[key] = struct{}{}
		node, found := parsed.Nodes[target.NodeID]
		if !found || target.InputName == "" || target.InputName == "class_type" {
			return ErrWorkflowBindingInvalid
		}
		definition := objectInfo[node.Type]
		if _, required := definition.Input.Required[target.InputName]; !required {
			if _, optional := definition.Input.Optional[target.InputName]; !optional {
				return ErrWorkflowBindingInvalid
			}
		}
		value, exists := template[target.NodeID].Inputs[target.InputName]
		if !exists {
			return ErrWorkflowBindingInvalid
		}
		if link, connected := value.([]any); connected && len(link) == 2 {
			return ErrWorkflowBindingInvalid
		}
		return nil
	}
	for _, target := range bindings.Prompts {
		if err := checkTarget(target); err != nil {
			return err
		}
	}
	if bindings.Negative != nil {
		if err := checkTarget(*bindings.Negative); err != nil {
			return err
		}
	}
	for _, reference := range bindings.References {
		if reference.Index < 0 {
			return ErrWorkflowBindingInvalid
		}
		if err := checkTarget(reference.Target); err != nil {
			return err
		}
	}
	for _, parameter := range bindings.Parameters {
		if strings.TrimSpace(parameter.Name) == "" {
			return ErrWorkflowBindingInvalid
		}
		if err := checkTarget(parameter.Target); err != nil {
			return err
		}
	}
	seenOutputs := make(map[string]struct{})
	for _, output := range bindings.Outputs {
		node, found := parsed.Nodes[output.NodeID]
		if !found || !objectInfo[node.Type].OutputNode || output.OutputIndex < 0 {
			return ErrWorkflowBindingInvalid
		}
		if _, duplicate := seenOutputs[output.NodeID]; duplicate {
			return ErrWorkflowBindingInvalid
		}
		seenOutputs[output.NodeID] = struct{}{}
	}
	return nil
}

func writeWorkflowInput(nodes map[string]apiWorkflowNode, target WorkflowTarget, value any) error {
	node, found := nodes[target.NodeID]
	if !found {
		return ErrWorkflowBindingInvalid
	}
	current, found := node.Inputs[target.InputName]
	if !found {
		return ErrWorkflowBindingInvalid
	}
	if link, connected := current.([]any); connected && len(link) == 2 {
		return ErrWorkflowBindingInvalid
	}
	node.Inputs[target.InputName] = value
	nodes[target.NodeID] = node
	return nil
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func digestJSON(raw json.RawMessage) string {
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("%x", digest[:])
}
