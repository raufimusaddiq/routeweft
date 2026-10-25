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
	kindTerminal        = "terminal_stop"
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
	// Content that is not text counts as empty, so the instruction replaces it
	// (executors/kiro.js:139-140: typeof content === "string" ? content : "").
	var content string
	if raw, set := message["content"]; set {
		if err := json.Unmarshal(raw, &content); err != nil {
			content = ""
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
	// MaxBytes bounds the converted output held (the pin counts the Chat SSE it
	// buffers, :570); zero is DefaultGateMaxBytes.
	MaxBytes int
	// TTFT and Stall bound the wait for the first validated frame and between
	// validated frames; zero is DefaultGateTimeout. A timeout is a missing
	// terminal, as in the pin.
	TTFT, Stall time.Duration
	// HeartbeatEvery is how often Heartbeat runs while the gate holds the answer;
	// zero is GateHeartbeat.
	HeartbeatEvery time.Duration
}

// Retry sends the one repair request. It returns the response body and status; a
// status outside 2xx is reported with the start of that body. An error is a
// transport failure after the request's own fallbacks (executors/base.js:100-184).
type Retry func(ctx context.Context, kind string) (body io.ReadCloser, status int, err error)

func (c GateConfig) bounds() (limit int, ttft, stall, beat time.Duration) {
	limit, ttft, stall, beat = c.MaxBytes, c.TTFT, c.Stall, c.HeartbeatEvery
	if limit <= 0 {
		limit = DefaultGateMaxBytes
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

// gateFailure is encodeSSEError (executors/kiro.js:199-206): type upstream_error,
// the code, the message and, when there are any, the details.
func gateFailure(code, message string, details map[string]any) error {
	value := map[string]any{"type": "upstream_error", "code": code, "message": message}
	if details != nil {
		value["details"] = details
	}
	return &concerns.StreamFailure{Value: value}
}

// attempt is what readIntegrityAttempt returns (:543-627).
type attempt struct {
	kind    string
	bytes   []byte
	message string
	diag    map[string]any
	// failure is the terminal failure of an answer that is not retried (:445-447).
	failure error
}

// Gate holds the upstream answer until it is known to be whole and usable
// (executors/kiro.js:437-505). It returns the EventStream to convert, or a
// concerns.StreamFailure the response leg reports in the client's wire. An answer
// that is cut short, malformed, only an ellipsis or only an announced action is
// retried exactly once through retry; a second such answer is the failure. A
// final stop, an upstream error event or an over-bound buffer is the failure
// at once. src is closed before Gate returns. beat runs at once and then every
// HeartbeatEvery until the answer is released (:393-394); it may be nil.
func Gate(ctx context.Context, src io.ReadCloser, config GateConfig, options ResponseOptions, retry Retry, beat func()) (io.Reader, error) {
	limit, ttft, stall, every := config.bounds()
	if beat == nil {
		beat = func() {}
	}
	beat()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	run := gateRun{limit: limit, ttft: ttft, stall: stall, tick: ticker.C, beat: beat, options: options}

	first, err := run.read(ctx, src, "initial")
	if err != nil {
		return nil, err
	}
	switch first.kind {
	case kindComplete:
		return bytes.NewReader(first.bytes), nil
	case kindTerminal:
		return nil, first.failure
	case kindInvalidTool:
		if !config.Repair {
			return nil, gateFailure("invalid_kiro_tool_call", first.message, first.diag)
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
	// The repair may wait a long time for response headers (and, below, for an error
	// body): the pin's heartbeat is a timer of its own, so it keeps running through
	// both waits (executors/kiro.js:393-394, :418-419). beat is only ever called from
	// this goroutine, so the writes it makes stay serialized with each other.
	sent, err := awaiting(ctx, run.tick, beat, func() repairResponse {
		body, status, err := retry(ctx, repair)
		return repairResponse{body, status, err}
	}, func(r repairResponse) {
		if r.body != nil {
			_ = r.body.Close()
		}
	})
	if err != nil {
		return nil, err
	}
	body, status, err := sent.body, sent.status, sent.err
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// The repair request itself threw: the gate reports it as its own failure (:407-416).
		message := err.Error()
		if message == "" {
			message = "Kiro integrity validation failed"
		}
		return nil, gateFailure("kiro_integrity_gate_failed", message, nil)
	}
	if status < 200 || status >= 300 {
		prefix, err := awaiting(ctx, run.tick, beat, func() string {
			return readPrefix(ctx, body, min(limit, retryErrorBodyBytes), stall)
		}, nil)
		if err != nil {
			return nil, err
		}
		message := prefix
		if message == "" {
			message = fmt.Sprintf("Kiro integrity retry failed with HTTP %d", status)
		}
		return nil, gateFailure("kiro_integrity_retry_upstream_error", message, map[string]any{"status": status})
	}
	second, err := run.read(ctx, body, "retry")
	if err != nil {
		return nil, err
	}
	switch second.kind {
	case kindComplete:
		return bytes.NewReader(second.bytes), nil
	case kindTerminal:
		return nil, second.failure
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
		map[string]any{"attempts": []any{first.diag, second.diag}})
}

type repairResponse struct {
	body   io.ReadCloser
	status int
	err    error
}

// awaiting runs work in its own goroutine and keeps the heartbeat going until it
// returns. On cancellation it still waits for work (which honours ctx) so that
// nothing is left running, and hands what work returned to discard.
func awaiting[T any](ctx context.Context, tick <-chan time.Time, beat func(), work func() T, discard func(T)) (T, error) {
	done := make(chan T, 1)
	go func() { done <- work() }()
	for {
		select {
		case value := <-done:
			return value, nil
		case <-tick:
			beat()
		case <-ctx.Done():
			value := <-done
			if discard != nil {
				discard(value)
			}
			var zero T
			return zero, ctx.Err()
		}
	}
}

// gateRun is the bounds and the heartbeat both attempts share.
type gateRun struct {
	limit       int
	ttft, stall time.Duration
	tick        <-chan time.Time
	beat        func()
	options     ResponseOptions
}

type gateChunk struct {
	data []byte
	err  error
}

// errStalled and errOverflow end a read; they are the gate's own verdicts and pass
// through the machine's error classification untouched.
type gateStop struct{ stalled bool }

func (e *gateStop) Error() string {
	if e.stalled {
		return "Kiro integrity validation stalled"
	}
	return "Kiro integrity buffer exceeded"
}
func (e *gateStop) PassThrough() {}

// gateSource is the reader the EventStream parser pulls from: it owns the timers,
// the heartbeat, the raw copy kept for the response leg, and the converted-output
// accounting the pin's bound is defined on.
type gateSource struct {
	ctx    context.Context
	run    *gateRun
	chunks chan gateChunk
	cur    []byte
	raw    []byte
	rawCap int
	last   time.Time
	m      *machine
	conv   *convertedSize

	framesSeen, deltasSeen int
}

func (g *gateSource) Read(p []byte) (int, error) {
	if len(g.cur) > 0 {
		n := copy(p, g.cur)
		g.cur = g.cur[n:]
		return n, nil
	}
	// A read that completed frames and produced nothing is acknowledged with the
	// keep-alive comment the pin writes, which counts against the bound (:1162-1164).
	if g.m.frames > g.framesSeen && g.m.deltas == g.deltasSeen {
		if err := g.conv.add(len(": kiro-upstream\n\n")); err != nil {
			return 0, err
		}
	}
	g.framesSeen, g.deltasSeen = g.m.frames, g.m.deltas
	for {
		// The watchdog restarts on every validated frame, not on every byte: a
		// partial frame is not progress (:559-566, kiro-terminal-integrity "treats
		// validated non-semantic frames as watchdog activity").
		budget := g.run.ttft
		if g.m.frames > 0 {
			budget = g.run.stall
		}
		timer := time.NewTimer(time.Until(g.last.Add(budget)))
		select {
		case next, open := <-g.chunks:
			timer.Stop()
			if !open || next.err == io.EOF {
				return 0, io.EOF
			}
			if next.err != nil {
				return 0, next.err
			}
			if len(g.raw)+len(next.data) > g.rawCap {
				return 0, &gateStop{}
			}
			g.raw = append(g.raw, next.data...)
			g.cur = next.data
			n := copy(p, g.cur)
			g.cur = g.cur[n:]
			return n, nil
		case <-g.run.tick:
			timer.Stop()
			g.run.beat()
		case <-timer.C:
			return 0, &gateStop{stalled: true}
		case <-g.ctx.Done():
			timer.Stop()
			return 0, g.ctx.Err()
		}
	}
}

// read reads one answer into memory, bounded in converted size, raw size and time,
// and says what it is (readIntegrityAttempt, :543-627). Only cancellation is
// returned as an error. The reader goroutine has ended, and src is closed, when
// read returns.
func (r *gateRun) read(ctx context.Context, src io.ReadCloser, label string) (attempt, error) {
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
	defer func() {
		close(quit)
		_ = src.Close()
		<-ended
	}()

	options := r.options
	options.MaxToolBytes = max(1, r.limit/2)
	conv := &convertedSize{limit: r.limit, model: options.Model}
	if conv.model == "" {
		conv.model = fallbackModel
	}
	var content, reasoning strings.Builder
	hasTools := false
	var m *machine
	m = newMachine(options, func(delta map[string]any) error {
		content.WriteString(concerns.Text(delta["content"]))
		reasoning.WriteString(concerns.Text(delta["reasoning_content"]))
		if len(concerns.Items(delta["tool_calls"])) > 0 {
			hasTools = true
			return conv.delta(delta)
		}
		// Text, reasoning and code are sized as the pin writes them (m.pinDelta), not as
		// Routeweft delivers them: the pin drops what is inside <thinking> tags.
		return nil
	})
	m.pinDelta = conv.delta
	source := &gateSource{ctx: ctx, run: r, chunks: chunks, last: time.Now(), m: m, conv: conv, rawCap: r.limit + concerns.MaxEventStreamMessageBytes}
	m.onFrame = func() { source.last = time.Now() }

	reason, err := m.run(ctx, source)
	if err == nil {
		// The finish chunk and the terminator are converted output too (:1121-1122).
		err = conv.finish(reason, m.usage)
	}
	raw := source.raw
	if ctx.Err() != nil {
		return attempt{}, ctx.Err()
	}
	if err != nil {
		return r.verdict(m, err, raw, label), nil
	}
	diag := m.diagnostics("", "clean_eof", "", 0)
	diag["attempt"] = label
	if !hasTools {
		if isEllipsisOnly(content.String()) || (trimJS(content.String()) == "" && isEllipsisOnly(reasoning.String())) {
			return attempt{kind: kindEllipsis, diag: diag}, nil
		}
		if isShortFutureAction(content.String()) {
			return attempt{kind: kindShortFinal, diag: diag}, nil
		}
	}
	return attempt{kind: kindComplete, bytes: raw, diag: diag}, nil
}

// verdict maps the way an answer ended to the pin's attempt kinds (:596-616).
func (r *gateRun) verdict(m *machine, err error, raw []byte, label string) attempt {
	var stop *gateStop
	if errors.As(err, &stop) {
		if !stop.stalled {
			message := fmt.Sprintf("Kiro integrity buffer exceeded %d bytes", r.limit)
			return attempt{kind: kindTerminal, failure: gateFailure("kiro_integrity_buffer_exceeded", message,
				map[string]any{"terminal_provenance": "integrity_buffer_exceeded"})}
		}
		// A timeout is a read error the gate itself raised (:526-540).
		diag := map[string]any{"attempt": label, "terminal_provenance": "transport_read_error", "transport_state": "upstream_error",
			"stop_reason": nil, "stop_disposition": dispIncomplete, "response_state": "no_semantic_output",
			"event_counts": map[string]any{}, "incomplete_frame_bytes": 0}
		return attempt{kind: kindMissingTerminal, message: stop.Error(), diag: diag}
	}
	var failure *concerns.StreamFailure
	if !errors.As(err, &failure) {
		return attempt{kind: kindMissingTerminal, message: err.Error()}
	}
	details, _ := failure.Value["details"].(map[string]any)
	if details != nil {
		details["attempt"] = label
	}
	code := concerns.Text(failure.Value["code"])
	message := concerns.Text(failure.Value["message"])
	switch code {
	case "invalid_kiro_tool_call":
		return attempt{kind: kindInvalidTool, message: message, diag: details}
	case "kiro_retryable_protocol_failure":
		return attempt{kind: kindRetryableStop, message: message, diag: details}
	case "kiro_missing_terminal":
		return attempt{kind: kindMissingTerminal, message: message, diag: details}
	}
	// A refusal, an incomplete stop, an unknown stop, an upstream error event, an
	// over-bound tool buffer, an event Routeweft refuses: final, reported as it is (:445-447).
	return attempt{kind: kindTerminal, failure: failure, message: message, diag: details}
}

// convertedSize counts the bytes of the Chat SSE the answer converts to, the unit
// the pin's bound is defined on (:569-577). Deltas are sized as the pin writes
// them: one envelope per delta, the role merged into the first.
type convertedSize struct {
	limit, total int
	model        string
	started      bool
}

func (c *convertedSize) add(n int) error {
	c.total += n
	if c.total > c.limit {
		return &gateStop{}
	}
	return nil
}

func (c *convertedSize) chunk(delta map[string]any, finish any, usage map[string]any) int {
	if !c.started {
		merged := map[string]any{"role": "assistant"}
		for key, value := range delta {
			merged[key] = value
		}
		delta, c.started = merged, true
	}
	document := map[string]any{"id": "chatcmpl-1700000000000", "object": "chat.completion.chunk", "created": 1700000000, "model": c.model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	if usage != nil {
		document["usage"] = usage
	}
	return len("data: ") + jsonLength(document) + len("\n\n")
}

// jsonLength is the byte length of JSON.stringify(value): Go's encoder escapes <, > and &
// and the line separators U+2028/U+2029, which JavaScript writes as they are.
func jsonLength(value any) int {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return 0
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	length := 0
	for i := 0; i < len(encoded); {
		switch {
		case encoded[i] != '\\':
			length++
			i++
		case i+5 < len(encoded) && encoded[i+1] == 'u' && encoded[i+2] == '2' && encoded[i+3] == '0' && encoded[i+4] == '2' && (encoded[i+5] == '8' || encoded[i+5] == '9'):
			length += 3 // U+2028 and U+2029 are three bytes of UTF-8
			i += 6
		default:
			length += 2 // any other escape pair, or the start of \uXXXX, is counted as written
			i += 2
		}
	}
	return length
}

func (c *convertedSize) delta(delta map[string]any) error { return c.add(c.chunk(delta, nil, nil)) }

func (c *convertedSize) finish(reason string, usage map[string]any) error {
	if err := c.add(c.chunk(map[string]any{}, reason, usage)); err != nil {
		return err
	}
	return c.add(len("data: [DONE]\n\n"))
}

// readPrefix reads at most limit bytes of an error body (readResponsePrefix,
// executors/kiro.js:104-128) and closes it. A body that stalls is reported
// without its text (:473-475).
func readPrefix(ctx context.Context, body io.ReadCloser, limit int, stall time.Duration) string {
	if body == nil {
		return ""
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
		return string(data)
	case <-timer.C:
	case <-ctx.Done():
	}
	_ = body.Close()
	<-result
	return ""
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
