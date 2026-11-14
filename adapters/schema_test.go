package adapters

import (
	"encoding/json"
	"testing"
)

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
