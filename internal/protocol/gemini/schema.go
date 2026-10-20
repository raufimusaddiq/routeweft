package gemini

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// unsupportedKeywords are the JSON Schema keywords Gemini's schema proto has no
// field for; one occurrence fails the whole request with "Unknown name ...:
// Cannot find field" (translator/formats/gemini.js:8-44). The pin deletes them
// by name at every depth, which also deletes a tool PARAMETER called title,
// format, default or description-less "const": the model is then never told the
// parameter exists. Here they are keywords only where a keyword can stand, never
// the name of an entry of properties (PARITY 6.6 G9).
var unsupportedKeywords = map[string]bool{
	"minLength": true, "maxLength": true, "exclusiveMinimum": true, "exclusiveMaximum": true,
	"minItems": true, "maxItems": true, "format": true, "multipleOf": true,
	"uniqueItems": true, "contains": true,
	"unevaluatedProperties": true, "unevaluatedItems": true, "contentSchema": true,
	"prefixItems": true, "additionalItems": true,
	"default": true, "examples": true,
	"$schema": true, "$defs": true, "definitions": true, "const": true, "$ref": true, "$comment": true,
	"deprecated": true, "readOnly": true, "writeOnly": true,
	"additionalProperties": true, "propertyNames": true, "patternProperties": true, "enumDescriptions": true,
	"anyOf": true, "oneOf": true, "allOf": true, "not": true,
	"dependencies": true, "dependentSchemas": true, "dependentRequired": true,
	"title": true, "optional": true, "if": true, "then": true, "else": true, "contentMediaType": true, "contentEncoding": true,
	"cornerRadius": true, "fillColor": true, "fontFamily": true, "fontSize": true, "fontWeight": true,
	"gap": true, "padding": true, "strokeColor": true, "strokeThickness": true, "textColor": true,
	"errorMessage": true, "errorMessages": true, "x-errorMessage": true, "x-errorMessages": true,
	"markdownDescription": true, "x-intellij-html-description": true,
	"x-taplo-info": true, "x-taplo": true, "doNotSuggest": true, "suggestSortText": true,
	"minProperties": true, "maxProperties": true,
}

// maxRefDepth bounds how deep a local $ref is inlined; a schema that needs more
// (a recursive one) cannot be sent, because Gemini has no reference.
const maxRefDepth = 8

// CleanSchema is cleanJSONSchemaForAntigravity (translator/formats/gemini.js:358-447)
// minus the placeholder phase: it returns a copy of schema narrowed to the subset
// Gemini accepts. It returns nil for a schema that declares nothing (no
// properties), so the caller omits `parameters` (PARITY 6.6 G5: the pin's
// injected `reason` parameter makes the model return an argument the client never
// declared). A local $ref is inlined where the pin drops it; a reference it
// cannot inline is an error. Only the Gemini-specific narrowing the pin also does
// is silent: union types keep their best member, allOf merges properties and
// required, and the constraint keywords above are removed.
func CleanSchema(schema any) (map[string]any, error) {
	root, _ := deepCopy(schema).(map[string]any)
	if root == nil {
		return nil, nil
	}
	resolved, err := inlineRefs(root, root, 0)
	if err != nil {
		return nil, err
	}
	node, _ := resolved.(map[string]any)
	cleanNode(node)
	if len(concerns.Object(node["properties"])) == 0 {
		return nil, nil
	}
	return node, nil
}

func deepCopy(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = deepCopy(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = deepCopy(item)
		}
		return out
	}
	return value
}

// inlineRefs replaces every local $ref with a copy of its target.
func inlineRefs(node any, root map[string]any, depth int) (any, error) {
	switch v := node.(type) {
	case []any:
		for i, item := range v {
			resolved, err := inlineRefs(item, root, depth)
			if err != nil {
				return nil, err
			}
			v[i] = resolved
		}
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok {
			target, found := lookupRef(root, ref)
			if !found {
				return nil, fmt.Errorf("schema $ref %q is not a local reference that can be inlined", ref)
			}
			if depth >= maxRefDepth {
				return nil, fmt.Errorf("schema $ref %q is recursive; Gemini has no reference to express it", ref)
			}
			inlined, err := inlineRefs(deepCopy(target), root, depth+1)
			if err != nil {
				return nil, err
			}
			merged, _ := inlined.(map[string]any)
			if merged == nil {
				return nil, fmt.Errorf("schema $ref %q does not point at a schema", ref)
			}
			for key, item := range v {
				if key != "$ref" {
					if _, taken := merged[key]; !taken {
						merged[key] = item
					}
				}
			}
			return merged, nil
		}
		for key, item := range v {
			if key == "$defs" || key == "definitions" {
				continue
			}
			resolved, err := inlineRefs(item, root, depth)
			if err != nil {
				return nil, err
			}
			v[key] = resolved
		}
	}
	return node, nil
}

func lookupRef(root map[string]any, ref string) (any, bool) {
	path, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return nil, false
	}
	var current any = root
	for _, segment := range strings.Split(path, "/") {
		segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
		object, isObject := current.(map[string]any)
		if !isObject {
			return nil, false
		}
		next, present := object[segment]
		if !present {
			return nil, false
		}
		current = next
	}
	return current, true
}

// cleanNode narrows one schema node and the schemas under it (properties, items).
func cleanNode(node map[string]any) {
	if node == nil {
		return
	}
	if constant, ok := node["const"]; ok && node["enum"] == nil {
		node["enum"] = []any{constant}
	}
	if values, ok := node["enum"].([]any); ok {
		for i, value := range values {
			values[i] = enumText(value)
		}
		if node["type"] == nil {
			node["type"] = "string"
		}
	}
	mergeAllOf(node)
	if prefix, ok := node["prefixItems"].([]any); ok && len(prefix) > 0 && node["items"] == nil {
		if variants := nonNull(prefix); len(variants) > 0 {
			node["items"] = variants[bestSchema(variants)]
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		for guard := 0; guard < maxRefDepth; guard++ {
			variants := nonNull(concerns.Items(node[keyword]))
			if len(variants) == 0 {
				break
			}
			delete(node, keyword)
			for key, value := range variants[bestSchema(variants)] {
				node[key] = value
			}
		}
	}
	if types, ok := node["type"].([]any); ok {
		node["type"] = "string"
		for _, candidate := range types {
			if text, isText := candidate.(string); isText && text != "null" {
				node["type"] = text
				break
			}
		}
	}
	if node["properties"] != nil && node["type"] == nil {
		node["type"] = "object"
	}
	if items, ok := node["items"].([]any); ok {
		if variants := nonNull(items); len(variants) > 0 {
			node["items"] = variants[bestSchema(variants)]
		} else {
			delete(node, "items")
		}
	}
	if node["type"] == "array" && node["items"] == nil {
		node["items"] = map[string]any{"type": "string"}
	}
	for key := range node {
		if unsupportedKeywords[key] || strings.HasPrefix(key, "x-") {
			delete(node, key)
		}
	}
	properties := concerns.Object(node["properties"])
	for _, child := range properties {
		cleanNode(concerns.Object(child))
	}
	cleanNode(concerns.Object(node["items"]))
	if required, ok := node["required"].([]any); ok && properties != nil {
		kept := make([]any, 0, len(required))
		for _, name := range required {
			if text, isText := name.(string); isText {
				if _, present := properties[text]; present {
					kept = append(kept, name)
				}
			}
		}
		if len(kept) == 0 {
			delete(node, "required")
		} else {
			node["required"] = kept
		}
	}
}

func enumText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case nil:
		return "null"
	}
	return concerns.JSONText(value)
}

func mergeAllOf(node map[string]any) {
	parts, ok := node["allOf"].([]any)
	if !ok {
		return
	}
	properties := concerns.Object(node["properties"])
	required, _ := node["required"].([]any)
	for _, raw := range parts {
		part := concerns.Object(raw)
		for name, value := range concerns.Object(part["properties"]) {
			if properties == nil {
				properties = map[string]any{}
			}
			properties[name] = value
		}
		for _, name := range concerns.Items(part["required"]) {
			duplicate := false
			for _, seen := range required {
				duplicate = duplicate || seen == name
			}
			if !duplicate {
				required = append(required, name)
			}
		}
	}
	delete(node, "allOf")
	if properties != nil {
		node["properties"] = properties
	}
	if len(required) > 0 {
		node["required"] = required
	}
}

func nonNull(schemas []any) []map[string]any {
	var kept []map[string]any
	for _, raw := range schemas {
		if schema := concerns.Object(raw); schema != nil && schema["type"] != "null" {
			kept = append(kept, schema)
		}
	}
	return kept
}

// bestSchema is selectBest (gemini.js:239-264): an object beats an array beats a
// scalar; the first of equals wins.
func bestSchema(schemas []map[string]any) int {
	best, bestScore := 0, -1
	for i, schema := range schemas {
		score := 0
		switch kind, _ := schema["type"].(string); {
		case kind == "object" || schema["properties"] != nil:
			score = 3
		case kind == "array" || schema["items"] != nil:
			score = 2
		case kind != "" && kind != "null":
			score = 1
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	return best
}
