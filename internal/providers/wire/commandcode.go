package wire

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// CommandCode is served at {base}/generate (registry/commandcode.js:21-28 names the
// whole URL; the catalog base is its /alpha prefix). The pin's executor
// (executors/commandcode.js) adds four things the conversion legs do not: the
// version and session headers (:28-40), a forced stream (:23-25), a read of the
// stream's first events that turns a 200 carrying an AI SDK `error` event into
// an HTTP error (:140-241), and a bounded retry of the 502/503/504 that produces
// (:42-61).

const (
	commandCodeEndpoint = "generate"
	// commandCodeVersion and commandCodeEnvironment are registry/commandcode.js:25-28.
	commandCodeVersion     = "0.25.7"
	commandCodeEnvironment = "cli"
	// commandCodeRetries is the pin's maxRetries (:43): three attempts in all.
	commandCodeRetries = 2
	// commandCodeInspectLimit bounds the bytes held while looking for the first
	// decisive event. The pin buffers without a bound; a stream that has said
	// nothing decisive within this much is relayed as it is.
	commandCodeInspectLimit = 1 << 20
)

// commandCodeSleep is the retry delay, replaced by tests.
var commandCodeSleep = func(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var commandCodeHook = Hook{
	OpenAIHeaders: commandCodeHeaders,
	Target:        func(routing.ProviderRef) (Target, error) { return Target{Endpoint: commandCodeEndpoint}, nil },
	Finalize:      commandCodeStream,
	Do:            CommandCodeDo,
}

// commandCodeHeaders is buildHeaders (:28-40). The credential is the bearer the
// shared path already sets. Accept is event-stream because the stream is forced.
func commandCodeHeaders(routing.ProviderRef) map[string]string {
	return map[string]string{
		"Content-Type":           "application/json",
		"Accept":                 "text/event-stream",
		"x-command-code-version": commandCodeVersion,
		"x-cli-environment":      commandCodeEnvironment,
		"x-session-id":           uuid.NewString(),
	}
}

// commandCodeStream is transformRequest (:23-25) together with the stream the
// pin hands its translator when the provider forces streaming (chatCore.js:146):
// the envelope and its params both ask for a stream whatever the client sent.
func commandCodeStream(protocol string, body Body) bool {
	if protocol != "commandcode" {
		return false
	}
	changed := false
	if string(bytes.TrimSpace(body["stream"])) != "true" {
		body["stream"] = json.RawMessage("true")
		changed = true
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(body["params"], &params) == nil && params != nil && string(bytes.TrimSpace(params["stream"])) != "true" {
		params["stream"] = json.RawMessage("true")
		if encoded, err := json.Marshal(params); err == nil {
			body["params"] = encoded
			changed = true
		}
	}
	return changed
}

// CommandCodeError is a parsed upstream `error` event (parseCommandCodeError, :80-138).
type CommandCodeError struct {
	Status  int
	Message string
	Type    string
}

// ParseCommandCodeError ports parseCommandCodeError. An error event carries its
// own status when it names one in 400-599; otherwise the message decides.
func ParseCommandCodeError(event map[string]any) CommandCodeError {
	if event == nil {
		return CommandCodeError{Status: 503, Message: "CommandCode upstream error", Type: "server_error"}
	}
	value, ok := event["error"]
	if !ok || value == nil {
		value = event["message"]
	}
	if value == nil {
		value = "unknown"
	}
	message, kind, status := "", "server_error", 0
	switch typed := value.(type) {
	case map[string]any:
		message = commandCodeText(typed["message"])
		if message == "" {
			message = commandCodeText(typed["error"])
		}
		if message == "" {
			encoded, _ := json.Marshal(typed)
			message = string(encoded)
		}
		if code, ok := commandCodeInteger(typed["statusCode"]); ok {
			status = code
		} else if code, ok := commandCodeInteger(typed["status"]); ok {
			status = code
		}
		if name := commandCodeText(typed["type"]); name != "" {
			kind = name
		}
	case string:
		message = typed
	default:
		encoded, _ := json.Marshal(typed)
		message = string(encoded)
	}
	if code, ok := commandCodeInteger(event["statusCode"]); ok {
		status = code
	}
	if status < 400 || status > 599 {
		lower := strings.ToLower(message)
		contains := func(needles ...string) bool {
			for _, needle := range needles {
				if strings.Contains(lower, needle) {
					return true
				}
			}
			return false
		}
		switch {
		case contains("rate limit", "too many requests"):
			status, kind = 429, "rate_limit_error"
		case contains("unauthorized", "invalid api key", "authentication"):
			status, kind = 401, "authentication_error"
		case contains("payment required", "billing"):
			status, kind = 402, "billing_error"
		case contains("quota", "forbidden", "permission"):
			status, kind = 403, "permission_error"
		case contains("not found"):
			status, kind = 404, "invalid_request_error"
		case contains("unavailable", "overloaded", "server error"):
			status, kind = 503, "server_error"
		default:
			status = 503
		}
	}
	return CommandCodeError{Status: status, Message: message, Type: kind}
}

// commandCodeText is JS `||` over a message candidate: only a non-empty string counts.
func commandCodeText(value any) string {
	text, _ := value.(string)
	return text
}

// commandCodeInteger is `value && Number.isInteger(Number(value))`.
func commandCodeInteger(value any) (int, bool) {
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	if number == 0 || number != math.Trunc(number) || math.Abs(number) > 1<<31 {
		return 0, false
	}
	return int(number), true
}

// CommandCodeDo sends the request and, on a 200, reads far enough into the
// stream to tell an error event from an answer; it retries the 502/503/504 an
// error event maps to (execute, :42-61). A non-2xx upstream status is returned
// as it is, never retried (:42-61).
func CommandCodeDo(client *http.Client, request *http.Request) (*http.Response, error) {
	var payload []byte
	if request.Body != nil {
		var err error
		payload, err = io.ReadAll(request.Body)
		request.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	for attempt := 0; ; attempt++ {
		attemptRequest := request.Clone(request.Context())
		attemptRequest.Body = io.NopCloser(bytes.NewReader(payload))
		attemptRequest.ContentLength = int64(len(payload))
		attemptRequest.GetBody = nil
		if attempt > 0 {
			// buildHeaders runs again on a retry, so the session id is new (:28-40).
			attemptRequest.Header.Set("x-session-id", uuid.NewString())
		}
		response, err := client.Do(attemptRequest)
		if err != nil {
			return nil, err
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 || response.Body == nil {
			return response, nil
		}
		inspected, failure := inspectCommandCode(response)
		if failure == nil {
			return inspected, nil
		}
		retryable := failure.Status == 502 || failure.Status == 503 || failure.Status == 504
		if !retryable || attempt >= commandCodeRetries {
			return commandCodeFailure(response, *failure), nil
		}
		if err := commandCodeSleep(request.Context(), time.Second*time.Duration(attempt+1)); err != nil {
			return nil, err
		}
	}
}

// inspectCommandCode is inspectAndWrapCommandCodeResponse's read (:140-241): it
// reads lines until one is an error (the response is replaced), a content or
// finish event, or the [DONE] marker (the stream is relayed). Everything it read
// is replayed ahead of the rest. A read error ends the inspection and surfaces
// on the relayed body instead.
func inspectCommandCode(response *http.Response) (*http.Response, *CommandCodeError) {
	var consumed bytes.Buffer
	reader := bufio.NewReader(io.TeeReader(response.Body, &consumed))
	var failure *CommandCodeError
	for consumed.Len() <= commandCodeInspectLimit {
		line, err := reader.ReadBytes('\n')
		complete := err == nil
		// A final line with no newline is judged only for an error, as the pin judges its leftover buffer.
		if event, done := commandCodeLine(line); event != nil && event["type"] == "error" {
			parsed := ParseCommandCodeError(event)
			failure = &parsed
			break
		} else if done && complete {
			break
		}
		if err != nil {
			break
		}
	}
	if failure != nil {
		return nil, failure
	}
	// Whatever the reader buffered came through the tee, so consumed holds it all.
	response.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(consumed.Bytes()), response.Body), response.Body}
	return response, nil
}

// commandCodeLine parses one NDJSON or `data:` line. done reports that the line
// ends the inspection: [DONE], or the first content/finish event.
func commandCodeLine(line []byte) (event map[string]any, done bool) {
	text := strings.TrimSpace(string(line))
	if text == "" {
		return nil, false
	}
	text = strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	if text == "" || text == "[DONE]" {
		return nil, true
	}
	if json.Unmarshal([]byte(text), &event) != nil {
		return nil, false
	}
	switch event["type"] {
	case "text-delta", "reasoning-delta", "tool-input-start", "tool-call", "finish", "finish-step":
		return event, true
	}
	return event, false
}

// commandCodeFailure is the pin's replacement response (:219-236).
func commandCodeFailure(original *http.Response, failure CommandCodeError) *http.Response {
	original.Body.Close()
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"message": "[CommandCode error: " + failure.Message + "]",
		"type":    failure.Type,
		"code":    failure.Status,
	}})
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", failure.Status, http.StatusText(failure.Status)),
		StatusCode:    failure.Status,
		Proto:         original.Proto,
		ProtoMajor:    original.ProtoMajor,
		ProtoMinor:    original.ProtoMinor,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       original.Request,
	}
}
