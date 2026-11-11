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
	Type    Type `json:"type"`
	Default any  `json:"default,omitempty"`
}

type Ports map[string]Port

func (ports *Ports) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*ports = Ports{}
	for _, name := range keys(raw) {
		var port Port
		var err error
		if raw[name][0] == '"' {
			err = json.Unmarshal(raw[name], &port.Type)
		} else {
			decoder := json.NewDecoder(bytes.NewReader(raw[name]))
			decoder.DisallowUnknownFields()
			err = decoder.Decode(&port)
		}
		if err == nil && port.Type.Name == "" {
			err = fmt.Errorf("specify the port's type")
		}
		if err == nil && !portName(name) {
			err = fmt.Errorf("rename the port without dots or equals signs")
		}
		if err == nil && port.Default != nil {
			if err = match(port.Default, port.Type); err != nil {
				err = fmt.Errorf("fix the default: %w", err)
			}
		}
		if err != nil {
			return Problem{Input: name, Message: err.Error()}
		}
		(*ports)[name] = port
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

type Step struct {
	Use   string   `json:"use"`
	In    Values   `json:"in,omitempty"`
	After []string `json:"after,omitempty"`
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
	run             func(context.Context, Values) (Values, error)
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
		if _, exists := p.nodes[name]; exists {
			node := p.nodes[name]
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
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&raw); err != nil {
		return flow, fmt.Errorf("%s: fix the workflow YAML: %w", path, err)
	}
	data, err = json.Marshal(raw)
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&flow)
	}
	if err != nil {
		err = fmt.Errorf("%s: fix the workflow: %w", path, err)
	}
	return flow, err
}

func (p *Project) Nodes() []Node {
	nodes := make([]Node, 0, len(p.nodes))
	for _, name := range keys(p.nodes) {
		nodes = append(nodes, p.nodes[name])
	}
	return nodes
}

func (p *Project) Describe(name string) (Node, error) {
	plan, report := p.check(name)
	if len(report.Problems) != 0 {
		return Node{}, Problems(report.Problems)
	}
	return plan.Node, nil
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
