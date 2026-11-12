package inode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func triggerFlow(kind string, settings Values, node string, inputs Values) Workflow {
	return Workflow{Steps: map[string]Step{
		"event": {Use: Definition{Name: kind}, In: settings},
		"run":   {Use: Definition{Name: node}, In: inputs, After: []string{"event"}},
	}}
}

func addTestTrigger(t *testing.T, client *Client, flow Workflow) string {
	t.Helper()
	var name string
	if err := json.Unmarshal(request(t, client, "trigger add", flow)[0], &name); err != nil {
		t.Fatal(err)
	}
	return name
}

func waitTrigger(t *testing.T, client *Client, name string, ready func(Trigger) bool) Trigger {
	t.Helper()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	var last Trigger
	for {
		var rows []Trigger
		if err := json.Unmarshal(request(t, client, "trigger list")[0], &rows); err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Name == name {
				last = row
				if ready(row) {
					return row
				}
			}
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatalf("trigger did not reach the expected state: %+v", last)
		}
	}
}

func TestTriggerQueue(t *testing.T) {
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var active, maximum atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		entered <- struct{}{}
		select {
		case <-release:
			io.WriteString(w, "ok")
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	job := func(divisor int) string {
		return fmt.Sprintf("inputs: {text: text}\nsteps:\n  gate: {use: http.get, in: {url: %s}}\n  divide: {use: number.divide, in: {a: 1, b: %d}, after: [gate]}\n  upper: {use: text.upper, in: {text: =inputs.text}, after: [divide]}\noutputs: {text: =upper.text}", server.URL, divisor)
	}
	p := fileProject(t, map[string]string{"job.flow": job(0)})
	client := runningBackend(t, p.dir)
	name := addTestTrigger(t, client, triggerFlow("schedule.every", Values{"interval": 0.002}, "job", Values{"text": "=event.time"}))
	row := waitTrigger(t, client, name, func(row Trigger) bool { return row.State == "off" })
	if row.Waiting != 0 || row.Current != nil {
		t.Fatalf("new trigger is not off and empty: %+v", row)
	}
	request(t, client, "trigger on", name)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("trigger did not start its first run")
	}
	row = waitTrigger(t, client, name, func(row Trigger) bool { return row.Current != nil && row.Waiting >= 2 })
	first := row.Current.ID
	request(t, client, "trigger off", name)
	waiting := waitTrigger(t, client, name, func(row Trigger) bool { return row.State == "off" }).Waiting
	request(t, client, "trigger on", name)
	waitTrigger(t, client, name, func(row Trigger) bool { return row.Waiting > waiting })
	request(t, client, "trigger off", name)
	runs, err := p.Runs()
	if err != nil {
		t.Fatal(err)
	}
	var queued []Record
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Trigger == name && runs[i].ID != first && !runs[i].Started.IsZero() {
			t.Fatalf("off then on overlapped the current run: %+v", runs[i])
		}
		if runs[i].Trigger == name && runs[i].pending() {
			queued = append(queued, runs[i])
		}
	}
	if len(queued) < 2 {
		t.Fatal("off discarded accepted events")
	}
	// The started run keeps its definition; queued runs check the new definition.
	writeFiles(t, p.dir, map[string]string{"job.flow": job(1)})
	release <- struct{}{}
	frames := request(t, client, "runs follow", first)
	var failed Record
	json.Unmarshal(frames[len(frames)-1], &failed)
	if failed.State != "failed" || !strings.Contains(failed.Error, "non-zero divisor") {
		t.Fatalf("first run did not fail as checked: %+v", failed)
	}
	row = waitTrigger(t, client, name, func(row Trigger) bool { return row.Current == nil })
	if row.State != "off" || row.Waiting != len(queued) || (row.LastFailure == nil || row.LastFailure.ID != first) {
		t.Fatalf("off did not retain the queue and failure: %+v", row)
	}
	request(t, client, "trigger on", name)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("queued event did not resume")
	}
	request(t, client, "trigger off", name)
	row = waitTrigger(t, client, name, func(row Trigger) bool { return row.Current != nil })
	if row.Current.ID != queued[0].ID {
		t.Fatalf("queue ran out of order: got %s, want %s", row.Current.ID, queued[0].ID)
	}
	request(t, client, "trigger clear", name)
	row = waitTrigger(t, client, name, func(row Trigger) bool { return row.Waiting == 0 })
	if row.Current.ID != queued[0].ID {
		t.Fatal("clear stopped the current run")
	}
	release <- struct{}{}
	frames = request(t, client, "runs follow", queued[0].ID)
	var done Record
	json.Unmarshal(frames[len(frames)-1], &done)
	if done.State != "done" || done.Inputs["text"] != queued[0].Inputs["text"] || done.Outputs["text"] != strings.ToUpper(queued[0].Inputs["text"].(string)) || done.Started.Before(failed.Finished) || maximum.Load() != 1 {
		t.Fatalf("run after failure/order/overlap: %+v; maximum active %d", done, maximum.Load())
	}
	request(t, client, "trigger remove", name)
	if data := string(request(t, client, "trigger list")[0]); data != "[]" {
		t.Fatalf("remove left a trigger: %s", data)
	}
}

func TestTriggerRestart(t *testing.T) {
	p := fileProject(t, map[string]string{"upper.flow": "inputs: {text: text}\nsteps: {u: {use: text.upper, in: {text: =inputs.text}}}\noutputs: {text: =u.text}"})
	client, stop := startBackend(t, p.dir)
	name := addTestTrigger(t, client, triggerFlow("schedule.every", Values{"interval": 0.02}, "upper", Values{"text": "=event.time"}))
	stop()
	queued := newRun(Definition{Name: "upper"}, Values{"text": "hi"}, "")
	queued.Trigger, queued.Created = name, time.Now().Add(time.Hour).UTC()
	if err := p.save(queued, true); err != nil {
		t.Fatal(err)
	}
	started := newRun(Definition{Name: "file.write"}, Values{"path": "marker", "text": "bad"}, "")
	started.Trigger, started.Started = name, time.Now().UTC()
	if err := p.save(started, true); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]bool{name: true})
	if err := writeFile(filepath.Join(p.dir, ".inode", "triggers.json"), data, 0600, true); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	client, stop = startBackend(t, p.dir)
	frames := request(t, client, "runs follow", queued.ID)
	var done Record
	json.Unmarshal(frames[len(frames)-1], &done)
	if done.State != "done" || done.Outputs["text"] != "HI" || done.ID != queued.ID {
		t.Fatalf("accepted event did not run after restart: %+v", done)
	}
	waitTrigger(t, client, name, func(row Trigger) bool {
		return row.Last != nil && row.Last.ID != queued.ID && row.Last.ID != started.ID
	})
	request(t, client, "trigger off", name)
	stop()
	client = runningBackend(t, p.dir)
	runs, err := p.Runs()
	if err != nil {
		t.Fatal(err)
	}
	hi := 0
	for _, run := range runs {
		if run.Inputs["text"] == "hi" {
			hi++
			if run.ID != queued.ID || !run.Finished.Equal(done.Finished) {
				t.Fatal("queued run was replayed")
			}
		} else if run.Node == "upper" {
			at, err := time.Parse(time.RFC3339Nano, run.Inputs["text"].(string))
			if err != nil || at.Before(before) || !run.Created.After(queued.Created) {
				t.Fatalf("replayed a missed schedule time: %+v", run)
			}
		}
	}
	if hi != 1 {
		t.Fatalf("accepted event ran %d times", hi)
	}
	old, err := p.ShowRun(started.ID)
	if err != nil || old.State != "interrupted" {
		t.Fatalf("started record was not interrupted: %+v, %v", old, err)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "marker")); !os.IsNotExist(err) {
		t.Fatal("replayed a started outside action")
	}
}

func TestTriggerCheckerAndCopy(t *testing.T) {
	p := project(t, nil)
	client := runningBackend(t, p.dir)
	valid := triggerFlow("schedule.every", Values{"interval": 0.002}, "text.upper", Values{"text": "=event.time"})
	for _, flow := range []Workflow{
		triggerFlow("absent", Values{}, "text.upper", Values{"text": "hi"}),
		triggerFlow("schedule.every", Values{}, "text.upper", Values{"text": "hi"}),
		triggerFlow("schedule.every", Values{"interval": "bad"}, "text.upper", Values{"text": "hi"}),
		triggerFlow("schedule.every", Values{"interval": 0.002}, "text.upper", Values{"text": "=event.missing"}),
		triggerFlow("schedule.every", Values{"interval": 0.002}, "number.add", Values{"a": "=event.time", "b": 1}),
		triggerFlow("text.upper", Values{"text": "hi"}, "text.upper", Values{"text": "=event.text"}),
	} {
		if err := client.Call(t.Context(), "trigger add", []any{flow}, func(json.RawMessage) error { return nil }); err == nil {
			t.Fatalf("invalid trigger accepted: %+v", flow)
		}
	}
	name := addTestTrigger(t, client, valid)
	missingAfter := triggerFlow("schedule.every", Values{"interval": 0.002}, "text.upper", Values{"text": "hi"})
	step := missingAfter.Steps["run"]
	step.After = nil
	missingAfter.Steps["run"] = step
	if err := client.Call(t.Context(), "trigger add", []any{missingAfter}, func(json.RawMessage) error { return nil }); err == nil || !strings.Contains(err.Error(), "after") {
		t.Fatalf("missing event dependency: %v", err)
	}
	path, _ := p.triggerFile(name)
	var saved Workflow
	if err := readYAML(path, &saved); err != nil || saved.Steps["event"].Use.Name != "schedule.every" {
		t.Fatalf("trigger file did not reopen: %+v, %v", saved, err)
	}
	var raw Values
	if err := readYAML(path, &raw); err != nil || len(raw) != 1 || len(saved.Steps["run"].After) != 1 {
		t.Fatalf("trigger file contains extra fields or dependencies: %+v, %v", raw, err)
	}
	request(t, client, "trigger on", name)
	copy := fileProject(t, nil)
	if err := os.CopyFS(filepath.Join(copy.dir, "triggers"), os.DirFS(filepath.Join(p.dir, "triggers"))); err != nil {
		t.Fatal(err)
	}
	other := runningBackend(t, copy.dir)
	row := waitTrigger(t, other, name, func(row Trigger) bool { return row.State == "off" })
	if row.Current != nil || row.Waiting != 0 {
		t.Fatalf("copied project activated a trigger: %+v", row)
	}
	request(t, client, "trigger off", name)
}

func TestTriggerQueuedCheckFailure(t *testing.T) {
	p := fileProject(t, map[string]string{"bad.flow": "outputs: {text: hi}"})
	client, stop := startBackend(t, p.dir)
	name := addTestTrigger(t, client, triggerFlow("schedule.every", Values{"interval": 0.02}, "text.upper", Values{"text": "=event.time"}))
	stop()
	first := newRun(Definition{Name: "missing"}, Values{}, "")
	bad := newRun(Definition{Name: "bad"}, Values{}, "")
	second := newRun(Definition{Name: "text.upper"}, Values{"text": "hi"}, "")
	for _, record := range []*Record{first, bad, second} {
		record.Trigger = name
		if err := p.save(record, true); err != nil {
			t.Fatal(err)
		}
	}
	writeFiles(t, p.dir, map[string]string{"bad.flow": "inputs: {text: nonexistent}"})
	data, _ := json.Marshal(map[string]bool{name: true})
	if err := writeFile(filepath.Join(p.dir, ".inode", "triggers.json"), data, 0600, true); err != nil {
		t.Fatal(err)
	}
	client = runningBackend(t, p.dir)
	frames := request(t, client, "runs follow", second.ID)
	var done Record
	json.Unmarshal(frames[len(frames)-1], &done)
	failed, err := p.ShowRun(first.ID)
	if err != nil || failed.State != "failed" || !strings.Contains(failed.Error, "unknown node") || done.State != "done" || done.Outputs["text"] != "HI" {
		t.Fatalf("check failure stopped the queue: %+v, %+v, %v", failed, done, err)
	}
	invalid, err := p.ShowRun(bad.ID)
	if err != nil || invalid.State != "failed" || !strings.Contains(invalid.Error, "unsupported type") {
		t.Fatalf("unparseable definition stopped the queue: %+v, %v", invalid, err)
	}
	request(t, client, "trigger off", name)
}

func TestFolderTriggerAndWatchCancellation(t *testing.T) {
	interval := folderInterval
	folderInterval = time.Millisecond
	t.Cleanup(func() { folderInterval = interval })
	p := fileProject(t, map[string]string{"in/old": "hi"})
	client := runningBackend(t, p.dir)
	name := addTestTrigger(t, client, triggerFlow("folder.new_file", Values{"path": "in"}, "text.upper", Values{"text": "=event.name"}))
	request(t, client, "trigger on", name)
	writeFiles(t, p.dir, map[string]string{"in/yo": "hi"})
	row := waitTrigger(t, client, name, func(row Trigger) bool { return row.Last != nil })
	request(t, client, "trigger off", name)
	run, err := p.ShowRun(row.Last.ID)
	if err != nil || run.Inputs["text"] != "yo" || run.Outputs["text"] != "YO" {
		t.Fatalf("folder event: %+v, %v", run, err)
	}
	runs, _ := p.Runs()
	if len(runs) != 1 {
		t.Fatalf("existing files were emitted: %+v", runs)
	}
	for _, test := range []struct {
		name   string
		inputs Values
	}{
		{"schedule.every", Values{"interval": 60}}, {"schedule.daily", Values{"at": "08:00"}}, {"folder.new_file", Values{"path": "in"}},
	} {
		ctx, cancel := context.WithCancel(t.Context())
		node := p.nodes[test.name]
		data, _ := normalise(test.inputs)
		listen, err := node.watch(ctx, data.(Values))
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- listen(func(Values) error { return nil }) }()
		cancel()
		select {
		case err := <-done:
			if err != context.Canceled {
				t.Fatalf("%s: %v", test.name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not stop promptly", test.name)
		}
	}
	run, err = p.Start(t.Context(), "schedule.every", Values{"interval": 0.001})
	if err != nil || run.State != "done" || run.Outputs["time"] == nil {
		t.Fatalf("kind did not run as an ordinary node: %+v, %v", run, err)
	}
}

func TestTriggerProcess(t *testing.T) {
	if dir := os.Getenv("INODE_TRIGGER_PROCESS"); dir != "" {
		if err := Serve(context.Background(), dir); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Skip("backend subprocess")
	}
}

func TestTriggerProcessCrash(t *testing.T) {
	entered := make(chan int32, 32)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		select {
		case entered <- n:
		case <-r.Context().Done():
			return
		}
		if n == 1 {
			<-r.Context().Done()
		} else {
			io.WriteString(w, "ok")
		}
	}))
	t.Cleanup(server.Close)
	p := fileProject(t, map[string]string{"job.flow": "inputs: {text: text}\nsteps: {gate: {use: http.get, in: {url: " + server.URL + "}}}\noutputs: {text: =inputs.text}"})
	cmd := exec.Command(os.Args[0], "-test.run=^TestTriggerProcess$")
	cmd.Env = append(os.Environ(), "INODE_TRIGGER_PROCESS="+p.dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	var client *Client
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for client == nil {
		client, _, _ = Connect(t.Context(), p.dir, false)
		if client == nil {
			select {
			case <-tick.C:
			case <-deadline:
				t.Fatal("backend subprocess did not start")
			}
		}
	}
	name := addTestTrigger(t, client, triggerFlow("schedule.every", Values{"interval": 0.005}, "job", Values{"text": "=event.time"}))
	request(t, client, "trigger on", name)
	select {
	case <-entered:
	case <-deadline:
		t.Fatal("subprocess run did not start")
	}
	row := waitTrigger(t, client, name, func(row Trigger) bool { return row.Current != nil && row.Waiting > 0 })
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	runs, err := p.Runs()
	if err != nil {
		t.Fatal(err)
	}
	var queued Record
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].pending() {
			queued = runs[i]
			break
		}
	}
	if queued.ID == "" {
		t.Fatal("crash lost the accepted event")
	}
	client = runningBackend(t, p.dir)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("accepted event did not resume after process crash")
	}
	request(t, client, "trigger off", name)
	frames := request(t, client, "runs follow", queued.ID)
	var done Record
	json.Unmarshal(frames[len(frames)-1], &done)
	old, err := p.ShowRun(row.Current.ID)
	if err != nil || old.State != "interrupted" || done.State != "done" || done.Outputs["text"] != queued.Inputs["text"] {
		t.Fatalf("crash recovery: old=%+v, queued=%+v, error=%v", old, done, err)
	}
	runs, err = p.Runs()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, run := range runs {
		if run.Inputs["text"] == queued.Inputs["text"] {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("accepted event became %d runs", count)
	}
}
