package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"inode/adapters"
)

func content(ctx context.Context, result object, dir string) (object, error) {
	blocks, ok := result["content"].([]any)
	if !ok {
		return nil, fmt.Errorf("server must return a content list")
	}
	files := []string{}
	for _, value := range blocks {
		block, ok := value.(object)
		if !ok {
			return nil, fmt.Errorf("server returned an invalid content block")
		}
		delete(block, "_meta")
		binary, field := block, "data"
		switch block["type"] {
		case "text":
			if _, ok := block["text"].(string); !ok {
				return nil, fmt.Errorf("text content needs text")
			}
			continue
		case "resource_link":
			continue
		case "resource":
			binary, ok = block["resource"].(object)
			if !ok {
				return nil, fmt.Errorf("resource content needs a resource")
			}
			delete(binary, "_meta")
			if _, text := binary["text"].(string); text {
				continue
			}
			field = "blob"
		case "image", "audio":
		default:
			return nil, fmt.Errorf("unsupported content type %v; MCP Apps and live content are unavailable", block["type"])
		}
		encoded, ok := binary[field].(string)
		if !ok {
			return nil, fmt.Errorf("binary content needs %s", field)
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("server returned invalid base64 content")
		}
		typ, _ := binary["mimeType"].(string)
		name, _ := binary["name"].(string)
		if uri, _ := binary["uri"].(string); name == "" && uri != "" {
			if parsed, err := url.Parse(uri); err == nil {
				name = filepath.Base(parsed.Path)
			}
		}
		if name == "" || name == "." || name == "/" {
			name = fmt.Sprint(block["type"]) + adapters.Extension(typ)
		}
		path := filepath.Join(dir, fmt.Sprintf("%d-%s", len(files)+1, filepath.Base(name)))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			return nil, err
		}
		delete(binary, field)
		binary["file"] = len(files)
		files = append(files, path)
	}
	response := object{"content": blocks}
	if value, ok := result["structuredContent"]; ok {
		response["structuredContent"] = value
	}
	return object{"result": object{"response": response, "files": files}}, ctx.Err()
}
