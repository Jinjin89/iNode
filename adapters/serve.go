package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Serve answers the host's describe, run and cancel messages until its input ends or done closes; runs still working are then cancelled.
func Serve(ctx context.Context, in io.Reader, out io.Writer, done <-chan struct{}, nodes map[string]Object, run func(ctx context.Context, node string, inputs Object, dir string) (Object, error)) error {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer workers.Wait()
	defer cancel()
	dir, _ := os.Getwd()
	plugin := filepath.Base(dir)
	var mu, writer sync.Mutex
	active := map[string]context.CancelFunc{}
	reply := func(id string, value Object, err error) {
		writer.Lock()
		defer writer.Unlock()
		result := Object{"id": id}
		if err != nil {
			text := err.Error()
			if len(text) > 8192 {
				text = text[:8192] + " [truncated]"
			}
			result["error"] = text
		} else {
			for key, value := range value {
				result[key] = value
			}
		}
		data, _ := json.Marshal(result)
		if len(data) > 1<<20 {
			data, _ = json.Marshal(Object{"id": id, "error": "plugin reply exceeds 1 MiB; use files for large content"})
		}
		fmt.Fprintf(out, "%s\n", data)
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	type hostRequest struct {
		ID, Method, Node, Dir string
		Inputs                Object
	}
	requests, ended := make(chan hostRequest), make(chan error, 1)
	go func() {
		for scanner.Scan() {
			var request hostRequest
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				ended <- err
				return
			}
			select {
			case requests <- request:
			case <-done:
				return
			}
		}
		ended <- scanner.Err()
	}()
	for {
		var request hostRequest
		select {
		case request = <-requests:
		case err := <-ended:
			return err
		case <-done:
			return nil
		}
		switch request.Method {
		case "describe":
			reply(request.ID, Object{"nodes": nodes}, nil)
		case "cancel":
			mu.Lock()
			if stop := active[request.ID]; stop != nil {
				stop()
			}
			mu.Unlock()
		case "run":
			runCtx, stop := context.WithCancel(ctx)
			mu.Lock()
			active[request.ID] = stop
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer stop()
				outputs, err := run(runCtx, request.Node, request.Inputs, request.Dir)
				if err != nil {
					err = fmt.Errorf("%s.%s: %w", plugin, request.Node, err)
				}
				mu.Lock()
				delete(active, request.ID)
				mu.Unlock()
				reply(request.ID, Object{"outputs": outputs}, err)
			}()
		default:
			return fmt.Errorf("unsupported host method %q", request.Method)
		}
	}
}
