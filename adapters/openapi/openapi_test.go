package openapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const secret = "private-token"

func service(t *testing.T) (*httptest.Server, chan struct{}) {
	t.Helper()
	spec, err := os.ReadFile("testdata/petshop.yaml")
	if err != nil {
		t.Fatal(err)
	}
	slow := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/docs/petshop.yaml":
			w.Write(spec)
		case "/v1/photo":
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Disposition", `attachment; filename="../pet.png"`)
			w.Write([]byte("\x89PNG pixels"))
		case "/v1/fail":
			w.WriteHeader(http.StatusTeapot)
			io.WriteString(w, "refused "+r.Header.Get("Authorization")+strings.Repeat("x", 1<<20))
		case "/v1/slow":
			slow <- struct{}{}
			<-r.Context().Done()
			slow <- struct{}{}
		default:
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			json.NewEncoder(w).Encode(object{"method": r.Method, "path": r.URL.Path, "query": r.URL.RawQuery, "authorization": r.Header.Get("Authorization"), "trace": r.Header.Get("X-Trace"), "kind": r.Header.Get("Content-Type"), "body": string(body)})
		}
	}))
	t.Cleanup(server.Close)
	return server, slow
}

// adapter runs Serve as the host would and returns a function that sends one message and reads one reply.
func adapter(t *testing.T, source, base string) func(message object) object {
	t.Helper()
	in, requests, _ := os.Pipe()
	replies, out, _ := os.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, []string{source, base}, in, out, io.Discard) }()
	t.Cleanup(func() {
		requests.Close()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
		cancel()
	})
	reader := bufio.NewReaderSize(replies, 2<<20)
	return func(message object) object {
		t.Helper()
		data, _ := json.Marshal(message)
		requests.Write(append(data, '\n'))
		if message["method"] == "cancel" {
			return nil
		}
		line, err := reader.ReadBytes('\n')
		var reply object
		if err != nil || json.Unmarshal(line, &reply) != nil {
			t.Fatalf("reply: %s, %v", line, err)
		}
		return reply
	}
}

func TestOperations(t *testing.T) {
	server, _ := service(t)
	for name, value := range map[string]string{"token": secret, "key": "private key", "login": "user:pass", "oauth": "unused"} {
		t.Setenv(name, value)
	}
	var document any
	data, _ := os.ReadFile("testdata/petshop.yaml")
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(document)
	asJSON := filepath.Join(t.TempDir(), "petshop.json")
	if err := os.WriteFile(asJSON, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, source := range [][2]string{{"testdata/petshop.yaml", server.URL + "/v1/"}, {asJSON, server.URL + "/v1"}, {server.URL + "/docs/petshop.yaml", ""}} {
		call := adapter(t, source[0], source[1])
		nodes := call(object{"id": "1", "method": "describe"})["nodes"].(object)
		want := map[string]object{
			"pets%2Eget":            {"X-Trace": "text", "id": "number", "verbose": "bool"},
			"put%2Fpets%2F%7Bid%7D": {"id": "number", "body": "object{name: text, parent: any}"},
			"listPets":              {"limit": "number", "tag": object{"type": "list of text", "optional": true}, "a%2Eb": object{"type": "text", "optional": true}, "filter%5Bname%5D": object{"type": "text", "optional": true}},
			"photo":                 {},
			"fail":                  {},
			"slow":                  {},
			"basic":                 {},
		}
		for name, inputs := range want {
			node, _ := nodes[name].(object)
			if !reflect.DeepEqual(node["inputs"], inputs) || node["no_outside_effect"] != (name != "put%2Fpets%2F%7Bid%7D") {
				t.Fatalf("%s %s: %#v", source[0], name, node)
			}
		}
		if len(nodes) != len(want) || nodes["pets%2Eget"].(object)["doc"] != "Fetch one pet.\nGET /pets/{id}" || nodes["listPets"].(object)["doc"] != "GET /pets\nlimit: Maximum pets to return. (one of [0 2])" {
			t.Fatalf("%s catalog: %#v", source[0], nodes)
		}
		echo := func(node string, inputs object) object {
			t.Helper()
			reply := call(object{"id": "2", "method": "run", "node": node, "inputs": inputs, "dir": t.TempDir()})
			outputs, _ := reply["outputs"].(object)
			result, _ := outputs["result"].(object)
			body, _ := result["body"].(object)
			if result["status"] != float64(200) || body == nil || len(result["files"].([]any)) != 0 {
				t.Fatalf("%s: %#v", node, reply)
			}
			return body
		}
		if got := echo("pets%2Eget", object{"id": 7, "X-Trace": "a b", "verbose": true}); got["method"] != "GET" || got["path"] != "/v1/pets/7" || got["query"] != "verbose=true" || got["trace"] != "a b" || got["authorization"] != "Bearer "+secret {
			t.Fatalf("path, query, header and bearer: %#v", got)
		}
		if got := echo("put%2Fpets%2F%7Bid%7D", object{"id": "a/b", "body": object{"name": "Rex"}}); got["method"] != "PUT" || got["path"] != "/v1/pets/a/b" || got["query"] != "api_key=private+key" || got["body"] != `{"name":"Rex"}` || got["kind"] != "application/json" || got["authorization"] != "" {
			t.Fatalf("body and query key: %#v", got)
		}
		if got := echo("listPets", object{"limit": 2, "tag": []any{"a", "b"}}); got["query"] != "limit=2&tag=a&tag=b" || got["authorization"] != "" {
			t.Fatalf("named inputs without security: %#v", got)
		}
		if got := echo("listPets", object{"limit": 0}); got["query"] != "limit=0" {
			t.Fatalf("unset optional inputs sent: %#v", got)
		}
		if got := echo("listPets", object{"limit": 2, "a%2Eb": "path", "filter%5Bname%5D": "Rex"}); got["query"] != "a.b=path&filter%5Bname%5D=Rex&limit=2" {
			t.Fatalf("original parameter names: %#v", got)
		}
		if got := echo("basic", object{}); got["authorization"] != "Basic dXNlcjpwYXNz" {
			t.Fatalf("basic: %#v", got)
		}
		if reply := call(object{"id": "3", "method": "run", "node": "listPets", "inputs": object{}}); !strings.Contains(reply["error"].(string), "supply input limit") {
			t.Fatalf("required argument: %#v", reply)
		}
	}
}

func TestFilesFailuresAndCancellation(t *testing.T) {
	server, slow := service(t)
	t.Setenv("token", secret)
	call := adapter(t, "testdata/petshop.yaml", server.URL+"/v1")
	dir := t.TempDir()
	reply := call(object{"id": "1", "method": "run", "node": "photo", "dir": dir})
	result := reply["outputs"].(object)["result"].(object)
	if data, err := os.ReadFile(filepath.Join(dir, "pet.png")); err != nil || string(data) != "\x89PNG pixels" || result["body"] != nil || !reflect.DeepEqual(result["files"], []any{filepath.Join(dir, "pet.png")}) {
		t.Fatalf("binary response must be a file inside the run's folder: %#v, %v", reply, err)
	}
	failure, _ := call(object{"id": "2", "method": "run", "node": "fail", "dir": dir})["error"].(string)
	if !strings.Contains(failure, ".fail: status 418 I'm a teapot: refused Bearer "+secret) || len(failure) > 4200 {
		t.Fatalf("failure needs the status and a bounded body: %d %.200s", len(failure), failure)
	}
	if reply := call(object{"id": "3", "method": "run", "node": "missing"}); !strings.Contains(reply["error"].(string), "unknown operation") {
		t.Fatalf("unknown: %#v", reply)
	}
	requests := make(chan object)
	go func() { requests <- call(object{"id": "4", "method": "run", "node": "slow", "dir": dir}) }()
	<-slow
	call(object{"id": "4", "method": "cancel"})
	if reply := <-requests; !strings.Contains(reply["error"].(string), "context canceled") {
		t.Fatalf("cancel: %#v", reply)
	}
	<-slow
}

func TestInspect(t *testing.T) {
	server, _ := service(t)
	name, base, variables, err := Inspect("testdata/petshop.yaml", "")
	if err != nil || name != "petshop" || base != "/v1" || len(variables) != 3 {
		t.Fatalf("file: %s %s %v, %v", name, base, variables, err)
	}
	name, base, variables, err = Inspect(server.URL+"/docs/petshop.yaml", "")
	if err != nil || name != "127.0.0.1" || base != server.URL+"/v1" || !reflect.DeepEqual(variables, object{"key": "", "login": "", "token": ""}) {
		t.Fatalf("URL: %s %s %v, %v", name, base, variables, err)
	}
	other := filepath.Join(t.TempDir(), "notes.yaml")
	os.WriteFile(other, []byte("swagger: \"2.0\"\npaths: {}\n"), 0600)
	for _, source := range []string{other, "testdata", "testdata/missing.yaml", server.URL + "/v1/fail", "openapi_test.go"} {
		if _, _, _, err := Inspect(source, ""); err == nil || !strings.Contains(err.Error(), "is not a readable OpenAPI 3 document") {
			t.Fatalf("%s: %v", source, err)
		}
	}
	if err := Serve(context.Background(), []string{"testdata/petshop.yaml", ""}, os.Stdin, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), `base URL "/v1" is not an http or https address`) {
		t.Fatalf("relative base: %v", err)
	}
}
