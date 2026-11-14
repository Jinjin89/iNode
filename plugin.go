package inode

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Plugin struct {
	Name     string   `json:"name" yaml:"name"`
	Version  string   `json:"version" yaml:"version"`
	Source   string   `json:"source,omitempty" yaml:"source,omitempty"`
	Doc      string   `json:"doc,omitempty" yaml:"doc,omitempty"`
	Accesses []string `json:"accesses,omitempty" yaml:"accesses,omitempty"`
	Setup    any      `json:"setup,omitempty" yaml:"setup,omitempty"`
	State    string   `json:"state,omitempty" yaml:"-"`
}

func (plugin Plugin) validate() error {
	if !portName(plugin.Name) || !filepath.IsLocal(plugin.Name) || filepath.Base(plugin.Name) != plugin.Name {
		return fmt.Errorf("rename plugin %q to a nonempty name without dots, equals signs or path separators", plugin.Name)
	}
	if plugin.Version == "" {
		return fmt.Errorf("plugin %q: supply a version", plugin.Name)
	}
	if plugin.Setup != nil {
		args, err := argumentList(plugin.Setup, "setup")
		if err == nil {
			_, err = arguments(args, nil, "setup", true)
		}
		return err
	}
	return nil
}

func (p *Project) pluginDir(name string) string {
	return filepath.Join(p.dir, ".inode", "plugins", name)
}

func (p *Project) loadPlugins() error {
	p.plugins = []Plugin{}
	err := readYAML(filepath.Join(p.dir, "plugins"), &p.plugins)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, plugin := range p.plugins {
		if err := plugin.validate(); err != nil {
			return err
		}
		if plugin.Source == "" || seen[plugin.Name] {
			return fmt.Errorf("plugins: list %q once, with its source", plugin.Name)
		}
		seen[plugin.Name] = true
		var installed Plugin
		err := readYAML(filepath.Join(p.pluginDir(plugin.Name), "plugin"), &installed)
		p.plugins[i].State = "missing"
		if err == nil && installed.Name == plugin.Name && installed.Version == plugin.Version {
			p.plugins[i].State = "installed"
			p.plugins[i].Doc, p.plugins[i].Accesses = installed.Doc, installed.Accesses
			if err := p.loadNodes(filepath.Join(p.pluginDir(plugin.Name), "nodes"), plugin.Name+"."); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Project) lookup(name string) Node {
	if node, exists := p.nodes[name]; exists {
		return node
	}
	for _, plugin := range p.plugins {
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
		_, err = process(ctx, p.dir, nil, nil, "git", "clone", "--", url, stage)
		if err == nil && ref != "" {
			var commit []byte
			commit, err = process(ctx, stage, nil, nil, "git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
			if err == nil {
				_, err = process(ctx, stage, nil, nil, "git", "checkout", "--detach", strings.TrimSpace(string(commit)))
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

func (p *Project) InspectPlugin(ctx context.Context, source string) (Plugin, error) {
	plugin, stage, err := p.fetchPlugin(ctx, source)
	if err == nil {
		err = os.RemoveAll(stage)
	}
	return plugin, err
}

func (p *Project) Add(ctx context.Context, source string, out io.Writer) error {
	plugin, stage, err := p.fetchPlugin(ctx, source)
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
		if _, err = process(ctx, destination, nil, nil, args...); err != nil {
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
