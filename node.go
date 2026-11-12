package inode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Values = map[string]any

type Port struct {
	Type            Type `json:"type"`
	Default         any  `json:"default,omitempty"`
	NoOutsideEffect bool `json:"no_outside_effect,omitempty"`
}

type Ports map[string]Port

func (ports *Ports) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*ports = Ports{}
	for _, name := range keys(raw) {
		var declaration struct {
			Type    Type `json:"type"`
			Default any  `json:"default"`
		}
		var err error
		if raw[name][0] == '"' {
			err = json.Unmarshal(raw[name], &declaration.Type)
		} else {
			err = decode(raw[name], &declaration)
		}
		if err == nil && declaration.Type.Name == "" {
			err = fmt.Errorf("specify the port's type")
		}
		if err == nil && !portName(name) {
			err = fmt.Errorf("rename the port without dots or equals signs")
		}
		if err != nil {
			return Problem{Input: name, Message: err.Error()}
		}
		(*ports)[name] = Port{Type: declaration.Type, Default: declaration.Default}
	}
	return nil
}

func (ports Ports) tree() Values {
	values := Values{}
	for name, port := range ports {
		values[name] = port.Type
	}
	return values
}

func (ports Ports) substitute(types map[string]Type) Ports {
	result := Ports{}
	for name, port := range ports {
		port.Type = port.Type.substitute(types)
		result[name] = port
	}
	return result
}

type Step struct {
	Use      Definition `json:"use"`
	In       Values     `json:"in,omitempty"`
	After    []string   `json:"after,omitempty"`
	plan     *plan
	contract *Type
}

type Definition struct {
	Name     string
	Workflow *Workflow
}

func (d *Definition) UnmarshalJSON(data []byte) error {
	if data[0] == '"' {
		return json.Unmarshal(data, &d.Name)
	}
	d.Workflow = &Workflow{}
	return decode(data, d.Workflow)
}

func (d Definition) MarshalJSON() ([]byte, error) {
	if d.Workflow != nil {
		return json.Marshal(d.Workflow)
	}
	return json.Marshal(d.Name)
}

type Workflow struct {
	Doc     string          `json:"doc,omitempty"`
	Inputs  Ports           `json:"inputs"`
	Steps   map[string]Step `json:"steps"`
	Outputs Values          `json:"outputs"`
	View    any             `json:"view,omitempty"`
}

type Node struct {
	Name            string    `json:"name"`
	Doc             string    `json:"doc"`
	Inputs          Ports     `json:"inputs"`
	Outputs         Ports     `json:"outputs"`
	NoOutsideEffect bool      `json:"no_outside_effect"`
	Workflow        *Workflow `json:"workflow,omitempty"`
	run             func(context.Context, Values, Call) (Values, error)
	problem         error
}

type Project struct {
	dir   string
	nodes map[string]Node
}

func Open(dir string) (*Project, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	p := &Project{dir: dir, nodes: builtins(dir)}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".flow") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		name := strings.TrimSuffix(entry.Name(), ".flow")
		if node, exists := p.nodes[name]; exists {
			node.problem = fmt.Errorf("%s: rename the workflow; node %q already exists", path, name)
			p.nodes[name] = node
			continue
		}
		flow, err := readWorkflow(path)
		p.nodes[name] = Node{Doc: flow.Doc, Inputs: flow.Inputs, Workflow: &flow, problem: err}
	}
	for name, node := range p.nodes {
		node.Name = name
		p.nodes[name] = node
	}
	return p, nil
}

func readWorkflow(path string) (Workflow, error) {
	var flow Workflow
	data, err := os.ReadFile(path)
	if err != nil {
		return flow, err
	}
	var raw any
	err = yaml.NewDecoder(bytes.NewReader(data)).Decode(&raw)
	if err == nil {
		data, err = json.Marshal(raw)
	}
	if err == nil {
		err = decode(data, &flow)
	}
	if err != nil {
		err = fmt.Errorf("%s: fix the workflow: %w", path, err)
	}
	return flow, err
}

func decode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func (p *Project) Nodes() []Node {
	nodes := make([]Node, 0, len(p.nodes))
	for _, name := range keys(p.nodes) {
		nodes = append(nodes, p.nodes[name])
	}
	return nodes
}

func (p *Project) Describe(name string) (Node, error) {
	name = strings.TrimSuffix(name, ".flow")
	plan, report := p.check(name)
	if len(report.Problems) != 0 {
		return Node{}, Problems(report.Problems)
	}
	node := plan.Node
	if node.Workflow == nil {
		node.Inputs, node.Outputs = p.nodes[name].Inputs, p.nodes[name].Outputs
	}
	return node, nil
}

func jsonData[T any](value T) (T, error) {
	data, err := json.Marshal(value)
	var result T
	if err == nil {
		err = json.Unmarshal(data, &result)
	}
	return result, err
}

func keys[V any](m map[string]V) []string {
	result := make([]string, 0, len(m))
	for key := range m {
		result = append(result, key)
	}
	slices.Sort(result)
	return result
}
