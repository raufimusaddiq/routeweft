package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
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
// Not replicated: V8's legacy Date.parse fallback for a Retry-After that is not a
// number (antigravity.js:364). It reads zone-less forms in the host's zone, so the
// pin's own answer differs from host to host. The forms a server sends are read: an
// HTTP-date, and the non-positive integers the fallback turns into a month or a year
// (legacyIntegerDate). Anything else is read as no date, so the next header is tried.

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
func AntigravityDo(client *http.Client, request *http.Request) (*http.Response, error) {
	body, err := replayableBody(request)
	if err != nil {
		return nil, err
	}
	ctx := request.Context()
	used := 0
	for {
		attempt := request.Clone(ctx)
		if attempt.Body, err = body(); err != nil {
			return nil, err
		}
		attempt.GetBody = body
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

// replayableBody returns a function that yields the request body as often as it is
// asked, without holding a copy when the request can already be replayed.
func replayableBody(request *http.Request) (func() (io.ReadCloser, error), error) {
	if request.Body == nil || request.Body == http.NoBody {
		return func() (io.ReadCloser, error) { return http.NoBody, nil }, nil
	}
	if request.GetBody != nil {
		request.Body.Close()
		return request.GetBody, nil
	}
	payload, err := io.ReadAll(request.Body)
	request.Body.Close()
	if err != nil {
		return nil, err
	}
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil }, nil
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
		if diff, valid := antigravityDate(text, numeric, now); valid {
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

// antigravityDate reads a Retry-After that was not a positive number as the pin's
// new Date(text) does, for the forms a server sends. It reports the milliseconds
// from now (negative when past) and whether text was a date at all.
func antigravityDate(text string, numeric bool, now time.Time) (int64, bool) {
	if numeric {
		return legacyIntegerDate(text, now)
	}
	for _, layout := range []string{"Mon, 02 Jan 2006 15:04:05 GMT", "Mon, 02 Jan 2006 15:04:05 -0700", "Monday, 02-Jan-06 15:04:05 GMT"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UnixMilli() - now.UnixMilli(), true
		}
	}
	return 0, false
}

// legacyIntegerDate is what V8 makes of an integer that parseInt read as not
// positive ("0", "-5", "-2030"): 1 to 12 is a month of 2000 or 2001 (past), 13 to
// 31 is no date, 32 to 49 is the year 20xx, 50 to 99 the year 19xx and 100 up a
// year as written, taken as 1 January in UTC. Text that is not an integer is no date.
func legacyIntegerDate(text string, now time.Time) (int64, bool) {
	digits := strings.TrimLeft(text, "+-")
	if len(text)-len(digits) > 1 || digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, false
	}
	value, _ := jsParseInt(digits)
	var year int64
	switch {
	case value <= 12:
		return -1, true
	case value <= 31:
		return 0, false
	case value <= 49:
		year = 2000 + value
	case value <= 99:
		year = 1900 + value
	default:
		year = value
	}
	if year > 275760 {
		return 0, false
	}
	return time.Date(int(year), time.January, 1, 0, 0, 0, 0, time.UTC).UnixMilli() - now.UnixMilli(), true
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
	for _, r := range text {
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
