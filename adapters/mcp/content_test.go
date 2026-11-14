package mcp

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func TestContentFiles(t *testing.T) {
	dir := t.TempDir()
	data := strings.Repeat("binary\x00\xff", 10000)
	result := object{"content": []any{
		object{"type": "text", "text": "{\"is still\":\"text\"}", "_meta": object{"hidden": true}},
		object{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString([]byte(data)), "annotations": object{"audience": []any{"user"}}},
		object{"type": "resource", "resource": object{"uri": "file:///name.bin", "mimeType": "application/octet-stream", "blob": base64.StdEncoding.EncodeToString([]byte("blob"))}},
		object{"type": "resource_link", "uri": "https://example.invalid/not-fetched"},
	}, "structuredContent": object{"_meta": "business data"}}
	outputs, err := content(context.Background(), result, dir)
	if err != nil {
		t.Fatal(err)
	}
	value := outputs["result"].(object)
	files := value["files"].([]string)
	if len(files) != 2 {
		t.Fatalf("files: %v", files)
	}
	for i, want := range []string{data, "blob"} {
		got, err := os.ReadFile(files[i])
		if err != nil || string(got) != want {
			t.Fatalf("file %d: %v", i, err)
		}
	}
	response := value["response"].(object)
	blocks := response["content"].([]any)
	if blocks[0].(object)["text"] != "{\"is still\":\"text\"}" || blocks[0].(object)["_meta"] != nil || blocks[1].(object)["data"] != nil || blocks[1].(object)["file"] != 0 || response["structuredContent"].(object)["_meta"] != "business data" {
		t.Fatalf("normalized response: %#v", response)
	}
}
