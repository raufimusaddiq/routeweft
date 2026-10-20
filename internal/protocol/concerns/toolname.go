package concerns

import (
	"fmt"
	"regexp"
	"strings"
)

const maxToolNameLen = 128

var validToolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var toolNameStrip = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// ToolNameRule is what an upstream accepts as a tool name. Valid says whether a
// name can be sent as it is, Sanitize derives the name to send otherwise, and
// Max is the longest name, which bounds the numeric suffix a non-strict rule
// appends to stay unique. A Strict rule never suffixes: when two names would be
// the same name after normalization, the request is refused, because a tool the
// model cannot be called back by under the name the client declared is a broken
// answer, not a served one.
type ToolNameRule struct {
	Valid    func(string) bool
	Sanitize func(string) string
	Max      int
	Strict   bool
}

// defaultToolNameRule is the rule of the Messages legs (concerns/toolNames.js).
var defaultToolNameRule = ToolNameRule{
	Valid: validToolName.MatchString,
	Sanitize: func(original string) string {
		base := toolNameStrip.ReplaceAllString(original, "_")
		if len(base) > maxToolNameLen {
			base = base[:maxToolNameLen]
		}
		if base == "" {
			base = "tool"
		}
		return base
	},
	Max: maxToolNameLen,
}

// ToolNames rewrites tool names an upstream rejects (MCP "server.tool") and maps
// them back on the response (concerns/toolNames.js). Names that are already
// valid are reserved first so an alias can never collide with a real tool.
type ToolNames struct {
	rule       ToolNameRule
	used       map[string]bool
	toOriginal map[string]string
	toSafe     map[string]string
}

// NewToolNames reserves the valid names among the declared ones.
func NewToolNames(declared []string) *ToolNames {
	names, _ := NewToolNamesFor(defaultToolNameRule, declared)
	return names
}

// NewToolNamesFor reserves the valid names among the declared ones, then
// rewrites each invalid one. Under a Strict rule it returns an error naming the
// first two names that collide; under any other rule it never fails.
func NewToolNamesFor(rule ToolNameRule, declared []string) (*ToolNames, error) {
	names := &ToolNames{rule: rule, used: map[string]bool{}, toOriginal: map[string]string{}, toSafe: map[string]string{}}
	for _, name := range declared {
		if rule.Valid(name) {
			names.used[name] = true
		}
	}
	for _, name := range declared {
		if _, err := names.SafeChecked(name); err != nil {
			return names, err
		}
	}
	return names, nil
}

// Safe returns the upstream-acceptable name for original. Under a Strict rule a
// collision is an error from SafeChecked, which such a caller must use instead.
func (n *ToolNames) Safe(original string) string {
	safe, _ := n.SafeChecked(original)
	return safe
}

// SafeChecked is Safe with the collision error of a Strict rule.
func (n *ToolNames) SafeChecked(original string) (string, error) {
	if n.rule.Valid(original) {
		return original, nil
	}
	if safe, ok := n.toSafe[original]; ok {
		return safe, nil
	}
	base := n.rule.Sanitize(original)
	candidate := base
	if n.used[candidate] {
		if n.rule.Strict {
			other := n.toOriginal[candidate]
			if other == "" {
				other = candidate
			}
			return "", fmt.Errorf("tool names %q and %q are the same name once normalized (%q)", original, other, candidate)
		}
		for suffix := 2; n.used[candidate]; suffix++ {
			tail := fmt.Sprintf("_%d", suffix)
			head := base
			if len(head) > n.rule.Max-len(tail) {
				head = head[:n.rule.Max-len(tail)]
			}
			candidate = head + tail
		}
	}
	n.used[candidate] = true
	n.toOriginal[candidate] = original
	n.toSafe[original] = candidate
	return candidate, nil
}

// Original maps an upstream name back to the one the client declared. A name the
// sanitiser never rewrote is returned unchanged.
func (n *ToolNames) Original(safe string) string {
	if n != nil {
		if original, ok := n.toOriginal[safe]; ok {
			return original
		}
	}
	return safe
}

// Rewritten reports whether any name was rewritten.
func (n *ToolNames) Rewritten() bool { return n != nil && len(n.toOriginal) > 0 }

// ChatToolNames lists every tool name a Chat request mentions: declarations,
// assistant history and an explicit tool_choice (concerns/toolNames.js:32-39).
func ChatToolNames(body map[string]any) []string {
	var names []string
	add := func(value any) {
		if name, ok := value.(string); ok {
			if name = strings.TrimSpace(name); name != "" {
				names = append(names, name)
			}
		}
	}
	for _, rawTool := range Items(body["tools"]) {
		tool := Object(rawTool)
		if function := Object(tool["function"]); function != nil && function["name"] != nil {
			add(function["name"])
		} else {
			add(tool["name"])
		}
	}
	for _, rawMessage := range Items(body["messages"]) {
		for _, rawCall := range Items(Object(rawMessage)["tool_calls"]) {
			add(Object(Object(rawCall)["function"])["name"])
		}
	}
	choice := Object(body["tool_choice"])
	add(Object(choice["function"])["name"])
	add(choice["name"])
	return names
}
