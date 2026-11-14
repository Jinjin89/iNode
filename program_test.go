package inode

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, source := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func fileProject(t *testing.T, files map[string]string) *Project {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, files)
	p, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProgramNodes(t *testing.T) {
	p := fileProject(t, map[string]string{
		"nodes/words.node": `doc: Count words
inputs: {text: text}
outputs: {count: number}
no_outside_effect: true
run: [python3, "count words.py"]
`,
		"nodes/count words.py": `import json, sys
json.dump({"count": len(json.load(sys.stdin)["text"].split())}, sys.stdout)
`,
		"count.flow": `inputs: {text: text}
steps: {count: {use: words, in: {text: =inputs.text}}}
outputs: {count: =count.count}`,
		"counts.flow": `steps: {count: {use: map, in: {over: [a, a cat], do: words}}}
outputs: {counts: =count.result}`,
		"again.flow": `steps: {count: {use: retry, in: {times: 2, do: {use: words, in: {text: a cat}}}}}
outputs: {count: =count.result}`,
	})
	for _, test := range []struct {
		name   string
		inputs Values
		want   Values
	}{
		{"words", Values{"text": "a cat"}, Values{"count": float64(2)}},
		{"count", Values{"text": "a cat"}, Values{"count": float64(2)}},
		{"counts", nil, Values{"counts": []any{float64(1), float64(2)}}},
		{"again", nil, Values{"count": float64(2)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			run, err := p.Start(context.Background(), test.name, test.inputs)
			if err != nil || !reflect.DeepEqual(run.Outputs, test.want) {
				t.Fatalf("got %v, %v; want %v", run, err, test.want)
			}
			if test.name == "counts" {
				saved, err := p.ShowRun(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				call := saved.Steps["count"].Steps["2"]
				if call.Node != "words" || call.Inputs["text"] != "a cat" || call.Outputs["count"] != float64(2) {
					t.Fatalf("program child record: %+v", call)
				}
			}
		})
	}
	node, err := p.Describe("words")
	if err != nil || !node.NoOutsideEffect || node.Doc != "Count words" || node.Outputs["count"].Type.Name != "number" {
		t.Fatalf("program description: %+v, %v", node, err)
	}
	p = fileProject(t, map[string]string{
		"nodes/write.node": "doc: Write\noutputs: {text: text}\nrun: [printf, '{\"text\":\"hi\"}']",
		"again.flow":       `steps: {again: {use: retry, in: {times: 2, do: write}}}`,
	})
	if report := p.Check(Definition{Name: "again"}); !strings.Contains(Problems(report.Problems).Error(), "outside effect") {
		t.Fatalf("undeclared effect accepted: %+v", report)
	}
}

func TestProgramFailures(t *testing.T) {
	for _, test := range []struct{ name, script, message string }{
		{"exit", `sys.stderr.write("broken"); sys.exit(2)`, "broken"},
		{"exit without stderr", "sys.exit(2)", "exit status 2"},
		{"not JSON", `print("oops")`, "print one JSON object"},
		{"list", `print("[]")`, "print one JSON object"},
		{"null", `print("null")`, "print one JSON object"},
		{"two objects", `print("{}{}")`, "print one JSON object"},
		{"wrong output", `print('{"count":"two"}')`, "outputs.count"},
		{"missing output", `print("{}")`, "outputs.count"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := fileProject(t, map[string]string{"nodes/bad.node": "doc: Bad\noutputs: {count: number}\nrun: python3 bad.py", "nodes/bad.py": "import sys\n" + test.script})
			run, err := p.Start(context.Background(), "bad", nil)
			if err == nil || !strings.Contains(err.Error(), test.message) || run == nil || run.State != "failed" {
				t.Fatalf("got %+v, %v; want %s", run, err, test.message)
			}
		})
	}
}

func TestCancelProgram(t *testing.T) {
	p := fileProject(t, map[string]string{
		"nodes/wait.node": "doc: Wait\nrun: python3 wait.py",
		"nodes/wait.py": `import json, os, signal, sys
json.load(sys.stdin)
with open("ready", "w") as ready:
    ready.write(str(os.getpid()))
signal.pause()
`,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		run *Record
		err error
	}
	done := make(chan result, 1)
	go func() { run, err := p.Start(ctx, "wait", nil); done <- result{run, err} }()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	var pid int
	for pid == 0 {
		select {
		case result := <-done:
			t.Fatalf("program stopped before cancellation: %+v", result)
		case <-deadline:
			t.Fatal("program did not start")
		case <-tick.C:
			data, _ := os.ReadFile(filepath.Join(p.dir, "nodes", "ready"))
			pid, _ = strconv.Atoi(string(data))
		}
	}
	cancel()
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) || result.run.State != "interrupted" {
			t.Fatalf("cancellation: %+v, %v", result.run, result.err)
		}
	case <-deadline:
		t.Fatal("cancellation did not stop the program")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("program %d is still present: %v", pid, err)
	}
}

func TestMalformedDescriptionsAndCollisions(t *testing.T) {
	for _, source := range []string{
		"doc: [", "doc: Bad\nrun: true", "doc: Bad\nrun: cat\ninputs: {x: nope}",
		"doc: Bad\nrun: cat\ninnputs: {}", "doc: Bad", "run: cat",
	} {
		p := fileProject(t, map[string]string{"nodes/bad.node": source})
		if report := p.Check(Definition{Name: "bad"}); len(report.Problems) == 0 || report.Problems[0].Step != "bad" {
			t.Fatalf("bad description: %+v", report)
		}
		if _, err := p.Start(context.Background(), "text.upper", Values{"text": "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"text.upper", "own"} {
		p := fileProject(t, map[string]string{
			"own.flow": "outputs: {text: hi}", "nodes/" + name + ".node": "doc: Duplicate\nrun: cat",
		})
		if report := p.Check(Definition{Name: name}); !strings.Contains(Problems(report.Problems).Error(), "more than one definition") {
			t.Fatalf("catalog collision: %+v", report)
		}
	}
}

func describedProgram(t *testing.T, description Values, script string) *Project {
	t.Helper()
	description["doc"] = "A test program"
	data, err := json.Marshal(description)
	if err != nil {
		t.Fatal(err)
	}
	return fileProject(t, map[string]string{"nodes/probe.node": string(data), "nodes/probe.py": script})
}

func TestProgramArguments(t *testing.T) {
	p := describedProgram(t, Values{
		"inputs":  Values{"value": "text", "more": "list of text", "enabled": Values{"type": "bool", "default": false}, "options": Values{"type": "list of text", "default": []any{}}, "n": Values{"type": "number", "default": 3}},
		"outputs": Values{"args": "list of text"},
		"run": []any{"python3", "probe.py", "literal", "=inputs.value", "=inputs.more",
			Values{"flag": "--verbose", "value": "=inputs.enabled"}, Values{"flag": "--option", "value": "=inputs.options"}, Values{"flag": "-n", "value": "=inputs.n"}, "==literal"},
	}, "import json, sys\njson.dump({'args': sys.argv[1:]}, sys.stdout)\n")
	dangerous := []any{"space in one argument", `quotes ' "`, ";", "$(touch marker)", "line\nbreak", "=inputs.not_evaluated"}
	for _, enabled := range []bool{false, true} {
		inputs := Values{"value": "hello world", "more": dangerous}
		want := append([]any{"literal", "hello world"}, dangerous...)
		if enabled {
			inputs["enabled"], inputs["options"] = true, []any{"x y", "z;$(stuff)"}
			want = append(want, "--verbose", "--option", "x y", "--option", "z;$(stuff)")
		}
		want = append(want, "-n", "3", "=literal")
		run, err := p.Start(t.Context(), "probe", inputs)
		if err != nil || !reflect.DeepEqual(run.Outputs["args"], want) {
			t.Fatalf("argv: %+v, %v; want %v", run, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(p.dir, "nodes", "marker")); !os.IsNotExist(err) {
		t.Fatal("argument value was executed")
	}
}

func TestProgramNumbers(t *testing.T) {
	p := describedProgram(t, Values{
		"inputs": Values{"n": "number", "values": "list of number"}, "outputs": Values{"args": "list of text", "env": "text"},
		"run": Values{"args": []any{"python3", "probe.py", "=inputs.n", Values{"flag": "--number", "value": "=inputs.n"}, "=inputs.values"}, "env": Values{"NUMBER": "=inputs.n"}},
	}, "import json,os,sys\njson.dump({'args':sys.argv[1:],'env':os.environ['NUMBER']},sys.stdout)\n")
	for _, test := range []struct {
		value float64
		text  string
	}{
		{1000000, "1000000"}, {1e21, "1000000000000000000000"}, {0.1, "0.1"}, {-3, "-3"},
		{1e-7, "0.0000001"}, {1.0 / 3, "0.3333333333333333"},
	} {
		t.Run(test.text, func(t *testing.T) {
			run, err := p.Start(t.Context(), "probe", Values{"n": test.value, "values": []any{test.value, test.value}})
			want := Values{"args": []any{test.text, "--number", test.text, test.text, test.text}, "env": test.text}
			if err != nil || !reflect.DeepEqual(run.Outputs, want) {
				t.Fatalf("decimal arguments and environment: %+v, %v; want %v", run, err, want)
			}
		})
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if run, err := p.Start(t.Context(), "probe", Values{"n": value, "values": []any{}}); err == nil || run != nil || !strings.Contains(err.Error(), "JSON-compatible") {
			t.Fatalf("non-finite input must be refused before starting: %+v, %v", run, err)
		}
	}
}

func TestProgramStderrLimit(t *testing.T) {
	ending := "\nfinal reason\n"
	p := describedProgram(t, Values{"run": "python3 probe.py"},
		"import sys\nsys.stderr.write('discard this beginning\\n'+'x'*"+strconv.Itoa(stderrLimit*256)+"+"+strconv.Quote(ending)+")\nsys.exit(9)\n")
	run, err := p.Start(t.Context(), "probe", nil)
	if err == nil || run.State != "failed" || !strings.Contains(err.Error(), `program "python3": exit status 9`) {
		t.Fatalf("noisy failure: %+v, %v", run, err)
	}
	notice := "[stderr truncated; showing last " + strconv.Itoa(stderrLimit) + " bytes]\n"
	_, tail, found := strings.Cut(run.Error, notice)
	want := strings.TrimSpace(strings.Repeat("x", stderrLimit-len(ending)) + ending)
	if !found || tail != want || strings.Contains(run.Error, "discard this beginning") {
		t.Fatalf("stderr must retain only its bounded ending and mark truncation: %d bytes", len(run.Error))
	}
	saved, err := p.ShowRun(run.ID)
	if err != nil || saved.Error != run.Error {
		t.Fatalf("saved stderr differs: %+v, %v", saved, err)
	}
}

func TestProgramStreams(t *testing.T) {
	for _, test := range []struct {
		name, outputType, data string
		want                   any
	}{
		{"text", "text", "  hello\nworld\n", "  hello\nworld\n"},
		{"number", "number", " \t42\n", float64(42)},
		{"bool", "bool", "true\n", true},
		{"list", "list of number", "[1,2]\n", []any{float64(1), float64(2)}},
		{"object", "object{n: number}", "{\"n\":2}\n", Values{"n": float64(2)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := describedProgram(t, Values{"inputs": Values{"text": "text"}, "outputs": Values{"value": test.outputType},
				"run": Values{"args": []any{"python3", "probe.py"}, "stdin": "=inputs.text", "stdout": "value"}},
				"import sys\nsys.stdout.write(sys.stdin.read())\n")
			run, err := p.Start(t.Context(), "probe", Values{"text": test.data})
			if err != nil || !reflect.DeepEqual(run.Outputs["value"], test.want) {
				t.Fatalf("stdout: %+v, %v; want %v", run, err, test.want)
			}
		})
	}
	// Each stream binding leaves the other stream's JSON default intact.
	p := describedProgram(t, Values{"inputs": Values{"text": "text"}, "outputs": Values{"text": "text"},
		"run": Values{"args": []any{"python3", "probe.py"}, "stdin": "=inputs.text"}},
		"import json,sys\njson.dump({'text':sys.stdin.read()},sys.stdout)\n")
	run, err := p.Start(t.Context(), "probe", Values{"text": "\";$(literal)\n"})
	if err != nil || run.Outputs["text"] != "\";$(literal)\n" {
		t.Fatalf("stdin-only binding: %+v, %v", run, err)
	}
	p = describedProgram(t, Values{"inputs": Values{"text": "text"}, "outputs": Values{"text": "text"},
		"run": Values{"args": []any{"python3", "probe.py"}, "stdout": "text"}},
		"import json,sys\nline=sys.stdin.readline()\nassert line.endswith('\\n')\nsys.stdout.write(json.loads(line)['text'])\n")
	run, err = p.Start(t.Context(), "probe", Values{"text": "literal\n"})
	if err != nil || run.Outputs["text"] != "literal\n" {
		t.Fatalf("stdout-only binding: %+v, %v", run, err)
	}
}

func TestProgramEnvironment(t *testing.T) {
	p := describedProgram(t, Values{"inputs": Values{"text": "text"}, "outputs": Values{"values": "list of text"},
		"run": Values{"args": []any{"python3", "probe.py"}, "env": Values{"TEXT": "=inputs.text", "NUMBER": 12, "BOOL": true}}},
		"import json,os,sys\nassert os.environ['PATH']\njson.dump({'values':[os.environ[k] for k in ['TEXT','NUMBER','BOOL']]},sys.stdout)\n")
	value := "spaces; quotes'\" $(data)\n"
	run, err := p.Start(t.Context(), "probe", Values{"text": value})
	if err != nil || !reflect.DeepEqual(run.Outputs["values"], []any{value, "12", "true"}) {
		t.Fatalf("environment: %+v, %v", run, err)
	}
}

func TestProgramBoundOutputProblems(t *testing.T) {
	for _, test := range []struct{ data, message string }{
		{"two", "run.stdout: output \"count\" needs one JSON number value"},
		{"1 2", "run.stdout: output \"count\" needs one JSON number value"},
		{`"two"`, "outputs.count"},
	} {
		p := describedProgram(t, Values{"inputs": Values{"text": "text"}, "outputs": Values{"count": "number"},
			"run": Values{"args": []any{"python3", "probe.py"}, "stdin": "=inputs.text", "stdout": "count"}},
			"import sys\nsys.stdout.write(sys.stdin.read())\n")
		run, err := p.Start(t.Context(), "probe", Values{"text": test.data})
		if err == nil || !strings.Contains(err.Error(), test.message) || run.State != "failed" {
			t.Fatalf("bad bound output: %+v, %v", run, err)
		}
	}
}

func TestProgramSecretsInStreams(t *testing.T) {
	for _, stream := range []string{"env", "stdin"} {
		for _, fail := range []bool{false, true} {
			t.Run(stream+strconv.FormatBool(fail), func(t *testing.T) {
				command := Values{"args": []any{"python3", "probe.py"}, "stdout": "size"}
				read := "sys.stdin.read()"
				if stream == "env" {
					command["env"] = Values{"TOKEN": "=inputs.token"}
					read = "os.environ['TOKEN']"
				} else {
					command["stdin"] = "=inputs.token"
				}
				script := "import os,sys\nvalue = " + read + "\n"
				if fail {
					script += "sys.stderr.write('denied: '+value)\nsys.exit(7)\n"
				} else {
					script += "print(len(value))\n"
				}
				p := describedProgram(t, Values{"inputs": Values{"token": "secret"}, "outputs": Values{"size": "number"}, "run": command}, script)
				secret := "private token; with spaces"
				if err := p.SetSecret("credential", secret); err != nil {
					t.Fatal(err)
				}
				run, err := p.Start(t.Context(), "probe", Values{"token": "credential"})
				if fail {
					if err == nil || !strings.Contains(err.Error(), "exit status 7") || !strings.Contains(err.Error(), "denied: [secret credential]") || strings.Contains(err.Error(), secret) {
						t.Fatalf("secret failure: %+v, %v", run, err)
					}
				} else if err != nil || run.Outputs["size"] != float64(len(secret)) {
					t.Fatalf("secret passed by %s: %+v, %v", stream, run, err)
				}
				data, err := os.ReadFile(filepath.Join(p.dir, ".inode", "runs", run.ID+".json"))
				if err != nil || strings.Contains(string(data), secret) || run.Inputs["token"] != "credential" {
					t.Fatalf("secret in record: %s, %v", data, err)
				}
			})
		}
	}
}

func TestProgramBindingProblems(t *testing.T) {
	for _, test := range []struct {
		name            string
		inputs, outputs Values
		command         any
		key, help       string
	}{
		{"unknown argument input", Values{"text": "text"}, Values{}, []any{"echo", "=inputs.absent"}, "run.args.1", "declared input path"},
		{"unknown stdin input", Values{}, Values{}, Values{"args": []any{"cat"}, "stdin": "=inputs.absent"}, "run.stdin", "declared input path"},
		{"unknown env input", Values{}, Values{}, Values{"args": []any{"cat"}, "env": Values{"TOKEN": "=inputs.absent"}}, "run.env.TOKEN", "declared input path"},
		{"secret argument", Values{"token": "secret"}, Values{}, []any{"echo", "=inputs.token"}, "run.args.1", "stdin or env"},
		{"secret flag", Values{"token": "secret"}, Values{}, []any{"echo", Values{"flag": "--token", "value": "=inputs.token"}}, "run.args.1.value", "stdin or env"},
		{"nested secret argument", Values{"data": "object{token: secret}"}, Values{}, []any{"echo", "=inputs.data.token"}, "run.args.1", "stdin or env"},
		{"secret list argument", Values{"tokens": "list of secret"}, Values{}, []any{"echo", "=inputs.tokens"}, "run.args.1", "stdin or env"},
		{"object flag", Values{"data": "object"}, Values{}, []any{"echo", Values{"flag": "--x", "value": "=inputs.data"}}, "run.args.1.value", "use text"},
		{"two stdout outputs", Values{}, Values{"a": "text", "b": "text"}, Values{"args": []any{"cat"}, "stdout": []any{"a", "b"}}, "run.stdout", "one output"},
		{"unfilled output", Values{}, Values{"a": "text", "b": "text"}, Values{"args": []any{"cat"}, "stdout": "a"}, "run.stdout", "one object"},
		{"shell string", Values{}, Values{}, "echo hi; touch marker", "run", "argument list"},
		{"shell expansion", Values{}, Values{}, "echo $(touch marker)", "run", "argument list"},
		{"dynamic executable", Values{"name": "text"}, Values{}, []any{"=inputs.name"}, "run.args.0", "fixed program"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := describedProgram(t, Values{"inputs": test.inputs, "outputs": test.outputs, "run": test.command}, "raise Exception('must not run')\n")
			_, err := p.Describe("probe")
			if err == nil || !strings.Contains(err.Error(), "probe.node") || !strings.Contains(err.Error(), test.key) || !strings.Contains(err.Error(), test.help) {
				t.Fatalf("load problem: %v; want file, %s, %s", err, test.key, test.help)
			}
			if _, err := p.Start(t.Context(), "text.upper", Values{"text": "hi"}); err != nil {
				t.Fatal("invalid node damaged the catalog:", err)
			}
		})
	}
}

func TestMissingProgram(t *testing.T) {
	p := describedProgram(t, Values{"run": "inode-program-that-does-not-exist"}, "")
	run, err := p.Start(t.Context(), "probe", nil)
	if err == nil || !strings.Contains(err.Error(), "inode-program-that-does-not-exist") || !strings.Contains(err.Error(), "install it or correct") || run.State != "failed" {
		t.Fatalf("missing program: %+v, %v", run, err)
	}
}
