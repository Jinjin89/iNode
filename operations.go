package inode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
)

type operation struct {
	call  any
	write bool
}

var Operations = map[string]operation{
	"nodes":       {call: (*Project).Nodes},
	"show":        {call: (*Project).Describe},
	"check":       {call: (*Project).Check},
	"run":         {call: (*backend).startRun},
	"runs":        {call: (*Project).Runs},
	"runs show":   {call: (*Project).ShowRun},
	"runs stop":   {call: (*backend).stopRun},
	"runs follow": {call: (*backend).followRun},
	"add":         {call: (*Project).Add, write: true},
	"remove":      {call: (*Project).Remove, write: true},
	"plugins":     {call: (*Project).Plugins},
	"stop":        {call: (*backend).stop},
}

type parameters struct {
	ctx     context.Context
	project *Project
	backend *backend
	send    func(any) error
}

func (p parameters) value(t reflect.Type) reflect.Value {
	switch t {
	case reflect.TypeFor[context.Context]():
		return reflect.ValueOf(&p.ctx).Elem()
	case reflect.TypeFor[*Project]():
		return reflect.ValueOf(p.project)
	case reflect.TypeFor[*backend]():
		return reflect.ValueOf(p.backend)
	case reflect.TypeFor[func(any) error]():
		return reflect.ValueOf(p.send)
	case reflect.TypeFor[io.Writer]():
		return reflect.ValueOf(streamWriter{p.send})
	}
	return reflect.Value{}
}

func OperationArgs(name string) (int, bool) {
	fn, exists := Operations[name]
	if !exists {
		return 0, false
	}
	t := reflect.TypeOf(fn.call)
	n := 0
	for i := range t.NumIn() {
		if !(parameters{}).value(t.In(i)).IsValid() {
			n++
		}
	}
	return n, true
}

func (p parameters) invoke(name string, args []json.RawMessage) error {
	n, exists := OperationArgs(name)
	if !exists {
		return fmt.Errorf("unknown operation %q", name)
	}
	if len(args) != n {
		return fmt.Errorf("%s needs %d arguments", name, n)
	}
	op := Operations[name]
	if op.write {
		p.backend.catalog.Lock()
		defer p.backend.catalog.Unlock()
	}
	fn := reflect.ValueOf(op.call)
	values := make([]reflect.Value, fn.Type().NumIn())
	for i := range values {
		t := fn.Type().In(i)
		if t == reflect.TypeFor[*Project]() {
			project, err := Open(p.backend.dir)
			if err != nil {
				return err
			}
			p.project = project
			project.mu, project.publish = &p.backend.mu, p.backend.publish
		}
		value := p.value(t)
		if !value.IsValid() {
			value = reflect.New(t)
			if err := decode(args[0], value.Interface()); err != nil {
				return err
			}
			args, value = args[1:], value.Elem()
		}
		values[i] = value
	}
	results := fn.Call(values)
	for _, result := range results {
		if result.Type() == reflect.TypeFor[error]() && !result.IsNil() {
			return result.Interface().(error)
		}
	}
	for _, result := range results {
		if result.Type() != reflect.TypeFor[error]() {
			if err := p.send(result.Interface()); err != nil {
				return err
			}
		}
	}
	return nil
}

type streamWriter struct{ send func(any) error }

func (w streamWriter) Write(data []byte) (int, error) {
	if err := w.send(string(data)); err != nil {
		return 0, err
	}
	return len(data), nil
}
