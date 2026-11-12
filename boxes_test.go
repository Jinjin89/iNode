package inode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestControlExamples(t *testing.T) {
	flows := map[string]string{}
	for _, name := range []string{"map_words", "choose_text", "count_to_two", "retry_text", "guard_division"} {
		data, err := os.ReadFile(filepath.Join("examples", name+".flow"))
		if err != nil {
			t.Fatal(err)
		}
		flows[name] = string(data)
	}
	p := project(t, flows)
	for _, test := range []struct {
		name string
		want Values
	}{
		{"map_words", Values{"words": []any{"HI", "YO"}}},
		{"choose_text", Values{"text": "YES"}},
		{"count_to_two", Values{"count": float64(2)}},
		{"retry_text", Values{"text": "HI"}},
		{"guard_division", Values{"value": float64(0)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if report := p.Check(Definition{Name: test.name}); len(report.Problems) != 0 {
				t.Fatal(report)
			}
			run, err := p.Start(context.Background(), test.name, nil)
			if err != nil || !reflect.DeepEqual(run.Outputs, test.want) {
				t.Fatalf("got %v, %v", run, err)
			}
		})
	}
	node, err := p.Describe("map_words")
	if err != nil || node.Outputs["words"].Type.String() != "list of text" || !node.NoOutsideEffect {
		t.Fatalf("map inference: %+v, %v", node, err)
	}
}

func TestBoxSignatures(t *testing.T) {
	for _, test := range []struct{ name, flow, message string }{
		{"open input type", `steps: {a: {use: map, in: {over: [1], do: text.upper}}}`, "needs text"},
		{"open input count", `steps: {a: {use: map, in: {over: [1], do: number.add}}}`, "at most one open input"},
		{"multiple outputs", `steps: {a: {use: retry, in: {times: 1, do: {use: http.get, in: {url: http://unused}}}}}`, "exactly one output"},
		{"choose results", `steps: {a: {use: choose, in: {if: true, then: {use: text.upper, in: {text: hi}}, else: {use: number.add, in: {a: 1, b: 2}}}}}`, "needs number"},
		{"choose open branch", `steps: {a: {use: choose, in: {if: true, then: text.upper, else: text.upper}}}`, "open input count"},
		{"repeat result", `steps: {a: {use: repeat, in: {start: 0, max: 1, do: {use: number.add, in: {b: 1}}}}}`, "needs object"},
		{"repeat state", `steps: {a: {use: repeat, in: {start: 0, max: 1, do: text.upper}}}`, "needs object"},
		{"retry open input", `steps: {a: {use: retry, in: {times: 1, do: text.upper}}}`, "open input count"},
		{"try handler input", `steps: {a: {use: try, in: {do: {use: text.upper, in: {text: hi}}, on_error: text.upper}}}`, "open input"},
		{"try results", `steps: {a: {use: try, in: {do: {use: text.upper, in: {text: hi}}, on_error: {use: {inputs: {e: object}, steps: {}, outputs: {n: 1}}}}}}`, "needs text"},
		{"literal mixed list", `steps: {a: {use: map, in: {over: [hi, 1], do: text.upper}}}`, "needs text"},
		{"unknown capture", `steps: {a: {use: retry, in: {times: 1, do: {use: text.upper, in: {text: =missing.text}}}}}`, `no field "missing"`},
		{"unknown filled port", `steps: {a: {use: retry, in: {times: 1, do: {use: text.upper, in: {text: hi, other: 1}}}}}`, "unknown port"},
		{"no outer scope", `steps: {outside: {use: text.upper, in: {text: hi}}, a: {use: retry, in: {times: 1, do: {use: {steps: {}, outputs: {v: =outside.text}}}}}}`, `no field "outside"`},
		{"saved box cycle", `steps: {a: {use: map, in: {over: [hi], do: x}}}`, "calls itself"},
		{"capture cycle", `steps: {a: {use: retry, in: {times: 1, do: {use: text.upper, in: {text: =b.text}}}}, b: {use: text.upper, in: {text: =a.result}}}`, "cycle"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := project(t, map[string]string{"x": test.flow})
			report := p.Check(Definition{Name: "x"})
			if !strings.Contains(Problems(report.Problems).Error(), test.message) {
				t.Fatalf("wanted %q, got %+v", test.message, report)
			}
			if run, err := p.Start(context.Background(), "x", nil); err == nil || run != nil {
				t.Fatalf("invalid box started: %v, %v", run, err)
			}
		})
	}
	p := project(t, map[string]string{"a": `steps: {b: {use: retry, in: {times: 1, do: b}}}`, "b": `steps: {a: {use: retry, in: {times: 1, do: a}}}`})
	if report := p.Check(Definition{Name: "a"}); !strings.Contains(Problems(report.Problems).Error(), "calls itself") {
		t.Fatal(report)
	}
}

func TestRetryEffectThroughBoxes(t *testing.T) {
	p := project(t, map[string]string{
		"write": `steps: {write: {use: file.write, in: {path: marker, text: hi}}}
outputs: {path: =write.path}`,
		"mapped": `steps: {each: {use: map, in: {over: [hi], do: {use: file.write, in: {path: marker}}}}}
outputs: {paths: =each.result}`,
		"x": `steps: {again: {use: retry, in: {times: 2, do: mapped}}}`,
	})
	for _, name := range []string{"write", "mapped"} {
		node, err := p.Describe(name)
		if err != nil || node.NoOutsideEffect {
			t.Fatalf("effect lost in %s: %+v, %v", name, node, err)
		}
	}
	report := p.Check(Definition{Name: "x"})
	message := Problems(report.Problems).Error()
	if !strings.Contains(message, "requires a node with no outside effect") || !strings.Contains(message, "file.write") || !strings.Contains(message, "/each/do") {
		t.Fatal(message)
	}
	if run, err := p.Start(context.Background(), "x", nil); err == nil || run != nil {
		t.Fatalf("unsafe retry started: %v, %v", run, err)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "marker")); !os.IsNotExist(err) {
		t.Fatal("preflight wrote a file")
	}
}

func TestRetryAttempts(t *testing.T) {
	for _, times := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprint(times), func(t *testing.T) {
			p := project(t, map[string]string{
				"x": fmt.Sprintf(`steps: {again: {use: retry, in: {times: %d, do: attempt}}}
outputs: {text: =again.result}`, times),
				"attempt": `steps: {read: {use: flaky}, upper: {use: text.upper, in: {text: =read.text}}}
outputs: {text: =upper.text}`,
			})
			attempts := 0
			p.nodes["flaky"] = Node{Name: "flaky", NoOutsideEffect: true, Outputs: Ports{"text": {Type: textType}}, run: func(context.Context, Values, Call) (Values, error) {
				attempts++
				if attempts < 3 {
					return nil, fmt.Errorf("not yet")
				}
				return Values{"text": "hi"}, nil
			}}
			run, err := p.Start(context.Background(), "x", nil)
			wantCalls := min(times, 3)
			if attempts != wantCalls || len(run.Steps["again"].Steps) != wantCalls || (err == nil) != (times >= 3) {
				t.Fatalf("attempts %d, record %+v, error %v", attempts, run, err)
			}
			for i := 1; i <= wantCalls; i++ {
				child := run.Steps["again"].Steps[fmt.Sprint(i)]
				wantState := "failed"
				if i == 3 {
					wantState = "done"
				}
				if child.State != wantState {
					t.Fatalf("attempt %d: %+v", i, child)
				}
			}
		})
	}
}

func TestMapRecordsAndCapture(t *testing.T) {
	p := project(t, map[string]string{"x": `
steps:
  prefix: {use: text.upper, in: {text: "==missing.text"}}
  each:
    use: map
    in:
      over: [hi, yo]
      do:
        use:
          inputs: {prefix: text, word: text}
          steps:
            join: {use: text.join, in: {parts: [=inputs.prefix, =inputs.word]}}
          outputs: {text: =join.text}
        in: {prefix: =prefix.text}
outputs: {words: =each.result}
`})
	run, err := p.Start(context.Background(), "x", nil)
	if err != nil || !reflect.DeepEqual(run.Outputs["words"], []any{"=MISSING.TEXT hi", "=MISSING.TEXT yo"}) {
		t.Fatalf("captures: %v, %v", run, err)
	}
	saved, err := p.ShowRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	calls := saved.Steps["each"].Steps
	if len(calls) != 2 {
		t.Fatalf("map calls: %+v", calls)
	}
	for i, word := range []string{"hi", "yo"} {
		call := calls[fmt.Sprint(i+1)]
		if call.State != "done" || call.Inputs["prefix"] != "=MISSING.TEXT" || call.Inputs["word"] != word || call.Outputs["text"] != "=MISSING.TEXT "+word || call.Steps["join"].Outputs["text"] != "=MISSING.TEXT "+word || call.Workflow == nil {
			t.Fatalf("call tree missing data: %+v", call)
		}
		if i > 0 && call.Started.Before(calls[fmt.Sprint(i)].Finished) {
			t.Fatal("map calls overlapped")
		}
	}
	runs, err := p.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("child runs stored separately: %v, %v", runs, err)
	}
}

func TestChooseOnlyCallsSelectedBranch(t *testing.T) {
	for _, flag := range []bool{true, false} {
		p := project(t, map[string]string{"x": fmt.Sprintf(`steps: {pick: {use: choose, in: {if: %t, then: {use: text.upper, in: {text: yes}}, else: {use: text.upper, in: {text: no}}}}}
outputs: {text: =pick.result}`, flag)})
		run, err := p.Start(context.Background(), "x", nil)
		want := "NO"
		if flag {
			want = "YES"
		}
		if err != nil || run.Outputs["text"] != want || len(run.Steps["pick"].Steps) != 1 {
			t.Fatalf("choose: %v, %v", run, err)
		}
	}
}

func TestTryReceivesFailureAndKeepsItInRecords(t *testing.T) {
	p := project(t, map[string]string{"x": `
inputs: {divisor: number}
steps:
  guard:
    use: try
    in:
      do:
        use:
          steps:
            bad: {use: number.divide, in: {a: 1, b: =inputs.divisor}}
            blocked: {use: text.upper, in: {text: hi}, after: [bad]}
          inputs: {divisor: number}
          outputs: {value: {step: ok, message: ok}}
        in: {divisor: =inputs.divisor}
      on_error:
        use:
          inputs: {error: object}
          steps: {}
          outputs: {value: =inputs.error}
outputs: {reply: =guard.result}
`})
	for _, divisor := range []int{0, 1} {
		run, err := p.Start(context.Background(), "x", Values{"divisor": divisor})
		if err != nil || run.State != "done" {
			t.Fatalf("try: %v, %v", run, err)
		}
		calls := run.Steps["guard"].Steps
		if divisor == 0 {
			reply := run.Outputs["reply"].(Values)
			if len(calls) != 2 || calls["1"].State != "failed" || calls["1"].Steps["blocked"].State != "waiting" || reply["step"] != "x/guard/1/bad" || !strings.Contains(reply["message"].(string), "non-zero divisor") {
				t.Fatalf("failure not handled or recorded: %+v", run)
			}
		} else if len(calls) != 1 || calls["1"].State != "done" {
			t.Fatalf("handler called after success: %+v", run)
		}
	}
}

func TestRepeatAndCountLimits(t *testing.T) {
	for _, test := range []struct {
		max      string
		done     bool
		calls    int
		succeeds bool
	}{
		{"2", true, 1, true}, {"2", false, 2, false}, {"0", true, 0, false}, {"-1", true, 0, false}, {"1.5", true, 0, false},
	} {
		p := project(t, map[string]string{"x": fmt.Sprintf(`steps: {loop: {use: repeat, in: {start: 0, max: %s, do: tick}}}
outputs: {count: =loop.result}`, test.max)})
		calls := 0
		p.nodes["tick"] = Node{Name: "tick", NoOutsideEffect: true, Inputs: Ports{"n": {Type: numberType}}, Outputs: Ports{"reply": {Type: typ("object{state: number, done: bool}")}}, run: func(_ context.Context, in Values, _ Call) (Values, error) {
			calls++
			return Values{"reply": Values{"state": in["n"].(float64) + 1, "done": test.done}}, nil
		}}
		run, err := p.Start(context.Background(), "x", nil)
		if (err == nil) != test.succeeds || calls != test.calls || len(run.Steps["loop"].Steps) != test.calls {
			t.Fatalf("max %s done %v: %+v, %v, calls %d", test.max, test.done, run, err, calls)
		}
		if test.succeeds && run.Outputs["count"] != float64(1) {
			t.Fatal(run.Outputs)
		}
	}
	for _, times := range []string{"0", "-1", "1.5"} {
		p := project(t, map[string]string{"x": `steps: {again: {use: retry, in: {times: ` + times + `, do: {use: text.upper, in: {text: hi}}}}}`})
		run, err := p.Start(context.Background(), "x", nil)
		if err == nil || !strings.Contains(err.Error(), "positive whole number") || len(run.Steps["again"].Steps) != 0 {
			t.Fatalf("retry times %s: %v, %v", times, run, err)
		}
	}
}

func TestUncheckedBoxEdges(t *testing.T) {
	for _, value := range []string{"false", "{state: 1, done: wrong}", "{state: wrong, done: true}"} {
		p := project(t, map[string]string{"x": `steps: {loop: {use: repeat, in: {start: 0, max: 1, do: {use: {inputs: {n: number}, steps: {v: {use: data.value, in: {value: ` + value + `}}}, outputs: {reply: =v.value}}}}}}`})
		report := p.Check(Definition{Name: "x"})
		if len(report.Problems) != 0 || len(report.Unchecked) == 0 {
			t.Fatal(report)
		}
		run, err := p.Start(context.Background(), "x", nil)
		if err == nil || run.Steps["loop"].Steps["1"].State != "failed" {
			t.Fatalf("unchecked box output reached repeat: %v, %v", run, err)
		}
	}
}

func TestLocalPlaceholderBindings(t *testing.T) {
	p := project(t, map[string]string{
		"identity": `inputs: {value: T}
steps: {}
outputs: {value: =inputs.value}`,
		"x": `steps:
  word: {use: identity, in: {value: hi}}
  number: {use: identity, in: {value: 2}}
  words: {use: map, in: {over: [hi], do: text.upper}}
  numbers: {use: map, in: {over: [2], do: {use: number.add, in: {b: 1}}}}
outputs: {word: =word.value, number: =number.value, words: =words.result, numbers: =numbers.result}`,
	})
	run, err := p.Start(context.Background(), "x", nil)
	want := Values{"word": "hi", "number": float64(2), "words": []any{"HI"}, "numbers": []any{float64(3)}}
	if err != nil || !reflect.DeepEqual(run.Outputs, want) {
		t.Fatalf("local bindings: %v, %v", run, err)
	}
	node, err := p.Describe("x")
	if err != nil {
		t.Fatal(err)
	}
	for name, typ := range map[string]string{"word": "text", "number": "number", "words": "list of text", "numbers": "list of number"} {
		if node.Outputs[name].Type.String() != typ {
			t.Fatal(node.Outputs)
		}
	}
}

func TestNodeInputsThroughWorkflows(t *testing.T) {
	p := project(t, map[string]string{
		"apply": `inputs: {f: node(text) gives text}
steps: {each: {use: map, in: {over: [hi], do: =inputs.f}}}
outputs: {words: =each.result}`,
		"x": `steps: {each: {use: apply, in: {f: text.upper}}}
outputs: {words: =each.words}`,
	})
	run, err := p.Start(context.Background(), "x", nil)
	if err != nil || !reflect.DeepEqual(run.Outputs["words"], []any{"HI"}) {
		t.Fatalf("node passed through workflow: %v, %v", run, err)
	}
}

func TestTypedObjectsAndReferenceLocation(t *testing.T) {
	p := project(t, map[string]string{"x": `inputs: {reply: "object{name: text, numbers: list of number}"}
outputs: {name: =inputs.reply.name}`})
	for _, test := range []struct {
		value Values
		valid bool
	}{
		{Values{"name": "hi", "numbers": []any{1, 2}}, true},
		{Values{"name": false, "numbers": []any{1}}, false},
		{Values{"name": "hi", "numbers": []any{"no"}}, false},
		{Values{"numbers": []any{1}}, false},
	} {
		run, err := p.Start(context.Background(), "x", Values{"reply": test.value})
		if (err == nil) != test.valid || !test.valid && run != nil {
			t.Fatalf("object edge: %v, %v", run, err)
		}
	}
	p = project(t, map[string]string{"bad": `steps: {a: {use: text.upper, in: {text: hi}}, b: {use: text.upper, in: {text: =a.missing}}}`})
	message := Problems(p.Check(Definition{Name: "bad"}).Problems).Error()
	if strings.Count(message, `input "text"`) != 1 {
		t.Fatal(message)
	}
}

func TestDefaultsAndInlineSteps(t *testing.T) {
	p := project(t, map[string]string{"x": `
inputs: {prefix: {type: text, default: "=literal.text"}}
steps:
  inline:
    use:
      inputs: {text: text}
      steps: {up: {use: text.upper, in: {text: =inputs.text}}}
      outputs: {text: =up.text}
    in: {text: =inputs.prefix}
  each: {use: map, in: {over: [[hi, yo]], do: text.join}}
outputs: {text: =inline.text, words: =each.result}
`})
	run, err := p.Start(context.Background(), "x", nil)
	want := Values{"text": "=LITERAL.TEXT", "words": []any{"hi yo"}}
	if err != nil || !reflect.DeepEqual(run.Outputs, want) {
		t.Fatalf("defaults or inline step: %v, %v", run, err)
	}
}

func TestRetryRequiresKnownEffect(t *testing.T) {
	p := project(t, map[string]string{"x": `inputs: {work: node() gives text}
steps: {again: {use: retry, in: {times: 1, do: =inputs.work}}}
outputs: {text: =again.result}`})
	if report := p.Check(Definition{Name: "x"}); !strings.Contains(Problems(report.Problems).Error(), "outside effect is unknown") {
		t.Fatal(report)
	}
	run, err := p.Start(context.Background(), "x", Values{"work": Values{"use": "text.upper", "in": Values{"text": "hi"}}})
	if err == nil || run != nil {
		t.Fatalf("workflow input effect was inferred: %v, %v", run, err)
	}
	for _, flag := range []string{"true", "false"} {
		p := project(t, map[string]string{"x": `inputs: {work: {type: node() gives text, no_outside_effect: ` + flag + `}}`})
		if report := p.Check(Definition{Name: "x"}); !strings.Contains(Problems(report.Problems).Error(), "unknown field") {
			t.Fatal(report)
		}
	}
}

func TestNestedLiteralTypes(t *testing.T) {
	p := project(t, map[string]string{"x": `steps: {each: {use: map, in: {over: [[hi, 1]], do: text.join}}}`})
	if report := p.Check(Definition{Name: "x"}); !strings.Contains(Problems(report.Problems).Error(), "needs text") {
		t.Fatal(report)
	}
}

func TestTypeSyntax(t *testing.T) {
	for _, source := range []string{"object{name: text, values: list of number}", "node(object{n: number}) gives object{state: number, done: bool}", "list of node() gives T", "node(error: object) gives T", "object{}"} {
		var parsed Type
		if err := parsed.UnmarshalText([]byte(source)); err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		var again Type
		if err := again.UnmarshalText([]byte(parsed.String())); err != nil || !reflect.DeepEqual(parsed, again) {
			t.Fatalf("type roundtrip %s: %+v, %v", source, again, err)
		}
	}
	for _, source := range []string{"object{x text}", "object{x: text, x: number}", "node(text)", "node(text gives number", "list of absent", "node() gives text extra", "node(text, number) gives text"} {
		var parsed Type
		if err := parsed.UnmarshalText([]byte(source)); err == nil {
			t.Fatalf("accepted invalid type %s", source)
		}
	}
}

func TestNodeValuesInListsAndObjects(t *testing.T) {
	p := project(t, map[string]string{
		"x": `steps:
  call: {use: wrapper, in: {options: {functions: [text.upper]}}}
outputs: {text: =call.text}`,
		"wrapper": `inputs: {options: "object{functions: list of node(text) gives text}"}
steps: {call: {use: invoke, in: {options: =inputs.options}}}
outputs: {text: =call.result}`,
		"opaque": `inputs: {options: object}
steps: {call: {use: invoke, in: {options: =inputs.options}}}
outputs: {text: =call.result}`,
	})
	p.nodes["invoke"] = Node{Name: "invoke", NoOutsideEffect: true,
		Inputs:  Ports{"options": {Type: typ("object{functions: list of node(text) gives text}")}},
		Outputs: Ports{"result": {Type: textType}},
		run: func(ctx context.Context, in Values, call Call) (Values, error) {
			functions := in["options"].(Values)["functions"].([]any)
			value, err := call(ctx, functions[0].(*Step), "hi")
			return Values{"result": value}, err
		},
	}
	run, err := p.Start(context.Background(), "x", nil)
	if err != nil || run.Outputs["text"] != "HI" {
		t.Fatalf("nested node value: %v, %v", run, err)
	}
	for _, name := range []string{"wrapper", "opaque"} {
		node, err := p.Describe(name)
		if err != nil || node.NoOutsideEffect {
			t.Fatalf("unknown nested node effect: %+v, %v", node, err)
		}
	}
}

func TestNodeValueOutputsAndEmptyMap(t *testing.T) {
	p := project(t, map[string]string{
		"pass": `inputs: {f: node(text) gives text}
steps: {}
outputs: {f: =inputs.f}`,
		"x": `steps:
  function: {use: pass, in: {f: text.upper}}
  each: {use: map, in: {over: [], do: =function.f}}
outputs: {words: =each.result}`,
	})
	run, err := p.Start(context.Background(), "x", nil)
	if err != nil || !reflect.DeepEqual(run.Outputs["words"], []any{}) || len(run.Steps["each"].Steps) != 0 {
		t.Fatalf("node output or empty map: %v, %v", run, err)
	}
}

func TestUnknownTypesDoNotBindPlaceholders(t *testing.T) {
	p := project(t, map[string]string{"x": `steps:
  source: {use: data.value, in: {value: hi}}
  both: {use: same, in: {a: =source.value, b: 2}}
outputs: {value: =both.value}`})
	p.nodes["same"] = Node{Name: "same", NoOutsideEffect: true,
		Inputs: Ports{"a": {Type: typ("T")}, "b": {Type: typ("T")}}, Outputs: Ports{"value": {Type: typ("T")}},
		run: func(_ context.Context, in Values, _ Call) (Values, error) { return Values{"value": in["a"]}, nil },
	}
	node, err := p.Describe("x")
	if err != nil || node.Outputs["value"].Type.Name != "number" {
		t.Fatalf("any bound T: %+v, %v", node, err)
	}
	run, err := p.Start(context.Background(), "x", nil)
	if err == nil || !run.Steps["both"].Started.IsZero() {
		t.Fatalf("unknown input reached consumer: %v, %v", run, err)
	}
}

func TestUnboundBoxTypesBecomeAny(t *testing.T) {
	p := project(t, map[string]string{
		"identity": `inputs: {value: T}
steps: {}
outputs: {value: =inputs.value}`,
		"x": `steps: {each: {use: map, in: {over: [hi], do: identity}}}
outputs: {words: =each.result}`,
	})
	node, err := p.Describe("x")
	if err != nil || node.Outputs["words"].Type.String() != "list of any" {
		t.Fatalf("unbound type escaped: %+v, %v", node, err)
	}
	run, err := p.Start(context.Background(), "x", nil)
	if err != nil || !reflect.DeepEqual(run.Outputs["words"], []any{"hi"}) {
		t.Fatalf("generic box: %v, %v", run, err)
	}
}
