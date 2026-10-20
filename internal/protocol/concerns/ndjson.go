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
	return readLines(ctx, src, false, consume)
}

// ReadLinesLenient is ReadLines for an upstream that may wrap a line in SSE
// framing: a `data:` prefix is removed and a `[DONE]` line is skipped
// (response/commandcode-to-openai.js:63-67). A line that is not JSON after that
// is still an error, where the pin ignores it, because a garbled line may have
// held content.
func ReadLinesLenient(ctx context.Context, src io.Reader, consume func(map[string]any) (bool, error)) error {
	return readLines(ctx, src, true, consume)
}

func readLines(ctx context.Context, src io.Reader, lenient bool, consume func(map[string]any) (bool, error)) error {
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
		if lenient {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if line == "[DONE]" {
				continue
			}
		}
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
