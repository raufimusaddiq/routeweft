package concerns

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxTranslatedResponseBytes bounds buffered output and the total translated
// stream. Native streaming remains byte-preserving and does not use this limit.
// shortcut: 32 MiB translated output; revisit with measured long-output workloads.
const MaxTranslatedResponseBytes = 32 << 20

// ReadEvents handles SSE framing, not transport chunks. Only differing protocols
// use it; it neither buffers the complete upstream body nor starts a goroutine.
func ReadEvents(ctx context.Context, src io.Reader, consume func(string, map[string]any) (bool, error)) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var data strings.Builder
	var event string
	total := 0
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		advance, token, err := bufio.ScanLines(data, atEOF)
		total += advance
		if total > MaxTranslatedResponseBytes {
			return 0, nil, errors.New("translated upstream response exceeds limit")
		}
		return advance, token, err
	})
	emit := func() (bool, error) {
		if data.Len() == 0 {
			return false, nil
		}
		payload := strings.TrimSuffix(data.String(), "\n")
		data.Reset()
		if payload == "[DONE]" {
			return consume("[DONE]", nil)
		}
		value, err := WireObject([]byte(payload))
		if err != nil {
			return false, errors.New("invalid upstream stream event")
		}
		kind := Text(value["type"])
		if kind == "" {
			kind = event
		}
		event = ""
		return consume(kind, value)
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Text()
		if data.Len()+len(line) > 4<<20 {
			return errors.New("translated upstream response exceeds limit")
		}
		if line == "" {
			stop, err := emit()
			event = ""
			if err != nil || stop {
				return err
			}
		} else if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			data.WriteByte('\n')
		} else if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	stop, err := emit()
	if err != nil || stop {
		return err
	}
	return io.ErrUnexpectedEOF
}

// SSEWriter writes server-sent events to a client and flushes each one. A write
// after the request context is done fails, so a cancelled client gets no
// synthetic terminal.
type SSEWriter struct {
	Ctx context.Context
	Dst io.Writer
}

func (w *SSEWriter) flush() {
	if flusher, ok := w.Dst.(interface{ Flush() }); ok {
		flusher.Flush()
	}
}

// Event writes `event: name` (omitted when empty) and a JSON data line.
func (w *SSEWriter) Event(name string, value any) error {
	if err := w.Ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	prefix := ""
	if name != "" {
		prefix = "event: " + name + "\n"
	}
	if _, err := fmt.Fprintf(w.Dst, "%sdata: %s\n\n", prefix, payload); err != nil {
		return err
	}
	w.flush()
	return nil
}

// Done writes the Chat Completions terminal marker.
func (w *SSEWriter) Done() error {
	if err := w.Ctx.Err(); err != nil {
		return err
	}
	_, err := io.WriteString(w.Dst, "data: [DONE]\n\n")
	w.flush()
	return err
}
