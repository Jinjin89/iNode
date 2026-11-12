package inode

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type backend struct {
	dir      string
	ctx      context.Context
	cancel   context.CancelFunc
	offline  chan struct{}
	mu       sync.Mutex
	catalog  sync.Mutex
	active   map[string]*activeRun
	wg       sync.WaitGroup
	requests sync.WaitGroup
}

type activeRun struct {
	cancel  context.CancelFunc
	latest  json.RawMessage
	changed chan struct{}
}

func Serve(ctx context.Context, dir string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	private := filepath.Join(dir, ".inode")
	if err := os.MkdirAll(private, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(private, "backend.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("a backend already holds this project's lock: %w", err)
	}
	p, err := Open(dir)
	if err != nil {
		return err
	}
	runs, err := p.Runs()
	if err != nil {
		return err
	}
	for i := range runs {
		if runs[i].State == "running" || runs[i].State == "waiting" {
			run := &runs[i]
			e := execution{project: p, root: run}
			if err := e.update(run, nil, func() { interrupt(run) }); err != nil {
				return err
			}
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	client := Client{Address: listener.Addr().String(), Credential: rand.Text()}
	data, _ := json.Marshal(client)
	if err := writeFile(backendFile(dir), data, 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b := &backend{dir: dir, ctx: ctx, cancel: cancel, offline: make(chan struct{}), active: map[string]*activeRun{}}
	page := frontend()
	server := &http.Server{BaseContext: func(net.Listener) context.Context { return ctx }, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		sent := false
		line := func(value reply) error {
			sent = true
			if err := json.NewEncoder(w).Encode(value); err != nil {
				return err
			}
			return http.NewResponseController(w).Flush()
		}
		send := func(value any) error {
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			return line(reply{Value: data})
		}
		fail := func(status int, message string) {
			if !sent {
				w.WriteHeader(status)
			}
			line(reply{Error: &message})
		}
		if r.Host != client.Address || r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+client.Address {
			fail(http.StatusForbidden, "request refused")
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			w.Header().Del("Content-Type")
			page.ServeHTTP(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+client.Credential)) != 1 {
			fail(http.StatusForbidden, "request refused")
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/" {
			fail(http.StatusMethodNotAllowed, "POST a JSON operation to /")
			return
		}
		var args []json.RawMessage
		err := json.NewDecoder(r.Body).Decode(&args)
		var name string
		if err == nil && len(args) == 0 {
			err = fmt.Errorf("send [operation, arguments...]")
		}
		if err == nil {
			err = json.Unmarshal(args[0], &name)
		}
		if err == nil {
			b.mu.Lock()
			err = b.ctx.Err()
			if err == nil {
				b.requests.Add(1)
			}
			b.mu.Unlock()
		}
		if err == nil {
			defer b.requests.Done()
			err = (parameters{ctx: r.Context(), backend: b, send: send}).invoke(name, args[1:])
		}
		if err != nil {
			fail(http.StatusBadRequest, err.Error())
		} else if !sent {
			w.WriteHeader(http.StatusNoContent)
			http.NewResponseController(w).Flush()
		}
	})}
	ended := make(chan error, 1)
	go func() { ended <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
	case err = <-ended:
	}
	b.mu.Lock()
	b.cancel()
	b.mu.Unlock()
	b.wg.Wait()
	os.Remove(backendFile(dir))
	close(b.offline)
	b.requests.Wait()
	if closeErr := server.Close(); err == nil {
		err = closeErr
	}
	return err
}

func interrupt(r *Record) {
	r.fail(context.Canceled)
	r.Finished = time.Now().UTC()
	for _, child := range r.Steps {
		if child.State == "running" {
			interrupt(child)
		}
	}
}

func (b *backend) startRun(p *Project, use Definition, inputs Values, name string) (string, error) {
	b.mu.Lock()
	if err := b.ctx.Err(); err != nil {
		b.mu.Unlock()
		return "", err
	}
	b.wg.Add(1)
	b.mu.Unlock()
	e, err := p.begin(use, inputs, name)
	if err != nil {
		b.wg.Done()
		return "", err
	}
	runCtx, cancel := context.WithCancel(b.ctx)
	b.mu.Lock()
	data, _ := json.Marshal(e.root)
	b.active[e.root.ID] = &activeRun{cancel, data, make(chan struct{})}
	b.mu.Unlock()
	go func() {
		defer b.wg.Done()
		defer cancel()
		e.run(runCtx)
	}()
	return e.root.ID, nil
}

func (b *backend) again(p *Project, id string) (string, error) {
	record, err := p.ShowRun(id)
	if err != nil {
		return "", err
	}
	use := Definition{Name: record.Node}
	if record.Unsaved {
		use = Definition{Workflow: record.Workflow}
	}
	return b.startRun(p, use, record.Inputs, record.Node)
}

func (b *backend) stopRun(p *Project, id string) error {
	b.mu.Lock()
	run := b.active[id]
	b.mu.Unlock()
	if run != nil {
		run.cancel()
		return nil
	}
	_, err := p.ShowRun(id)
	return err
}

func (b *backend) publish(r *Record) {
	if run := b.active[r.ID]; run != nil {
		run.latest, _ = json.Marshal(r)
		close(run.changed)
		run.changed = make(chan struct{})
		if r.State != "running" && r.State != "waiting" {
			delete(b.active, r.ID)
		}
	}
}

func (b *backend) followRun(ctx context.Context, p *Project, id string, send func(any) error) error {
	for {
		b.mu.Lock()
		var value any
		var changed <-chan struct{}
		var err error
		if run := b.active[id]; run != nil {
			value, changed = run.latest, run.changed
		} else {
			value, err = p.ShowRun(id)
		}
		b.mu.Unlock()
		if err != nil {
			return err
		}
		if err := send(value); err != nil {
			return err
		}
		if changed == nil {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (b *backend) stop() { b.cancel(); <-b.offline }
