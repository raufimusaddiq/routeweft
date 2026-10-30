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

	controlevents "github.com/raufimusaddiq/routeweft/internal/control/events"
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
	feed     *consoleFeed
}

// Console records reach the dashboard on the shared event channel (SPEC 22.1, #277), in the
// oracle's shape: the buffered lines are read once (GET /admin/v1/logs), then new lines and the
// clear are pushed (src/app/api/console-logs/stream/route.js:28-65). A burst is coalesced into
// one "console.log" event per consoleBatchDelay, as the live view coalesces "usage.pending", so
// per-request log volume cannot flood the bounded subscriber queues that carry configuration
// invalidations. A burst larger than consoleBatchMax sends the newest consoleBatchMax lines and
// says so (truncated), and the page re-reads the buffer.
const (
	consoleBatchDelay = 150 * time.Millisecond
	consoleBatchMax   = 100
)

type consoleFeed struct {
	bus   *controlevents.Bus
	delay time.Duration

	mu        sync.Mutex
	scheduled bool
	published uint64 // the id of the newest record already announced
}

// PublishTo announces new records and clears on bus. A nil bus stops announcing.
func (b *LogBuffer) PublishTo(bus *controlevents.Bus) { b.publishTo(bus, consoleBatchDelay) }

func (b *LogBuffer) publishTo(bus *controlevents.Bus, delay time.Duration) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if bus == nil {
		b.feed = nil
		return
	}
	b.feed = &consoleFeed{bus: bus, delay: delay, published: b.nextID}
}

func (f *consoleFeed) changed(b *LogBuffer) {
	f.mu.Lock()
	if f.scheduled {
		f.mu.Unlock()
		return
	}
	f.scheduled = true
	f.mu.Unlock()
	time.AfterFunc(f.delay, func() { f.flush(b) })
}

func (f *consoleFeed) flush(b *LogBuffer) {
	f.mu.Lock()
	f.scheduled = false
	after := f.published
	f.mu.Unlock()
	lines, newest, truncated := b.since(after, consoleBatchMax)
	if len(lines) == 0 {
		return
	}
	f.mu.Lock()
	if newest > f.published {
		f.published = newest
	}
	f.mu.Unlock()
	f.bus.Publish("console.log", map[string]any{"lines": lines, "truncated": truncated})
}

// since returns the records after id in id order, at most max of them (the newest ones when more
// arrived), the newest id seen, and whether older new records were left out.
func (b *LogBuffer) since(id uint64, max int) ([]any, uint64, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	start := len(b.entries)
	for start > 0 && b.entries[start-1].ID > id {
		start--
	}
	fresh := b.entries[start:]
	if len(fresh) == 0 {
		return nil, id, false
	}
	truncated := false
	if len(fresh) > max {
		fresh, truncated = fresh[len(fresh)-max:], true
	}
	lines := make([]any, 0, len(fresh))
	for _, entry := range fresh {
		line := map[string]any{"id": entry.ID, "time": entry.Time.Format(time.RFC3339Nano), "level": entry.Level, "message": entry.Message}
		if len(entry.Attributes) > 0 {
			line["attributes"] = entry.Attributes
		}
		lines = append(lines, line)
	}
	return lines, fresh[len(fresh)-1].ID, truncated
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
	b.nextID++
	entry := ConsoleLog{ID: b.nextID, Time: record.Time.UTC(), Level: record.Level.String(), Message: message, Attributes: attrs}
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	if len(b.entries) == b.capacity {
		copy(b.entries, b.entries[1:])
		b.entries[len(b.entries)-1] = entry
	} else {
		b.entries = append(b.entries, entry)
	}
	feed := b.feed
	b.mu.Unlock()
	if feed != nil {
		feed.changed(b)
	}
}

// clear drops every buffered record and reports how many it removed. Record ids
// keep increasing so a client polling by id never sees an id reused.
func (b *LogBuffer) clear() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	removed := len(b.entries)
	b.entries = b.entries[:0]
	feed, newest := b.feed, b.nextID
	b.mu.Unlock()
	if feed != nil {
		// Nothing before the clear is announced afterwards, and every page is told at once
		// (the oracle's "clear" message, console-logs/stream/route.js:53-61).
		feed.mu.Lock()
		feed.published = newest
		feed.mu.Unlock()
		feed.bus.Publish("console.cleared", map[string]any{"cleared": removed})
	}
	return removed
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
	return logControlReplacer.Replace(message)
}

// The replacers are built once: every request now writes a log line, and a
// Replacer builds its tables on first use.
var (
	logControlReplacer = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")
	logKeyReplacer     = strings.NewReplacer("_", "", "-", "", ".", "")
)

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
	normalized := strings.ToLower(logKeyReplacer.Replace(key))
	for _, marker := range []string{"authorization", "apikey", "token", "secret", "password", "cookie", "credential", "privatekey"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
