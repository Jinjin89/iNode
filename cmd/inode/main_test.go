package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"inode"
)

var cliBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "inode-cli-test-")
	if err != nil {
		panic(err)
	}
	cliBinary = filepath.Join(dir, "inode")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-race", "-o", cliBinary, ".")
	if data, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		fmt.Fprintf(os.Stderr, "%s: %v\n", data, err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func invokeCLI(ctx context.Context, args []string, out, diagnostic *bytes.Buffer) error {
	cmd := exec.CommandContext(ctx, cliBinary, args...)
	cmd.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	cmd.Stdout, cmd.Stderr = out, diagnostic
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, diagnostic)
	}
	return nil
}

func cliProject(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var out, diagnostic bytes.Buffer
		if err := invokeCLI(ctx, []string{"stop"}, &out, &diagnostic); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		if _, _, err := inode.Connect(ctx, ".", false); err == nil {
			t.Error("backend left running")
		}
	})
}

func TestHelp(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, arg := range []string{"help", "-h", "--help"} {
		var out, diagnostic bytes.Buffer
		if err := invokeCLI(context.Background(), []string{arg}, &out, &diagnostic); err != nil {
			t.Fatal(err)
		}
		if out.String() != inode.Help()+"\n"+usage+"\n" || diagnostic.Len() != 0 {
			t.Fatalf("help: %s; %s", &out, &diagnostic)
		}
	}
	if _, err := os.Stat(".inode"); !os.IsNotExist(err) {
		t.Fatal("help touched the project")
	}
}

func TestCommands(t *testing.T) {
	cliProject(t)
	if err := os.WriteFile("upper.flow", []byte("inputs: {text: text}\nsteps: {upper: {use: text.upper, in: {text: =inputs.text}}}\noutputs: {text: =upper.text}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args    []string
		content string
		fails   bool
	}{
		{[]string{"nodes"}, "upper\t", false},
		{[]string{"show", "upper"}, `"no_outside_effect": true`, false},
		{[]string{"check", "upper.flow"}, `"problems": []`, false},
		{[]string{"check", `{"outputs":{"text":"hi"}}`}, `"problems": []`, false},
		{[]string{"save", "draft", "", `{"outputs":{"text":"hi"}}`}, `"name": "draft"`, false},
		{[]string{"run", "upper", "text=hi"}, `"text": "HI"`, false},
		{[]string{"run", "text.upper", "text=123"}, `"text": "123"`, false},
		{[]string{"run", "map", "over=[hi, yo]", "do=text.upper"}, `"YO"`, false},
		{[]string{"run", "retry", "times=1", "do={use: text.upper, in: {text: hi}}"}, `"result": "HI"`, false},
		{[]string{"run", "number.add", "a=2", "b=3"}, `"value": 5`, false},
		{[]string{"run", "text.join", "parts=[a, b]", "separator=,"}, `"text": "a,b"`, false},
		{[]string{"run", "data.value", "value==a.b"}, `"value": "=a.b"`, false},
		{[]string{"runs"}, "\tupper\tdone", false},
		{[]string{"run", "upper"}, "required port", true},
		{[]string{"run", "upper", "text=hi", "other=1"}, "unknown port", true},
		{[]string{"run", "upper", "text=hi", "text=bye"}, "repeated", true},
		{[]string{"run", "upper", "text"}, "use key=value", true},
		{[]string{"run", "number.add", "a=hi", "b=3"}, "needs number", true},
		{[]string{"run", "number.divide", "a=1", "b=0"}, "non-zero divisor", true},
		{[]string{"run", "text.join", "parts=["}, "YAML value", true},
		{[]string{"show", "missing"}, "unknown node", true},
		{[]string{"check", "missing"}, "unknown node", true},
		{[]string{"nodes", "extra"}, "usage:", true},
		{nil, "usage:", true},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			err := invokeCLI(context.Background(), test.args, &out, &diagnostic)
			if test.fails {
				if err == nil || !strings.Contains(err.Error(), test.content) {
					t.Fatalf("wanted %q; got %v", test.content, err)
				}
			} else if err != nil || !strings.Contains(out.String(), test.content) || test.args[0] != "nodes" && test.args[0] != "runs" && !json.Valid(out.Bytes()) {
				t.Fatalf("got %s, %v; wanted %q", &out, err, test.content)
			}
		})
	}
	var out, diagnostic bytes.Buffer
	if err := invokeCLI(context.Background(), []string{"runs"}, &out, &diagnostic); err != nil || out.Len() == 0 {
		t.Fatalf("missing records: %s, %v", &out, err)
	}
	id := strings.Fields(out.String())[0]
	out.Reset()
	if err := invokeCLI(context.Background(), []string{"runs", "show", id}, &out, &diagnostic); err != nil || !strings.Contains(out.String(), id) {
		t.Fatalf("show run: %s, %v", &out, err)
	}
	out.Reset()
	if err := invokeCLI(context.Background(), []string{"runs", "again", id}, &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	var next string
	if err := json.Unmarshal(out.Bytes(), &next); err != nil || next == "" || next == id {
		t.Fatalf("run again: %s, %v", &out, err)
	}
}

func TestPluginCommands(t *testing.T) {
	source, err := filepath.Abs("../../examples/tiny_plugin")
	if err != nil {
		t.Fatal(err)
	}
	cliProject(t)
	if err := os.CopyFS("tiny_plugin", os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args    []string
		content string
	}{
		{[]string{"add", "tiny_plugin"}, "tiny\t1\nAdd a short greeting\nAccesses:\n- Reads and writes its own greeting file."},
		{[]string{"plugins"}, "tiny\t1\tinstalled\ttiny_plugin"},
		{[]string{"nodes"}, "tiny.greet\tGreet a name"},
		{[]string{"show", "tiny.greet"}, `"name": "tiny.greet"`},
		{[]string{"run", "tiny.greet", "name=cat"}, `"text": "hi cat"`},
	} {
		var out, diagnostic bytes.Buffer
		if err := invokeCLI(context.Background(), test.args, &out, &diagnostic); err != nil || !strings.Contains(out.String(), test.content) {
			t.Fatalf("%v: %s, %v", test.args, &out, err)
		}
	}
	if err := os.RemoveAll(".inode/plugins/tiny"); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if err := invokeCLI(context.Background(), []string{"plugins"}, &out, &diagnostic); err != nil || !strings.Contains(out.String(), "tiny\t1\tmissing\ttiny_plugin") {
		t.Fatalf("missing: %s, %v", &out, err)
	}
	if err := invokeCLI(context.Background(), []string{"show", "tiny.greet"}, &out, &diagnostic); err == nil || !strings.Contains(err.Error(), "inode add tiny_plugin") {
		t.Fatalf("missing diagnosis: %v", err)
	}
	if err := invokeCLI(context.Background(), []string{"remove", "tiny"}, &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := invokeCLI(context.Background(), []string{"plugins"}, &out, &diagnostic); err != nil || out.Len() != 0 {
		t.Fatalf("removed: %s, %v", &out, err)
	}
}

func TestTriggerCommands(t *testing.T) {
	cliProject(t)
	if err := os.WriteFile("upper.flow", []byte("inputs: {text: text}\nsteps: {u: {use: text.upper, in: {text: =inputs.text}}}\noutputs: {text: =u.text}"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if err := invokeCLI(t.Context(), []string{"trigger", "add", "schedule.every", "interval=0.002", "run=upper", "text=hi"}, &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := json.Unmarshal(out.Bytes(), &name); err != nil || name != "schedule.every-1" {
		t.Fatalf("trigger name: %s, %v", &out, err)
	}
	for _, args := range [][]string{{"trigger", "list"}, {"trigger", "on", name}, {"trigger", "off", name}, {"trigger", "clear", name}, {"trigger", "remove", name}} {
		out.Reset()
		diagnostic.Reset()
		if err := invokeCLI(t.Context(), args, &out, &diagnostic); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if args[1] == "list" && !strings.Contains(out.String(), name+"\tschedule.every interval=0.002 -> upper\toff\t") {
			t.Fatalf("trigger list: %s", &out)
		}
	}
	out.Reset()
	if err := invokeCLI(t.Context(), []string{"trigger", "list"}, &out, &diagnostic); err != nil || out.Len() != 0 {
		t.Fatalf("trigger remove: %s, %v", &out, err)
	}
}

func TestSecretCommands(t *testing.T) {
	cliProject(t)
	var out, diagnostic bytes.Buffer
	if err := invokeCLI(t.Context(), []string{"secret", "set", "token", "argument-value"}, &out, &diagnostic); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("secret set must refuse an argument value: %v", err)
	}
	if _, err := os.Stat(".inode"); !os.IsNotExist(err) {
		t.Fatal("invalid command started a backend")
	}
	if err := os.Mkdir("nodes", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("nodes/length.node", []byte("doc: Length\ninputs: {token: secret}\noutputs: {length: number}\nrun: python3 length.py"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("nodes/length.py", []byte("import json,sys\nprint(json.dumps({'length':len(json.load(sys.stdin)['token'])}))\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		input string
		want  int
	}{{"abc", 3}, {"abc\n", 3}, {"abc\r\n", 3}, {"abc\n\n", 4}, {"abc\r", 4}} {
		out.Reset()
		diagnostic.Reset()
		cmd := exec.Command(cliBinary, "secret", "set", "123")
		cmd.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
		cmd.Stdin = strings.NewReader(test.input)
		cmd.Stdout, cmd.Stderr = &out, &diagnostic
		if err := cmd.Run(); err != nil {
			t.Fatalf("set from stdin: %s, %v", &diagnostic, err)
		}
		if err := invokeCLI(t.Context(), []string{"run", "length", "token=123"}, &out, &diagnostic); err != nil || !strings.Contains(out.String(), fmt.Sprintf(`"length": %d`, test.want)) || strings.Contains(out.String()+diagnostic.String(), "abc") {
			t.Fatalf("stdin %q: %s, %v", test.input, &out, err)
		}
	}
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"secret", "list"}, "123\n"},
		{[]string{"secret", "remove", "123"}, ""},
		{[]string{"secret", "list"}, ""},
	} {
		out.Reset()
		diagnostic.Reset()
		if err := invokeCLI(t.Context(), test.args, &out, &diagnostic); err != nil || out.String() != test.want {
			t.Fatalf("%v: %s, %v", test.args, &out, err)
		}
	}
}

func TestSecretTerminalPrompt(t *testing.T) {
	cliProject(t)
	script := `import os, pty, select, subprocess, sys, termios, time
master, slave = pty.openpty()
initial = termios.tcgetattr(slave)
process = subprocess.Popen([sys.argv[1], "secret", "set", "token"], stdin=slave, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
try:
    diagnostic = b""
    while not diagnostic.endswith(b"Secret value: "):
        assert select.select([process.stderr], [], [], 5)[0], "no prompt"
        data = os.read(process.stderr.fileno(), 4096)
        assert data, "exited before prompting"
        diagnostic += data
    deadline = time.monotonic() + 5
    while termios.tcgetattr(slave)[3] & termios.ECHO:
        assert time.monotonic() < deadline, "echo stayed enabled"
        time.sleep(0.001)
    os.write(master, b"abc\n")
    output, error = process.communicate(timeout=5)
    assert process.returncode == 0, error
    assert termios.tcgetattr(slave) == initial, "terminal was not restored"
    echoed = os.read(master, 4096) if select.select([master], [], [], 0)[0] else b""
    assert b"abc" not in diagnostic + output + error + echoed, "secret was echoed"
    assert output == b"" and error == b"\n", (output, error)
finally:
    if process.poll() is None:
        process.kill()
        process.wait()
    os.close(master)
    os.close(slave)
`
	cmd := exec.CommandContext(t.Context(), "python3", "-c", script, cliBinary)
	cmd.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("terminal prompt: %s, %v", data, err)
	}
	value, err := os.ReadFile(".inode/secrets/token.secret")
	if err != nil || string(value) != "abc" {
		t.Fatalf("terminal input: %q, %v", value, err)
	}
}

func TestRunFromCommand(t *testing.T) {
	cliProject(t)
	flow := "steps: {a: {use: text.upper, in: {text: hi}}, b: {use: text.upper, in: {text: =a.text}}}\noutputs: {text: =b.text}"
	if err := os.WriteFile("tiny.flow", []byte(flow), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if err := invokeCLI(t.Context(), []string{"run", "tiny"}, &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := invokeCLI(t.Context(), []string{"runs"}, &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	id := strings.Fields(out.String())[0]
	for _, start := range []bool{false, true} {
		args := []string{"runs", "from", id, "b"}
		if start {
			args = append(args, "--start")
		}
		out.Reset()
		diagnostic.Reset()
		if err := invokeCLI(t.Context(), args, &out, &diagnostic); err != nil {
			t.Fatal(err)
		}
		if !start && (!strings.Contains(out.String(), `"source"`) || !strings.Contains(out.String(), `"HI"`) || !strings.Contains(diagnostic.String(), "ago") || !strings.Contains(diagnostic.String(), "--start")) {
			t.Fatalf("preview/provenance/confirmation missing: %s, %s", &out, &diagnostic)
		}
		if start {
			var next string
			if err := json.Unmarshal(out.Bytes(), &next); err != nil || next == "" || next == id {
				t.Fatalf("start: %s, %v", &out, err)
			}
		}
	}
}

func TestBackendLifecycleCommands(t *testing.T) {
	cliProject(t)
	for _, args := range [][]string{nil, {"help"}, {"version"}, {"unknown"}, {"nodes", "extra"}, {"stop"}} {
		var out, diagnostic bytes.Buffer
		invokeCLI(context.Background(), args, &out, &diagnostic)
		if _, err := os.Stat(".inode"); !os.IsNotExist(err) {
			t.Fatalf("%v started or prepared a backend", args)
		}
	}
	var out, diagnostic bytes.Buffer
	if err := invokeCLI(context.Background(), []string{"nodes"}, &out, &diagnostic); err != nil || !strings.Contains(diagnostic.String(), "started backend") || !strings.Contains(diagnostic.String(), "inode stop") {
		t.Fatalf("automatic start: %s, %v", &diagnostic, err)
	}
	first, err := os.ReadFile(".inode/backend")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"nodes"}, {"start"}} {
		out.Reset()
		diagnostic.Reset()
		if err := invokeCLI(context.Background(), args, &out, &diagnostic); err != nil || strings.Contains(diagnostic.String(), "started backend") {
			t.Fatalf("reuse %v: %s, %v", args, &diagnostic, err)
		}
		current, err := os.ReadFile(".inode/backend")
		if err != nil || !bytes.Equal(first, current) {
			t.Fatalf("%v replaced the backend", args)
		}
	}
	out.Reset()
	diagnostic.Reset()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := invokeCLI(ctx, []string{"serve"}, &out, &diagnostic); err == nil || !strings.Contains(diagnostic.String(), "already holds") {
		t.Fatalf("second backend: %s, %v", &diagnostic, err)
	}
	out.Reset()
	diagnostic.Reset()
	if err := invokeCLI(ctx, []string{"stop"}, &out, &diagnostic); err != nil || out.String() != "backend stopped\n" {
		t.Fatalf("stop: %s, %v", &out, err)
	}
	if _, err := os.Stat(".inode/backend"); !os.IsNotExist(err) {
		t.Fatal("stop kept the connection file")
	}
	out.Reset()
	diagnostic.Reset()
	if err := invokeCLI(ctx, []string{"start"}, &out, &diagnostic); err != nil || !strings.Contains(diagnostic.String(), "started backend") || !strings.Contains(out.String(), "http://127.0.0.1:") {
		t.Fatalf("explicit start: %s, %s, %v", &out, &diagnostic, err)
	}
}

func TestInterruptRunCommand(t *testing.T) {
	cliProject(t)
	if err := os.Mkdir("nodes", 0700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"wait.node": "doc: Wait\nrun: python3 wait.py",
		"wait.py":   "import json, signal, sys\njson.load(sys.stdin)\nopen('ready', 'w').close()\nsignal.pause()\n",
	} {
		if err := os.WriteFile(filepath.Join("nodes", name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out, diagnostic bytes.Buffer
	cmd := exec.Command(cliBinary, "run", "wait")
	cmd.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat("nodes/ready"); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("command exited before its run: %s, %v", &diagnostic, err)
		case <-deadline:
			t.Fatal("program did not start")
		case <-tick.C:
		}
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(diagnostic.String(), "interrupted") {
			t.Fatalf("interruption: %s, %v", &diagnostic, err)
		}
	case <-deadline:
		t.Fatal("interrupted command did not stop its run")
	}
	out.Reset()
	diagnostic.Reset()
	if err := invokeCLI(context.Background(), []string{"runs"}, &out, &diagnostic); err != nil || !strings.Contains(out.String(), "\twait\tinterrupted") {
		t.Fatalf("backend run survived the interrupt: %s, %v", &out, err)
	}
}
