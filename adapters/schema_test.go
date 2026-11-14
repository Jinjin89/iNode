package adapters

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParameterNotesPreservePorts(t *testing.T) {
	property := Object{"type": "string", "description": "A choice.\n Pick one.", "enum": []any{"ready", "pending"}}
	for _, schema := range []Object{
		{"properties": Object{"state": property, "quiet": Object{"type": "number"}}, "required": []any{"state"}},
		{"properties": Object{"state": Object{"schema": Object{"type": "string", "enum": property["enum"]}, "description": property["description"], "required": true}, "quiet": Object{"schema": Object{"type": "number"}}}},
	} {
		ports, doc := Describe(schema, "Choose a state.")
		want := Object{"state": "text", "quiet": Object{"type": "number", "optional": true}}
		if !reflect.DeepEqual(ports, want) || doc != "Choose a state.\nstate: A choice. Pick one. (one of [ready pending])" {
			t.Fatalf("ports or notes: %#v, %q", ports, doc)
		}
	}
	ports, doc := Describe(Object{}, "")
	if len(ports) != 0 || doc != "No description supplied." {
		t.Fatalf("a tool without a description must still load: %#v, %q", ports, doc)
	}
}

func TestProjection(t *testing.T) {
	for _, test := range []struct {
		schema, want string
		named        bool
	}{
		{`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`, "object{text: text}", true},
		{`{"type":"object","additionalProperties":false}`, "object{}", true},
		{`{"type":"object","properties":{"text":{"type":"string"}},"additionalProperties":false}`, "object", false},
		{`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`, "object", false},
		{`{"type":"object","properties":{"a.b":{"type":"string"}},"required":["a.b"],"additionalProperties":false}`, "object", false},
		{`{"type":"array","items":{"type":"object","properties":{"ready":{"type":"boolean"}},"required":["ready"],"additionalProperties":false}}`, "list of object{ready: bool}", false},
		{`{"type":"string","enum":["ready"]}`, "text", false},
		{`{"type":"integer","minimum":1}`, "number", false},
		{`{"type":"object","properties":{"猫":{"type":"string"}},"required":["猫"],"additionalProperties":false}`, "object{猫: text}", true},
		{`{"type":"array","prefixItems":[{"type":"number"}],"items":{"type":"string"}}`, "any", false},
		{`{"type":["string","null"]}`, "any", false},
		{`{"oneOf":[{"type":"string"},{"type":"number"}]}`, "any", false},
		{`{"$ref":"https://example.invalid/schema"}`, "any", false},
		{`{"$ref":"#/$defs/recursive","$defs":{}}`, "any", false},
	} {
		var schema Object
		if err := json.Unmarshal([]byte(test.schema), &schema); err != nil {
			t.Fatal(err)
		}
		if got := project(schema, 0); got != test.want || closed(schema) != test.named {
			t.Fatalf("%s: %s, named=%t", test.schema, got, closed(schema))
		}
	}
	if Escape("a.b=%猫") != "a%2Eb%3D%25%E7%8C%AB" || Escape("a%2Eb") == Escape("a.b") {
		t.Fatal("tool names must escape reversibly without collisions")
	}
}
