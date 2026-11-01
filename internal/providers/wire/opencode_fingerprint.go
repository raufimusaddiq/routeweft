package wire

// OpenCode Zen's free-tier fingerprint tools (executors/opencode.js:455-464,
// utils/opencodeFingerprint.js). The gate requires the lowercase quartet
// bash/glob/grep/read even when the caller supplied its own tools under other
// casing. Case variants are renamed to one canonical declaration, missing
// members are appended with a "do not use" description, and the response is
// rewritten back to the caller's spelling so its own tool calls still match.
// Only Chat bodies reach this wire: the Responses/Messages models are refused
// by opencodeTarget.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

const opencodeFingerprintHint = "This tool is currently unavailable and must not be used."

var opencodeQuartet = []string{"bash", "glob", "grep", "read"}

// opencodeFingerprint applies utils/opencodeFingerprint.js applyFingerprintTools
// to a Chat body (flat=false). It returns the rewritten body and the map of
// sent name -> caller's name.
func opencodeFingerprint(raw []byte) ([]byte, map[string]string, error) {
	body, err := concerns.WireObject(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("request is not a JSON object")
	}
	tools, _ := body["tools"].([]any)
	hadTools := len(tools) > 0
	renamed := map[string]string{}
	seen := map[string]bool{}
	kept := make([]any, 0, len(tools)+len(opencodeQuartet))
	for _, rawTool := range tools {
		tool, _ := rawTool.(map[string]any)
		function, _ := tool["function"].(map[string]any)
		name := strings.TrimSpace(concerns.Text(function["name"]))
		key := opencodeQuartetKey(name)
		if key == "" || tool == nil || function == nil {
			kept = append(kept, rawTool)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if name != key {
			renamed[key] = name
			copyFunction := map[string]any{}
			for k, v := range function {
				copyFunction[k] = v
			}
			copyFunction["name"] = key
			copyTool := map[string]any{}
			for k, v := range tool {
				copyTool[k] = v
			}
			copyTool["function"] = copyFunction
			kept = append(kept, copyTool)
			continue
		}
		kept = append(kept, rawTool)
	}
	for _, name := range opencodeQuartet {
		if seen[name] {
			continue
		}
		kept = append(kept, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": opencodeFingerprintHint,
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		})
	}
	body["tools"] = kept
	if choice, ok := body["tool_choice"].(map[string]any); ok {
		if function, ok := choice["function"].(map[string]any); ok {
			if key := opencodeQuartetKey(concerns.Text(function["name"])); key != "" && renamed[key] != "" {
				function["name"] = key
			}
		}
		if key := opencodeQuartetKey(concerns.Text(choice["name"])); key != "" && renamed[key] != "" {
			choice["name"] = key
		}
	}
	if _, exists := body["tool_choice"]; !exists && !hadTools {
		body["tool_choice"] = "none"
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	return encoded, renamed, nil
}

func opencodeQuartetKey(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, key := range opencodeQuartet {
		if lower == key {
			return key
		}
	}
	return ""
}

// opencodeRestore is utils/opencodeFingerprint.js restoreToolNames for the Chat
// shapes OpenCode answers: SSE deltas and a buffered message body.
func opencodeRestore(payload any, renamed map[string]string) bool {
	if len(renamed) == 0 {
		return false
	}
	changed := false
	restoreCalls := func(calls []any) {
		for _, rawCall := range calls {
			call, _ := rawCall.(map[string]any)
			function, _ := call["function"].(map[string]any)
			if original := renamed[concerns.Text(function["name"])]; original != "" {
				function["name"] = original
				changed = true
			}
		}
	}
	object, _ := payload.(map[string]any)
	choices, _ := object["choices"].([]any)
	for _, rawChoice := range choices {
		choice, _ := rawChoice.(map[string]any)
		for _, holder := range []string{"delta", "message"} {
			value, _ := choice[holder].(map[string]any)
			calls, _ := value["tool_calls"].([]any)
			restoreCalls(calls)
		}
	}
	return changed
}

func opencodeRefusal(request *http.Request, status int, message string) *http.Response {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error"}})
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}
}

// opencodeDo is the fingerprint exchange around one Chat request.
func opencodeDo(_ routing.ProviderRef, client *http.Client, request *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(io.LimitReader(request.Body, concerns.MaxTranslatedResponseBytes+1))
	request.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(raw) > concerns.MaxTranslatedResponseBytes {
		return opencodeRefusal(request, http.StatusRequestEntityTooLarge, "request exceeds the limit"), nil
	}
	body, renamed, err := opencodeFingerprint(raw)
	if err != nil {
		return opencodeRefusal(request, http.StatusBadRequest, "opencode request cannot be expressed: "+err.Error()), nil
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	response, err := client.Do(request)
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 || len(renamed) == 0 {
		return response, err
	}
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		reader, writer := io.Pipe()
		go opencodeRestoreStream(response.Body, writer, renamed)
		response.Body = reader
		response.ContentLength, response.Uncompressed = -1, true
		return response, nil
	}
	answer, err := io.ReadAll(io.LimitReader(response.Body, concerns.MaxTranslatedResponseBytes+1))
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	var payload any
	if json.Unmarshal(answer, &payload) == nil && opencodeRestore(payload, renamed) {
		if encoded, err := json.Marshal(payload); err == nil {
			answer = encoded
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(answer))
	response.ContentLength = int64(len(answer))
	return response, nil
}

// opencodeRestoreStream rewrites one data line at a time; the upstream Chat
// stream puts one JSON event per data line.
func opencodeRestoreStream(source io.ReadCloser, writer *io.PipeWriter, renamed map[string]string) {
	defer source.Close()
	defer writer.Close()
	reader := bufio.NewReader(source)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			if rewritten, ok := opencodeRestoreLine(line, renamed); ok {
				line = rewritten
			}
			if _, writeErr := io.WriteString(writer, line); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func opencodeRestoreLine(line string, renamed map[string]string) (string, bool) {
	trimmed := strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(trimmed, "data:") {
		return "", false
	}
	// SSE permits both `data: {...}` and `data:{...}`.
	payloadText := strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " ")
	var payload any
	if json.Unmarshal([]byte(payloadText), &payload) != nil {
		return "", false
	}
	if !opencodeRestore(payload, renamed) {
		return "", false
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	return "data: " + string(encoded) + line[len(trimmed):], true
}
