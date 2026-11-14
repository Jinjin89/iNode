package inode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type program struct {
	Args   []any  `json:"args"`
	Stdin  any    `json:"stdin"`
	Stdout any    `json:"stdout"`
	Env    Values `json:"env"`
}

func argumentList(value any, key string) ([]any, error) {
	if text, ok := value.(string); ok {
		if strings.ContainsAny(text, "\"'|&;<>$`()*?[]{}~!#\\\r\n") {
			return nil, fmt.Errorf("%s: write an argument list such as %s: [program, \"one argument\"] instead of shell quoting or characters", key, key)
		}
		args := []any{}
		for _, word := range strings.Fields(text) {
			args = append(args, word)
		}
		return args, nil
	}
	if args, ok := value.([]any); ok {
		return args, nil
	}
	return nil, fmt.Errorf("%s: supply a program or an argument list, such as %s: [wc, -w]", key, key)
}

func programValue(value any, inputs Values, key string) (any, error) {
	result, _, err := resolve(value, nil, Values{"inputs": inputs})
	if err != nil {
		if problem, ok := err.(Problem); ok {
			err = fmt.Errorf("%s", problem.Message)
		}
		return nil, fmt.Errorf("%s: %w; use a declared input path or == for literal text starting with =", key, err)
	}
	return result, nil
}

func argumentType(t Type) bool {
	if t.Name == "list" {
		return t.Item != nil && argumentScalar(*t.Item)
	}
	return argumentScalar(t)
}

func argumentScalar(t Type) bool {
	return t.Name == "text" || t.Name == "number" || t.Name == "bool"
}

func programText(value any) string {
	if number, ok := value.(float64); ok {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	return fmt.Sprint(value)
}

func secretType(t Type) bool {
	if t.Name == "secret" || t.Item != nil && secretType(*t.Item) {
		return true
	}
	for _, field := range t.Fields {
		if secretType(field) {
			return true
		}
	}
	return false
}

func arguments(args []any, inputs Values, key string, checking bool) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("%s: supply a fixed program as the first argument", key)
	}
	name, ok := args[0].(string)
	if !ok || name == "" || referenceParts(name) != nil {
		return nil, fmt.Errorf("%s.0: write a fixed program name, such as wc, not an input reference", key)
	}
	result := []string{name}
	for i, raw := range args[1:] {
		where := fmt.Sprintf("%s.%d", key, i+1)
		flag := ""
		if fields, ok := raw.(map[string]any); ok {
			flag, ok = fields["flag"].(string)
			if !ok || flag == "" || referenceParts(flag) != nil || len(fields) != 2 {
				return nil, fmt.Errorf("%s: write {flag: --name, value: =inputs.name} with a literal flag", where)
			}
			var found bool
			raw, found = fields["value"]
			if !found {
				return nil, fmt.Errorf("%s.value: supply a value, such as =inputs.name", where)
			}
			where += ".value"
		}
		value, err := programValue(raw, inputs, where)
		if err != nil {
			return nil, err
		}
		t := typeOf(value)
		if items, ok := value.([]any); !checking && ok && len(items) == 0 {
			continue
		}
		if secretType(t) {
			return nil, fmt.Errorf("%s: secrets cannot be arguments; bind the input through stdin or env instead", where)
		}
		if !argumentType(t) {
			return nil, fmt.Errorf("%s: use text, number, bool or a list of those for an argument or flag; got %s", where, t)
		}
		if checking {
			continue
		}
		if enabled, ok := value.(bool); flag != "" && ok {
			if enabled {
				result = append(result, flag)
			}
			continue
		}
		items, list := value.([]any)
		if !list {
			items = []any{value}
		}
		for _, item := range items {
			if flag != "" {
				result = append(result, flag)
			}
			result = append(result, programText(item))
		}
	}
	return result, nil
}

func loadProgram(value any, inputs, outputs Ports) (program, error) {
	var p program
	if _, object := value.(map[string]any); object {
		data, _ := json.Marshal(value)
		if err := decode(data, &p); err != nil {
			return p, fmt.Errorf("run: %w; use args, stdin, stdout and env", err)
		}
	} else {
		args, err := argumentList(value, "run")
		if err != nil {
			return p, err
		}
		p.Args = args
	}
	if _, err := arguments(p.Args, inputs.tree(), "run.args", true); err != nil {
		return p, err
	}
	if p.Stdin != nil {
		if _, err := programValue(p.Stdin, inputs.tree(), "run.stdin"); err != nil {
			return p, err
		}
	}
	if p.Stdout != nil {
		name, ok := p.Stdout.(string)
		if !ok || !portName(name) {
			return p, fmt.Errorf("run.stdout: bind one output name, such as stdout: count, not a list or object")
		}
		if _, found := outputs[name]; !found || len(outputs) != 1 {
			return p, fmt.Errorf("run.stdout: declare just output %q; use one object output to return several values", name)
		}
	}
	for _, name := range keys(p.Env) {
		where := "run.env." + name
		if name == "" || strings.ContainsAny(name, "=\x00") {
			return p, fmt.Errorf("%s: choose a nonempty environment name without equals signs or NUL", where)
		}
		value, err := programValue(p.Env[name], inputs.tree(), where)
		if err != nil {
			return p, err
		}
		if t := typeOf(value); !argumentScalar(t) && t.Name != "secret" {
			return p, fmt.Errorf("%s: bind a text, number, bool or secret input, not %s", where, t)
		}
	}
	return p, nil
}

func (p program) run(ctx context.Context, dir string, inputs Values, outputs Ports) (Values, error) {
	args, err := arguments(p.Args, inputs, "run.args", false)
	if err != nil {
		return nil, err
	}
	var input any = inputs
	if p.Stdin != nil {
		input, err = programValue(p.Stdin, inputs, "run.stdin")
		if err != nil {
			return nil, err
		}
	}
	data, err := json.Marshal(input)
	if text, ok := input.(string); ok {
		data = []byte(text)
	} else {
		data = append(data, '\n')
	}
	if err != nil {
		return nil, err
	}
	env := []string{}
	for _, name := range keys(p.Env) {
		value, err := programValue(p.Env[name], inputs, "run.env."+name)
		if err != nil {
			return nil, err
		}
		env = append(env, name+"="+programText(value))
	}
	data, err = process(ctx, dir, data, env, args...)
	if err != nil {
		return nil, err
	}
	if p.Stdout != nil {
		name := p.Stdout.(string)
		var value any = string(data)
		if outputs[name].Type.Name != "text" {
			if err := json.Unmarshal(data, &value); err != nil {
				return nil, fmt.Errorf("run.stdout: output %q needs one JSON %s value: %w", name, outputs[name].Type, err)
			}
		}
		return Values{name: value}, nil
	}
	var result Values
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("print one JSON object of outputs: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("print one JSON object of outputs, not null")
	}
	return result, nil
}

func (p *Project) loadNodes(dir, prefix string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".node") {
			continue
		}
		var description struct {
			Doc             string `json:"doc"`
			Inputs          Ports  `json:"inputs"`
			Outputs         Ports  `json:"outputs"`
			NoOutsideEffect bool   `json:"no_outside_effect"`
			Run             any    `json:"run"`
		}
		path := filepath.Join(dir, entry.Name())
		err := readYAML(path, &description)
		var command program
		if err == nil {
			if description.Doc == "" {
				err = fmt.Errorf("doc: supply a one-sentence description")
			} else {
				command, err = loadProgram(description.Run, description.Inputs, description.Outputs)
			}
			if err != nil {
				err = fmt.Errorf("%s: %w", path, err)
			}
		}
		p.register(prefix+strings.TrimSuffix(entry.Name(), ".node"), Node{
			Doc: description.Doc, Inputs: description.Inputs, Outputs: description.Outputs,
			NoOutsideEffect: description.NoOutsideEffect, problem: err,
			run: func(ctx context.Context, inputs Values, _ Call) (Values, error) {
				return command.run(ctx, dir, inputs, description.Outputs)
			},
		})
	}
	return nil
}

const stderrLimit = 8 * 1024

type stderrTail struct {
	data      []byte
	truncated bool
}

func (tail *stderrTail) Write(data []byte) (int, error) {
	n := len(data)
	if n > stderrLimit {
		data = data[n-stderrLimit:]
		tail.truncated = true
	}
	if excess := len(tail.data) + len(data) - stderrLimit; excess > 0 {
		tail.data = tail.data[excess:]
		tail.truncated = true
	}
	tail.data = append(tail.data, data...)
	return n, nil
}

func process(ctx context.Context, dir string, input []byte, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir, cmd.Stdin = dir, bytes.NewReader(input)
	if len(env) != 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var stderr stderrTail
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if cmd.ProcessState == nil {
			return nil, fmt.Errorf("program %q could not start: %w; install it or correct the program name", args[0], err)
		}
		message := strings.TrimSpace(string(stderr.data))
		if stderr.truncated {
			message = fmt.Sprintf("[stderr truncated; showing last %d bytes]\n%s", stderrLimit, message)
		}
		return nil, fmt.Errorf("program %q: %w: %s", args[0], err, message)
	}
	return output, nil
}
