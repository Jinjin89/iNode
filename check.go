package inode

import (
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
	steps map[string]*plan
	deps  map[string]map[string]string
}

type checker struct {
	project *Project
	plans   map[string]*plan
	active  map[string]bool
	report  CheckReport
}

func (p *Project) Check(name string) CheckReport {
	_, report := p.check(name)
	return report
}

func (p *Project) check(name string) (*plan, CheckReport) {
	name = strings.TrimSuffix(name, ".flow")
	c := checker{project: p, plans: map[string]*plan{}, active: map[string]bool{}, report: CheckReport{Problems: []Problem{}, Unchecked: []Problem{}}}
	root := c.compile(name, name)
	return root, c.report
}

func (c *checker) problem(step, input, message string) {
	c.report.Problems = append(c.report.Problems, Problem{step, input, message})
}

func (c *checker) compile(name, location string) *plan {
	if c.active[name] {
		c.problem(location, "use", fmt.Sprintf("workflow %q calls itself; remove this workflow dependency", name))
		return nil
	}
	if p, ok := c.plans[name]; ok {
		return p
	}
	node, ok := c.project.nodes[name]
	if !ok {
		c.problem(location, "use", fmt.Sprintf("unknown node %q; choose a name from inode nodes", name))
		return nil
	}
	if node.problem != nil {
		var problem Problem
		if errors.As(node.problem, &problem) {
			problem.Step = location
			c.report.Problems = append(c.report.Problems, problem)
		} else {
			c.problem(location, "use", node.problem.Error())
		}
		return nil
	}
	p := &plan{Node: node, steps: map[string]*plan{}, deps: map[string]map[string]string{}}
	c.plans[name], c.active[name] = p, true
	defer delete(c.active, name)
	if node.Workflow == nil {
		return p
	}
	flow := node.Workflow
	p.Outputs = Ports{}
	p.NoOutsideEffect = true
	for _, id := range keys(flow.Steps) {
		step := flow.Steps[id]
		if !portName(id) || id == "inputs" {
			c.problem(location+"/"+id, "use", "rename the step without dots or equals signs, and do not use the reserved name inputs")
		}
		p.deps[id] = map[string]string{}
		p.steps[id] = c.compile(strings.TrimSuffix(step.Use, ".flow"), location+"/"+id)
		if p.steps[id] == nil || !p.steps[id].NoOutsideEffect {
			p.NoOutsideEffect = false
		}
	}
	tree := Values{"inputs": p.Inputs.tree()}
	for id, child := range p.steps {
		tree[id] = anyType
		if child != nil {
			tree[id] = child.Outputs.tree()
		}
	}
	expression := func(value any, step, input string) any {
		where := location
		if step != "" {
			where += "/" + step
		}
		selected, err := resolve(value, input, tree, func(parts []string, path string, selected any) {
			if step != "" && parts[0] != "inputs" {
				p.deps[step][parts[0]] = input
			}
			if typ, ok := selected.(Type); ok && typ.unchecked() {
				c.report.Unchecked = append(c.report.Unchecked, Problem{where, path, "contents are unknown until execution; the path and consumer's expected type will be checked then"})
			}
		})
		if err != nil {
			c.problem(where, input, err.Error())
			return anyType
		}
		return selected
	}
	for _, id := range keys(flow.Steps) {
		step := flow.Steps[id]
		for _, dependency := range step.After {
			if _, ok := flow.Steps[dependency]; !ok {
				c.problem(location+"/"+id, "after", fmt.Sprintf("unknown step %q; choose an existing step", dependency))
			} else {
				p.deps[id][dependency] = "after"
			}
		}
		values := Values{}
		for _, input := range keys(step.In) {
			values[input] = expression(step.In[input], id, input)
		}
		if child := p.steps[id]; child != nil {
			_, problems := bind(child.Inputs, values, location+"/"+id, "")
			c.report.Problems = append(c.report.Problems, problems...)
		}
	}
	for _, output := range keys(flow.Outputs) {
		if !portName(output) {
			c.problem(location, "outputs."+output, "rename the output without dots or equals signs")
		}
		p.Outputs[output] = Port{Type: typeOf(expression(flow.Outputs[output], "", "outputs."+output))}
	}
	colors := map[string]int{}
	var visit func(string)
	visit = func(id string) {
		colors[id] = 1
		for _, dependency := range keys(p.deps[id]) {
			if colors[dependency] == 1 {
				c.problem(location+"/"+id, p.deps[id][dependency], fmt.Sprintf("dependency on %q makes a cycle; remove that reference or after entry", dependency))
			} else if colors[dependency] == 0 {
				visit(dependency)
			}
		}
		colors[id] = 2
	}
	for _, id := range keys(flow.Steps) {
		if colors[id] == 0 {
			visit(id)
		}
	}
	return p
}

func portName(name string) bool {
	return name != "" && !strings.ContainsAny(name, ".=")
}

func bind(ports Ports, supplied Values, step, prefix string) (Values, Problems) {
	values := Values{}
	var problems Problems
	for _, input := range keys(supplied) {
		if _, ok := ports[input]; !ok {
			problems = append(problems, Problem{step, prefix + input, fmt.Sprintf("unknown port; remove it or choose one of %v", keys(ports))})
		}
		values[input] = supplied[input]
	}
	for _, input := range keys(ports) {
		port := ports[input]
		value, exists := values[input]
		if !exists && port.Default != nil {
			value, exists = port.Default, true
			values[input] = value
		}
		if !exists {
			problems = append(problems, Problem{step, prefix + input, "required port is missing; supply a value or reference"})
			continue
		}
		err := match(value, port.Type)
		if err != nil {
			problems = append(problems, Problem{step, prefix + input, err.Error()})
		}
	}
	return values, problems
}
