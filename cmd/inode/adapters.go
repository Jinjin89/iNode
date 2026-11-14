package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"inode"
	"inode/adapters/mcp"
	"inode/adapters/openapi"
)

// The adapter programs this executable runs as `inode <kind> -- <arguments>`.
var adapterPrograms = map[string]func(context.Context, []string, *os.File, io.Writer, io.Writer) error{"mcp": mcp.Serve, "openapi": openapi.Serve}

func init() {
	inode.Adapters = []inode.Adapter{{Kind: "openapi", Label: "OpenAPI document", Plugin: openapiPlugin}, {Kind: "command", Label: "MCP server command", Plugin: mcpPlugin}}
}

// A file or URL that reads as an OpenAPI document is certainly one.
func openapiPlugin(dir string, input inode.Values) (inode.Values, []inode.Editable, error) {
	source, _ := input["source"].(string)
	if !strings.Contains(source, "://") {
		source = inode.LocalPath(dir, source)
	}
	base, _ := input["base"].(string)
	name, base, env, err := openapi.Inspect(source, base)
	if err != nil {
		if input["kind"] == nil {
			err = nil
		}
		return nil, nil, err
	}
	executable, err := os.Executable()
	return inode.Values{"name": name, "doc": "OpenAPI operations from " + source, "accesses": []string{"Calls " + base + " over the network and sends it the bound secrets; reads " + source + " each time it starts."},
		"process": []string{executable, "openapi", "--", source, base}, "env": env}, []inode.Editable{{Name: "base", Label: "Base URL", Kind: "text", Value: base}}, err
}

// A line with spaces is certainly a command; a lone word is one only when chosen.
func mcpPlugin(dir string, input inode.Values) (inode.Values, []inode.Editable, error) {
	source, _ := input["source"].(string)
	if input["kind"] == nil && !strings.ContainsFunc(source, unicode.IsSpace) {
		return nil, nil, nil
	}
	args, err := inode.SplitCommand(source)
	if err != nil {
		return nil, nil, err
	}
	if len(args) == 0 || args[0] == "" || strings.Contains(args[0], "://") {
		return nil, nil, fmt.Errorf("supply a local program command, not a URL")
	}
	if strings.ContainsRune(args[0], os.PathSeparator) {
		args[0] = inode.LocalPath(dir, args[0])
	}
	name := strings.TrimSuffix(filepath.Base(args[0]), filepath.Ext(args[0]))
	for _, arg := range args {
		if _, server, found := strings.Cut(arg, "server-"); found {
			name = server
			break
		}
	}
	executable, err := os.Executable()
	return inode.Values{"name": name, "doc": "MCP tools from " + args[0], "accesses": []string{"Runs a local program with this user's permissions; finer access claims are not verified."},
		"process": append([]string{executable, "mcp", "--"}, args...)}, nil, err
}
