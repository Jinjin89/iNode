package inode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func commandPlugins(t *testing.T) {
	t.Helper()
	previous := CommandPlugin
	CommandPlugin = func(args []string) ([]string, string, string, error) {
		return append([]string{"bridge", "--"}, args...), "Tools from " + args[0], "Tool server command", nil
	}
	t.Cleanup(func() { CommandPlugin = previous })
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
		args, err := splitCommand(test.line)
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
	draft, err := prepareSource(dir, `runner -y @tools/server-filesystem ~/docs`)
	if err != nil || draft.Manifest["name"] != "filesystem" || draft.Directory != filepath.Join(dir, "filesystem") {
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
	if err != nil || len(bare.Editable[0].Choices) != 2 || bare.Manifest != nil {
		t.Fatalf("bare source must ask: %+v, %v", bare, err)
	}
	for _, source := range []string{"https://example.invalid/tools", "ssh://host/tools.git#main", "git@host:tools", "file:///tmp/repo"} {
		prepared, err := prepareSource(dir, source)
		if err != nil || len(prepared.Editable) != 0 || prepared.Input.Source != source {
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
	if err != nil || overlap.Manifest != nil || overlap.Input.Source != filepath.Join(dir, "a folder") {
		t.Fatalf("existing folder must stay a plugin: %+v, %v", overlap, err)
	}
	explicit, err := prepareSource(dir, Values{"source": "a folder", "kind": "plugin"})
	if err != nil || explicit.Manifest != nil || explicit.Input.Source != filepath.Join(dir, "a folder") {
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
