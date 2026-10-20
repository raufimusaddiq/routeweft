package ingress

import (
	"context"
	"fmt"
	"io"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	"github.com/raufimusaddiq/routeweft/internal/protocol/hub"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
)

// CanTranslate declares the implemented protocol pairs, independent of providers.
func CanTranslate(source, target string) bool {
	return source != target && hub.CanTranslate(source, target)
}

func (h *Handler) translationAvailable(source, target string, compact bool) bool {
	if !CanTranslate(source, target) {
		return false
	}
	switch source {
	case openai.ChatProtocol:
		return h.opts.TranslateChat != nil
	case openai.ResponsesProtocol:
		if compact {
			return h.opts.TranslateResponsesCompact != nil
		}
		return h.opts.TranslateResponses != nil
	case anthropic.MessagesProtocol:
		return h.opts.TranslateMessages != nil
	}
	return false
}

// streamFunc is a streaming response converter.
type streamFunc = func(context.Context, io.Reader, io.Writer) error

// chainStreams feeds the first converter's output to the second. A hub
// conversion between two non-Chat formats is two legs, and each leg is a
// stream-to-stream function, so the pair runs through a pipe. The first leg's
// failure is returned even when the second leg finished cleanly on the failure
// terminal it was handed.
func chainStreams(first, second streamFunc) streamFunc {
	return func(ctx context.Context, src io.Reader, dst io.Writer) error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		reader, writer := io.Pipe()
		done := make(chan error, 1)
		go func() {
			err := first(ctx, src, writer)
			_ = writer.CloseWithError(err)
			done <- err
		}()
		err := second(ctx, reader, dst)
		_ = reader.CloseWithError(err)
		cancel()
		// The first leg may be parked reading the upstream body, which no context
		// check can interrupt; closing the source unblocks it.
		if closer, ok := src.(io.Closer); ok {
			_ = closer.Close()
		}
		if firstErr := <-done; err == nil {
			err = firstErr
		}
		return err
	}
}

func chainBodies(first, second func([]byte) ([]byte, error)) func([]byte) ([]byte, error) {
	return func(body []byte) ([]byte, error) {
		converted, err := first(body)
		if err != nil {
			return nil, err
		}
		return second(converted)
	}
}

// TranslateChat wires both halves of a Chat request served by a provider that
// speaks another wire.
func TranslateChat(request *openai.ChatRequest, target string) (ChatTranslation, error) {
	switch target {
	case openai.ResponsesProtocol:
		body, err := openai.ChatToResponses(request)
		return ChatTranslation{Endpoint: "responses", Body: body, Response: openai.ResponsesResponseToChat,
			Stream: func(ctx context.Context, src io.Reader, dst io.Writer) error {
				return openai.ResponsesStreamToChat(ctx, src, dst, request.Model)
			}}, err
	case anthropic.MessagesProtocol:
		parsed, err := concerns.WireObject(request.Raw)
		if err != nil {
			return ChatTranslation{}, err
		}
		body, names, err := anthropic.ChatRequestToMessages(parsed, request.Model, request.Stream, true, endsOnAssistant(parsed["messages"]))
		if err != nil {
			return ChatTranslation{}, err
		}
		return ChatTranslation{Endpoint: "messages", Body: body,
			Response: func(body []byte) ([]byte, error) { return anthropic.MessagesResponseToChat(body, names) },
			Stream: func(ctx context.Context, src io.Reader, dst io.Writer) error {
				return anthropic.MessagesStreamToChat(ctx, src, dst, names)
			}}, nil
	}
	return ChatTranslation{}, fmt.Errorf("no Chat translator for %q", target)
}

// TranslateResponses keeps custom-tool response metadata local to this request.
func TranslateResponses(request *openai.ResponsesRequest, target string) (ChatTranslation, error) {
	if request.Compact || (target != openai.ChatProtocol && target != anthropic.MessagesProtocol) {
		return ChatTranslation{}, fmt.Errorf("no Responses translator for %q", target)
	}
	body, err := openai.ResponsesToChat(request)
	if err != nil {
		return ChatTranslation{}, err
	}
	customTools := openai.CustomToolNames(request)
	toResponses := func(body []byte) ([]byte, error) { return openai.ChatResponseToResponses(body, customTools) }
	toResponsesStream := func(ctx context.Context, src io.Reader, dst io.Writer) error {
		return openai.ChatStreamToResponses(ctx, src, dst, request.Model, customTools)
	}
	if target == openai.ChatProtocol {
		return ChatTranslation{Endpoint: "chat/completions", Body: body, Response: toResponses, Stream: toResponsesStream}, nil
	}
	chat, err := concerns.WireObject(body)
	if err != nil {
		return ChatTranslation{}, err
	}
	var raw map[string]any
	if parsed, parseErr := concerns.WireObject(request.Raw); parseErr == nil {
		raw = parsed
	}
	messagesBody, names, err := anthropic.ChatRequestToMessages(chat, request.Model, request.Stream, false, endsOnAssistant(raw["input"]))
	if err != nil {
		return ChatTranslation{}, err
	}
	return ChatTranslation{Endpoint: "messages", Body: messagesBody,
		Response: chainBodies(func(body []byte) ([]byte, error) { return anthropic.MessagesResponseToChat(body, names) }, toResponses),
		Stream: chainStreams(func(ctx context.Context, src io.Reader, dst io.Writer) error {
			return anthropic.MessagesStreamToChat(ctx, src, dst, names)
		}, toResponsesStream)}, nil
}

// TranslateMessages wires both halves of a Messages request served by a
// provider that speaks Chat or Responses.
func TranslateMessages(request *anthropic.MessagesRequest, target string) (ChatTranslation, error) {
	if target != openai.ChatProtocol && target != openai.ResponsesProtocol {
		return ChatTranslation{}, fmt.Errorf("no Messages translator for %q", target)
	}
	body, err := anthropic.MessagesRequestToChat(request.Raw, request.Model, request.Stream)
	if err != nil {
		return ChatTranslation{}, err
	}
	if target == openai.ChatProtocol {
		return ChatTranslation{Endpoint: "chat/completions", Body: body,
			Response: anthropic.ChatResponseToMessages, Stream: anthropic.ChatStreamToMessages}, nil
	}
	chat, err := openai.ParseChatRequest(body)
	if err != nil {
		return ChatTranslation{}, err
	}
	responsesBody, err := openai.ChatToResponses(chat)
	if err != nil {
		return ChatTranslation{}, err
	}
	return ChatTranslation{Endpoint: "responses", Body: responsesBody,
		Response: chainBodies(openai.ResponsesResponseToChat, anthropic.ChatResponseToMessages),
		Stream: chainStreams(func(ctx context.Context, src io.Reader, dst io.Writer) error {
			return openai.ResponsesStreamToChat(ctx, src, dst, request.Model)
		}, anthropic.ChatStreamToMessages)}, nil
}

// endsOnAssistant is translator/index.js:60-66: whether the client's own
// conversation ended on an assistant turn, which is prefill rather than an
// emptied turn to restore.
func endsOnAssistant(value any) bool {
	list := concerns.Items(value)
	if len(list) == 0 {
		return false
	}
	return concerns.Text(concerns.Object(list[len(list)-1])["role"]) == "assistant"
}
