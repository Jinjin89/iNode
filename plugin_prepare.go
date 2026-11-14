package inode

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// Adapters make a process plugin from a source that is not a plugin folder or git URL; the application lists them, most specific first.
var Adapters []Adapter

// Plugin drafts a manifest (name, doc, accesses, process and suggested env) with the adapter's own editable fields.
// Until input has its kind it returns no manifest unless the source is certainly its own.
type Adapter struct {
	Kind, Label string
	Plugin      func(dir string, input Values) (Values, []Editable, error)
}

type Prepared struct {
	Input      Values     `json:"input"`
	Editable   []Editable `json:"editable,omitempty"`
	Manifest   Values     `json:"manifest,omitempty"`
	Directory  string     `json:"directory,omitempty"`
	WorkingDir string     `json:"working_dir,omitempty"`
	Message    string     `json:"message,omitempty"`
}

type Editable struct {
	Name    string            `json:"name"`
	Label   string            `json:"label"`
	Kind    string            `json:"kind"`
	Value   any               `json:"value,omitempty"`
	Choices map[string]string `json:"choices,omitempty"`
}

// LocalPath expands ~/ and places a relative path in dir.
func LocalPath(dir, path string) string {
	if home, _ := os.UserHomeDir(); strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return path
}

func prepareSource(dir string, value any) (Prepared, error) {
	input, ok := value.(Values)
	if !ok {
		input = Values{"source": value}
	}
	source, _ := input["source"].(string)
	kind, _ := input["kind"].(string)
	result := Prepared{Input: maps.Clone(input)}
	if info, _ := os.Stat(LocalPath(dir, source)); info != nil && info.IsDir() && (kind == "" || kind == "plugin") {
		result.Input["source"] = LocalPath(dir, source)
		return result, nil
	}
	address := !strings.ContainsFunc(source, unicode.IsSpace) && (strings.HasSuffix(strings.Split(source, "#")[0], ".git") || slices.ContainsFunc([]string{"git@", "git://", "ssh://", "file://"}, func(prefix string) bool { return strings.HasPrefix(source, prefix) }))
	var manifest Values
	var fields []Editable
	choices := map[string]string{"plugin": "Plugin folder or git URL"}
	for _, adapter := range Adapters {
		choices[adapter.Kind] = adapter.Label
		if manifest != nil || kind == "" && address || kind != "" && kind != adapter.Kind {
			continue
		}
		var err error
		if manifest, fields, err = adapter.Plugin(dir, input); err != nil {
			return result, err
		}
		if manifest != nil {
			result.Input["kind"] = adapter.Kind
		}
	}
	if manifest == nil {
		if choices[kind] == "" && kind != "" {
			return result, fmt.Errorf("choose one of the listed source types")
		}
		if kind == "" && !address && !strings.Contains(source, "://") {
			result.Message = "Choose how to use this source."
			result.Editable = []Editable{{Name: "kind", Label: "Source type", Kind: "choice", Choices: choices}}
		}
		return result, nil
	}
	name, _ := input["name"].(string)
	if name == "" {
		name, _ = manifest["name"].(string)
		name = strings.Map(func(c rune) rune {
			if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' || c == '-' {
				return c
			}
			return '-'
		}, name)
	}
	process, _ := manifest["process"].([]string)
	for i, arg := range process {
		if strings.HasPrefix(arg, "=") {
			process[i] = "=" + arg
		}
	}
	env := manifest["env"]
	if input["env"] != nil {
		env = input["env"]
	}
	manifest["name"], manifest["version"], manifest["env"] = name, "1", input["env"]
	result.Input["name"], result.Manifest, result.Directory, result.WorkingDir = name, manifest, filepath.Join(dir, name), filepath.Join(dir, ".inode", "plugins", name)
	result.Editable = append(append([]Editable{{Name: "name", Label: "Plugin name", Kind: "text", Value: name}}, fields...), Editable{Name: "env", Label: "Environment variables", Kind: "bindings", Value: env})
	return result, nil
}

func (p Prepared) Write() (string, error) {
	if p.Manifest == nil {
		return "", fmt.Errorf("choose whether this is a plugin or command before installing")
	}
	if err := os.Mkdir(p.Directory, 0755); err != nil {
		return "", fmt.Errorf("choose an unused plugin folder: %w", err)
	}
	data, _ := json.MarshalIndent(p.Manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(p.Directory, "plugin"), append(data, '\n'), 0644); err != nil {
		os.Remove(p.Directory)
		return "", err
	}
	return p.Directory, nil
}

func SplitCommand(text string) ([]string, error) {
	home, _ := os.UserHomeDir()
	args, word, quote, escaped, started := []string{}, strings.Builder{}, rune(0), false, false
	finish := func() {
		if started {
			value := word.String()
			if strings.HasPrefix(value, "~/") {
				value = filepath.Join(home, value[2:])
			}
			args = append(args, value)
			word.Reset()
			started = false
		}
	}
	for _, c := range text {
		switch {
		case escaped:
			word.WriteRune(c)
			escaped = false
		case c == '\\' && quote != '\'':
			escaped = true
			started = true
		case quote != 0:
			if c == quote {
				quote = 0
			} else if quote == '"' && strings.ContainsRune("$`", c) {
				return nil, fmt.Errorf("shell substitution is unsupported; use literal arguments")
			} else {
				word.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote = c
			started = true
		case strings.ContainsRune("|&;<>$`(){}*?[]!\n\r", c):
			return nil, fmt.Errorf("shell operators and substitutions are unsupported; launch one program with arguments")
		case unicode.IsSpace(c):
			finish()
		default:
			word.WriteRune(c)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("finish the quoted word or escape in the command")
	}
	finish()
	return args, nil
}
