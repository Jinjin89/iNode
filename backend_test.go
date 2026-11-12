package inode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func runningBackend(t *testing.T, dir string) *Client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var serveErr error
	go func() { serveErr = Serve(ctx, dir); close(done) }()
	t.Cleanup(func() {
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
	})
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if client, _, err := Connect(ctx, dir, false); err == nil {
			return client
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
	if err := json.Unmarshal(request(t, client, "run", "upper", Values{"text": "hi"})[0], &id); err != nil {
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
	if err := json.Unmarshal(request(t, client, "plugins")[0], &plugins); err != nil || len(plugins) != 1 || !plugins[0].Installed {
		t.Fatalf("plugins: %+v, %v", plugins, err)
	}
	request(t, client, "remove", "tiny")
	if data := string(request(t, client, "plugins")[0]); data != "null" && data != "[]" {
		t.Fatal(data)
	}
	for _, test := range []struct {
		op   string
		args []any
	}{
		{"absent", nil}, {"show", nil}, {"show", []any{2}}, {"run", []any{"upper", Values{"extra": 1}}},
		{"runs show", []any{"absent"}}, {"runs stop", []any{"absent"}}, {"runs follow", []any{"absent"}},
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
	json.Unmarshal(request(t, client, "run", "x", Values{})[0], &id)
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
			t.Fatal("follow did not finish")
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
		json.Unmarshal(request(t, client, "run", "x", Values{})[0], &id)
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
			t.Fatal("follow did not finish")
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
	if err := p.save(run); err != nil {
		t.Fatal(err)
	}
	pending, err := p.begin("file.write", Values{"path": "marker", "text": "hi"})
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
	if err := json.Unmarshal(request(t, client, "plugins")[0], &plugins); err != nil || len(plugins) != 2 || !plugins[0].Installed || !plugins[1].Installed || plugins[0].Name == plugins[1].Name {
		t.Fatalf("one install overwrote the other: %+v, %v", plugins, err)
	}
}

func TestStopDuringPluginSetup(t *testing.T) {
	p := fileProject(t, map[string]string{"nodes/wait.node": "doc: Wait\nrun: exec python3 -c 'import signal; signal.pause()'"})
	client := runningBackend(t, p.dir)
	var id string
	json.Unmarshal(request(t, client, "run", "wait", Values{})[0], &id)
	source := pluginFolder(t)
	manifest, err := os.ReadFile(filepath.Join(source, "plugin"))
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, source, map[string]string{
		"plugin":  strings.Replace(string(manifest), "printf x >> setup_count; printf hi > nodes/greeting.txt", "exec python3 wait.py", 1),
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
