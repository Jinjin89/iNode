package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"go.yaml.in/yaml/v3"
	"inode"
)

const usage = "usage: inode run <node> key=value ... | nodes | show <node> | check <workflow> | runs [show|stop|follow <id>] | add <path or git URL> | remove <plugin> | plugins | start | stop | help | version"

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
	if !exists || operation == "run" && len(rest) < 1 || operation != "run" && len(rest) != n {
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
		fmt.Fprintln(diagnostic, "started backend for this project; use inode stop to stop it")
	}
	if operation == "start" {
		_, err := fmt.Fprintf(out, "backend running at http://%s\n", client.Address)
		return err
	}
	if operation == "run" {
		return run(ctx, client, rest, out, diagnostic)
	}
	arguments := make([]any, len(rest))
	for i, value := range rest {
		arguments[i] = value
	}
	err = client.Call(ctx, operation, arguments, func(data json.RawMessage) error {
		columns := map[string][]string{"nodes": {"name", "doc"}, "runs": {"id", "node", "state"}, "plugins": {"name", "version", "installed", "source"}}[operation]
		if len(columns) > 0 {
			var rows []inode.Values
			if err := json.Unmarshal(data, &rows); err != nil {
				return err
			}
			for _, row := range rows {
				values := make([]string, len(columns))
				for i, column := range columns {
					values[i] = fmt.Sprint(row[column])
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

func run(ctx context.Context, client *inode.Client, args []string, out, diagnostic io.Writer) error {
	var node inode.Values
	if err := client.Call(ctx, "show", []any{args[0]}, func(data json.RawMessage) error { return json.Unmarshal(data, &node) }); err != nil {
		return err
	}
	ports, _ := node["inputs"].(map[string]any)
	inputs := inode.Values{}
	for _, arg := range args[1:] {
		key, raw, found := strings.Cut(arg, "=")
		if !found {
			return fmt.Errorf("input %q: use key=value", arg)
		}
		if _, duplicate := inputs[key]; duplicate {
			return fmt.Errorf("input %q is repeated; supply it once", key)
		}
		var value any = raw
		port, _ := ports[key].(map[string]any)
		if port != nil && port["type"] != "text" {
			if err := yaml.Unmarshal([]byte(raw), &value); err != nil {
				return fmt.Errorf("input %q: supply a YAML value: %w", key, err)
			}
		}
		inputs[key] = value
	}
	var id string
	// Keep the acknowledgement so an interrupt can stop the accepted run.
	if err := client.Call(context.WithoutCancel(ctx), "run", []any{args[0], inputs}, func(data json.RawMessage) error { return json.Unmarshal(data, &id) }); err != nil {
		return err
	}
	var record inode.Record
	receive := func(data json.RawMessage) error { return json.Unmarshal(data, &record) }
	err := client.Call(ctx, "runs follow", []any{id}, receive)
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
