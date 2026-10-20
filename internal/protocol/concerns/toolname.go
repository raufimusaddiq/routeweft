package concerns

import (
	"fmt"
	"regexp"
	"strings"
)

const maxToolNameLen = 128

var validToolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var toolNameStrip = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// ToolNames rewrites tool names an upstream rejects (MCP "server.tool") and maps
// them back on the response (concerns/toolNames.js). Names that are already
// valid are reserved first so an alias can never collide with a real tool.
type ToolNames struct {
	used       map[string]bool
	toOriginal map[string]string
	toSafe     map[string]string
}

// NewToolNames reserves the valid names among the declared ones.
func NewToolNames(declared []string) *ToolNames {
	names := &ToolNames{used: map[string]bool{}, toOriginal: map[string]string{}, toSafe: map[string]string{}}
	for _, name := range declared {
		if validToolName.MatchString(name) {
			names.used[name] = true
		}
	}
	return names
}

// Safe returns the upstream-acceptable name for original.
func (n *ToolNames) Safe(original string) string {
	if validToolName.MatchString(original) {
		return original
	}
	if safe, ok := n.toSafe[original]; ok {
		return safe
	}
	base := toolNameStrip.ReplaceAllString(original, "_")
	if len(base) > maxToolNameLen {
		base = base[:maxToolNameLen]
	}
	if base == "" {
		base = "tool"
	}
	candidate := base
	for suffix := 2; n.used[candidate]; suffix++ {
		tail := fmt.Sprintf("_%d", suffix)
		head := base
		if len(head) > maxToolNameLen-len(tail) {
			head = head[:maxToolNameLen-len(tail)]
		}
		candidate = head + tail
	}
	n.used[candidate] = true
	n.toOriginal[candidate] = original
	n.toSafe[original] = candidate
	return candidate
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
