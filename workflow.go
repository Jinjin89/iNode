package inode

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

func version(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func (p *Project) Save(name, basedOn string, value json.RawMessage) (Node, error) {
	name = strings.TrimSuffix(name, ".flow")
	if name == "" || !filepath.IsLocal(name) || filepath.Base(name) != name {
		return Node{}, fmt.Errorf("choose a workflow name without path separators")
	}
	if node, exists := p.nodes[name]; exists && node.Workflow == nil {
		return Node{}, fmt.Errorf("node %q already exists; choose another workflow name", name)
	}
	var raw any
	if err := json.Unmarshal(value, &raw); err != nil {
		return Node{}, err
	}
	var flow Workflow
	if err := decode(value, &flow); err != nil {
		return Node{}, err
	}
	data, err := yaml.Marshal(raw)
	if err != nil {
		return Node{}, err
	}
	path := filepath.Join(p.dir, name+".flow")
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return Node{}, err
	}
	current := ""
	if err == nil {
		current = version(old)
	}
	if current != basedOn {
		return Node{}, fmt.Errorf("workflow %q changed on disk; open it again before saving", name)
	}
	if err := writeFile(path, data, 0644); err != nil {
		return Node{}, err
	}
	if err := p.load(); err != nil {
		return Node{}, err
	}
	return p.Describe(name)
}
