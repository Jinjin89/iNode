package inode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func runningBackend(t *testing.T, dir string) *Client {
	client, _ := startBackend(t, dir)
	return client
}

func startBackend(t *testing.T, dir string) (*Client, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var serveErr error
	go func() { serveErr = Serve(ctx, dir); close(done) }()
	stopBackend := func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if client, _, err := Connect(stopCtx, dir, false); err == nil {
			client.Call(stopCtx, "stop", nil, func(json.RawMessage) error { return nil })
		}
		cancel()
		select {
		case <-done:
			if serveErr != nil {
				t.Error(serveErr)
			}
		case <-stopCtx.Done():
			t.Error("backend did not stop")
		}
	}
	t.Cleanup(stopBackend)
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if client, _, err := Connect(ctx, dir, false); err == nil {
			return client, stopBackend
		}
		select {
		case <-done:
			t.Fatalf("backend failed: %v", serveErr)
		case <-deadline:
			t.Fatal("backend did not start")
		case <-tick.C:
		}
	}
}

func request(t *testing.T, client *Client, name string, args ...any) []json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var results []json.RawMessage
	if err := client.Call(ctx, name, args, func(data json.RawMessage) error { results = append(results, data); return nil }); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return results
}

func TestHelp(t *testing.T) {
	text, err := os.ReadFile("docs/tutorial.md")
	if err != nil {
		t.Fatal(err)
	}
	client := runningBackend(t, t.TempDir())
	var received string
	if err := json.Unmarshal(request(t, client, "help")[0], &received); err != nil {
		t.Fatal(err)
	}
	if received != string(text) || received != Help() {
		t.Fatal("help differs from the embedded tutorial")
	}
}

func TestStopWithUnusedConnection(t *testing.T) {
	var unused net.Conn
	// Keep the connection open until the fixture has checked shutdown.
	t.Cleanup(func() {
		if unused != nil {
			unused.Close()
		}
	})
	client := runningBackend(t, t.TempDir())
	var err error
	unused, err = net.Dial("tcp", client.Address)
	if err != nil {
		t.Fatal(err)
	}
	request(t, client, "nodes")
	request(t, client, "stop")
}

func TestBackendOperations(t *testing.T) {
	p := fileProject(t, map[string]string{
		"upper.flow": "inputs: {text: text}\nsteps: {upper: {use: text.upper, in: {text: =inputs.text}}}\noutputs: {text: =upper.text}",
		"bad.flow":   "steps: {bad: {use: absent}}",
	})
	client := runningBackend(t, p.dir)
	if err := Serve(context.Background(), p.dir); err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("second backend: %v", err)
	}
	if nodes := string(request(t, client, "nodes")[0]); !strings.Contains(nodes, `"name":"upper"`) {
		t.Fatal(nodes)
	}
	if node := string(request(t, client, "show", "upper")[0]); !strings.Contains(node, `"no_outside_effect":true`) {
		t.Fatal(node)
	}
	for _, name := range []string{"upper", "bad"} {
		var report CheckReport
		if err := json.Unmarshal(request(t, client, "check", name)[0], &report); err != nil || (len(report.Problems) == 0) != (name == "upper") {
			t.Fatalf("check %s: %+v, %v", name, report, err)
		}
	}
	var id string
	if err := json.Unmarshal(request(t, client, "run", "upper", Values{"text": "hi"}, "")[0], &id); err != nil {
		t.Fatal(err)
	}
	frames := request(t, client, "runs follow", id)
	var run Record
	if err := json.Unmarshal(frames[len(frames)-1], &run); err != nil || run.State != "done" || run.Outputs["text"] != "HI" {
		t.Fatalf("run: %+v, %v", run, err)
	}
	request(t, client, "runs stop", id)
	if rows := string(request(t, client, "runs")[0]); !strings.Contains(rows, id) {
		t.Fatal(rows)
	}
	if row := string(request(t, client, "runs show", id)[0]); !strings.Contains(row, id) {
		t.Fatal(row)
	}
	writeFiles(t, p.dir, map[string]string{"upper.flow": "outputs: {text: changed}"})
	if node := string(request(t, client, "show", "upper")[0]); !strings.Contains(node, "changed") {
		t.Fatal("request used an old workflow definition")
	}
	var disclosure string
	for _, data := range request(t, client, "add", pluginFolder(t)) {
		disclosure += string(data)
	}
	if !strings.Contains(disclosure, "Reads and writes") {
		t.Fatal("access disclosure not returned")
	}
	var plugins []Plugin
	if err := json.Unmarshal(request(t, client, "plugins")[0], &plugins); err != nil || len(plugins) != 1 || plugins[0].State != "installed" {
		t.Fatalf("plugins: %+v, %v", plugins, err)
	}
	if plugins[0].Doc != "Add a short greeting" || !slices.Equal(plugins[0].Accesses, []string{"Reads and writes its own greeting file."}) {
		t.Fatalf("plugin description missing from list: %+v", plugins[0])
	}
	request(t, client, "remove", "tiny")
	if data := string(request(t, client, "plugins")[0]); data != "null" && data != "[]" {
		t.Fatal(data)
	}
	for _, test := range []struct {
		op   string
		args []any
	}{
		{"absent", nil}, {"show", nil}, {"show", []any{2}}, {"run", []any{"upper", Values{"extra": 1}, ""}},
		{"runs show", []any{"absent"}}, {"runs stop", []any{"absent"}}, {"runs follow", []any{"absent"}}, {"runs again", []any{"absent"}},
	} {
		if err := client.Call(context.Background(), test.op, test.args, func(json.RawMessage) error { return nil }); err == nil {
			t.Fatalf("invalid request succeeded: %+v", test)
		}
	}
	writeFiles(t, p.dir, map[string]string{"plugins": "["})
	request(t, client, "stop")
}

func TestFollowRunOrder(t *testing.T) {
	entered := make(chan string, 2)
	release := map[string]chan struct{}{"/a": make(chan struct{}), "/b": make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- r.URL.Path
		select {
		case <-release[r.URL.Path]:
			io.WriteString(w, "hi")
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	p := fileProject(t, map[string]string{"x.flow": "steps:\n  a: {use: http.get, in: {url: " + server.URL + "/a}}\n  b: {use: http.get, after: [a], in: {url: " + server.URL + "/b}}\noutputs: {text: =b.body}"})
	client := runningBackend(t, p.dir)
	var id string
	json.Unmarshal(request(t, client, "run", "x", Values{}, "")[0], &id)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case path := <-entered:
		if path != "/a" {
			t.Fatal(path)
		}
	case <-ctx.Done():
		t.Fatal("first step did not start")
	}
	type result struct {
		states []string
		err    error
	}
	done := make(chan result, 2)
	for range 2 {
		initial := make(chan struct{})
		go func() {
			var states []string
			err := client.Call(ctx, "runs follow", []any{id}, func(data json.RawMessage) error {
				if bytes.ContainsAny(data, "\r\n") {
					return fmt.Errorf("a record spans more than one line")
				}
				var run Record
				if err := json.Unmarshal(data, &run); err != nil {
					return err
				}
				states = append(states, run.State+"/"+run.Steps["a"].State+"/"+run.Steps["b"].State)
				if len(states) == 1 {
					close(initial)
				}
				return nil
			})
			done <- result{states, err}
		}()
		select {
		case <-initial:
		case <-ctx.Done():
			t.Fatal("follow did not send the current record")
		}
	}
	close(release["/a"])
	select {
	case path := <-entered:
		if path != "/b" {
			t.Fatal(path)
		}
	case <-ctx.Done():
		t.Fatal("second step did not start")
	}
	close(release["/b"])
	want := []string{"running/running/waiting", "running/done/waiting", "running/done/running", "running/done/done", "done/done/done"}
	for range 2 {
		select {
		case result := <-done:
			if result.err != nil || len(result.states) == 0 || result.states[len(result.states)-1] != want[len(want)-1] {
				t.Fatalf("follow: %v, %v", result.states, result.err)
			}
			previous := -1
			for _, state := range result.states {
				i := slices.Index(want, state)
				if i <= previous {
					t.Fatalf("snapshots out of order: %v", result.states)
				}
				previous = i
			}
		case <-ctx.Done():
			stack := make([]byte, 1<<20)
			n := runtime.Stack(stack, true)
			t.Fatalf("follow did not finish\n%s", stack[:n])
		}
	}
}

func TestStopRunsAndBackend(t *testing.T) {
	entered := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-r.Context().Done() }))
	t.Cleanup(server.Close)
	p := fileProject(t, map[string]string{"x.flow": "steps: {wait: {use: http.get, in: {url: " + server.URL + "}}}"})
	client := runningBackend(t, p.dir)
	for _, operation := range []string{"runs stop", "stop"} {
		var id string
		json.Unmarshal(request(t, client, "run", "x", Values{}, "")[0], &id)
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("run did not start")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		initial, done := make(chan struct{}), make(chan error, 1)
		var final Record
		go func() {
			first := true
			done <- client.Call(ctx, "runs follow", []any{id}, func(data json.RawMessage) error {
				if err := json.Unmarshal(data, &final); err != nil {
					return err
				}
				if first {
					close(initial)
					first = false
				}
				return nil
			})
		}()
		select {
		case <-initial:
		case <-ctx.Done():
			t.Fatal("follow did not start")
		}
		if operation == "runs stop" {
			request(t, client, operation, id)
		} else {
			request(t, client, operation)
		}
		select {
		case err := <-done:
			if operation == "runs stop" && (err != nil || final.State != "interrupted") {
				t.Fatalf("interruption was not delivered: %+v, %v", final, err)
			}
			if operation == "stop" && err != nil && err.Error() != context.Canceled.Error() {
				t.Fatalf("backend shutdown did not cancel its request: %v", err)
			}
		case <-ctx.Done():
			stack := make([]byte, 1<<20)
			n := runtime.Stack(stack, true)
			t.Fatalf("follow did not finish\n%s", stack[:n])
		}
		run, err := p.ShowRun(id)
		if err != nil || run.State != "interrupted" || run.Steps["wait"].State != "interrupted" || !strings.Contains(run.Error, "outcome unknown") {
			t.Fatalf("%s: %+v, %v", operation, run, err)
		}
	}
}

func TestStartupInterruptsRecords(t *testing.T) {
	p := project(t, nil)
	run := &Record{ID: "old", Node: "file.write", State: "running", Inputs: Values{"path": "marker", "text": "hi"}, Steps: map[string]*Record{
		"running": {Node: "text.upper", State: "running"}, "done": {Node: "text.upper", State: "done", Outputs: Values{"text": "HI"}}, "waiting": {Node: "text.upper", State: "waiting"},
	}}
	if err := p.save(run, true); err != nil {
		t.Fatal(err)
	}
	pending, err := p.begin(newRun(Definition{Name: "file.write"}, Values{"path": "marker", "text": "hi"}, ""))
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := p.ShowRun(pending.root.ID); err != nil || saved.State != "waiting" || saved.Inputs["text"] != "hi" {
		t.Fatalf("begin did not save the bound record: %+v, %v", saved, err)
	}
	client := runningBackend(t, p.dir)
	var restored Record
	if err := json.Unmarshal(request(t, client, "runs show", "old")[0], &restored); err != nil || restored.State != "interrupted" || restored.Finished.IsZero() || restored.Steps["running"].State != "interrupted" || restored.Steps["done"].State != "done" || restored.Steps["waiting"].State != "waiting" {
		t.Fatalf("restored: %+v, %v", restored, err)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "marker")); !os.IsNotExist(err) {
		t.Fatal("startup replayed the operation")
	}
	if err := json.Unmarshal(request(t, client, "runs show", pending.root.ID)[0], &restored); err != nil || restored.State != "interrupted" {
		t.Fatalf("startup left the begun run waiting: %+v, %v", restored, err)
	}
}

func TestBackendReach(t *testing.T) {
	p := project(t, nil)
	client := runningBackend(t, p.dir)
	host, _, err := net.SplitHostPort(client.Address)
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("listener is not loopback: %s, %v", client.Address, err)
	}
	info, err := os.Stat(backendFile(p.dir))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential file permissions: %v, %v", info, err)
	}
	for _, test := range []struct {
		credential, origin, host string
		status                   int
	}{
		{"", "", client.Address, 403}, {"wrong", "", client.Address, 403},
		{client.Credential, "http://elsewhere.invalid", client.Address, 403},
		{client.Credential, "", "elsewhere.invalid", 403},
		{client.Credential, "http://" + client.Address, client.Address, 200},
		{client.Credential, "", client.Address, 200},
	} {
		req, err := http.NewRequest(http.MethodPost, "http://"+client.Address, strings.NewReader(`["nodes"]`))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = test.host
		req.Header.Set("Authorization", "Bearer "+test.credential)
		req.Header.Set("Origin", test.origin)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != test.status {
			t.Fatalf("reach %+v: %s", test, response.Status)
		}
	}
}

func TestConcurrentPluginRequests(t *testing.T) {
	p := project(t, nil)
	client := runningBackend(t, p.dir)
	first, second := pluginFolder(t), pluginFolder(t)
	manifest, err := os.ReadFile(filepath.Join(second, "plugin"))
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, second, map[string]string{"plugin": strings.Replace(string(manifest), "name: tiny", "name: other", 1)})
	done := make(chan error, 2)
	for _, source := range []string{first, second} {
		go func() {
			done <- client.Call(context.Background(), "add", []any{source}, func(json.RawMessage) error { return nil })
		}()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	var plugins []Plugin
	if err := json.Unmarshal(request(t, client, "plugins")[0], &plugins); err != nil || len(plugins) != 2 || plugins[0].State != "installed" || plugins[1].State != "installed" || plugins[0].Name == plugins[1].Name {
		t.Fatalf("one install overwrote the other: %+v, %v", plugins, err)
	}
}

func TestStopDuringPluginSetup(t *testing.T) {
	p := fileProject(t, map[string]string{"nodes/wait.node": "doc: Wait\nrun: python3 wait.py", "nodes/wait.py": "import signal\nsignal.pause()\n"})
	client := runningBackend(t, p.dir)
	var id string
	json.Unmarshal(request(t, client, "run", "wait", Values{}, "")[0], &id)
	source := pluginFolder(t)
	manifest, err := os.ReadFile(filepath.Join(source, "plugin"))
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, source, map[string]string{
		"plugin":  strings.Replace(string(manifest), `[python3, "setup count.py"]`, "python3 wait.py", 1),
		"wait.py": "import signal\nopen('ready', 'w').close()\nsignal.pause()\n",
	})
	done := make(chan error, 1)
	go func() {
		done <- client.Call(context.Background(), "add", []any{source}, func(json.RawMessage) error { return nil })
	}()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(filepath.Join(p.pluginDir("tiny"), "ready")); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("setup exited before stopping: %v", err)
		case <-deadline:
			t.Fatal("setup did not start")
		case <-tick.C:
		}
	}
	request(t, client, "runs stop", id)
	frames := request(t, client, "runs follow", id)
	var run Record
	if err := json.Unmarshal(frames[len(frames)-1], &run); err != nil || run.State != "interrupted" {
		t.Fatalf("setup blocked stopping a run: %+v, %v", run, err)
	}
	request(t, client, "stop")
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("setup was not cancelled: %v", err)
		}
	case <-deadline:
		t.Fatal("setup blocked shutdown")
	}
	if _, err := os.Stat(p.pluginDir("tiny")); !os.IsNotExist(err) {
		t.Fatal("interrupted setup kept its installed folder")
	}
}

func startupProject(t *testing.T, delays ...int) *Project {
	t.Helper()
	p := fileProject(t, nil)
	plugins := []Plugin{}
	for i, delay := range delays {
		name := []string{"first", "second"}[i]
		manifest := Plugin{Name: name, Version: "1", Doc: "Delayed plugin", Process: []any{"python3", "plugin.py"}}
		data, _ := json.Marshal(manifest)
		body := strings.Replace(fakePlugin, "for line in sys.stdin:", fmt.Sprintf("Path('started').write_text(json.dumps({'pid': os.getpid(), 'at': time.time()}))\ntime.sleep(%d)\nfor line in sys.stdin:", delay), 1)
		body = strings.Replace(body, " if m['method']=='describe':", " if m['method']=='describe':\n  Path('described').write_text(json.dumps(time.time()))", 1)
		writeFiles(t, p.dir, map[string]string{
			".inode/plugins/" + name + "/plugin":    string(data),
			".inode/plugins/" + name + "/plugin.py": body,
		})
		plugins = append(plugins, Plugin{Name: name, Version: "1", Source: name})
	}
	if err := p.savePlugins(plugins); err != nil {
		t.Fatal(err)
	}
	return p
}

func startupReply(t *testing.T, client *Client, operation string) json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	var result json.RawMessage
	if err := client.Call(ctx, operation, nil, func(data json.RawMessage) error { result = append(json.RawMessage(nil), data...); return nil }); err != nil {
		t.Fatalf("%s did not answer within 300ms: %v", operation, err)
	}
	return result
}

func TestBackendStartupServesAndWaits(t *testing.T) {
	p := startupProject(t, 3, 3)
	client := runningBackend(t, p.dir)
	startupReply(t, client, "help")
	var rows []Plugin
	if err := json.Unmarshal(startupReply(t, client, "plugins"), &rows); err != nil || len(rows) != 2 || rows[0].State != "starting" || rows[1].State != "starting" {
		t.Fatalf("startup status: %+v, %v", rows, err)
	}
	pageCtx, cancelPage := context.WithTimeout(t.Context(), 300*time.Millisecond)
	req, _ := http.NewRequestWithContext(pageCtx, http.MethodGet, "http://"+client.Address+"/", nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("page did not answer during startup:", err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	cancelPage()
	if response.StatusCode != http.StatusOK {
		t.Fatal(response.Status)
	}
	cancelled, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	err = client.Call(cancelled, "run", []any{"first.probe", Values{"mode": "echo", "value": "must not run"}, ""}, func(json.RawMessage) error { return nil })
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request did not cancel its startup wait: %v", err)
	}
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	type acknowledged struct {
		id  string
		err error
	}
	ack := make(chan acknowledged, 1)
	go func() {
		var id string
		err := client.Call(ctx, "run", []any{"first.probe", Values{"mode": "echo", "value": "ready"}, ""}, func(data json.RawMessage) error { return json.Unmarshal(data, &id) })
		ack <- acknowledged{id, err}
	}()
	select {
	case value := <-ack:
		t.Fatalf("run did not wait for the complete catalog: %+v", value)
	case <-time.After(100 * time.Millisecond):
	}
	var value acknowledged
	select {
	case value = <-ack:
	case <-ctx.Done():
		t.Fatal("run stayed blocked after startup")
	}
	if value.err != nil {
		t.Fatal(value.err)
	}
	run := finishProcessRun(t, client, value.id)
	if run.State != "done" || run.Outputs["text"] != "ready" {
		t.Fatalf("run after startup: %+v", run)
	}
	runs, err := p.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("cancelled request was accepted later: %+v, %v", runs, err)
	}
	latestStart, earliestDescription := float64(0), float64(1e20)
	for _, name := range []string{"first", "second"} {
		var started struct {
			At float64
		}
		data, _ := os.ReadFile(filepath.Join(p.pluginDir(name), "started"))
		if err := json.Unmarshal(data, &started); err != nil {
			t.Fatal(err)
		}
		var described float64
		data, _ = os.ReadFile(filepath.Join(p.pluginDir(name), "described"))
		if err := json.Unmarshal(data, &described); err != nil {
			t.Fatal(err)
		}
		latestStart, earliestDescription = max(latestStart, started.At), min(earliestDescription, described)
	}
	if latestStart >= earliestDescription {
		t.Fatal("plugin startup was sequential")
	}
}

func TestBackendStopDuringInitialization(t *testing.T) {
	p := startupProject(t, 3, 3)
	client, stop := startBackend(t, p.dir)
	startupReply(t, client, "help")
	for _, name := range []string{"first", "second"} {
		waitProcess(t, func() bool { _, err := os.Stat(filepath.Join(p.pluginDir(name), "started")); return err == nil })
	}
	pids := []int{processPID(t, p.pluginDir("first")), processPID(t, p.pluginDir("second"))}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := client.Call(ctx, "stop", nil, func(json.RawMessage) error { return nil }); err != nil {
		t.Fatal("stop did not promptly cancel initialization:", err)
	}
	stop()
	for _, pid := range pids {
		if pid == 0 || syscall.Kill(pid, 0) == nil {
			t.Fatalf("plugin process survived stop: %d", pid)
		}
	}
	if _, err := os.Stat(backendFile(p.dir)); !os.IsNotExist(err) {
		t.Fatal("stop left the backend credential")
	}
}

func TestBackendStartupDefersTriggers(t *testing.T) {
	interval := folderInterval
	folderInterval = 10 * time.Millisecond
	t.Cleanup(func() { folderInterval = interval })
	p := startupProject(t, 0, 3)
	flow := triggerFlow("folder.new_file", Values{"path": "in"}, "first.probe", Values{"mode": "echo", "value": "=event.name"})
	data, _ := json.Marshal(flow)
	writeFiles(t, p.dir, map[string]string{
		"in/old": "old", "triggers/folder.new_file-1.flow": string(data), ".inode/triggers.json": `{"folder.new_file-1":true}`,
	})
	client := runningBackend(t, p.dir)
	waitProcess(t, func() bool {
		var rows []Plugin
		json.Unmarshal(startupReply(t, client, "plugins"), &rows)
		return len(rows) == 2 && rows[0].State == "running" && rows[1].State == "starting"
	})
	writeFiles(t, p.dir, map[string]string{"in/during-startup": "early"})
	time.Sleep(100 * time.Millisecond)
	if runs, err := p.Runs(); err != nil || len(runs) != 0 {
		t.Fatalf("an event was handled before the catalog was complete: %+v, %v", runs, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := client.Call(ctx, "nodes", nil, func(json.RawMessage) error { return nil }); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, p.dir, map[string]string{"in/one": "new"})
	row := waitTrigger(t, client, "folder.new_file-1", func(row Trigger) bool { return row.Last != nil })
	if row.State != "on" || row.Error != "" || row.Last.State != "done" || row.Last.Outputs["text"] != "one" {
		t.Fatalf("persisted trigger did not start after initialization: %+v", row)
	}
	time.Sleep(3 * folderInterval)
	request(t, client, "trigger off", "folder.new_file-1")
	if runs, err := p.Runs(); err != nil || len(runs) != 1 {
		t.Fatalf("trigger was activated more than once or emitted old files: %+v, %v", runs, err)
	}
}

func TestBackendInitializationError(t *testing.T) {
	p := fileProject(t, map[string]string{".inode/triggers.json": "["})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := Serve(ctx, p.dir); err == nil || !strings.Contains(err.Error(), "triggers.json") {
		t.Fatalf("initialization error was lost: %v", err)
	}
	if _, err := os.Stat(backendFile(p.dir)); !os.IsNotExist(err) {
		t.Fatal("initialization failure left the credential file")
	}
}

func TestClientReplyEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "{\"value\":{\"error\":\"data\"}}\n{\"value\":null}\n{\"error\":\"\"}\n")
	}))
	defer server.Close()
	client := Client{Address: strings.TrimPrefix(server.URL, "http://")}
	var values []string
	err := client.Call(context.Background(), "nodes", nil, func(data json.RawMessage) error { values = append(values, string(data)); return nil })
	if err == nil || err.Error() != "" || !slices.Equal(values, []string{`{"error":"data"}`, `null`}) {
		t.Fatalf("wire envelopes: %v, %v", values, err)
	}
}
