package inode

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginInspectionRetainsInvalidEditableDraft(t *testing.T) {
	commandPlugins(t)
	p := fileProject(t, nil)
	for _, request := range []Values{
		{"source": "python3 -c 'pass'", "kind": "command", "name": "draft", "env": Values{"API_KEY": ""}},
		{"source": "python3 -c 'pass'", "kind": "command", "name": "bad.name"},
	} {
		plugin, err := p.InspectPlugin(context.Background(), request)
		if err != nil || plugin.Error == "" || plugin.Preparation == nil || len(plugin.Preparation.Editable) != 2 {
			t.Fatalf("draft must stay editable: %+v, %v", plugin, err)
		}
		if err := p.Add(context.Background(), request, io.Discard); err == nil {
			t.Fatal("invalid draft was installed")
		}
		if _, err := os.Stat(filepath.Join(p.dir, request["name"].(string))); !os.IsNotExist(err) {
			t.Fatal("invalid draft materialized a folder")
		}
	}
	plugin, err := p.InspectPlugin(context.Background(), Values{"source": "python3 -c 'pass'", "kind": "command", "name": "draft"})
	if err != nil || plugin.Error != "" || !strings.Contains(plugin.Preparation.WorkingDir, filepath.Join(".inode", "plugins", "draft")) {
		t.Fatalf("valid draft: %+v, %v", plugin, err)
	}
}
