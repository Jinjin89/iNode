package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"go.yaml.in/yaml/v3"
	"inode"
)

const usage = "usage: inode run <node> key=value ... | nodes | show <node> | check <workflow or JSON> | save <workflow> <version> <JSON> | runs [show|stop|follow|again <id>] | trigger add <kind> key=value ... run=<node> key=value ... | trigger list/on/off/remove/clear [name] | add <path or git URL> | remove <plugin> | plugins | start | stop | help | version"

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
	if len(args) == 1 {
		switch args[0] {
		case "help", "--help", "-h":
			_, err := fmt.Fprintln(out, usage)
			return err
		case "version", "--version":
			_, err := fmt.Fprintln(out, "inode dev")
			return err
		case "serve":
			return inode.Serve(ctx, ".")
		}
	}
	operation, rest := args[0], args[1:]
	if len(rest) > 0 {
		if _, exists := inode.OperationArgs(operation + " " + rest[0]); exists {
			operation, rest = operation+" "+rest[0], rest[1:]
		}
	}
	n, exists := inode.OperationArgs(operation)
	if operation == "start" && len(rest) == 0 {
		exists = true
	}
	commands := map[string]func(context.Context, *inode.Client, []string, io.Writer, io.Writer) error{"run": run, "trigger add": addTrigger}
	if !exists || commands[operation] != nil && len(rest) < 1 || commands[operation] == nil && len(rest) != n {
		return fmt.Errorf("%s", usage)
	}
	client, started, err := inode.Connect(ctx, ".", operation != "stop")
	if err != nil {
		if operation == "stop" {
			_, err := fmt.Fprintln(out, "no backend is running")
			return err
		}
		return err
	}
	if started {
		fmt.Fprintln(diagnostic, "started backend for this project; switched-on triggers are active; use inode stop to stop it")
	}
	if operation == "start" {
		url := "http://" + client.Address + "/#" + client.Credential
		_, err := fmt.Fprintln(out, url)
		if err != nil {
			return err
		}
		var args []string
		switch runtime.GOOS {
		case "darwin":
			args = []string{"open", url}
		default:
			args = []string{"xdg-open", url}
		}
		cmd := exec.Command(args[0], args[1:]...)
		if err := cmd.Start(); err == nil {
			go cmd.Wait()
		}
		return err
	}
	if command := commands[operation]; command != nil {
		return command(ctx, client, rest, out, diagnostic)
	}
	arguments := make([]any, len(rest))
	for i, value := range rest {
		arguments[i] = value
		if strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") {
			if err := json.Unmarshal([]byte(value), &arguments[i]); err != nil {
				return err
			}
		}
	}
	err = client.Call(ctx, operation, arguments, func(data json.RawMessage) error {
		columns := map[string][]func(inode.Values) any{
			"nodes":        {field("name"), field("doc")},
			"runs":         {field("id"), field("node"), field("state"), field("trigger")},
			"plugins":      {field("name"), field("version"), field("state"), field("source")},
			"trigger list": {field("name"), triggerInstruction, field("state"), runField("current"), field("waiting"), runField("last"), runField("last_failure"), field("error")},
		}[operation]
		if len(columns) > 0 {
			var rows []inode.Values
			if err := json.Unmarshal(data, &rows); err != nil {
				return err
			}
			for _, row := range rows {
				values := make([]string, len(columns))
				for i, column := range columns {
					if value := column(row); value != nil {
						values[i] = fmt.Sprint(value)
					}
				}
				if _, err := fmt.Fprintln(out, strings.Join(values, "\t")); err != nil {
					return err
				}
			}
			return nil
		}
		if operation == "check" {
			var report inode.CheckReport
			if err := json.Unmarshal(data, &report); err != nil {
				return err
			}
			if len(report.Problems) > 0 {
				return inode.Problems(report.Problems)
			}
		}
		if operation == "add" {
			var message string
			if err := json.Unmarshal(data, &message); err != nil {
				return err
			}
			_, err := fmt.Fprint(out, message)
			return err
		}
		encoder := json.NewEncoder(out)
		if operation != "runs follow" {
			encoder.SetIndent("", "  ")
		}
		return encoder.Encode(data)
	})
	if err == nil && operation == "stop" {
		_, err = fmt.Fprintln(out, "backend stopped")
	}
	return err
}

func field(name string) func(inode.Values) any {
	return func(row inode.Values) any { return row[name] }
}

func runField(name string) func(inode.Values) any {
	return func(row inode.Values) any {
		run, _ := row[name].(map[string]any)
		if run == nil {
			return nil
		}
		at := run["finished"]
		if at == nil {
			at = run["started"]
		}
		return fmt.Sprintf("%s %s %s", run["state"], at, run["id"])
	}
}

func triggerInstruction(row inode.Values) any {
	flow, _ := row["workflow"].(map[string]any)
	if flow == nil {
		return nil
	}
	steps, _ := flow["steps"].(map[string]any)
	event, _ := steps["event"].(map[string]any)
	run, _ := steps["run"].(map[string]any)
	if event == nil || run == nil {
		return nil
	}
	settings, _ := event["in"].(map[string]any)
	parts := []string{fmt.Sprint(event["use"])}
	for _, name := range slices.Sorted(maps.Keys(settings)) {
		value, _ := json.Marshal(settings[name])
		parts = append(parts, name+"="+string(value))
	}
	node, ok := run["use"].(string)
	if !ok {
		node = "Inline workflow"
	}
	return strings.Join(append(parts, "->", node), " ")
}

func readInputs(ctx context.Context, client *inode.Client, name string, args []string) (inode.Values, error) {
	var node inode.Values
	if err := client.Call(ctx, "show", []any{name}, func(data json.RawMessage) error { return json.Unmarshal(data, &node) }); err != nil {
		return nil, err
	}
	ports, _ := node["inputs"].(map[string]any)
	inputs := inode.Values{}
	for _, arg := range args {
		key, raw, found := strings.Cut(arg, "=")
		if !found {
			return nil, fmt.Errorf("input %q: use key=value", arg)
		}
		if _, duplicate := inputs[key]; duplicate {
			return nil, fmt.Errorf("input %q is repeated; supply it once", key)
		}
		var value any = raw
		port, _ := ports[key].(map[string]any)
		if port != nil && port["type"] != "text" {
			if err := yaml.Unmarshal([]byte(raw), &value); err != nil {
				return nil, fmt.Errorf("input %q: supply a YAML value: %w", key, err)
			}
		}
		inputs[key] = value
	}
	return inputs, nil
}

func addTrigger(ctx context.Context, client *inode.Client, args []string, out, _ io.Writer) error {
	i := slices.IndexFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "run=") })
	if i < 1 {
		return fmt.Errorf("use trigger add <kind> key=value ... run=<node> key=value ...")
	}
	settings, err := readInputs(ctx, client, args[0], args[1:i])
	if err != nil {
		return err
	}
	name := strings.TrimPrefix(args[i], "run=")
	inputs, err := readInputs(ctx, client, name, args[i+1:])
	if err != nil {
		return err
	}
	flow := inode.Workflow{Steps: map[string]inode.Step{
		"event": {Use: inode.Definition{Name: args[0]}, In: settings},
		"run":   {Use: inode.Definition{Name: name}, In: inputs, After: []string{"event"}},
	}}
	return client.Call(ctx, "trigger add", []any{flow}, func(data json.RawMessage) error { return json.NewEncoder(out).Encode(data) })
}

func run(ctx context.Context, client *inode.Client, args []string, out, diagnostic io.Writer) error {
	inputs, err := readInputs(ctx, client, args[0], args[1:])
	if err != nil {
		return err
	}
	var id string
	// Keep the acknowledgement so an interrupt can stop the accepted run.
	if err := client.Call(context.WithoutCancel(ctx), "run", []any{args[0], inputs, ""}, func(data json.RawMessage) error { return json.Unmarshal(data, &id) }); err != nil {
		return err
	}
	var record inode.Record
	receive := func(data json.RawMessage) error { return json.Unmarshal(data, &record) }
	err = client.Call(ctx, "runs follow", []any{id}, receive)
	if ctx.Err() != nil {
		if err := client.Call(context.Background(), "runs stop", []any{id}, func(json.RawMessage) error { return nil }); err != nil {
			return err
		}
		err = client.Call(context.Background(), "runs follow", []any{id}, receive)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(diagnostic, "run %s: %s\n", record.ID, record.State)
	if record.State != "done" {
		return errors.New(record.Error)
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(record.Outputs)
}
