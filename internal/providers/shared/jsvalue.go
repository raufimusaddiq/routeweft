package shared

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// The helpers below read one JSON value the way the reference's usage readers
// read a parsed JavaScript value (open-sse/services/usage/shared.js), so a
// reader ported from it keeps the reference's aliases and precedence: `a || b`
// takes the first truthy value, `a ?? b` the first non-nullish one.

// Nullish reports a value JavaScript's `??` skips: absent or null.
func Nullish(raw json.RawMessage) bool {
	text := bytes.TrimSpace(raw)
	return len(text) == 0 || string(text) == "null"
}

// Truthy reports a value JavaScript's `||` keeps: not absent, null, false, 0
// or the empty string. Objects and arrays are truthy even when empty.
func Truthy(raw json.RawMessage) bool {
	text := bytes.TrimSpace(raw)
	switch {
	case Nullish(text), string(text) == "false", string(text) == `""`:
		return false
	case text[0] == '-' || (text[0] >= '0' && text[0] <= '9'):
		value, err := strconv.ParseFloat(string(text), 64)
		return err != nil || value != 0
	}
	return true
}

// FirstTruthy is `a || b || ...`: the first truthy value, or nil.
func FirstTruthy(values ...json.RawMessage) json.RawMessage {
	for _, value := range values {
		if Truthy(value) {
			return value
		}
	}
	return nil
}

// FirstNonNullish is `a ?? b ?? ...`: the first non-nullish value, or nil.
func FirstNonNullish(values ...json.RawMessage) json.RawMessage {
	for _, value := range values {
		if !Nullish(value) {
			return value
		}
	}
	return nil
}

// Field is `value?.[key]` on a JSON object; anything else has no fields.
func Field(raw json.RawMessage, key string) json.RawMessage {
	object, ok := Object(raw)
	if !ok {
		return nil
	}
	return object[key]
}

// Object decodes a JSON object. Arrays, scalars and null are not objects.
func Object(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	text := bytes.TrimSpace(raw)
	if len(text) == 0 || text[0] != '{' {
		return nil, false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(text, &object) != nil {
		return nil, false
	}
	return object, true
}

// Array decodes a JSON array (Array.isArray).
func Array(raw json.RawMessage) ([]json.RawMessage, bool) {
	text := bytes.TrimSpace(raw)
	if len(text) == 0 || text[0] != '[' {
		return nil, false
	}
	var array []json.RawMessage
	if json.Unmarshal(text, &array) != nil {
		return nil, false
	}
	return array, true
}

// IsNumber reports `typeof value === "number"` for a finite JSON number.
func IsNumber(raw json.RawMessage) (float64, bool) {
	text := bytes.TrimSpace(raw)
	if len(text) == 0 || !(text[0] == '-' || (text[0] >= '0' && text[0] <= '9')) {
		return 0, false
	}
	value, err := strconv.ParseFloat(string(text), 64)
	return value, err == nil && !math.IsInf(value, 0)
}

// FiniteNumber is the reference's toFiniteNumber (shared.js:45-52): a finite
// number, or a non-blank string that converts to one.
func FiniteNumber(raw json.RawMessage) (float64, bool) {
	if value, ok := IsNumber(raw); ok {
		return value, true
	}
	var text string
	if json.Unmarshal(bytes.TrimSpace(raw), &text) != nil || strings.TrimSpace(text) == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, false
	}
	return value, true
}

// String is `String(value)` for the scalar values a provider label carries: a
// string as is, a number or boolean in its JavaScript spelling. Other values
// give "", which no reference label matches.
func String(raw json.RawMessage) string {
	text := bytes.TrimSpace(raw)
	var value string
	if json.Unmarshal(text, &value) == nil {
		return value
	}
	if number, ok := IsNumber(text); ok {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	if string(text) == "true" || string(text) == "false" {
		return string(text)
	}
	return ""
}

// Number is JavaScript's Number(value) on a JSON value: absent is NaN, null is
// 0, a boolean is 0 or 1, a string is read as a numeric literal (blank is 0), a
// one-element array is its element, and anything else is NaN. The result may be
// NaN or infinite, as in JavaScript, so a caller ports `Number(x) || 0` as
// OrZero(Number(x)).
func Number(raw json.RawMessage) float64 {
	text := bytes.TrimSpace(raw)
	switch {
	case len(text) == 0:
		return math.NaN()
	case string(text) == "null", string(text) == "false":
		return 0
	case string(text) == "true":
		return 1
	}
	if text[0] == '-' || (text[0] >= '0' && text[0] <= '9') {
		value, err := strconv.ParseFloat(string(text), 64)
		if err != nil && !math.IsInf(value, 0) {
			return math.NaN()
		}
		return value
	}
	if array, ok := Array(text); ok {
		switch len(array) {
		case 0:
			return 0
		case 1:
			if element := bytes.TrimSpace(array[0]); string(element) != "true" && string(element) != "false" {
				if Nullish(element) {
					return 0
				}
				return Number(element)
			}
		}
		return math.NaN()
	}
	var value string
	if json.Unmarshal(text, &value) != nil {
		return math.NaN()
	}
	return stringNumber(value)
}

// stringNumber is ECMA-262 StringToNumber for the literals providers send.
func stringNumber(text string) float64 {
	text = strings.TrimSpace(text)
	switch text {
	case "":
		return 0
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(text) > 2 && text[0] == '0' {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[text[1]]
		if base != 0 {
			value, err := strconv.ParseUint(text[2:], base, 64)
			if err != nil {
				return math.NaN()
			}
			return float64(value)
		}
	}
	if !decimalLiteral(text) {
		return math.NaN()
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil && !math.IsInf(value, 0) {
		return math.NaN()
	}
	return value
}

// decimalLiteral accepts [+-] digits [. digits] [e [+-] digits], with digits on
// at least one side of the point: the decimal forms JavaScript reads and
// strconv.ParseFloat also reads, without Go's extra forms (inf, nan, 1_000, hex
// floats).
func decimalLiteral(text string) bool {
	i := 0
	if i < len(text) && (text[i] == '+' || text[i] == '-') {
		i++
	}
	digits := 0
	for ; i < len(text) && text[i] >= '0' && text[i] <= '9'; i++ {
		digits++
	}
	if i < len(text) && text[i] == '.' {
		for i++; i < len(text) && text[i] >= '0' && text[i] <= '9'; i++ {
			digits++
		}
	}
	if digits == 0 {
		return false
	}
	if i < len(text) && (text[i] == 'e' || text[i] == 'E') {
		i++
		if i < len(text) && (text[i] == '+' || text[i] == '-') {
			i++
		}
		exponent := 0
		for ; i < len(text) && text[i] >= '0' && text[i] <= '9'; i++ {
			exponent++
		}
		if exponent == 0 {
			return false
		}
	}
	return i == len(text)
}

// OrZero is `value || 0` on a number: NaN and zero become 0.
func OrZero(value float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	return value
}

// Template is `${value}` (String(value)) on a JSON value: absent is
// "undefined", null is "null", a scalar is its JavaScript spelling, an object
// is "[object Object]" and an array joins its elements with commas, a null
// element spelled "".
func Template(raw json.RawMessage) string {
	text := bytes.TrimSpace(raw)
	switch {
	case len(text) == 0:
		return "undefined"
	case string(text) == "null":
		return "null"
	case text[0] == '{':
		return "[object Object]"
	}
	if array, ok := Array(text); ok {
		parts := make([]string, len(array))
		for i, element := range array {
			if !Nullish(element) {
				parts[i] = Template(element)
			}
		}
		return strings.Join(parts, ",")
	}
	return String(text)
}

// ResetTime is the reference's parseResetTime (shared.js:15-43): a falsy value
// is no reset; a number, or a string of digits, is Unix seconds below 1e12 and
// milliseconds otherwise; another string is a date. A value JavaScript cannot
// turn into a date is no reset, never a bogus time.
func ResetTime(raw json.RawMessage) *time.Time {
	return resetTime(raw, true)
}

// DateTime is the reference's Codex toIsoDate (codex.js:15-22): like ResetTime,
// except that a string is always read as a date, never as a timestamp.
func DateTime(raw json.RawMessage) *time.Time {
	return resetTime(raw, false)
}

func resetTime(raw json.RawMessage, digitStrings bool) *time.Time {
	if !Truthy(raw) {
		return nil
	}
	if value, ok := IsNumber(raw); ok {
		return unixStamp(value)
	}
	var text string
	if json.Unmarshal(bytes.TrimSpace(raw), &text) != nil {
		return nil
	}
	if digitStrings && isDigits(text) {
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil
		}
		return unixStamp(value)
	}
	return parseDate(text)
}

// jsMaxTime is the largest |time value| a JavaScript Date holds (ECMA-262 21.4.1.1).
const jsMaxTime = 8.64e15

func unixStamp(value float64) *time.Time {
	if value < 1e12 {
		value *= 1000
	}
	value = math.Trunc(value)
	if math.Abs(value) > jsMaxTime {
		return nil
	}
	stamp := time.UnixMilli(int64(value)).UTC()
	return &stamp
}

// parseDate accepts the date forms providers send: RFC 3339 with or without
// fractional seconds, a bare date (UTC midnight, as JavaScript reads it), the
// RFC 1123 HTTP-date forms and Tencent's "2006-01-02 15:04:05" (CodeBuddy
// billing). JavaScript reads that last form in the host's zone; it is read as
// UTC here, which is the reference's answer on a UTC host.
func parseDate(text string) *time.Time {
	text = strings.TrimSpace(text)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02", time.RFC1123, time.RFC1123Z, time.DateTime} {
		if parsed, err := time.Parse(layout, text); err == nil {
			stamp := parsed.UTC().Truncate(time.Millisecond)
			return &stamp
		}
	}
	return nil
}

func isDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
