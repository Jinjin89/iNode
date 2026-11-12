package inode

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fromFlow = `inputs: {tag: {type: text, default: hi}}
steps:
  a: {use: count, in: {prefix: =inputs.tag}}
  b: {use: number.add, in: {a: =a.value, b: 1}}
  c: {use: number.add, in: {a: =b.value, b: 1}}
  branch: {use: file.write, in: {path: branch, text: hi}}
outputs: {value: =c.value, branch: =branch.path}
`

func fromProject(t *testing.T) *Project {
	t.Helper()
	return fileProject(t, map[string]string{
		"chain.flow": fromFlow,
		"nodes/count.node": `doc: Count calls
inputs: {prefix: text}
outputs: {value: number}
run: python3 count.py`,
		"nodes/count.py": `import json, pathlib, sys
json.load(sys.stdin)
counter = pathlib.Path("counter")
with counter.open("a") as output:
    output.write("x\n")
json.dump({"value": len(counter.read_text().splitlines())}, sys.stdout)
`,
	})
}

func reload(t *testing.T, p *Project) *Project {
	t.Helper()
	next, err := Open(p.dir)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestRunFromHere(t *testing.T) {
	p := fromProject(t)
	client := runningBackend(t, p.dir)
	var first string
	json.Unmarshal(request(t, client, "run", "chain", Values{}, "")[0], &first)
	request(t, client, "runs follow", first)
	writeFiles(t, p.dir, map[string]string{"chain.flow": strings.Replace(fromFlow, "a: =a.value, b: 1", "a: =a.value, b: 10", 1)})
	var preview RunFrom
	json.Unmarshal(request(t, client, "runs from", first, "b", false)[0], &preview)
	if !reflect.DeepEqual(keys(preview.Workflow.Steps), []string{"b", "c"}) || !reflect.DeepEqual(preview.Inputs, Values{"copy1": float64(1), "copy2": "branch"}) || !reflect.DeepEqual(preview.Source.Inputs, map[string][]string{"copy1": {"a", "value"}, "copy2": {"branch", "path"}}) || len(preview.Workflow.Outputs) != 2 {
		t.Fatalf("preview: %+v", preview)
	}
	if runs, _ := p.Runs(); len(runs) != 1 {
		t.Fatal("preview started a run")
	}
	var id string
	json.Unmarshal(request(t, client, "runs from", first, "b", true)[0], &id)
	frames := request(t, client, "runs follow", id)
	var run Record
	json.Unmarshal(frames[len(frames)-1], &run)
	if run.State != "done" || run.Node != "chain from b" || !run.Unsaved || run.Trigger != "" || !reflect.DeepEqual(run.Outputs, Values{"value": float64(12), "branch": "branch"}) || len(run.Steps) != 2 || run.Source.Run != first || run.Source.Step != "b" || !reflect.DeepEqual(run.Inputs, preview.Inputs) {
		t.Fatalf("partial run: %+v", run)
	}
	for _, name := range []string{"nodes/counter", "branch"} {
		text, err := os.ReadFile(filepath.Join(p.dir, name))
		want := "hi"
		if name == "nodes/counter" {
			want = "x\n"
		}
		if err != nil || string(text) != want {
			t.Fatalf("upstream or other branch ran again: %s: %q, %v", name, text, err)
		}
	}
	var again string
	json.Unmarshal(request(t, client, "runs again", id)[0], &again)
	frames = request(t, client, "runs follow", again)
	json.Unmarshal(frames[len(frames)-1], &run)
	if run.State != "done" || run.Source.Run != first || run.Outputs["value"] != float64(12) {
		t.Fatalf("run again lost copied inputs/provenance: %+v", run)
	}
	// Start recomputes the preview; a later upstream edit must be refused.
	writeFiles(t, p.dir, map[string]string{"chain.flow": strings.Replace(fromFlow, "prefix: =inputs.tag", "prefix: bye", 1)})
	if err := client.Call(t.Context(), "runs from", []any{first, "b", true}, func(json.RawMessage) error { return nil }); err == nil || !strings.Contains(err.Error(), "filled-in inputs changed") {
		t.Fatalf("start trusted an old preview: %v", err)
	}
}

func TestRunFromIgnoresUnneededProblems(t *testing.T) {
	p := project(t, map[string]string{"x": `steps:
  a: {use: length, in: {token: demo_token}}
  b: {use: number.add, in: {a: =a.value, b: 1}}
  branch: {use: text.upper, in: {text: hi}}
outputs: {value: =b.value}`})
	p.nodes["length"] = Node{Name: "length", Inputs: Ports{"token": {Type: typ("secret")}}, Outputs: Ports{"value": {Type: numberType}}, run: func(_ context.Context, in Values, _ Call) (Values, error) {
		return Values{"value": len(in["token"].(string))}, nil
	}}
	if err := p.SetSecret("demo_token", "abc"); err != nil {
		t.Fatal(err)
	}
	run, err := p.Start(t.Context(), "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveSecret("demo_token"); err != nil {
		t.Fatal(err)
	}
	flow := *p.nodes["x"].Workflow
	flow.Steps = maps.Clone(flow.Steps)
	flow.Steps["branch"] = Step{Use: Definition{Name: "absent"}}
	node := p.nodes["x"]
	node.Workflow = &flow
	p.nodes["x"] = node
	preview, err := p.From(run, "b")
	if err != nil || len(preview.Workflow.Steps) != 1 || preview.Inputs["copy1"] != float64(3) {
		t.Fatalf("unexecuted branches/upstream secrets were checked as if rerun: %+v, %v", preview, err)
	}
}

func TestRunFromViewsAndLiteralData(t *testing.T) {
	for _, test := range []struct{ name, before, after string }{
		{"inline", `steps:
  a: {use: retry, in: {times: 1, do: {use: {view: {x: 1}, steps: {}, outputs: {text: hi}}}}}
  b: {use: text.upper, in: {text: =a.result}}
outputs: {text: =b.text}`, "x: 2"},
		{"default box", `inputs:
  f: {type: node() gives text, default: {use: {view: {x: 1}, steps: {}, outputs: {text: hi}}}}
steps:
  a: {use: choose, in: {if: true, then: =inputs.f, else: =inputs.f}}
  b: {use: text.upper, in: {text: =a.result}}
outputs: {text: =b.text}`, "x: 2"},
		{"literal", `steps:
  a: {use: data.value, in: {value: {view: {x: 1}}}}
  b: {use: data.value, in: {value: =a.value}}
outputs: {value: =b.value}`, "x: 2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := project(t, map[string]string{"x": test.before})
			run, err := p.Start(t.Context(), "x", nil)
			if err != nil {
				t.Fatal(err)
			}
			writeFiles(t, p.dir, map[string]string{"x.flow": strings.Replace(test.before, "x: 1", test.after, 1) + "\nview: {x: 2}\n"})
			p = reload(t, p)
			_, err = p.From(run, "b")
			if (err != nil) != (test.name == "literal") {
				t.Fatalf("workflow views must be ignored and literal data compared: %v", err)
			}
		})
	}
}

func TestRunFromRefusals(t *testing.T) {
	for _, test := range []struct {
		name, step, message string
		change              func(*testing.T, *Project, *Record)
	}{
		{"not completed", "b", "completed step", func(t *testing.T, p *Project, record *Record) {
			record.Steps["a"].State = "interrupted"
			if err := p.save(record, false); err != nil {
				t.Fatal(err)
			}
		}},
		{"after crossing", "b", "after crosses", func(t *testing.T, p *Project, _ *Record) {
			writeFiles(t, p.dir, map[string]string{"chain.flow": strings.Replace(fromFlow, "b: 1}}", "b: 1}, after: [a]}", 1)})
		}},
		{"arguments", "b", "filled-in inputs changed", func(t *testing.T, p *Project, _ *Record) {
			writeFiles(t, p.dir, map[string]string{"chain.flow": strings.Replace(fromFlow, "prefix: =inputs.tag", "prefix: bye", 1)})
		}},
		{"use", "b", "upstream use", func(t *testing.T, p *Project, _ *Record) {
			description, err := os.ReadFile(filepath.Join(p.dir, "nodes/count.node"))
			if err != nil {
				t.Fatal(err)
			}
			writeFiles(t, p.dir, map[string]string{"nodes/other.node": string(description), "chain.flow": strings.Replace(fromFlow, "use: count", "use: other", 1)})
		}},
		{"filled values", "b", "filled-in inputs changed", func(t *testing.T, p *Project, _ *Record) {
			writeFiles(t, p.dir, map[string]string{"nodes/count.node": "doc: Count calls\ninputs: {prefix: text, suffix: {type: text, default: bye}}\noutputs: {value: number}\nrun: python3 count.py"})
		}},
		{"new ancestor", "b", "upstream use", func(t *testing.T, p *Project, _ *Record) {
			flow := strings.Replace(fromFlow, "prefix: =inputs.tag}}", "prefix: =inputs.tag}, after: [prep]}", 1)
			writeFiles(t, p.dir, map[string]string{"chain.flow": strings.Replace(flow, "steps:\n", "steps:\n  prep: {use: data.value, in: {value: hi}}\n", 1)})
		}},
		{"output branch changed", "b", "filled-in inputs changed", func(t *testing.T, p *Project, _ *Record) {
			writeFiles(t, p.dir, map[string]string{"chain.flow": strings.Replace(fromFlow, "path: branch, text: hi", "path: branch, text: bye", 1)})
		}},
		{"output branch unfinished", "b", "completed step", func(t *testing.T, p *Project, record *Record) {
			record.Steps["branch"].State = "waiting"
			if err := p.save(record, false); err != nil {
				t.Fatal(err)
			}
		}},
		{"iteration", "b/1", "top-level step", func(*testing.T, *Project, *Record) {}},
		{"removed", "b", "removed or renamed", func(t *testing.T, p *Project, _ *Record) {
			writeFiles(t, p.dir, map[string]string{"chain.flow": strings.ReplaceAll(strings.ReplaceAll(fromFlow, "  b:", "  renamed:"), "=b.value", "=renamed.value")})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := fromProject(t)
			run, err := p.Start(t.Context(), "chain", nil)
			if err != nil {
				t.Fatal(err)
			}
			test.change(t, p, run)
			p = reload(t, p)
			_, err = p.From(run, test.step)
			if err == nil || !strings.Contains(err.Error(), test.message) || !strings.Contains(err.Error(), `step "chain`) {
				t.Fatalf("want %s; got %v", test.message, err)
			}
		})
	}
}

func TestRunFromNestedWorkflows(t *testing.T) {
	for _, boxed := range []bool{false, true} {
		p := project(t, map[string]string{
			"inner": "steps: {up: {use: text.upper, in: {text: hi}}}\noutputs: {text: =up.text}",
			"outer": "steps: {a: {use: inner}, b: {use: text.upper, in: {text: =a.text}}}\noutputs: {text: =b.text}",
		})
		if boxed {
			writeFiles(t, p.dir, map[string]string{"outer.flow": "steps: {a: {use: retry, in: {times: 1, do: inner}}, b: {use: text.upper, in: {text: =a.result}}}\noutputs: {text: =b.text}"})
			p = reload(t, p)
		}
		run, err := p.Start(t.Context(), "outer", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.From(run, "b"); err != nil {
			t.Fatal(err)
		}
		writeFiles(t, p.dir, map[string]string{"inner.flow": "steps: {up: {use: text.upper, in: {text: bye}}}\noutputs: {text: =up.text}"})
		p = reload(t, p)
		if _, err := p.From(run, "b"); err == nil || !strings.Contains(err.Error(), "nested workflow changed") {
			t.Fatalf("nested workflow change ignored (boxed=%v): %v", boxed, err)
		}
	}
}

func TestRunFromCopiesReferencesWithoutEnteringBoxScope(t *testing.T) {
	p := project(t, map[string]string{"x": `inputs: {prefix: {type: text, default: yo}}
steps:
  a: {use: text.upper, in: {text: hi}}
  b:
    use: map
    in:
      over: [=a.text]
      do:
        use:
          inputs: {prefix: text, text: text}
          steps: {join: {use: text.join, in: {parts: [=inputs.prefix, =inputs.text]}}}
          outputs: {text: =join.text}
        in: {prefix: =inputs.prefix}
  c: {use: data.value, in: {value: {words: =b.result, other: =a.text}}}
outputs: {value: =c.value}`})
	run, err := p.Start(t.Context(), "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	past, _ := p.ShowRun(run.ID)
	current, _, report := p.prepare(Definition{Name: "x"}, past.Inputs)
	before, _ := json.Marshal([]any{past, current})
	preview, err := buildFrom(past, current, report, "b", p.nodes)
	after, _ := json.Marshal([]any{past, current})
	if err != nil || string(before) != string(after) {
		t.Fatalf("builder changed its arguments: %v", err)
	}
	e, err := p.begin(newRun(Definition{Workflow: preview.Workflow}, preview.Inputs, "partial"))
	if err == nil {
		err = e.run(context.Background())
	}
	if err != nil || !reflect.DeepEqual(e.root.Outputs, run.Outputs) || len(preview.Inputs) != 2 || len(preview.Source.Inputs) != 2 {
		t.Fatalf("references/box scope: %+v, %v", e.root, err)
	}
}

func TestRunFromUnsavedTriggerAndCodeBoundary(t *testing.T) {
	p := fromProject(t)
	e, err := p.begin(newRun(Definition{Workflow: p.nodes["chain"].Workflow}, nil, "draft"))
	if err == nil {
		err = e.run(t.Context())
	}
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, p.dir, map[string]string{"chain.flow": "steps: {bad: {use: absent}}", "nodes/count.py": "raise Exception('changed code')"})
	p = reload(t, p)
	preview, err := p.From(e.root, "b")
	if err != nil || preview.Inputs["copy1"] != float64(1) {
		t.Fatalf("unsaved snapshot or code-change boundary: %+v, %v", preview, err)
	}
	e.root.Unsaved, e.root.Node, e.root.Trigger = false, "chain", "daily-1"
	writeFiles(t, p.dir, map[string]string{"chain.flow": fromFlow})
	if err := p.save(e.root, false); err != nil {
		t.Fatal(err)
	}
	p = reload(t, p)
	if _, err := p.From(e.root, "b"); err != nil {
		t.Fatalf("trigger-started run refused: %v", err)
	}
}

func TestRunFromEquivalentInputs(t *testing.T) {
	for _, replacement := range []string{
		strings.Replace(fromFlow, "default: hi", "default: bye", 1),
		strings.Replace(fromFlow, "prefix: =inputs.tag", "prefix: hi", 1),
	} {
		p := fromProject(t)
		run, err := p.Start(t.Context(), "chain", nil)
		if err != nil {
			t.Fatal(err)
		}
		writeFiles(t, p.dir, map[string]string{"chain.flow": replacement})
		if _, err := reload(t, p).From(run, "b"); err != nil {
			t.Fatalf("the received values did not change: %v", err)
		}
	}
}

func TestRunFromCapturedDependencyWithProblem(t *testing.T) {
	flow := `steps:
  a: {use: text.upper, in: {text: hi}}
  b: {use: retry, in: {times: 1, do: {use: text.join, in: {parts: [=a.text]}}}}
outputs: {text: =a.text}`
	p := project(t, map[string]string{"x": flow})
	run, err := p.Start(t.Context(), "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, p.dir, map[string]string{"x.flow": strings.Replace(flow, "use: text.join", "use: absent", 1)})
	p = reload(t, p)
	current, _, report := p.prepare(Definition{Name: "x"}, nil)
	if len(report.Problems) == 0 || !current.plan.deps["b"]["a"] {
		t.Fatalf("a reported captured reference must remain a dependency: %+v", report)
	}
	if _, err := p.From(run, "a"); err == nil || !strings.Contains(err.Error(), `unknown node "absent"`) {
		t.Fatalf("broken downstream box was omitted: %v", err)
	}
}

func TestRunFromGenericWorkflowSnapshots(t *testing.T) {
	for _, test := range []struct{ use, output string }{
		{"{use: identity, in: {value: 1}}", "value"},
		{"{use: map, in: {over: [1], do: identity}}", "result"},
		{"{use: inline}", "value"},
	} {
		p := project(t, map[string]string{
			"identity": "inputs: {value: T}\nsteps: {}\noutputs: {value: =inputs.value}",
			"inline":   "steps: {one: {use: {inputs: {x: T}, steps: {}, outputs: {value: =inputs.x}}, in: {x: 1}}}\noutputs: {value: =one.value}",
			"x":        "steps: {a: " + test.use + ", b: {use: data.value, in: {value: =a." + test.output + "}}}\noutputs: {value: =b.value}",
		})
		run, err := p.Start(t.Context(), "x", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.From(run, "b"); err != nil {
			t.Fatalf("unchanged generic workflow (%s): %v", test.use, err)
		}
	}
}

func TestRunFromInlineBoxDefaultChange(t *testing.T) {
	flow := `steps:
  a: {use: map, in: {over: [], do: {use: {inputs: {item: text, x: {type: text, default: hi}}, steps: {}, outputs: {text: =inputs.x}}}}}
  b: {use: data.value, in: {value: =a.result}}
outputs: {value: =b.value}`
	p := project(t, map[string]string{"x": flow})
	run, err := p.Start(t.Context(), "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, p.dir, map[string]string{"x.flow": strings.Replace(flow, "default: hi", "default: bye", 1)})
	if _, err := reload(t, p).From(run, "b"); err == nil || !strings.Contains(err.Error(), "filled-in inputs changed") {
		t.Fatalf("the filled box's input declaration changed: %v", err)
	}
}

func TestRunFromNestedProgramDefaultBoundary(t *testing.T) {
	p := project(t, map[string]string{
		"inner":  "steps: {up: {use: text.upper}}\noutputs: {text: =up.text}",
		"middle": "steps: {one: {use: inner}}\noutputs: {text: =one.text}",
		"outer":  "steps: {a: {use: middle}, b: {use: text.upper, in: {text: =a.text}}}\noutputs: {text: =b.text}",
	})
	node := p.nodes["text.upper"]
	node.Inputs["text"] = Port{Type: textType, Default: "hi"}
	p.nodes["text.upper"] = node
	run, err := p.Start(t.Context(), "outer", nil)
	if err != nil {
		t.Fatal(err)
	}
	node.Inputs["text"] = Port{Type: textType, Default: "bye"}
	p.nodes["text.upper"] = node
	if _, err := p.From(run, "b"); err != nil {
		t.Fatalf("a nested program's default is outside the snapshot comparison: %v", err)
	}
}
