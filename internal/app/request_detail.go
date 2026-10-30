package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/ingress"
	"github.com/raufimusaddiq/routeweft/internal/telemetry"
)

// requestDetail builds the request detail of one finished request: the record the
// reference keeps per request (chatCore/requestDetail.js:82-98, nonStreamingHandler.js:
// 446-462, streamingHandler.js:140-170) with the same fields. Everything the request
// carried is redacted first and then each captured field is held to limit
// (PRD-OBS-002), so a preview of an over-limit field cannot hold a credential. It
// runs on the telemetry batcher, not on the request.
func requestDetail(outcome ingress.RequestOutcome, limit int) telemetry.Detail {
	capture := outcome.Capture
	status := "error"
	if outcome.Status >= 200 && outcome.Status < 300 && outcome.Termination == "" {
		status = "success"
	}
	payload := map[string]any{
		"provider":       outcome.ProviderID,
		"model":          outcome.ModelID,
		"connectionId":   outcome.ConnectionID,
		"requestedModel": outcome.RequestedModel,
		"selectedModel":  outcome.SelectedModel,
		"timestamp":      time.Now().UTC().Format(time.RFC3339Nano),
		"status":         status,
		"httpStatus":     outcome.Status,
		"latency":        map[string]any{"ttft": outcome.TTFT.Milliseconds(), "total": outcome.Duration.Milliseconds()},
		"tokens": map[string]any{
			"prompt_tokens": outcome.InputTokens, "completion_tokens": outcome.OutputTokens,
			"cached_tokens": outcome.CacheRead, "cache_creation_input_tokens": outcome.CacheWrite,
		},
	}
	if outcome.Termination != "" {
		payload["termination"] = outcome.Termination
	}
	if outcome.UsageUnobserved {
		payload["usageUnobserved"] = true
	}
	var secrets []string
	if capture != nil {
		secrets = capture.Secrets
		payload["routeMode"] = capture.RouteMode
		payload["request"] = fieldOrMarker(capture.ClientBody, capture.ClientBodySize, limit, func(body []byte) any {
			if config := ingress.RequestConfig(body, outcome.Stream); config != nil {
				return config
			}
			return string(body)
		})
		payload["providerRequest"] = fieldOrMarker(capture.ProviderRequest, capture.ProviderReqSize, limit, jsonOrText)
		providerResponse, response := responseFields(outcome, capture, limit)
		payload["providerResponse"] = providerResponse
		payload["response"] = response
	}
	redactor := telemetry.DetailRedactor{Secrets: secrets}
	redacted, _ := redactor.Redact(payload).(map[string]any)
	// Bound after redacting: an over-limit field keeps a preview of what was redacted.
	for _, field := range []string{"request", "providerRequest", "providerResponse", "response"} {
		if value, ok := redacted[field]; ok {
			redacted[field] = telemetry.TruncateField(value, limit)
		}
	}
	mode, _ := payload["routeMode"].(string)
	return telemetry.Detail{
		RequestID: outcome.RequestID, RouteMode: mode, Provider: outcome.ProviderID, Model: outcome.ModelID,
		Connection: outcome.ConnectionID, Status: status, Payload: redacted,
	}
}

// fieldOrMarker decodes a captured body. A body that is larger than what was kept
// cannot be decoded (it is cut off), so it becomes the marker TruncateField would
// make, with the true size and a preview of the part that was kept.
func fieldOrMarker(kept []byte, size, limit int, decode func([]byte) any) any {
	if len(kept) == 0 && size == 0 {
		return nil
	}
	if size > len(kept) || size > limit {
		return truncatedMarker(kept, size)
	}
	return decode(kept)
}

func truncatedMarker(kept []byte, size int) map[string]any {
	preview := []rune(string(kept))
	if len(preview) > 200 {
		preview = preview[:200]
	}
	return map[string]any{"_truncated": true, "_originalSize": size, "_preview": string(preview)}
}

func jsonOrText(body []byte) any {
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&decoded) == nil {
		return decoded
	}
	return string(body)
}

// responseFields are the provider's answer and the summary of it the client got
// (the reference's providerResponse and response). A stream keeps its text, not its
// bytes; an error keeps the upstream's message and status.
func responseFields(outcome ingress.RequestOutcome, capture *ingress.Capture, limit int) (any, any) {
	failed := outcome.Status < 200 || outcome.Status >= 300
	if capture.ProviderRespSize == 0 && !failed {
		return nil, map[string]any{}
	}
	if capture.ProviderRespSize > len(capture.ProviderResponse) && !outcome.Stream {
		marker := truncatedMarker(capture.ProviderResponse, capture.ProviderRespSize)
		return marker, map[string]any{"finish_reason": "unknown", "note": "provider response exceeded the detail limit"}
	}
	if failed {
		message := string(capture.ProviderResponse)
		if len(message) > 0 {
			if body, ok := decodeMap(capture.ProviderResponse); ok {
				if text := errorMessage(body); text != "" {
					message = text
				}
			}
		}
		return jsonOrText(capture.ProviderResponse), map[string]any{"error": message, "status": outcome.Status, "thinking": nil}
	}
	summary := ingress.SummarizeResponse(capture.ProviderResponse, outcome.Stream)
	if outcome.Stream {
		note := map[string]any{"content": summary.Content, "thinking": nilIfEmpty(summary.Thinking), "type": "streaming", "finish_reason": nilIfEmpty(summary.FinishReason)}
		if capture.ProviderRespSize > len(capture.ProviderResponse) {
			note["truncated"] = true
		}
		return summary.Content, note
	}
	return jsonOrText(capture.ProviderResponse), map[string]any{"content": nilIfEmpty(summary.Content), "thinking": nilIfEmpty(summary.Thinking), "finish_reason": orUnknown(summary.FinishReason)}
}

func decodeMap(body []byte) (map[string]any, bool) {
	var decoded map[string]any
	return decoded, json.Unmarshal(body, &decoded) == nil && decoded != nil
}

// errorMessage reads error.message, a string error or a top-level message.
func errorMessage(body map[string]any) string {
	if object, ok := body["error"].(map[string]any); ok {
		if text, ok := object["message"].(string); ok {
			return text
		}
	}
	if text, ok := body["error"].(string); ok {
		return text
	}
	text, _ := body["message"].(string)
	return strings.TrimSpace(text)
}

func nilIfEmpty(text string) any {
	if text == "" {
		return nil
	}
	return text
}

func orUnknown(text string) string {
	if text == "" {
		return "unknown"
	}
	return text
}
