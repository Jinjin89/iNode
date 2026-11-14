package inode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

var CommandPlugin func([]string) (process []string, doc, label string, err error)

type Source struct {
	Source string            `json:"source"`
	Kind   string            `json:"kind,omitempty"`
	Name   string            `json:"name,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

type Prepared struct {
	Input      Source     `json:"input"`
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

func prepareSource(dir string, value any) (Prepared, error) {
	input := Source{}
	if source, ok := value.(string); ok {
		input.Source = source
	} else {
		data, _ := json.Marshal(value)
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			return Prepared{}, err
		}
	}
	result := Prepared{Input: input}
	local := input.Source
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(local, "~/") {
		local = filepath.Join(home, local[2:])
	}
	if !filepath.IsAbs(local) {
		local = filepath.Join(dir, local)
	}
	info, _ := os.Stat(local)
	folder := info != nil && info.IsDir()
	space := strings.ContainsFunc(input.Source, unicode.IsSpace)
	git := !space && (strings.HasPrefix(input.Source, "git@") || strings.Contains(input.Source, "://") || strings.HasSuffix(strings.Split(input.Source, "#")[0], ".git"))
	if input.Kind == "plugin" || input.Kind == "" && (folder || git) {
		if folder {
			result.Input.Source = local
		}
		return result, nil
	}
	if input.Kind != "" && input.Kind != "command" {
		return result, fmt.Errorf("choose plugin or command")
	}
	args, err := splitCommand(input.Source)
	if err != nil {
		return result, err
	}
	if len(args) == 0 || args[0] == "" || strings.Contains(args[0], "://") {
		return result, fmt.Errorf("supply a local program command, not a URL")
	}
	if strings.ContainsRune(args[0], os.PathSeparator) && !filepath.IsAbs(args[0]) {
		args[0] = filepath.Join(dir, args[0])
	}
	if CommandPlugin == nil {
		return result, fmt.Errorf("command plugins are unavailable in this application")
	}
	process, doc, label, err := CommandPlugin(args)
	if err != nil {
		return result, err
	}
	if input.Kind == "" && !space {
		result.Message = "Choose how to use this source."
		result.Editable = []Editable{{Name: "kind", Label: "Source type", Kind: "choice", Choices: map[string]string{"plugin": "Plugin folder or git URL", "command": label}}}
		return result, nil
	}
	if input.Name == "" {
		input.Name = strings.TrimSuffix(filepath.Base(args[0]), filepath.Ext(args[0]))
		for _, arg := range args {
			if _, name, found := strings.Cut(arg, "server-"); found {
				input.Name = name
				break
			}
		}
		input.Name = strings.Map(func(c rune) rune {
			if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' || c == '-' {
				return c
			}
			return '-'
		}, input.Name)
	}
	for i, arg := range process {
		if strings.HasPrefix(arg, "=") {
			process[i] = "=" + arg
		}
	}
	result.Directory, result.WorkingDir = filepath.Join(dir, input.Name), filepath.Join(dir, ".inode", "plugins", input.Name)
	result.Manifest = Values{"name": input.Name, "version": "1", "doc": doc, "accesses": []string{"Runs a local program with this user's permissions; finer access claims are not verified."}, "process": process, "env": input.Env}
	result.Input.Kind = "command"
	result.Input.Name = input.Name
	result.Editable = []Editable{{Name: "name", Label: "Plugin name", Kind: "text", Value: input.Name}, {Name: "env", Label: "Environment variables", Kind: "bindings", Value: input.Env}}
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

func splitCommand(text string) ([]string, error) {
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
