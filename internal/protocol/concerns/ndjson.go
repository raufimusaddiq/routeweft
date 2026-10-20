package concerns

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
)

// ReadLines is the NDJSON counterpart of ReadEvents: one JSON object per line,
// no event framing (Ollama's /api/chat stream; utils/stream.js:480-489 reads the
// unterminated tail line too). consume returns true to stop. A stream that ends
// without consume stopping it is io.ErrUnexpectedEOF, so a truncated upstream is
// never mistaken for a finished one. It neither buffers the whole body nor
// starts a goroutine, and it shares ReadEvents' size limits.
func ReadLines(ctx context.Context, src io.Reader, consume func(map[string]any) (bool, error)) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	total := 0
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		advance, token, err := bufio.ScanLines(data, atEOF)
		total += advance
		if total > MaxTranslatedResponseBytes {
			return 0, nil, errors.New("translated upstream response exceeds limit")
		}
		return advance, token, err
	})
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		value, err := WireObject([]byte(line))
		if err != nil {
			return errors.New("invalid upstream stream event")
		}
		if stop, err := consume(value); err != nil || stop {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}
