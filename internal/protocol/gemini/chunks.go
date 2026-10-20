package gemini

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// readChunks reads a streamGenerateContent body as the objects it carries. With
// ?alt=sse (executors/vertex.js:102, "required for proper SSE streaming") that is
// SSE, one object per data line. Without it Gemini sends one JSON array that
// grows as the model speaks. A provider route cannot put a query on a translated
// endpoint (ingress newUpstreamRequest), and the URL hook that will is #115
// PR-C's, so both framings are read; consume sees each object as it arrives
// either way.
//
// A nil return means the body ended cleanly, which for SSE is a plain EOF (no
// [DONE] follows). Whether the response was COMPLETE is the caller's to decide
// from the finishReason it saw, so a body cut short is never mistaken for one
// that finished.
func readChunks(ctx context.Context, src io.Reader, consume func(map[string]any) error) error {
	reader := bufio.NewReaderSize(src, 32<<10)
	for {
		first, err := reader.Peek(1)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		if first[0] == ' ' || first[0] == '\n' || first[0] == '\r' || first[0] == '\t' {
			_, _ = reader.Discard(1)
			continue
		}
		if first[0] == '[' {
			return readArray(ctx, reader, consume)
		}
		break
	}
	err := concerns.ReadEvents(ctx, reader, func(_ string, value map[string]any) (bool, error) {
		if value == nil {
			return false, nil
		}
		return false, consume(value)
	})
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return nil
	}
	return err
}

func readArray(ctx context.Context, reader io.Reader, consume func(map[string]any) error) error {
	limited := &io.LimitedReader{R: reader, N: concerns.MaxTranslatedResponseBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.UseNumber()
	if _, err := decoder.Token(); err != nil {
		return err
	}
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var value map[string]any
		if err := decoder.Decode(&value); err != nil {
			if limited.N <= 0 {
				return errors.New("translated upstream response exceeds limit")
			}
			return err
		}
		if value == nil {
			return errors.New("invalid upstream stream event")
		}
		if err := consume(value); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}
