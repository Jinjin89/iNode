package inode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type Client struct {
	Address    string `json:"address"`
	Credential string `json:"credential"`
}

type reply struct {
	Value json.RawMessage `json:"value,omitempty"`
	Error *string         `json:"error,omitempty"`
}

func backendFile(dir string) string { return filepath.Join(dir, ".inode", "backend") }

func locate(ctx context.Context, dir string) (*Client, error) {
	data, err := os.ReadFile(backendFile(dir))
	if err != nil {
		return nil, err
	}
	var client Client
	if err := json.Unmarshal(data, &client); err != nil {
		return nil, err
	}
	connection, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "tcp", client.Address)
	if err != nil {
		return nil, err
	}
	connection.Close()
	return &client, nil
}

func Connect(ctx context.Context, dir string, start bool) (*Client, bool, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, false, err
	}
	if client, err := locate(ctx, dir); err == nil {
		return client, false, nil
	}
	if !start {
		return nil, false, fmt.Errorf("no backend is running")
	}
	private := filepath.Dir(backendFile(dir))
	if err := os.MkdirAll(private, 0700); err != nil {
		return nil, false, err
	}
	log, err := os.OpenFile(filepath.Join(private, "backend.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, false, err
	}
	defer log.Close()
	executable, err := os.Executable()
	if err != nil {
		return nil, false, err
	}
	cmd := exec.Command(executable, "serve")
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	go cmd.Wait()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if client, err := locate(ctx, dir); err == nil {
			return client, true, nil
		}
		select {
		case <-ctx.Done():
			return nil, false, fmt.Errorf("backend did not start; see %s: %w", log.Name(), ctx.Err())
		case <-tick.C:
		}
	}
}

func (c *Client) Call(ctx context.Context, operation string, args []any, receive func(json.RawMessage) error) error {
	data, err := json.Marshal(append([]any{operation}, args...))
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+c.Address+"/", bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.Credential)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	decoder := json.NewDecoder(response.Body)
	for {
		var line reply
		err := decoder.Decode(&line)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if line.Error != nil {
			return errors.New(*line.Error)
		}
		if err := receive(line.Value); err != nil {
			return err
		}
	}
}
