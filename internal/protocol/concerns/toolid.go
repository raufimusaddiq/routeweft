package concerns

import (
	"fmt"
	"regexp"
)

// An Anthropic tool_use id must match this (concerns/toolCall.js:5).
var toolIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
var toolIDStrip = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// GenerateToolCallID is the deterministic, cache-friendly id for a call that
// arrived without a usable one (concerns/toolCall.js:14-17).
func GenerateToolCallID(msgIndex, callIndex int, name string) string {
	suffix := ""
	if name != "" {
		suffix = "_" + toolIDStrip.ReplaceAllString(name, "")
	}
	return fmt.Sprintf("call_msg%d_tc%d%s", msgIndex, callIndex, suffix)
}

// repairToolID keeps a valid id, strips an invalid one, and regenerates when
// nothing is left (concerns/toolCall.js:36-39). Both halves of a tool call run
// through this, so the id a result names still matches the call it answers.
func repairToolID(id any, generate func() string) any {
	text, isText := id.(string)
	if isText && toolIDPattern.MatchString(text) {
		return id
	}
	if !isText && Truthy(id) {
		return id
	}
	if stripped := toolIDStrip.ReplaceAllString(text, ""); stripped != "" {
		return stripped
	}
	return generate()
}

// EnsureToolCallIDs gives every tool call and tool result in a message list a
// valid id and string arguments (concerns/toolCall.js:27-74), in both the Chat
// and the Messages shape. A call with no id gets a generated one; a result with
// no id answers the oldest call of the preceding assistant turn that is still
// unanswered, and only gets a generated id when there is none to answer. (The
// pin repairs an invalid id but leaves an absent one; PARITY V16.) It mutates body.
func EnsureToolCallIDs(body map[string]any) {
	var pending []string
	answer := func(id any, generate func() string) any {
		if Truthy(id) {
			repaired := repairToolID(id, generate)
			for k, candidate := range pending {
				if candidate == repaired {
					pending = append(pending[:k:k], pending[k+1:]...)
					break
				}
			}
			return repaired
		}
		if len(pending) > 0 {
			first := pending[0]
			pending = pending[1:]
			return first
		}
		return generate()
	}
	for i, raw := range Items(body["messages"]) {
		message := Object(raw)
		if message == nil {
			continue
		}
		if Text(message["role"]) == "assistant" {
			pending = nil
			for j, rawCall := range Items(message["tool_calls"]) {
				call := Object(rawCall)
				if call == nil {
					continue
				}
				function := Object(call["function"])
				name := Text(function["name"])
				call["id"] = repairToolID(call["id"], func() string { return GenerateToolCallID(i, j, name) })
				pending = append(pending, Text(call["id"]))
				if !Truthy(call["type"]) {
					call["type"] = "function"
				}
				if arguments := function["arguments"]; Truthy(arguments) {
					if _, isText := arguments.(string); !isText {
						function["arguments"] = JSONText(arguments)
					}
				}
			}
		}
		if Text(message["role"]) == "tool" {
			message["tool_call_id"] = answer(message["tool_call_id"], func() string { return GenerateToolCallID(i, 0, "") })
		}
		for k, rawBlock := range Items(message["content"]) {
			block := Object(rawBlock)
			if block == nil {
				continue
			}
			switch Text(block["type"]) {
			case "tool_use":
				block["id"] = repairToolID(block["id"], func() string { return GenerateToolCallID(i, k, Text(block["name"])) })
				if Text(message["role"]) == "assistant" {
					pending = append(pending, Text(block["id"]))
				}
			case "tool_result":
				block["tool_use_id"] = answer(block["tool_use_id"], func() string { return GenerateToolCallID(i, k, "") })
			}
		}
	}
}

func assistantToolCallIDs(message map[string]any) []string {
	if Text(message["role"]) != "assistant" {
		return nil
	}
	var ids []string
	for _, rawCall := range Items(message["tool_calls"]) {
		if id := Text(Object(rawCall)["id"]); id != "" {
			ids = append(ids, id)
		}
	}
	for _, rawBlock := range Items(message["content"]) {
		block := Object(rawBlock)
		if Text(block["type"]) == "tool_use" {
			if id := Text(block["id"]); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func answersToolCalls(message map[string]any, ids []string) bool {
	if message == nil || len(ids) == 0 {
		return false
	}
	has := func(id string) bool {
		for _, candidate := range ids {
			if candidate == id {
				return true
			}
		}
		return false
	}
	role := Text(message["role"])
	if role == "tool" && Truthy(message["tool_call_id"]) {
		return has(Text(message["tool_call_id"]))
	}
	if role == "user" {
		for _, rawBlock := range Items(message["content"]) {
			block := Object(rawBlock)
			if Text(block["type"]) == "tool_result" && has(Text(block["tool_use_id"])) {
				return true
			}
		}
	}
	return false
}

// FixMissingToolResponses inserts an empty `tool` message for each call of an
// assistant turn whose next message answers none of them
// (concerns/toolCall.js:123-154). The oracle skips it when the target is Kiro.
func FixMissingToolResponses(body map[string]any) {
	messages := Items(body["messages"])
	if messages == nil {
		return
	}
	fixed := make([]any, 0, len(messages))
	for i, raw := range messages {
		fixed = append(fixed, raw)
		ids := assistantToolCallIDs(Object(raw))
		if len(ids) == 0 || i+1 >= len(messages) {
			continue
		}
		if answersToolCalls(Object(messages[i+1]), ids) {
			continue
		}
		for _, id := range ids {
			fixed = append(fixed, map[string]any{"role": "tool", "tool_call_id": id, "content": ""})
		}
	}
	body["messages"] = fixed
}
