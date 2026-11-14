package inode

import (
	"fmt"
	"strconv"
	"strings"
)

func selectPath(value any, parts []string) (any, error) {
	for _, part := range parts {
		if load, ok := value.(func() (any, error)); ok {
			var err error
			value, err = load()
			if err != nil {
				return nil, err
			}
		}
		var next any
		var names []string
		found, list := false, false
		index, indexErr := strconv.Atoi(part)
		switch current := value.(type) {
		case map[string]any:
			next, found = current[part]
			if !found {
				names = keys(current)
			}
		case []any:
			list = true
			if indexErr == nil && index >= 0 && index < len(current) {
				next, found = current[index], true
			}
		case Type:
			list = current.Name == "list"
			if current.Name == "any" || current.Name == "object" && current.Fields == nil {
				next, found = anyType, true
			} else if current.Fields != nil {
				next, found = current.Fields[part]
				if !found {
					names = keys(current.Fields)
				}
			} else if list && indexErr == nil && index >= 0 {
				next, found = *current.Item, true
			}
		}
		if !found {
			var err error
			switch {
			case list:
				err = fmt.Errorf("list index %q is invalid or out of range", part)
			case names != nil:
				err = fmt.Errorf("no field %q; choose one of %v", part, names)
			default:
				err = fmt.Errorf("no field %q; remove this path segment", part)
			}
			return nil, err
		}
		value = next
	}
	return value, nil
}

type valuePath []string

func (p valuePath) child(parts ...string) valuePath {
	return append(append(valuePath{}, p...), parts...)
}
func (p valuePath) String() string { return strings.Join(p, ".") }

func referenceParts(value string) []string {
	if strings.HasPrefix(value, "=") && !strings.HasPrefix(value, "==") {
		return strings.Split(value[1:], ".")
	}
	return nil
}

// A node-valued port is a leaf even when its syntax is an object.
func walk(value any, want Type, path valuePath, leaf func(any, Type, valuePath) (any, error)) (any, error) {
	if want.Name != "node" && !fileLike(value) {
		switch v := value.(type) {
		case []any:
			result := make([]any, len(v))
			itemType := anyType
			if want.Item != nil {
				itemType = *want.Item
			}
			for i, item := range v {
				var err error
				result[i], err = walk(item, itemType, path.child(strconv.Itoa(i)), leaf)
				if err != nil {
					return nil, err
				}
			}
			value = result
		case map[string]any:
			result := Values{}
			for _, name := range keys(v) {
				field := anyType
				if typ, exists := want.Fields[name]; exists {
					field = typ
				}
				var err error
				result[name], err = walk(v[name], field, path.child(name), leaf)
				if err != nil {
					return nil, err
				}
			}
			value = result
		}
	}
	return leaf(value, want, path)
}

type Reference struct {
	Step      string    `json:"step"`
	Input     string    `json:"input"`
	From      []string  `json:"from"`
	Path      valuePath `json:"path"`
	Unchecked bool      `json:"unchecked,omitempty"`
	Bad       bool      `json:"bad,omitempty"`
}

func resolve(value any, want Type, path valuePath, tree Values) (any, []Reference, error) {
	var found []Reference
	result, err := walk(value, want, path, func(value any, want Type, path valuePath) (any, error) {
		switch v := value.(type) {
		case string:
			if strings.HasPrefix(v, "==") {
				return v[1:], nil
			}
			if parts := referenceParts(v); parts != nil {
				if len(parts) < 2 {
					return nil, Problem{Input: path.String(), Message: v + " needs a step and output; use =step.output or == for literal text starting with ="}
				}
				selected, err := selectPath(tree, parts)
				if err != nil {
					return nil, Problem{Input: path.String(), Message: v + ": " + err.Error()}
				}
				unchecked, _ := match(selected, want, map[string]Type{})
				found = append(found, Reference{Input: path.String(), From: parts, Path: path, Unchecked: typeOf(selected).unchecked() || unchecked})
				return selected, nil
			}
		case *Step:
			bound := *v
			inputs, refs, err := resolve(v.In, typeOf(v.plan.Inputs.tree()), path.child("in"), tree)
			if err != nil {
				return nil, err
			}
			bound.In = inputs.(Values)
			found = append(found, refs...)
			return &bound, nil
		}
		return value, nil
	})
	return result, found, err
}

func normalise(value any) (any, error) {
	return walk(value, anyType, nil, func(value any, _ Type, _ valuePath) (any, error) {
		switch value.(type) {
		case *Step, fileRef, map[string]any, []any:
			return value, nil
		}
		return jsonData(value)
	})
}

func match(value any, want Type, types map[string]Type) (bool, error) {
	got := typeOf(value)
	if want.variable() {
		if bound, exists := types[want.Name]; exists {
			return match(value, bound, types)
		}
		if got.Name == "any" {
			return true, nil
		}
		types[want.Name], want = got, got
	}
	if want.Name == "any" {
		return false, nil
	}
	if fileLike(value) && want.Name == "file" {
		return false, nil
	}
	if got.Name == "text" && want.Name == "file" {
		_, unknown := value.(Type)
		return unknown, nil
	}
	if leaf, ok := value.(Type); ok && leaf.Name == "any" {
		return true, nil
	}
	unchecked := false
	switch v := value.(type) {
	case Type:
		if v.Name != want.Name {
			break
		}
		if v.Name == "object" && (v.Fields == nil || want.Fields == nil) {
			return true, nil
		}
		if v.Name == "object" || v.Name == "node" {
			if v.Name == "node" && len(v.Fields) != len(want.Fields) {
				return false, fmt.Errorf("open input count does not match %s; fill or expose one input", want)
			}
			return match(v.shape(), Type{Name: "object", Fields: want.Fields}, types)
		}
		if v.Name == "list" {
			return match(v.shape(), want, types)
		}
		return false, nil
	case *Step:
		return match(got, want, types)
	case []any:
		if want.Name != "list" {
			break
		}
		for i, item := range v {
			unknown, err := match(item, *want.Item, types)
			if err != nil {
				return false, fmt.Errorf("item %d: %w", i, err)
			}
			unchecked = unchecked || unknown
		}
		return unchecked, nil
	case map[string]any:
		if want.Name != "object" {
			break
		}
		if want.Fields == nil {
			return true, nil
		}
		if len(v) != len(want.Fields) {
			break
		}
		for _, name := range keys(want.Fields) {
			item, exists := v[name]
			if !exists {
				return false, fmt.Errorf("missing field %q; supply it", name)
			}
			unknown, err := match(item, want.Fields[name], types)
			if err != nil {
				return false, fmt.Errorf("field %q: %w", name, err)
			}
			unchecked = unchecked || unknown
		}
		return unchecked, nil
	default:
		if got.Name == want.Name || want.Name == "secret" && got.Name == "text" {
			return false, nil
		}
	}
	return false, fmt.Errorf("needs %s, got %s; supply a %s value or output", want, got, want)
}
