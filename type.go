package inode

import (
	"fmt"
	"reflect"
	"strings"
)

type Type struct {
	Name   string
	Item   *Type
	Fields map[string]Type
}

var (
	textType   = Type{Name: "text"}
	numberType = Type{Name: "number"}
	boolType   = Type{Name: "bool"}
	anyType    = Type{Name: "any"}
)

type typeParser struct{ tokens []string }

func (p *typeParser) take(token string) bool {
	if len(p.tokens) == 0 || p.tokens[0] != token {
		return false
	}
	p.tokens = p.tokens[1:]
	return true
}

func (p *typeParser) parse() (Type, error) {
	if len(p.tokens) == 0 {
		return Type{}, fmt.Errorf("supply a type")
	}
	name := p.tokens[0]
	p.tokens = p.tokens[1:]
	t := Type{Name: name}
	switch name {
	case "text", "number", "bool", "any":
	case "list":
		t.Item = &anyType
		if p.take("of") {
			item, err := p.parse()
			if err != nil {
				return Type{}, err
			}
			t.Item = &item
		}
	case "object":
		if p.take("{") {
			t.Fields = map[string]Type{}
			for !p.take("}") {
				if len(p.tokens) < 3 {
					return Type{}, fmt.Errorf("finish object fields as name: type inside braces")
				}
				field := p.tokens[0]
				p.tokens = p.tokens[1:]
				if !portName(field) || !p.take(":") {
					return Type{}, fmt.Errorf("write an object field as name: type")
				}
				if _, exists := t.Fields[field]; exists {
					return Type{}, fmt.Errorf("declare field %q only once", field)
				}
				typ, err := p.parse()
				if err != nil {
					return Type{}, err
				}
				t.Fields[field] = typ
				if !p.take(",") {
					if !p.take("}") {
						return Type{}, fmt.Errorf("separate object fields with commas and finish with }")
					}
					break
				}
			}
		}
	case "node":
		if !p.take("(") {
			return Type{}, fmt.Errorf("write a node type as node() gives U or node(T) gives U")
		}
		t.Fields = map[string]Type{}
		if !p.take(")") {
			if len(p.tokens) > 1 && p.tokens[1] == ":" {
				p.tokens = p.tokens[2:]
			}
			input, err := p.parse()
			if err != nil {
				return Type{}, err
			}
			t.Fields["open input"] = input
			if !p.take(")") {
				return Type{}, fmt.Errorf("a node type has at most one input; pass one object for several values")
			}
		}
		if !p.take("gives") {
			return Type{}, fmt.Errorf("give the node's single output type after gives")
		}
		result, err := p.parse()
		if err != nil {
			return Type{}, err
		}
		t.Fields["output"] = result
	default:
		if !t.variable() {
			return Type{}, fmt.Errorf("unsupported type %q; use a plain type, list, object, node signature, or capital placeholder letter", name)
		}
	}
	return t, nil
}

func (t *Type) UnmarshalText(data []byte) error {
	spaced := strings.NewReplacer("{", " { ", "}", " } ", "(", " ( ", ")", " ) ", ":", " : ", ",", " , ").Replace(string(data))
	p := typeParser{strings.Fields(spaced)}
	parsed, err := p.parse()
	if err == nil && len(p.tokens) != 0 {
		err = fmt.Errorf("unexpected %q after type; remove it", p.tokens[0])
	}
	if err == nil {
		*t = parsed
	}
	return err
}

func (t Type) String() string {
	switch {
	case t.Item != nil:
		return "list of " + t.Item.String()
	case t.Name == "node":
		return "node(" + t.Fields["open input"].String() + ") gives " + t.Fields["output"].String()
	case t.Fields != nil:
		fields := make([]string, 0, len(t.Fields))
		for _, name := range keys(t.Fields) {
			fields = append(fields, name+": "+t.Fields[name].String())
		}
		return "object{" + strings.Join(fields, ", ") + "}"
	}
	return t.Name
}

func (t Type) MarshalText() ([]byte, error) { return []byte(t.String()), nil }
func (t Type) variable() bool               { return len(t.Name) == 1 && t.Name[0] >= 'A' && t.Name[0] <= 'Z' }
func (t Type) substitute(types map[string]Type) Type {
	if t.variable() {
		if bound, ok := types[t.Name]; ok {
			return bound
		}
		return anyType
	}
	if t.Item != nil {
		item := t.Item.substitute(types)
		t.Item = &item
	}
	if t.Fields != nil {
		fields := map[string]Type{}
		for name, field := range t.Fields {
			fields[name] = field.substitute(types)
		}
		t.Fields = fields
	}
	return t
}

func (t Type) unchecked() bool {
	if t.Name == "any" || t.Name == "object" && t.Fields == nil {
		return true
	}
	if t.Item != nil && t.Item.unchecked() {
		return true
	}
	for _, field := range t.Fields {
		if field.unchecked() {
			return true
		}
	}
	return false
}

func (t Type) shape() any {
	if t.Name == "list" {
		return []any{*t.Item}
	}
	fields := Values{}
	for name, field := range t.Fields {
		fields[name] = field
	}
	return fields
}

func typeOf(value any) Type {
	switch value := value.(type) {
	case Type:
		return value
	case *Step:
		if value.contract != nil {
			return *value.contract
		}
		t := Type{Name: "node", Fields: map[string]Type{}}
		for name, port := range value.plan.Inputs {
			if _, filled := value.In[name]; !filled && port.Default == nil {
				t.Fields["open input"] = port.Type
			}
		}
		for _, port := range value.plan.Outputs {
			t.Fields["output"] = port.Type
		}
		return t
	case string:
		return textType
	case float64:
		return numberType
	case bool:
		return boolType
	case map[string]any:
		fields := map[string]Type{}
		for name, item := range value {
			fields[name] = typeOf(item)
		}
		return Type{Name: "object", Fields: fields}
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
