package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultLogCapacity = 1000
	maxLogMessageRunes = 2048
	maxLogAttrsSize    = 4096
)

var (
	logBearer       = regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`)
	logSecretMarker = regexp.MustCompile(`(?i)\b(authorization|api[_-]?key|access[_-]?token|refresh[_-]?token|secret|password|cookie|credential)\b`)
	logURL          = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s]+`)
)

// ConsoleLog is one bounded operator log record. Attributes are redacted and
// size-bounded; a secret-shaped key or value never reaches the API.
type ConsoleLog struct {
	ID         uint64         `json:"id"`
	Time       time.Time      `json:"time"`
	Level      string         `json:"level"`
	Message    string         `json:"message"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// LogBuffer keeps only the newest bounded operator log records.
type LogBuffer struct {
	mu       sync.RWMutex
	entries  []ConsoleLog
	capacity int
	nextID   uint64
}

func NewLogBuffer(capacity int) *LogBuffer {
	if capacity <= 0 {
		capacity = defaultLogCapacity
	}
	return &LogBuffer{entries: make([]ConsoleLog, 0, capacity), capacity: capacity}
}

func (b *LogBuffer) append(record slog.Record, attrs map[string]any) {
	if b == nil {
		return
	}
	message := safeLogText(record.Message)
	if runes := []rune(message); len(runes) > maxLogMessageRunes {
		message = string(runes[:maxLogMessageRunes]) + "…"
	}
	if encoded, err := json.Marshal(attrs); err != nil || len(encoded) > maxLogAttrsSize {
		attrs = map[string]any{"omitted": "log attributes exceeded size limit"}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	entry := ConsoleLog{ID: b.nextID, Time: record.Time.UTC(), Level: record.Level.String(), Message: message, Attributes: attrs}
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	if len(b.entries) == b.capacity {
		copy(b.entries, b.entries[1:])
		b.entries[len(b.entries)-1] = entry
		return
	}
	b.entries = append(b.entries, entry)
}

func (b *LogBuffer) list(p page, level, query string) ([]ConsoleLog, int64) {
	if b == nil {
		return []ConsoleLog{}, 0
	}
	level, query = strings.ToLower(strings.TrimSpace(level)), strings.ToLower(strings.TrimSpace(query))
	b.mu.RLock()
	defer b.mu.RUnlock()
	filtered := make([]ConsoleLog, 0, len(b.entries))
	for i := len(b.entries) - 1; i >= 0; i-- {
		entry := b.entries[i]
		if level != "" && !strings.EqualFold(entry.Level, level) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(entry.Message), query) {
			attrs, _ := json.Marshal(entry.Attributes)
			if !strings.Contains(strings.ToLower(string(attrs)), query) {
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	return slice(filtered, p), int64(len(filtered))
}

// escapeLogControl strips CR/LF so a log message cannot break the JSON/SSE
// framing it is embedded in.
func escapeLogControl(message string) string {
	return strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(message)
}

func safeLogText(value string) string {
	if logSecretMarker.MatchString(value) {
		return "[redacted]"
	}
	value = escapeLogControl(logBearer.ReplaceAllString(value, "Bearer [redacted]"))
	value = logURL.ReplaceAllStringFunc(value, safeURL)
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return safeURL(value)
	}
	return value
}

// LogHandler mirrors records to the next handler and retains a bounded,
// redacted view for authenticated operator queries.
func LogHandler(next slog.Handler, buffer *LogBuffer) slog.Handler {
	return &captureHandler{next: next, buffer: buffer}
}

type captureHandler struct {
	next   slog.Handler
	buffer *LogBuffer
	groups []string
	attrs  map[string]any
}

func (h *captureHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next == nil || h.next.Enabled(ctx, level)
}

func (h *captureHandler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, safeLogText(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		clean.AddAttrs(sanitizeLogAttr(attr))
		return true
	})
	if h.next != nil {
		if err := h.next.Handle(ctx, clean); err != nil {
			return err
		}
	}
	attrs := cloneLogAttrs(h.attrs)
	clean.Attrs(func(attr slog.Attr) bool {
		addLogAttr(attrs, attr, h.groups)
		return true
	})
	h.buffer.append(clean, attrs)
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	bound := cloneLogAttrs(h.attrs)
	cleanAttrs := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		attr = sanitizeLogAttr(attr)
		cleanAttrs[i] = attr
		addLogAttr(bound, attr, h.groups)
	}
	var next slog.Handler
	if h.next != nil {
		next = h.next.WithAttrs(cleanAttrs)
	}
	return &captureHandler{next: next, buffer: h.buffer, groups: append([]string(nil), h.groups...), attrs: bound}
}

func sanitizeLogAttr(attr slog.Attr) slog.Attr {
	value := attr.Value.Resolve()
	if logKeySensitive(attr.Key) || strings.EqualFold(attr.Key, "error") || (value.Kind() == slog.KindAny && isErrorValue(value)) {
		return slog.String(attr.Key, "[redacted]")
	}
	switch value.Kind() {
	case slog.KindString:
		return slog.String(attr.Key, safeLogText(value.String()))
	case slog.KindGroup:
		children := value.Group()
		clean := make([]slog.Attr, 0, len(children))
		for _, child := range children {
			clean = append(clean, sanitizeLogAttr(child))
		}
		return slog.Attr{Key: attr.Key, Value: slog.GroupValue(clean...)}
	case slog.KindAny:
		switch item := value.Any().(type) {
		case string:
			return slog.String(attr.Key, safeLogText(item))
		case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, time.Time, time.Duration:
			return slog.Attr{Key: attr.Key, Value: value}
		default:
			return slog.String(attr.Key, "[omitted]")
		}
	}
	return slog.Attr{Key: attr.Key, Value: value}
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	var next slog.Handler
	if h.next != nil {
		next = h.next.WithGroup(name)
	}
	groups := append([]string(nil), h.groups...)
	if name != "" {
		groups = append(groups, name)
	}
	return &captureHandler{next: next, buffer: h.buffer, groups: groups, attrs: cloneLogAttrs(h.attrs)}
}

func cloneLogAttrs(attrs map[string]any) map[string]any {
	cloned := make(map[string]any, len(attrs))
	for key, value := range attrs {
		cloned[key] = value
	}
	return cloned
}

func addLogAttr(dst map[string]any, attr slog.Attr, groups []string) {
	value := attr.Value.Resolve()
	if value.Kind() == slog.KindGroup {
		group := append(append([]string(nil), groups...), attr.Key)
		for _, child := range value.Group() {
			addLogAttr(dst, child, group)
		}
		return
	}
	key := strings.Join(append(append([]string(nil), groups...), attr.Key), ".")
	if key == "" {
		return
	}
	if logKeySensitive(key) || strings.EqualFold(key, "error") || (value.Kind() == slog.KindAny && isErrorValue(value)) {
		dst[key] = "[redacted]"
		return
	}
	switch value.Kind() {
	case slog.KindString:
		dst[key] = safeLogText(value.String())
	case slog.KindInt64:
		dst[key] = value.Int64()
	case slog.KindUint64:
		dst[key] = value.Uint64()
	case slog.KindFloat64:
		dst[key] = value.Float64()
	case slog.KindBool:
		dst[key] = value.Bool()
	case slog.KindDuration:
		dst[key] = value.Duration().String()
	case slog.KindTime:
		dst[key] = value.Time().UTC()
	case slog.KindAny:
		switch item := value.Any().(type) {
		case string:
			dst[key] = safeLogText(item)
		case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
			dst[key] = item
		case time.Time:
			dst[key] = item.UTC()
		case time.Duration:
			dst[key] = item.String()
		default:
			dst[key] = "[omitted]"
		}
	default:
		dst[key] = "[omitted]"
	}
}

func isErrorValue(value slog.Value) bool {
	_, ok := value.Any().(error)
	return ok
}

func logKeySensitive(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(key))
	for _, marker := range []string{"authorization", "apikey", "token", "secret", "password", "cookie", "credential", "privatekey"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
