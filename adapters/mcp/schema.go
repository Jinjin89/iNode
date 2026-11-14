package mcp

import (
	"fmt"
	"slices"
	"strings"
)

type object = map[string]any

func closed(schema object) bool {
	properties, _ := schema["properties"].(object)
	required, _ := schema["required"].([]any)
	if schema["type"] != "object" || schema["additionalProperties"] != false || len(required) != len(properties) {
		return false
	}
	for _, key := range []string{"$ref", "anyOf", "oneOf", "allOf", "if", "patternProperties", "dependentSchemas"} {
		if _, ok := schema[key]; ok {
			return false
		}
	}
	seen := map[string]bool{}
	for _, key := range required {
		name, ok := key.(string)
		if !ok || name == "" || strings.ContainsAny(name, ".={}():,") || len(strings.Fields(name)) != 1 || strings.TrimSpace(name) != name || seen[name] || properties[name] == nil {
			return false
		}
		seen[name] = true
	}
	return true
}

func project(value any, depth int) string {
	schema, _ := value.(object)
	if depth > 32 || schema["$ref"] != nil || schema["anyOf"] != nil || schema["oneOf"] != nil || schema["allOf"] != nil || schema["prefixItems"] != nil {
		return "any"
	}
	switch schema["type"] {
	case "string":
		return "text"
	case "integer", "number":
		return "number"
	case "boolean":
		return "bool"
	case "array":
		return "list of " + project(schema["items"], depth+1)
	case "object":
		if !closed(schema) {
			return "object"
		}
		properties, _ := schema["properties"].(object)
		fields := []string{}
		for name, typ := range properties {
			fields = append(fields, name+": "+project(typ, depth+1))
		}
		slices.Sort(fields)
		return "object{" + strings.Join(fields, ", ") + "}"
	}
	return "any"
}

func plain(name string) bool {
	return name != "" && strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == ""
}

func escape(name string) string {
	var result strings.Builder
	for _, c := range []byte(name) {
		if plain(string(c)) {
			result.WriteByte(c)
		} else {
			fmt.Fprintf(&result, "%%%02X", c)
		}
	}
	return result.String()
}
