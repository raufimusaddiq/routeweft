package wire

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Codex answers HTTP 200 and then says, inside the event stream, that it cannot
// serve the request. The pin reads the head of every Codex stream for the
// phrases that mean so, before the client has seen a byte (executors/codex.js:
// 17-27,280-386), and acts on what it finds:
//
//   - the model is at capacity: this account is not the one to ask, so the
//     response becomes a 503 and dispatch moves on (:312-316);
//   - the endpoint is overloaded or its protection is down: the same request is
//     repeated, up to three times, two seconds apart (the 503 row of
//     DEFAULT_RETRY_CONFIG, runtimeConfig.js:82-87), then it becomes a 503
//     (:317-325).
//
// A stream that has produced output is never repeated (:347-348): the client has
// seen it.
//
// Differences from the pin, each so a request is not refused for text it merely
// carries:
//   - the pin matches the phrases anywhere in the first 256 KiB, and the first
//     event of every Codex stream, response.created, echoes the request's own
//     instructions and tools. A prompt that quotes "model_at_capacity" would be
//     answered 503 on every account. Here the phrases are read only in the error of
//     an `error` or `response.failed` event;
//   - a stream that ends inside the window with no phrase is relayed whole, as the
//     pin does; so is one whose read fails, with the failure on the relayed body.

const (
	// codexInspectLimit is CODEX_SSE_PEEK_BYTES (:26).
	codexInspectLimit = 256 * 1024
	// codexRetries and codexRetryDelay are the 503 row of the default retry
	// config (runtimeConfig.js:85): three repeats, two seconds apart.
	codexRetries    = 3
	codexRetryDelay = 2 * time.Second
	// codexCapacityMessage is CODEX_MODEL_CAPACITY_MESSAGE (:27).
	codexCapacityMessage = "Selected model is at capacity. Please try a different model."
)

var (
	// codexRetryPhrases repeat the request (:18).
	codexRetryPhrases = []string{"server_is_overloaded", "service_unavailable_error", "response protection is unavailable"}
	// codexAccountPhrases fall back to another account (:19).
	codexAccountPhrases = []string{"selected model is at capacity", "model_at_capacity"}
)

// codexSleep is the retry delay, replaced by tests.
var codexSleep = func(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// codexVerdict is what the head of a stream said.
type codexVerdict struct {
	phrase  string // the matched phrase; empty when the stream is fine
	account bool   // the phrase asks for another account, not a repeat
	message string
}

// CodexDo sends the request and judges the head of its stream (execute,
// :280-327). The compact endpoint answers JSON and is not judged (:281).
func CodexDo(client *http.Client, request *http.Request) (*http.Response, error) {
	if strings.HasSuffix(strings.TrimRight(request.URL.Path, "/"), "/compact") {
		return client.Do(request)
	}
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
		response, err := client.Do(attemptRequest)
		if err != nil {
			return nil, err
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 || response.Body == nil {
			return response, nil
		}
		verdict := inspectCodexStream(response)
		if verdict.phrase == "" {
			return response, nil
		}
		if verdict.account {
			return codexUnavailable(response, verdict.message), nil
		}
		if attempt >= codexRetries {
			return codexUnavailable(response, verdict.message), nil
		}
		// inspectCodexStream closed the attempt's body, so its connection is free.
		if err := codexSleep(request.Context(), codexRetryDelay); err != nil {
			return nil, err
		}
	}
}

// inspectCodexStream reads the head of the stream and replaces the response's
// body with the bytes it read followed by the rest, so a relayed stream is
// byte-identical. It reads event by event until a phrase, output, a terminal
// event, the end of the stream or the limit. When it reports a phrase the body
// has been closed and must not be used.
func inspectCodexStream(response *http.Response) codexVerdict {
	var consumed bytes.Buffer
	reader := bufio.NewReader(io.TeeReader(io.LimitReader(response.Body, codexInspectLimit), &consumed))
	var verdict codexVerdict
	for verdict.phrase == "" {
		line, err := reader.ReadBytes('\n')
		if stop, found := judgeCodexLine(line); found.phrase != "" {
			verdict = found
			break
		} else if stop {
			break
		}
		if err != nil {
			break
		}
	}
	if verdict.phrase != "" {
		response.Body.Close()
		return verdict
	}
	response.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(consumed.Bytes()), response.Body), response.Body}
	return verdict
}

// judgeCodexLine reads one SSE line. stop reports that the line decides the
// stream is not an error to repeat: output the client would see, or the end.
func judgeCodexLine(line []byte) (stop bool, verdict codexVerdict) {
	text := strings.TrimSpace(string(line))
	if !strings.HasPrefix(text, "data:") {
		return false, codexVerdict{}
	}
	text = strings.TrimSpace(strings.TrimPrefix(text, "data:"))
	if text == "" || text == "[DONE]" {
		return text == "[DONE]", codexVerdict{}
	}
	var event struct {
		Type     string          `json:"type"`
		Error    json.RawMessage `json:"error"`
		Response struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(text), &event) != nil {
		return false, codexVerdict{}
	}
	switch event.Type {
	case "response.output_text.delta", "response.function_call_arguments.delta",
		"response.completed", "response.done", "response.incomplete":
		return true, codexVerdict{}
	case "error", "response.failed":
		detail := event.Error
		if event.Type == "response.failed" {
			detail = event.Response.Error
		}
		return false, codexErrorVerdict(detail)
	}
	return false, codexVerdict{}
}

// codexErrorVerdict matches the phrases in an error's own text. The account
// phrases are tried first, as the pin does (:349-351).
func codexErrorVerdict(detail json.RawMessage) codexVerdict {
	lower := strings.ToLower(string(detail))
	for _, phrase := range codexAccountPhrases {
		if strings.Contains(lower, phrase) {
			return codexVerdict{phrase: phrase, account: true, message: codexErrorMessage(detail, codexCapacityMessage)}
		}
	}
	for _, phrase := range codexRetryPhrases {
		if strings.Contains(lower, phrase) {
			return codexVerdict{phrase: phrase, message: codexErrorMessage(detail, phrase)}
		}
	}
	return codexVerdict{}
}

// codexErrorMessage is extractSseErrorMessage (:173-190): the capacity sentence
// when the error says it, else the error's own message, else the fallback.
func codexErrorMessage(detail json.RawMessage, fallback string) string {
	if strings.Contains(strings.ToLower(string(detail)), strings.ToLower(codexCapacityMessage)) {
		return codexCapacityMessage
	}
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(detail, &parsed) == nil && strings.TrimSpace(parsed.Message) != "" {
		return parsed.Message
	}
	var text string
	if json.Unmarshal(detail, &text) == nil && strings.TrimSpace(text) != "" {
		return text
	}
	return fallback
}

// codexUnavailable is codexSseErrorResponse (:192-203): a 503 whose body is ours.
func codexUnavailable(original *http.Response, message string) *http.Response {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"message": message,
		"type":    "server_error",
		"code":    "service_unavailable",
	}})
	header := original.Header.Clone()
	if header == nil {
		header = http.Header{}
	}
	for _, name := range []string{"Content-Length", "Content-Encoding", "Transfer-Encoding", "Content-Type"} {
		header.Del(name)
	}
	header.Set("Content-Type", "application/json")
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", http.StatusServiceUnavailable, http.StatusText(http.StatusServiceUnavailable)),
		StatusCode:    http.StatusServiceUnavailable,
		Proto:         original.Proto,
		ProtoMajor:    original.ProtoMajor,
		ProtoMinor:    original.ProtoMinor,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       original.Request,
	}
}
