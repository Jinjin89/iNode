package inode

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCheckReportReferencesWithProblems(t *testing.T) {
	p := project(t, map[string]string{"x": `
steps:
  a: {use: text.upper, in: {text: hi}}
  n: {use: number.add, in: {a: 1, b: 2}}
  unknown: {use: absent, in: {x: =a.text}}
  missing: {use: text.join, in: {separator: =a.text}}
  wrong: {use: text.upper, in: {text: =n.value}}
  data: {use: data.value, in: {value: [=a.missing, =a.text, {"a.b": =a.text}, "==a.text"]}}
outputs: {text: =a.text}
`})
	report := p.Check(Definition{Name: "x"})
	if len(report.Problems) == 0 || len(report.References) != 7 {
		t.Fatalf("report: %+v", report)
	}
	refs := map[string]Reference{}
	for _, ref := range report.References {
		key, _ := json.Marshal(ref.Path)
		refs[string(key)] = ref
	}
	for _, test := range []struct {
		path string
		from []string
		bad  bool
	}{
		{`["steps","unknown","in","x"]`, []string{"a", "text"}, false},
		{`["steps","missing","in","separator"]`, []string{"a", "text"}, false},
		{`["steps","wrong","in","text"]`, []string{"n", "value"}, true},
		{`["steps","data","in","value","0"]`, []string{"a", "missing"}, true},
		{`["steps","data","in","value","1"]`, []string{"a", "text"}, false},
		{`["steps","data","in","value","2","a.b"]`, []string{"a", "text"}, false},
		{`["outputs","text"]`, []string{"a", "text"}, false},
	} {
		ref, exists := refs[test.path]
		if !exists || !reflect.DeepEqual(ref.From, test.from) || ref.Bad != test.bad {
			t.Fatalf("%s: %+v", test.path, ref)
		}
	}
}

func TestCheckReportBoxScopes(t *testing.T) {
	p := project(t, map[string]string{
		"holder": "inputs: {arg: 'object{fns: list of node() gives text, literal: any}'}\noutputs: {text: ok}",
		"x": `
steps:
  a: {use: text.upper, in: {text: hi}}
  b:
    use: holder
    in:
      arg:
        fns:
          - use:
              inputs: {cap: text}
              steps: {a: {use: text.upper, in: {text: =inputs.cap}}}
              outputs: {text: =a.text}
            in: {cap: =a.text}
        literal: {use: =a.text}
outputs: {text: =b.text}
`,
	})
	report := p.Check(Definition{Name: "x"})
	if len(report.Problems) != 0 || len(report.References) != 3 {
		t.Fatalf("report: %+v", report)
	}
	var paths []valuePath
	for _, ref := range report.References {
		paths = append(paths, ref.Path)
	}
	want := []valuePath{
		{"steps", "b", "in", "arg", "fns", "0", "in", "cap"},
		{"steps", "b", "in", "arg", "literal", "use"},
		{"outputs", "text"},
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths: %v", paths)
	}
}

func TestCheckReportKeepsBoxReferencesAfterErrors(t *testing.T) {
	p := project(t, map[string]string{"x": `
inputs: {data: object}
steps:
  a: {use: text.upper, in: {text: hi}}
  m: {use: map, in: {over: [hi], do: {use: absent, in: {text: =a.text}}}}
  t: {use: try, in: {do: {use: text.upper, after: [a], in: {text: =a.text}}, on_error: {use: text.upper, in: {text: =a.text}}}}
  u: {use: text.upper, in: {text: =inputs.data.word}}
outputs: {text: =a.text}
`})
	report := p.Check(Definition{Name: "x"})
	if len(report.Problems) == 0 || len(report.References) != 5 {
		t.Fatalf("report: %+v", report)
	}
	found := false
	for _, ref := range report.References {
		if ref.Step == "x/u" && ref.Input == "text" {
			found = ref.Unchecked
		}
	}
	if !found {
		t.Fatal("unknown object path was not marked unchecked")
	}
}

func TestCheckReportBoundPorts(t *testing.T) {
	p := project(t, map[string]string{"x": `steps: {m: {use: map, in: {over: [hi], do: text.upper}}, open: {use: map}}`})
	report := p.Check(Definition{Name: "x"})
	if got := report.Nodes[`["steps","m"]`].Outputs["result"].Type.String(); got != "list of text" {
		t.Fatal(got)
	}
	if got := report.Nodes[`["steps","open"]`].Outputs["result"].Type.String(); !strings.Contains(got, "U") {
		t.Fatal(got)
	}
}
