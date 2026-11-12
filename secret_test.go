package inode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func secretRecord(t *testing.T, p *Project, run *Record, value string) {
	t.Helper()
	data, err := os.ReadFile(p.runFile(run.ID))
	if err != nil || strings.Contains(string(data), value) || !strings.Contains(string(data), "demo_token") {
		t.Fatalf("record must contain the name and no secret value: %s, %v", data, err)
	}
}

func TestSecretOperations(t *testing.T) {
	p := project(t, nil)
	client := runningBackend(t, p.dir)
	request(t, client, "secret set", "demo_token", "first")
	request(t, client, "secret set", "demo_token", "second\n")
	data, err := os.ReadFile(filepath.Join(p.dir, ".inode", "secrets", "demo_token.secret"))
	if err != nil || string(data) != "second\n" {
		t.Fatalf("replacement or exact stdin value lost: %q, %v", data, err)
	}
	info, err := os.Stat(filepath.Join(p.dir, ".inode", "secrets", "demo_token.secret"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("secret permissions: %v, %v", info, err)
	}
	if got := request(t, client, "secret list"); string(got[0]) != `["demo_token"]` {
		t.Fatalf("list must return names only: %s", got)
	}
	request(t, client, "secret remove", "demo_token")
	if got := request(t, client, "secret list"); string(got[0]) != `[]` {
		t.Fatalf("removed secret still listed: %s", got)
	}
	for _, name := range []string{"", "../outside", "/absolute", "a/b"} {
		if err := p.SetSecret(name, "hi"); err == nil {
			t.Fatalf("invalid secret name accepted: %q", name)
		}
	}
}

func TestSecretsThroughWorkflowAndBox(t *testing.T) {
	files := map[string]string{
		"boxed.flow": `inputs: {token: secret}
steps: {each: {use: map, in: {over: [hi, yo], do: {use: token_text, in: {token: =inputs.token}}}}}
outputs: {results: =each.result}`,
	}
	for _, name := range []string{"token_report.flow", "nodes/token_text.node", "nodes/token_text.py"} {
		data, err := os.ReadFile(filepath.Join("examples", name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(data)
	}
	p := fileProject(t, files)
	const value = "hidden-token"
	if err := p.SetSecret("demo_token", value); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"token_text", "token_report", "boxed"} {
		inputs := Values{"text": "hi"}
		if name != "token_report" {
			inputs["token"] = "demo_token"
		}
		if name == "boxed" {
			delete(inputs, "text")
		}
		run, err := p.Start(t.Context(), name, inputs)
		if err != nil {
			t.Fatal(err)
		}
		want := Values{"text": "hi", "length": float64(len(value))}
		if name == "boxed" {
			want = Values{"results": []any{want, Values{"text": "yo", "length": float64(len(value))}}}
		} else {
			want = Values{"result": want}
		}
		if !reflect.DeepEqual(run.Outputs, want) {
			t.Fatalf("%s outputs: %v", name, run.Outputs)
		}
		secretRecord(t, p, run, value)
		if name == "boxed" && run.Steps["each"].Steps["1"].Inputs["token"] != "demo_token" {
			t.Fatal("boxed call did not record the name")
		}
	}
	copied := fileProject(t, files)
	report := copied.Check(Definition{Name: "token_report"})
	if len(report.Problems) != 1 || report.Problems[0].Step != "token_report/report" || report.Problems[0].Input != "token" || !strings.Contains(report.Problems[0].Message, "store it with inode secret set demo_token") {
		t.Fatalf("copied project must ask for its secret: %+v", report.Problems)
	}
	if run, err := copied.Start(t.Context(), "boxed", Values{"token": "demo_token"}); err == nil || run != nil {
		t.Fatalf("missing run input secret accepted: %+v, %v", run, err)
	}
}

func TestNestedSecretsAndScrubbing(t *testing.T) {
	for _, fails := range []bool{false, true} {
		p := project(t, nil)
		p.nodes["use"] = Node{Name: "use", Inputs: Ports{"options": {Type: typ("object{tokens: list of secret, text: text}")}}, Outputs: Ports{"result": {Type: anyType}}, run: func(_ context.Context, in Values, _ Call) (Values, error) {
			options := in["options"].(Values)
			if !reflect.DeepEqual(options, Values{"tokens": []any{"hidden", "hidden-token", ""}, "text": "demo_token"}) {
				t.Errorf("substitution must follow the declared types: %v", options)
			}
			if fails {
				return nil, fmt.Errorf("cannot use hidden-token or hidden")
			}
			return Values{"result": Values{"hidden-token": []string{"prefix hidden-token / hidden suffix"}}}, nil
		}}
		for name, value := range map[string]string{"demo_token": "hidden-token", "short": "hidden", "empty": ""} {
			if err := p.SetSecret(name, value); err != nil {
				t.Fatal(err)
			}
		}
		run, err := p.Start(t.Context(), "use", Values{"options": Values{"tokens": []any{"short", "demo_token", "empty"}, "text": "demo_token"}})
		if (err != nil) != fails {
			t.Fatalf("call outcome: %+v, %v", run, err)
		}
		if fails {
			if !strings.Contains(run.Error, "[secret demo_token] or [secret short]") || strings.Contains(err.Error(), "hidden") {
				t.Fatalf("error not scrubbed: %v", err)
			}
		} else if want := (Values{"result": Values{"[secret demo_token]": []any{"prefix [secret demo_token] / [secret short] suffix"}}}); !reflect.DeepEqual(run.Outputs, want) {
			t.Fatalf("output strings and object keys not scrubbed: %v", run.Outputs)
		}
		secretRecord(t, p, run, "hidden")
	}
}

func TestSecretProgramErrorAndOutput(t *testing.T) {
	for _, fails := range []bool{false, true} {
		code := `json.dump({"text": "token=" + token}, sys.stdout)`
		if fails {
			code = `sys.stderr.write("bad token=" + token); sys.exit(1)`
		}
		p := fileProject(t, map[string]string{
			"nodes/use.node": "doc: Use token\ninputs: {token: secret}\noutputs: {text: text}\nrun: python3 use.py",
			"nodes/use.py":   "import json, sys\ntoken = json.load(sys.stdin)['token']\n" + code,
			"x.flow":         "steps: {use: {use: use, in: {token: demo_token}}}\noutputs: {text: =use.text}",
		})
		if err := p.SetSecret("demo_token", "hidden-token"); err != nil {
			t.Fatal(err)
		}
		run, err := p.Start(t.Context(), "x", nil)
		if (err != nil) != fails || !fails && run.Outputs["text"] != "token=[secret demo_token]" || fails && !strings.Contains(run.Error, "bad token=[secret demo_token]") {
			t.Fatalf("program call not scrubbed before recording/forwarding: %+v, %v", run, err)
		}
		secretRecord(t, p, run, "hidden-token")
	}
}

func TestSecretTypeCompatibilityAndForwarding(t *testing.T) {
	p := project(t, map[string]string{
		"text_to_secret": "inputs: {value: text}\nsteps: {use: {use: length, in: {token: =inputs.value}}}",
		"secret_to_text": "inputs: {value: secret}\nsteps: {use: {use: text.upper, in: {text: =inputs.value}}}",
		"names":          "inputs: {value: secret}\noutputs: {value: =inputs.value}",
		"forward": `inputs: {a: secret, b: secret}
steps:
  pick: {use: choose, in: {if: true, then: {use: names, in: {value: =inputs.a}}, else: {use: names, in: {value: =inputs.b}}}}
  length: {use: length, in: {token: =pick.result}}
outputs: {name: =pick.result, length: =length.length}`,
	})
	p.nodes["length"] = Node{Name: "length", Inputs: Ports{"token": {Type: typ("secret")}}, Outputs: Ports{"length": {Type: numberType}}, run: func(_ context.Context, in Values, _ Call) (Values, error) {
		return Values{"length": len(in["token"].(string))}, nil
	}}
	for _, name := range []string{"text_to_secret", "secret_to_text"} {
		if report := p.Check(Definition{Name: name}); len(report.Problems) == 0 || !strings.Contains(report.Problems[0].Message, "needs") {
			t.Fatalf("secret/text references must remain distinct: %+v", report)
		}
	}
	for _, name := range []string{"demo_token", "other"} {
		if err := p.SetSecret(name, "hidden-token"); err != nil {
			t.Fatal(err)
		}
	}
	node, err := p.Describe("forward")
	if err != nil || node.Outputs["name"].Type.Name != "secret" {
		t.Fatalf("secret output did not retain its type: %+v, %v", node.Outputs, err)
	}
	run, err := p.Start(t.Context(), "forward", Values{"a": "demo_token", "b": "other"})
	if err != nil || !reflect.DeepEqual(run.Outputs, Values{"name": "demo_token", "length": float64(12)}) {
		data, _ := json.Marshal(run)
		t.Fatalf("control node must forward the name: %s, %v", data, err)
	}
	secretRecord(t, p, run, "hidden-token")
}
