package inode

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Record struct {
	ID       string             `json:"id,omitempty"`
	Node     string             `json:"node"`
	Unsaved  bool               `json:"unsaved,omitempty"`
	State    string             `json:"state"`
	Trigger  string             `json:"trigger,omitempty"`
	Source   *RunSource         `json:"source,omitempty"`
	Created  time.Time          `json:"created,omitzero"`
	Started  time.Time          `json:"started,omitzero"`
	Finished time.Time          `json:"finished,omitzero"`
	Inputs   Values             `json:"inputs"`
	Outputs  Values             `json:"outputs"`
	Error    string             `json:"error,omitempty"`
	Steps    map[string]*Record `json:"steps,omitempty"`
	Workflow *Workflow          `json:"workflow,omitempty"`
}

func (r Record) pending() bool {
	return r.Trigger != "" && r.State == "waiting" && r.Started.IsZero()
}

func newRun(use Definition, inputs Values, name string) *Record {
	if use.Workflow == nil {
		name = strings.TrimSuffix(use.Name, ".flow")
	} else if name == "" {
		name = "Unsaved workflow"
	}
	return &Record{ID: rand.Text(), Node: name, Unsaved: use.Workflow != nil, Workflow: use.Workflow,
		State: "waiting", Created: time.Now().UTC(), Inputs: inputs}
}

func (r *Record) fail(err error) {
	r.State, r.Error = "failed", err.Error()
	if err == context.Canceled || err == context.DeadlineExceeded {
		r.State, r.Error = "interrupted", "run interrupted; outcome unknown: "+err.Error()
	}
}

func newRecord(p *plan) *Record {
	r := &Record{Node: p.Name, Workflow: p.Workflow, State: "waiting", Steps: map[string]*Record{}}
	if p.Workflow != nil {
		for id, child := range p.Workflow.Steps {
			r.Steps[id] = newRecord(child.plan)
		}
	}
	return r
}

type Call func(context.Context, *Step, any) (any, error)

type runFailure struct {
	step string
	err  error
}

func (f *runFailure) Error() string { return fmt.Sprintf("step %q: %s", f.step, f.err) }
func (f *runFailure) Unwrap() error { return f.err }

type execution struct {
	project *Project
	root    *Record
	step    Step
}

func (e *execution) update(record *Record, err error, durable bool, change func()) error {
	e.project.mu.Lock()
	defer e.project.mu.Unlock()
	change()
	if err != nil {
		record.fail(err)
	}
	if saveErr := e.project.save(e.root, durable); saveErr != nil {
		record.fail(saveErr)
		err = saveErr
	}
	if e.project.publish != nil {
		e.project.publish(e.root)
	}
	return err
}

func (p *Project) Start(ctx context.Context, name string, inputs Values) (*Record, error) {
	e, err := p.begin(newRun(Definition{Name: name}, inputs, ""))
	if e == nil {
		return nil, err
	}
	if err == nil {
		err = e.run(ctx)
	}
	return e.root, err
}

func (p *Project) begin(queued *Record) (*execution, error) {
	use := Definition{Name: queued.Node}
	if queued.Unsaved {
		use = Definition{Workflow: queued.Workflow}
	}
	data, err := normalise(queued.Inputs)
	if err != nil {
		return nil, fmt.Errorf("inputs: use JSON-compatible values: %w", err)
	}
	inputs, _ := data.(Values)
	step, bound, report := p.prepare(use, inputs)
	report.Problems = append(report.Problems, missing(bound.Open, use.Name, "")...)
	if len(report.Problems) > 0 {
		return nil, Problems(report.Problems)
	}
	*step, err = p.acceptInputs(*step, bound.Values)
	if err != nil {
		return nil, err
	}
	record := newRecord(step.plan)
	record.ID, record.Node, record.Unsaved = queued.ID, queued.Node, queued.Unsaved
	record.Created, record.Trigger = queued.Created, queued.Trigger
	record.Source = queued.Source
	e := &execution{project: p, root: record, step: *step}
	err = e.update(record, nil, false, func() { record.Inputs = step.In })
	return e, err
}

func (e *execution) run(ctx context.Context) error {
	return e.execute(ctx, e.step, e.root, e.step.plan.Name)
}

func (e *execution) start(node Step, record *Record) error {
	if !record.Started.IsZero() {
		return nil
	}
	return e.update(record, nil, record == e.root && record.pending(), func() {
		record.State, record.Started, record.Inputs = "running", time.Now().UTC(), node.In
	})
}

func (e *execution) execute(ctx context.Context, node Step, record *Record, location string) error {
	data, inputErr := e.project.files(node.In, typeOf(node.plan.Inputs.tree()), takeFiles)
	if inputErr == nil {
		node.In, _ = data.(Values)
	}
	plan, inputs := node.plan, node.In
	err := e.start(node, record)
	if err == nil {
		err = inputErr
	}
	var outputs Values
	scrub := func(value any) any { return value }
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		if plan.Workflow != nil {
			outputs, err = e.workflow(ctx, plan, record, inputs, location)
		} else {
			var supplied Values
			supplied, scrub, err = e.project.secretInputs(inputs, e.project.nodes[plan.Name].Inputs)
			if err == nil {
				outputs, err = plan.run(ctx, supplied, func(ctx context.Context, node *Step, value any) (any, error) {
					return e.call(ctx, record, node, location, value)
				})
			}
		}
	}
	if outputs != nil {
		data, dataErr := normalise(outputs)
		outputs, _ = scrub(data).(Values)
		if dataErr != nil {
			err = fmt.Errorf("node %q must return JSON-compatible outputs: %w", plan.Name, dataErr)
		}
	}
	if err == nil {
		data, fileErr := e.project.files(outputs, typeOf(plan.Outputs.tree()), returnedFiles)
		outputs, _ = data.(Values)
		err = fileErr
	}
	if err == nil {
		bound := bind(plan.Outputs, outputs, location, "outputs.", nil)
		outputs = bound.Values
		problems := append(bound.Problems, missing(bound.Open, location, "outputs.")...)
		if len(problems) > 0 {
			err = problems
		}
		if err == nil && node.contract != nil {
			port := keys(plan.Outputs)[0]
			bound = bind(Ports{port: {Type: node.contract.Fields["output"]}}, outputs, location, "outputs.", nil)
			outputs = bound.Values
			if len(bound.Problems) > 0 {
				err = bound.Problems
			}
		}
	}
	if err != nil {
		if message := scrub(err.Error()).(string); message != err.Error() {
			err = errors.New(message)
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		outputs = nil
	}
	err = e.update(record, err, false, func() {
		record.State, record.Finished, record.Outputs = "done", time.Now().UTC(), outputs
	})
	var failed *runFailure
	if err != nil && !errors.As(err, &failed) {
		err = &runFailure{location, err}
	}
	return err
}

func (e *execution) call(ctx context.Context, parent *Record, node *Step, location string, value any) (any, error) {
	inputs := Values{}
	for name, value := range node.In {
		inputs[name] = value
	}
	var open string
	for name, port := range node.plan.Inputs {
		if _, filled := inputs[name]; !filled && port.required() {
			inputs[name], open = value, name
		}
	}
	child := newRecord(node.plan)
	var id string
	if err := e.update(parent, nil, false, func() { id = strconv.Itoa(len(parent.Steps) + 1); parent.Steps[id] = child }); err != nil {
		return nil, err
	}
	where := location + "/" + id
	if input, exists := node.contract.Fields["open input"]; exists {
		bound := bind(Ports{open: {Type: input}}, Values{open: value}, where, "", nil)
		if len(bound.Problems) > 0 {
			return nil, e.update(child, &runFailure{where, bound.Problems}, false, func() { child.Inputs = inputs })
		}
		inputs[open] = bound.Values[open]
	}
	bound := bind(node.plan.Inputs, inputs, where, "", nil)
	if len(bound.Problems) > 0 {
		return nil, e.update(child, &runFailure{where, bound.Problems}, false, func() { child.Inputs = inputs })
	}
	call := *node
	call.In = bound.Values
	if err := e.execute(ctx, call, child, where); err != nil {
		return nil, err
	}
	return child.Outputs[keys(node.plan.Outputs)[0]], nil
}

func (e *execution) workflow(ctx context.Context, plan *plan, record *Record, inputs Values, location string) (Values, error) {
	type completion struct {
		step string
		err  error
	}
	finished := make(chan completion, len(plan.Workflow.Steps))
	waiting := maps.Clone(plan.Workflow.Steps)
	values := Values{"inputs": inputs}
	running := 0
	var failure error
	for {
		if ctx.Err() != nil {
			failure = ctx.Err()
		}
		for _, id := range keys(waiting) {
			if failure != nil {
				break
			}
			ready := true
			for dependency := range plan.deps[id] {
				_, done := values[dependency]
				ready = ready && done
			}
			if !ready {
				continue
			}
			arguments, err := plan.Workflow.Steps[id].arguments(values, location+"/"+id)
			if err != nil {
				failure = e.update(record.Steps[id], &runFailure{location + "/" + id, err}, false, func() { record.Steps[id].Inputs = arguments })
				break
			}
			delete(waiting, id)
			running++
			go func() {
				step := plan.Workflow.Steps[id]
				step.In = arguments
				finished <- completion{id, e.execute(ctx, step, record.Steps[id], location+"/"+id)}
			}()
		}
		if running == 0 {
			break
		}
		result := <-finished
		running--
		if result.err != nil && failure == nil {
			failure = result.err
		}
		if result.err == nil {
			values[result.step] = record.Steps[result.step].Outputs
		}
	}
	if failure != nil {
		return nil, failure
	}
	outputs, _, err := resolve(plan.Workflow.Outputs, anyType, valuePath{"outputs"}, values)
	result, _ := outputs.(map[string]any)
	return result, err
}

func (node Step) arguments(tree Values, location string) (Values, error) {
	resolved, _, err := resolve(node.In, typeOf(node.plan.Inputs.tree()), nil, tree)
	if err != nil {
		return nil, err
	}
	arguments, _ := resolved.(Values)
	bound := bind(node.plan.Inputs, arguments, location, "", nil)
	problems := append(bound.Problems, missing(bound.Open, location, "")...)
	if len(problems) > 0 {
		return bound.Values, problems
	}
	return bound.Values, nil
}

func (p *Project) save(record *Record, durable bool) error {
	dir := filepath.Join(p.dir, ".inode", "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(p.runFile(record.ID), data, 0600, durable)
}

func (p *Project) runFile(id string) string {
	return filepath.Join(p.dir, ".inode", "runs", id+".json")
}

func (p *Project) ShowRun(id string) (*Record, error) {
	if !filepath.IsLocal(id) || filepath.Base(id) != id {
		return nil, fmt.Errorf("invalid run id %q; choose an id from inode runs", id)
	}
	data, err := os.ReadFile(p.runFile(id))
	if err != nil {
		return nil, err
	}
	var record Record
	err = json.Unmarshal(data, &record)
	return &record, err
}

func (p *Project) Runs() ([]Record, error) {
	entries, err := os.ReadDir(filepath.Join(p.dir, ".inode", "runs"))
	if os.IsNotExist(err) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	runs := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		run, err := p.ShowRun(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}
	created := func(r Record) time.Time {
		if r.Created.IsZero() {
			return r.Started
		}
		return r.Created
	}
	slices.SortFunc(runs, func(a, b Record) int { return created(b).Compare(created(a)) })
	return runs, nil
}
