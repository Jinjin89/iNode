package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"inode"
)

func TestCommands(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("upper.flow", []byte("inputs: {text: text}\nsteps: {upper: {use: text.upper, in: {text: =inputs.text}}}\noutputs: {text: =upper.text}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args    []string
		content string
		fails   bool
	}{
		{[]string{"nodes"}, "upper\t", false},
		{[]string{"show", "upper"}, `"no_outside_effect": true`, false},
		{[]string{"check", "upper.flow"}, `"problems": []`, false},
		{[]string{"run", "upper", "text=hi"}, `"text": "HI"`, false},
		{[]string{"run", "text.upper", "text=123"}, `"text": "123"`, false},
		{[]string{"run", "map", "over=[hi, yo]", "do=text.upper"}, `"YO"`, false},
		{[]string{"run", "retry", "times=1", "do={use: text.upper, in: {text: hi}}"}, `"result": "HI"`, false},
		{[]string{"run", "number.add", "a=2", "b=3"}, `"value": 5`, false},
		{[]string{"run", "text.join", "parts=[a, b]", "separator=,"}, `"text": "a,b"`, false},
		{[]string{"run", "data.value", "value==a.b"}, `"value": "=a.b"`, false},
		{[]string{"runs"}, "\tupper\tdone", false},
		{[]string{"run", "upper"}, "required port", true},
		{[]string{"run", "upper", "text=hi", "other=1"}, "unknown port", true},
		{[]string{"run", "upper", "text=hi", "text=bye"}, "repeated", true},
		{[]string{"run", "upper", "text"}, "use key=value", true},
		{[]string{"run", "number.add", "a=hi", "b=3"}, "needs number", true},
		{[]string{"run", "number.divide", "a=1", "b=0"}, "non-zero divisor", true},
		{[]string{"run", "text.join", "parts=["}, "YAML value", true},
		{[]string{"show", "missing"}, "unknown node", true},
		{[]string{"check", "missing"}, "unknown node", true},
		{[]string{"nodes", "extra"}, "usage:", true},
		{nil, "usage:", true},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			err := command(context.Background(), test.args, &out, &diagnostic)
			if test.fails {
				if err == nil || !strings.Contains(err.Error(), test.content) {
					t.Fatalf("wanted %q; got %v", test.content, err)
				}
			} else if err != nil || !strings.Contains(out.String(), test.content) || test.args[0] != "nodes" && test.args[0] != "runs" && !json.Valid(out.Bytes()) {
				t.Fatalf("got %s, %v; wanted %q", &out, err, test.content)
			}
		})
	}
	p, err := inode.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	runs, err := p.Runs()
	if err != nil || len(runs) == 0 {
		t.Fatalf("missing records: %v, %v", runs, err)
	}
	var out, diagnostic bytes.Buffer
	if err := command(context.Background(), []string{"runs", "show", runs[0].ID}, &out, &diagnostic); err != nil || !strings.Contains(out.String(), runs[0].ID) {
		t.Fatalf("show run: %s, %v", &out, err)
	}
}
