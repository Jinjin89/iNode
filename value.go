package inode

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

type Type struct {
	Name string
	Item *Type
}

var (
	textType   = Type{Name: "text"}
	numberType = Type{Name: "number"}
	boolType   = Type{Name: "bool"}
	anyType    = Type{Name: "any"}
	objectType = Type{Name: "object"}
	listType   = Type{Name: "list", Item: &anyType}
)

func (t *Type) UnmarshalText(data []byte) error {
	name := string(data)
	if item, ok := strings.CutPrefix(name, "list of "); ok {
		var element Type
		if err := element.UnmarshalText([]byte(item)); err != nil {
			return err
		}
		*t = Type{Name: "list", Item: &element}
		return nil
	}
	for _, known := range []Type{textType, numberType, boolType, anyType, objectType, listType} {
		if name == known.Name {
			*t = known
			return nil
		}
	}
	return fmt.Errorf("unsupported type %q; use text, number, bool, any, object, list, or list of a supported type", name)
}

func (t Type) String() string {
	if t.Item != nil {
		return t.Name + " of " + t.Item.String()
	}
	return t.Name
}

func (t Type) MarshalText() ([]byte, error) { return []byte(t.String()), nil }

func (t Type) fits(want Type) bool {
	return t.Name == "any" || want.Name == "any" || t.Name == want.Name && (t.Name != "list" || t.Item.fits(*want.Item))
}

func (t Type) unchecked() bool {
	return t.Name == "any" || t.Name == "object" || t.Item != nil && t.Item.unchecked()
}

func typeOf(value any) Type {
	switch value := value.(type) {
	case Type:
		return value
	case string:
		return textType
	case float64:
		return numberType
	case bool:
		return boolType
	case map[string]any:
		return objectType
	case []any:
		item := anyType
		if len(value) > 0 {
			item = typeOf(value[0])
			for _, next := range value[1:] {
				if !reflect.DeepEqual(item, typeOf(next)) {
					item = anyType
					break
				}
			}
		}
		return Type{Name: "list", Item: &item}
	}
	return anyType
}

func match(value any, want Type) error {
	if want.Name == "any" {
		return nil
	}
	if leaf, ok := value.(Type); ok {
		if leaf.fits(want) {
			return nil
		}
	} else if list, ok := value.([]any); ok && want.Name == "list" {
		for i, item := range list {
			if err := match(item, *want.Item); err != nil {
				return fmt.Errorf("item %d: %w", i, err)
			}
		}
		return nil
	} else if typeOf(value).Name == want.Name {
		return nil
	}
	return fmt.Errorf("needs %s, got %s; supply a %s value or output", want, typeOf(value), want)
}

func selectPath(value any, parts []string) (any, error) {
	for _, part := range parts {
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
			if current.Name == "any" || current.Name == "object" {
				next, found = anyType, true
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

func resolve(value any, path string, tree Values, visit func([]string, string, any)) (any, error) {
	switch value := value.(type) {
	case string:
		if strings.HasPrefix(value, "==") {
			return value[1:], nil
		}
		if strings.HasPrefix(value, "=") {
			ref := value[1:]
			parts := strings.Split(ref, ".")
			if len(parts) < 2 {
				return nil, fmt.Errorf("input %q: =%s needs a step and output; use =step.output or == for literal text starting with =", path, ref)
			}
			selected, err := selectPath(tree, parts)
			if err != nil {
				return nil, fmt.Errorf("input %q: =%s: %w", path, ref, err)
			}
			if visit != nil {
				visit(parts, path, selected)
			}
			return selected, nil
		}
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			var err error
			result[i], err = resolve(item, fmt.Sprintf("%s.%d", path, i), tree, visit)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	case map[string]any:
		result := Values{}
		for _, key := range keys(value) {
			item, err := resolve(value[key], strings.TrimPrefix(path+"."+key, "."), tree, visit)
			if err != nil {
				return nil, err
			}
			result[key] = item
		}
		return result, nil
	}
	return value, nil
}
