package wire

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// The two web-chat providers (grok-web, perplexity-web) are not API gateways:
// Routeweft presents a pasted web-session cookie and a declared browser identity
// to a site that offers no API for this (#195). They share the cookie rules, the
// Chat message flattening and the Chat output framing below.

// ErrWebSession is every failure to present a session: the credential is absent
// or not a cookie value. It never carries the credential.
var ErrWebSession = errors.New("web session cookie is missing or not a valid cookie value")

// ValidCookieValue rejects cookie separators and control bytes in a credential
// that is sent as a Cookie header value.
func ValidCookieValue(value string) bool {
	for _, c := range value {
		if c < 0x21 || c > 0x7e || strings.ContainsRune("\";,\\", c) {
			return false
		}
	}
	return true
}

// webCookie is the Cookie header for a pasted session value. prefix is the
// cookie name the operator may have pasted along with the value.
func webCookie(name, token, prefix string) (string, error) {
	token = strings.TrimPrefix(strings.TrimSpace(token), prefix)
	if token == "" || !ValidCookieValue(token) {
		return "", ErrWebSession
	}
	return name + "=" + token, nil
}

// webMessage is one non-empty text message with the pin's role fold
// (developer is system).
type webMessage struct{ role, text string }

// webMessages reads Chat messages the way both pin executors do
// (E/grok-web.js:46-59, E/perplexity-web.js:138-152): string content or the text
// parts of an array joined by a space; messages with no text are dropped.
func webMessages(chat map[string]any) ([]webMessage, error) {
	raw, _ := chat["messages"].([]any)
	if len(raw) == 0 {
		return nil, errors.New("messages are missing or empty")
	}
	var out []webMessage
	for _, item := range raw {
		message, _ := item.(map[string]any)
		role, _ := message["role"].(string)
		if role == "" {
			role = "user"
		}
		if role == "developer" {
			role = "system"
		}
		var text string
		switch content := message["content"].(type) {
		case string:
			text = content
		case []any:
			var parts []string
			for _, part := range content {
				if p, _ := part.(map[string]any); p["type"] == "text" {
					s, _ := p["text"].(string)
					parts = append(parts, s)
				}
			}
			text = strings.Join(parts, " ")
		}
		if strings.TrimSpace(text) != "" {
			out = append(out, webMessage{role, text})
		}
	}
	return out, nil
}

// webChunkWriter frames Chat completion chunks as the pin's executors do
// (E/grok-web.js:135-188, E/perplexity-web.js:294-355).
type webChunkWriter struct {
	dst         io.Writer
	id, model   string
	created     int64
	fingerprint string
}

func newWebChunkWriter(dst io.Writer, idPrefix, model string) *webChunkWriter {
	return &webChunkWriter{dst: dst, id: "chatcmpl-" + idPrefix + "-" + uuid.NewString()[:12], model: model, created: time.Now().Unix()}
}

func (w *webChunkWriter) chunk(delta map[string]any, finish any) error {
	var fingerprint any
	if w.fingerprint != "" {
		fingerprint = w.fingerprint
	}
	body, err := json.Marshal(map[string]any{
		"id": w.id, "object": "chat.completion.chunk", "created": w.created, "model": w.model, "system_fingerprint": fingerprint,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish, "logprobs": nil}},
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w.dst, "data: %s\n\n", body)
	return err
}

func (w *webChunkWriter) done() error {
	if err := w.chunk(map[string]any{}, "stop"); err != nil {
		return err
	}
	return w.terminator()
}

func (w *webChunkWriter) terminator() error {
	_, err := io.WriteString(w.dst, "data: [DONE]\n\n")
	return err
}

// failed ends a stream the way the pin does: the failure is delivered as assistant
// text (E/grok-web.js:149-155, E/perplexity-web.js:309-314), then the stop chunk
// and [DONE]. The text is the site's own message, never the credential.
func (w *webChunkWriter) failed(message string) error {
	if err := w.chunk(map[string]any{"content": "[Error: " + message + "]"}, nil); err != nil {
		return err
	}
	return w.done()
}

// broke is the pin's catch (E/grok-web.js:177-182): the read itself failed.
func (w *webChunkWriter) broke(cause error) error {
	w.fingerprint = ""
	if err := w.chunk(map[string]any{"content": "[Stream error: " + cause.Error() + "]"}, "stop"); err != nil {
		return err
	}
	return w.terminator()
}

// estimateTokens is the pin's ceil(length/4) over JS string length (UTF-16 units).
// Neither site reports token counts, so this is an estimate, never a measurement:
// ParseUsage has no family for these providers and accounting stays unobserved.
func estimateTokens(text string) int {
	return (len(utf16.Encode([]rune(text))) + 3) / 4
}

// completion is the buffered Chat body, with the pin's usage estimate.
func (w *webChunkWriter) completion(content, reasoning string, promptTokens int) ([]byte, error) {
	message := map[string]any{"role": "assistant", "content": content}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	var fingerprint any
	if w.fingerprint != "" {
		fingerprint = w.fingerprint
	}
	completionTokens := estimateTokens(content)
	return json.Marshal(map[string]any{
		"id": w.id, "object": "chat.completion", "created": w.created, "model": w.model, "system_fingerprint": fingerprint,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": "stop", "logprobs": nil}},
		"usage":   map[string]any{"prompt_tokens": promptTokens, "completion_tokens": completionTokens, "total_tokens": promptTokens + completionTokens},
	})
}

// WebAnswerError is a failure the site reported inside an answer. A stream has
// already carried the pin's error frame when this is returned; a buffered answer
// is the pin's 502 with Body() (E/grok-web.js:197-200, E/perplexity-web.js:365-367),
// which the caller writes. It tells the dispatcher the attempt failed although the
// client was shown a 200 frame.
type WebAnswerError struct{ Message, Code string }

func (e *WebAnswerError) Error() string { return e.Message }

// Body is the pin's error document.
func (e *WebAnswerError) Body() []byte {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": e.Message, "type": "upstream_error", "code": e.Code}})
	return body
}

// webDo sends the request and replaces a non-2xx answer with the pin's own error
// document, keeping the status (E/grok-web.js:307-317, E/perplexity-web.js:467-477).
// The site's body is discarded, so an HTML challenge page is not relayed.
func webDo(describe func(status int) string) func(routing.ProviderRef, *http.Client, *http.Request) (*http.Response, error) {
	return func(_ routing.ProviderRef, client *http.Client, request *http.Request) (*http.Response, error) {
		response, err := client.Do(request)
		if err != nil || (response.StatusCode >= 200 && response.StatusCode < 300) {
			return response, err
		}
		_ = response.Body.Close()
		body, _ := json.Marshal(map[string]any{"error": map[string]any{
			"message": describe(response.StatusCode), "type": "upstream_error", "code": fmt.Sprintf("HTTP_%d", response.StatusCode),
		}})
		header := response.Header.Clone()
		for _, name := range []string{"Content-Length", "Content-Encoding", "Transfer-Encoding", "Content-Type"} {
			header.Del(name)
		}
		header.Set("Content-Type", "application/json")
		response.Header, response.Body, response.ContentLength = header, io.NopCloser(bytes.NewReader(body)), int64(len(body))
		return response, nil
	}
}

// webMaxLine bounds one upstream line; a longer one fails the answer.
const webMaxLine = 8 << 20

// webLines feeds each non-blank trimmed line of src to handle until it returns
// stop, the stream ends, or ctx is done. A read error is a failure, not an end.
func webLines(ctx context.Context, src io.Reader, handle func(line string) (stop bool, err error)) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64<<10), webMaxLine)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimRight(scanner.Text(), "\r")
		if stop, err := handle(line); err != nil || stop {
			return err
		}
	}
	return scanner.Err()
}

// marshalPlain is json.Marshal without HTML escaping, as JSON.stringify writes.
func marshalPlain(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}
