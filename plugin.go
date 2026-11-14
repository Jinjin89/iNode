package inode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Plugin struct {
	Name        string            `json:"name" yaml:"name"`
	Version     string            `json:"version" yaml:"version"`
	Source      string            `json:"source,omitempty" yaml:"source,omitempty"`
	Doc         string            `json:"doc,omitempty" yaml:"doc,omitempty"`
	Accesses    []string          `json:"accesses,omitempty" yaml:"accesses,omitempty"`
	Setup       any               `json:"setup,omitempty" yaml:"setup,omitempty"`
	Process     any               `json:"process,omitempty" yaml:"process,omitempty"`
	Env         map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	State       string            `json:"state,omitempty" yaml:"-"`
	Error       string            `json:"error,omitempty" yaml:"-"`
	Preparation *Prepared         `json:"preparation,omitempty" yaml:"-"`
}

func (plugin *Plugin) validate() error {
	if !portName(plugin.Name) || !filepath.IsLocal(plugin.Name) || filepath.Base(plugin.Name) != plugin.Name {
		return fmt.Errorf("rename plugin %q to a nonempty name without dots, equals signs or path separators", plugin.Name)
	}
	if plugin.Version == "" {
		return fmt.Errorf("plugin %q: supply a version", plugin.Name)
	}
	for _, key := range []string{"setup", "process"} {
		value := plugin.Setup
		if key == "process" {
			value = plugin.Process
		}
		if value == nil {
			continue
		}
		args, err := argumentList(value, key)
		if err == nil {
			_, err = arguments(args, nil, key, true)
		}
		if err != nil {
			return err
		}
		if key == "process" {
			argv, _ := arguments(args, nil, key, false)
			plugin.Process, _ = jsonData[any](argv)
		}
	}
	for name, secret := range plugin.Env {
		if plugin.Process == nil || name == "" || strings.ContainsAny(name, "=\x00") || secret == "" || !filepath.IsLocal(secret) || filepath.Base(secret) != secret {
			return fmt.Errorf("env.%s: bind an environment variable to a stored secret name for process", name)
		}
	}
	return nil
}

func (p *Project) pluginDir(name string) string {
	return filepath.Join(p.dir, ".inode", "plugins", name)
}

func readPlugins(dir string) ([]Plugin, error) {
	plugins := []Plugin{}
	if err := readYAML(filepath.Join(dir, "plugins"), &plugins); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	seen := map[string]bool{}
	for i, plugin := range plugins {
		if err := plugin.validate(); err != nil {
			return nil, err
		}
		if plugin.Source == "" || seen[plugin.Name] {
			return nil, fmt.Errorf("plugins: list %q once, with its source", plugin.Name)
		}
		seen[plugin.Name] = true
		var installed Plugin
		err := readYAML(filepath.Join(dir, ".inode", "plugins", plugin.Name, "plugin"), &installed)
		plugins[i].State = "missing"
		if err == nil && installed.Name == plugin.Name && installed.Version == plugin.Version {
			if err := installed.validate(); err != nil {
				plugins[i].State, plugins[i].Error = "unavailable", fmt.Sprintf("Plugin %q is unavailable (%v). Add the plugin again.", plugin.Name, err)
				continue
			}
			installed.Source, installed.State = plugin.Source, "installed"
			plugins[i] = installed
		}
	}
	return plugins, nil
}

func (b *backend) plugins() ([]Plugin, error) {
	plugins, err := readPlugins(b.dir)
	for i, plugin := range plugins {
		if plugin.State == "installed" && plugin.Process != nil {
			plugins[i].State, plugins[i].Error = b.host.state(plugin.Name)
		}
	}
	return plugins, err
}

func (p *Project) lookup(name string) Node {
	if node, exists := p.nodes[name]; exists {
		return node
	}
	for _, plugin := range p.plugins {
		if plugin.State == "unavailable" && strings.HasPrefix(name, plugin.Name+".") {
			return Node{Name: name, problem: fmt.Errorf("%s", plugin.Error)}
		}
		if plugin.State == "missing" && strings.HasPrefix(name, plugin.Name+".") {
			return Node{Name: name, problem: fmt.Errorf("plugin %q version %s is listed but not installed; run inode add %s",
				plugin.Name, plugin.Version, plugin.Source)}
		}
	}
	return Node{Name: name, problem: fmt.Errorf("unknown node %q; choose a name from inode nodes", name)}
}

func (p *Project) Plugins() []Plugin { return p.plugins }

func (p *Project) fetchPlugin(ctx context.Context, source string) (Plugin, string, error) {
	if err := os.MkdirAll(p.pluginDir(""), 0700); err != nil {
		return Plugin{}, "", err
	}
	stage, err := os.MkdirTemp(p.pluginDir(""), ".add-")
	if err != nil {
		return Plugin{}, "", err
	}
	local := source
	if !filepath.IsAbs(local) {
		local = filepath.Join(p.dir, local)
	}
	if info, statErr := os.Stat(local); statErr == nil && info.IsDir() {
		source, err = filepath.Rel(p.dir, local)
		if err == nil {
			err = os.CopyFS(stage, os.DirFS(local))
		}
	} else {
		url, ref, _ := strings.Cut(source, "#")
		_, err = process(ctx, p.dir, nil, nil, nil, "git", "clone", "--", url, stage)
		if err == nil && ref != "" {
			var commit []byte
			commit, err = process(ctx, stage, nil, nil, nil, "git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
			if err == nil {
				_, err = process(ctx, stage, nil, nil, nil, "git", "checkout", "--detach", strings.TrimSpace(string(commit)))
			}
		}
	}
	var plugin Plugin
	if err == nil {
		err = readYAML(filepath.Join(stage, "plugin"), &plugin)
	}
	if err == nil {
		err = plugin.validate()
		if err != nil {
			err = fmt.Errorf("%s: %w", filepath.Join(stage, "plugin"), err)
		}
	}
	if err == nil && plugin.Doc == "" {
		err = fmt.Errorf("plugin %q: supply doc", plugin.Name)
	}
	if err != nil {
		os.RemoveAll(stage)
	}
	plugin.Source = source
	return plugin, stage, err
}

func (p *Project) preparePlugin(ctx context.Context, source any, install bool) (Plugin, string, error) {
	prepared, err := prepareSource(p.dir, source)
	if err != nil {
		return Plugin{}, "", err
	}
	if len(prepared.Editable) == 0 {
		location, _ := prepared.Input["source"].(string)
		return p.fetchPlugin(ctx, location)
	}
	name, _ := prepared.Input["name"].(string)
	plugin := Plugin{Preparation: &prepared, Source: name}
	if prepared.Manifest != nil {
		data, _ := json.Marshal(prepared.Manifest)
		err = decode(data, &plugin)
		if err == nil {
			err = plugin.validate()
		}
	}
	if !install {
		if err != nil {
			plugin.Error = err.Error()
		}
		return plugin, "", nil
	}
	if err != nil {
		return plugin, "", err
	}
	folder, err := prepared.Write()
	if err != nil {
		return Plugin{}, "", err
	}
	return p.fetchPlugin(ctx, folder)
}

func (p *Project) InspectPlugin(ctx context.Context, source any) (Plugin, error) {
	plugin, stage, err := p.preparePlugin(ctx, source, false)
	if err == nil {
		err = os.RemoveAll(stage)
	}
	return plugin, err
}

func (p *Project) Add(ctx context.Context, source any, out io.Writer) error {
	plugin, stage, err := p.preparePlugin(ctx, source, true)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if _, err := fmt.Fprintf(out, "%s\t%s\n%s\nAccesses:\n", plugin.Name, plugin.Version, plugin.Doc); err != nil {
		return err
	}
	for _, access := range plugin.Accesses {
		if _, err := fmt.Fprintf(out, "- %s\n", access); err != nil {
			return err
		}
	}
	if plugin.Process != nil {
		if _, err := fmt.Fprintf(out, "Program: %s\n", CommandText(plugin.Process.([]any))); err != nil {
			return err
		}
		for _, key := range keys(plugin.Env) {
			if _, err := fmt.Fprintf(out, "%s from secret %s\n", key, plugin.Env[key]); err != nil {
				return err
			}
		}
	}
	if err := p.remove(plugin.Name); err != nil {
		return err
	}
	destination := p.pluginDir(plugin.Name)
	if err := os.Rename(stage, destination); err != nil {
		return err
	}
	if plugin.Setup != nil {
		raw, _ := argumentList(plugin.Setup, "setup")
		args, _ := arguments(raw, nil, "setup", false)
		if _, err = process(ctx, destination, nil, nil, nil, args...); err != nil {
			err = fmt.Errorf("plugin %q setup: %w", plugin.Name, err)
		}
	}
	if err == nil {
		err = p.savePlugins(append(p.plugins, plugin))
	}
	if err != nil {
		os.RemoveAll(destination)
		return err
	}
	if p.host != nil {
		p.host.mu.Lock()
		delete(p.host.processes, plugin.Name)
		p.host.mu.Unlock()
	}
	if plugin.Process != nil && p.host != nil {
		if _, err := fmt.Fprintln(out, "Starting program…"); err != nil {
			return err
		}
	}
	return p.load()
}

func (p *Project) Remove(name string) error {
	for _, plugin := range p.plugins {
		if plugin.Name == name {
			return p.remove(name)
		}
	}
	return fmt.Errorf("unknown plugin %q; choose a name from inode plugins", name)
}

func (p *Project) remove(name string) error {
	if p.host != nil {
		p.host.mu.Lock()
		if process := p.host.processes[name]; process != nil {
			process.close()
		}
		p.host.mu.Unlock()
	}
	if err := os.RemoveAll(p.pluginDir(name)); err != nil {
		return err
	}
	next := []Plugin{}
	for _, plugin := range p.plugins {
		if plugin.Name != name {
			next = append(next, plugin)
		}
	}
	if len(next) == len(p.plugins) {
		return nil
	}
	if err := p.savePlugins(next); err != nil {
		return err
	}
	return p.load()
}

func (p *Project) savePlugins(plugins []Plugin) error {
	records := make([]Plugin, len(plugins))
	for i, plugin := range plugins {
		records[i] = Plugin{Name: plugin.Name, Version: plugin.Version, Source: plugin.Source}
	}
	data, err := yaml.Marshal(records)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(p.dir, "plugins"), data, 0644, false)
}
