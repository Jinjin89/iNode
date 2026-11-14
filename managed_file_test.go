package inode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func fileProgram(t *testing.T, command Values, script string) *Project {
	t.Helper()
	helper := filepath.Join(t.TempDir(), "helper.py")
	writeFiles(t, filepath.Dir(helper), map[string]string{"helper.py": script})
	args, _ := command["args"].([]any)
	command["args"] = append([]any{"python3", helper}, args...)
	return describedProgram(t, Values{"inputs": Values{"source": "file"}, "outputs": Values{"result": "file"}, "run": command}, "")
}

func storedBytes(t *testing.T, p *Project, value any) []byte {
	t.Helper()
	value, err := p.files(value, Type{Name: "file"}, returnedFiles)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p.blob(value.(fileRef)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func noScratch(t *testing.T, p *Project) {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(p.dir, ".inode", "work", "*"))
	stdout, _ := filepath.Glob(filepath.Join(p.dir, ".inode", ".stdout-*"))
	if len(paths)+len(stdout) != 0 {
		t.Fatalf("scratch remains: %v %v", paths, stdout)
	}
}

func TestFileAcceptanceAndSave(t *testing.T) {
	for _, change := range []string{"change", "delete"} {
		t.Run(change, func(t *testing.T) {
			p := fileProgram(t, Values{"args": []any{"in.bin", "out.bin"}, "files": Values{"in.bin": "=inputs.source", "out.bin": "=outputs.result"}},
				"from pathlib import Path\nimport sys\ndata=Path(sys.argv[1]).read_bytes()\nPath(sys.argv[1]).write_bytes(b'mutated staged input')\nPath(sys.argv[2]).write_bytes(data)\nprint('unused stdout is not JSON')\n")
			original := []byte("\x00\xff accepted bytes\n")
			name := "π folder/猫 file.bin"
			writeFiles(t, p.dir, map[string]string{name: string(original)})
			flow := &Workflow{Steps: map[string]Step{"convert": {Use: Definition{Name: "probe"}, In: Values{"source": name}}}, Outputs: Values{"file": "=convert.result"}}
			if report := p.Check(Definition{Workflow: flow}); len(report.Problems) != 0 {
				t.Fatal(report.Problems)
			}
			if _, err := os.Stat(filepath.Join(p.dir, ".inode", "files")); !os.IsNotExist(err) {
				t.Fatal("checking imported bytes")
			}
			e, err := p.begin(newRun(Definition{Workflow: flow}, nil, "accepted"))
			if err != nil {
				t.Fatal(err)
			}
			if change == "change" {
				writeFiles(t, p.dir, map[string]string{name: "changed original"})
			} else if err := os.Remove(filepath.Join(p.dir, name)); err != nil {
				t.Fatal(err)
			}
			if err := e.run(t.Context()); err != nil {
				t.Fatal(err)
			}
			ref := e.root.Outputs["file"].(fileRef)
			if ref.Name != "out.bin" || ref.Size != int64(len(original)) || ref.Hash != "sha256:"+version(original) || !bytes.Equal(storedBytes(t, p, ref), original) {
				t.Fatalf("accepted bytes changed: %+v", ref)
			}
			input := e.root.Steps["convert"].Inputs["source"].(fileRef)
			if input.Name != "猫 file.bin" || input.Hash != ref.Hash {
				t.Fatalf("file labels/identity: %+v, %+v", input, ref)
			}
			entries, _ := os.ReadDir(filepath.Join(p.dir, ".inode", "files"))
			if len(entries) != 1 {
				t.Fatalf("identical input/output bytes not shared: %v", entries)
			}
			noScratch(t, p)
			// Reopening the project and using the literal recorded wire value needs no source path.
			saved, err := p.ShowRun(e.root.ID)
			if err != nil {
				t.Fatal(err)
			}
			p, err = Open(p.dir)
			if err != nil {
				t.Fatal(err)
			}
			home, _ := os.UserHomeDir()
			relative, _ := filepath.Rel(home, filepath.Join(p.dir, "saved", "copy.bin"))
			destination := "~/" + filepath.ToSlash(relative)
			for range 2 {
				run, err := p.Start(t.Context(), "file.save", Values{"file": saved.Outputs["file"], "path": destination})
				if err != nil || run.Outputs["path"] != p.fullPath(destination) {
					t.Fatalf("save: %+v, %v", run, err)
				}
				data, err := os.ReadFile(p.fullPath(destination))
				if err != nil || !bytes.Equal(data, original) {
					t.Fatalf("saved bytes: %x, %v", data, err)
				}
			}
			wire, _ := json.Marshal(saved)
			if !bytes.Contains(wire, []byte(`"$file":"sha256:`)) || bytes.Contains(wire, []byte(name)) {
				t.Fatalf("record depends on source path: %s", wire)
			}
		})
	}
}

func TestFileStreamsAndJSON(t *testing.T) {
	for _, command := range []Values{
		{"stdin": "=inputs.source", "stdout": "result"},
		{"files": Values{"in.bin": "=inputs.source"}, "stdout": "result", "args": []any{"in.bin"}},
	} {
		p := fileProgram(t, command, "import shutil,sys\nsource=open(sys.argv[1],'rb') if len(sys.argv)>1 else sys.stdin.buffer\nshutil.copyfileobj(source,sys.stdout.buffer)\n")
		data := []byte{0, 1, 255, 13, 10, 0, 128}
		writeFiles(t, p.dir, map[string]string{"source.bin": string(data)})
		run, err := p.Start(t.Context(), "probe", Values{"source": "source.bin"})
		if err != nil || !bytes.Equal(storedBytes(t, p, run.Outputs["result"]), data) {
			t.Fatalf("binary streams: %+v, %v", run, err)
		}
		noScratch(t, p)
	}
	if typeOf(Values{"$file": textType}).Name != "object" {
		t.Fatal("a port type tree is not a file reference")
	}
	legacy := fileProject(t, map[string]string{"nodes/probe.node": "doc: No outputs\ninputs: {source: file}\nrun: {args: [printf, 'not JSON'], files: {in.bin: =inputs.source}}", "source.bin": "bytes"})
	if run, err := legacy.Start(t.Context(), "probe", Values{"source": "source.bin"}); err == nil || !strings.Contains(err.Error(), "print one JSON object") || len(run.Outputs) != 0 {
		t.Fatalf("input staging changed the JSON default: %+v, %v", run, err)
	}
	p := fileProject(t, map[string]string{"nodes/echo.node": "doc: Echo file metadata\ninputs: {source: file}\noutputs: {source: file}\nrun: cat", "source.bin": "bytes"})
	run, err := p.Start(t.Context(), "echo", Values{"source": "source.bin"})
	if err != nil || !reflect.DeepEqual(run.Inputs, run.Outputs) {
		t.Fatalf("JSON file references: %+v, %v", run, err)
	}
	// A relative executable is resolved from the description even in a private folder.
	helper := filepath.Join(p.dir, "nodes", "mixed.py")
	writeFiles(t, p.dir, map[string]string{"nodes/mixed.node": "doc: File and JSON\ninputs: {source: file}\noutputs: {result: file, count: number}\nrun: {args: [./mixed.py], stdin: =inputs.source, files: {out.bin: =outputs.result}}", "nodes/mixed.py": "#!/usr/bin/env python3\nfrom pathlib import Path\nimport json,sys\ndata=sys.stdin.buffer.read()\nPath('out.bin').write_bytes(data)\nprint(json.dumps({'count':len(data)}))\n"})
	if err := os.Chmod(helper, 0700); err != nil {
		t.Fatal(err)
	}
	p, _ = Open(p.dir)
	run, err = p.Start(t.Context(), "mixed", Values{"source": "source.bin"})
	if err != nil || run.Outputs["count"] != float64(5) || string(storedBytes(t, p, run.Outputs["result"])) != "bytes" {
		t.Fatalf("mixed outputs: %+v, %v", run, err)
	}
	noScratch(t, p)
}

func TestFileProblems(t *testing.T) {
	p := fileProject(t, map[string]string{"source.bin": "bytes"})
	run, err := p.Start(t.Context(), "file.save", Values{"file": "source.bin", "path": "copy.bin"})
	if err != nil {
		t.Fatal(err)
	}
	ref := run.Inputs["file"].(fileRef)
	constant := p.Check(Definition{Workflow: &Workflow{Outputs: Values{"result": Values{"$file": "sha256:bad", "name": "bad", "size": 0}}}})
	if len(constant.Problems) == 0 || constant.Problems[0].Input != "outputs.result" {
		t.Fatalf("literal file output not checked: %+v", constant.Problems)
	}
	for _, value := range []any{
		"absent.bin", p.dir,
		Values{"$file": "sha256:" + strings.Repeat("0", 64), "name": "lost.bin", "size": 5},
		Values{"$file": ref.Hash, "name": "../escape", "size": 5},
		Values{"$file": ref.Hash, "name": "label", "size": -1},
		Values{"$file": ref.Hash, "name": "label", "size": 6},
		Values{"$file": ref.Hash, "name": "label"},
		Values{"$file": "sha256:bad", "name": "label", "size": 5},
	} {
		flow := &Workflow{Steps: map[string]Step{"save": {Use: Definition{Name: "file.save"}, In: Values{"file": value, "path": "out"}}}}
		report := p.Check(Definition{Workflow: flow})
		if len(report.Problems) == 0 || report.Problems[0].Step != "(inline)/save" || report.Problems[0].Input != "file" || !strings.Contains(report.Problems[0].Message, "; ") {
			t.Fatalf("file problem: %v -> %+v", value, report.Problems)
		}
	}
	for _, files := range []Values{
		{"../bad": "=outputs.result"}, {"/absolute": "=outputs.result"}, {"a/../b": "=outputs.result"},
		{"a": "=outputs.result", "b": "=outputs.result"}, {"a": "=inputs.source", "a/b": "=outputs.result"},
		{"a": "=inputs.absent"}, {"a": "=outputs.absent"}, {"a": "not a binding"},
	} {
		p := describedProgram(t, Values{"inputs": Values{"source": "file"}, "outputs": Values{"result": "file"}, "run": Values{"args": []any{"cat"}, "files": files}}, "")
		_, err := p.Describe("probe")
		if err == nil || !strings.Contains(err.Error(), "probe.node: run.files.") {
			t.Fatalf("file binding accepted: %v, %v", files, err)
		}
	}
}

func TestFilePathMessages(t *testing.T) {
	// Opening as an unprivileged process also checks the unreadable-file message
	// on developer machines where the test suite runs as root.
	if dir := os.Getenv("INODE_FILE_PERMISSION_PROBE"); dir != "" {
		p := &Project{dir: dir}
		_, err := p.files("private.bin", Type{Name: "file"}, checkFiles, "source")
		if err == nil || !strings.Contains(err.Error(), `file "private.bin" cannot be read at `+filepath.Join(dir, "private.bin")+"; choose a file you have permission to read") {
			t.Fatalf("unreadable file: %v", err)
		}
		return
	}
	p := fileProject(t, map[string]string{"folder/inside": "data"})
	for name, message := range map[string]string{
		"nope.txt": fmt.Sprintf("file %q was not found at %s; enter the path of an existing file", "nope.txt", filepath.Join(p.dir, "nope.txt")),
		"folder":   fmt.Sprintf("%q is a folder at %s; enter the path of a file", "folder", filepath.Join(p.dir, "folder")),
	} {
		_, err := p.files(name, Type{Name: "file"}, checkFiles, "source")
		var problem Problem
		if !errors.As(err, &problem) || problem.Input != "source" || problem.Message != message {
			t.Fatalf("%s: %v", name, err)
		}
	}
	dir, err := os.MkdirTemp("", "inode-unreadable-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "private.bin"), []byte("private"), 0000); err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(dir, "probe")
	if err := os.WriteFile(probe, data, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), probe, "-test.run=^TestFilePathMessages$")
	cmd.Env = append(os.Environ(), "INODE_FILE_PERMISSION_PROBE="+dir)
	if os.Geteuid() == 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("permission probe: %s, %v", output, err)
	}
}

func TestFileTextConnections(t *testing.T) {
	p := fileProject(t, map[string]string{"my fruit.txt": "pear\napple\n"})
	flow := &Workflow{Inputs: Ports{"p": {Type: textType}}, Steps: map[string]Step{
		"path":   {Use: Definition{Name: "text.join"}, In: Values{"parts": []any{"my ", "fruit.txt"}, "separator": ""}},
		"joined": {Use: Definition{Name: "file.save"}, In: Values{"file": "=path.text", "path": "joined.txt"}},
		"input":  {Use: Definition{Name: "file.save"}, In: Values{"file": "=inputs.p", "path": "input.txt"}},
		"boxed":  {Use: Definition{Name: "map"}, In: Values{"over": []any{"boxed.txt"}, "do": Values{"use": "file.save", "in": Values{"file": "=inputs.p"}}}},
	}}
	report := p.Check(Definition{Workflow: flow})
	if len(report.Problems) != 0 || len(report.Unchecked) != 3 {
		t.Fatalf("text paths: %+v", report)
	}
	for _, ref := range report.References {
		if (ref.Input == "file" || ref.Input == "do.in.file") && !ref.Unchecked {
			t.Fatalf("path line is checked: %+v", ref)
		}
	}
	e, err := p.begin(newRun(Definition{Workflow: flow}, Values{"p": "my fruit.txt"}, ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"joined", "input"} {
		if string(storedBytes(t, p, e.root.Steps[name].Inputs["file"])) != "pear\napple\n" {
			t.Fatal("arriving path was not stored")
		}
	}
	if data, err := os.ReadFile(filepath.Join(p.dir, "boxed.txt")); err != nil || string(data) != "pear\napple\n" {
		t.Fatalf("boxed path: %q, %v", data, err)
	}
	missing := &Workflow{Inputs: flow.Inputs, Steps: map[string]Step{"input": flow.Steps["input"]}}
	e, err = p.begin(newRun(Definition{Workflow: missing}, Values{"p": "nope.txt"}, ""))
	if err != nil {
		t.Fatal(err)
	}
	err = e.run(t.Context())
	var problem Problem
	if !errors.As(err, &problem) || problem.Input != "file" || !strings.Contains(err.Error(), "(inline)/input") || !strings.Contains(problem.Message, `file "nope.txt" was not found at `+filepath.Join(p.dir, "nope.txt")) || e.root.Steps["input"].State != "failed" {
		t.Fatalf("arriving path problem lost its step/input: %+v, %v", e.root.Steps["input"], err)
	}
}

func TestFileFolderTriggerQueue(t *testing.T) {
	interval := folderInterval
	folderInterval = time.Millisecond
	t.Cleanup(func() { folderInterval = interval })
	entered, release := make(chan struct{}, 2), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
			fmt.Fprint(w, "ok")
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	p := fileProject(t, map[string]string{
		"incoming/old.txt": "old",
		"save.flow":        fmt.Sprintf("inputs: {source: file, path: text}\nsteps:\n  gate: {use: http.get, in: {url: %s}}\n  save: {use: file.save, in: {file: =inputs.source, path: =inputs.path}, after: [gate]}\noutputs: {path: =save.path}", server.URL),
	})
	client := runningBackend(t, p.dir)
	name := addTestTrigger(t, client, triggerFlow("folder.new_file", Values{"path": "incoming"}, "save", Values{"source": "=event.path", "path": "=event.name"}))
	request(t, client, "trigger on", name)
	writeFiles(t, p.dir, map[string]string{"incoming/first.txt": "first"})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first file did not start")
	}
	writeFiles(t, p.dir, map[string]string{"incoming/second.txt": "accepted second"})
	row := waitTrigger(t, client, name, func(row Trigger) bool { return row.Waiting == 1 })
	request(t, client, "trigger off", name)
	runs, err := p.Runs()
	if err != nil {
		t.Fatal(err)
	}
	var queued Record
	for _, run := range runs {
		if run.pending() {
			queued = run
		}
	}
	if queued.ID == "" || string(storedBytes(t, p, queued.Inputs["source"])) != "accepted second" {
		t.Fatalf("waiting event did not pin bytes: %+v", queued)
	}
	if err := os.Remove(filepath.Join(p.dir, "incoming/second.txt")); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	request(t, client, "runs follow", row.Current.ID)
	request(t, client, "trigger on", name)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("queued file did not resume")
	}
	request(t, client, "trigger off", name)
	release <- struct{}{}
	frames := request(t, client, "runs follow", queued.ID)
	var done Record
	json.Unmarshal(frames[len(frames)-1], &done)
	data, err := os.ReadFile(filepath.Join(p.dir, "second.txt"))
	if err != nil || done.State != "done" || string(data) != "accepted second" {
		t.Fatalf("queued file changed: %+v, %q, %v", done, data, err)
	}
}

func TestFileNestedAndUnchecked(t *testing.T) {
	p := fileProject(t, map[string]string{"nodes/echo.node": "doc: Echo\ninputs: {source: file}\noutputs: {source: file}\nrun: cat", "a.bin": "A", "b.bin": "B"})
	run, err := p.Start(t.Context(), "map", Values{"over": []any{"a.bin", "b.bin"}, "do": "echo"})
	if err != nil {
		t.Fatal(err)
	}
	for i, ref := range run.Outputs["result"].([]any) {
		if string(storedBytes(t, p, ref)) != []string{"A", "B"}[i] {
			t.Fatal("file in box/list lost")
		}
	}
	p.register("nested", Node{Inputs: Ports{"data": {Type: Type{Name: "object", Fields: map[string]Type{"files": {Name: "list", Item: &Type{Name: "file"}}}}}}, Outputs: Ports{"result": {Type: anyType}}, run: func(_ context.Context, in Values, _ Call) (Values, error) { return Values{"result": in["data"]}, nil }})
	run, err = p.Start(t.Context(), "nested", Values{"data": Values{"files": []any{"a.bin"}}})
	if err != nil || !fileLike(run.Outputs["result"].(Values)["files"].([]any)[0]) {
		t.Fatalf("nested files: %+v, %v", run, err)
	}
	p.register("unknown", Node{Outputs: Ports{"result": {Type: anyType}}, run: func(context.Context, Values, Call) (Values, error) { return Values{"result": "a.bin"}, nil }})
	flow := &Workflow{Steps: map[string]Step{"source": {Use: Definition{Name: "unknown"}}, "copy": {Use: Definition{Name: "echo"}, In: Values{"source": "=source.result"}}}, Outputs: Values{"file": "=copy.source"}}
	e, err := p.begin(newRun(Definition{Workflow: flow}, nil, "unchecked"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.run(t.Context()); err != nil || string(storedBytes(t, p, e.root.Outputs["file"])) != "A" {
		t.Fatalf("unchecked file: %+v, %v", e.root, err)
	}
	if !fileLike(e.root.Steps["copy"].Inputs["source"]) {
		t.Fatal("unchecked path not normalized in record")
	}
}

func TestFileDefaultSnapshotAndOutputValidation(t *testing.T) {
	p := fileProject(t, map[string]string{"source.bin": "accepted default", "nodes/echo.node": "doc: Echo\ninputs: {source: {type: file, default: source.bin}}\noutputs: {source: file}\nno_outside_effect: true\nrun: cat"})
	e, err := p.begin(newRun(Definition{Name: "retry"}, Values{"do": "echo", "times": 1}, ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(p.dir, "source.bin")); err != nil {
		t.Fatal(err)
	}
	if err := e.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved, err := p.ShowRun(e.root.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.Start(t.Context(), "retry", saved.Inputs)
	if err != nil || string(storedBytes(t, p, run.Outputs["result"])) != "accepted default" {
		t.Fatalf("boxed default lost after recording: %+v, %v", run, err)
	}
	p.register("badfile", Node{Outputs: Ports{"result": {Type: anyType}}, run: func(context.Context, Values, Call) (Values, error) {
		return Values{"result": Values{"$file": "sha256:bad", "name": "bad", "size": 0}}, nil
	}})
	run, err = p.Start(t.Context(), "badfile", nil)
	if err == nil || len(run.Outputs) != 0 || !strings.Contains(err.Error(), "file") {
		t.Fatalf("bad file output published: %+v, %v", run, err)
	}
}

func TestFileTriggerAcceptance(t *testing.T) {
	for _, shape := range []string{"named", "inline", "default"} {
		t.Run(shape, func(t *testing.T) {
			p := fileProgram(t, Values{"stdin": "=inputs.source", "stdout": "result"}, "import shutil,sys\nshutil.copyfileobj(sys.stdin.buffer,sys.stdout.buffer)\n")
			writeFiles(t, p.dir, map[string]string{"event.bin": "accepted event"})
			target, _, _ := p.prepare(Definition{Name: "probe"}, nil)
			target.In = Values{"source": "=event.file"}
			if shape == "inline" {
				target, _, _ = p.prepare(Definition{Workflow: &Workflow{Steps: map[string]Step{"copy": {Use: Definition{Name: "probe"}, In: Values{"source": "event.bin"}}}, Outputs: Values{"result": "=copy.result"}}}, nil)
			}
			if shape == "default" {
				port := target.plan.Inputs["source"]
				port.Default = "event.bin"
				target.plan.Inputs["source"], target.In = port, Values{}
			}
			plan := &plan{Node: Node{Workflow: &Workflow{Steps: map[string]Step{"event": {plan: &plan{Node: Node{Outputs: Ports{"file": {Type: Type{Name: "file"}}}}}}, "run": *target}}}}
			s := &triggerService{backend: &backend{dir: p.dir}}
			if err := s.accept(t.Context(), p, "files", plan, Values{}, Values{"file": "event.bin"}, make(chan struct{}, 1)); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(p.dir, "event.bin")); err != nil {
				t.Fatal(err)
			}
			p, _ = Open(p.dir)
			runs, err := p.Runs()
			if err != nil || len(runs) != 1 || !runs[0].pending() {
				t.Fatalf("queued files: %+v, %v", runs, err)
			}
			e, err := p.begin(&runs[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := e.run(t.Context()); err != nil || string(storedBytes(t, p, e.root.Outputs["result"])) != "accepted event" {
				t.Fatalf("queued bytes lost: %+v, %v", e.root, err)
			}
		})
	}
}

func TestFileFailureAndCancellation(t *testing.T) {
	for _, script := range []string{
		"pass", "Path('out.bin').symlink_to('in.bin')", "Path('out.bin').mkdir()", "os.mkfifo('out.bin')", "Path('out.bin').write_bytes(b'partial'); sys.exit(2)",
	} {
		p := fileProgram(t, Values{"files": Values{"in.bin": "=inputs.source", "out.bin": "=outputs.result"}}, "import os,sys\nfrom pathlib import Path\n"+script+"\n")
		writeFiles(t, p.dir, map[string]string{"source.bin": "source"})
		run, err := p.Start(t.Context(), "probe", Values{"source": "source.bin"})
		if err == nil || run.State != "failed" || len(run.Outputs) != 0 {
			t.Fatalf("failed file published outputs: %+v, %v", run, err)
		}
		noScratch(t, p)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	p := fileProgram(t, Values{"args": []any{ready}, "files": Values{"in.bin": "=inputs.source", "out.bin": "=outputs.result"}}, "import signal,sys\nfrom pathlib import Path\nPath('out.bin').write_bytes(b'partial')\nPath(sys.argv[1]).touch()\nsignal.pause()\n")
	writeFiles(t, p.dir, map[string]string{"source.bin": "source"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan *Record, 1)
	go func() {
		run, err := p.Start(ctx, "probe", Values{"source": "source.bin"})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancel error: %v", err)
		}
		done <- run
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("file program did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case run := <-done:
		if run.State != "interrupted" || len(run.Outputs) != 0 {
			t.Fatalf("cancelled outputs: %+v", run)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation hung")
	}
	noScratch(t, p)
}

func TestFileParallelDedupAndLargeStream(t *testing.T) {
	p := fileProgram(t, Values{"stdin": "=inputs.source", "stdout": "result"}, "import shutil,sys\nshutil.copyfileobj(sys.stdin.buffer,sys.stdout.buffer)\n")
	file, err := os.Create(filepath.Join(p.dir, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(50 << 20); err != nil {
		t.Fatal(err)
	}
	file.Close()
	var group sync.WaitGroup
	results := make(chan fileRef, 2)
	for range 2 {
		group.Go(func() {
			run, err := p.Start(t.Context(), "probe", Values{"source": "large.bin"})
			if err != nil {
				t.Error(err)
				return
			}
			results <- run.Outputs["result"].(fileRef)
		})
	}
	group.Wait()
	close(results)
	var refs []fileRef
	for ref := range results {
		refs = append(refs, ref)
	}
	if len(refs) != 2 || refs[0].Size != 50<<20 || refs[0].Hash != refs[1].Hash {
		t.Fatalf("large binary copies: %+v", refs)
	}
	entries, _ := os.ReadDir(filepath.Join(p.dir, ".inode", "files"))
	if len(entries) != 1 {
		t.Fatalf("dedup: %v", entries)
	}
	noScratch(t, p)
}
