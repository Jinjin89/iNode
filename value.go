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

// A node-valued port is a leaf even when its syntax is an object.
func walk(value any, want Type, path string, leaf func(any, Type, string) (any, error)) (any, error) {
	if want.Name != "node" {
		switch v := value.(type) {
		case []any:
			result := make([]any, len(v))
			itemType := anyType
			if want.Item != nil {
				itemType = *want.Item
			}
			for i, item := range v {
				var err error
				result[i], err = walk(item, itemType, fmt.Sprintf("%s.%d", path, i), leaf)
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
				result[name], err = walk(v[name], field, strings.TrimPrefix(path+"."+name, "."), leaf)
				if err != nil {
					return nil, err
				}
			}
			value = result
		}
	}
	return leaf(value, want, path)
}

type reference struct {
	Step, Input string
	Unchecked   bool
}

func resolve(value any, path string, tree Values) (any, []reference, error) {
	var found []reference
	result, err := walk(value, anyType, path, func(value any, _ Type, path string) (any, error) {
		switch v := value.(type) {
		case string:
			if strings.HasPrefix(v, "==") {
				return v[1:], nil
			}
			if strings.HasPrefix(v, "=") {
				parts := strings.Split(v[1:], ".")
				if len(parts) < 2 {
					return nil, Problem{Input: path, Message: v + " needs a step and output; use =step.output or == for literal text starting with ="}
				}
				selected, err := selectPath(tree, parts)
				if err != nil {
					return nil, Problem{Input: path, Message: v + ": " + err.Error()}
				}
				found = append(found, reference{parts[0], path, typeOf(selected).unchecked()})
				return selected, nil
			}
		case *Step:
			bound := *v
			inputs, refs, err := resolve(v.In, path+".in", tree)
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
	return walk(value, anyType, "", func(value any, _ Type, _ string) (any, error) {
		switch value.(type) {
		case *Step, map[string]any, []any:
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
		if got.Name == want.Name {
			return false, nil
		}
	}
	return false, fmt.Errorf("needs %s, got %s; supply a %s value or output", want, got, want)
}
