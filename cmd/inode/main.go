package main

import (
	"bufio"
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
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/term"
	"inode"
)

const usage = "usage: inode run <node> key=value ... | nodes | show <node> | check <workflow or JSON> | save <workflow> <version> <JSON> | runs [show|stop|follow|again <id>] | runs from <id> <step> [--start] | trigger add <kind> key=value ... run=<node> key=value ... | trigger list/on/off/remove/clear [name] | secret set <name> (prompt or stdin; one trailing line ending removed) | secret list | secret remove <name> | add <plugin source> | remove <plugin> | plugins [inspect <plugin source>] | start | stop | help | version"

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
	if program := adapterPrograms[args[0]]; program != nil && len(args) > 1 && args[1] == "--" {
		return program(ctx, args[2:], os.Stdin, out, diagnostic)
	}
	if len(args) == 1 {
		switch args[0] {
		case "help", "--help", "-h":
			_, err := fmt.Fprint(out, inode.Help(), "\n", usage, "\n")
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
	if operation == "secret set" {
		n = 1
	}
	if operation == "start" && len(rest) == 0 {
		exists = true
	}
	commands := map[string]func(context.Context, *inode.Client, []string, io.Writer, io.Writer) error{"run": run, "trigger add": addTrigger}
	arguments := make([]any, len(rest))
	for i, value := range rest {
		arguments[i] = value
		if commands[operation] == nil && (strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[")) {
			if err := json.Unmarshal([]byte(value), &arguments[i]); err != nil {
				return err
			}
		}
	}
	startFrom := false
	if operation == "runs from" {
		startFrom = len(rest) == 3 && rest[2] == "--start"
		if startFrom {
			arguments = arguments[:2]
		}
		arguments = append(arguments, startFrom)
	}
	if !exists || commands[operation] != nil && len(rest) < 1 || commands[operation] == nil && len(arguments) != n {
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
		fmt.Fprintln(diagnostic, "started backend for this project; switched-on triggers start when plugins are ready; use inode stop to stop it")
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
	if operation == "secret set" {
		var value []byte
		fd := int(os.Stdin.Fd())
		if term.IsTerminal(fd) {
			fmt.Fprint(diagnostic, "Secret value: ")
			value, err = term.ReadPassword(fd)
			fmt.Fprintln(diagnostic)
		} else {
			value, err = io.ReadAll(os.Stdin)
		}
		if err != nil {
			return err
		}
		secret := string(value)
		if text, ok := strings.CutSuffix(secret, "\n"); ok {
			secret = strings.TrimSuffix(text, "\r")
		}
		arguments = append(arguments, secret)
	}
	if operation == "add" {
		source, err := confirmPlugin(ctx, client, arguments[0], out, diagnostic)
		if err != nil || source == nil {
			return err
		}
		arguments[0] = source
	}
	err = client.Call(ctx, operation, arguments, func(data json.RawMessage) error {
		columns := map[string][]func(any) any{
			"nodes":        {field("name"), field("doc")},
			"runs":         {field("id"), field("node"), field("state"), startedBy},
			"plugins":      {field("name"), field("version"), field("state"), field("source"), field("error")},
			"trigger list": {field("name"), triggerInstruction, field("state"), runField("current"), field("waiting"), runField("last"), runField("last_failure"), field("error")},
			"secret list":  {func(row any) any { return row }},
		}[operation]
		if len(columns) > 0 {
			var rows []any
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
		if operation == "runs from" && !startFrom {
			var preview inode.RunFrom
			if err := json.Unmarshal(data, &preview); err != nil {
				return err
			}
			var source inode.Record
			if err := client.Call(ctx, "runs show", []any{preview.Source.Run}, func(data json.RawMessage) error { return json.Unmarshal(data, &source) }); err != nil {
				return err
			}
			fmt.Fprintf(diagnostic, "Source run %s, step %s, finished %s (%s ago)\n", preview.Source.Run, preview.Source.Step, source.Finished.Format(time.RFC3339), time.Since(source.Finished).Round(time.Second))
			fmt.Fprintf(diagnostic, "Start with: inode runs from %s %s --start\n", preview.Source.Run, preview.Source.Step)
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

func confirmPlugin(ctx context.Context, client *inode.Client, source any, out, diagnostic io.Writer) (any, error) {
	reader := bufio.NewScanner(os.Stdin)
	ask := func(label string, value any) string {
		if value != nil {
			label += fmt.Sprintf(" [%v]", value)
		}
		fmt.Fprint(diagnostic, label+": ")
		if !reader.Scan() {
			return ""
		}
		if reader.Text() == "" && value != nil {
			return fmt.Sprint(value)
		}
		return strings.TrimSpace(reader.Text())
	}
	answered := map[string]bool{}
	for {
		var plugin inode.Plugin
		if err := client.Call(ctx, "plugins inspect", []any{source}, func(data json.RawMessage) error { return json.Unmarshal(data, &plugin) }); err != nil {
			return nil, err
		}
		prepared := plugin.Preparation
		if prepared == nil {
			break
		}
		if plugin.Error != "" {
			return nil, fmt.Errorf("%s", plugin.Error)
		}
		fields := slices.DeleteFunc(slices.Clone(prepared.Editable), func(field inode.Editable) bool { return answered[field.Name] })
		if len(fields) == 0 {
			fmt.Fprintln(out, "Program:", inode.CommandText(plugin.Process.([]any)), "\nWorking folder:", prepared.WorkingDir)
			data, _ := json.MarshalIndent(prepared.Manifest, "", "  ")
			fmt.Fprintln(out, string(data))
			if answer := ask("Install plugin? [y/N]", nil); answer != "y" && answer != "yes" {
				return nil, reader.Err()
			}
			break
		}
		request := maps.Clone(prepared.Input)
		for _, field := range fields {
			answered[field.Name] = true
			switch field.Kind {
			case "bindings":
				var names []string
				if err := client.Call(ctx, "secret list", nil, func(data json.RawMessage) error { return json.Unmarshal(data, &names) }); err != nil {
					return nil, err
				}
				fmt.Fprintln(diagnostic, "Stored secrets:", strings.Join(names, ", "))
				bindings, _ := field.Value.(map[string]any)
				if bindings == nil {
					bindings = map[string]any{}
				}
				suggested := slices.DeleteFunc(slices.Sorted(maps.Keys(bindings)), func(key string) bool { return bindings[key] != "" })
				for {
					var key string
					if len(suggested) > 0 {
						key, suggested = suggested[0], suggested[1:]
					} else if key = ask(field.Label+" — variable name (blank to finish)", nil); key == "" {
						break
					}
					name := ask("Stored secret name for "+key+" (blank to leave unset)", nil)
					if delete(bindings, key); name == "" {
						continue
					}
					if !slices.Contains(names, name) {
						return nil, fmt.Errorf("store secret %q first with inode secret set", name)
					}
					bindings[key] = name
				}
				request[field.Name] = bindings
			default:
				for _, key := range slices.Sorted(maps.Keys(field.Choices)) {
					fmt.Fprintf(diagnostic, "%s: %s\n", key, field.Choices[key])
				}
				value := ask(field.Label, field.Value)
				if value != "" {
					request[field.Name] = value
				}
				if len(field.Choices) > 0 && field.Choices[value] == "" {
					return nil, fmt.Errorf("choose one of the listed values for %s", field.Label)
				}
			}
		}
		source = request
	}
	return source, nil
}

func field(name string) func(any) any {
	return func(row any) any { return row.(inode.Values)[name] }
}

func startedBy(row any) any {
	if source, ok := field("source")(row).(map[string]any); ok {
		return fmt.Sprintf("%s / %s", source["run"], source["step"])
	}
	return field("trigger")(row)
}

func runField(name string) func(any) any {
	return func(row any) any {
		run, _ := field(name)(row).(map[string]any)
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

func triggerInstruction(row any) any {
	flow, _ := field("workflow")(row).(map[string]any)
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
		if port != nil && port["type"] == "file" {
			if strings.HasPrefix(strings.TrimSpace(raw), "{") {
				if json.Unmarshal([]byte(raw), &value) != nil {
					value = raw
				}
			}
		} else if port != nil && port["type"] != "text" && port["type"] != "secret" {
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
