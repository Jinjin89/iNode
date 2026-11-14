// Package adapters holds what adapter programs share: schema projection and the host's three messages.
package adapters

import (
	"fmt"
	"mime"
	"slices"
	"strings"
)

type Object = map[string]any

// Ports projects each property, escaping its name and preserving whether it may be absent.
func Ports(schema Object) Object {
	ports := Object{}
	properties, _ := schema["properties"].(Object)
	required, _ := schema["required"].([]any)
	for field, value := range properties {
		var port any = Project(value)
		if !slices.Contains(required, any(field)) {
			port = Object{"type": port, "optional": true}
		}
		ports[Escape(field)] = port
	}
	return ports
}

func closed(schema Object) bool {
	properties, _ := schema["properties"].(Object)
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

func Project(value any) string { return project(value, 0) }

func project(value any, depth int) string {
	schema, _ := value.(Object)
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
		properties, _ := schema["properties"].(Object)
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

func Escape(name string) string {
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

func Extension(mediaType string) string {
	if extensions, _ := mime.ExtensionsByType(mediaType); len(extensions) > 0 {
		return extensions[0]
	}
	return ".bin"
}
