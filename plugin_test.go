package inode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func pluginFolder(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "a plugin's $folder")
	if err := os.CopyFS(dir, os.DirFS("examples/tiny_plugin")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{"plugin": `name: tiny
version: "1"
doc: Add a short greeting
accesses: [Reads and writes its own greeting file.]
setup: printf x >> setup_count; printf hi > nodes/greeting.txt
`})
	return dir
}

func TestInspectPlugin(t *testing.T) {
	source := pluginFolder(t)
	dir := t.TempDir()
	client := runningBackend(t, dir)
	var plugin Plugin
	if err := json.Unmarshal(request(t, client, "plugins inspect", source)[0], &plugin); err != nil {
		t.Fatal(err)
	}
	if plugin.Name != "tiny" || plugin.Version != "1" || plugin.Doc != "Add a short greeting" || len(plugin.Accesses) != 1 {
		t.Fatalf("inspection: %+v", plugin)
	}
	if _, err := os.Stat(filepath.Join(dir, "plugins")); !os.IsNotExist(err) {
		t.Fatal("inspection changed the listing")
	}
	if _, err := os.Stat(filepath.Join(source, "setup_count")); !os.IsNotExist(err) {
		t.Fatal("inspection ran setup")
	}
	p, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, source, map[string]string{"plugin": "name: tiny\ndoc: hi"})
	if _, err := p.InspectPlugin(context.Background(), source); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("invalid inspection: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, ".inode", "plugins")); err != nil || len(entries) != 0 {
		t.Fatalf("inspection left a staged or installed plugin: %v, %v", entries, err)
	}
}

func TestPluginFolder(t *testing.T) {
	source := pluginFolder(t)
	p := fileProject(t, map[string]string{
		"greet.flow": "steps: {greet: {use: tiny.greet, in: {name: cat}}}\noutputs: {text: =greet.text}",
	})
	var out bytes.Buffer
	for range 2 {
		if err := p.Add(context.Background(), source, &out); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(out.String(), "tiny\t1") || !strings.Contains(out.String(), "Reads and writes its own greeting file.") {
		t.Fatalf("missing disclosure: %s", &out)
	}
	installed := p.pluginDir("tiny")
	count, err := os.ReadFile(filepath.Join(installed, "setup_count"))
	if err != nil || string(count) != "x" {
		t.Fatalf("setup must run once, in the installed folder: %q, %v", count, err)
	}
	if _, err := os.Stat(filepath.Join(source, "setup_count")); !os.IsNotExist(err) {
		t.Fatal("setup ran in the source folder")
	}
	plugins := p.Plugins()
	if len(plugins) != 1 || plugins[0].Name != "tiny" || plugins[0].Version != "1" || plugins[0].State != "installed" || filepath.IsAbs(plugins[0].Source) || filepath.Join(p.dir, plugins[0].Source) != source {
		t.Fatalf("plugin list: %+v", plugins)
	}
	var recorded []Plugin
	if err := readYAML(filepath.Join(p.dir, "plugins"), &recorded); err != nil || len(recorded) != 1 || recorded[0].Doc != "" || recorded[0].Setup != "" || len(recorded[0].Accesses) != 0 {
		t.Fatalf("shared file must contain only name, version and source: %+v, %v", recorded, err)
	}
	for _, test := range []struct {
		name   string
		inputs Values
		want   Values
	}{
		{"tiny.greet", Values{"name": "cat"}, Values{"text": "hi cat"}},
		{"greet", nil, Values{"text": "hi cat"}},
		{"map", Values{"over": []any{"a", "b"}, "do": "tiny.greet"}, Values{"result": []any{"hi a", "hi b"}}},
	} {
		run, err := p.Start(context.Background(), test.name, test.inputs)
		if err != nil || !reflect.DeepEqual(run.Outputs, test.want) {
			t.Fatalf("%s: %+v, %v; want %v", test.name, run, err, test.want)
		}
	}
	if err := p.Remove("tiny"); err != nil {
		t.Fatal(err)
	}
	if len(p.Plugins()) != 0 {
		t.Fatal("remove kept the project record")
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("remove kept the installed folder: %v", err)
	}
	if report := p.Check(Definition{Name: "greet"}); !strings.Contains(Problems(report.Problems).Error(), "unknown node") {
		t.Fatalf("remove kept the node in the catalog: %+v", report)
	}
	if err := p.Remove("tiny"); err == nil {
		t.Fatal("removing an unknown plugin succeeded")
	}
}

func TestMissingPlugin(t *testing.T) {
	p := fileProject(t, map[string]string{"greet.flow": "steps: {greet: {use: tiny.greet, in: {name: cat}}}"})
	source := "../a plugin's $(touch marker)"
	if err := p.savePlugins([]Plugin{{Name: "tiny", Version: "1", Source: source}}); err != nil {
		t.Fatal(err)
	}
	p, err := Open(p.dir)
	if err != nil {
		t.Fatal(err)
	}
	if plugins := p.Plugins(); len(plugins) != 1 || plugins[0].State == "installed" {
		t.Fatalf("missing plugin not listed: %+v", plugins)
	}
	report := p.Check(Definition{Name: "greet"})
	if len(report.Problems) != 1 || report.Problems[0].Step != "greet/greet" || report.Problems[0].Input != "use" || !strings.Contains(report.Problems[0].Message, "listed but not installed") || !strings.Contains(report.Problems[0].Message, "inode add "+source) {
		t.Fatalf("missing-plugin diagnosis: %+v", report)
	}
	if run, err := p.Start(context.Background(), "greet", nil); err == nil || run != nil {
		t.Fatalf("missing plugin started: %+v, %v", run, err)
	}
	if _, err := os.Stat(filepath.Join(p.dir, ".inode")); !os.IsNotExist(err) {
		t.Fatal("checking a copied project installed something")
	}
	if _, err := p.Start(context.Background(), "text.upper", Values{"text": "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove("tiny"); err != nil {
		t.Fatal(err)
	}
}

func TestPluginGit(t *testing.T) {
	repo := pluginFolder(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=iNode test", "-c", "user.email=test@example.invalid"}, args...)...)
		cmd.Dir = repo
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s, %v", args, data, err)
		}
	}
	git("init")
	git("add", ".")
	git("commit", "-m", "one")
	git("tag", "one")
	writeFiles(t, repo, map[string]string{"plugin": "name: tiny\nversion: '2'\ndoc: New version"})
	git("add", ".")
	git("commit", "-m", "two")
	writeFiles(t, repo, map[string]string{"untracked": "not installed"})
	source := (&url.URL{Scheme: "file", Path: repo, Fragment: "one"}).String()
	p := project(t, nil)
	if err := p.Add(context.Background(), source, io.Discard); err != nil {
		t.Fatal(err)
	}
	plugins := p.Plugins()
	if len(plugins) != 1 || plugins[0].Version != "1" || plugins[0].Source != source || plugins[0].State != "installed" {
		t.Fatalf("git ref or source was lost: %+v", plugins)
	}
	if _, err := os.Stat(filepath.Join(p.pluginDir("tiny"), "untracked")); !os.IsNotExist(err) {
		t.Fatal("git installation copied the working tree")
	}
	run, err := p.Start(context.Background(), "tiny.greet", Values{"name": "cat"})
	if err != nil || run.Outputs["text"] != "hi cat" {
		t.Fatalf("cloned node: %+v, %v", run, err)
	}
}

func TestFailedSetupAndReinstall(t *testing.T) {
	source := pluginFolder(t)
	manifest, err := os.ReadFile(filepath.Join(source, "plugin"))
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, source, map[string]string{"plugin": strings.Replace(string(manifest), "printf x >> setup_count; printf hi > nodes/greeting.txt", "printf broken >&2; exit 1", 1)})
	p := project(t, nil)
	var out bytes.Buffer
	if err := p.Add(context.Background(), source, &out); err == nil || !strings.Contains(err.Error(), "broken") || !strings.Contains(out.String(), "Accesses:") {
		t.Fatalf("failed setup: %s, %v", &out, err)
	}
	if _, err := os.Stat(p.pluginDir("tiny")); !os.IsNotExist(err) {
		t.Fatal("failed setup left an installation")
	}
	if _, err := os.Stat(filepath.Join(p.dir, "plugins")); !os.IsNotExist(err) {
		t.Fatal("failed setup recorded the plugin")
	}
	writeFiles(t, source, map[string]string{"plugin": string(manifest)})
	if err := p.Add(context.Background(), source, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(p.pluginDir("tiny")); err != nil {
		t.Fatal(err)
	}
	p, err = Open(p.dir)
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, source, map[string]string{"plugin": strings.Replace(string(manifest), `version: "1"`, `version: "2"`, 1)})
	if err := p.Add(context.Background(), source, io.Discard); err != nil || p.Plugins()[0].State != "installed" || p.Plugins()[0].Version != "2" {
		t.Fatalf("explicit add did not replace the listed version: %+v, %v", p.Plugins(), err)
	}
	writeFiles(t, source, map[string]string{"plugin": string(manifest)})
	if err := p.Add(context.Background(), source, io.Discard); err != nil || p.Plugins()[0].State != "installed" {
		t.Fatalf("explicit reinstall: %v", err)
	}
}

func TestPluginNames(t *testing.T) {
	for _, name := range []string{"../elsewhere", "/elsewhere", "", "a.b"} {
		source := t.TempDir()
		writeFiles(t, source, map[string]string{"plugin": "name: '" + name + "'\nversion: '1'\ndoc: Bad"})
		p := project(t, nil)
		if err := p.Add(context.Background(), source, io.Discard); err == nil || !strings.Contains(err.Error(), "rename plugin") {
			t.Fatalf("invalid plugin name %q: %v", name, err)
		}
	}
}

func TestProgramExamples(t *testing.T) {
	dir := t.TempDir()
	for _, folder := range []string{"nodes", "tiny_plugin"} {
		if err := os.CopyFS(filepath.Join(dir, folder), os.DirFS(filepath.Join("examples", folder))); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"count_text.flow", "plugin_greeting.flow", "plugins"} {
		data, err := os.ReadFile(filepath.Join("examples", name))
		if err != nil {
			t.Fatal(err)
		}
		writeFiles(t, dir, map[string]string{name: string(data)})
	}
	p, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Add(context.Background(), "tiny_plugin", io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		want Values
	}{
		{"count_text", Values{"count": float64(2)}}, {"plugin_greeting", Values{"text": "hi cat"}},
	} {
		run, err := p.Start(context.Background(), test.name, nil)
		if err != nil || !reflect.DeepEqual(run.Outputs, test.want) {
			t.Fatalf("%s: %+v, %v", test.name, run, err)
		}
	}
}
