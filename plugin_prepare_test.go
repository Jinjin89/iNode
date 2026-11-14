package inode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode"
)

func commandPlugins(t *testing.T) {
	t.Helper()
	previous := Adapters
	Adapters = []Adapter{{"spec", "Service description", func(dir string, input Values) (Values, []Editable, error) {
		source := input["source"].(string)
		if !strings.HasSuffix(source, ".yaml") {
			if input["kind"] != nil {
				return nil, nil, fmt.Errorf("supply a .yaml description")
			}
			return nil, nil, nil
		}
		address, _ := input["address"].(string)
		return Values{"name": source, "doc": "Calls from " + source, "process": []string{"caller", source, address}, "env": Values{"TOKEN": ""}}, []Editable{{Name: "address", Label: "Address", Kind: "text", Value: address}}, nil
	}}, {"command", "Tool server command", func(dir string, input Values) (Values, []Editable, error) {
		source := input["source"].(string)
		if input["kind"] == nil && !strings.ContainsFunc(source, unicode.IsSpace) {
			return nil, nil, nil
		}
		args, err := SplitCommand(source)
		if err != nil {
			return nil, nil, err
		}
		return Values{"name": args[0], "doc": "Tools from " + args[0], "process": append([]string{"bridge", "--"}, args...)}, nil, nil
	}}}
	t.Cleanup(func() { Adapters = previous })
}

func TestAdapterSources(t *testing.T) {
	commandPlugins(t)
	dir := t.TempDir()
	for _, source := range []string{"api.yaml", "my api.yaml"} {
		draft, err := prepareSource(dir, source)
		if err != nil || draft.Input["kind"] != "spec" || len(draft.Editable) != 3 || draft.Editable[1].Name != "address" || draft.Manifest["env"] != nil || !reflect.DeepEqual(draft.Editable[2].Value, Values{"TOKEN": ""}) {
			t.Fatalf("the first adapter certain of a source drafts it, with its own field and suggested bindings: %+v, %v", draft, err)
		}
	}
	edited, err := prepareSource(dir, Values{"source": "api.yaml", "kind": "spec", "address": "=here", "env": Values{"TOKEN": "stored"}})
	if err != nil || edited.Manifest["name"] != "api-yaml" || !reflect.DeepEqual(edited.Manifest["process"], []string{"caller", "api.yaml", "==here"}) || !reflect.DeepEqual(edited.Manifest["env"], Values{"TOKEN": "stored"}) {
		t.Fatalf("edited adapter field: %+v, %v", edited, err)
	}
	unsure, err := prepareSource(dir, "word")
	if err != nil || unsure.Manifest != nil || !reflect.DeepEqual(unsure.Editable[0].Choices, map[string]string{"plugin": "Plugin folder or git URL", "spec": "Service description", "command": "Tool server command"}) {
		t.Fatalf("a source no adapter is certain of must ask: %+v, %v", unsure, err)
	}
	if plugin, err := prepareSource(dir, Values{"source": "api.yaml", "kind": "plugin"}); err != nil || len(plugin.Editable) != 0 {
		t.Fatalf("a chosen plugin is not offered to adapters: %+v, %v", plugin, err)
	}
	for _, source := range []string{"git@host:api.yaml", "ssh://host/api.yaml", "file:///api.yaml", "https://host/api.yaml#v1.git"} {
		if address, err := prepareSource(dir, source); err != nil || len(address.Editable) != 0 {
			t.Fatalf("a source that reads as a git address is not offered to adapters: %+v, %v", address, err)
		}
	}
	if _, err := prepareSource(dir, Values{"source": "word", "kind": "spec"}); err == nil || !strings.Contains(err.Error(), "supply a .yaml") {
		t.Fatalf("the chosen adapter must explain: %v", err)
	}
	if _, err := prepareSource(dir, Values{"source": "word", "kind": "other"}); err == nil || !strings.Contains(err.Error(), "listed source types") {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestCommandPreparation(t *testing.T) {
	commandPlugins(t)
	dir := t.TempDir()
	home := filepath.Join(dir, "a home")
	t.Setenv("HOME", home)
	for _, test := range []struct {
		line    string
		want    []string
		problem string
	}{
		{`python3 "a server.py" '' 'a;b' ~/docs`, []string{"python3", "a server.py", "", "a;b", filepath.Join(home, "docs")}, ""},
		{`tool a\ b "a'b"`, []string{"tool", "a b", "a'b"}, ""},
		{`tool x | other`, nil, "shell operators"},
		{`tool "$(touch marker)"`, nil, "substitution"},
		{`tool 'unfinished`, nil, "quoted word"},
	} {
		args, err := SplitCommand(test.line)
		if test.problem != "" {
			if err == nil || !strings.Contains(err.Error(), test.problem) {
				t.Fatalf("%s: %v", test.line, err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(args, test.want) {
			t.Fatalf("%s: %q, %v", test.line, args, err)
		}
	}
	draft, err := prepareSource(dir, `my.runner -y @tools/server-filesystem ~/docs`)
	if err != nil || draft.Manifest["name"] != "my-runner" || draft.Directory != filepath.Join(dir, "my-runner") {
		t.Fatalf("draft: %+v, %v", draft, err)
	}
	if _, err := os.Stat(draft.Directory); !os.IsNotExist(err) {
		t.Fatal("inspection materialized a plugin")
	}
	args := draft.Manifest["process"].([]string)
	if args[0] != "bridge" || args[1] != "--" || args[len(args)-1] != filepath.Join(home, "docs") {
		t.Fatalf("argv: %q", args)
	}
	bare, err := prepareSource(dir, "filesystem")
	if err != nil || len(bare.Editable[0].Choices) != 3 || bare.Manifest != nil {
		t.Fatalf("bare source must ask: %+v, %v", bare, err)
	}
	for _, source := range []string{"https://example.invalid/tools", "ssh://host/tools.git#main", "git@host:tools", "file:///tmp/repo"} {
		prepared, err := prepareSource(dir, source)
		if err != nil || len(prepared.Editable) != 0 || prepared.Input["source"] != source {
			t.Fatalf("git source: %+v, %v", prepared, err)
		}
	}
	for _, source := range []string{"tool https://example.invalid", "tool\thttps://example.invalid", "tool repository.git"} {
		prepared, err := prepareSource(dir, source)
		if err != nil || prepared.Manifest == nil {
			t.Fatalf("command arguments must not classify the whole source: %+v, %v", prepared, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "a folder"), 0755); err != nil {
		t.Fatal(err)
	}
	overlap, err := prepareSource(dir, "a folder")
	if err != nil || overlap.Manifest != nil || overlap.Input["source"] != filepath.Join(dir, "a folder") {
		t.Fatalf("existing folder must stay a plugin: %+v, %v", overlap, err)
	}
	explicit, err := prepareSource(dir, Values{"source": "a folder", "kind": "plugin"})
	if err != nil || explicit.Manifest != nil || explicit.Input["source"] != filepath.Join(dir, "a folder") {
		t.Fatalf("explicit folder: %+v, %v", explicit, err)
	}
	choice, err := prepareSource(dir, Values{"source": "server", "kind": "command", "name": "edited", "env": Values{"API_KEY": "stored"}})
	if err != nil || choice.Manifest["name"] != "edited" {
		t.Fatalf("edited: %+v, %v", choice, err)
	}
	folder, err := choice.Write()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(folder, "plugin"))
	if err != nil || !json.Valid(data) || !strings.Contains(string(data), "stored") {
		t.Fatalf("manifest: %s, %v", data, err)
	}
	if _, err := choice.Write(); err == nil {
		t.Fatal("overwrote an existing source folder")
	}
}

func TestSharedQuotedCommands(t *testing.T) {
	home := filepath.Join(t.TempDir(), "a home")
	t.Setenv("HOME", home)
	line := `python3 'probe.py' '' 'two words' ~/docs 'a;b'`
	want := []any{"python3", "probe.py", "", "two words", filepath.Join(home, "docs"), "a;b"}
	for _, key := range []string{"setup", "process"} {
		plugin := Plugin{Name: "quoted", Version: "1"}
		if key == "setup" {
			plugin.Setup = line
		} else {
			plugin.Process = line
		}
		if err := plugin.validate(); err != nil {
			t.Fatal(err)
		}
		value := plugin.Process
		if key == "setup" {
			value = plugin.Setup
		}
		args, err := argumentList(value, key)
		if err != nil || !reflect.DeepEqual(args, want) {
			t.Fatalf("%s: %q, %v", key, args, err)
		}
	}
	p := describedProgram(t, Values{"run": line, "outputs": Values{"args": "list of text"}}, "import json,sys\njson.dump({'args':sys.argv[1:]},sys.stdout)\n")
	run, err := p.Start(t.Context(), "probe", nil)
	if err != nil || !reflect.DeepEqual(run.Outputs["args"], want[2:]) {
		t.Fatalf("quoted node execution: %+v, %v", run, err)
	}
}
