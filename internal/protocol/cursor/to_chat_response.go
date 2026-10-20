package cursor

import (
	"context"
	"errors"
	"io"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// The pin's response leg cursor>openai-chat is the identity
// (translator/response/cursor-to-openai.js:13-28): the executor decodes
// Cursor's protobuf into Chat chunks and the leg returns them unchanged. These
// functions do the same for the Chat the transport emits, and add the checks
// the pin lacks: an error object is a failure, a stream that ends without a
// finish chunk is a failure, and a finish for tool calls with no call behind it
// is an error (SPEC 9.3). They make nothing up: in particular no usage. A
// usage the transport itself reports is passed through unchanged, and the
// transport must not estimate one. Cursor reports none, and the pin's estimate (executors/cursor.js:834,1087,1310) is
// deliberately not made, so an absent usage is "not reported", never zero
// (PARITY 6.3 V8, 6.5 V32).

// CursorResponseToChat is the buffered leg. The body is returned as received.
func CursorResponseToChat(body []byte) ([]byte, error) {
	response, err := concerns.WireObject(body)
	if err != nil {
		return nil, err
	}
	if failure := upstreamFailure(response); failure != nil {
		return nil, failure
	}
	choices := concerns.Items(response["choices"])
	if len(choices) == 0 {
		return nil, errors.New("upstream completion has no choices")
	}
	for _, raw := range choices {
		choice := concerns.Object(raw)
		message := concerns.Object(choice["message"])
		if message == nil {
			return nil, errors.New("upstream choice has no message")
		}
		if _, err := concerns.ResolveFinish(concerns.Text(choice["finish_reason"]), len(concerns.Items(message["tool_calls"])), false); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// CursorStreamToChat is the streamed leg.
func CursorStreamToChat(ctx context.Context, src io.Reader, dst io.Writer) error {
	writer := &concerns.SSEWriter{Ctx: ctx, Dst: dst}
	calls := 0
	finished := false
	err := concerns.ReadEvents(ctx, src, func(kind string, chunk map[string]any) (bool, error) {
		if kind == "[DONE]" {
			if !finished {
				return true, io.ErrUnexpectedEOF
			}
			return true, nil
		}
		if failure := upstreamFailure(chunk); failure != nil {
			return false, failure
		}
		for _, raw := range concerns.Items(chunk["choices"]) {
			choice := concerns.Object(raw)
			calls += len(concerns.Items(concerns.Object(choice["delta"])["tool_calls"]))
			if reason := concerns.Text(choice["finish_reason"]); reason != "" {
				if _, err := concerns.ResolveFinish(reason, calls, false); err != nil {
					return false, err
				}
				finished = true
			}
		}
		return false, writer.Event("", chunk)
	})
	if err != nil {
		_ = concerns.FailChatStream(ctx, writer, err)
		return err
	}
	return writer.Done()
}

// upstreamFailure reads the error the executor reports, a Chat error object with
// no choices (executors/cursor.js:809-820, 863-869, 1005-1012).
func upstreamFailure(chunk map[string]any) error {
	if concerns.Items(chunk["choices"]) != nil {
		return nil
	}
	switch value := chunk["error"].(type) {
	case map[string]any:
		return &concerns.StreamFailure{Value: value}
	case string:
		if value != "" {
			return &concerns.StreamFailure{Value: map[string]any{"type": "api_error", "message": value}}
		}
	}
	return nil
}
