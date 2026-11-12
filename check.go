package inode

import (
	"encoding/json"
	"errors"
	"fmt"
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
	Problems  []Problem `json:"problems"`
	Unchecked []Problem `json:"unchecked"`
}

type plan struct {
	Node
	deps   map[string]map[string]bool
	effect string
}

type checker struct {
	project *Project
	active  map[string]bool
	report  CheckReport
}

func (p *Project) Check(name string) CheckReport { _, report := p.check(name); return report }
func (p *Project) check(name string) (*plan, CheckReport) {
	step, _, report := p.prepare(name, nil)
	return step.plan, report
}

func (p *Project) prepare(name string, inputs Values) (*Step, binding, CheckReport) {
	name = strings.TrimSuffix(name, ".flow")
	c := checker{project: p, active: map[string]bool{}, report: CheckReport{Problems: []Problem{}, Unchecked: []Problem{}}}
	step := &Step{Use: Definition{Name: name}, In: inputs}
	bound, _ := c.compile(step, name, nil)
	return step, bound, c.report
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

func (c *checker) compile(step *Step, location string, tree Values) (binding, []reference) {
	name := strings.TrimSuffix(step.Use.Name, ".flow")
	node := c.project.lookup(name)
	if step.Use.Workflow != nil {
		flow := step.Use.Workflow
		node = Node{Name: "(inline)", Doc: flow.Doc, Inputs: flow.Inputs, Workflow: flow}
	}
	if node.problem != nil {
		c.error(location, "use", node.problem)
		return binding{}, nil
	}
	if node.Workflow != nil && name != "" {
		if c.active[name] {
			c.problem(location, "use", fmt.Sprintf("workflow %q calls itself; remove this workflow dependency", name))
			return binding{}, nil
		}
		c.active[name] = true
		defer delete(c.active, name)
	}
	p := &plan{Node: node, deps: map[string]map[string]bool{}}
	step.plan = p
	p.Inputs = Ports{}
	for name, port := range node.Inputs {
		if port.Default != nil {
			port.Default = c.expression(port.Default, port.Type, location, name, nil)
		}
		p.Inputs[name] = port
	}
	parsed, supplied := Values{}, Values{}
	var refs []reference
	for _, input := range keys(step.In) {
		want := anyType
		if port, exists := p.Inputs[input]; exists {
			want = port.Type
		}
		before := len(c.report.Problems)
		parsed[input] = c.expression(step.In[input], want, location, input, tree)
		supplied[input] = anyType
		if len(c.report.Problems) != before {
			continue
		}
		supplied[input] = parsed[input]
		if tree != nil {
			selected, found, err := resolve(parsed[input], input, tree)
			if err != nil {
				c.error(location, input, err)
			} else {
				supplied[input] = selected
				refs = append(refs, found...)
			}
		}
	}
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
	return bound, refs
}

func (c *checker) workflow(p *plan, location string) {
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
		bound, refs := c.compile(&step, where, tree)
		c.report.Problems = append(c.report.Problems, missing(bound.Open, where, "")...)
		for dependency := range c.references(where, refs) {
			p.deps[id][dependency] = true
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
		parsed := c.expression(original.Outputs[output], anyType, location, "outputs."+output, tree)
		value, refs, err := resolve(parsed, "outputs."+output, tree)
		if err != nil {
			c.error(location, "outputs."+output, err)
			value = anyType
		}
		c.references(location, refs)
		flow.Outputs[output] = parsed
		p.Outputs[output] = Port{Type: typeOf(value)}
	}
}

func (c *checker) references(location string, refs []reference) map[string]bool {
	deps := map[string]bool{}
	for _, ref := range refs {
		if ref.Step != "inputs" {
			deps[ref.Step] = true
		}
		if ref.Unchecked {
			c.report.Unchecked = append(c.report.Unchecked, Problem{location, ref.Input, "contents are unknown until execution; the path and consumer's expected type will be checked then"})
		}
	}
	return deps
}

func (c *checker) expression(value any, want Type, location, path string, tree Values) any {
	result, err := walk(value, want, path, func(value any, want Type, path string) (any, error) {
		if want.Name == "node" {
			switch v := value.(type) {
			case string:
				if !strings.HasPrefix(v, "=") {
					value = &Step{Use: Definition{Name: v}}
				}
			case map[string]any:
				data, err := json.Marshal(v)
				if err != nil {
					return nil, err
				}
				step := &Step{}
				if err := decode(data, step); err != nil {
					return nil, err
				}
				value = step
			}
		}
		if node, ok := value.(*Step); ok {
			copy := *node
			copy.contract = nil
			node = &copy
			if len(node.After) > 0 {
				return nil, fmt.Errorf("a boxed node is not scheduled; put after on the receiving step")
			}
			bound, _ := c.compile(node, location+"/"+path, tree)
			if node.plan == nil {
				return anyType, nil
			}
			if len(bound.Open) > 1 {
				return nil, fmt.Errorf("a boxed node has at most one open input; fill inputs or expose one object")
			}
			if len(node.plan.Outputs) != 1 {
				return nil, fmt.Errorf("a boxed node must have exactly one output; expose one object to return several values")
			}
			return node, nil
		}
		return value, nil
	})
	if err != nil {
		c.error(location, path, err)
		return anyType
	}
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
		bound.Values[input], _ = walk(value, want, prefix+input, func(value any, want Type, _ string) (any, error) {
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
	var inspect func(any, Type, string) (any, error)
	inspect = func(value any, want Type, path string) (any, error) {
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
	_, _ = walk(value, want, "", inspect)
	return effect
}

func missing(open Ports, step, prefix string) Problems {
	var problems Problems
	for _, input := range keys(open) {
		problems = append(problems, Problem{step, prefix + input, "required port is missing; supply a value or reference"})
	}
	return problems
}
