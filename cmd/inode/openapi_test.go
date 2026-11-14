package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"inode"
)

func TestOpenAPIPluginAndSourceKinds(t *testing.T) {
	spec, err := filepath.Abs("../../adapters/openapi/testdata/petshop.yaml")
	if err != nil {
		t.Fatal(err)
	}
	document, err := os.ReadFile(spec)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/docs/petshop.yaml":
			w.Write(document)
		case "/v1/photo":
			w.Header().Set("Content-Type", "image/png")
			io.WriteString(w, "pixels")
		case "/v1/fail":
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "refused "+r.Header.Get("Authorization"))
		default:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path, "query": r.URL.RawQuery, "authorization": r.Header.Get("Authorization")})
		}
	}))
	defer server.Close()
	cliProject(t)
	seen := ""
	call := func(input string, args ...string) (string, string, error) {
		t.Helper()
		cmd := exec.Command(cliBinary, args...)
		cmd.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
		cmd.Stdin = strings.NewReader(input)
		var out, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostic
		err := cmd.Run()
		seen += out.String() + diagnostic.String()
		return out.String(), diagnostic.String(), err
	}
	inspect := func(source string) (inode.Plugin, error) {
		t.Helper()
		text, diag, err := call("", "plugins", "inspect", source)
		var plugin inode.Plugin
		if err == nil {
			err = json.Unmarshal([]byte(text), &plugin)
		}
		if err != nil {
			err = fmt.Errorf("%s: %w", diag, err)
		}
		return plugin, err
	}
	if err := os.Mkdir("folder", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("folder/plugin", []byte("name: folder\nversion: \"1\"\ndoc: A folder.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if plugin, err := inspect("folder"); err != nil || plugin.Preparation != nil || plugin.Name != "folder" {
		t.Fatalf("folder: %+v, %v", plugin, err)
	}
	for _, source := range []string{"file://" + filepath.Join(t.TempDir(), "absent"), server.URL + "/v1/not-a-document"} {
		if plugin, err := inspect(source); err == nil || !strings.Contains(err.Error(), `program "git"`) {
			t.Fatalf("an address that holds no OpenAPI document is a git URL: %+v, %v", plugin, err)
		}
	}
	plugin, err := inspect("runner -y @tools/server-filesystem ~/docs")
	if err != nil || plugin.Preparation.Input["kind"] != "command" || plugin.Name != "filesystem" || plugin.Process.([]any)[6] != filepath.Join(os.Getenv("HOME"), "docs") {
		t.Fatalf("command: %+v, %v", plugin, err)
	}
	if plugin, err := inspect("petshop"); err != nil || plugin.Name != "" || len(plugin.Preparation.Editable[0].Choices) != 3 {
		t.Fatalf("a lone word that is no folder, document or address must ask: %+v, %v", plugin, err)
	}
	for _, source := range []string{spec, server.URL + "/docs/petshop.yaml"} {
		plugin, err := inspect(source)
		if err != nil || plugin.Preparation.Input["kind"] != "openapi" || len(plugin.Preparation.Editable) != 3 || plugin.Preparation.Editable[1].Name != "base" || len(plugin.Preparation.Editable[2].Value.(map[string]any)) != 3 || plugin.Env != nil {
			t.Fatalf("document: %+v, %v", plugin, err)
		}
		if base := plugin.Preparation.Editable[1].Value; source != spec && base != server.URL+"/v1" {
			t.Fatalf("a relative server is resolved against the document's URL: %v", base)
		}
	}
	if _, diag, err := call("private-shop-token\n", "secret", "set", "shop"); err != nil {
		t.Fatalf("secret: %s %v", diag, err)
	}
	text, diag, err := call("shop\n"+server.URL+"/v1\n\n\nshop\n\ny\n", "add", spec)
	if err != nil || !strings.Contains(text, "Program:") || !strings.Contains(text, "token from secret shop") || !strings.Contains(text, "- Calls "+server.URL+"/v1 over the network") || strings.Contains(text, "key from") {
		t.Fatalf("install: %s %s, %v", text, diag, err)
	}
	if data, err := os.ReadFile("shop/plugin"); err != nil || !json.Valid(data) || !strings.Contains(string(data), `"openapi"`) {
		t.Fatalf("ordinary source manifest: %s, %v", data, err)
	}
	text, diag, err = call("", "run", "shop.pets%2Eget", "id=7", "X-Trace=t", "verbose=true")
	if err != nil || !strings.Contains(text, `"path": "/v1/pets/7"`) || !strings.Contains(text, `"query": "verbose=true"`) || !strings.Contains(text, "Bearer [secret shop]") {
		t.Fatalf("run: %s %s, %v", text, diag, err)
	}
	text, diag, err = call("", "show", "shop.listPets")
	var node inode.Node
	if err != nil || json.Unmarshal([]byte(text), &node) != nil || len(node.Inputs) != 4 || !node.Inputs["tag"].Optional || !node.Inputs["a%2Eb"].Optional || node.Inputs["limit"].Optional {
		t.Fatalf("named optional ports in show: %s %s, %v", text, diag, err)
	}
	text, diag, err = call("", "run", "shop.listPets", "limit=0")
	if err != nil || !strings.Contains(text, `"query": "limit=0"`) {
		t.Fatalf("optional inputs omitted by CLI: %s %s, %v", text, diag, err)
	}
	text, diag, err = call("", "run", "shop.photo")
	if err != nil || !strings.Contains(text, `"$file"`) || !strings.Contains(text, `"name": "body.png"`) {
		t.Fatalf("file: %s %s, %v", text, diag, err)
	}
	text, diag, err = call("", "run", "shop.fail")
	if err == nil || !strings.Contains(diag, "shop.fail: status 403 Forbidden: refused Bearer [secret shop]") {
		t.Fatalf("failure: %s %s, %v", text, diag, err)
	}
	if strings.Contains(seen, "private-shop-token") {
		t.Fatal("bound secret appeared in output")
	}
	if paths, _ := filepath.Glob(".inode/work/plugin-*"); len(paths) != 0 {
		t.Fatalf("run folders left behind: %v", paths)
	}
}
