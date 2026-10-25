package wire

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Antigravity retries a failed answer on the same URL before it is given up on:
// BaseExecutor.execute's tryRetry (executors/base.js:100-181) with the delay hook
// AntigravityExecutor.computeRetryDelay (executors/antigravity.js:356-446) and the
// provider's retry table.
//
// The pin retries 429, 500, 502, 503 and 504 (providers/registry/antigravity.js:27-37
// over DEFAULT_RETRY_CONFIG, runtimeConfig.js:82-87, merged at providers/schema.js:63),
// each up to its own number of attempts, counted together for the one URL
// (base.js:104,113,132). A thrown fetch error is retried as a 502 (base.js:172-173).
// The pin's transient-error patterns (antigravity.js:25-33,412-416) are not ported:
// the hook only runs for the statuses above, and every one of them is transient by
// status, so the patterns can never decide anything.
//
// Differences, each so a wait or a read cannot be unbounded:
//   - the pin's wait is a bare setTimeout (base.js:123) that a client leaving does
//     not cut short; the wait here ends with the request's context;
//   - the pin reads the whole error body (antigravity.js:427); the read here stops
//     at antigravityErrorLimit, and what it did not read is still the response's;
//   - the pin never closes the answer it retries on; this does.
//
// Each attempt is built again, as the pin's loop builds it (base.js:128-130): the
// IDE-format requestId carries the clock (antigravity.js:100-111), so a repeat has a
// new one. A Retry-After that is not a positive number is read as Date.parse reads
// it (antigravity_date.go).

const (
	// antigravityMaxRetryAfter is MAX_RETRY_AFTER_MS (antigravity.js:20): a longer
	// wait than this is not waited for, the answer stands.
	antigravityMaxRetryAfter = 10 * time.Second
	// antigravityTransientCap is ANTIGRAVITY_TRANSIENT_RETRY_MAX_MS (:21), the cap of
	// the backoff for a status other than 429.
	antigravityTransientCap = 15 * time.Second
	// antigravityNetworkDelay is the 502 row's delayMs (runtimeConfig.js:85), which
	// is the wait before repeating a request that threw (base.js:173).
	antigravityNetworkDelay = 3 * time.Second
	// antigravityErrorLimit bounds the error body read to find a duration.
	antigravityErrorLimit = 1 << 20
	// antigravityJSONDepth bounds the nesting read from an error body; a deeper
	// document is read as text only.
	antigravityJSONDepth = 100
	// maxSaturated keeps seconds-to-milliseconds in range: far above any wait
	// that is not vetoed.
	maxSaturated = int64(1) << 53
)

// antigravityRetries is the attempts per status: DEFAULT_RETRY_CONFIG overlaid with
// the provider's {429: 3, 500: 3, 503: 3}.
var antigravityRetries = map[int]int{
	http.StatusTooManyRequests:     3,
	http.StatusInternalServerError: 3,
	http.StatusBadGateway:          3,
	http.StatusServiceUnavailable:  3,
	http.StatusGatewayTimeout:      2,
}

// antigravityNow and antigravitySleep are replaced by tests.
var (
	antigravityNow   = time.Now
	antigravitySleep = func(ctx context.Context, delay time.Duration) error {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
)

// AntigravityDo sends the request and repeats it while the pin would (base.js:127-181).
func AntigravityDo(provider routing.ProviderRef, client *http.Client, request *http.Request) (*http.Response, error) {
	var payload []byte
	if request.Body != nil {
		var err error
		payload, err = io.ReadAll(request.Body)
		request.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	ctx := request.Context()
	used := 0
	for {
		attempt := request.Clone(ctx)
		body := withIDERequestID(payload, provider, antigravityNow())
		attempt.Body, attempt.GetBody, attempt.ContentLength = io.NopCloser(bytes.NewReader(body)), nil, int64(len(body))
		response, err := client.Do(attempt)
		if err != nil {
			// A client that left is not a failure to repeat (base.js:170).
			if ctx.Err() != nil || used >= antigravityRetries[http.StatusBadGateway] {
				return nil, err
			}
			used++
			if err := antigravitySleep(ctx, antigravityNetworkDelay); err != nil {
				return nil, err
			}
			continue
		}
		attempts, retriable := antigravityRetries[response.StatusCode]
		if !retriable || used >= attempts {
			return response, nil
		}
		head := peekAntigravityError(response)
		delay, repeat := antigravityRetryDelay(response.StatusCode, response.Header, head, used+1, antigravityNow())
		if !repeat {
			return response, nil
		}
		response.Body.Close()
		used++
		if err := antigravitySleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

var ideRequestID = regexp.MustCompile(`^agent/[^/]+/[0-9]+/[^/]+/[0-9]+$`)

// withIDERequestID is buildIdeRequestId (antigravity.js:100-111) as transformRequest
// puts it in the envelope (:309): an id already in the IDE form stays, any other is
// built from the session, the model, the length of the conversation and the clock.
// A body that is not a JSON object is returned as it is.
func withIDERequestID(payload []byte, provider routing.ProviderRef, now time.Time) []byte {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(payload, &envelope) != nil || envelope == nil {
		return payload
	}
	var current, model string
	_ = json.Unmarshal(envelope["requestId"], &current)
	if ideRequestID.MatchString(current) {
		return payload
	}
	_ = json.Unmarshal(envelope["model"], &model)
	var inner struct {
		SessionID string          `json:"sessionId"`
		Contents  json.RawMessage `json:"contents"`
	}
	_ = json.Unmarshal(envelope["request"], &inner)
	count := 1 // not a list: Array.isArray is false
	if bytes.HasPrefix(bytes.TrimSpace(inner.Contents), []byte("[")) {
		var list []json.RawMessage
		if json.Unmarshal(inner.Contents, &list) == nil {
			count = len(list)
		}
	}
	session := inner.SessionID
	if session == "" {
		session = provider.ConnectionID
	}
	if session == "" {
		session = "anonymous"
	}
	id := "agent/" + uuidFromSeed("antigravity:conversation:"+session) + "/" + strconv.FormatInt(now.UnixMilli(), 10) + "/" +
		uuidFromSeed("antigravity:trajectory:"+session+":"+model+":agent") + "/" + strconv.Itoa(max(1, count*2-1))
	encoded, err := json.Marshal(id)
	if err != nil {
		return payload
	}
	envelope["requestId"] = encoded
	out, err := json.Marshal(envelope)
	if err != nil {
		return payload
	}
	return out
}

// uuidFromSeed is the pin's (antigravity.js:92-98): the first 16 bytes of a SHA-256,
// marked as version 5.
func uuidFromSeed(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	sum[6] = sum[6]&0x0f | 0x50
	sum[8] = sum[8]&0x3f | 0x80
	hexed := hex.EncodeToString(sum[:16])
	return hexed[:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:]
}

// peekAntigravityError reads the head of an error body for the decision and puts it
// back ahead of the rest, so the caller still reads the whole body. A body that
// fails to read counts as no body, as the pin's failed clone().text() does (:426-431).
func peekAntigravityError(response *http.Response) []byte {
	head, err := io.ReadAll(io.LimitReader(response.Body, antigravityErrorLimit))
	response.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), response.Body), response.Body}
	if err != nil {
		return nil
	}
	return head
}

// antigravityRetryDelay is computeRetryDelay (antigravity.js:421-446). attempt counts
// from 1. It reports how long to wait, or false when the answer stands: a wait the
// upstream asked for that is longer than the cap is not waited for.
func antigravityRetryDelay(status int, header http.Header, body []byte, attempt int, now time.Time) (time.Duration, bool) {
	milliseconds, found := parseAntigravityRetryHeaders(header, now)
	if !found {
		milliseconds, found = parseAntigravityResetMessage(antigravityErrorText(body))
	}
	if found {
		if milliseconds > antigravityMaxRetryAfter.Milliseconds() {
			return 0, false
		}
		return time.Duration(milliseconds) * time.Millisecond, true
	}
	ceiling := antigravityTransientCap
	if status == http.StatusTooManyRequests {
		ceiling = antigravityMaxRetryAfter
	}
	return min(time.Second<<attempt, ceiling), true
}

// headerText is Headers.get: every value of the field, joined.
func headerText(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) == 0 {
		return "", false
	}
	return strings.Trim(strings.Join(values, ", "), " \t"), true
}

// parseAntigravityRetryHeaders is parseRetryHeaders (:356-385). A Retry-After that is
// not a positive number is tried as a date, and a date, past or not, ends the
// search; only a value that is no date goes on to the next header.
func parseAntigravityRetryHeaders(header http.Header, now time.Time) (int64, bool) {
	if text, present := headerText(header, "Retry-After"); present && text != "" {
		seconds, numeric := jsParseInt(text)
		if numeric && seconds > 0 {
			return seconds * 1000, true
		}
		if date, valid := jsDateParse(text); valid {
			diff := int64(date) - now.UnixMilli()
			return diff, diff > 0
		}
	}
	if text, present := headerText(header, "X-Ratelimit-Reset-After"); present && text != "" {
		if seconds, numeric := jsParseInt(text); numeric && seconds > 0 {
			return seconds * 1000, true
		}
	}
	if text, present := headerText(header, "X-Ratelimit-Reset"); present && text != "" {
		seconds, numeric := jsParseInt(text)
		if !numeric {
			return 0, false
		}
		diff := seconds*1000 - now.UnixMilli()
		return diff, diff > 0
	}
	return 0, false
}

const jsSpace = "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"

// jsParseInt is parseInt(text, 10): leading space, one sign, the digits that follow,
// whatever comes after them ignored. A value past maxSaturated reads as maxSaturated.
func jsParseInt(text string) (int64, bool) {
	text = strings.TrimLeft(text, jsSpace)
	negative := false
	if text != "" && (text[0] == '+' || text[0] == '-') {
		negative = text[0] == '-'
		text = text[1:]
	}
	var value int64
	digits := 0
	for ; digits < len(text) && text[digits] >= '0' && text[digits] <= '9'; digits++ {
		value = min(value*10+int64(text[digits]-'0'), maxSaturated)
	}
	if digits == 0 {
		return 0, false
	}
	if negative {
		value = -value
	}
	return value, true
}

// letters writes a literal with each ASCII letter as a pair, so a pattern is
// case-insensitive for ASCII only, as JavaScript's /i is without the u flag: Go's
// (?i) would also fold U+017F to "s" and U+212A to "k".
func letters(literal string) string {
	var b strings.Builder
	for _, r := range literal {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteString("[" + string(r) + string(r-32) + "]")
		case r >= 'A' && r <= 'Z':
			b.WriteString("[" + string(r+32) + string(r) + "]")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}

var resetAfterPattern = regexp.MustCompile(letters("reset after") + ` ([0-9]+` + letters("h") + `)?([0-9]+` + letters("m") + `)?([0-9]+` + letters("s") + `)?`)

// parseAntigravityResetMessage is parseRetryFromErrorMessage (:389-401): only the
// first "reset after" counts, and one with no duration after it is no answer.
func parseAntigravityResetMessage(message string) (int64, bool) {
	match := resetAfterPattern.FindStringSubmatch(message)
	if match == nil {
		return 0, false
	}
	var total int64
	for index, unit := range []int64{3600 * 1000, 60 * 1000, 1000} {
		if match[index+1] == "" {
			continue
		}
		count, _ := jsParseInt(match[index+1])
		if count > maxSaturated/unit {
			return maxSaturated, true
		}
		total = min(total+count*unit, maxSaturated)
	}
	return total, total > 0
}

// antigravityErrorText is extractErrorMessage (:403-410) over a body the pin parsed as
// JSON when it could: the error's message, the message, the error, then the body,
// those that are not empty, one per line.
func antigravityErrorText(body []byte) string {
	text := string(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
	var root *jsObject
	if text != "" && json.Valid([]byte(text)) {
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		if value, err := decodeJS(decoder, 0); err == nil {
			root, _ = value.(*jsObject)
		}
	}
	var failure any
	if root != nil {
		failure = root.values["error"]
	}
	inner, _ := failure.(*jsObject)
	var lines []string
	for _, candidate := range []any{inner.get("message"), root.get("message"), failure, text} {
		if line, truthy := jsText(candidate); truthy {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// jsObject is a parsed JSON object in the order JSON.stringify would write it back.
type jsObject struct {
	keys   []string
	values map[string]any
}

func (o *jsObject) get(key string) any {
	if o == nil {
		return nil
	}
	return o.values[key]
}

// decodeJS reads one value. A repeated key keeps its first place and its last value,
// as JSON.parse does.
func decodeJS(decoder *json.Decoder, depth int) (any, error) {
	if depth > antigravityJSONDepth {
		return nil, io.ErrUnexpectedEOF
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, nested := token.(json.Delim)
	if !nested {
		return token, nil
	}
	if delimiter == '[' {
		items := []any{}
		for decoder.More() {
			item, err := decodeJS(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		_, err = decoder.Token()
		return items, err
	}
	object := &jsObject{values: map[string]any{}}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		item, err := decodeJS(decoder, depth+1)
		if err != nil {
			return nil, err
		}
		name := key.(string)
		if _, seen := object.values[name]; !seen {
			object.keys = append(object.keys, name)
		}
		object.values[name] = item
	}
	_, err = decoder.Token()
	return object, err
}

// jsText is the pin's `filter(Boolean).map(v => typeof v === "string" ? v : JSON.stringify(v))`
// for one value: nothing for a falsy one.
func jsText(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "", false
	case bool:
		return "false", typed
	case string:
		return typed, typed != ""
	case json.Number:
		if number, _ := strconv.ParseFloat(string(typed), 64); number == 0 {
			return "", false
		}
	}
	var b strings.Builder
	writeJS(&b, value)
	return b.String(), true
}

// writeJS is JSON.stringify.
func writeJS(b *strings.Builder, value any) {
	switch typed := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(typed))
	case string:
		writeJSString(b, typed)
	case json.Number:
		number, _ := strconv.ParseFloat(string(typed), 64)
		b.WriteString(jsNumber(number))
	case float64:
		b.WriteString(jsNumber(typed))
	case int:
		b.WriteString(strconv.Itoa(typed))
	case []any:
		b.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				b.WriteByte(',')
			}
			writeJS(b, item)
		}
		b.WriteByte(']')
	case *jsObject:
		b.WriteByte('{')
		for index, key := range typed.order() {
			if index > 0 {
				b.WriteByte(',')
			}
			writeJSString(b, key)
			b.WriteByte(':')
			writeJS(b, typed.values[key])
		}
		b.WriteByte('}')
	}
}

// order is the key order of an object: the integer-like keys first, ascending, then
// the rest as they were written.
func (o *jsObject) order() []string {
	var indexes, others []string
	for _, key := range o.keys {
		if index, err := strconv.ParseUint(key, 10, 32); err == nil && index != math.MaxUint32 && strconv.FormatUint(index, 10) == key {
			indexes = append(indexes, key)
		} else {
			others = append(others, key)
		}
	}
	sort.Slice(indexes, func(i, j int) bool {
		a, _ := strconv.ParseUint(indexes[i], 10, 32)
		b, _ := strconv.ParseUint(indexes[j], 10, 32)
		return a < b
	})
	return append(indexes, others...)
}

func writeJSString(b *strings.Builder, text string) {
	b.WriteByte('"')
	for i := 0; i < len(text); {
		// A lone surrogate (a string cut inside a pair, kept as three WTF-8 bytes) is
		// written as the escape JSON.stringify gives it.
		if text[i] == 0xED && i+2 < len(text) && text[i+1] >= 0xA0 && text[i+1] <= 0xBF && text[i+2]&0xC0 == 0x80 {
			unit := 0xD000 | int(text[i+1]&0x3F)<<6 | int(text[i+2]&0x3F)
			b.WriteString(`\u` + strconv.FormatUint(uint64(unit), 16))
			i += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		i += size
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteString(strconv.FormatUint(uint64(r)+0x100, 16)[1:])
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// jsNumber is Number::toString: plain digits from 1e-6 up to 1e21, an exponent outside.
func jsNumber(number float64) string {
	switch {
	case math.IsInf(number, 0) || math.IsNaN(number):
		return "null"
	case number == 0:
		return "0"
	case math.Abs(number) >= 1e-6 && math.Abs(number) < 1e21:
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(number, 'e', -1, 64), "e")
	sign := exponent[:1]
	return mantissa + "e" + sign + strings.TrimLeft(exponent[1:], "0")
}
