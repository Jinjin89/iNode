package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"inode/adapters"
)

type object = adapters.Object

const modern, legacy, clientVersion = "2026-07-28", "2025-11-25", "dev"

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e.Data == nil {
		return e.Message
	}
	data, _ := json.Marshal(e.Data)
	return e.Message + ": " + string(data)
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  object          `json:"params,omitempty"`
	Result  object          `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type peer struct {
	mu, writer sync.Mutex
	stdin      *os.File
	cmd        *exec.Cmd
	done       chan struct{}
	err        error
	pending    map[string]chan message
	sequence   uint64
	version    string
	buffer     []byte
	readErr    error
}

func (p *peer) send(m message) error {
	p.writer.Lock()
	defer p.writer.Unlock()
	p.stdin.SetWriteDeadline(time.Now().Add(time.Second))
	if err := json.NewEncoder(p.stdin).Encode(m); err != nil {
		p.cmd.Process.Kill()
		return err
	}
	return nil
}

func (p *peer) Write(data []byte) (int, error) {
	p.buffer = append(p.buffer, data...)
	for {
		end := bytes.IndexByte(p.buffer, '\n')
		if end < 0 && len(p.buffer) <= 8<<20 {
			return len(data), nil
		}
		var m message
		if end < 0 || end > 8<<20 || !utf8.Valid(p.buffer[:end]) || json.Unmarshal(p.buffer[:end], &m) != nil || m.JSONRPC != "2.0" || m.Method == "" && (len(m.ID) == 0 || (m.Result == nil) == (m.Error == nil)) {
			p.readErr = fmt.Errorf("unreadable MCP stdout; use JSON-RPC lines up to 8 MiB")
			p.cmd.Process.Kill()
			return 0, p.readErr
		}
		p.buffer = p.buffer[end+1:]
		if m.Method != "" {
			if len(m.ID) != 0 {
				reply := message{JSONRPC: "2.0", ID: m.ID, Result: object{"_meta": object{}}}
				if m.Method != "ping" {
					reply.Result = nil
					reply.Error = &rpcError{Code: -32601, Message: "Client requests for sampling, roots, elicitation and other interactive features are unsupported"}
				}
				if err := p.send(reply); err != nil {
					return 0, err
				}
			}
			continue
		}
		p.mu.Lock()
		if reply := p.pending[string(m.ID)]; reply != nil {
			reply <- m
			delete(p.pending, string(m.ID))
		}
		p.mu.Unlock()
	}
}

func (p *peer) call(ctx context.Context, method string, params object) (object, error) {
	p.mu.Lock()
	p.sequence++
	id, _ := json.Marshal(fmt.Sprint(p.sequence))
	reply := make(chan message, 1)
	p.pending[string(id)] = reply
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, string(id)); p.mu.Unlock() }()
	if p.version == modern {
		params["_meta"] = object{"io.modelcontextprotocol/protocolVersion": modern, "io.modelcontextprotocol/clientCapabilities": object{}, "io.modelcontextprotocol/clientInfo": object{"name": "inode", "version": clientVersion}}
	}
	if err := p.send(message{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	select {
	case m := <-reply:
		if m.Error != nil {
			return nil, m.Error
		}
		if kind := m.Result["resultType"]; kind != nil && kind != "complete" {
			return nil, fmt.Errorf("unsupported MCP result %v; mid-call input, tasks and interactive results are unavailable", kind)
		}
		return m.Result, nil
	case <-ctx.Done():
		if method != "server/discover" {
			p.send(message{JSONRPC: "2.0", Method: "notifications/cancelled", Params: object{"requestId": json.RawMessage(id), "reason": "run cancelled"}})
		}
		return nil, ctx.Err()
	case <-p.done:
		return nil, p.err
	}
}

func (p *peer) catalog(ctx context.Context) (map[string]object, map[string]object, error) {
	info, err := p.call(ctx, "server/discover", object{})
	var remote *rpcError
	if errors.As(err, &remote) && remote.Code >= -32022 && remote.Code <= -32020 {
		details, _ := remote.Data.(object)
		supported, _ := details["supported"].([]any)
		if remote.Code != -32022 || !slices.Contains(supported, any(modern)) {
			return nil, nil, fmt.Errorf("%s; server supports %v; this adapter supports %s and earlier revisions for tools", remote.Message, supported, modern)
		}
		info, err = p.call(ctx, "server/discover", object{})
		if err != nil {
			return nil, nil, err
		}
	}
	if err != nil {
		p.version = legacy
		info, err = p.call(ctx, "initialize", object{"protocolVersion": legacy, "capabilities": object{}, "clientInfo": object{"name": "inode", "version": clientVersion}})
		if err == nil {
			err = p.send(message{JSONRPC: "2.0", Method: "notifications/initialized"})
		}
	} else {
		versions, _ := info["supportedVersions"].([]any)
		if !slices.Contains(versions, any(modern)) {
			err = fmt.Errorf("unsupported MCP versions %v in server/discover; expected %s", versions, modern)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	capabilities, _ := info["capabilities"].(object)
	if capabilities["tools"] == nil {
		return nil, nil, fmt.Errorf("the server does not offer tools; resources, prompts and completion are not exposed")
	}
	nodes, tools := map[string]object{}, map[string]object{}
	cursor, seen := "", map[string]bool{}
	for {
		params := object{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		page, err := p.call(ctx, "tools/list", params)
		if err != nil {
			return nil, nil, err
		}
		entries, ok := page["tools"].([]any)
		if !ok {
			return nil, nil, fmt.Errorf("tools/list must return a tools list")
		}
		for _, value := range entries {
			tool, ok := value.(object)
			if !ok {
				return nil, nil, fmt.Errorf("invalid tool description")
			}
			name, _ := tool["name"].(string)
			key := adapters.Escape(name)
			if name == "" || tools[key] != nil || len(tools) >= 10000 {
				return nil, nil, fmt.Errorf("tools need distinct nonempty names; at most 10000 tools")
			}
			schema, ok := tool["inputSchema"].(object)
			if !ok {
				return nil, nil, fmt.Errorf("tool %s needs an inputSchema object", name)
			}
			inputs, named := adapters.Inputs(schema)
			response := "object"
			output, structured := tool["outputSchema"]
			if structured {
				response = "object{content: list of object, structuredContent: " + adapters.Project(output) + "}"
			}
			doc, _ := tool["description"].(string)
			if unsupported(tool) {
				doc += "\nUnavailable: task-only tools and MCP Apps are unsupported."
			}
			raw, _ := json.Marshal(schema)
			nodes[key] = object{"doc": doc + "\nMCP tool: " + name + "\nInput schema: " + string(raw), "inputs": inputs, "outputs": object{"result": "object{response: " + response + ", files: list of file}"}}
			tools[key] = object{"name": name, "named": named, "structured": structured, "unsupported": unsupported(tool)}
		}
		catalogData, _ := json.Marshal(nodes)
		if len(catalogData) > 1<<20 {
			return nil, nil, fmt.Errorf("tool catalog exceeds the host's 1 MiB reply limit")
		}
		next, ok := page["nextCursor"]
		if !ok || next == nil || next == "" {
			break
		}
		cursor, ok = next.(string)
		if !ok || seen[cursor] {
			return nil, nil, fmt.Errorf("invalid or repeated tools/list cursor")
		}
		seen[cursor] = true
	}
	return nodes, tools, nil
}

func unsupported(tool object) bool {
	execution, _ := tool["execution"].(object)
	meta, _ := tool["_meta"].(object)
	return execution["taskSupport"] == "required" || meta["ui"] != nil || meta["io.modelcontextprotocol/ui"] != nil
}

func (p *peer) run(ctx context.Context, tool object, inputs object, dir string) (object, error) {
	if tool["unsupported"] == true {
		return nil, fmt.Errorf("task-only tools and MCP Apps are unsupported")
	}
	args, err := adapters.Arguments(inputs, tool["named"] == true)
	if err != nil {
		return nil, err
	}
	result, err := p.call(ctx, "tools/call", object{"name": tool["name"], "arguments": args})
	if err != nil {
		return nil, err
	}
	if result["isError"] == true {
		data, _ := json.Marshal(result["content"])
		return nil, fmt.Errorf("%s", data)
	}
	if tool["structured"] == true {
		if _, present := result["structuredContent"]; !present {
			return nil, fmt.Errorf("server omitted required structuredContent")
		}
	}
	return content(ctx, result, dir)
}

func Serve(ctx context.Context, args []string, in *os.File, out, diagnostic io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("supply a local stdio server command after --")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer in.Close()
	p := &peer{pending: map[string]chan message{}, done: make(chan struct{}), version: modern}
	p.cmd = exec.CommandContext(ctx, args[0], args[1:]...)
	p.cmd.Stdout, p.cmd.Stderr, p.cmd.WaitDelay = p, diagnostic, 250*time.Millisecond
	read, write, err := os.Pipe()
	if err != nil {
		return err
	}
	p.stdin = write
	p.cmd.Stdin = read
	err = p.cmd.Start()
	read.Close()
	if err != nil {
		write.Close()
		return err
	}
	go func() {
		err := p.cmd.Wait()
		if p.readErr != nil {
			err = p.readErr
		}
		if err == nil {
			err = fmt.Errorf("server exited")
		}
		p.err = fmt.Errorf("server stopped (%v); runs may or may not have finished", err)
		close(p.done)
		in.Close()
	}()
	defer func() {
		write.Close()
		select {
		case <-p.done:
		case <-time.After(200 * time.Millisecond):
			p.cmd.Process.Kill()
			<-p.done
		}
		cancel()
	}()
	nodes, tools, err := p.catalog(ctx)
	if err != nil {
		return err
	}
	err = adapters.Serve(ctx, in, out, p.done, nodes, func(ctx context.Context, node string, inputs object, dir string) (object, error) {
		tool := tools[node]
		if tool == nil {
			return nil, fmt.Errorf("unknown tool")
		}
		return p.run(ctx, tool, inputs, dir)
	})
	select {
	case <-p.done:
		return p.err
	default:
		return err
	}
}
