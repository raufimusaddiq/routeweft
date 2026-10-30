package ingress

import (
	"bytes"
	"strings"
)

// ResponseSummary is the assistant output of an answer, in the shape the reference
// keeps in a request detail (chatCore/nonStreamingHandler.js:453-457,
// streamingHandler.js:151-158): the text, any reasoning text, and why it ended.
type ResponseSummary struct {
	Content      string
	Thinking     string
	FinishReason string
}

// SummarizeResponse reads the assistant text out of a captured answer body. A
// stream is read event by event (OpenAI Chat, Anthropic Messages, OpenAI Responses,
// Gemini, and Ollama's line-delimited JSON); anything else is one JSON document in
// any of those shapes. A body cut off at the capture limit is read as far as it
// goes. Text that cannot be found is left empty, never guessed.
func SummarizeResponse(body []byte, stream bool) ResponseSummary {
	if stream {
		return summarizeStream(body)
	}
	answer, ok := decodeObject(body)
	if !ok {
		return ResponseSummary{}
	}
	summary := ResponseSummary{Content: extractPanelText(answer)}
	if choices, ok := answer["choices"].([]any); ok && len(choices) > 0 {
		choice := objectOf(choices[0])
		message := objectOf(choice["message"])
		if text, ok := message["reasoning_content"].(string); ok {
			summary.Thinking = text
		}
		if reason, ok := choice["finish_reason"].(string); ok {
			summary.FinishReason = reason
		}
	}
	if reason, ok := answer["stop_reason"].(string); ok && summary.FinishReason == "" {
		summary.FinishReason = reason
	}
	if reason, ok := answer["done_reason"].(string); ok && summary.FinishReason == "" {
		summary.FinishReason = reason
	}
	return summary
}

func summarizeStream(body []byte) ResponseSummary {
	var content, thinking strings.Builder
	var finish string
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		line = bytes.TrimPrefix(line, []byte("data:"))
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		event, ok := decodeObject(line)
		if !ok {
			continue // a final line cut off by the capture limit
		}
		if choices, ok := event["choices"].([]any); ok && len(choices) > 0 {
			choice := objectOf(choices[0])
			delta := objectOf(choice["delta"])
			if text, ok := delta["content"].(string); ok {
				content.WriteString(text)
			}
			for _, key := range []string{"reasoning_content", "reasoning"} {
				if text, ok := delta[key].(string); ok {
					thinking.WriteString(text)
				}
			}
			if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
				finish = reason
			}
		}
		switch event["type"] {
		case "content_block_delta":
			delta := objectOf(event["delta"])
			if text, ok := delta["text"].(string); ok {
				content.WriteString(text)
			}
			if text, ok := delta["thinking"].(string); ok {
				thinking.WriteString(text)
			}
		case "message_delta":
			if reason, ok := objectOf(event["delta"])["stop_reason"].(string); ok {
				finish = reason
			}
		case "response.output_text.delta":
			if text, ok := event["delta"].(string); ok {
				content.WriteString(text)
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if text, ok := event["delta"].(string); ok {
				thinking.WriteString(text)
			}
		case "response.completed":
			if status, ok := objectOf(event["response"])["status"].(string); ok {
				finish = status
			}
		}
		if candidates, ok := event["candidates"].([]any); ok && len(candidates) > 0 {
			candidate := objectOf(candidates[0])
			if parts, ok := objectOf(candidate["content"])["parts"].([]any); ok {
				for _, part := range parts {
					object := objectOf(part)
					if text, ok := object["text"].(string); ok {
						if object["thought"] == true {
							thinking.WriteString(text)
						} else {
							content.WriteString(text)
						}
					}
				}
			}
			if reason, ok := candidate["finishReason"].(string); ok {
				finish = reason
			}
		}
		if message := objectOf(event["message"]); message != nil {
			if text, ok := message["content"].(string); ok {
				content.WriteString(text)
			}
			if text, ok := message["thinking"].(string); ok {
				thinking.WriteString(text)
			}
		}
		if reason, ok := event["done_reason"].(string); ok {
			finish = reason
		}
	}
	return ResponseSummary{Content: content.String(), Thinking: thinking.String(), FinishReason: finish}
}

// RequestConfig is extractRequestConfig (chatCore/requestDetail.js:5-22): the
// conversation, the model, whether it streamed and the optional parameters that were
// set. The conversation is whichever array the protocol uses, so a Responses or
// Gemini request keeps its history instead of an empty list. The body is read as
// JSON; one that is not an object yields nothing.
func RequestConfig(body []byte, stream bool) map[string]any {
	request, ok := decodeObject(body)
	if !ok {
		return nil
	}
	config := map[string]any{"stream": stream}
	config["model"] = request["model"]
	conversation := false
	for _, key := range []string{"messages", "input", "contents"} {
		if value, ok := request[key]; ok {
			config[key] = value
			conversation = true
			break
		}
	}
	if !conversation {
		config["messages"] = []any{}
	}
	for _, param := range requestConfigParams {
		if value, ok := request[param]; ok {
			config[param] = value
		}
	}
	return config
}

var requestConfigParams = []string{
	"temperature", "top_p", "top_k", "max_tokens", "max_completion_tokens", "thinking", "reasoning", "enable_thinking",
	"presence_penalty", "frequency_penalty", "seed", "stop", "tools", "tool_choice", "response_format", "prediction", "store",
	"metadata", "n", "logprobs", "top_logprobs", "logit_bias", "user", "parallel_tool_calls",
	// The same settings under the other protocols' names.
	"max_output_tokens", "generationConfig", "system", "instructions", "options",
}
