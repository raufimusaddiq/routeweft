package ingress

import (
	"context"
	"fmt"
	"io"

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
	}
	return false
}

// TranslateChat wires both halves of a Chat request served by Responses.
func TranslateChat(request *openai.ChatRequest, target string) (ChatTranslation, error) {
	if target != openai.ResponsesProtocol {
		return ChatTranslation{}, fmt.Errorf("no Chat translator for %q", target)
	}
	body, err := openai.ChatToResponses(request)
	return ChatTranslation{Endpoint: "responses", Body: body, Response: openai.ResponsesResponseToChat,
		Stream: func(ctx context.Context, src io.Reader, dst io.Writer) error {
			return openai.ResponsesStreamToChat(ctx, src, dst, request.Model)
		}}, err
}

// TranslateResponses keeps custom-tool response metadata local to this request.
func TranslateResponses(request *openai.ResponsesRequest, target string) (ChatTranslation, error) {
	if target != openai.ChatProtocol || request.Compact {
		return ChatTranslation{}, fmt.Errorf("no Responses translator for %q", target)
	}
	body, err := openai.ResponsesToChat(request)
	customTools := openai.CustomToolNames(request)
	return ChatTranslation{Endpoint: "chat/completions", Body: body,
		Response: func(body []byte) ([]byte, error) {
			return openai.ChatResponseToResponses(body, customTools)
		},
		Stream: func(ctx context.Context, src io.Reader, dst io.Writer) error {
			return openai.ChatStreamToResponses(ctx, src, dst, request.Model, customTools)
		}}, err
}
