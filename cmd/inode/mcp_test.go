package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"inode"
)

func TestCommandPluginConfirmation(t *testing.T) {
	server, err := filepath.Abs("../../adapters/mcp/testdata/server.py")
	if err != nil {
		t.Fatal(err)
	}
	cliProject(t)
	call := func(args []string, input string) (string, string, error) {
		t.Helper()
		cmd := exec.Command(cliBinary, args...)
		cmd.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
		cmd.Stdin = strings.NewReader(input)
		var out, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostic
		err := cmd.Run()
		return out.String(), diagnostic.String(), err
	}
	source := inode.CommandText([]any{"python3", server, "modern"})
	text, diag, err := call([]string{"plugins", "inspect", source}, "")
	if err != nil {
		t.Fatalf("inspect: %s %s, %v", text, diag, err)
	}
	var inspected inode.Plugin
	if err := json.Unmarshal([]byte(text), &inspected); err != nil {
		t.Fatal(err)
	}
	if inspected.Preparation == nil || len(inspected.Preparation.Editable) != 2 || inspected.Preparation.Editable[1].Kind != "bindings" {
		t.Fatalf("generic editable fields: %+v", inspected)
	}
	if _, err := os.Stat("python3"); !os.IsNotExist(err) {
		t.Fatal("inspection wrote a source folder")
	}
	if _, _, err := call([]string{"add", source}, "declined\n\nn\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("declined"); !os.IsNotExist(err) {
		t.Fatal("declining wrote a source folder")
	}
	if _, err := os.Stat("plugins"); !os.IsNotExist(err) {
		t.Fatal("installed before confirmation")
	}
	text, diag, err = call([]string{"add", "python3"}, "command\nbare\n\nn\n")
	if err != nil || !strings.Contains(diag, "MCP server command") || strings.Count(diag, "Plugin name") != 1 || strings.Count(diag, "Source type") != 1 {
		t.Fatalf("metadata choices and fields must each be asked once: %s %s %v", text, diag, err)
	}
	if _, err := os.Stat("bare"); !os.IsNotExist(err) {
		t.Fatal("declining a source choice installed a plugin")
	}
	if _, diag, err := call([]string{"secret", "set", "launch"}, "private-adapter-key\n"); err != nil {
		t.Fatalf("secret: %s %v", diag, err)
	}
	text, diag, err = call([]string{"add", source}, "edited\nAPI_KEY\nlaunch\n\ny\n")
	if err != nil || !strings.Contains(text, "Program:") || !strings.Contains(text, "Working folder:") || !strings.Contains(text, "API_KEY from secret launch") {
		t.Fatalf("install: %s %s, %v", text, diag, err)
	}
	if strings.Contains(text+diag, "private-adapter-key") {
		t.Fatal("launch secret appeared in confirmation/log")
	}
	data, err := os.ReadFile("edited/plugin")
	if err != nil || !json.Valid(data) {
		t.Fatalf("ordinary source manifest: %s, %v", data, err)
	}
	for _, name := range []string{"edited.echo", "edited.media%2Erender"} {
		text, diag, err = call([]string{"run", name, "text=large"}, "")
		if err != nil {
			t.Fatalf("run: %s %s, %v", text, diag, err)
		}
		var outputs inode.Values
		if err := json.Unmarshal([]byte(text), &outputs); err != nil {
			t.Fatal(err)
		}
		result := outputs["result"].(map[string]any)
		if name == "edited.media%2Erender" {
			file := result["files"].([]any)[0].(map[string]any)
			if file["$file"] == nil || file["size"] != float64(200000) || file["name"] != "1-image.png" {
				t.Fatalf("stored media: %#v", file)
			}
		}
	}
	text, diag, err = call([]string{"run", "edited.protocol"}, "")
	if err == nil || strings.Contains(text+diag, "private-adapter-key") || !strings.Contains(diag, "[secret launch]") || !strings.Contains(diag, "edited.protocol") {
		t.Fatalf("host must scrub bound secret from protocol failure: %s %s %v", text, diag, err)
	}
	if paths, _ := filepath.Glob(".inode/work/plugin-*"); len(paths) != 0 {
		t.Fatalf("run folders left behind: %v", paths)
	}
	trace, err := os.ReadFile(".inode/plugins/edited/trace")
	if err != nil || !bytes.Contains(trace, []byte("tools/call")) {
		t.Fatalf("server was not reached: %v", err)
	}
	entries, _ := filepath.Glob("/proc/[0-9]*/cwd")
	installed, _ := filepath.Abs(".inode/plugins/edited")
	pid := 0
	for _, entry := range entries {
		dir, _ := os.Readlink(entry)
		args, _ := os.ReadFile(filepath.Join(filepath.Dir(entry), "cmdline"))
		if dir == installed && bytes.HasPrefix(args, []byte("python3\x00")) {
			fmtPID := filepath.Base(filepath.Dir(entry))
			json.Unmarshal([]byte(fmtPID), &pid)
			break
		}
	}
	if pid == 0 {
		t.Fatal("server PID not found through /proc")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	client, _, err := inode.Connect(context.Background(), ".", false)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var rows []inode.Plugin
		err = client.Call(context.Background(), "plugins", nil, func(data json.RawMessage) error { return json.Unmarshal(data, &rows) })
		if err != nil {
			t.Fatal(err)
		}
		if rows[0].State == "unavailable" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server crash did not mark plugin unavailable")
		}
	}
	if text, diag, err = call([]string{"add", "edited"}, ""); err != nil {
		t.Fatalf("add again: %s %s, %v", text, diag, err)
	}
	if text, diag, err = call([]string{"run", "edited.echo", "text=recovered"}, ""); err != nil || !strings.Contains(text, "recovered") {
		t.Fatalf("recovery: %s %s, %v", text, diag, err)
	}
	delayed := inode.CommandText([]any{"python3", server, "delayed"})
	text, diag, err = call([]string{"add", delayed}, "delayed\n\ny\n")
	if err != nil || !strings.Contains(text, "Starting program…") {
		t.Fatalf("delayed startup: %s %s %v", text, diag, err)
	}
	if text, diag, err = call([]string{"run", "delayed.echo", "text=ready"}, ""); err != nil || !strings.Contains(text, "ready") {
		t.Fatalf("delayed modern tool: %s %s %v", text, diag, err)
	}
}
