package parity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	"github.com/raufimusaddiq/routeweft/internal/protocol/ollama"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
)

// responseRecord is the oracle's conversion of one upstream stream, chunk by chunk,
// with the fold of what it produced (compat/parity/legs-fold.mjs).
type responseRecord struct {
	Leg    string `json:"leg"`
	Case   string `json:"case"`
	Input  []any  `json:"input"`
	Output []any  `json:"output"`
	Fold   any    `json:"fold"`
	Throws string `json:"throws"`
}

type legsPinResponses struct {
	Responses []responseRecord `json:"responses"`
}

func loadResponseRecords(t *testing.T) []responseRecord {
	t.Helper()
	raw, err := os.ReadFile("testdata/legs_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin legsPinResponses
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	return pin.Responses
}

// ---- folds: the same reductions as legs-fold.mjs ------------------------------

func num(v any) float64 {
	n, _ := v.(float64)
	return n
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

// compactUsage drops zero and missing figures, as compact() does in the JS folds.
func compactUsage(pairs map[string]float64) map[string]any {
	out := map[string]any{}
	for k, v := range pairs {
		if v != 0 {
			out[k] = v
		}
	}
	return out
}

func foldChat(chunks []any) any {
	out := map[string]any{"content": "", "reasoning": "", "finish": "", "usage": map[string]any{}}
	content, reasoning := "", ""
	type call struct{ id, name, args string }
	calls := map[int]*call{}
	var order []int
	for _, raw := range chunks {
		chunk := obj(raw)
		if chunk == nil {
			continue
		}
		if usage := obj(chunk["usage"]); usage != nil {
			cached := num(obj(usage["prompt_tokens_details"])["cached_tokens"])
			if cached == 0 {
				cached = num(usage["cached_tokens"])
			}
			creation := num(obj(usage["prompt_tokens_details"])["cache_creation_tokens"])
			if creation == 0 {
				creation = num(usage["cache_creation_input_tokens"])
			}
			out["usage"] = compactUsage(map[string]float64{"prompt": num(usage["prompt_tokens"]), "completion": num(usage["completion_tokens"]), "total": num(usage["total_tokens"]), "cached": cached, "reasoning": num(obj(usage["completion_tokens_details"])["reasoning_tokens"]), "cacheCreation": creation})
		}
		choices := list(chunk["choices"])
		if len(choices) == 0 {
			continue
		}
		choice := obj(choices[0])
		delta := obj(choice["delta"])
		if delta == nil {
			delta = obj(choice["message"])
		}
		content += str(delta["content"])
		reasoning += str(delta["reasoning_content"])
		for _, rawCall := range list(delta["tool_calls"]) {
			c := obj(rawCall)
			index := len(calls)
			if _, ok := c["index"].(float64); ok {
				index = int(num(c["index"]))
			}
			entry, ok := calls[index]
			if !ok {
				entry = &call{}
				calls[index] = entry
				order = append(order, index)
			}
			if id := str(c["id"]); id != "" {
				entry.id = id
			}
			function := obj(c["function"])
			if name := str(function["name"]); name != "" {
				entry.name = name
			}
			entry.args += str(function["arguments"])
		}
		if finish := str(choice["finish_reason"]); finish != "" {
			out["finish"] = finish
		}
	}
	sort.Ints(order)
	toolCalls := []any{}
	for _, index := range order {
		toolCalls = append(toolCalls, map[string]any{"id": calls[index].id, "name": calls[index].name, "arguments": calls[index].args})
	}
	out["content"], out["reasoning"], out["toolCalls"] = content, reasoning, toolCalls
	return out
}

func foldMessages(events []any) any {
	out := map[string]any{"stopReason": ""}
	type block struct{ kind, text, thinking, id, name, json string }
	blocks := map[int]*block{}
	var order []int
	usage := map[string]any{}
	for _, raw := range events {
		event := obj(raw)
		if event == nil {
			continue
		}
		switch str(event["type"]) {
		case "message_start":
			for k, v := range obj(obj(event["message"])["usage"]) {
				usage[k] = v
			}
		case "message_delta":
			for k, v := range obj(event["usage"]) {
				usage[k] = v
			}
			if stop := str(obj(event["delta"])["stop_reason"]); stop != "" {
				out["stopReason"] = stop
			}
		case "content_block_start":
			start := obj(event["content_block"])
			index := int(num(event["index"]))
			blocks[index] = &block{kind: str(start["type"]), id: str(start["id"]), name: str(start["name"])}
			order = append(order, index)
		case "content_block_delta":
			b := blocks[int(num(event["index"]))]
			if b == nil {
				continue
			}
			delta := obj(event["delta"])
			switch str(delta["type"]) {
			case "text_delta":
				b.text += str(delta["text"])
			case "thinking_delta":
				b.thinking += str(delta["thinking"])
			case "input_json_delta":
				b.json += str(delta["partial_json"])
			}
		}
	}
	sort.Ints(order)
	folded := []any{}
	for _, index := range order {
		b := blocks[index]
		switch b.kind {
		case "text":
			folded = append(folded, map[string]any{"type": "text", "text": b.text})
		case "thinking":
			folded = append(folded, map[string]any{"type": "thinking", "text": b.thinking})
		default:
			input := map[string]any{}
			if b.json != "" {
				_ = json.Unmarshal([]byte(b.json), &input)
			}
			folded = append(folded, map[string]any{"type": b.kind, "id": b.id, "name": b.name, "input": input})
		}
	}
	out["blocks"] = folded
	out["usage"] = compactUsage(map[string]float64{"input": num(usage["input_tokens"]), "output": num(usage["output_tokens"]), "cacheRead": num(usage["cache_read_input_tokens"]), "cacheCreation": num(usage["cache_creation_input_tokens"])})
	return out
}

func foldResponses(events []any) any {
	out := map[string]any{"status": "", "usage": map[string]any{}}
	type item struct{ kind, text, name, callID, args, id string }
	var items []*item
	last := func(kind string) *item {
		for i := len(items) - 1; i >= 0; i-- {
			if items[i].kind == kind {
				return items[i]
			}
		}
		return nil
	}
	find := func(id, kind string) *item {
		for _, it := range items {
			if it.id == id && id != "" {
				return it
			}
		}
		return last(kind)
	}
	for _, raw := range events {
		event := obj(raw)
		if inner := obj(event["data"]); inner != nil {
			event = inner // the Responses leg wraps each as {event, data}
		}
		if event == nil {
			continue
		}
		switch str(event["type"]) {
		case "response.output_item.added":
			it := obj(event["item"])
			items = append(items, &item{kind: str(it["type"]), name: str(it["name"]), callID: str(it["call_id"]), id: str(it["id"])})
		case "response.output_text.delta":
			if it := last("message"); it != nil {
				it.text += str(event["delta"])
			}
		case "response.reasoning_summary_text.delta":
			if it := last("reasoning"); it != nil {
				it.text += str(event["delta"])
			}
		case "response.function_call_arguments.delta":
			if it := find(str(event["item_id"]), "function_call"); it != nil {
				it.args += str(event["delta"])
			}
		case "response.output_item.done":
			if done := obj(event["item"]); str(done["type"]) == "function_call" {
				if it := find(str(done["id"]), "function_call"); it != nil {
					if name := str(done["name"]); name != "" {
						it.name = name
					}
					if id := str(done["call_id"]); id != "" {
						it.callID = id
					}
					if args, ok := done["arguments"].(string); ok {
						it.args = args
					}
				}
			}
		case "response.completed", "response.incomplete", "response.failed":
			response := obj(event["response"])
			out["status"] = str(response["status"])
			if usage := obj(response["usage"]); usage != nil {
				out["usage"] = compactUsage(map[string]float64{"input": num(usage["input_tokens"]), "output": num(usage["output_tokens"]), "total": num(usage["total_tokens"]), "cached": num(obj(usage["input_tokens_details"])["cached_tokens"]), "reasoning": num(obj(usage["output_tokens_details"])["reasoning_tokens"])})
			}
			if reason := str(obj(response["incomplete_details"])["reason"]); reason != "" {
				out["incomplete"] = reason
			}
		}
	}
	folded := []any{}
	for _, it := range items {
		if it.kind == "function_call" {
			folded = append(folded, map[string]any{"type": it.kind, "name": it.name, "callId": it.callID, "arguments": it.args})
		} else {
			folded = append(folded, map[string]any{"type": it.kind, "text": it.text})
		}
	}
	out["items"] = folded
	return out
}

func foldGemini(chunks []any) any {
	out := map[string]any{"text": "", "thought": "", "finish": "", "usage": map[string]any{}}
	text, thought := "", ""
	calls := []any{}
	for _, raw := range chunks {
		chunk := obj(raw)
		if inner := obj(chunk["response"]); inner != nil {
			chunk = inner
		}
		if chunk == nil {
			continue
		}
		if candidates := list(chunk["candidates"]); len(candidates) > 0 {
			candidate := obj(candidates[0])
			for _, rawPart := range list(obj(candidate["content"])["parts"]) {
				part := obj(rawPart)
				if s, ok := part["text"].(string); ok {
					if thinking, _ := part["thought"].(bool); thinking {
						thought += s
					} else {
						text += s
					}
				}
				if call := obj(part["functionCall"]); call != nil {
					args := call["args"]
					if args == nil {
						args = map[string]any{}
					}
					calls = append(calls, map[string]any{"name": str(call["name"]), "args": args})
				}
			}
			if finish := str(candidate["finishReason"]); finish != "" {
				out["finish"] = finish
			}
		}
		if usage := obj(chunk["usageMetadata"]); usage != nil {
			out["usage"] = compactUsage(map[string]float64{"prompt": num(usage["promptTokenCount"]), "candidates": num(usage["candidatesTokenCount"]), "thoughts": num(usage["thoughtsTokenCount"]), "cached": num(usage["cachedContentTokenCount"]), "total": num(usage["totalTokenCount"])})
		}
	}
	out["text"], out["thought"], out["calls"] = text, thought, calls
	return out
}

// ---- Routeweft's side of each response leg -------------------------------------

type responseAdapter struct {
	// serialize writes the upstream's events as the wire carries them.
	serialize func(events []any) []byte
	convert   func(src *bytes.Reader, dst *bytes.Buffer) error
	fold      func(chunks []any) any
}

func sse(withEvent, done bool) func([]any) []byte {
	return func(events []any) []byte {
		var out bytes.Buffer
		for _, event := range events {
			raw, _ := json.Marshal(event)
			if withEvent {
				fmt.Fprintf(&out, "event: %s\n", str(obj(event)["type"]))
			}
			fmt.Fprintf(&out, "data: %s\n\n", raw)
		}
		if done {
			out.WriteString("data: [DONE]\n\n")
		}
		return out.Bytes()
	}
}

func jsonLines(events []any) []byte {
	var out bytes.Buffer
	for _, event := range events {
		raw, _ := json.Marshal(event)
		out.Write(raw)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

var responseAdapters = map[string]responseAdapter{
	"claude>openai": {sse(true, false), func(src *bytes.Reader, dst *bytes.Buffer) error {
		return anthropic.MessagesStreamToChat(context.Background(), src, dst, nil)
	}, foldChat},
	"gemini>openai": {sse(false, false), func(src *bytes.Reader, dst *bytes.Buffer) error {
		return gemini.GeminiStreamToChat(context.Background(), src, dst, gemini.ResponseOptions{Model: "m", Wire: gemini.Protocol})
	}, foldChat},
	"vertex>openai": {sse(false, false), func(src *bytes.Reader, dst *bytes.Buffer) error {
		return gemini.GeminiStreamToChat(context.Background(), src, dst, gemini.ResponseOptions{Model: "m", Wire: gemini.VertexProtocol})
	}, foldChat},
	"gemini-cli>openai": {sse(false, false), func(src *bytes.Reader, dst *bytes.Buffer) error {
		return gemini.GeminiStreamToChat(context.Background(), src, dst, gemini.ResponseOptions{Model: "m", Wire: gemini.GeminiCLIProtocol})
	}, foldChat},
	"antigravity>openai": {sse(false, false), func(src *bytes.Reader, dst *bytes.Buffer) error {
		return gemini.GeminiStreamToChat(context.Background(), src, dst, gemini.ResponseOptions{Model: "m", Wire: gemini.AntigravityProtocol})
	}, foldChat},
	"ollama>openai": {jsonLines, func(src *bytes.Reader, dst *bytes.Buffer) error {
		return ollama.OllamaStreamToChat(context.Background(), src, dst, "m")
	}, foldChat},
	"openai-responses>openai": {sse(true, false), func(src *bytes.Reader, dst *bytes.Buffer) error {
		return openai.ResponsesStreamToChat(context.Background(), src, dst, "m")
	}, foldChat},
	"openai>claude": {sse(false, true), func(src *bytes.Reader, dst *bytes.Buffer) error {
		return anthropic.ChatStreamToMessages(context.Background(), src, dst)
	}, foldMessages},
	"openai>openai-responses": {sse(false, true), func(src *bytes.Reader, dst *bytes.Buffer) error {
		return openai.ChatStreamToResponses(context.Background(), src, dst, "m", nil)
	}, foldResponses},
	"openai>antigravity": {sse(false, true), func(src *bytes.Reader, dst *bytes.Buffer) error {
		return gemini.ChatStreamToAntigravity(context.Background(), src, dst)
	}, foldGemini},
}

// parseStream reads the data lines of an SSE stream (or JSON lines) into events.
func parseStream(raw []byte) []any {
	var events []any
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if line == "" || line == "[DONE]" || strings.HasPrefix(line, "event:") || strings.HasPrefix(line, ":") {
			continue
		}
		var event any
		if json.Unmarshal([]byte(line), &event) == nil {
			events = append(events, event)
		}
	}
	return events
}

// runResponseLeg converts the recorded upstream stream with Routeweft's leg and
// folds it, returning every difference from the oracle's fold.
func runResponseLeg(record responseRecord) []legDiff {
	adapter, ok := responseAdapters[record.Leg]
	if !ok {
		return []legDiff{{"$", "go-error", nil, "no adapter for leg " + record.Leg}}
	}
	var out bytes.Buffer
	err := adapter.convert(bytes.NewReader(adapter.serialize(record.Input)), &out)
	got := adapter.fold(parseStream(out.Bytes()))
	// Compare as JSON so a float and an int of one value are one number.
	encoded, _ := json.Marshal(got)
	var normalized any
	_ = json.Unmarshal(encoded, &normalized)
	var diffs []legDiff
	if err != nil {
		diffs = append(diffs, legDiff{"$", "go-error", nil, err.Error()})
	}
	diffValues("$", normalize("", record.Fold), normalize("", normalized), &diffs)
	return diffs
}

func groupResponseDiffs(records []responseRecord) map[string]*legGroup {
	groups := map[string]*legGroup{}
	for _, record := range records {
		for _, d := range runResponseLeg(record) {
			g := legGroup{Leg: record.Leg, Kind: d.Kind, Path: indexPattern.ReplaceAllString(d.Path, "[]")}
			existing, ok := groups[g.key()]
			if !ok {
				g.Oracle, g.Go = short(d.Oracle), short(d.Go)
				existing = &g
				groups[g.key()] = existing
			}
			existing.Cases = append(existing.Cases, record.Case)
		}
	}
	return groups
}

// TestFoldsAgreeOnTheOraclesOwnChunks keeps the comparison honest: folding the
// oracle's raw output here gives what legs-fold.mjs recorded for it. A fold that
// disagreed with itself would make every difference below meaningless.
func TestFoldsAgreeOnTheOraclesOwnChunks(t *testing.T) {
	for _, record := range loadResponseRecords(t) {
		if record.Throws != "" {
			continue
		}
		adapter, ok := responseAdapters[record.Leg]
		if !ok {
			t.Fatalf("no adapter for %s", record.Leg)
		}
		encoded, _ := json.Marshal(adapter.fold(record.Output))
		var got any
		_ = json.Unmarshal(encoded, &got)
		var diffs []legDiff
		diffValues("$", record.Fold, got, &diffs)
		if len(diffs) > 0 {
			t.Errorf("%s / %s: the Go fold disagrees with the JS fold: %+v", record.Leg, record.Case, diffs[0])
		}
	}
}

// TestResponseLegsReport prints the grouped response differences (LEGS_REPORT=path).
func TestResponseLegsReport(t *testing.T) {
	if os.Getenv("LEGS_REPORT") == "" {
		t.Skip("set LEGS_REPORT=1 to print the differences")
	}
	groups := groupResponseDiffs(loadResponseRecords(t))
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		g := groups[key]
		fmt.Fprintf(&out, "%3d %s\n      case=%q\n      oracle=%s\n      go    =%s\n", len(g.Cases), key, g.Cases[0], g.Oracle, g.Go)
	}
	if path := os.Getenv("LEGS_REPORT"); path != "1" {
		if err := os.WriteFile(path+".responses", []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%d groups", len(groups))
}

// TestResponseLegsMatchTheOracleOrAreInTheLedger is the response-side differential.
func TestResponseLegsMatchTheOracleOrAreInTheLedger(t *testing.T) {
	groups := groupResponseDiffs(loadResponseRecords(t))
	listed := map[string]ledgerEntry{}
	for _, entry := range loadLedger(t).Entries {
		if entry.Direction == "response" {
			listed[entry.key()] = entry
		}
	}
	checkLedger(t, groups, listed)
}
