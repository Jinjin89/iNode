package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"go.yaml.in/yaml/v3"
	"inode"
)

const usage = "usage: inode run <node> key=value ... | nodes | show <node> | check <workflow> | runs [show <id>] | add <path or git URL> | remove <plugin> | plugins"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := command(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func command(ctx context.Context, args []string, out, diagnostic io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	project, err := inode.Open(".")
	if err != nil {
		return err
	}
	var result any
	switch {
	case args[0] == "add" && len(args) == 2:
		return project.Add(ctx, args[1], out)
	case args[0] == "remove" && len(args) == 2:
		return project.Remove(args[1])
	case args[0] == "plugins" && len(args) == 1:
		for _, plugin := range project.Plugins() {
			state := "missing"
			if plugin.Installed {
				state = "installed"
			}
			if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", plugin.Name, plugin.Version, state, plugin.Source); err != nil {
				return err
			}
		}
		return nil
	case args[0] == "nodes" && len(args) == 1:
		for _, node := range project.Nodes() {
			if _, err := fmt.Fprintf(out, "%s\t%s\n", node.Name, node.Doc); err != nil {
				return err
			}
		}
		return nil
	case args[0] == "show" && len(args) == 2:
		result, err = project.Describe(args[1])
	case args[0] == "check" && len(args) == 2:
		report := project.Check(args[1])
		if len(report.Problems) > 0 {
			return inode.Problems(report.Problems)
		}
		result = report
	case args[0] == "runs" && len(args) == 1:
		runs, err := project.Runs()
		if err != nil {
			return err
		}
		for _, run := range runs {
			if _, err := fmt.Fprintf(out, "%s\t%s\t%s\n", run.ID, run.Node, run.State); err != nil {
				return err
			}
		}
		return nil
	case args[0] == "runs" && len(args) == 3 && args[1] == "show":
		result, err = project.ShowRun(args[2])
	case args[0] == "run" && len(args) >= 2:
		var node inode.Node
		node, err = project.Describe(args[1])
		if err != nil {
			return err
		}
		inputs := inode.Values{}
		for _, arg := range args[2:] {
			key, raw, found := strings.Cut(arg, "=")
			if !found {
				return fmt.Errorf("input %q: use key=value", arg)
			}
			if _, duplicate := inputs[key]; duplicate {
				return fmt.Errorf("input %q is repeated; supply it once", key)
			}
			var value any = raw
			if input, exists := node.Inputs[key]; exists && input.Type.Name != "text" {
				if err := yaml.Unmarshal([]byte(raw), &value); err != nil {
					return fmt.Errorf("input %q: supply a YAML value: %w", key, err)
				}
			}
			inputs[key] = value
		}
		var run *inode.Record
		run, err = project.Start(ctx, args[1], inputs)
		if run != nil {
			fmt.Fprintf(diagnostic, "run %s: %s\n", run.ID, run.State)
			result = run.Outputs
		}
	default:
		return fmt.Errorf("%s", usage)
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
