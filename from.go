package inode

import (
	"encoding/json"
	"maps"
	"strconv"
	"strings"
)

type RunSource struct {
	Run    string              `json:"run"`
	Step   string              `json:"step"`
	Inputs map[string][]string `json:"inputs"`
}

type RunFrom struct {
	Workflow *Workflow `json:"workflow"`
	Inputs   Values    `json:"inputs"`
	Source   RunSource `json:"source"`
}

func (p *Project) From(past *Record, selected string) (RunFrom, error) {
	if past.Workflow == nil || past.Finished.IsZero() {
		return RunFrom{}, Problem{past.Node + "/" + selected, "use", "choose a step in a finished workflow run"}
	}
	use := Definition{Name: past.Node}
	if past.Unsaved {
		use = Definition{Workflow: past.Workflow}
	}
	current, _, report := p.prepare(use, past.Inputs)
	if current.plan == nil {
		return RunFrom{}, Problems(report.Problems)
	}
	result, err := buildFrom(past, current, report, selected, p.nodes)
	if err == nil {
		_, _, checked := p.prepare(Definition{Workflow: result.Workflow}, result.Inputs)
		if len(checked.Problems) > 0 {
			err = Problems(checked.Problems)
		}
	}
	return result, err
}

func buildFrom(past *Record, current *Step, report CheckReport, selected string, catalog map[string]Node) (RunFrom, error) {
	flow := current.plan.Workflow
	where := past.Node + "/" + selected
	if flow == nil {
		return RunFrom{}, Problem{where, "use", "the current node is not a workflow; choose a workflow run"}
	}
	if _, exists := past.Workflow.Steps[selected]; !exists {
		return RunFrom{}, Problem{where, "use", "choose a top-level step from the past workflow, not a call inside a box"}
	}
	if _, exists := flow.Steps[selected]; !exists {
		return RunFrom{}, Problem{where, "use", "this step was removed or renamed; run the workflow first"}
	}
	steps, err := selectPart(current.plan, selected, report.Problems)
	if err != nil {
		return RunFrom{}, err
	}
	result := RunFrom{Workflow: &Workflow{Doc: flow.Doc, Inputs: Ports{}, Steps: steps, Outputs: maps.Clone(flow.Outputs), View: flow.View},
		Inputs: Values{}, Source: RunSource{past.ID, selected, map[string][]string{}}}
	tree := recordedValues(past)
	tree["inputs"] = current.In
	types := Values{"inputs": current.plan.Inputs.tree()}
	for id, step := range flow.Steps {
		if step.plan != nil {
			types[id] = step.plan.Outputs.tree()
		}
	}
	names := map[string]string{}
	for _, ref := range report.References {
		_, kept := steps[ref.Path[1]]
		_, inside := steps[ref.From[0]]
		if ref.Path[0] == "steps" && !kept || inside {
			continue
		}
		if ref.From[0] != "inputs" {
			if old := past.Steps[ref.From[0]]; old == nil || old.State != "done" {
				return RunFrom{}, Problem{ref.Step, ref.Input, "needed value from " + ref.From[0] + " did not come from a completed step; run the workflow first"}
			}
		}
		key := strings.Join(ref.From, ".")
		name := names[key]
		if name == "" {
			value, err := selectPath(tree, ref.From)
			if err != nil {
				return RunFrom{}, Problem{ref.Step, ref.Input, "no recorded value for " + key + "; run the workflow first"}
			}
			typ, _ := selectPath(types, ref.From)
			name = "copy" + strconv.Itoa(len(names)+1)
			names[key], result.Inputs[name], result.Source.Inputs[name] = name, value, ref.From
			result.Workflow.Inputs[name] = Port{Type: typeOf(typ)}
		}
		if ref.Path[0] == "steps" {
			step := steps[ref.Path[1]]
			step.In = setPath(step.In, ref.Path[3:], "=inputs."+name).(Values)
			steps[ref.Path[1]] = step
		} else {
			result.Workflow.Outputs = setPath(result.Workflow.Outputs, ref.Path[1:], "=inputs."+name).(Values)
		}
	}
	if err := verifyOutside(past, current, report, result.Source.Inputs, catalog); err != nil {
		return RunFrom{}, err
	}
	return result, nil
}

func selectPart(p *plan, selected string, problems []Problem) (map[string]Step, error) {
	part := map[string]bool{selected: true}
	for changed := true; changed; {
		changed = false
		for id := range p.Workflow.Steps {
			for dependency := range p.deps[id] {
				if part[dependency] && !part[id] {
					part[id], changed = true, true
				}
			}
		}
	}
	steps := map[string]Step{}
	for _, id := range keys(part) {
		if err := problemAt(problems, p.Name+"/"+id); err != nil {
			return nil, err
		}
		step := p.Workflow.Steps[id]
		for _, dependency := range step.After {
			if !part[dependency] {
				return nil, Problem{p.Name + "/" + id, "after", "after crosses in from " + dependency + "; remove it or run the whole workflow"}
			}
		}
		steps[id] = step
	}
	return steps, nil
}

func verifyOutside(past *Record, current *Step, report CheckReport, sources map[string][]string, catalog map[string]Node) error {
	tree := recordedValues(past)
	tree["inputs"] = current.In
	checked := map[string]bool{"inputs": true}
	var visit func(string) error
	visit = func(id string) error {
		if checked[id] {
			return nil
		}
		checked[id] = true
		step := current.plan.Workflow.Steps[id]
		where := past.Node + "/" + id
		if step.plan == nil {
			return problemAt(report.Problems, current.plan.Name+"/"+id)
		}
		values, err := step.arguments(tree, where)
		if err != nil {
			return err
		}
		before := Step{Use: past.Workflow.Steps[id].Use, plan: step.plan}
		if record := past.Steps[id]; record != nil {
			before.In = record.Inputs
		}
		after := Step{Use: step.Use, In: values, plan: step.plan}
		if !same(snapshot(before, catalog, false), snapshot(after, catalog, false)) {
			return Problem{where, "in", "upstream use or filled-in inputs changed; run the whole workflow first"}
		}
		for _, dependency := range keys(current.plan.deps[id]) {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		return savedWorkflows(past.Steps[id], where, catalog)
	}
	for _, input := range keys(sources) {
		if err := visit(sources[input][0]); err != nil {
			return err
		}
	}
	return nil
}

func savedWorkflows(record *Record, where string, catalog map[string]Node) error {
	if record.Workflow != nil && record.Node != "(inline)" {
		before := Step{Use: Definition{Workflow: record.Workflow}}
		after := Step{Use: Definition{Workflow: catalog[record.Node].Workflow}}
		if !same(snapshot(before, catalog, true), snapshot(after, catalog, true)) {
			return Problem{where, "use", "upstream nested workflow changed; run the whole workflow first"}
		}
	}
	for _, id := range keys(record.Steps) {
		if err := savedWorkflows(record.Steps[id], where+"/"+id, catalog); err != nil {
			return err
		}
	}
	return nil
}

func recordedValues(record *Record) Values {
	tree := Values{"inputs": record.Inputs}
	for id, child := range record.Steps {
		tree[id] = child.Outputs
	}
	return tree
}

func same(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func problemAt(problems []Problem, scope string) error {
	for _, problem := range problems {
		if problem.Step == scope || strings.HasPrefix(problem.Step, scope+"/") {
			return problem
		}
	}
	return nil
}

func snapshot(step Step, catalog map[string]Node, body bool) Step {
	ports := catalog[step.Use.Name].Inputs
	if step.plan != nil {
		ports = step.plan.Inputs
	}
	if step.Use.Workflow != nil {
		flow := *step.Use.Workflow
		flow.View, flow.Steps, flow.Inputs = nil, map[string]Step{}, maps.Clone(flow.Inputs)
		ports = flow.Inputs
		for name, port := range flow.Inputs {
			port.Default = snapshotValue(port.Default, port.Type, catalog, body)
			flow.Inputs[name] = port
		}
		for id, child := range step.Use.Workflow.Steps {
			flow.Steps[id] = snapshot(child, catalog, body)
		}
		if body {
			flow.Doc, flow.Inputs = "", nil
		}
		step.Use.Workflow = &flow
	}
	step.In, _ = snapshotValue(step.In, typeOf(ports.tree()), catalog, body).(Values)
	return step
}

func snapshotValue(value any, want Type, catalog map[string]Node, body bool) any {
	value, _ = walk(value, want, nil, func(value any, want Type, _ valuePath) (any, error) {
		if want.Name == "node" {
			box, _ := nodeValue(value)
			if node, ok := box.(*Step); ok {
				copy := snapshot(*node, catalog, body)
				return &copy, nil
			}
		}
		return value, nil
	})
	return value
}

func setPath(value any, path []string, next any) any {
	if len(path) == 0 {
		return next
	}
	switch value := value.(type) {
	case map[string]any:
		copy := maps.Clone(value)
		copy[path[0]] = setPath(value[path[0]], path[1:], next)
		return copy
	case []any:
		copy := append([]any{}, value...)
		index, _ := strconv.Atoi(path[0])
		copy[index] = setPath(value[index], path[1:], next)
		return copy
	default:
		copy := *value.(*Step)
		copy.In = setPath(copy.In, path[1:], next).(Values)
		return &copy
	}
}
