package inode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type pluginHost struct {
	mu        sync.Mutex
	ctx       context.Context
	processes map[string]*pluginProcess
}

type pluginReply struct {
	ID      string `json:"id"`
	Nodes   Values `json:"nodes,omitempty"`
	Outputs Values `json:"outputs,omitempty"`
	Error   string `json:"error,omitempty"`
}

type pluginProcess struct {
	mu, writer sync.Mutex
	name       string
	cmd        *exec.Cmd
	stdin      *os.File
	done       chan struct{}
	ready      chan struct{}
	pending    map[string]chan pluginReply
	nodes      map[string]Node
	err        error
	clean      func(any) any
	secrets    map[string]string
	stderr     stderrTail
	buffer     []byte
	readErr    error
	sequence   uint64
}

func (h *pluginHost) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, process := range h.processes {
		<-process.ready
		process.close()
	}
}

func (h *pluginHost) launch(p *Project, plugin Plugin) *pluginProcess {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.processes[plugin.Name]
	if r == nil {
		r = &pluginProcess{name: plugin.Name, done: make(chan struct{}), ready: make(chan struct{}), pending: map[string]chan pluginReply{}, clean: func(v any) any { return v }}
		h.processes[plugin.Name] = r
		go r.start(h.ctx, p, plugin)
	}
	return r
}

func (h *pluginHost) state(name string) (string, string) {
	if h == nil {
		return "unavailable", "start the project's backend to run this plugin"
	}
	h.mu.Lock()
	r := h.processes[name]
	h.mu.Unlock()
	if r != nil {
		select {
		case <-r.ready:
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.err != nil {
				return "unavailable", r.err.Error()
			}
			return "running", ""
		default:
		}
	}
	return "starting", ""
}

func (p *Project) attachProcess(r *pluginProcess) {
	<-r.ready
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, node := range r.nodes {
		node.problem = r.err
		node.run = func(ctx context.Context, inputs Values, _ Call) (Values, error) {
			return r.run(ctx, p, name, inputs, node)
		}
		p.register(r.name+"."+name, node)
	}
}

func (r *pluginProcess) start(ctx context.Context, p *Project, plugin Plugin) {
	defer close(r.ready)
	ports, names := Ports{}, Values{}
	for key, name := range plugin.Env {
		ports[key], names[key] = Port{Type: Type{Name: "secret"}}, name
	}
	values, clean, err := p.secretInputs(names, ports)
	r.clean = clean
	r.secrets = map[string]string{}
	env := []string{}
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "LANG"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	for _, key := range keys(values) {
		env = append(env, key+"="+values[key].(string))
		if value := values[key].(string); value != "" {
			r.secrets[value] = "[secret " + plugin.Env[key] + "]"
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		args := []string{}
		for _, arg := range plugin.Process.([]any) {
			args = append(args, arg.(string))
		}
		r.cmd = processCommand(context.WithoutCancel(ctx), p.pluginDir(plugin.Name), env, args...)
		r.cmd.Stdout, r.cmd.Stderr, r.cmd.WaitDelay = r, &r.stderr, 250*time.Millisecond
		var input *os.File
		input, r.stdin, err = os.Pipe()
		if err == nil {
			r.cmd.Stdin = input
			err = r.cmd.Start()
			input.Close()
		}
	}
	if err != nil {
		r.fail(err)
		close(r.done)
		r.close()
		return
	}
	go func() {
		err := r.cmd.Wait()
		r.cmd.Cancel()
		if r.readErr != nil {
			err = r.readErr
		}
		if err == nil {
			err = fmt.Errorf("process exited")
		}
		r.fail(err)
		r.stdin.Close()
		close(r.done)
	}()
	startup, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	reply, err := r.call(startup, Values{"method": "describe"})
	if err == nil {
		data, _ := json.Marshal(r.clean(reply.Nodes))
		err = decode(data, &r.nodes)
		for name, node := range r.nodes {
			if !portName(name) || node.Doc == "" || node.Workflow != nil || node.Trigger {
				err = fmt.Errorf("describe: supply ordinary node names, descriptions and ports")
			}
		}
		if reply.Nodes == nil {
			err = fmt.Errorf("describe: reply with nodes")
		}
	}
	if err != nil {
		r.fail(err)
		r.close()
	}
}

func (r *pluginProcess) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	status := "stopped"
	if r.cmd == nil || r.cmd.Process == nil {
		status = "could not start"
	}
	message := fmt.Sprintf("The program for plugin %q %s (%v). Runs that were in progress may or may not have finished. Add the plugin again.", r.name, status, err)
	if err == context.DeadlineExceeded {
		message = fmt.Sprintf("The program for plugin %q did not answer within 2 minutes. Add the plugin again.", r.name)
	}
	if tail := r.stderr.String(); tail != "" {
		message += "\n" + tail
	}
	r.err = fmt.Errorf("%s", r.clean(message))
	for id, reply := range r.pending {
		reply <- pluginReply{Error: r.err.Error()}
		delete(r.pending, id)
	}
}

func (r *pluginProcess) close() {
	if r.stdin != nil {
		r.stdin.Close()
	}
	if r.cmd == nil || r.cmd.Process == nil {
		return
	}
	select {
	case <-r.done:
	case <-time.After(250 * time.Millisecond):
		r.cmd.Cancel()
		<-r.done
	}
}

// exec drains stdout through this writer, so Wait sees every decoded reply first.
func (r *pluginProcess) Write(data []byte) (int, error) {
	r.buffer = append(r.buffer, data...)
	for {
		end := bytes.IndexByte(r.buffer, '\n')
		if end < 0 && len(r.buffer) <= 1<<20 {
			return len(data), nil
		}
		var reply pluginReply
		err := fmt.Errorf("unreadable plugin stdout; use one JSON reply per line, at most 1 MiB")
		if end >= 0 && end <= 1<<20 && json.Valid(r.buffer[:end]) {
			err = decode(r.buffer[:end], &reply)
		}
		if err == nil && (reply.ID == "" || (reply.Nodes == nil && reply.Outputs == nil && reply.Error == "")) {
			err = fmt.Errorf("reply needs id and nodes, outputs or error")
		}
		if err != nil {
			r.readErr = err
			r.cmd.Cancel()
			return 0, err
		}
		r.mu.Lock()
		pending := r.pending[reply.ID]
		delete(r.pending, reply.ID)
		if pending != nil {
			pending <- reply
		}
		r.mu.Unlock()
		if pending == nil {
			r.readErr = fmt.Errorf("reply has an unknown or repeated id")
			r.cmd.Cancel()
			return 0, r.readErr
		}
		r.buffer = r.buffer[end+1:]
	}
}

func (r *pluginProcess) send(message Values) error {
	r.writer.Lock()
	defer r.writer.Unlock()
	r.stdin.SetWriteDeadline(time.Now().Add(time.Second))
	err := json.NewEncoder(r.stdin).Encode(message)
	if err != nil {
		r.stdin.Close()
		r.cmd.Cancel()
	}
	return err
}

func (r *pluginProcess) call(ctx context.Context, message Values) (pluginReply, error) {
	r.mu.Lock()
	if r.err != nil {
		err := r.err
		r.mu.Unlock()
		return pluginReply{}, err
	}
	r.sequence++
	id := fmt.Sprint(r.sequence)
	message["id"] = id
	received := make(chan pluginReply, 1)
	r.pending[id] = received
	r.mu.Unlock()
	if err := r.send(message); err != nil {
		r.fail(err)
	}
	select {
	case reply := <-received:
		if reply.Error != "" {
			return reply, fmt.Errorf("%s", r.scrub(reply.Error))
		}
		return reply, nil
	case <-ctx.Done():
		r.send(Values{"id": id, "method": "cancel"})
		go func() {
			<-received
			if dir, ok := message["dir"].(string); ok {
				os.RemoveAll(dir)
			}
		}()
		return pluginReply{}, ctx.Err()
	}
}

func (r *pluginProcess) scrub(value any) any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clean(value)
}

func (r *pluginProcess) run(ctx context.Context, p *Project, name string, inputs Values, node Node) (Values, error) {
	r.mu.Lock()
	walk(inputs, typeOf(node.Inputs.tree()), nil, func(value any, want Type, _ valuePath) (any, error) {
		if text, ok := value.(string); ok && text != "" && want.Name == "secret" && r.secrets[text] == "" {
			r.secrets[text] = "[secret]"
		}
		return value, nil
	})
	r.clean = secretScrubber(r.secrets)
	r.mu.Unlock()
	work := filepath.Join(p.dir, ".inode", "work")
	if err := os.MkdirAll(work, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(work, "plugin-")
	if err != nil {
		return nil, err
	}
	reply, err := r.call(ctx, Values{"method": "run", "node": name, "inputs": inputs, "dir": dir})
	if err != nil && err == ctx.Err() {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err != nil {
		return nil, err
	}
	if reply.Outputs == nil {
		return nil, fmt.Errorf("run: reply with outputs")
	}
	value, err := walk(r.scrub(reply.Outputs), typeOf(node.Outputs.tree()), valuePath{"outputs"}, func(value any, want Type, path valuePath) (any, error) {
		if name, ok := value.(string); ok && want.Name == "file" {
			rel, err := filepath.Rel(dir, name)
			if err != nil || !filepath.IsLocal(rel) {
				return nil, Problem{Input: path.String(), Message: "return a file inside this run's temporary folder"}
			}
		}
		return value, nil
	})
	if err == nil {
		value, err = p.files(value, typeOf(node.Outputs.tree()), takeFiles, "outputs")
	}
	result, _ := value.(Values)
	return result, err
}
