package wire

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"
)

// The answer half of executors/qoder.js (wrapQoderSSE, :362-597) and
// shared/qoder/sse.js: Qoder answers `data: {statusCodeValue, body}` frames whose
// body is a Chat chunk or "[DONE]". They are unwrapped to plain Chat SSE, the finish
// and the usage frames (Qoder sends usage after the finish) are joined into one chunk,
// and the stream ends at the terminal frame although the agent keeps the socket open.

const sseDone = "data: [DONE]\n\n"

// QoderAnswerError is an upstream failure reported by the first frame. Nothing has
// been written when it is returned, so the caller answers with an HTTP error
// (executors/qoder.js:434-443); a billing block is a 403 so the account falls back.
type QoderAnswerError struct {
	Status  int     // the HTTP status the pin answers with
	Code    float64 // statusCodeValue, as the frame carried it
	Message string
	Billing bool
}

func (e *QoderAnswerError) Error() string { return e.Message }

// Body is the JSON error body, `{"error":{"message","code"}}`.
func (e *QoderAnswerError) Body() []byte {
	return []byte(stringifyJS(newJSObject().set("error", newJSObject().set("message", e.Message).set("code", e.Code))))
}

// QoderStreamOptions are the inputs to QoderUnwrapSSE.
type QoderStreamOptions struct {
	// Model is the name the chunks carry when the upstream gave none, "qoder/<key>".
	Model string
	// Now stamps the synthetic chunks; nil is the wall clock.
	Now func() time.Time
	// Logf receives the one line the pin writes for an error frame; nil is silent.
	Logf func(format string, args ...any)
}

var qoderLegacyBilling = regexp.MustCompile(`"code"\s*:\s*"(112|10605)"`)

// IsQoderBillingBlock is isBillingBlock (:342-354): the daily count (110), exhausted
// quota (112) and queue throttle (10605) codes, or a pricingUrl.
func IsQoderBillingBlock(inner string) bool {
	if inner == "" {
		return false
	}
	if strings.Contains(strings.ToLower(inner), "pricingurl") {
		return true
	}
	if parsed, err := parseJS([]byte(inner)); err == nil {
		code := ""
		if o, ok := parsed.(*jsObject); ok {
			if v := o.get("code"); v != nil {
				code = jsToString(v)
			}
		}
		if code == "110" || code == "112" || code == "10605" {
			return true
		}
	}
	return qoderLegacyBilling.MatchString(inner)
}

// qoderFrame reads one data frame: statusCodeValue as Number() sees it (NaN when it
// is absent or not a number) and the inner body as text. ok is false when the frame
// is not JSON; null is a frame whose JSON is null.
func qoderFrame(data string) (status float64, inner string, ok bool, null bool) {
	value, err := parseJS([]byte(data))
	if err != nil {
		return 0, "", false, false
	}
	if value == nil {
		return 0, "", true, true
	}
	status = math.NaN()
	if obj, isObj := value.(*jsObject); isObj {
		// A missing statusCodeValue is undefined, which is NaN; a null one is 0.
		if v, present := obj.lookup("statusCodeValue"); present {
			status = jsToNumber(v)
		}
		switch body := obj.get("body").(type) {
		case string:
			inner = body
		case nil:
		default:
			inner = stringifyJS(body)
		}
	}
	return status, inner, true, false
}

func qoderDataLine(raw string) (data string, isData bool) {
	line := jsTrim(strings.TrimSuffix(raw, "\r"))
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	return strings.TrimLeftFunc(line[5:], isJSSpace), true
}

// QoderUnwrapSSE converts Qoder's SSE body to Chat SSE. A failure on the first frame
// is returned as *QoderAnswerError before anything is written. After that it writes
// until the terminal frame, an end of stream or a failed read (the pin ends the
// stream cleanly on those), and returns nil; the caller closes src. A write error or
// a cancelled context is returned as it is. A read error before the first data frame
// is returned, as the pin's peek lets it escape.
func QoderUnwrapSSE(ctx context.Context, src io.Reader, dst io.Writer, opts QoderStreamOptions) error {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &qoderStream{dst: dst, opts: opts, coalescer: newQoderCoalescer(opts.Model, opts.Now)}
	reader := bufio.NewReader(src)
	peeking := true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, readErr := reader.ReadString('\n')
		if line != "" {
			line = strings.TrimSuffix(strings.ToValidUTF8(line, "\uFFFD"), "\n")
			if data, isData := qoderDataLine(line); peeking && isData {
				peeking = false
				if err := peekQoderFrame(data); err != nil {
					return err
				}
			}
			ended, err := s.processLine(line)
			if err != nil {
				return err
			}
			if ended {
				return nil
			}
		}
		if readErr != nil {
			if peeking && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			return s.finish()
		}
	}
}

// peekQoderFrame is peekFirstQoderFrame (:362-399) for the first data line.
func peekQoderFrame(data string) error {
	if data == "[DONE]" {
		return nil
	}
	status, inner, ok, null := qoderFrame(data)
	if !ok || null {
		return nil
	}
	if math.IsNaN(status) {
		status = 200
	}
	if status == 200 {
		return nil
	}
	billing := IsQoderBillingBlock(inner)
	message := inner
	if message == "" {
		message = "upstream status " + jsNumberString(status)
	}
	code := 502
	switch {
	case billing:
		code = 403
	case status == math.Trunc(status) && status >= 400 && status <= 599:
		code = int(status)
	}
	return &QoderAnswerError{Status: code, Code: status, Message: message, Billing: billing}
}

type qoderStream struct {
	dst       io.Writer
	opts      QoderStreamOptions
	coalescer *qoderCoalescer
	done      bool
}

func (s *qoderStream) write(text string) error {
	_, err := io.WriteString(s.dst, text)
	return err
}

// finish is the pin's `finally` (:574-581) when the stream ends without a terminal frame.
func (s *qoderStream) finish() error {
	if s.done {
		return nil
	}
	s.done = true
	return s.coalescer.flush(s.write)
}

// processLine is processLine (:458-518); it reports whether the stream is over.
func (s *qoderStream) processLine(raw string) (bool, error) {
	data, ok := qoderDataLine(raw)
	if !ok || s.done {
		return s.done, nil
	}
	if data == "[DONE]" {
		err := s.coalescer.flush(s.write)
		s.done = s.coalescer.doneEmitted
		return s.done, err
	}
	status, inner, ok, null := qoderFrame(data)
	if !ok {
		return false, nil
	}
	if null {
		// envelope.statusCodeValue on null throws in the pin, which ends the stream.
		return true, s.finish()
	}
	if math.IsNaN(status) || status == 0 {
		status = 200
	}
	if status != 200 {
		return true, s.errorFrame(status, inner)
	}
	if inner == "" {
		return false, nil
	}
	err := s.coalescer.handleInner(inner, s.write)
	s.done = s.coalescer.doneEmitted
	return s.done, err
}

func (s *qoderStream) errorFrame(status float64, inner string) error {
	s.done = true
	if s.opts.Logf != nil {
		prefix, _ := sliceUTF16(inner, 300)
		s.opts.Logf("[QODER] error envelope status=%s body=%s", jsNumberString(status), prefix)
	}
	if IsQoderBillingBlock(inner) {
		message := inner
		if message == "" {
			message = fmt.Sprintf("qoder billing block (%s)", jsNumberString(status))
		}
		chunk := newJSObject().set("error", newJSObject().
			set("message", message).set("code", "qoder_billing_block").set("status", float64(403)).set("type", "quota_error"))
		if err := s.write("data: " + stringifyJS(chunk) + "\n\n"); err != nil {
			return err
		}
		return s.write(sseDone)
	}
	message := inner
	if message == "" {
		message = "upstream status " + jsNumberString(status)
	}
	now := s.opts.Now()
	cut := truncateUTF16(message, 200)
	chunk := newJSObject().
		set("id", fmt.Sprintf("qoder-error-%d", now.UnixMilli())).
		set("object", "chat.completion.chunk").
		set("created", float64(now.Unix())).
		set("model", s.opts.Model).
		set("choices", []any{newJSObject().set("index", float64(0)).
			set("delta", newJSObject().set("content", "\n[qoder error "+jsNumberString(status)+": "+cut+"]")).
			set("finish_reason", "stop")})
	if err := s.write("data: " + stringifyJS(chunk) + "\n\n"); err != nil {
		return err
	}
	return s.write(sseDone)
}

// ---- the coalescer (shared/qoder/sse.js) ---------------------------------------

type qoderCoalescer struct {
	model                  string
	now                    func() time.Time
	pendingFinish          any
	pendingUsage           *jsObject
	metaID                 string
	metaCreated            float64
	metaModel              string
	doneEmitted            bool
	finishAlreadyForwarded bool
}

func newQoderCoalescer(model string, now func() time.Time) *qoderCoalescer {
	return &qoderCoalescer{model: model, now: now, metaModel: model}
}

func (c *qoderCoalescer) emitRaw(write func(string) error, text string) error {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", ""), "\n", "")
	return write("data: " + text + "\n\n")
}

func (c *qoderCoalescer) emitDone(write func(string) error) error {
	if c.doneEmitted {
		return nil
	}
	c.doneEmitted = true
	return write(sseDone)
}

func (c *qoderCoalescer) emitTerminal(write func(string) error) error {
	if c.pendingFinish == nil && c.pendingUsage == nil {
		return nil
	}
	id := c.metaID
	if id == "" {
		id = fmt.Sprintf("qoder-%d", c.now().UnixMilli())
	}
	created := c.metaCreated
	if created == 0 {
		created = float64(c.now().Unix())
	}
	model := c.metaModel
	if model == "" {
		model = c.model
	}
	finish := c.pendingFinish
	if !jsIsTruthy(finish) {
		finish = "stop"
	}
	chunk := newJSObject().
		set("id", id).set("object", "chat.completion.chunk").set("created", created).set("model", model).
		set("choices", []any{newJSObject().set("index", float64(0)).set("delta", newJSObject()).set("finish_reason", finish)})
	if c.pendingUsage != nil {
		chunk.set("usage", c.pendingUsage)
	}
	c.pendingFinish, c.pendingUsage = nil, nil
	return c.emitRaw(write, stringifyJS(chunk))
}

func (c *qoderCoalescer) flush(write func(string) error) error {
	if c.doneEmitted {
		return nil
	}
	if c.pendingUsage != nil || (jsIsTruthy(c.pendingFinish) && !c.finishAlreadyForwarded) {
		if err := c.emitTerminal(write); err != nil {
			return err
		}
	}
	return c.emitDone(write)
}

func firstChoice(parsed *jsObject) *jsObject {
	switch choices := parsed.get("choices").(type) {
	case []any:
		if len(choices) > 0 {
			choice, _ := choices[0].(*jsObject)
			return choice
		}
	case *jsObject:
		choice, _ := choices.get("0").(*jsObject)
		return choice
	}
	return nil
}

func finishReasonOf(parsed *jsObject) any {
	choice := firstChoice(parsed)
	for _, v := range []any{choice.get("finish_reason"), prop(choice.get("delta"), "finish_reason"), parsed.get("finish_reason")} {
		if jsIsTruthy(v) {
			return v
		}
	}
	return nil
}

func hasValuableDelta(parsed *jsObject) bool {
	delta, ok := firstChoice(parsed).get("delta").(*jsObject)
	if !ok {
		return false
	}
	if s, ok := delta.get("content").(string); ok && s != "" {
		return true
	}
	if s, ok := delta.get("reasoning_content").(string); ok && s != "" {
		return true
	}
	if calls, ok := delta.get("tool_calls").([]any); ok && len(calls) > 0 {
		return true
	}
	return jsIsTruthy(delta.get("role"))
}

// handleInner takes one unwrapped frame body (the pin's handleInner, sse.js:149-199).
func (c *qoderCoalescer) handleInner(inner string, write func(string) error) error {
	if c.doneEmitted {
		return nil
	}
	if inner == "[DONE]" {
		return c.flush(write)
	}
	value, err := parseJS([]byte(inner))
	if err != nil {
		return c.emitRaw(write, inner)
	}
	parsed, ok := value.(*jsObject)
	if !ok {
		return nil
	}
	if id, ok := parsed.get("id").(string); ok && id != "" {
		c.metaID = id
	}
	if created, ok := jsNumeric(parsed.get("created")); ok {
		c.metaCreated = created
	}
	if model, ok := parsed.get("model").(string); ok && model != "" {
		c.metaModel = model
	}
	if usage := canonicalQoderUsage(parsed.get("usage")); usage != nil {
		c.pendingUsage = usage
	}
	finish := finishReasonOf(parsed)
	if hasValuableDelta(parsed) {
		if err := c.emitRaw(write, inner); err != nil {
			return err
		}
		if finish != nil {
			c.finishAlreadyForwarded = true
			c.pendingFinish = nil
			if c.pendingUsage != nil {
				c.pendingFinish = finish
			}
		}
		if c.pendingFinish != nil && c.pendingUsage != nil {
			if err := c.emitTerminal(write); err != nil {
				return err
			}
			return c.emitDone(write)
		}
		return nil
	}
	if finish != nil {
		c.pendingFinish = finish
	}
	if (c.pendingFinish != nil || c.finishAlreadyForwarded) && c.pendingUsage != nil {
		if c.pendingFinish == nil {
			c.pendingFinish = "stop"
		}
		if err := c.emitTerminal(write); err != nil {
			return err
		}
		return c.emitDone(write)
	}
	return nil
}

// qoderOperand is one side of a `??` chain: undefined when the key is missing.
type qoderOperand struct {
	value   any
	present bool
}

func at(o *jsObject, key string) qoderOperand {
	v, ok := o.lookup(key)
	return qoderOperand{v, ok}
}

// coalesce is `a ?? b ?? c`: the first operand that is not null or undefined, else the last.
func coalesce(operands ...qoderOperand) qoderOperand {
	for _, op := range operands {
		if op.value != nil {
			return op
		}
	}
	return operands[len(operands)-1]
}

// num is the pin's num(): Number(v) when finite, and null (false) for undefined.
func (op qoderOperand) num() (float64, bool) {
	if !op.present {
		return 0, false
	}
	return jsFinite(op.value)
}

// canonicalQoderUsage is canonicalizeQoderUsage (sse.js:23-68): Qoder's or OpenAI's
// usage in the shape the rest of the pipeline reads, with the cached count taken from
// whichever spelling carries it.
func canonicalQoderUsage(value any) *jsObject {
	usage, ok := value.(*jsObject)
	if !ok {
		return nil
	}
	prompt, hasPrompt := coalesce(at(usage, "prompt_tokens"), at(usage, "input_tokens")).num()
	completion, hasCompletion := coalesce(at(usage, "completion_tokens"), at(usage, "output_tokens")).num()
	if !hasPrompt && !hasCompletion {
		return nil
	}
	details := newJSObject()
	switch d := usage.get("prompt_tokens_details").(type) {
	case *jsObject, []any:
		details = spreadObject(d)
	}
	cached, hasCached := coalesce(at(details, "cached_tokens"), at(usage, "cached_tokens"), at(usage, "prompt_cache_hit_tokens"), at(usage, "cache_read_input_tokens")).num()
	creation, hasCreation := coalesce(at(details, "cache_creation_tokens"), at(usage, "cache_creation_input_tokens")).num()

	total, hasTotal := at(usage, "total_tokens").num()
	if !hasTotal {
		total = prompt + completion
	}
	out := newJSObject().set("prompt_tokens", prompt).set("completion_tokens", completion).set("total_tokens", total)
	if hasCached {
		out.set("cached_tokens", cached)
		details.set("cached_tokens", cached)
	}
	if hasCreation {
		details.set("cache_creation_tokens", creation)
	}
	if len(details.keys) > 0 {
		out.set("prompt_tokens_details", details)
	}
	completionDetails := usage.get("completion_tokens_details")
	switch completionDetails.(type) {
	case *jsObject, []any:
		out.set("completion_tokens_details", completionDetails)
	}
	nested := qoderOperand{}
	if cd, ok := completionDetails.(*jsObject); ok {
		nested = at(cd, "reasoning_tokens")
	}
	if reasoning, ok := coalesce(at(usage, "reasoning_tokens"), nested).num(); ok {
		out.set("reasoning_tokens", reasoning)
	}
	return out
}
