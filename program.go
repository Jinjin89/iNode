package inode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type program struct {
	Args   []any             `json:"args"`
	Stdin  any               `json:"stdin"`
	Stdout any               `json:"stdout"`
	Env    Values            `json:"env"`
	Files  map[string]string `json:"files"`
}

func CommandText(args []any) string {
	words := make([]string, len(args))
	for i, value := range args {
		word := value.(string)
		if word == "" || strings.ContainsAny(word, " \t\r\n\v\f'\"$`\\|&;<>*?[]{}()!~#") {
			word = "'" + strings.ReplaceAll(word, "'", "'\\''") + "'"
		}
		words[i] = word
	}
	return strings.Join(words, " ")
}

func argumentList(value any, key string) ([]any, error) {
	if text, ok := value.(string); ok {
		words, err := SplitCommand(text)
		if err != nil {
			return nil, fmt.Errorf("%s: %w; use an argument list for literal shell characters", key, err)
		}
		args := []any{}
		for _, word := range words {
			args = append(args, word)
		}
		return args, nil
	}
	if args, ok := value.([]any); ok {
		return args, nil
	}
	return nil, fmt.Errorf("%s: supply a program or an argument list, such as %s: [wc, -w]", key, key)
}

var unsetInput = errors.New("input is not filled; use a declared input path")

func programValue(value any, inputs Values, key string) (any, error) {
	result, err := walk(value, anyType, nil, func(value any, _ Type, _ valuePath) (any, error) {
		if text, ok := value.(string); ok {
			if parts := referenceParts(text); len(parts) > 1 && parts[0] == "inputs" {
				if _, filled := inputs[parts[1]]; !filled {
					return nil, unsetInput
				}
			}
			resolved, _, err := resolve(text, anyType, nil, Values{"inputs": inputs})
			return resolved, err
		}
		return value, nil
	})
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

func containsType(t Type, name string) bool {
	if t.Name == name || t.Item != nil && containsType(*t.Item, name) {
		return true
	}
	for _, field := range t.Fields {
		if containsType(field, name) {
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
		items, list := value.([]any)
		if !checking && (errors.Is(err, unsetInput) || list && len(items) == 0) {
			continue
		}
		if err != nil {
			return nil, err
		}
		t := typeOf(value)
		if containsType(t, "secret") {
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
			return p, fmt.Errorf("run: %w; use args, stdin, stdout, env and files", err)
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
	remaining := maps.Clone(outputs)
	for _, name := range keys(p.Files) {
		where := "run.files." + name
		if !filepath.IsLocal(name) || name == "." || filepath.Clean(name) != name || strings.ContainsAny(name, "\\\x00") || slices.Contains(strings.Split(name, "/"), "..") {
			return p, fmt.Errorf("%s: use a plain relative filename without .., such as output.html", where)
		}
		for other := range p.Files {
			if strings.HasPrefix(other, name+"/") {
				return p, fmt.Errorf("%s: filenames cannot overlap; choose separate input and output names", where)
			}
		}
		parts := referenceParts(p.Files[name])
		if len(parts) < 2 {
			return p, fmt.Errorf("%s: bind =inputs.name or =outputs.name", where)
		}
		if parts[0] == "outputs" {
			if len(parts) != 2 || remaining[parts[1]].Type.Name != "file" {
				return p, fmt.Errorf("%s: bind each declared file output once, using =outputs.name", where)
			}
			delete(remaining, parts[1])
		} else {
			value, err := programValue(p.Files[name], inputs.tree(), where)
			if err != nil {
				return p, err
			}
			if typeOf(value).Name != "file" {
				return p, fmt.Errorf("%s: stage a file input with =inputs.name", where)
			}
		}
	}
	if p.Stdout != nil {
		name, ok := p.Stdout.(string)
		if !ok || !portName(name) {
			return p, fmt.Errorf("run.stdout: bind one output name, such as stdout: count, not a list or object")
		}
		if _, found := remaining[name]; !found || len(remaining) != 1 {
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

func (p program) run(ctx context.Context, project *Project, dir string, inputs Values, outputs Ports) (Values, error) {
	args, err := arguments(p.Args, inputs, "run.args", false)
	if err != nil {
		return nil, err
	}
	if strings.ContainsRune(args[0], filepath.Separator) && !filepath.IsAbs(args[0]) {
		args[0] = filepath.Join(dir, args[0])
	}
	if len(p.Files) > 0 {
		work := filepath.Join(project.dir, ".inode", "work")
		if err := os.MkdirAll(work, 0700); err != nil {
			return nil, err
		}
		dir, err = os.MkdirTemp(work, "run-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		for _, name := range keys(p.Files) {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
				return nil, err
			}
			if referenceParts(p.Files[name])[0] == "inputs" {
				value, err := programValue(p.Files[name], inputs, "run.files."+name)
				if errors.Is(err, unsetInput) {
					continue
				}
				if err != nil {
					return nil, err
				}
				source, err := os.Open(project.blob(value.(fileRef)))
				if err != nil {
					return nil, err
				}
				err = writeStream(filepath.Join(dir, name), source, 0600, false)
				source.Close()
				if err != nil {
					return nil, err
				}
			}
		}
	}
	var input any = inputs
	if p.Stdin != nil {
		input, err = programValue(p.Stdin, inputs, "run.stdin")
		if errors.Is(err, unsetInput) {
			input, err = "", nil
		}
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
	var reader io.Reader = bytes.NewReader(data)
	if ref, ok := input.(fileRef); ok {
		file, err := os.Open(project.blob(ref))
		if err != nil {
			return nil, err
		}
		defer file.Close()
		reader = file
	}
	var writer io.Writer
	var stdout *os.File
	if p.Stdout != nil && outputs[p.Stdout.(string)].Type.Name == "file" {
		stdout, err = os.CreateTemp(filepath.Join(project.dir, ".inode"), ".stdout-")
		if err != nil {
			return nil, err
		}
		defer os.Remove(stdout.Name())
		defer stdout.Close()
		writer = stdout
	} else if p.Stdout == nil && len(p.Files) > 0 {
		remaining := maps.Clone(outputs)
		for _, ref := range p.Files {
			if parts := referenceParts(ref); parts[0] == "outputs" {
				delete(remaining, parts[1])
			}
		}
		if len(remaining) == 0 && len(outputs) > 0 {
			writer = io.Discard
		}
	}
	env := []string{}
	for _, name := range keys(p.Env) {
		value, err := programValue(p.Env[name], inputs, "run.env."+name)
		if errors.Is(err, unsetInput) {
			continue
		}
		if err != nil {
			return nil, err
		}
		env = append(env, name+"="+programText(value))
	}
	data, err = process(ctx, dir, reader, writer, env, args...)
	if err != nil {
		return nil, err
	}
	result := Values{}
	if stdout != nil {
		if _, err = stdout.Seek(0, 0); err != nil {
			return nil, err
		}
		result[p.Stdout.(string)], err = project.storeFile(stdout, p.Stdout.(string)+".bin")
		if err != nil {
			return nil, err
		}
	} else if p.Stdout != nil {
		name := p.Stdout.(string)
		var value any = string(data)
		if outputs[name].Type.Name != "text" {
			if err := json.Unmarshal(data, &value); err != nil {
				return nil, fmt.Errorf("run.stdout: output %q needs one JSON %s value: %w", name, outputs[name].Type, err)
			}
		}
		result[name] = value
	} else if writer == nil {
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("print one JSON object of outputs: %w", err)
		}
		if result == nil {
			return nil, fmt.Errorf("print one JSON object of outputs, not null")
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for _, name := range keys(p.Files) {
		parts := referenceParts(p.Files[name])
		if parts[0] != "outputs" {
			continue
		}
		if _, exists := result[parts[1]]; exists {
			return nil, fmt.Errorf("run.files.%s: output %s is already bound; omit it from JSON stdout", name, parts[1])
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		segments := strings.Split(name, "/")
		for i := range segments {
			info, err := root.Lstat(strings.Join(segments[:i+1], "/"))
			if err != nil || info.Mode()&os.ModeSymlink != 0 || i == len(segments)-1 && !info.Mode().IsRegular() {
				return nil, fmt.Errorf("run.files.%s: output %s must be a regular file without symlinks: %v", name, parts[1], err)
			}
		}
		file, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("run.files.%s: output %s must be a regular file", name, parts[1])
		}
		result[parts[1]], err = project.storeFile(file, name)
		if err != nil {
			return nil, err
		}
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
				return command.run(ctx, p, dir, inputs, description.Outputs)
			},
		})
	}
	return nil
}

const stderrLimit = 8 * 1024

type stderrTail struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (tail *stderrTail) Write(data []byte) (int, error) {
	tail.mu.Lock()
	defer tail.mu.Unlock()
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

func (tail *stderrTail) String() string {
	tail.mu.Lock()
	defer tail.mu.Unlock()
	message := strings.TrimSpace(string(tail.data))
	if tail.truncated {
		message = fmt.Sprintf("[stderr truncated; showing last %d bytes]\n%s", stderrLimit, message)
	}
	return message
}

func processCommand(ctx context.Context, dir string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return cmd
}

func process(ctx context.Context, dir string, input io.Reader, output io.Writer, env []string, args ...string) ([]byte, error) {
	cmd := processCommand(ctx, dir, append(os.Environ(), env...), args...)
	cmd.Stdin = input
	var buffer bytes.Buffer
	if output == nil {
		output = &buffer
	}
	cmd.Stdout = output
	var stderr stderrTail
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if cmd.ProcessState == nil {
			return nil, fmt.Errorf("program %q could not start: %w; install it or correct the program name", args[0], err)
		}
		return nil, fmt.Errorf("program %q: %w: %s", args[0], err, &stderr)
	}
	return buffer.Bytes(), nil
}
