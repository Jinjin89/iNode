package inode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func project(t *testing.T, flows map[string]string) *Project {
	t.Helper()
	dir := t.TempDir()
	for name, source := range flows {
		if err := os.WriteFile(filepath.Join(dir, name+".flow"), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestChecker(t *testing.T) {
	tests := []struct {
		name, flow, step, input, message string
	}{
		{"unknown node", `steps: {bad: {use: absent}}`, "x/bad", "use", "unknown node"},
		{"unknown port", `steps: {bad: {use: text.upper, in: {text: hi, extra: 1}}}`, "x/bad", "extra", "unknown port"},
		{"missing input", `steps: {bad: {use: text.upper}}`, "x/bad", "text", "required port"},
		{"literal mismatch", `steps: {bad: {use: text.upper, in: {text: 1}}}`, "x/bad", "text", "needs text"},
		{"reference mismatch", `steps: {n: {use: number.add, in: {a: 1, b: 2}}, bad: {use: text.upper, in: {text: =n.value}}}`, "x/bad", "text", "needs text"},
		{"missing step", `steps: {bad: {use: text.upper, in: {text: =absent.text}}}`, "x/bad", "text", `no field "absent"`},
		{"missing output", `steps: {a: {use: text.upper, in: {text: hi}}, bad: {use: text.upper, in: {text: =a.absent}}}`, "x/bad", "text", `no field "absent"`},
		{"missing workflow input", `steps: {bad: {use: text.upper, in: {text: =inputs.absent}}}`, "x/bad", "text", `no field "absent"`},
		{"primitive path", `steps: {a: {use: text.upper, in: {text: hi}}, bad: {use: text.upper, in: {text: =a.text.name}}}`, "x/bad", "text", `no field "name"`},
		{"list element", `steps: {bad: {use: text.join, in: {parts: [hi, 2]}}}`, "x/bad", "parts", "item 1"},
		{"typed list", "inputs: {parts: list of number}\nsteps: {bad: {use: text.join, in: {parts: =inputs.parts}}}", "x/bad", "parts", "needs text"},
		{"unsupported type", `inputs: {x: secret}`, "x", "x", "unsupported type"},
		{"invalid default", `inputs: {x: {type: text, default: 3}}`, "x", "x", "fix the default"},
		{"missing after", `steps: {bad: {use: data.value, in: {value: 1}, after: [absent]}}`, "x/bad", "after", "unknown step"},
		{"data cycle", `steps: {a: {use: text.upper, in: {text: =b.text}}, b: {use: text.upper, in: {text: =a.text}}}`, "x/b", "text", "cycle"},
		{"after cycle", `steps: {a: {use: data.value, in: {value: 1}, after: [b]}, b: {use: data.value, in: {value: 2}, after: [a]}}`, "x/b", "after", "cycle"},
		{"mixed cycle", `steps: {a: {use: data.value, in: {value: =b.value}}, b: {use: data.value, in: {value: 2}, after: [a]}}`, "x/b", "after", "cycle"},
		{"workflow output", `outputs: {text: =absent.text}`, "x", "outputs.text", `no field "absent"`},
		{"self reference", `steps: {bad: {use: x}}`, "x/bad", "use", "calls itself"},
		{"malformed reference", `steps: {bad: {use: text.upper, in: {text: =oops}}}`, "x/bad", "text", "needs a step and output"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p := project(t, map[string]string{"x": test.flow})
			report := p.Check(Definition{Name: "x.flow"})
			for _, problem := range report.Problems {
				if problem.Step == test.step && problem.Input == test.input && strings.Contains(problem.Message, test.message) {
					return
				}
			}
			t.Fatalf("wanted %s / %s / %s; got %+v", test.step, test.input, test.message, report.Problems)
		})
	}
	if problems := project(t, nil).Check(Definition{Name: "missing"}).Problems; len(problems) != 1 || problems[0].Input != "use" {
		t.Fatalf("unknown root: %+v", problems)
	}
	p := project(t, map[string]string{"a": `steps: {b: {use: b}}`, "b": `steps: {a: {use: a}}`})
	if report := p.Check(Definition{Name: "a"}); !strings.Contains(Problems(report.Problems).Error(), "calls itself") {
		t.Fatalf("indirect workflow cycle: %+v", report)
	}
}

func TestPreflightHasNoEffects(t *testing.T) {
	p := project(t, map[string]string{"x": `steps: {write: {use: file.write, in: {path: marker, text: hi}}, bad: {use: absent}}`})
	if run, err := p.Start(context.Background(), "x", nil); err == nil || run != nil {
		t.Fatalf("invalid graph started: %v, %v", run, err)
	}
	for _, name := range []string{"marker", ".inode"} {
		if _, err := os.Stat(filepath.Join(p.dir, name)); !os.IsNotExist(err) {
			t.Fatalf("preflight created %s", name)
		}
	}
	for _, inputs := range []Values{{}, {"text": 1}, {"text": "hi", "extra": true}} {
		if run, err := p.Start(context.Background(), "text.upper", inputs); err == nil || run != nil {
			t.Fatalf("invalid root inputs started: %v, %v", run, err)
		}
	}
}

func TestExamples(t *testing.T) {
	flows := map[string]string{}
	for _, name := range []string{"upper", "word_report", "greeting"} {
		data, err := os.ReadFile(filepath.Join("examples", name+".flow"))
		if err != nil {
			t.Fatal(err)
		}
		flows[name] = string(data)
	}
	p := project(t, flows)
	for _, test := range []struct {
		name   string
		inputs Values
		want   Values
	}{
		{"upper", Values{"text": "hi"}, Values{"text": "HI"}},
		{"word_report", Values{"text": "a cat"}, Values{"report": Values{"original": "a cat", "count": float64(2), "versions": []any{"a cat", "A CAT"}}}},
		{"greeting", Values{"name": "sam"}, Values{"text": "Hello SAM"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			run, err := p.Start(context.Background(), test.name, test.inputs)
			if err != nil || !reflect.DeepEqual(run.Outputs, test.want) {
				t.Fatalf("got %v, %v; want %v", run, err, test.want)
			}
			saved, err := p.ShowRun(run.ID)
			expected, dataErr := jsonData[any](run)
			actual, readErr := jsonData[any](saved)
			if err != nil || dataErr != nil || readErr != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatalf("record round trip: %v, %v", saved, err)
			}
			if test.name == "greeting" {
				child := saved.Steps["name"]
				if child.Node != "upper" || child.Inputs["text"] != "sam" || child.Outputs["text"] != "SAM" || child.Steps["upper"].Outputs["text"] != "SAM" || saved.Steps["greeting"].Inputs["separator"] != " " {
					t.Fatalf("nested records or filled inputs missing: %+v", saved)
				}
			}
			if saved.Workflow == nil {
				t.Fatal("workflow snapshot missing")
			}
		})
	}
	runs, err := p.Runs()
	if err != nil || len(runs) != 3 {
		t.Fatalf("want 3 user-started runs: %v, %v", runs, err)
	}
}

func TestDefaultsAndPlainData(t *testing.T) {
	p := project(t, map[string]string{"x": `
inputs:
  text: {type: text, default: ""}
  number: {type: number, default: 0}
  flag: {type: bool, default: false}
  list: {type: list of text, default: []}
outputs:
  value: {text: "=inputs.text", number: "=inputs.number", flag: "=inputs.flag", list: "=inputs.list"}
  literal: "==inputs.text"
`})
	run, err := p.Start(context.Background(), "x", nil)
	want := Values{"value": Values{"text": "", "number": float64(0), "flag": false, "list": []any{}}, "literal": "=inputs.text"}
	if err != nil || !reflect.DeepEqual(run.Outputs, want) {
		t.Fatalf("defaults: %v, %v", run, err)
	}
}

func TestUncheckedEdges(t *testing.T) {
	for _, test := range []struct {
		name, value, reference, message string
	}{
		{"valid", `{user: {name: hi}}`, "source.value.user.name", ""},
		{"missing field", `{user: {}}`, "source.value.user.name", "no field \"name\""},
		{"wrong type", `{user: {name: false}}`, "source.value.user.name", "needs text"},
		{"list index", `[hi]`, "source.value.0", ""},
		{"list index spelling", `[hi]`, "source.value.00", ""},
		{"missing index", `[hi]`, "source.value.1", `list index "1" is invalid or out of range`},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := project(t, map[string]string{"x": "steps:\n  source: {use: data.value, in: {value: " + test.value + "}}\n  upper: {use: text.upper, in: {text: =" + test.reference + "}}\noutputs: {text: =upper.text}"})
			report := p.Check(Definition{Name: "x"})
			if len(report.Problems) != 0 || len(report.Unchecked) == 0 {
				t.Fatalf("unchecked connection: %+v", report)
			}
			run, err := p.Start(context.Background(), "x", nil)
			if test.message == "" {
				if err != nil || run.Outputs["text"] != "HI" {
					t.Fatalf("got %v, %v", run, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.message) || !strings.Contains(err.Error(), "x/upper") || run.Steps["upper"].State != "failed" || !run.Steps["upper"].Started.IsZero() {
				t.Fatalf("bad value reached consumer: %v, %v", run, err)
			}
		})
	}
}

func TestOutputEdges(t *testing.T) {
	for _, outputs := range []Values{{"value": "wrong"}, {}, {"value": 1, "extra": true}} {
		p := project(t, nil)
		p.nodes["bad"] = Node{Name: "bad", Outputs: Ports{"value": {Type: numberType}}, run: func(context.Context, Values, Call) (Values, error) { return outputs, nil }}
		run, err := p.Start(context.Background(), "bad", nil)
		if err == nil || !strings.Contains(err.Error(), "outputs.") || run.State != "failed" {
			t.Fatalf("invalid outputs accepted: %v, %v", run, err)
		}
	}
}

func TestFanoutRunsOnce(t *testing.T) {
	p := project(t, map[string]string{"x": `
steps:
  n: {use: count}
  a: {use: number.add, in: {a: =n.value, b: 1}}
  b: {use: number.add, in: {a: =n.value, b: 2}}
outputs: {a: =a.value, b: =b.value}
`})
	var calls atomic.Int32
	p.nodes["count"] = Node{Name: "count", Outputs: Ports{"value": {Type: numberType}}, run: func(context.Context, Values, Call) (Values, error) { return Values{"value": calls.Add(1)}, nil }}
	run, err := p.Start(context.Background(), "x", nil)
	if err != nil || calls.Load() != 1 || run.Outputs["a"] != float64(2) || run.Outputs["b"] != float64(3) {
		t.Fatalf("fanout: %v, %v, calls %d", run, err, calls.Load())
	}
}

func TestFailureDrainsStartedWorkAndStartsNothingElse(t *testing.T) {
	p := project(t, map[string]string{"x": `
steps:
  hold: {use: hold}
  source: {use: source}
  fail: {use: text.upper, in: {text: =source.value}}
  blocked: {use: text.upper, in: {text: no}, after: [fail]}
  newly_ready: {use: text.upper, in: {text: no}, after: [hold]}
`})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	p.nodes["hold"] = Node{Name: "hold", run: func(ctx context.Context, _ Values, _ Call) (Values, error) {
		close(started)
		select {
		case <-release:
			return Values{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	p.nodes["source"] = Node{Name: "source", Outputs: Ports{"value": {Type: anyType}}, run: func(ctx context.Context, _ Values, _ Call) (Values, error) {
		select {
		case <-started:
			return Values{"value": false}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	done := make(chan *Record, 1)
	go func() { run, _ := p.Start(ctx, "x", nil); done <- run }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	observed := false
	for !observed {
		select {
		case <-ctx.Done():
			t.Fatal("independent steps did not start in parallel or failure was not recorded")
		case <-ticker.C:
			runs, err := p.Runs()
			if err != nil {
				t.Fatal(err)
			}
			observed = len(runs) == 1 && runs[0].Steps["fail"].State == "failed"
		}
	}
	select {
	case <-done:
		t.Fatal("run failed before already-started work finished")
	default:
	}
	close(release)
	run := <-done
	if run.State != "failed" || !strings.Contains(run.Error, "fail") || run.Steps["hold"].State != "done" || run.Steps["blocked"].State != "waiting" || run.Steps["newly_ready"].State != "waiting" {
		t.Fatalf("failure boundary: %+v", run)
	}
}

func TestOperationFailure(t *testing.T) {
	p := project(t, map[string]string{"x": `
steps:
  fail: {use: number.divide, in: {a: 1, b: 0}}
  next: {use: text.upper, in: {text: hi}, after: [fail]}
`})
	run, err := p.Start(context.Background(), "x", nil)
	if err == nil || !strings.Contains(err.Error(), "x/fail") || run.State != "failed" || run.Steps["fail"].State != "failed" || run.Steps["next"].State != "waiting" {
		t.Fatalf("operation failure did not propagate: %+v, %v", run, err)
	}
}

func TestFileOrderingAndEffectInference(t *testing.T) {
	p := project(t, map[string]string{
		"read": `steps: {r: {use: file.read, in: {path: note.txt}}}
outputs: {text: =r.text}`,
		"write": `steps: {w: {use: file.write, in: {path: note.txt, text: hi}}, r: {use: read, after: [w]}}
outputs: {text: =r.text}`,
	})
	for name, want := range map[string]bool{"read": true, "write": false} {
		node, err := p.Describe(name)
		if err != nil || node.NoOutsideEffect != want {
			t.Fatalf("effect %s: %v, %v", name, node, err)
		}
	}
	run, err := p.Start(context.Background(), "write", nil)
	if err != nil || run.Outputs["text"] != "hi" {
		t.Fatalf("after ordering: %v, %v", run, err)
	}
	data, err := os.ReadFile(filepath.Join(p.dir, "note.txt"))
	if err != nil || string(data) != "hi" {
		t.Fatalf("project path: %q, %v", data, err)
	}
}

func TestHTTPAndHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte(`<p>tiny <b>page</b></p><script>hidden</script><style>hidden</style>`))
	}))
	defer server.Close()
	p := project(t, map[string]string{"x": `
inputs: {url: text}
steps:
  page: {use: http.get, in: {url: =inputs.url}}
  plain: {use: html.to_text, in: {html: =page.body}}
outputs: {text: =plain.text, status: =page.status}
`})
	run, err := p.Start(context.Background(), "x", Values{"url": server.URL})
	if err != nil || run.Outputs["text"] != "tiny page" || run.Outputs["status"] != float64(418) {
		t.Fatalf("HTTP/HTML: %v, %v", run, err)
	}
}

func TestCheckedSnapshot(t *testing.T) {
	p := project(t, map[string]string{"x": "view: {position: [1, 2]}\nsteps: {a: {use: text.upper, in: {text: hi}}}\noutputs: {text: =a.text}"})
	if err := os.WriteFile(filepath.Join(p.dir, "x.flow"), []byte("steps: {bad: {use: absent}}"), 0600); err != nil {
		t.Fatal(err)
	}
	run, err := p.Start(context.Background(), "x", nil)
	if err != nil || run.Outputs["text"] != "HI" || run.Workflow.View == nil {
		t.Fatalf("snapshot changed: %v, %v", run, err)
	}
}

func TestUnknownPathsInsideContainers(t *testing.T) {
	p := project(t, map[string]string{"x": `
steps:
  source: {use: data.value, in: {value: {name: hi}}}
  copy: {use: data.value, in: {value: {names: [=source.value.name]}}}
outputs: {name: =source.value.name}
`})
	report := p.Check(Definition{Name: "x"})
	if len(report.Problems) != 0 || len(report.Unchecked) != 2 || report.Unchecked[0].Input != "value.names.0" || report.Unchecked[1].Input != "outputs.name" {
		t.Fatalf("unknown paths must remain visible inside containers and outputs: %+v", report)
	}
}

func TestInputAndListTypes(t *testing.T) {
	p := project(t, map[string]string{"x": `
inputs: {flag: bool, payload: object, items: list, numbers: list of list of number}
outputs: {flag: =inputs.flag, payload: =inputs.payload, items: =inputs.items, numbers: =inputs.numbers}
`})
	inputs := Values{"flag": true, "payload": Values{"n": 1}, "items": []any{true, "hi"}, "numbers": []any{[]any{1, 2}}}
	run, err := p.Start(context.Background(), "x", inputs)
	want, _ := jsonData(inputs)
	if err != nil || !reflect.DeepEqual(run.Outputs, want) {
		t.Fatalf("plain data: %v, %v", run, err)
	}
	inputs["numbers"] = []any{[]any{true}}
	if run, err := p.Start(context.Background(), "x", inputs); err == nil || run != nil {
		t.Fatalf("invalid nested list started: %v, %v", run, err)
	}
}

func TestMalformedWorkflowsDoNotBlockHistory(t *testing.T) {
	for _, source := range []string{
		`steps: [`,
		`steps: {a: {use: text.upper, inn: {text: hi}}}`,
		`inputs: {x: {type: text, type: number}}`,
		`inputs: {x: {type: text, defalt: hi}}`,
	} {
		p := project(t, map[string]string{"bad": source})
		if report := p.Check(Definition{Name: "bad"}); len(report.Problems) == 0 || report.Problems[0].Step != "bad" {
			t.Fatalf("malformed workflow not diagnosed: %+v", report)
		}
		run, err := p.Start(context.Background(), "text.upper", Values{"text": "hi"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.ShowRun(run.ID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInterruptedAndUnrecordableRuns(t *testing.T) {
	p := project(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run, err := p.Start(ctx, "file.write", Values{"path": "marker", "text": "hi"})
	if err == nil || run.State != "interrupted" {
		t.Fatalf("cancelled run: %v, %v", run, err)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "marker")); !os.IsNotExist(err) {
		t.Fatal("cancelled run executed its operation")
	}
	p = project(t, nil)
	if err := os.WriteFile(filepath.Join(p.dir, ".inode"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	run, err = p.Start(context.Background(), "file.write", Values{"path": "marker", "text": "hi"})
	if err == nil || run.State != "failed" {
		t.Fatalf("unrecordable run: %v, %v", run, err)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "marker")); !os.IsNotExist(err) {
		t.Fatal("operation executed without its initial record")
	}
}

func TestBuiltinDeclarations(t *testing.T) {
	for name, node := range builtins(t.TempDir()) {
		if node.Doc == "" || node.run == nil {
			t.Fatalf("%s needs a description and implementation", name)
		}
		for _, ports := range []Ports{node.Inputs, node.Outputs} {
			for name, port := range ports {
				var parsed Type
				if err := parsed.UnmarshalText([]byte(port.Type.String())); err != nil || !reflect.DeepEqual(parsed, port.Type) || !portName(name) {
					t.Fatalf("invalid port %s: %+v, %v", name, port, err)
				}
				if port.Default != nil {
					if _, err := match(port.Default, port.Type, nil); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	p := project(t, nil)
	node, err := p.Describe("http.get")
	if err != nil || !node.NoOutsideEffect {
		t.Fatalf("http.get must have no outside effect: %+v, %v", node, err)
	}
}
