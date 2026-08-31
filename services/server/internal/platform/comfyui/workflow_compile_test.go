package comfyui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectUIWorkflowSuggestsButDoesNotConfirmBindings(t *testing.T) {
	inspection, err := InspectUIWorkflow(loadFixture(t, "ui_t2i.json"), loadObjectInfo(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Suggestions.Prompts) == 0 || inspection.Suggestions.Prompts[0].NodeID != "6" || inspection.Confirmed {
		t.Fatalf("inspection = %#v", inspection)
	}
}

func TestCompileUIWorkflowRequiresConfirmedOutputAndPrompt(t *testing.T) {
	bindings := WorkflowBindings{Confirmed: false}
	if _, err := CompileUIWorkflow(loadFixture(t, "ui_t2i.json"), loadObjectInfo(t), bindings); !errors.Is(err, ErrWorkflowBindingsUnconfirmed) {
		t.Fatalf("error = %v, want ErrWorkflowBindingsUnconfirmed", err)
	}
}

func TestCompileUIWorkflowProducesDeterministicAPITemplate(t *testing.T) {
	bindings := confirmedT2IBindings()
	first, err := CompileUIWorkflow(loadFixture(t, "ui_t2i.json"), loadObjectInfo(t), bindings)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CompileUIWorkflow(loadFixture(t, "ui_t2i.json"), loadObjectInfo(t), bindings)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.APITemplate, second.APITemplate) || first.APITemplateDigest != second.APITemplateDigest || first.WorkflowDigest != second.WorkflowDigest {
		t.Fatalf("non-deterministic compile: first=%#v second=%#v", first, second)
	}
}

func TestInstantiateWorkflowDoesNotMutateTemplate(t *testing.T) {
	compiled := mustCompileFixture(t, "ui_i2i.json")
	before := append([]byte(nil), compiled.APITemplate...)
	seed, width, height := int64(7), 1024, 1024
	instantiated, err := InstantiateWorkflow(compiled.APITemplate, compiled.Bindings, WorkflowInputs{
		Prompts: []string{"portrait"}, Seed: &seed, Width: &width, Height: &height,
		References: []UploadedReference{{Name: "ref.png", Type: "input"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, compiled.APITemplate) {
		t.Fatal("stored template mutated")
	}
	if bytes.Equal(instantiated, compiled.APITemplate) {
		t.Fatal("instantiated workflow did not receive declared inputs")
	}
}

func TestInstantiateWorkflowRejectsConnectedBinding(t *testing.T) {
	template := json.RawMessage(`{"6":{"class_type":"CLIPTextEncode","inputs":{"text":["4",0]}}}`)
	bindings := WorkflowBindings{Confirmed: true, Prompts: []WorkflowTarget{{NodeID: "6", InputName: "text"}}, Outputs: []OutputBinding{{NodeID: "9"}}}
	if _, err := InstantiateWorkflow(template, bindings, WorkflowInputs{Prompts: []string{"must not overwrite link"}}); !errors.Is(err, ErrWorkflowBindingInvalid) {
		t.Fatalf("error = %v, want ErrWorkflowBindingInvalid", err)
	}
}

func TestUIWorkflowParserRejectsUnsafeOrUnboundedPayloads(t *testing.T) {
	largeString := strings.Repeat("x", 64*1024+1)
	nodes := make([]string, 2049)
	for index := range nodes {
		nodes[index] = fmt.Sprintf(`{"id":%d,"type":"SaveImage","inputs":[],"outputs":[],"widgets_values":[]}`, index+1)
	}
	links := make([]string, 8193)
	for index := range links {
		links[index] = fmt.Sprintf(`[%d,1,0,2,0,"IMAGE"]`, index+1)
	}
	tests := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "over 8 MiB", raw: json.RawMessage(`{"nodes":[],"links":[],"extra":"` + strings.Repeat("x", 8*1024*1024) + `"}`)},
		{name: "depth 65", raw: json.RawMessage(strings.Repeat(`{"x":`, 65) + `0` + strings.Repeat(`}`, 65))},
		{name: "2049 nodes", raw: json.RawMessage(`{"nodes":[` + strings.Join(nodes, ",") + `],"links":[]}`)},
		{name: "8193 links", raw: json.RawMessage(`{"nodes":[{"id":1,"type":"LoadImage","inputs":[],"outputs":[],"widgets_values":[]},{"id":2,"type":"SaveImage","inputs":[],"outputs":[],"widgets_values":[]}],"links":[` + strings.Join(links, ",") + `]}`)},
		{name: "duplicate node ids", raw: json.RawMessage(`{"nodes":[{"id":1,"type":"A"},{"id":1,"type":"B"}],"links":[]}`)},
		{name: "dangling link", raw: json.RawMessage(`{"nodes":[{"id":1,"type":"A"}],"links":[[1,1,0,2,0,"X"]]}`)},
		{name: "non object root", raw: json.RawMessage(`[]`)},
		{name: "API format only", raw: json.RawMessage(`{"1":{"class_type":"KSampler","inputs":{}}}`)},
		{name: "url field", raw: json.RawMessage(`{"nodes":[],"links":[],"url":"https://example.com"}`)},
		{name: "command field", raw: json.RawMessage(`{"nodes":[],"links":[],"command":"whoami"}`)},
		{name: "script field", raw: json.RawMessage(`{"nodes":[],"links":[],"script":"alert(1)"}`)},
		{name: "string over 64 KiB", raw: json.RawMessage(`{"nodes":[],"links":[],"extra":"` + largeString + `"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := InspectUIWorkflow(test.raw, ObjectInfo{}); !errors.Is(err, ErrInvalidUIWorkflow) {
				t.Fatalf("error = %v, want ErrInvalidUIWorkflow", err)
			}
		})
	}
}

func FuzzUIWorkflowParser(f *testing.F) {
	f.Add([]byte(`{"nodes":[],"links":[]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = InspectUIWorkflow(raw, ObjectInfo{})
	})
}

func confirmedT2IBindings() WorkflowBindings {
	return WorkflowBindings{
		Confirmed: true,
		Prompts:   []WorkflowTarget{{NodeID: "6", InputName: "text"}},
		Outputs:   []OutputBinding{{NodeID: "9"}},
	}
}

func mustCompileFixture(t *testing.T, name string) CompiledWorkflow {
	t.Helper()
	bindings := confirmedT2IBindings()
	if name == "ui_i2i.json" {
		bindings.References = []ReferenceBinding{{Index: 0, Target: WorkflowTarget{NodeID: "10", InputName: "image"}}}
	}
	compiled, err := CompileUIWorkflow(loadFixture(t, name), loadObjectInfo(t), bindings)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func loadFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func loadObjectInfo(t *testing.T) ObjectInfo {
	t.Helper()
	raw := loadFixture(t, "object_info.json")
	var objectInfo ObjectInfo
	if err := json.Unmarshal(raw, &objectInfo); err != nil {
		t.Fatal(err)
	}
	return objectInfo
}
