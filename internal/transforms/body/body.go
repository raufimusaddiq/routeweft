package body

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	"github.com/raufimusaddiq/routeweft/internal/protocol/ollama"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/protocol/systemone"
	"github.com/raufimusaddiq/routeweft/internal/transforms"
	"github.com/raufimusaddiq/routeweft/internal/transforms/caveman"
	"github.com/raufimusaddiq/routeweft/internal/transforms/headroom"
	"github.com/raufimusaddiq/routeweft/internal/transforms/ponytail"
	"github.com/raufimusaddiq/routeweft/internal/transforms/pxpipe"
	"github.com/raufimusaddiq/routeweft/internal/transforms/rtk"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

var publicClient = transport.NewSSRFProtectedClient()
var localClient = transport.NewTrustedLocalSSRFProtectedClient()

type Pipeline struct {
	public, local transforms.Pipeline
}

// Compile freezes saver settings with the routing snapshot, not per attempt.
//
// observePXPIPE, when given, receives every PXPIPE attempt's diagnostic. It is
// supplied by the process, not the snapshot, so counters survive republishing.
func Compile(settings map[string]string, observePXPIPE ...func(pxpipe.Diagnostic)) *Pipeline {
	var observer func(pxpipe.Diagnostic)
	if len(observePXPIPE) > 0 {
		observer = observePXPIPE[0]
	}
	build := func(local bool) transforms.Pipeline {
		var steps []transforms.Transform
		client := publicClient
		if local {
			client = localClient
		}
		if settings["rtkEnabled"] == "true" {
			steps = append(steps, rtk.Transform{Enabled: true})
		}
		if settings["headroomEnabled"] == "true" {
			steps = append(steps, headroom.Transform{URL: settings["headroomUrl"], Timeout: milliseconds(settings["headroomTimeoutMs"]), CompressUserMessages: settings["headroomCompressUserMessages"] == "true", Client: client, AllowPrivate: local})
		}
		if settings["cavemanEnabled"] == "true" {
			steps = append(steps, caveman.Transform{Enabled: true, Level: settings["cavemanLevel"]})
		}
		if settings["ponytailEnabled"] == "true" {
			steps = append(steps, ponytail.Transform{Enabled: true, Level: settings["ponytailLevel"]})
		}
		if settings["pxpipeEnabled"] == "true" {
			min, _ := strconv.Atoi(settings["pxpipeMinChars"])
			steps = append(steps, pxpipe.Transform{Enabled: true, URL: settings["pxpipeUrl"], MinChars: min, Timeout: milliseconds(settings["pxpipeTimeoutMs"]), Client: client, AllowPrivate: local, Diagnostics: observer})
		}
		return transforms.Pipeline{Steps: steps}
	}
	return &Pipeline{public: build(false), local: build(true)}
}

func milliseconds(raw string) time.Duration {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 || n > int64((1<<63-1)/time.Millisecond) {
		return 0
	}
	return time.Duration(n) * time.Millisecond
}

func (p *Pipeline) Run(ctx context.Context, protocol string, raw []byte, allowPrivate bool) ([]byte, error) {
	if p == nil {
		return raw, nil
	}
	pipeline := p.public
	if allowPrivate {
		pipeline = p.local
	}
	if len(pipeline.Steps) == 0 {
		return raw, nil
	}
	return Run(ctx, protocol, raw, pipeline)
}

// Run edits known text slots only; media, tools, thinking and unknown fields stay opaque.
func Run(ctx context.Context, protocol string, raw []byte, pipeline transforms.Pipeline) ([]byte, error) {
	if protocol == systemone.Protocol {
		// Typed state/questions are data, not a prompt-policy injection point.
		return raw, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return raw, errors.New("transform body must be a JSON object")
	}
	v := view{request: transforms.Request{System: []string{}, Messages: []transforms.Message{}}}
	var inject func([]string)
	switch protocol {
	case openai.ChatProtocol, ollama.Protocol:
		v.messages(fields["messages"], "content")
		inject = func(policies []string) {
			messages, _ := fields["messages"].([]any)
			fields["messages"] = append([]any{map[string]any{"role": "system", "content": strings.Join(policies, "\n\n")}}, messages...)
		}
	case openai.ResponsesProtocol:
		if instructions := fields["instructions"]; instructions != nil {
			if _, ok := instructions.(string); !ok {
				return raw, nil
			}
		}
		v.text(fields, "instructions", "system", false)
		if _, ok := fields["input"].(string); ok {
			v.text(fields, "input", "user", false)
		} else {
			v.messages(fields["input"], "content")
		}
		inject = func(policies []string) {
			text, _ := fields["instructions"].(string)
			fields["instructions"] = joinPolicy(policies, text)
		}
	case anthropic.MessagesProtocol:
		switch fields["system"].(type) {
		case nil, string, []any:
		default:
			return raw, nil
		}
		v.content(fields, "system", "system", false)
		v.messages(fields["messages"], "content")
		inject = func(policies []string) {
			var blocks []any
			for _, policy := range policies {
				blocks = append(blocks, map[string]any{"type": "text", "text": policy})
			}
			switch system := fields["system"].(type) {
			case string:
				blocks = append(blocks, map[string]any{"type": "text", "text": system})
			case []any:
				blocks = append(blocks, system...)
			}
			fields["system"] = blocks
		}
	case gemini.Protocol:
		key := "systemInstruction"
		if _, ok := fields["system_instruction"]; ok {
			key = "system_instruction"
		}
		if system, ok := fields[key].(map[string]any); ok {
			if parts := system["parts"]; parts != nil {
				if _, ok := parts.([]any); !ok {
					return raw, nil
				}
			}
			v.content(system, "parts", "system", false)
		} else if fields[key] != nil {
			return raw, nil
		}
		v.messages(fields["contents"], "parts")
		inject = func(policies []string) {
			system, _ := fields[key].(map[string]any)
			if system == nil {
				system = map[string]any{}
				fields[key] = system
			}
			parts, _ := system["parts"].([]any)
			system["parts"] = append([]any{map[string]any{"text": strings.Join(policies, "\n\n")}}, parts...)
		}
	default:
		return raw, nil
	}
	updated := pipeline.RunContext(ctx, v.request)
	if reflect.DeepEqual(updated, v.request) {
		return raw, nil
	}
	var policies, system []string
	for _, text := range updated.System {
		isNewPolicy := false
		for _, marker := range []string{caveman.Marker, ponytail.Marker} {
			if strings.HasPrefix(text, marker) && !containsMarker(v.request.System, marker) {
				isNewPolicy = true
			}
		}
		if isNewPolicy {
			policies = append(policies, text)
		} else {
			system = append(system, text)
		}
	}
	if err := transforms.ValidateReplacement(v.request, system, updated.Messages); err != nil {
		return raw, err
	}
	for i, slot := range v.systemSlots {
		slot(system[i])
	}
	for i, slot := range v.messageSlots {
		slot(updated.Messages[i].Content)
	}
	if len(policies) > 0 {
		inject(policies)
	}
	return json.Marshal(fields)
}

func joinPolicy(policies []string, text string) string {
	if text != "" {
		policies = append(policies, text)
	}
	return strings.Join(policies, "\n\n")
}

func containsMarker(system []string, marker string) bool {
	for _, text := range system {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

type view struct {
	request                   transforms.Request
	systemSlots, messageSlots []func(string)
}

func (v *view) text(object map[string]any, key, role string, tool bool) {
	text, ok := object[key].(string)
	if !ok {
		return
	}
	set := func(value string) { object[key] = value }
	if role == "system" || role == "developer" {
		v.request.System = append(v.request.System, text)
		v.systemSlots = append(v.systemSlots, set)
	} else {
		v.request.Messages = append(v.request.Messages, transforms.Message{Role: role, Content: text, ToolResult: tool})
		v.messageSlots = append(v.messageSlots, set)
	}
}

func (v *view) content(object map[string]any, key, role string, tool bool) {
	if _, ok := object[key].(string); ok {
		v.text(object, key, role, tool)
		return
	}
	blocks, _ := object[key].([]any)
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case nil, "text", "input_text", "output_text":
			v.text(block, "text", role, tool)
		case "tool_result":
			v.content(block, "content", role, true)
		}
	}
}

func (v *view) messages(value any, key string) {
	messages, _ := value.([]any)
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		if message["type"] == "function_call_output" {
			v.content(message, "output", "tool", true)
		} else {
			v.content(message, key, role, role == "tool" || role == "function")
		}
	}
}
