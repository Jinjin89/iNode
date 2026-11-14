package inode

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestOptionalInputMatrix(t *testing.T) {
	for _, declaration := range []struct {
		name string
		port Values
	}{
		{"required", Values{"type": "number"}},
		{"optional", Values{"type": "number", "optional": true}},
		{"default", Values{"type": "number", "default": 7}},
	} {
		for _, supplied := range []string{"missing", "zero", "wrong"} {
			t.Run(declaration.name+"/"+supplied, func(t *testing.T) {
				p := describedProgram(t, Values{"inputs": Values{"x": declaration.port}, "outputs": Values{"seen": "object"}, "run": []any{"python3", "probe.py"}},
					"import json,sys\njson.dump({'seen':json.load(sys.stdin)},sys.stdout)")
				inputs := Values{}
				if supplied == "zero" {
					inputs["x"] = float64(0)
				} else if supplied == "wrong" {
					inputs["x"] = false
				}
				flow := &Workflow{Steps: map[string]Step{"probe": {Use: Definition{Name: "probe"}, In: inputs}}}
				report := p.Check(Definition{Workflow: flow})
				bad := supplied == "wrong" || supplied == "missing" && declaration.name == "required"
				if (len(report.Problems) > 0) != bad {
					t.Fatalf("checker: %+v", report)
				}
				run, err := p.Start(t.Context(), "probe", inputs)
				if bad {
					if err == nil || run != nil {
						t.Fatalf("invalid input ran: %+v, %v", run, err)
					}
					return
				}
				want := inputs
				if supplied == "missing" && declaration.name == "default" {
					want = Values{"x": float64(7)}
				}
				if err != nil || !reflect.DeepEqual(run.Inputs, want) || !reflect.DeepEqual(run.Outputs["seen"], want) {
					t.Fatalf("input omission/default: %+v, %v; want %v", run, err, want)
				}
			})
		}
	}
}

func TestOptionalDeclarations(t *testing.T) {
	for _, value := range []string{"null", "false", "0", `""`} {
		var ports Ports
		err := decode([]byte(`{"x":{"type":"any","optional":true,"default":`+value+`}}`), &ports)
		if err == nil || !strings.Contains(err.Error(), "choose optional or a default") {
			t.Fatalf("optional/default %s: %v", value, err)
		}
	}
	p := fileProject(t, map[string]string{
		"bad.flow":         "inputs: {x: {type: text, optional: true}}\noutputs: {x: =inputs.x}",
		"nodes/out.node":   "doc: Total outputs\noutputs: {x: {type: text, optional: true}}\nrun: cat",
		"nodes/both.node":  "doc: Invalid input\ninputs: {x: {type: text, optional: true, default: hi}}\nrun: cat",
		"nodes/probe.node": "doc: Optional input\ninputs: {x: {type: bool, optional: true}}\nrun: cat",
		"nodes/safe.node":  "doc: Optional box\nno_outside_effect: true\ninputs: {do: {type: node() gives text, optional: true}}\noutputs: {text: text}\nrun: cat",
	})
	for name, message := range map[string]string{"bad": "workflow inputs", "out": "outputs always", "both": "choose optional or a default"} {
		if _, err := p.Describe(name); err == nil || !strings.Contains(err.Error(), message) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	inline := Definition{Workflow: &Workflow{Inputs: Ports{"x": {Type: textType, Optional: true}}}}
	if report := p.Check(inline); !strings.Contains(Problems(report.Problems).Error(), "workflow inputs") {
		t.Fatalf("inline optional input accepted: %+v", report)
	}
	node, err := p.Describe("probe")
	if err != nil || !node.Inputs["x"].Optional {
		t.Fatalf("description: %+v, %v", node, err)
	}
	for _, marshal := range []func(any) ([]byte, error){json.Marshal, yaml.Marshal} {
		data, err := marshal(node.Inputs)
		var ports Ports
		if err != nil || decodeYAML(data, &ports) != nil || !ports["x"].Optional || ports["x"].Default != nil {
			t.Fatalf("port round trip: %s, %v, %+v", data, err, ports)
		}
	}
	if safe, err := p.Describe("safe"); err != nil || !safe.NoOutsideEffect {
		t.Fatalf("an unset optional box has no unknown effect: %+v, %v", safe, err)
	}
}

func TestOptionalProgramBindings(t *testing.T) {
	script := `import json,os,sys
from pathlib import Path
path=Path('in/data.txt')
json.dump({'seen':{'args':sys.argv[1:],'stdin':sys.stdin.read(),'env':os.environ.get('INODE_OPTIONAL_TEST','absent'),'file':path.read_text() if path.exists() else 'absent'}},sys.stdout)`
	p := describedProgram(t, Values{
		"inputs":  Values{"x": Values{"type": "text", "optional": true}, "source": Values{"type": "file", "optional": true}},
		"outputs": Values{"seen": "object"},
		"run": Values{"args": []any{"python3", "-c", script, "=inputs.x", Values{"flag": "--name", "value": "=inputs.x"}},
			"stdin": "=inputs.x", "env": Values{"INODE_OPTIONAL_TEST": "=inputs.x"}, "files": Values{"in/data.txt": "=inputs.source"}},
	}, "")
	if err := os.WriteFile(filepath.Join(p.dir, "source.txt"), []byte("file bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, filled := range []bool{false, true} {
		inputs := Values{}
		want := Values{"args": []any{}, "stdin": "", "env": "absent", "file": "absent"}
		if filled {
			inputs = Values{"x": "", "source": "source.txt"}
			want = Values{"args": []any{"", "--name", ""}, "stdin": "", "env": "", "file": "file bytes"}
		}
		run, err := p.Start(t.Context(), "probe", inputs)
		if err != nil || !reflect.DeepEqual(run.Outputs["seen"], want) {
			t.Fatalf("filled=%t: %+v, %v; want %v", filled, run, err, want)
		}
	}
	// Whole bindings are omitted, even when an absent input is nested in a written value.
	value := Values{"list": []any{"=inputs.x", "==inputs.literal"}}
	if _, err := programValue(value, Values{}, "run.stdin"); !errors.Is(err, unsetInput) {
		t.Fatalf("missing nested binding: %v", err)
	}
	got, err := programValue(value, Values{"x": "=inputs.literal"}, "run.stdin")
	if err != nil || !reflect.DeepEqual(got, Values{"list": []any{"=inputs.literal", "=inputs.literal"}}) {
		t.Fatalf("a reference result was evaluated again: %v, %v", got, err)
	}
	if _, err := programValue("=inputs.x.missing", Values{"x": Values{}}, "run.stdin"); err == nil || errors.Is(err, unsetInput) {
		t.Fatalf("invalid nested field must fail: %v", err)
	}
	for _, binding := range []Values{
		{"args": []any{"cat", "=inputs.typo"}}, {"args": []any{"cat"}, "stdin": "=inputs.typo"},
		{"args": []any{"cat"}, "env": Values{"X": "=inputs.typo"}}, {"args": []any{"cat"}, "files": Values{"x": "=inputs.typo"}},
	} {
		if _, err := loadProgram(binding, Ports{}, Ports{}); err == nil {
			t.Fatalf("undeclared binding accepted: %v", binding)
		}
	}
}

func TestOptionalInputsInBoxes(t *testing.T) {
	p := fileProject(t, map[string]string{
		"nodes/probe.node": `doc: One open input
inputs: {text: text, file: {type: file, optional: true}, enabled: {type: bool, optional: true}}
outputs: {seen: object}
run: [python3, probe.py]`,
		"nodes/probe.py": "import json,sys\njson.dump({'seen':json.load(sys.stdin)},sys.stdout)",
		"mapped.flow": `steps: {m: {use: map, in: {over: [a, b], do: probe}}}
outputs: {seen: =m.result}`,
		"closed.flow": `steps: {c: {use: choose, in: {if: true, then: {use: probe, in: {text: yes, enabled: false}}, else: {use: probe, in: {text: no}}}}}
outputs: {seen: =c.result}`,
		"reference.flow": `steps: {source: {use: data.value, in: {value: false}}, p: {use: probe, in: {text: hi, enabled: =source.value}}}
outputs: {seen: =p.seen}`,
		"bad-reference.flow": `steps: {p: {use: probe, in: {text: hi, enabled: =absent.value}}}`,
	})
	for name, want := range map[string]any{
		"mapped":    []any{Values{"text": "a"}, Values{"text": "b"}},
		"closed":    Values{"text": "yes", "enabled": false},
		"reference": Values{"text": "hi", "enabled": false},
	} {
		if report := p.Check(Definition{Name: name}); len(report.Problems) != 0 {
			t.Fatalf("%s optional port became open: %+v", name, report)
		}
		run, err := p.Start(t.Context(), name, nil)
		if err != nil || !reflect.DeepEqual(run.Outputs["seen"], want) {
			t.Fatalf("%s: %+v, %v; want %v", name, run, err, want)
		}
	}
	if report := p.Check(Definition{Name: "bad-reference"}); len(report.Problems) == 0 {
		t.Fatal("an optional input hid an invalid reference")
	}
}

func TestOptionalProcessPluginInputs(t *testing.T) {
	p, client := processFixture(t)
	script := strings.Replace(fakePlugin, "'value':{'type':'text','default':''}", "'value':{'type':'text','optional':True}", 1)
	script = strings.Replace(script, "elif mode=='launch':", "elif mode=='inputs': reply(m,outputs={'text':json.dumps(m['inputs'])})\n   elif mode=='launch':", 1)
	writeFiles(t, p.dir, map[string]string{"fake/fake.py": script})
	request(t, client, "add", "fake")
	for _, inputs := range []Values{{"mode": "inputs"}, {"mode": "inputs", "value": ""}} {
		run := finishProcessRun(t, client, startProcessRun(t, client, "fake.probe", inputs))
		var got Values
		err := json.Unmarshal([]byte(fmt.Sprint(run.Outputs["text"])), &got)
		if run.State != "done" || err != nil || !reflect.DeepEqual(got, inputs) || !reflect.DeepEqual(run.Inputs, inputs) {
			t.Fatalf("process input omission: %+v, %v; got %v", run, err, got)
		}
	}
}
