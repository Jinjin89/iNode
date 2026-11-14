package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type adapterTest struct {
	t       *testing.T
	in      *os.File
	replies chan object
	done    chan struct{}
	err     error
	dir     string
}

func startAdapter(t *testing.T, version string) *adapterTest {
	t.Helper()
	server, err := filepath.Abs("testdata/server.py")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "demo")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	in, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	reader, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, err := os.Create(filepath.Join(dir, "diagnostic"))
	if err != nil {
		t.Fatal(err)
	}
	h := &adapterTest{t: t, in: writer, replies: make(chan object, 10), done: make(chan struct{}), dir: dir}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		h.err = Serve(ctx, []string{"python3", server, version}, in, out, diagnostic)
		out.Close()
		close(h.done)
	}()
	go func() {
		defer close(h.replies)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			var reply object
			if json.Unmarshal(scanner.Bytes(), &reply) == nil {
				h.replies <- reply
			}
		}
	}()
	t.Cleanup(func() {
		writer.Close()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			cancel()
			t.Error("adapter did not stop")
		}
		cancel()
		reader.Close()
		in.Close()
		diagnostic.Close()
	})
	return h
}

func (h *adapterTest) send(value object) {
	h.t.Helper()
	if err := json.NewEncoder(h.in).Encode(value); err != nil {
		h.t.Fatal(err)
	}
}

func (h *adapterTest) receive() object {
	h.t.Helper()
	select {
	case reply, ok := <-h.replies:
		if !ok {
			<-h.done
			h.t.Fatalf("adapter ended: %v", h.err)
		}
		return reply
	case <-time.After(6 * time.Second):
		h.t.Fatal("adapter reply timed out")
	}
	return nil
}

func (h *adapterTest) run(id, node string, inputs object) object {
	h.t.Helper()
	dir := filepath.Join(h.dir, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		h.t.Fatal(err)
	}
	h.send(object{"id": id, "method": "run", "node": node, "inputs": inputs, "dir": dir})
	return h.receive()
}

func (h *adapterTest) trace() string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.dir, "trace"))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(data)
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("file did not appear: %s", path)
}

func TestAdapterVersionsAndCalls(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	t.Setenv("FOO", "cli")
	t.Setenv("BAR", "1")
	for _, version := range []string{"modern", "legacy", "2025-06-18", "2025-03-26", "2024-11-05", "delayed", "negotiate"} {
		t.Run(version, func(t *testing.T) {
			t.Setenv("API_KEY", "private-launch-token")
			started := time.Now()
			h := startAdapter(t, version)
			h.send(object{"id": "describe", "method": "describe"})
			nodes := h.receive()["nodes"].(object)
			if version == "delayed" {
				if time.Since(started) < 3*time.Second {
					t.Fatal("fake server did not delay startup")
				}
				t.Logf("modern catalog ready after %s", time.Since(started).Round(time.Millisecond))
			}
			if len(nodes) != 20 {
				t.Fatalf("paginated nodes: %d", len(nodes))
			}
			optional := nodes["optional"].(object)["inputs"].(object)
			if nodes["echo"].(object)["inputs"].(object)["text"] != "text" || len(optional) != 3 || optional["x"].(object)["optional"] != true || optional["a%2Eb"].(object)["type"] != "text" || optional["filter%5Bname%5D"].(object)["optional"] != true {
				t.Fatalf("inputs: %#v", nodes)
			}
			if nodes["echo"].(object)["no_outside_effect"] == true || !strings.Contains(nodes["enum"].(object)["doc"].(string), "ready") {
				t.Fatal("annotations or enum description")
			}
			if nodes["echo"].(object)["doc"] != "Test echo" || !strings.Contains(nodes["media%2Erender"].(object)["doc"].(string), "\nMCP tool: media.render") || strings.Contains(nodes["enum"].(object)["doc"].(string), "Input schema:") {
				t.Fatal("only escaped tool names need a name note; schemas must not be dumped")
			}
			for _, call := range []struct {
				id, node string
				inputs   object
			}{
				{"one", "echo", object{"text": "literal JSON {\"x\":1}"}},
				{"image", "media%2Erender", object{"text": "large"}},
				{"odd", "odd%2E%3D%E7%8C%AB%25", object{}},
				{"optional", "optional", object{}},
				{"optional-filled", "optional", object{"x": "", "a%2Eb": "path", "filter%5Bname%5D": 0}},
				{"primitive", "primitive", object{}},
				{"primitive-false", "primitive", object{}},
				{"primitive-zero", "primitive", object{}},
			} {
				reply := h.run(call.id, call.node, call.inputs)
				if reply["error"] != nil || reply["id"] != call.id {
					t.Fatalf("%s: %#v", call.node, reply)
				}
				result := reply["outputs"].(object)["result"].(object)
				response := result["response"].(object)
				if call.id == "optional" && response["content"].([]any)[0].(object)["text"] != "{}" {
					t.Fatalf("unset optional inputs sent: %#v", response)
				}
				if call.id == "optional-filled" && response["content"].([]any)[0].(object)["text"] != `{"a.b": "path", "filter[name]": 0, "x": ""}` {
					var args object
					json.Unmarshal([]byte(response["content"].([]any)[0].(object)["text"].(string)), &args)
					if args["x"] != "" || args["a.b"] != "path" || args["filter[name]"] != float64(0) || len(args) != 3 {
						t.Fatalf("original parameter names/zero values: %#v", response)
					}
				}
				if call.id == "one" && response["content"].([]any)[0].(object)["text"] != call.inputs["text"] {
					t.Fatal("text was parsed or changed")
				}
				if call.id == "image" {
					files := result["files"].([]any)
					data, err := os.ReadFile(files[0].(string))
					if err != nil || len(data) != 200000 {
						t.Fatalf("binary: %d, %v", len(data), err)
					}
					block := response["content"].([]any)[1].(object)
					if block["file"] != float64(0) || block["data"] != nil || block["_meta"] != nil || response["_meta"] != nil {
						t.Fatalf("media metadata: %#v", response)
					}
				}
				if call.id == "primitive-false" && response["structuredContent"] != false {
					t.Fatal("lost false structured content")
				}
				if call.id == "primitive-zero" && response["structuredContent"] != float64(0) {
					t.Fatal("lost zero structured content")
				}
				if call.id == "primitive" {
					if value, ok := response["structuredContent"]; !ok || value != nil {
						t.Fatal("lost null structured content")
					}
				}
			}
			for _, call := range []struct {
				node, want string
				inputs     object
			}{
				{"error", "Tool failed", object{}}, {"protocol", "arguments.text", object{}}, {"interactive", "mid-call input", object{}}, {"missing", "omitted required structuredContent", object{}}, {"task-only", "task-only", object{}}, {"app-only", "MCP Apps", object{}}, {"enum", "state must be ready", object{"state": "wrong"}},
			} {
				reply := h.run("fail-"+call.node, call.node, call.inputs)
				text, _ := reply["error"].(string)
				if !strings.Contains(text, "demo."+call.node) || !strings.Contains(text, call.want) || reply["outputs"] != nil {
					t.Fatalf("failure: %#v", reply)
				}
				if (call.node == "error" || call.node == "protocol") && !strings.Contains(text, "private-launch-token") {
					t.Fatalf("adapter changed server text; only the host knows bound secrets: %s", text)
				}
			}
			h.send(object{"id": "first", "method": "run", "node": "reverse", "inputs": object{"text": "first"}, "dir": h.dir})
			for deadline := time.Now().Add(3 * time.Second); !strings.Contains(h.trace(), `"text": "first"`); time.Sleep(5 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatal("first reverse call did not arrive")
				}
			}
			h.send(object{"id": "second", "method": "run", "node": "reverse", "inputs": object{"text": "second"}, "dir": h.dir})
			expected := map[any]bool{"first": true, "second": true}
			for range 2 {
				reply := h.receive()
				if !expected[reply["id"]] || reply["outputs"].(object)["result"].(object)["response"].(object)["content"].([]any)[0].(object)["text"] != reply["id"] {
					t.Fatalf("out-of-order correlation: %#v", reply)
				}
				delete(expected, reply["id"])
			}
			order, _ := os.ReadFile(filepath.Join(h.dir, "order"))
			if string(order) != "second,first" {
				t.Fatalf("server reply order: %s", order)
			}
			if version == "legacy" || strings.HasPrefix(version, "202") {
				if reply := h.run("callback", "callback", object{}); !strings.Contains(reply["error"].(string), "Client declined sampling") {
					t.Fatalf("legacy sampling: %#v", reply)
				}
			}
			h.send(object{"id": "slow", "method": "run", "node": "slow", "inputs": object{}, "dir": h.dir})
			waitFile(t, filepath.Join(h.dir, "waiting"))
			h.send(object{"id": "slow", "method": "cancel"})
			if reply := h.receive(); reply["id"] != "slow" || !strings.Contains(reply["error"].(string), "canceled") {
				t.Fatalf("cancel: %#v", reply)
			}
			waitFile(t, filepath.Join(h.dir, "cancelled"))
			time.Sleep(80 * time.Millisecond)
			if reply := h.run("after-cancel", "echo", object{"text": "alive"}); reply["error"] != nil {
				t.Fatal(reply)
			}
			trace := h.trace()
			if !strings.Contains(trace, `"cursor": "second"`) || !strings.Contains(trace, `"name": "odd.=\u732b%"`) {
				t.Fatalf("wire arguments: %s", trace)
			}
			if version == "legacy" || strings.HasPrefix(version, "202") {
				if !strings.Contains(trace, `"method": "initialize"`) || !strings.Contains(trace, `"id": 999`) {
					t.Fatal("legacy handshake/ping missing")
				}
			} else if strings.Contains(trace, `"method": "initialize"`) {
				t.Fatal("modern server received initialization")
			}
		})
	}
}

func TestAdapterCrashAndUnsupportedVersion(t *testing.T) {
	for _, mode := range []string{"crash", "garbage", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			h := startAdapter(t, map[bool]string{true: "unsupported", false: "modern"}[mode == "unsupported"])
			h.send(object{"id": "describe", "method": "describe"})
			if mode != "unsupported" {
				h.receive()
				h.send(object{"id": "bad", "method": "run", "node": mode, "inputs": object{}, "dir": h.dir})
			}
			select {
			case <-h.done:
			case <-time.After(5 * time.Second):
				t.Fatal("server failure did not end adapter")
			}
			if h.err == nil {
				t.Fatal("server failure was hidden")
			}
			if mode == "unsupported" && strings.Contains(h.trace(), `"method": "initialize"`) {
				t.Fatal("recognized modern error fell back to legacy")
			}
			if mode == "garbage" && !strings.Contains(h.err.Error(), "unreadable MCP stdout") {
				t.Fatal(h.err)
			}
		})
	}
}
