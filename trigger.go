package inode

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
)

type Trigger struct {
	Name        string    `json:"name"`
	Workflow    *Workflow `json:"workflow,omitempty"`
	State       string    `json:"state"`
	Current     *Record   `json:"current,omitempty"`
	Waiting     int       `json:"waiting"`
	Last        *Record   `json:"last,omitempty"`
	LastFailure *Record   `json:"last_failure,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type triggerService struct {
	backend *backend
	mu      sync.Mutex
	on      map[string]bool
	active  map[string]*triggerActivation
	last    time.Time
}

type triggerActivation struct {
	cancel context.CancelFunc
	error  string
}

func (p *Project) triggerFile(name string) (string, error) {
	if name == "" || !filepath.IsLocal(name) || filepath.Base(name) != name {
		return "", fmt.Errorf("choose a trigger name from inode trigger list")
	}
	return filepath.Join(p.dir, "triggers", name+".flow"), nil
}

func (p *Project) triggerDefinitions() ([]Trigger, error) {
	entries, err := os.ReadDir(filepath.Join(p.dir, "triggers"))
	if os.IsNotExist(err) {
		return []Trigger{}, nil
	}
	if err != nil {
		return nil, err
	}
	rows := []Trigger{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".flow") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".flow")
		path, _ := p.triggerFile(name)
		row := Trigger{Name: name, State: "off", Workflow: &Workflow{}}
		if err := readYAML(path, row.Workflow); err != nil {
			row.Error, row.Workflow = err.Error(), nil
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (p *Project) triggerPlan(name string, flow *Workflow) (*plan, Values, error) {
	if len(flow.Steps) != 2 || flow.Steps["event"].Use.Name == "" || flow.Steps["run"].Use == (Definition{}) {
		return nil, nil, fmt.Errorf("trigger %q: use two steps named event and run", name)
	}
	if !slices.Contains(flow.Steps["run"].After, "event") {
		return nil, nil, Problem{Step: "run", Input: "after", Message: "add event to after so the event is read before the run starts"}
	}
	step, bound, report := p.prepare(Definition{Workflow: flow}, nil)
	report.Problems = append(report.Problems, missing(bound.Open, name, "")...)
	if len(report.Problems) != 0 {
		return nil, nil, Problems(report.Problems)
	}
	if step.plan.Workflow.Steps["event"].plan.watch == nil {
		return nil, nil, Problem{Step: "event", Input: "use", Message: "choose a trigger kind from inode nodes"}
	}
	return step.plan, Values{"inputs": bound.Values}, nil
}

func (b *backend) project() (*Project, error) {
	p, err := Open(b.dir)
	if err == nil {
		p.mu, p.publish = &b.mu, b.publish
	}
	return p, err
}

func (s *triggerService) background(ctx context.Context, entry *triggerActivation, run func() error) error {
	b := s.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		if err := run(); err != nil {
			s.mu.Lock()
			defer s.mu.Unlock()
			if ctx.Err() == nil {
				entry.error = err.Error()
				entry.cancel()
			}
		}
	}()
	return nil
}

func (b *backend) openTriggers(p *Project, runs []Record) error {
	s := &triggerService{backend: b, on: map[string]bool{}, active: map[string]*triggerActivation{}}
	b.triggers = s
	if err := readYAML(filepath.Join(b.dir, ".inode", "triggers.json"), &s.on); err != nil && !os.IsNotExist(err) {
		return err
	}
	p.mu, p.publish = &b.mu, b.publish
	for i := range runs {
		if runs[i].Created.After(s.last) {
			s.last = runs[i].Created
		}
		if runs[i].pending() {
			b.publish(&runs[i])
		}
	}
	rows, err := p.triggerDefinitions()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		if s.on[row.Name] {
			fmt.Fprintf(os.Stderr, "trigger %s: events missed while the backend was stopped are skipped\n", row.Name)
			if err := s.activate(p, row.Name); err != nil {
				fmt.Fprintf(os.Stderr, "trigger %s: %s\n", row.Name, err)
			}
		}
	}
	return nil
}

func (s *triggerService) state(name string, on bool) error {
	next := maps.Clone(s.on)
	if on {
		next[name] = true
	} else {
		delete(next, name)
	}
	data, _ := json.Marshal(next)
	if err := writeFile(filepath.Join(s.backend.dir, ".inode", "triggers.json"), data, 0600, true); err != nil {
		return err
	}
	s.on = next
	return nil
}

func (s *triggerService) activate(p *Project, name string) (err error) {
	if entry := s.active[name]; entry != nil {
		entry.cancel()
	}
	ctx, cancel := context.WithCancel(s.backend.ctx)
	entry := &triggerActivation{cancel: cancel}
	s.active[name] = entry
	defer func() {
		if err != nil {
			entry.error = err.Error()
			cancel()
		}
	}()
	path, err := p.triggerFile(name)
	var flow Workflow
	if err == nil {
		err = readYAML(path, &flow)
	}
	var plan *plan
	var tree Values
	if err == nil {
		plan, tree, err = p.triggerPlan(name, &flow)
	}
	var listen watch
	if err == nil {
		source := plan.Workflow.Steps["event"]
		var settings Values
		settings, err = source.arguments(tree, "event")
		if err == nil {
			listen, err = source.plan.watch(ctx, settings)
		}
	}
	if err != nil {
		return err
	}
	if err := s.state(name, true); err != nil {
		return err
	}
	wake := make(chan struct{}, 1)
	if err := s.background(ctx, entry, func() error { return s.loop(ctx, name, wake) }); err != nil {
		return err
	}
	return s.background(ctx, entry, func() error {
		return listen(func(event Values) error {
			return s.accept(ctx, p, name, plan, tree, event, wake)
		})
	})
}

func (s *triggerService) accept(ctx context.Context, p *Project, name string, plan *plan, tree Values, event Values, wake chan<- struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := normalise(event)
	if err != nil {
		return err
	}
	data, err = p.files(data, typeOf(plan.Workflow.Steps["event"].plan.Outputs.tree()), takeFiles)
	if err != nil {
		return err
	}
	bound := bind(plan.Workflow.Steps["event"].plan.Outputs, data.(Values), "event", "", nil)
	problems := append(bound.Problems, missing(bound.Open, "event", "")...)
	if len(problems) > 0 {
		return problems
	}
	tree = Values{"inputs": tree["inputs"], "event": bound.Values}
	target := plan.Workflow.Steps["run"]
	inputs, err := target.arguments(tree, "run")
	if err != nil {
		return err
	}
	target, err = p.acceptInputs(target, inputs)
	if err != nil {
		return err
	}
	record := newRun(target.Use, target.In, name+" (inline)")
	record.Trigger = name
	if !record.Created.After(s.last) {
		record.Created = s.last.Add(time.Nanosecond)
	}
	s.last = record.Created
	e := execution{project: p, root: record}
	if err := e.update(record, nil, true, func() {}); err != nil {
		return err
	}
	select {
	case wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *triggerService) next(name string) (*Project, string, error) {
	b := s.backend
	b.mu.Lock()
	var record *Record
	for _, run := range b.active {
		r := run.record
		if r.Trigger != name {
			continue
		}
		if !r.pending() {
			record = r
			break
		}
		if record == nil || r.Created.Before(record.Created) {
			record = r
		}
	}
	wait := record != nil && !record.pending()
	b.mu.Unlock()
	if record == nil {
		return nil, "", nil
	}
	p, err := b.project()
	if err != nil || wait {
		return p, record.ID, err
	}
	e, err := p.begin(record)
	if err == nil {
		id, err := b.launch(e)
		return p, id, err
	}
	failed := execution{project: p, root: record}
	problem := err
	err = failed.update(record, nil, false, func() {
		record.fail(problem)
		record.Finished = time.Now().UTC()
		if node := p.lookup(record.Node); !record.Unsaved && node.problem == nil {
			record.Workflow = node.Workflow
		}
	})
	return p, record.ID, err
}

func (s *triggerService) loop(ctx context.Context, name string, wake <-chan struct{}) error {
	for {
		s.mu.Lock()
		var id string
		err := ctx.Err()
		var p *Project
		if err == nil {
			p, id, err = s.next(name)
		}
		s.mu.Unlock()
		if err == nil && id != "" {
			err = s.backend.followRun(ctx, p, id, func(any) error { return nil })
		}
		if err != nil {
			return err
		}
		if id != "" {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}

func (b *backend) triggerList(p *Project) ([]Trigger, error) {
	s := b.triggers
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := p.triggerDefinitions()
	if err != nil {
		return nil, err
	}
	runs, err := p.Runs()
	if err != nil {
		return nil, err
	}
	for i := range rows {
		row := &rows[i]
		if s.on[row.Name] {
			row.State = "on"
		}
		if entry := s.active[row.Name]; entry != nil && entry.error != "" {
			row.Error = entry.error
		}
		for j := range runs {
			run := &runs[j]
			if run.Trigger != row.Name {
				continue
			}
			if run.pending() {
				row.Waiting++
			} else if run.State == "running" || run.State == "waiting" {
				row.Current = run
			} else {
				if row.Last == nil {
					row.Last = run
				}
				if row.LastFailure == nil && (run.State == "failed" || run.State == "interrupted") {
					row.LastFailure = run
				}
			}
		}
	}
	return rows, nil
}

func (b *backend) addTrigger(p *Project, flow Workflow) (string, error) {
	s := b.triggers
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := p.triggerPlan("new", &flow); err != nil {
		return "", err
	}
	rows, err := p.triggerDefinitions()
	if err != nil {
		return "", err
	}
	used := map[string]bool{}
	for _, row := range rows {
		used[row.Name] = true
	}
	runs, err := p.Runs()
	if err != nil {
		return "", err
	}
	for _, run := range runs {
		used[run.Trigger] = true
	}
	var name string
	for n := 1; ; n++ {
		name = fmt.Sprintf("%s-%d", flow.Steps["event"].Use.Name, n)
		if !used[name] {
			break
		}
	}
	if err := os.MkdirAll(filepath.Join(p.dir, "triggers"), 0755); err != nil {
		return "", err
	}
	raw, err := jsonData(any(flow))
	var data []byte
	if err == nil {
		data, err = yaml.Marshal(raw)
	}
	if err == nil {
		path, _ := p.triggerFile(name)
		err = writeFile(path, data, 0644, false)
	}
	return name, err
}

func (b *backend) setTrigger(p *Project, name string, on bool) error {
	s := b.triggers
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.switchOn(p, name, on)
}

func (s *triggerService) switchOn(p *Project, name string, on bool) error {
	path, err := p.triggerFile(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	if on {
		return s.activate(p, name)
	}
	if err := s.state(name, false); err != nil {
		return err
	}
	if entry := s.active[name]; entry != nil {
		entry.cancel()
		delete(s.active, name)
	}
	return nil
}

func (s *triggerService) clear(p *Project, name string) error {
	b := s.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, active := range b.active {
		if active.record.Trigger == name && active.record.pending() {
			if err := removeFile(p.runFile(id)); err != nil {
				return err
			}
			delete(b.active, id)
			close(active.changed)
		}
	}
	return nil
}

func (b *backend) clearTrigger(p *Project, name string) error {
	b.triggers.mu.Lock()
	defer b.triggers.mu.Unlock()
	return b.triggers.clear(p, name)
}

func (b *backend) removeTrigger(p *Project, name string) error {
	s := b.triggers
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.switchOn(p, name, false); err != nil {
		return err
	}
	path, _ := p.triggerFile(name)
	if err := os.Remove(path); err != nil {
		return err
	}
	return s.clear(p, name)
}
