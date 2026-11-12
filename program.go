package inode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func (p *Project) loadNodes(dir, prefix string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".node") {
			continue
		}
		var description struct {
			Doc             string `json:"doc"`
			Inputs          Ports  `json:"inputs"`
			Outputs         Ports  `json:"outputs"`
			NoOutsideEffect bool   `json:"no_outside_effect"`
			Run             string `json:"run"`
		}
		path := filepath.Join(dir, entry.Name())
		err := readYAML(path, &description)
		if err == nil && (description.Doc == "" || description.Run == "") {
			err = fmt.Errorf("%s: supply doc and run", path)
		}
		node := Node{Doc: description.Doc, Inputs: description.Inputs, Outputs: description.Outputs,
			NoOutsideEffect: description.NoOutsideEffect, problem: err}
		node.run = func(ctx context.Context, inputs Values, _ Call) (Values, error) {
			data, err := json.Marshal(inputs)
			if err != nil {
				return nil, err
			}
			data, err = process(ctx, dir, append(data, '\n'), "sh", "-c", description.Run)
			if err != nil {
				return nil, err
			}
			var outputs Values
			if err := json.Unmarshal(data, &outputs); err != nil {
				return nil, fmt.Errorf("print one JSON object of outputs: %w", err)
			}
			if outputs == nil {
				return nil, fmt.Errorf("print one JSON object of outputs, not null")
			}
			return outputs, nil
		}
		p.register(prefix+strings.TrimSuffix(entry.Name(), ".node"), node)
	}
	return nil
}

func process(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir, cmd.Stdin = dir, bytes.NewReader(input)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("%s", message)
		}
	}
	return output, err
}
