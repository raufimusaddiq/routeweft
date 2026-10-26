package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// The Qoder body is hashed and signed byte for byte, and the pin builds it with
// JSON.parse and JSON.stringify, so the codec keeps the request as the JavaScript
// values the pin holds: nil, bool, json.Number (a parsed number), float64 (one the
// codec computed), string, []any and *jsObject, the ordered object that
// antigravity_retry.go already reads and writes. This file adds what the Qoder codec
// builds and converts with them.

func newJSObject() *jsObject { return &jsObject{values: map[string]any{}} }

// lookup is o[key] with whether the key is there. A missing key reads as undefined and
// a key holding null as null; `??` and Number() tell them apart.
func (o *jsObject) lookup(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.values[key]
	return v, ok
}

// set assigns a key; an existing key keeps its position.
func (o *jsObject) set(key string, value any) *jsObject {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
	return o
}

// remove is `delete o[key]`.
func (o *jsObject) remove(key string) {
	if _, ok := o.values[key]; !ok {
		return
	}
	delete(o.values, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			return
		}
	}
}

func (o *jsObject) clone() *jsObject {
	c := &jsObject{keys: append([]string(nil), o.keys...), values: make(map[string]any, len(o.values))}
	for k, v := range o.values {
		c.values[k] = v
	}
	return c
}

// spreadObject is {...value} for the values a message can be.
func spreadObject(value any) *jsObject {
	switch v := value.(type) {
	case *jsObject:
		return v.clone()
	case []any:
		o := newJSObject()
		for i, item := range v {
			o.set(strconv.Itoa(i), item)
		}
		return o
	}
	return newJSObject()
}

// parseJS is JSON.parse.
func parseJS(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := decodeJS(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the JSON value")
	}
	return value, nil
}

// stringifyJS is JSON.stringify of a value with no undefined in it.
func stringifyJS(value any) string {
	var b strings.Builder
	writeJS(&b, value)
	return b.String()
}

// jsNumberString is Number.prototype.toString (ECMA-262 Number::toString).
func jsNumberString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0"
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d[.ddd]e±XX
	mantissa, exponent, _ := strings.Cut(e, "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	x, _ := strconv.Atoi(exponent)
	k, n := len(digits), x+1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	exp := n - 1
	expSign := "+"
	if exp < 0 {
		expSign, exp = "-", -exp
	}
	if k == 1 {
		return sign + digits + "e" + expSign + strconv.Itoa(exp)
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + expSign + strconv.Itoa(exp)
}

// jsNumeric reads a value that is a JavaScript number: a parsed or a computed one.
func jsNumeric(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil && !math.IsInf(f, 0) {
			return math.NaN(), true
		}
		return f, true
	}
	return 0, false
}

// jsToString is String(value).
func jsToString(value any) string {
	if f, ok := jsNumeric(value); ok {
		return jsNumberString(f)
	}
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(v)
	case string:
		return v
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			if item != nil {
				parts[i] = jsToString(item)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

// jsIsTruthy is JavaScript truthiness of a parsed value.
func jsIsTruthy(value any) bool {
	if f, ok := jsNumeric(value); ok {
		return f != 0 && !math.IsNaN(f)
	}
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	}
	return true
}

var jsDecimal = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

// jsTrim is String.prototype.trim: WhiteSpace and LineTerminator.
func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// jsToNumber is Number(value); NaN is the answer for what is not a number.
func jsToNumber(value any) float64 {
	if f, ok := jsNumeric(value); ok {
		return f
	}
	switch v := value.(type) {
	case nil:
		return 0
	case bool:
		if v {
			return 1
		}
		return 0
	case string:
		return jsStringToNumber(v)
	case []any:
		return jsStringToNumber(jsToString(v))
	}
	return math.NaN()
}

func jsStringToNumber(s string) float64 {
	s = jsTrim(s)
	if s == "" {
		return 0
	}
	switch s {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(s) > 2 && s[0] == '0' {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[s[1]]
		if base != 0 {
			n, err := strconv.ParseUint(s[2:], base, 64)
			if err != nil {
				return math.NaN()
			}
			return float64(n)
		}
	}
	if !jsDecimal.MatchString(s) {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !math.IsInf(f, 0) {
		return math.NaN()
	}
	return f
}

// jsFinite is the pin's num(): Number(value) when finite.
func jsFinite(value any) (float64, bool) {
	f := jsToNumber(value)
	return f, !math.IsNaN(f) && !math.IsInf(f, 0)
}

// utf16Len is String.prototype.length.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}

// truncateUTF16 is `s.length > n ? s.slice(0, n) + "..." : s`.
func truncateUTF16(s string, n int) string {
	if prefix, cut := sliceUTF16(s, n); cut {
		return prefix + "..."
	}
	return s
}

// sliceUTF16 is s.slice(0, n), and whether it cut anything. A cut through a
// surrogate pair leaves the high surrogate alone, kept as its three WTF-8 bytes so
// that writeJSString can write it the way JSON.stringify does.
func sliceUTF16(s string, n int) (string, bool) {
	if utf16Len(s) <= n {
		return s, false
	}
	var b strings.Builder
	units := 0
	for _, r := range s {
		width := 1
		if r >= 0x10000 {
			width = 2
		}
		if units+width > n {
			if width == 2 && units+1 == n {
				high := 0xD800 + int((r-0x10000)>>10)
				b.WriteByte(0xED)
				b.WriteByte(byte(0x80 | high>>6&0x3F))
				b.WriteByte(byte(0x80 | high&0x3F))
			}
			break
		}
		b.WriteRune(r)
		units += width
	}
	return b.String(), true
}
