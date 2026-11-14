package inode

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type Problem struct {
	Step    string `json:"step"`
	Input   string `json:"input"`
	Message string `json:"message"`
}

func (p Problem) Error() string {
	if p.Step == "" {
		return fmt.Sprintf("input %q: %s", p.Input, p.Message)
	}
	return fmt.Sprintf("step %q, input %q: %s", p.Step, p.Input, p.Message)
}

type Problems []Problem

func (ps Problems) Error() string {
	lines := make([]string, len(ps))
	for i, p := range ps {
		lines[i] = p.Error()
	}
	return strings.Join(lines, "\n")
}

type CheckReport struct {
	Problems   []Problem       `json:"problems"`
	Unchecked  []Problem       `json:"unchecked"`
	References []Reference     `json:"references"`
	Nodes      map[string]Node `json:"nodes"`
}

type plan struct {
	Node
	deps   map[string]map[string]bool
	effect string
}

type checker struct {
	project     *Project
	active      map[string]bool
	report      CheckReport
	root, scope string
	path        valuePath
}

func (p *Project) Check(use Definition) CheckReport {
	_, _, report := p.prepare(use, nil)
	return report
}
func (p *Project) check(name string) (*plan, CheckReport) {
	step, _, report := p.prepare(Definition{Name: name}, nil)
	return step.plan, report
}

func (p *Project) prepare(use Definition, inputs Values) (*Step, binding, CheckReport) {
	use.Name = strings.TrimSuffix(use.Name, ".flow")
	name := use.Name
	if use.Workflow != nil {
		name = "(inline)"
	}
	c := checker{project: p, root: name, scope: name, active: map[string]bool{}, report: CheckReport{
		Problems: []Problem{}, Unchecked: []Problem{}, References: []Reference{}, Nodes: map[string]Node{},
	}}
	step := &Step{Use: use, In: inputs}
	bound, _ := c.compile(step, name, nil, nil)
	for i := range c.report.References {
		ref := &c.report.References[i]
		for _, problem := range c.report.Problems {
			ref.Bad = ref.Bad || problem.covers(*ref)
		}
		for _, problem := range c.report.Unchecked {
			ref.Unchecked = ref.Unchecked || problem.covers(*ref)
		}
	}
	return step, bound, c.report
}

func (p Problem) covers(ref Reference) bool {
	if p.Step == ref.Step {
		return p.Input == ref.Input || strings.HasPrefix(ref.Input, p.Input+".")
	}
	return ref.Step == p.Step+"/"+p.Input || strings.HasPrefix(ref.Step, p.Step+"/"+p.Input+"/")
}

func (c *checker) problem(step, input, message string) {
	c.report.Problems = append(c.report.Problems, Problem{step, input, message})
}
func (c *checker) error(step, input string, err error) {
	var problem Problem
	if errors.As(err, &problem) {
		problem.Step = step
		c.report.Problems = append(c.report.Problems, problem)
	} else {
		c.problem(step, input, err.Error())
	}
}

func (c *checker) compile(step *Step, location string, tree Values, path valuePath) (binding, []Reference) {
	beforePath := c.path
	c.path = path.child("in")
	defer func() { c.path = beforePath }()
	name := strings.TrimSuffix(step.Use.Name, ".flow")
	node := c.project.lookup(name)
	if step.Use.Workflow != nil {
		flow := step.Use.Workflow
		node = Node{Name: "(inline)", Doc: flow.Doc, Inputs: flow.Inputs, Workflow: flow}
	}
	problem := node.problem
	if node.Workflow != nil && name != "" && problem == nil {
		if c.active[name] {
			problem = fmt.Errorf("workflow %q calls itself; remove this workflow dependency", name)
		} else {
			c.active[name] = true
			defer delete(c.active, name)
		}
	}
	if problem != nil {
		c.error(location, "use", problem)
	}
	p := &plan{Node: node, deps: map[string]map[string]bool{}}
	p.Inputs = Ports{}
	for name, port := range node.Inputs {
		if port.Default != nil {
			port.Default = c.expression(port.Default, port.Type, location, valuePath{name}, nil)
		}
		p.Inputs[name] = port
	}
	parsed, supplied := Values{}, Values{}
	var refs []Reference
	for _, input := range keys(step.In) {
		want := anyType
		if port, exists := p.Inputs[input]; exists {
			want = port.Type
		}
		before := len(c.report.Problems)
		parsed[input] = c.expression(step.In[input], want, location, valuePath{input}, tree)
		supplied[input] = anyType
		if len(c.report.Problems) != before {
			continue
		}
		supplied[input] = parsed[input]
		if tree != nil {
			selected, found, err := resolve(parsed[input], want, valuePath{input}, tree)
			if err != nil {
				c.error(location, input, err)
			} else {
				supplied[input] = selected
				refs = append(refs, found...)
			}
		}
	}
	if problem != nil {
		return binding{}, refs
	}
	step.plan = p
	types := map[string]Type{}
	bound := bind(p.Inputs, supplied, location, "", types)
	for i := range bound.Problems {
		if _, explicit := step.In[bound.Problems[i].Input]; !explicit {
			if port := p.Inputs[bound.Problems[i].Input]; port.Default != nil {
				bound.Problems[i].Message = "fix the default: " + bound.Problems[i].Message
			}
		}
	}
	c.report.Problems = append(c.report.Problems, bound.Problems...)
	for _, input := range bound.Unchecked {
		c.report.Unchecked = append(c.report.Unchecked, Problem{location, input, "the node signature is partly unstated; each call will check its inputs and output"})
	}
	step.In = parsed
	p.Inputs, p.Outputs = p.Inputs.substitute(types), p.Outputs.substitute(types)
	if c.scope == c.root {
		for _, ref := range refs {
			for i := range c.report.References {
				if slices.Equal(c.report.References[i].Path, c.path.child(ref.Path...)) {
					c.report.References[i].Unchecked = ref.Unchecked
				}
			}
		}
	}
	for _, input := range keys(bound.Values) {
		if _, err := c.project.files(bound.Values[input], p.Inputs[input].Type, checkFiles, input); err != nil {
			c.error(location, input, err)
		}
		if err := c.project.checkSecrets(bound.Values[input], p.Inputs[input].Type, valuePath{input}); err != nil {
			c.error(location, input, err)
		}
	}
	if !node.NoOutsideEffect && node.Workflow == nil {
		p.effect = location + " (" + node.Name + ")"
	}
	for _, input := range keys(p.Inputs) {
		want := p.Inputs[input].Type
		value, exists := bound.Values[input]
		if !exists {
			value = want
		}
		if effect := outsideEffect(value, want); p.effect == "" {
			p.effect = effect
		}
	}
	if node.Workflow != nil {
		c.workflow(p, location)
	}
	p.NoOutsideEffect = p.effect == ""
	if step.Use.Workflow != nil {
		step.Use.Workflow = p.Workflow
	}
	if c.scope == c.root {
		display := map[string]Type{}
		for letter := 'A'; letter <= 'Z'; letter++ {
			display[string(letter)] = Type{Name: string(letter)}
		}
		for name, typ := range types {
			display[name] = typ
		}
		node.Inputs, node.Outputs, node.Workflow = node.Inputs.substitute(display), node.Outputs.substitute(display), nil
		node.NoOutsideEffect = p.NoOutsideEffect
		if p.Workflow != nil {
			node.Outputs = p.Outputs
		}
		key, _ := json.Marshal(path)
		c.report.Nodes[string(key)] = node
	}
	return bound, refs
}

func (c *checker) workflow(p *plan, location string) {
	beforeScope, beforePath := c.scope, c.path
	c.scope, c.path = location, nil
	defer func() { c.scope, c.path = beforeScope, beforePath }()
	original := p.Workflow
	flow := *original
	flow.Inputs, flow.Steps, flow.Outputs = p.Inputs, map[string]Step{}, Values{}
	p.Workflow, p.Outputs = &flow, Ports{}
	tree := Values{"inputs": p.Inputs.tree()}
	states := map[string]int{}
	var get func(string) (any, error)
	get = func(id string) (any, error) {
		if states[id] == 1 {
			return nil, fmt.Errorf("dependency on %q makes a cycle; remove that reference or after entry", id)
		}
		if states[id] == 2 {
			if child := flow.Steps[id].plan; child != nil {
				return child.Outputs.tree(), nil
			}
			return anyType, nil
		}
		states[id] = 1
		step := original.Steps[id]
		where := location + "/" + id
		p.deps[id] = map[string]bool{}
		if !portName(id) || id == "inputs" {
			c.problem(where, "use", "rename the step without dots or equals signs, and do not use the reserved name inputs")
		}
		for _, dependency := range step.After {
			if _, exists := original.Steps[dependency]; !exists {
				c.problem(where, "after", fmt.Sprintf("unknown step %q; choose an existing step", dependency))
			} else {
				p.deps[id][dependency] = true
				if _, err := get(dependency); err != nil {
					c.error(where, "after", err)
				}
			}
		}
		before := len(c.report.References)
		bound, refs := c.compile(&step, where, tree, valuePath{"steps", id})
		c.report.Problems = append(c.report.Problems, missing(bound.Open, where, "")...)
		for dependency := range c.references(where, refs) {
			p.deps[id][dependency] = true
		}
		for _, ref := range c.report.References[before:] {
			if ref.Path[0] == "steps" && ref.Path[1] == id && ref.From[0] != "inputs" {
				p.deps[id][ref.From[0]] = true
			}
		}
		flow.Steps[id], states[id] = step, 2
		if step.plan != nil {
			if p.effect == "" {
				p.effect = step.plan.effect
			}
			return step.plan.Outputs.tree(), nil
		}
		return anyType, nil
	}
	for _, id := range keys(original.Steps) {
		tree[id] = func() (any, error) { return get(id) }
	}
	for _, id := range keys(original.Steps) {
		_, _ = get(id)
	}
	for _, output := range keys(original.Outputs) {
		if !portName(output) {
			c.problem(location, "outputs."+output, "rename the output without dots or equals signs")
		}
		parsed := c.expression(original.Outputs[output], anyType, location, valuePath{"outputs", output}, tree)
		value, refs, err := resolve(parsed, anyType, valuePath{"outputs", output}, tree)
		if err != nil {
			c.error(location, "outputs."+output, err)
			value = anyType
		}
		c.references(location, refs)
		flow.Outputs[output] = parsed
		p.Outputs[output] = Port{Type: typeOf(value)}
	}
}

func (c *checker) references(location string, refs []Reference) map[string]bool {
	deps := map[string]bool{}
	for _, ref := range refs {
		if ref.From[0] != "inputs" {
			deps[ref.From[0]] = true
		}
		if ref.Unchecked {
			c.report.Unchecked = append(c.report.Unchecked, Problem{location, ref.Input, "contents are unknown until execution; the path and consumer's expected type will be checked then"})
		}
	}
	return deps
}

func (c *checker) expression(value any, want Type, location string, path valuePath, tree Values) any {
	result, _ := walk(value, want, path, func(value any, want Type, path valuePath) (any, error) {
		if fileLike(value) {
			if _, err := c.project.files(value, want, checkFiles, path...); err != nil {
				c.error(location, path.String(), err)
			}
		}
		if text, ok := value.(string); ok && tree != nil && c.scope == c.root {
			if from := referenceParts(text); from != nil {
				c.report.References = append(c.report.References, Reference{Step: location, Input: path.String(), From: from, Path: c.path.child(path...)})
			}
		}
		if want.Name == "node" {
			var err error
			value, err = nodeValue(value)
			if err != nil {
				c.error(location, path.String(), err)
				return anyType, nil
			}
		}
		if node, ok := value.(*Step); ok {
			copy := *node
			copy.contract = nil
			node = &copy
			bound, _ := c.compile(node, location+"/"+path.String(), tree, c.path.child(path...))
			if len(node.After) > 0 {
				c.problem(location, path.String(), "a boxed node is not scheduled; put after on the receiving step")
			}
			if node.plan == nil {
				return anyType, nil
			}
			if len(bound.Open) > 1 {
				c.problem(location, path.String(), "a boxed node has at most one open input; fill inputs or expose one object")
			}
			if len(node.plan.Outputs) != 1 {
				c.problem(location, path.String(), "a boxed node must have exactly one output; expose one object to return several values")
			}
			return node, nil
		}
		return value, nil
	})
	return result
}

func portName(name string) bool { return name != "" && !strings.ContainsAny(name, ".=") }

type binding struct {
	Values    Values
	Open      Ports
	Problems  Problems
	Unchecked []string
}

func bind(ports Ports, supplied Values, step, prefix string, types map[string]Type) binding {
	bound := binding{Values: Values{}, Open: Ports{}}
	for _, input := range keys(supplied) {
		if _, exists := ports[input]; !exists {
			bound.Problems = append(bound.Problems, Problem{step, prefix + input, fmt.Sprintf("unknown port; remove it or choose one of %v", keys(ports))})
		}
		bound.Values[input] = supplied[input]
	}
	for _, input := range keys(ports) {
		port := ports[input]
		value, exists := bound.Values[input]
		if !exists && port.Default != nil {
			value, exists = port.Default, true
		}
		if !exists {
			bound.Open[input] = port
			value = port.Type.substitute(types)
		}
		unchecked, err := match(value, port.Type, types)
		if err != nil {
			bound.Problems = append(bound.Problems, Problem{step, prefix + input, err.Error()})
		}
		if exists {
			bound.Values[input] = value
			if unchecked && port.Type.substitute(types).Name == "node" {
				bound.Unchecked = append(bound.Unchecked, prefix+input)
			}
		}
	}
	for _, input := range keys(ports) {
		port := ports[input]
		want := port.Type.substitute(types)
		value, exists := bound.Values[input]
		if !exists {
			continue
		}
		if port.NoOutsideEffect {
			if effect := outsideEffect(value, want); effect != "" {
				bound.Problems = append(bound.Problems, Problem{step, prefix + input, "requires a node with no outside effect: " + effect + " has an outside effect or is unmarked"})
			}
		}
		bound.Values[input], _ = walk(value, want, valuePath{prefix + input}, func(value any, want Type, _ valuePath) (any, error) {
			if node, ok := value.(*Step); ok {
				copy := *node
				if want.Name == "any" {
					want = typeOf(node)
				}
				copy.contract = &want
				return &copy, nil
			}
			return value, nil
		})
	}
	return bound
}

func outsideEffect(value any, want Type) string {
	effect := ""
	var inspect func(any, Type, valuePath) (any, error)
	inspect = func(value any, want Type, path valuePath) (any, error) {
		switch v := value.(type) {
		case *Step:
			if effect == "" {
				effect = v.plan.effect
			}
		case Type:
			if v.Name == "any" || v.Name == "object" && v.Fields == nil {
				v = want
			}
			if v.Name == "node" {
				if effect == "" {
					effect = "the node's outside effect is unknown"
				}
			} else if v.Name == "list" || v.Name == "object" && v.Fields != nil {
				_, _ = walk(v.shape(), want, path, inspect)
			}
		}
		return value, nil
	}
	_, _ = walk(value, want, nil, inspect)
	return effect
}

func missing(open Ports, step, prefix string) Problems {
	var problems Problems
	for _, input := range keys(open) {
		problems = append(problems, Problem{step, prefix + input, "required port is missing; supply a value or reference"})
	}
	return problems
}
