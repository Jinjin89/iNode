package inode

import (
	"context"
	"errors"
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
run: python3 'count words.py'
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
		"nodes/write.node": "doc: Write\noutputs: {text: text}\nrun: |\n  printf '{\"text\":\"hi\"}'",
		"again.flow":       `steps: {again: {use: retry, in: {times: 2, do: write}}}`,
	})
	if report := p.Check(Definition{Name: "again"}); !strings.Contains(Problems(report.Problems).Error(), "outside effect") {
		t.Fatalf("undeclared effect accepted: %+v", report)
	}
}

func TestProgramFailures(t *testing.T) {
	for _, test := range []struct{ name, command, message string }{
		{"exit", "printf broken >&2; exit 2", "broken"},
		{"exit without stderr", "exit 2", "exit status 2"},
		{"not JSON", "printf oops", "print one JSON object"},
		{"list", "printf '[]'", "print one JSON object"},
		{"null", "printf null", "print one JSON object"},
		{"two objects", "printf '{}{}'", "print one JSON object"},
		{"wrong output", `printf '{"count":"two"}'`, "outputs.count"},
		{"missing output", "printf '{}'", "outputs.count"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := fileProject(t, map[string]string{"nodes/bad.node": "doc: Bad\noutputs: {count: number}\nrun: |\n  " + test.command})
			run, err := p.Start(context.Background(), "bad", nil)
			if err == nil || !strings.Contains(err.Error(), test.message) || run == nil || run.State != "failed" {
				t.Fatalf("got %+v, %v; want %s", run, err, test.message)
			}
		})
	}
}

func TestCancelProgram(t *testing.T) {
	p := fileProject(t, map[string]string{
		"nodes/wait.node": "doc: Wait\nrun: exec python3 wait.py",
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
