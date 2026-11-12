package inode

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (p *Project) secretFile(name string) (string, error) {
	if name == "" || !filepath.IsLocal(name) || filepath.Base(name) != name {
		return "", fmt.Errorf("choose a secret name without path separators")
	}
	return filepath.Join(p.dir, ".inode", "secrets", name+".secret"), nil
}

func (p *Project) SetSecret(name, value string) error {
	path, err := p.secretFile(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return writeFile(path, []byte(value), 0600, true)
}

func (p *Project) SecretNames() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(p.dir, ".inode", "secrets"))
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".secret") {
			names = append(names, strings.TrimSuffix(entry.Name(), ".secret"))
		}
	}
	return names, nil
}

func (p *Project) RemoveSecret(name string) error {
	path, err := p.secretFile(name)
	if err != nil {
		return err
	}
	return removeFile(path)
}

func secretError(name string, err error) error {
	if os.IsNotExist(err) {
		return fmt.Errorf("secret %q is not stored; store it with inode secret set %s", name, name)
	}
	return err
}

func (p *Project) checkSecrets(value any, want Type, path valuePath) error {
	_, err := walk(value, want, path, func(value any, want Type, path valuePath) (any, error) {
		if name, ok := value.(string); ok && want.Name == "secret" {
			file, err := p.secretFile(name)
			if err == nil {
				_, err = os.Stat(file)
			}
			if err != nil {
				return nil, Problem{Input: path.String(), Message: secretError(name, err).Error()}
			}
		}
		return value, nil
	})
	return err
}

func (p *Project) secretInputs(inputs Values, ports Ports) (Values, func(any) any, error) {
	replacements := map[string]string{}
	values, err := walk(inputs, typeOf(ports.tree()), nil, func(value any, want Type, path valuePath) (any, error) {
		if name, ok := value.(string); ok && want.Name == "secret" {
			file, err := p.secretFile(name)
			var data []byte
			if err == nil {
				data, err = os.ReadFile(file)
			}
			if err != nil {
				return nil, Problem{Input: path.String(), Message: secretError(name, err).Error()}
			}
			if len(data) > 0 {
				replacements[string(data)] = "[secret " + name + "]"
			}
			return string(data), nil
		}
		return value, nil
	})
	secrets := keys(replacements)
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	pairs := []string{}
	for _, value := range secrets {
		pairs = append(pairs, value, replacements[value])
	}
	replacer := strings.NewReplacer(pairs...)
	scrub := func(value any) any {
		if len(replacements) == 0 {
			return value
		}
		clean, _ := walk(value, anyType, nil, func(value any, _ Type, _ valuePath) (any, error) {
			switch value := value.(type) {
			case string:
				return replacer.Replace(value), nil
			case map[string]any:
				fields := Values{}
				for _, name := range keys(value) {
					fields[replacer.Replace(name)] = value[name]
				}
				return fields, nil
			}
			return value, nil
		})
		return clean
	}
	bound, _ := values.(Values)
	return bound, scrub, err
}
