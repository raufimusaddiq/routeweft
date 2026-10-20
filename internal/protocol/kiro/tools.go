package kiro

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

const (
	// toolNameMax, toolDescriptionMax and toolIDMax are KIRO_TOOL_NAME_MAX_LENGTH,
	// KIRO_TOOL_DESCRIPTION_MAX_LENGTH and KIRO_TOOL_ID_MAX_LENGTH
	// (kiroConstants.js:23-25). They are the pin's figures; whether Kiro enforces
	// exactly these is not verifiable offline, so they are named here, once.
	toolNameMax        = 64
	toolDescriptionMax = 10237
	toolIDMax          = 64
)

var (
	validKiroName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	kiroNameStrip = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	validKiroID   = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	idStrip       = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
)

// toolNameRule is what Kiro accepts as a tool name (kiroConversation.js:22,
// :47-61). It is Strict: where the pin suffixes a name that is taken
// (`_2`), two names that are one name once normalized are refused. A name that
// is already valid is never rewritten (the pin also trims its underscores). The
// pin replaces per UTF-16 unit and Go per rune; the result is an opaque name the
// response maps back, so the difference is not visible to anyone.
var toolNameRule = concerns.ToolNameRule{
	Valid: validKiroName.MatchString,
	Sanitize: func(original string) string {
		cleaned := strings.Trim(kiroNameStrip.ReplaceAllString(strings.TrimSpace(original), "_"), "_")
		if cleaned == "" {
			cleaned = "tool"
		}
		if len(cleaned) > toolNameMax {
			cleaned = cleaned[:toolNameMax]
		}
		return cleaned
	},
	Max:    toolNameMax,
	Strict: true,
}

// Normalized counts what schema normalisation did, so each row of PARITY 6.8 has
// a number a test can assert.
type Normalized struct {
	// AdditionalPropertiesStripped counts additionalProperties true/false removed.
	AdditionalPropertiesStripped int
	// AdditionalPropertiesFalse is the subset that was false: a guarantee about
	// what the model returns, which Kiro cannot be given.
	AdditionalPropertiesFalse int
	// RequiredDropped counts required entries naming an undeclared property.
	RequiredDropped int
	// EmptyRequiredDropped counts required: [] removed.
	EmptyRequiredDropped int
}

// schemaMaps hold name→schema; their keys are names, never keywords.
var schemaMaps = map[string]bool{"properties": true, "patternProperties": true, "$defs": true, "definitions": true, "dependentSchemas": true, "dependencies": true}

// schemaOne hold one schema (or, for items, possibly a list of them).
var schemaOne = map[string]bool{"items": true, "additionalItems": true, "contains": true, "not": true, "if": true, "then": true, "else": true,
	"propertyNames": true, "contentSchema": true, "unevaluatedItems": true, "unevaluatedProperties": true}

// schemaList hold a list of schemas.
var schemaList = map[string]bool{"allOf": true, "anyOf": true, "oneOf": true, "prefixItems": true}

func clone(value any) any {
	switch v := value.(type) {
	case map[string]any:
		copied := make(map[string]any, len(v))
		for key, item := range v {
			copied[key] = clone(item)
		}
		return copied
	case []any:
		copied := make([]any, len(v))
		for i, item := range v {
			copied[i] = clone(item)
		}
		return copied
	}
	return value
}

// cleanSchema is cleanSchemaValue (kiroConversation.js:63-74) done by keyword,
// where the pin walks every key of every object. That walk also stripped a
// property that is named additionalProperties and edited enum, const and
// default values; here only schemas are visited. inherited preserves constraints
// whose properties may be declared by a parent, sibling or referencing schema.
func cleanSchema(schema map[string]any, path string, counts *Normalized, inherited bool) error {
	if raw, present := schema["additionalProperties"]; present {
		switch v := raw.(type) {
		case bool:
			counts.AdditionalPropertiesStripped++
			if !v {
				counts.AdditionalPropertiesFalse++
			}
			delete(schema, "additionalProperties")
		case map[string]any:
			return unsupported("%s: additionalProperties declares a schema, which Kiro cannot be told about", path)
		case nil:
			delete(schema, "additionalProperties")
		default:
			return unsupported("%s: additionalProperties must be a boolean or a schema", path)
		}
	}
	if raw, present := schema["required"]; present {
		list, isList := raw.([]any)
		if !isList {
			return unsupported("%s: required must be a list", path)
		}
		for _, entry := range list {
			if _, ok := entry.(string); !ok {
				return unsupported("%s: required entries must be strings", path)
			}
		}
		if len(list) == 0 {
			delete(schema, "required")
			counts.EmptyRequiredDropped++
		}
	}
	for key, value := range schema {
		switch {
		case schemaMaps[key]:
			children, isMap := value.(map[string]any)
			if !isMap {
				return unsupported("%s.%s must be an object", path, key)
			}
			for name, child := range children {
				if list, ok := child.([]any); key == "dependencies" && ok {
					for _, entry := range list {
						if _, ok := entry.(string); !ok {
							return unsupported("%s.dependencies.%s must be a schema or property-name list", path, name)
						}
					}
					continue
				}
				object, isObject := child.(map[string]any)
				if !isObject {
					if _, isBool := child.(bool); isBool {
						continue
					}
					return unsupported("%s.%s.%s must be a schema", path, key, name)
				}
				if err := cleanSchema(object, path+"."+key+"."+name, counts, key != "properties" && key != "patternProperties"); err != nil {
					return err
				}
			}
		case schemaOne[key]:
			switch v := value.(type) {
			case map[string]any:
				if err := cleanSchema(v, path+"."+key, counts, key == "not" || key == "if" || key == "then" || key == "else"); err != nil {
					return err
				}
			case bool:
			case []any:
				if key != "items" {
					return unsupported("%s.%s must be a schema", path, key)
				}
				for i, child := range v {
					if object, isObject := child.(map[string]any); isObject {
						if err := cleanSchema(object, fmt.Sprintf("%s.%s.%d", path, key, i), counts, false); err != nil {
							return err
						}
					} else if _, ok := child.(bool); !ok {
						return unsupported("%s.items.%d must be a schema", path, i)
					}
				}
			default:
				return unsupported("%s.%s must be a schema", path, key)
			}
		case schemaList[key]:
			list, isList := value.([]any)
			if !isList {
				return unsupported("%s.%s must be a list", path, key)
			}
			for i, child := range list {
				if object, isObject := child.(map[string]any); isObject {
					if err := cleanSchema(object, fmt.Sprintf("%s.%s.%d", path, key, i), counts, key != "prefixItems"); err != nil {
						return err
					}
				} else if _, ok := child.(bool); !ok {
					return unsupported("%s.%s.%d must be a schema", path, key, i)
				}
			}
		}
	}
	if list, present := schema["required"].([]any); present {
		properties := concerns.Object(schema["properties"])
		_, composed := schema["$ref"]
		composed = composed || inherited
		for _, key := range []string{"allOf", "anyOf", "oneOf"} {
			if schema[key] != nil {
				composed = true
			}
		}
		kept := []any{}
		seen := map[string]bool{}
		for _, entry := range list {
			text, isText := entry.(string)
			if !isText {
				return unsupported("%s: required entries must be strings", path)
			}
			if seen[text] {
				continue
			}
			seen[text] = true
			if _, declared := properties[text]; !declared && !composed {
				// The model cannot satisfy it either way (kiroConversation.js:83-85).
				counts.RequiredDropped++
				continue
			}
			kept = append(kept, text)
		}
		if len(kept) == 0 {
			delete(schema, "required")
		} else {
			schema["required"] = kept
		}
	}
	return nil
}

// rootSchema is normalizeRootSchema (kiroConversation.js:76-89): a Kiro tool
// input is an object. A missing type becomes "object" and missing properties
// become none, which say the same thing; any other root type is refused.
func rootSchema(raw any, name string, counts *Normalized) (map[string]any, error) {
	var schema map[string]any
	switch v := raw.(type) {
	case nil:
		schema = map[string]any{}
	case map[string]any:
		schema = clone(v).(map[string]any)
	default:
		return nil, unsupported("tool %q: parameters must be an object", name)
	}
	if err := cleanSchema(schema, "tool "+name+" parameters", counts, false); err != nil {
		return nil, err
	}
	if kind, present := schema["type"]; present && kind != "object" {
		return nil, unsupported("tool %q: parameters type %s is not an object", name, concerns.JSONText(kind))
	}
	schema["type"] = "object"
	if _, isMap := schema["properties"].(map[string]any); !isMap {
		if schema["properties"] != nil {
			return nil, unsupported("tool %q: parameters.properties must be an object", name)
		}
		schema["properties"] = map[string]any{}
	}

	return schema, nil
}

type toolSpec struct {
	original string
	name     string
	body     map[string]any
}

// toolSpecs builds the toolSpecification list (normalizeKiroToolSpecs,
// kiroConversation.js:92-123) and the name map. Where the pin skips a nameless
// tool, skips a second definition of a name, suffixes a name that is taken,
// truncates a description and rewrites a schema without telling anyone, each of
// those is refused here or counted in Normalized.
func toolSpecs(tools []any, historyNames []string, counts *Normalized) ([]toolSpec, *concerns.ToolNames, error) {
	var declared []string
	type definition struct {
		original string
		function map[string]any
	}
	var definitions []definition
	byName := map[string]bool{}
	for i, rawTool := range tools {
		tool, isObject := rawTool.(map[string]any)
		if !isObject {
			return nil, nil, unsupported("tools[%d] must be an object", i)
		}
		if kind := tool["type"]; kind != nil && kind != "function" {
			return nil, nil, unsupported("tools[%d]: type %s is not a function", i, concerns.JSONText(kind))
		}
		if err := requestKeys(tool, "tool", "type", "function"); err != nil {
			return nil, nil, err
		}
		function, isObject := tool["function"].(map[string]any)
		if !isObject {
			return nil, nil, unsupported("tools[%d]: function must be an object", i)
		}
		if err := requestKeys(function, "tool.function", "name", "description", "parameters", "strict"); err != nil {
			return nil, nil, err
		}
		if value := function["strict"]; value != nil && value != false {
			return nil, nil, unsupported("tool.function.strict: Kiro cannot guarantee schema adherence")
		}
		name, isText := function["name"].(string)
		if !isText || strings.TrimSpace(name) == "" {
			return nil, nil, unsupported("tools[%d]: a tool needs a name", i)
		}
		if byName[name] {
			return nil, nil, unsupported("tool %q is declared twice", name)
		}
		byName[name] = true
		declared = append(declared, name)
		definitions = append(definitions, definition{name, function})
	}
	declared = append(declared, historyNames...)
	names, err := concerns.NewToolNamesFor(toolNameRule, declared)
	if err != nil {
		return nil, nil, unsupported("%v", err)
	}
	var specs []toolSpec
	for _, def := range definitions {
		description := "Tool: " + def.original
		switch v := def.function["description"].(type) {
		case nil:
		case string:
			if v != "" {
				description = v
			}
		default:
			return nil, nil, unsupported("tool %q: description must be a string", def.original)
		}
		if utf8.RuneCountInString(description) > toolDescriptionMax {
			return nil, nil, unsupported("tool %q: description is %d characters, over Kiro's %d", def.original, utf8.RuneCountInString(description), toolDescriptionMax)
		}
		schema, err := rootSchema(def.function["parameters"], def.original, counts)
		if err != nil {
			return nil, nil, err
		}
		safe, err := names.SafeChecked(def.original)
		if err != nil {
			return nil, nil, unsupported("%v", err)
		}
		specs = append(specs, toolSpec{original: def.original, name: safe, body: map[string]any{"toolSpecification": map[string]any{
			"name": safe, "description": description, "inputSchema": map[string]any{"json": schema},
		}}})
	}
	return specs, names, nil
}

// sanitizeID is reserveToolId's cleaning (kiroConversation.js:208-223) without
// its suffixing: characters Kiro rejects are removed, and a result that is empty
// or too long is refused. Two different ids that clean to the same id are
// refused by the caller.
func sanitizeID(id string) (string, error) {
	cleaned := idStrip.ReplaceAllString(id, "")
	if cleaned == "" || !validKiroID.MatchString(cleaned) {
		return "", unsupported("tool call id %q has no usable characters", id)
	}
	if len(cleaned) > toolIDMax {
		return "", unsupported("tool call id %q is over %d characters", id, toolIDMax)
	}
	return cleaned, nil
}

// strictArguments is the input of a history tool call: a JSON object, or empty
// for a tool that takes none. concerns.ToolInput turns anything else into {};
// that would send the model a call it never made.
func strictArguments(value any) (map[string]any, error) {
	switch v := value.(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return v, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return map[string]any{}, nil
		}
		object, err := concerns.WireObject([]byte(v))
		if err != nil {
			return nil, unsupported("tool call arguments are not a JSON object")
		}
		return object, nil
	case json.Number:
	}
	return nil, unsupported("tool call arguments are not a JSON object")
}
