package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// The integrity gate is executors/kiro.js:370-627 (attachIntegrityGate,
// runIntegrityRecovery, readIntegrityAttempt). The pin buffers the converted Chat
// SSE; here the raw EventStream is buffered and read by the same machine the
// response legs use, so the verdict and the bytes later converted cannot disagree.
const (
	// DefaultGateMaxBytes is KIRO_REPAIR_BUFFER_MAX_BYTES (executors/kiro.js:14).
	DefaultGateMaxBytes = 8 << 20
	// DefaultGateTimeout is STREAM_FIRST_CHUNK_TIMEOUT_MS, the default of both the
	// time-to-first-chunk and the stall timeout (executors/kiro.js:373-375).
	DefaultGateTimeout = 200 * time.Second
	// GateHeartbeat is KIRO_REPAIR_HEARTBEAT_MS (executors/kiro.js:15).
	GateHeartbeat = 10 * time.Second
	// shortFinalMaxChars is KIRO_SHORT_FINAL_MAX_CHARS (executors/kiro.js:16).
	shortFinalMaxChars = 800
	// retryErrorBodyBytes is the most of a failed retry's body that is reported
	// (executors/kiro.js:470, Math.min(maxBytes, 4096)).
	retryErrorBodyBytes = 4096
)

// Attempt kinds (executors/kiro.js:445-452, :596-626).
const (
	kindComplete        = "complete"
	kindPassThrough     = "pass_through"
	kindInvalidTool     = "invalid_tool"
	kindEllipsis        = "ellipsis"
	kindShortFinal      = "short_final"
	kindRetryableStop   = "retryable_stop"
	kindMissingTerminal = "missing_terminal"
)

// repairInstructions are REPAIR_INSTRUCTIONS (executors/kiro.js:41-45), verbatim.
var repairInstructions = map[string]string{
	"tool":        "Retry the previous response because its Kiro tool_call wrapper was malformed. If you use the wrapper tool named tool_call, its input must contain a non-empty name and an arguments field.",
	"ellipsis":    "Retry the previous response because it ended with only an ellipsis. Return the complete final answer, not only ... or ….",
	"short_final": "Retry the previous response because its final only announced a future action. Complete the check now and return the result or a concrete blocker.",
}

// RepairRequest is appendRepairInstruction (executors/kiro.js:134-143): the
// instruction is appended to the current user turn, never to a top-level
// systemPrompt, which kiro.dev answers with 400 REQUEST_BODY_INVALID (:130-133).
// An empty kind repeats the request unchanged (:455-457). Every other field of the
// body is kept as it was.
func RepairRequest(body []byte, kind string) ([]byte, error) {
	if kind == "" {
		return append([]byte(nil), body...), nil
	}
	instruction, known := repairInstructions[kind]
	if !known {
		return nil, fmt.Errorf("no Kiro repair instruction for %q", kind)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, errors.New("kiro request body is not an object")
	}
	// A body without a current user message is repeated unchanged (:138).
	steps := []string{"conversationState", "currentMessage", "userInputMessage"}
	objects := []map[string]json.RawMessage{root}
	for _, step := range steps {
		raw, set := objects[len(objects)-1][step]
		if !set {
			return append([]byte(nil), body...), nil
		}
		var next map[string]json.RawMessage
		if err := json.Unmarshal(raw, &next); err != nil || next == nil {
			return append([]byte(nil), body...), nil
		}
		objects = append(objects, next)
	}
	message := objects[len(objects)-1]
	var content string
	if raw, set := message["content"]; set {
		if err := json.Unmarshal(raw, &content); err != nil {
			return nil, errors.New("kiro current message content is not text")
		}
	}
	if content != "" {
		content += "\n\n"
	}
	encoded, err := json.Marshal(content + instruction)
	if err != nil {
		return nil, err
	}
	message["content"] = encoded
	for i := len(steps) - 1; i >= 0; i-- {
		raw, err := json.Marshal(objects[i+1])
		if err != nil {
			return nil, err
		}
		objects[i][steps[i]] = raw
	}
	return json.Marshal(root)
}

// GateConfig is what one gated attempt is bounded by.
type GateConfig struct {
	// Repair is the repair switch (executors/kiro.js:376-377). Off, a malformed tool
	// call is reported instead of retried; the other retries stay.
	Repair bool
	// MaxBytes bounds the EventStream held; zero is DefaultGateMaxBytes.
	MaxBytes int
	// TTFT and Stall bound the wait for the first chunk and between chunks; zero is
	// DefaultGateTimeout. A timeout is a missing terminal, as in the pin.
	TTFT, Stall time.Duration
	// HeartbeatEvery is how often Heartbeat runs while the gate holds the answer;
	// zero is GateHeartbeat.
	HeartbeatEvery time.Duration
}

// Retry sends the one repair request. It returns the response body and status; a
// status outside 2xx is reported with the start of that body.
type Retry func(ctx context.Context, kind string) (body io.ReadCloser, status int, err error)

func (c GateConfig) bounds() (max int, ttft, stall, beat time.Duration) {
	max, ttft, stall, beat = c.MaxBytes, c.TTFT, c.Stall, c.HeartbeatEvery
	if max <= 0 {
		max = DefaultGateMaxBytes
	}
	if ttft <= 0 {
		ttft = DefaultGateTimeout
	}
	if stall <= 0 {
		stall = DefaultGateTimeout
	}
	if beat <= 0 {
		beat = GateHeartbeat
	}
	return
}

func gateFailure(code, message string, details map[string]any) error {
	value := map[string]any{"type": "api_error", "code": code, "message": message}
	if details != nil {
		value["details"] = details
	}
	return &concerns.StreamFailure{Value: value}
}

type attempt struct {
	kind    string
	bytes   []byte
	message string
}

// Gate holds the upstream answer until it is known to be whole and usable
// (executors/kiro.js:437-505). It returns the EventStream to convert, or a
// concerns.StreamFailure the response leg reports in the client's wire. A
// protocol failure the response leg already reports itself (a refusal, an upstream
// error event, a stop that is final) is returned as the bytes, not retried. An
// answer that is cut short, malformed, only an ellipsis or only an announced
// action is retried exactly once through retry; a second such answer is the
// failure. src is closed before Gate returns. beat runs at once and then every
// HeartbeatEvery until the answer is released (:393-394); it may be nil.
func Gate(ctx context.Context, src io.ReadCloser, config GateConfig, options ResponseOptions, retry Retry, beat func()) (io.Reader, error) {
	max, ttft, stall, every := config.bounds()
	if beat == nil {
		beat = func() {}
	}
	beat()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	options.MaxToolBytes = max / 2
	if options.MaxToolBytes < 1 {
		options.MaxToolBytes = 1
	}

	first, err := readAttempt(ctx, src, max, ttft, stall, ticker.C, beat, options)
	if err != nil {
		return nil, err
	}
	switch first.kind {
	case kindComplete, kindPassThrough:
		return bytes.NewReader(first.bytes), nil
	case kindInvalidTool:
		if !config.Repair {
			return nil, gateFailure("invalid_kiro_tool_call", first.message, nil)
		}
	}

	repair := ""
	switch first.kind {
	case kindInvalidTool:
		repair = "tool"
	case kindEllipsis, kindShortFinal:
		repair = first.kind
	}
	if retry == nil {
		return nil, gateFailure("kiro_integrity_gate_failed", "Kiro integrity validation has no way to retry", nil)
	}
	body, status, err := retry(ctx, repair)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, gateFailure("kiro_integrity_retry_upstream_error", "Kiro integrity retry failed: "+err.Error(), map[string]any{"status": 502})
	}
	if status < 200 || status >= 300 {
		prefix, readErr := readPrefix(ctx, body, min(max, retryErrorBodyBytes), stall)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		_ = readErr // a stalled error body is reported without its text (:473-475)
		message := strings.TrimSpace(prefix)
		if message == "" {
			message = fmt.Sprintf("Kiro integrity retry failed with HTTP %d", status)
		}
		return nil, gateFailure("kiro_integrity_retry_upstream_error", message, map[string]any{"status": status})
	}
	second, err := readAttempt(ctx, body, max, ttft, stall, ticker.C, beat, options)
	if err != nil {
		return nil, err
	}
	if second.kind == kindComplete || second.kind == kindPassThrough {
		return bytes.NewReader(second.bytes), nil
	}
	code := "kiro_missing_terminal_retry_failed"
	switch second.kind {
	case kindEllipsis:
		code = "kiro_ellipsis_retry_failed"
	case kindShortFinal:
		code = "kiro_short_final_retry_failed"
	case kindInvalidTool:
		code = "kiro_tool_call_repair_retry_failed"
	}
	detail := second.message
	if detail == "" {
		detail = second.kind
	}
	return nil, gateFailure(code, "Kiro integrity validation failed after one bounded retry: "+detail,
		map[string]any{"attempts": []any{first.kind, second.kind}})
}

type gateChunk struct {
	data []byte
	err  error
}

// readAttempt reads one answer into memory, bounded in size and time, and says
// what it is. Only cancellation is returned as an error: everything else the
// answer can do is a verdict. The reader goroutine has ended, and src is closed,
// when readAttempt returns.
func readAttempt(ctx context.Context, src io.ReadCloser, max int, ttft, stall time.Duration, beatTick <-chan time.Time, beat func(), options ResponseOptions) (attempt, error) {
	chunks := make(chan gateChunk)
	quit := make(chan struct{})
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		buffer := make([]byte, 32<<10)
		for {
			n, err := src.Read(buffer)
			if n > 0 {
				select {
				case chunks <- gateChunk{data: append([]byte(nil), buffer[:n]...)}:
				case <-quit:
					return
				}
			}
			if err != nil {
				select {
				case chunks <- gateChunk{err: err}:
				case <-quit:
				}
				return
			}
		}
	}()
	finish := func() {
		close(quit)
		_ = src.Close()
		<-ended
	}
	defer finish()

	var raw []byte
	sawChunk := false
	for {
		wait := ttft
		if sawChunk {
			wait = stall
		}
		timer := time.NewTimer(wait)
		var next gateChunk
	receive:
		for {
			select {
			case next = <-chunks:
				break receive
			case <-beatTick:
				beat()
			case <-timer.C:
				timer.Stop()
				return attempt{kind: kindMissingTerminal, message: "Kiro integrity validation stalled"}, nil
			case <-ctx.Done():
				timer.Stop()
				return attempt{}, ctx.Err()
			}
		}
		timer.Stop()
		if next.err != nil {
			if next.err == io.EOF {
				break
			}
			if ctx.Err() != nil {
				return attempt{}, ctx.Err()
			}
			return attempt{kind: kindMissingTerminal, message: next.err.Error()}, nil
		}
		sawChunk = true
		if len(raw)+len(next.data) > max {
			return attempt{}, gateFailure("kiro_integrity_buffer_exceeded", fmt.Sprintf("Kiro integrity buffer exceeded %d bytes", max), nil)
		}
		raw = append(raw, next.data...)
	}
	return analyse(raw, options), nil
}

// readPrefix reads at most limit bytes of an error body (readResponsePrefix,
// executors/kiro.js:104-128) and closes it.
func readPrefix(ctx context.Context, body io.ReadCloser, limit int, stall time.Duration) (string, error) {
	if body == nil {
		return "", nil
	}
	result := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(io.LimitReader(body, int64(limit)))
		result <- data
	}()
	timer := time.NewTimer(stall)
	defer timer.Stop()
	select {
	case data := <-result:
		_ = body.Close()
		return string(data), nil
	case <-timer.C:
		_ = body.Close()
		<-result
		return "", errors.New("Kiro retry error body stalled")
	case <-ctx.Done():
		_ = body.Close()
		<-result
		return "", ctx.Err()
	}
}

// analyse is readIntegrityAttempt's verdict (executors/kiro.js:586-626) over a
// whole EventStream.
func analyse(raw []byte, options ResponseOptions) attempt {
	var content, reasoning strings.Builder
	hasTools := false
	state := newMachine(options, func(delta map[string]any) error {
		content.WriteString(concerns.Text(delta["content"]))
		reasoning.WriteString(concerns.Text(delta["reasoning_content"]))
		if len(concerns.Items(delta["tool_calls"])) > 0 {
			hasTools = true
		}
		return nil
	})
	err := concerns.ReadEventStream(context.Background(), bytes.NewReader(raw), state.handle)
	if err == nil {
		_, err = state.finish()
	}
	if err != nil {
		var failure *concerns.StreamFailure
		if !errors.As(err, &failure) {
			// A cut, corrupt or unterminated stream (:605-611, :614-616).
			return attempt{kind: kindMissingTerminal, message: err.Error()}
		}
		code := concerns.Text(failure.Value["code"])
		message := concerns.Text(failure.Value["message"])
		switch {
		case code == "invalid_kiro_tool_call" || code == "kiro_tool_use_without_call":
			return attempt{kind: kindInvalidTool, message: message}
		case code == "kiro_terminal_protocol_failure":
			return attempt{kind: kindRetryableStop, message: message}
		}
		// A refusal, an incomplete stop, an upstream error event, an over-bound
		// tool buffer or an event this leg refuses is final (:445-447): the
		// response leg reports it from the same bytes.
		return attempt{kind: kindPassThrough, bytes: raw, message: message}
	}
	if !hasTools {
		if isEllipsisOnly(content.String()) || (strings.TrimSpace(content.String()) == "" && isEllipsisOnly(reasoning.String())) {
			return attempt{kind: kindEllipsis}
		}
		if isShortFutureAction(content.String()) {
			return attempt{kind: kindShortFinal}
		}
	}
	return attempt{kind: kindComplete, bytes: raw}
}

// jsWhitespace is what JavaScript's \s and String.prototype.trim treat as space;
// Go's \s is ASCII only.
const jsWhitespace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// pattern compiles a pin regular expression: {S} is \s outside a class, {s} inside.
func pattern(source string) *regexp.Regexp {
	source = strings.ReplaceAll(source, "{S}", "["+jsWhitespace+"]")
	source = strings.ReplaceAll(source, "{s}", jsWhitespace)
	return regexp.MustCompile("(?i)" + source)
}

var (
	// SHORT_FUTURE_ACTION (executors/kiro.js:46).
	shortFutureAction = pattern(`^(?:(?:(?:現在|接著|接下來|下一步)[，,:：{s}]*(?:我(?:只)?(?:會|要|將|再)?{S}*)?|我只再)(?:補|查|確認|驗證|追(?:查|蹤)?|繼續|檢查|測試)|我(?:會|要|將)(?:再|重新)?(?:補(?:齊|查)?|抓取|查(?:詢)?|確認|驗證|追(?:查|蹤)?|繼續|檢查|測試)|(?:(?:next|now|then)\b[{s},:-]*)?(?:i(?:'ll| will| am going to| need to)|let me){S}+(?:verify|check|confirm|validate|investigate|trace|continue|follow up|test)\b)`)
	// OBSERVED_TRAILING_FUTURE_ACTION (:49).
	observedTrailingFutureAction = pattern(`^目前證據顯示(?s:.){1,700}[。.!?；;]{S}*最後補查{S}+504{S}+access{S}+log[，,]{S}*確認{S}+host[／/]路徑與是否為集中流量[。.!]?$`)
	// ENGLISH_FUTURE_ACTION (:50) and ENGLISH_RESULT_CLAUSE (:51).
	englishFutureAction = pattern(`^(?:(?:next|now|then)\b[{s},:-]*)?(?:i(?:'ll| will| am going to| need to)|let me){S}+(?:verify|check|confirm|validate|investigate|trace|continue|follow up|test)\b`)
	englishResultClause = pattern(`(?:[:;\n]|[.!?]{S}+\S|\b(?:status|checksum|response|deployment){S}+(?:is|are|was|were|matches?|equals?|returned)\b)`)
	// CHINESE_FUTURE_ACTION (:52) and CHINESE_RESULT_CLAUSE (:53).
	chineseFutureAction = pattern(`^(?:(?:現在|接著|接下來|下一步)[，,:：{s}]*(?:我(?:只)?(?:會|要|將|再)?{S}*)?|我只再|我(?:會|要|將)(?:再|重新)?)(?:補|抓取|查|確認|驗證|追|繼續|檢查|測試)`)
	chineseResultClause = pattern(`(?:[。！？]{S}*\S|(?:版本|狀態|回應|結果|部署|校驗碼)(?:是|為|等於|顯示))`)
	// USER_WAIT (:54), COMPLETED_FINAL (:55), RESULT_EVIDENCE (:56).
	userWait       = pattern(`(?:請(?:你|先)|你(?:先|需要|可以|提供|確認|批准|允許)|等待(?:你|使用者)|等你|核准|同意|授權|\b(?:after|when|once){S}+you\b|\byour{S}+(?:approval|confirmation|permission|input)\b|\bwait(?:ing)?{S}+for{S}+you\b|\bplease{S}+(?:approve|confirm|provide|send)\b)`)
	completedFinal = pattern(`(?:已(?:經)?完成|完成(?:了|驗證|確認)|修復完成|確認無誤|驗證(?:完成|通過)|測試(?:均)?通過|結論|總結|\b(?:done|completed|fixed|verified|confirmed|passed|in conclusion|summary)\b|\b(?:is|are) complete\b)`)
	resultEvidence = pattern(`(?:顯示|發現|因此|成功|失敗|正常|無錯誤|沒有錯誤|\b(?:found|shows?|showed|because|therefore|succeeded|failed|healthy|green|no errors?)\b)`)
)

func trimJS(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		switch {
		case r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' || r == ' ' || r == 0xa0 || r == 0x1680,
			r >= 0x2000 && r <= 0x200a, r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000 || r == 0xfeff:
			return true
		}
		return false
	})
}

// isEllipsisOnly is executors/kiro.js:185-187.
func isEllipsisOnly(value string) bool {
	text := trimJS(value)
	return text == "..." || text == "…"
}

// isShortFutureAction is executors/kiro.js:189-197. The length is in UTF-16 units,
// as JavaScript counts it.
func isShortFutureAction(value string) bool {
	text := strings.ReplaceAll(trimJS(value), "’", "'")
	if observedTrailingFutureAction.MatchString(text) {
		return true
	}
	if englishFutureAction.MatchString(text) && englishResultClause.MatchString(text) {
		return false
	}
	if chineseFutureAction.MatchString(text) && chineseResultClause.MatchString(text) {
		return false
	}
	length := len(utf16.Encode([]rune(text)))
	return length > 0 && length <= shortFinalMaxChars &&
		shortFutureAction.MatchString(text) && !userWait.MatchString(text) &&
		!completedFinal.MatchString(text) && !resultEvidence.MatchString(text)
}
