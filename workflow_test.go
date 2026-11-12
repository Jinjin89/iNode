package inode

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveWorkflow(t *testing.T) {
	p := fileProject(t, map[string]string{"tiny.flow": "outputs: {text: hi}"})
	client := runningBackend(t, p.dir)
	var original Node
	json.Unmarshal(request(t, client, "show", "tiny")[0], &original)
	if original.Version == "" {
		t.Fatal("show did not return a version")
	}
	flow := Values{"outputs": Values{"text": "bye"}, "view": Values{"positions": Values{"step:a": Values{"x": 2, "y": 3}}, "other": "kept"}}
	var saved Node
	json.Unmarshal(request(t, client, "save", "tiny", original.Version, flow)[0], &saved)
	if saved.Version == original.Version || saved.Workflow.Outputs["text"] != "bye" {
		t.Fatalf("saved: %+v", saved)
	}
	if !strings.Contains(string(request(t, client, "show", "tiny")[0]), `"other":"kept"`) {
		t.Fatal("view was not preserved")
	}
	path := filepath.Join(p.dir, "tiny.flow")
	before, _ := os.ReadFile(path)
	for _, test := range []struct {
		name, version string
		flow          any
	}{
		{"tiny", original.Version, flow},
		{"tiny", saved.Version, Values{"inputs": Values{"x": "unknown type"}}},
		{"tiny", saved.Version, Values{"steps": Values{"a": Values{"use": "text.upper", "extra": true}}}},
		{"tiny", saved.Version, Values{"unknown": true}},
		{"../outside", "", flow},
		{"text.upper", "", flow},
	} {
		if err := client.Call(t.Context(), "save", []any{test.name, test.version, test.flow}, func(json.RawMessage) error { return nil }); err == nil {
			t.Fatalf("save accepted: %+v", test)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("rejected save changed the file")
		}
	}
	if _, err := os.Stat(filepath.Join(p.dir, "text.upper.flow")); !os.IsNotExist(err) {
		t.Fatal("rejected name created a second node definition")
	}
	// A half-built workflow is parseable, can be reopened, and is checked as a value.
	invalid := Values{"steps": Values{"a": Values{"use": "absent"}}, "outputs": Values{}}
	json.Unmarshal(request(t, client, "save", "new", "", invalid)[0], &saved)
	if saved.Workflow.Steps["a"].Use.Name != "absent" {
		t.Fatal("half-built workflow was not saved")
	}
	request(t, client, "show", "new")
	var report CheckReport
	json.Unmarshal(request(t, client, "check", invalid)[0], &report)
	if len(report.Problems) == 0 || report.Problems[0].Step != "(inline)/a" {
		t.Fatalf("draft check: %+v", report)
	}
	writeFiles(t, p.dir, map[string]string{"new.flow": "outputs: {text: edited outside}"})
	if err := client.Call(t.Context(), "save", []any{"new", saved.Version, invalid}, func(json.RawMessage) error { return nil }); err == nil {
		t.Fatal("save ignored an external edit")
	}
	// Starting a draft uses the same checked snapshot, including its view.
	var id string
	json.Unmarshal(request(t, client, "run", flow, Values{}, "")[0], &id)
	var run Record
	frames := request(t, client, "runs follow", id)
	json.Unmarshal(frames[len(frames)-1], &run)
	if run.State != "done" || run.Outputs["text"] != "bye" || run.Workflow.View == nil {
		t.Fatalf("draft run: %+v", run)
	}
}

func TestFrontend(t *testing.T) {
	client := runningBackend(t, t.TempDir())
	for _, test := range []struct {
		credential, origin, host string
		status                   int
	}{
		{"", "", client.Address, 200}, {client.Credential, "", client.Address, 200},
		{"", "http://elsewhere.invalid", client.Address, 403}, {"", "", "elsewhere.invalid", 403},
	} {
		req, _ := http.NewRequest("GET", "http://"+client.Address+"/", nil)
		req.Host = test.host
		req.Header.Set("Origin", test.origin)
		if test.credential != "" {
			req.Header.Set("Authorization", "Bearer "+test.credential)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != test.status {
			t.Fatalf("frontend reach %+v: %s", test, response.Status)
		}
		if test.status != http.StatusOK {
			continue
		}
		if !strings.Contains(string(body), `<div id="root">`) || strings.Contains(string(body), "__OPERATIONS__") {
			t.Fatalf("frontend: %s, %s", response.Status, body)
		}
		for name := range Operations {
			if !strings.Contains(string(body), `"`+name+`"`) {
				t.Fatalf("page did not get operation %q", name)
			}
		}
	}
	response, err := http.Head("http://" + client.Address + "/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("page HEAD without credential: %s", response.Status)
	}
	// Public assets do not make operations public.
	response, err = http.Post("http://"+client.Address+"/", "application/json", strings.NewReader(`["nodes"]`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("operation without credential: %s", response.Status)
	}
	files, _ := os.ReadDir("web/dist/assets")
	for _, file := range files {
		response, err := http.Get("http://" + client.Address + "/assets/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("asset %s: %s", file.Name(), response.Status)
		}
	}
}

func TestCanvasRunNames(t *testing.T) {
	p := project(t, map[string]string{"base": "outputs: {text: old}"})
	client := runningBackend(t, p.dir)
	// A run's label must not make a draft's reference to the saved base recursive.
	draft := Values{"steps": Values{"read": Values{"use": "base"}}, "outputs": Values{"text": "=read.text"}}
	for _, name := range []string{"base", ""} {
		var id string
		json.Unmarshal(request(t, client, "run", draft, Values{}, name)[0], &id)
		frames := request(t, client, "runs follow", id)
		var run Record
		json.Unmarshal(frames[len(frames)-1], &run)
		want := name
		if want == "" {
			want = "Unsaved workflow"
		}
		if run.Node != want || !run.Unsaved || run.State != "done" || run.Outputs["text"] != "old" || run.Steps["read"].Node != "base" {
			t.Fatalf("run: %+v", run)
		}
		var rows []Record
		json.Unmarshal(request(t, client, "runs")[0], &rows)
		if rows[0].Node != want {
			t.Fatalf("listing: %+v", rows)
		}
	}
}

func TestRunAgain(t *testing.T) {
	original := "inputs: {text: text}\noutputs: {text: =inputs.text}"
	p := fileProject(t, map[string]string{"word.flow": original})
	client := runningBackend(t, p.dir)
	read := func(id string) Record {
		t.Helper()
		frames := request(t, client, "runs follow", id)
		var record Record
		if err := json.Unmarshal(frames[len(frames)-1], &record); err != nil {
			t.Fatal(err)
		}
		return record
	}
	for _, unsaved := range []bool{false, true} {
		writeFiles(t, p.dir, map[string]string{"word.flow": original})
		var definition any = "word"
		if unsaved {
			definition = p.nodes["word"].Workflow
		}
		var id, next string
		json.Unmarshal(request(t, client, "run", definition, Values{"text": "hi"}, "word")[0], &id)
		first := read(id)
		writeFiles(t, p.dir, map[string]string{"word.flow": "inputs: {text: text}\nsteps: {u: {use: text.upper, in: {text: =inputs.text}}}\noutputs: {text: =u.text}"})
		json.Unmarshal(request(t, client, "runs again", id)[0], &next)
		again := read(next)
		want := "HI"
		if unsaved {
			want = "hi"
		}
		if again.ID == first.ID || again.Node != "word" || again.Unsaved != unsaved || again.State != "done" || again.Inputs["text"] != "hi" || again.Outputs["text"] != want {
			t.Fatalf("again: %+v", again)
		}
		if err := os.Remove(filepath.Join(p.dir, "word.flow")); err != nil {
			t.Fatal(err)
		}
		if unsaved {
			json.Unmarshal(request(t, client, "runs again", id)[0], &next)
			if run := read(next); run.Outputs["text"] != "hi" || !run.Unsaved {
				t.Fatalf("unsaved snapshot after removal: %+v", run)
			}
		} else if err := client.Call(t.Context(), "runs again", []any{id}, func(json.RawMessage) error { return nil }); err == nil || !strings.Contains(err.Error(), "unknown node") {
			t.Fatalf("deleted saved workflow: %v", err)
		}
	}
}
