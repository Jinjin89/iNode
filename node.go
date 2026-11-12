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
	"sync"

	"go.yaml.in/yaml/v3"
)

type Values = map[string]any

type Port struct {
	Type            Type `json:"type" yaml:"type"`
	Default         any  `json:"default,omitempty" yaml:"default,omitempty"`
	NoOutsideEffect bool `json:"no_outside_effect,omitempty" yaml:"no_outside_effect,omitempty"`
}

type Ports map[string]Port

func (port Port) MarshalYAML() (any, error) {
	if port.Default == nil && !port.NoOutsideEffect {
		return port.Type.String(), nil
	}
	type declaration Port
	return declaration(port), nil
}

func (port Port) MarshalJSON() ([]byte, error) {
	type declaration Port
	value := struct {
		declaration
		Kind   string `json:"kind"`
		Item   *Port  `json:"item,omitempty"`
		Fields *Ports `json:"fields,omitempty"`
	}{declaration: declaration(port), Kind: port.Type.Name}
	if port.Type.Item != nil {
		value.Item = &Port{Type: *port.Type.Item}
	}
	if port.Type.Fields != nil {
		fields := Ports{}
		for name, typ := range port.Type.Fields {
			fields[name] = Port{Type: typ}
		}
		value.Fields = &fields
	}
	return json.Marshal(value)
}

func (ports *Ports) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*ports = Ports{}
	for _, name := range keys(raw) {
		var declaration struct {
			Type    Type            `json:"type"`
			Default any             `json:"default"`
			Kind    json.RawMessage `json:"kind"`
			Item    json.RawMessage `json:"item"`
			Fields  json.RawMessage `json:"fields"`
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
	Use      Definition `json:"use" yaml:"use"`
	In       Values     `json:"in,omitempty" yaml:"in,omitempty"`
	After    []string   `json:"after,omitempty" yaml:"after,omitempty"`
	plan     *plan
	contract *Type
}

func nodeValue(value any) (any, error) {
	switch v := value.(type) {
	case string:
		if !strings.HasPrefix(v, "=") {
			return &Step{Use: Definition{Name: v}}, nil
		}
	case map[string]any:
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		step := &Step{}
		return step, decode(data, step)
	}
	return value, nil
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

func (d Definition) MarshalYAML() (any, error) {
	if d.Workflow != nil {
		return d.Workflow, nil
	}
	return d.Name, nil
}

type Workflow struct {
	Doc     string          `json:"doc,omitempty" yaml:"doc,omitempty"`
	Inputs  Ports           `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	Steps   map[string]Step `json:"steps" yaml:"steps"`
	Outputs Values          `json:"outputs,omitempty" yaml:"outputs,omitempty"`
	View    any             `json:"view,omitempty" yaml:"view,omitempty"`
}

type Node struct {
	Name            string    `json:"name"`
	Doc             string    `json:"doc"`
	Inputs          Ports     `json:"inputs"`
	Outputs         Ports     `json:"outputs"`
	NoOutsideEffect bool      `json:"no_outside_effect"`
	Trigger         bool      `json:"trigger,omitempty"`
	Workflow        *Workflow `json:"workflow,omitempty"`
	Version         string    `json:"version,omitempty"`
	run             func(context.Context, Values, Call) (Values, error)
	watch           func(context.Context, Values) (watch, error)
	problem         error
}

type Project struct {
	dir     string
	nodes   map[string]Node
	plugins []Plugin
	mu      *sync.Mutex
	publish func(*Record) // Called under mu.
}

func Open(dir string) (*Project, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	p := &Project{dir: dir, mu: &sync.Mutex{}}
	return p, p.load()
}

func (p *Project) register(name string, node Node) {
	if _, exists := p.nodes[name]; exists {
		node.problem = fmt.Errorf("node %q has more than one definition; rename one of them", name)
	}
	node.Name = name
	node.Trigger = node.watch != nil
	if node.watch != nil {
		node.run = func(ctx context.Context, in Values, _ Call) (Values, error) {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			listen, err := node.watch(ctx, in)
			var event Values
			if err == nil {
				err = listen(func(value Values) error { event = value; cancel(); return context.Canceled })
			}
			if event != nil {
				return event, nil
			}
			return nil, err
		}
	}
	p.nodes[name] = node
}

func (p *Project) load() error {
	p.nodes = map[string]Node{}
	for name, node := range builtins(p.dir) {
		p.register(name, node)
	}
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".flow") {
			continue
		}
		path := filepath.Join(p.dir, entry.Name())
		name := strings.TrimSuffix(entry.Name(), ".flow")
		var flow Workflow
		data, err := os.ReadFile(path)
		if err == nil {
			err = decodeYAML(data, &flow)
		}
		if err != nil {
			err = fmt.Errorf("%s: %w", path, err)
		}
		p.register(name, Node{Doc: flow.Doc, Inputs: flow.Inputs, Workflow: &flow, Version: version(data), problem: err})
	}
	if err := p.loadNodes(filepath.Join(p.dir, "nodes"), ""); err != nil {
		return err
	}
	return p.loadPlugins()
}

func readYAML(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := decodeYAML(data, target); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func decodeYAML(data []byte, target any) error {
	var raw any
	err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&raw)
	if err == nil {
		data, err = json.Marshal(raw)
	}
	if err == nil {
		err = decode(data, target)
	}
	return err
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
	if plan == nil {
		return Node{}, Problems(report.Problems)
	}
	node := p.nodes[name]
	if node.Workflow != nil {
		node.Outputs, node.NoOutsideEffect = plan.Outputs, plan.NoOutsideEffect
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
