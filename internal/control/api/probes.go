package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

const modelProbeTimeout = 200 * time.Second
const maxProbeBytes = 1 << 20

func (r *modelTestResult) probeFailure(ctx context.Context, err error) {
	var networkError net.Error
	r.OK = false
	r.Status, r.Classification, r.Error = http.StatusBadGateway, "failed", "model test request failed or returned an invalid response"
	if errors.Is(ctx.Err(), context.Canceled) {
		r.Status, r.Classification, r.Error = http.StatusRequestTimeout, "canceled", "model test was canceled"
	} else if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
		r.Status, r.Classification, r.Error = http.StatusGatewayTimeout, "slow", "no first token within the probe deadline; the model may be slow, not unreachable"
	}
}

func readProbe(response *http.Response, protocol string, served *string) (bool, error) {
	typ := strings.ToLower(response.Header.Get("Content-Type"))
	sse := strings.Contains(typ, "text/event-stream")
	ndjson := protocol == string(registry.TransportOllama)
	if !sse && !ndjson {
		body, err := io.ReadAll(io.LimitReader(response.Body, maxProbeBytes+1))
		if err != nil {
			return false, err
		}
		if len(body) > maxProbeBytes {
			return false, errors.New("probe response exceeds limit")
		}
		ok, _, err := probePayload(body, protocol, false, served)
		return ok, err
	}
	scanner := bufio.NewScanner(io.LimitReader(response.Body, maxProbeBytes+1))
	scanner.Buffer(make([]byte, 4096), maxProbeBytes)
	scanner.Split(probeLines)
	total := 0
	var payloads []string
	emit := func() (bool, bool, error) {
		for _, payload := range payloads {
			ok, done, err := probePayload([]byte(payload), protocol, true, served)
			if done || err != nil {
				return ok, true, err
			}
		}
		payloads = payloads[:0]
		return false, false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > maxProbeBytes {
			return false, errors.New("probe response exceeds limit")
		}
		if !sse {
			if strings.TrimSpace(line) == "" {
				continue
			}
			ok, done, err := probePayload([]byte(line), protocol, true, served)
			if done || err != nil {
				return ok, err
			}
		} else if strings.HasPrefix(line, "data:") {
			payloads = append(payloads, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		} else if line == "" {
			if ok, done, err := emit(); done || err != nil {
				return ok, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	ok, _, err := emit()
	return ok, err
}

// A trailing CR may be half of a CRLF split across transport reads.
func probeLines(data []byte, eof bool) (int, []byte, error) {
	for i, ch := range data {
		if ch == '\n' {
			return i + 1, data[:i], nil
		}
		if ch == '\r' {
			if i+1 == len(data) && !eof {
				return 0, nil, nil
			}
			n := i + 1
			if n < len(data) && data[n] == '\n' {
				n++
			}
			return n, data[:i], nil
		}
	}
	if eof && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func probeObject(value any) map[string]any { m, _ := value.(map[string]any); return m }
func probeArray(value any) []any           { a, _ := value.([]any); return a }
func probeText(value any) bool             { s, _ := value.(string); return strings.TrimSpace(s) != "" }

func probeOutput(value any) bool {
	m := probeObject(value)
	for _, key := range []string{"content", "reasoning", "reasoning_content", "thinking", "thinking_content", "text", "partial_json"} {
		if probeText(m[key]) {
			return true
		}
	}
	return len(probeArray(m["tool_calls"])) > 0
}

func probePayload(body []byte, protocol string, stream bool, served *string) (ok, done bool, err error) {
	if len(body) == 0 {
		return false, false, nil
	}
	if string(body) == "[DONE]" {
		return false, true, nil
	}
	var p map[string]any
	if err := json.Unmarshal(body, &p); err != nil || p == nil {
		return false, true, errors.New("invalid probe response")
	}
	if model, _ := p["model"].(string); model != "" && validProbeModel(model) {
		*served = model
	}
	if p["error"] != nil || p["type"] == "error" || p["status"] == "failed" || p["success"] == false {
		return false, true, errors.New("provider returned an error frame")
	}
	switch registry.Protocol(protocol) {
	case registry.TransportOpenAIChat:
		choices := probeArray(p["choices"])
		if len(choices) > 0 {
			c := probeObject(choices[0])
			ok = probeOutput(c["delta"]) || probeOutput(c["message"]) || c["finish_reason"] == "length"
			return ok, ok || !stream || probeText(c["finish_reason"]), nil
		}
	case registry.TransportOpenAIResponses:
		r := probeObject(p["response"])
		if model, _ := r["model"].(string); model != "" && validProbeModel(model) {
			*served = model
		}
		kind, _ := p["type"].(string)
		if kind == "response.failed" || kind == "response.error" || r["error"] != nil {
			return false, true, errors.New("provider returned a failed response")
		}
		switch kind {
		case "response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta", "response.function_call_arguments.delta", "response.refusal.delta":
			ok = probeText(p["delta"])
		}
		ok = ok || len(probeArray(p["output"])) > 0 || probeText(p["output_text"])
		ok = ok || kind == "response.completed" && len(probeArray(r["output"])) > 0
		item := probeObject(p["item"])
		ok = ok || kind == "response.output_item.added" && item["type"] == "function_call"
		ok = ok || kind == "response.incomplete" && probeObject(r["incomplete_details"])["reason"] == "max_output_tokens"
		ok = ok || !stream && probeObject(p["incomplete_details"])["reason"] == "max_output_tokens"
		return ok, ok || kind == "response.completed" || kind == "response.incomplete" || !stream, nil
	case registry.TransportAnthropic:
		message := probeObject(p["message"])
		if model, _ := message["model"].(string); model != "" && validProbeModel(model) {
			*served = model
		}
		block := probeObject(p["content_block"])
		ok = probeOutput(p["delta"]) || probeOutput(block) || block["type"] == "tool_use" || !stream && len(probeArray(p["content"])) > 0
		ok = ok || probeObject(p["delta"])["stop_reason"] == "max_tokens" || !stream && p["stop_reason"] == "max_tokens"
		return ok, ok || p["type"] == "message_stop" || !stream, nil
	case registry.TransportGemini:
		if model, _ := p["modelVersion"].(string); model != "" && validProbeModel(model) {
			*served = model
		}
		for _, candidate := range probeArray(p["candidates"]) {
			c := probeObject(candidate)
			for _, part := range probeArray(probeObject(c["content"])["parts"]) {
				m := probeObject(part)
				if probeText(m["text"]) || m["functionCall"] != nil {
					return true, true, nil
				}
			}
			if c["finishReason"] == "MAX_TOKENS" {
				return true, true, nil
			}
			if probeText(c["finishReason"]) {
				return false, true, nil
			}
		}
	case registry.TransportOllama:
		ok = probeOutput(p["message"]) || probeText(p["response"]) || p["done_reason"] == "length"
		return ok, ok || p["done"] == true || !stream, nil
	case registry.TransportSystemOne:
		ok = len(probeObject(p["answers"])) > 0 || len(probeObject(probeObject(p["result"])["answers"])) > 0
		return ok, ok || !stream, nil
	}
	return false, !stream, nil
}
