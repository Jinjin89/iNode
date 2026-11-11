package inode

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

type Record struct {
	ID       string             `json:"id,omitempty"`
	Node     string             `json:"node"`
	State    string             `json:"state"`
	Started  time.Time          `json:"started,omitzero"`
	Finished time.Time          `json:"finished,omitzero"`
	Inputs   Values             `json:"inputs"`
	Outputs  Values             `json:"outputs"`
	Error    string             `json:"error,omitempty"`
	Steps    map[string]*Record `json:"steps,omitempty"`
	Workflow *Workflow          `json:"workflow,omitempty"`
}

func newRecord(p *plan) *Record {
	r := &Record{Node: p.Name, Workflow: p.Workflow, State: "waiting", Steps: map[string]*Record{}}
	for id, child := range p.steps {
		r.Steps[id] = newRecord(child)
	}
	return r
}

type execution struct {
	project *Project
	root    *Record
	mu      sync.Mutex
}

func (e *execution) update(record *Record, err error, change func()) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	change()
	fail := func(err error) {
		record.State, record.Error = "failed", err.Error()
		if err == context.Canceled || err == context.DeadlineExceeded {
			record.State, record.Error = "interrupted", "run interrupted; outcome unknown: "+err.Error()
		}
	}
	if err != nil {
		fail(err)
	}
	if saveErr := e.project.save(e.root); saveErr != nil {
		fail(saveErr)
		return saveErr
	}
	return err
}

func (p *Project) Start(ctx context.Context, name string, inputs Values) (*Record, error) {
	plan, report := p.check(name)
	if len(report.Problems) > 0 {
		return nil, Problems(report.Problems)
	}
	inputs, err := jsonData(inputs)
	if err != nil {
		return nil, fmt.Errorf("%s inputs: use JSON-compatible values: %w", name, err)
	}
	inputs, problems := bind(plan.Inputs, inputs, name, "")
	if len(problems) > 0 {
		return nil, problems
	}
	record := newRecord(plan)
	record.ID = rand.Text()
	e := execution{project: p, root: record}
	return record, e.execute(ctx, plan, record, inputs)
}

func (e *execution) execute(ctx context.Context, plan *plan, record *Record, inputs Values) error {
	err := e.update(record, nil, func() {
		record.State, record.Started, record.Inputs = "running", time.Now().UTC(), inputs
	})
	var outputs Values
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		if plan.Workflow != nil {
			outputs, err = e.workflow(ctx, plan, record, inputs)
		} else {
			outputs, err = plan.run(ctx, inputs)
		}
	}
	if outputs != nil {
		var dataErr error
		outputs, dataErr = jsonData(outputs)
		if dataErr != nil {
			err = fmt.Errorf("node %q must return JSON-compatible outputs: %w", plan.Name, dataErr)
		}
	}
	if err == nil {
		if _, problems := bind(plan.Outputs, outputs, plan.Name, "outputs."); len(problems) > 0 {
			err = problems
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return e.update(record, err, func() {
		record.State, record.Finished, record.Outputs = "done", time.Now().UTC(), outputs
	})
}

func (e *execution) workflow(ctx context.Context, plan *plan, record *Record, inputs Values) (Values, error) {
	type completion struct {
		step string
		err  error
	}
	finished := make(chan completion, len(plan.steps))
	waiting := maps.Clone(plan.steps)
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
			resolved, err := resolve(plan.Workflow.Steps[id].In, plan.Name+"/"+id, values, nil)
			arguments, _ := resolved.(map[string]any)
			if err == nil {
				var problems Problems
				arguments, problems = bind(plan.steps[id].Inputs, arguments, plan.Name+"/"+id, "")
				if len(problems) > 0 {
					err = problems
				}
			}
			if err != nil {
				failure = e.update(record.Steps[id], err, func() { record.Steps[id].Inputs = arguments })
				break
			}
			delete(waiting, id)
			running++
			go func() {
				finished <- completion{id, e.execute(ctx, plan.steps[id], record.Steps[id], arguments)}
			}()
		}
		if running == 0 {
			break
		}
		result := <-finished
		running--
		if result.err != nil && failure == nil {
			failure = fmt.Errorf("step %q: %w", plan.Name+"/"+result.step, result.err)
		}
		if result.err == nil {
			values[result.step] = record.Steps[result.step].Outputs
		}
	}
	if failure != nil {
		return nil, failure
	}
	outputs, err := resolve(plan.Workflow.Outputs, plan.Name+"/outputs", values, nil)
	result, _ := outputs.(map[string]any)
	return result, err
}

func (p *Project) save(record *Record) error {
	dir := filepath.Join(p.dir, ".inode", "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, record.ID+".json")
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
		return err
	}
	defer os.Remove(path + ".tmp")
	return os.Rename(path+".tmp", path)
}

func (p *Project) ShowRun(id string) (*Record, error) {
	if !filepath.IsLocal(id) || filepath.Base(id) != id {
		return nil, fmt.Errorf("invalid run id %q; choose an id from inode runs", id)
	}
	data, err := os.ReadFile(filepath.Join(p.dir, ".inode", "runs", id+".json"))
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
	slices.SortFunc(runs, func(a, b Record) int { return b.Started.Compare(a.Started) })
	return runs, nil
}
