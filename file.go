package inode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

func writeFile(path string, data []byte, mode os.FileMode, durable bool) error {
	return writeStream(path, bytes.NewReader(data), mode, durable)
}

func writeStream(path string, data io.Reader, mode os.FileMode, durable bool) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".inode-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = file.Chmod(mode)
	if err == nil {
		_, err = io.Copy(file, data)
	}
	if err == nil && durable {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err == nil && durable {
		err = syncDirectory(filepath.Dir(path))
	}
	return err
}

type fileRef struct {
	Hash string `json:"$file"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func fileLike(value any) bool {
	if _, ok := value.(fileRef); ok {
		return true
	}
	fields, ok := value.(map[string]any)
	_, tagged := fields["$file"].(string)
	return ok && tagged
}

func (p *Project) fullPath(name string) string {
	if name == "~" || strings.HasPrefix(name, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			name = filepath.Join(home, strings.TrimPrefix(name, "~"))
		}
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(p.dir, name)
	}
	return name
}

func (p *Project) blob(file fileRef) string {
	return filepath.Join(p.dir, ".inode", "files", strings.TrimPrefix(file.Hash, "sha256:"))
}

func (p *Project) storeFile(data io.Reader, name string) (fileRef, error) {
	dir := filepath.Join(p.dir, ".inode", "files")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fileRef{}, err
	}
	temp, err := os.CreateTemp(dir, ".pending-")
	if err != nil {
		return fileRef{}, err
	}
	defer os.Remove(temp.Name())
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temp, hash), data)
	if err == nil {
		err = temp.Chmod(0400)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	ref := fileRef{"sha256:" + hex.EncodeToString(hash.Sum(nil)), filepath.Base(name), size}
	if err == nil {
		err = os.Link(temp.Name(), p.blob(ref))
		if os.IsExist(err) {
			err = nil
		}
	}
	if err == nil {
		err = syncDirectory(dir)
	}
	return ref, err
}

type fileMode uint8

const (
	checkFiles fileMode = iota
	takeFiles
	returnedFiles
)

func (p *Project) localFile(name string, mode fileMode) (any, error) {
	if mode == returnedFiles {
		return nil, fmt.Errorf("return a stored file reference, not a path")
	}
	path := p.fullPath(name)
	info, err := os.Stat(path)
	if err == nil && !info.Mode().IsRegular() {
		kind := "not a regular file"
		if info.IsDir() {
			kind = "a folder"
		}
		return nil, fmt.Errorf("%q is %s at %s; enter the path of a file", name, kind, path)
	}
	var file *os.File
	if err == nil {
		file, err = os.Open(path)
	}
	switch {
	case err == nil:
		defer file.Close()
		if mode == checkFiles {
			return name, nil
		}
		return p.storeFile(file, name)
	case os.IsNotExist(err):
		return nil, fmt.Errorf("file %q was not found at %s; enter the path of an existing file", name, path)
	case os.IsPermission(err):
		return nil, fmt.Errorf("file %q cannot be read at %s; choose a file you have permission to read", name, path)
	default:
		return nil, fmt.Errorf("file %q could not be read at %s: %v; choose a readable file", name, path, err)
	}
}

// The one file-value normalizer. Checking opens files but never reads/copies bytes.
func (p *Project) files(value any, want Type, mode fileMode, at ...string) (any, error) {
	return walk(value, want, valuePath(at), func(value any, want Type, path valuePath) (any, error) {
		if node, ok := value.(*Step); ok && mode == takeFiles {
			accepted, err := p.acceptInputs(*node, node.In)
			return &accepted, err
		}
		if !fileLike(value) && want.Name != "file" {
			return value, nil
		}
		if _, ok := value.(Type); ok {
			return value, nil
		}
		if name, ok := value.(string); ok {
			value, err := p.localFile(name, mode)
			if err != nil {
				return nil, Problem{Input: path.String(), Message: err.Error()}
			}
			return value, nil
		}
		var ref fileRef
		data, _ := json.Marshal(value)
		err := decode(data, &ref)
		if fields, ok := value.(map[string]any); ok && len(fields) != 3 {
			err = fmt.Errorf("supply $file, name and size")
		}
		digest, decodeErr := hex.DecodeString(strings.TrimPrefix(ref.Hash, "sha256:"))
		if err == nil && (decodeErr != nil || len(digest) != sha256.Size || !strings.HasPrefix(ref.Hash, "sha256:") || ref.Hash != "sha256:"+hex.EncodeToString(digest) || ref.Size < 0 || ref.Name == "" || ref.Name == "." || ref.Name == ".." || strings.ContainsAny(ref.Name, "/\\\x00")) {
			err = fmt.Errorf("supply a sha256 file reference with a basename and nonnegative byte size")
		}
		if err == nil {
			var info os.FileInfo
			info, err = os.Lstat(p.blob(ref))
			if err == nil && (!info.Mode().IsRegular() || info.Size() != ref.Size) {
				err = fmt.Errorf("stored bytes do not match the file reference")
			}
		}
		if err != nil {
			return nil, Problem{Input: path.String(), Message: fmt.Sprintf("invalid file reference: %v; copy a reference from a run in this project", err)}
		}
		return ref, nil
	})
}

// Acceptance pins resolved inputs, boxes and the literal inputs in its snapshot.
func (p *Project) acceptInputs(node Step, inputs Values) (Step, error) {
	node.In = inputs
	var snapshot func(Step, bool) (Step, error)
	snapshot = func(node Step, expressions bool) (Step, error) {
		plan := *node.plan
		plan.Inputs = maps.Clone(plan.Inputs)
		node.plan = &plan
		inputs := Values{}
		for name, port := range plan.Inputs {
			if _, filled := node.In[name]; !filled && port.Default != nil {
				value, err := p.files(port.Default, port.Type, takeFiles, name)
				if err != nil {
					return node, err
				}
				port.Default = value
				plan.Inputs[name] = port
				if containsType(typeOf(value), "file") {
					inputs[name] = value
				}
			}
		}
		maps.Copy(inputs, node.In)
		values, err := walk(inputs, typeOf(plan.Inputs.tree()), nil, func(value any, want Type, path valuePath) (any, error) {
			if box, ok := value.(*Step); ok {
				pinned, err := snapshot(*box, expressions)
				return &pinned, err
			}
			if !fileLike(value) && want.Name != "file" {
				return value, nil
			}
			if text, ok := value.(string); ok && expressions {
				if referenceParts(text) != nil {
					return value, nil
				}
				if strings.HasPrefix(text, "==") {
					value = text[1:]
				}
			}
			return p.files(value, want, takeFiles, path...)
		})
		if err != nil {
			return node, err
		}
		node.In, _ = values.(Values)
		if plan.Workflow != nil {
			flow := *plan.Workflow
			flow.Inputs, flow.Steps = plan.Inputs, maps.Clone(flow.Steps)
			for id, child := range flow.Steps {
				flow.Steps[id], err = snapshot(child, true)
				if err != nil {
					return node, fmt.Errorf("step %q: %w", id, err)
				}
			}
			plan.Workflow = &flow
			if node.Use.Workflow != nil {
				node.Use.Workflow = &flow
			}
		}
		return node, nil
	}
	return snapshot(node, false)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	if closeErr := directory.Close(); err == nil {
		err = closeErr
	}
	return err
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
