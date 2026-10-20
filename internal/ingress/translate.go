package ingress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/commandcode"
	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	"github.com/raufimusaddiq/routeweft/internal/protocol/cursor"
	geminiwire "github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	"github.com/raufimusaddiq/routeweft/internal/protocol/hub"
	kiroadapter "github.com/raufimusaddiq/routeweft/internal/protocol/kiro"
	"github.com/raufimusaddiq/routeweft/internal/protocol/ollama"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// unwired are provider wires whose conversion legs exist (internal/protocol/hub)
// but whose transport does not: nothing here can send to them yet, so a request
// must not be routed to one. Cursor needs a Connect-RPC protobuf transport over
// HTTP/2 (executors/cursor.js); CommandCode needs its headers, forced stream and
// pre-stream error handling in internal/providers/wire (executors/commandcode.js).
// Vertex needs the project-scoped URL and the service-account or key
// authentication that #115 PR-C's target hook supplies (executors/vertex.js:66-123);
// until it does, a request routed there would fail at the last hop. Kiro needs
// the account-aware body and headers (profile ARN, auth method, region) and the
// EventStream Accept header from internal/providers/wire (#135), and its Messages
// legs (PR 4c): until then a Messages client would pivot through Chat where the
// pin has a direct pair. PR-C removes
// the vertex entry together with TranslateChat's vertex arm, which is not built
// here because the endpoint it would name is that hook's to compute.
// Gemini CLI and Antigravity need the same hook: the Cloud Code URL and its
// ?alt=sse query, the OAuth bearer and client headers, and the project id the
// envelope names, which the legs refuse to invent (executors/gemini-cli.js,
// executors/antigravity.js).
// A provider leaves this list in the PR that binds its wire.
var unwired = map[string]bool{
	cursor.Protocol: true, commandcode.Protocol: true, geminiwire.VertexProtocol: true,
	kiroadapter.Protocol:           true,
	geminiwire.GeminiCLIProtocol:   true,
	geminiwire.AntigravityProtocol: true,
}

// geminiSignatures keeps the thought signatures Gemini attached to its function
// calls so the next request of the conversation can send them back. In memory
// only, bounded, and keyed by translationScope (geminiwire.Signatures).
var geminiSignatures = geminiwire.NewSignatures(geminiwire.DefaultSignatureEntries, geminiwire.DefaultSignatureTTL)

// translationScope names who an attempt is for: the authenticated client key and
// the provider connection. A deployment that requires no key has an empty key id
// and its scope is the connection alone; a request with neither has no scope, and
// a leg that would keep state for it keeps none.
func translationScope(r *http.Request, provider routing.ProviderRef) string {
	key := ""
	if holder := attributionFrom(r); holder != nil {
		if id := holder.keyID.Load(); id != nil {
			key = *id
		}
	}
	if provider.ConnectionID == "" && key == "" {
		return ""
	}
	// Length-prefixed, so no pair of ids can join to the scope of another pair.
	return fmt.Sprintf("%d:%s%s", len(key), key, provider.ConnectionID)
}

// geminiEndpoint is the generateContent path of a model under the provider's
// base URL (compat.go's native route builds the same one). The model is a path
// segment: one that could end it, or start a query, is not sent.
func geminiEndpoint(model string, stream bool) (string, error) {
	if model == "" || strings.ContainsAny(model, "/?#:\\ ") {
		return "", fmt.Errorf("model %q cannot be named in a Gemini path", model)
	}
	method := "generateContent"
	if stream {
		method = "streamGenerateContent"
	}
	return "v1beta/models/" + model + ":" + method, nil
}

// geminiOptions are the request-leg options of a translated Gemini attempt.
func geminiRequestOptions(model, scope string, repair bool) geminiwire.RequestOptions {
	return geminiwire.RequestOptions{Model: model, Repair: repair, Signatures: geminiSignatures, Scope: scope}
}

// geminiTranslation turns a Chat body into the whole Gemini attempt; wrap adapts
// the Chat response legs to the client's wire (nil for a Chat client).
func geminiTranslation(chat map[string]any, model, scope string, stream, repair bool,
	wrapBody func(func([]byte) ([]byte, error)) func([]byte) ([]byte, error),
	wrapStream func(streamFunc) streamFunc) (ChatTranslation, error) {
	endpoint, err := geminiEndpoint(model, stream)
	if err != nil {
		return ChatTranslation{}, err
	}
	body, names, err := geminiwire.ChatRequestToGemini(chat, geminiRequestOptions(model, scope, repair))
	if err != nil {
		return ChatTranslation{}, err
	}
	options := geminiwire.ResponseOptions{Model: model, Names: names, Signatures: geminiSignatures, Scope: scope}
	response := func(body []byte) ([]byte, error) { return geminiwire.GeminiResponseToChat(body, options) }
	streamer := func(ctx context.Context, src io.Reader, dst io.Writer) error {
		return geminiwire.GeminiStreamToChat(ctx, src, dst, options)
	}
	if wrapBody != nil {
		response = wrapBody(response)
	}
	if wrapStream != nil {
		streamer = wrapStream(streamer)
	}
	return ChatTranslation{Endpoint: endpoint, Body: body, Response: response, Stream: streamer}, nil
}

// CanTranslate declares the implemented protocol pairs, independent of providers.
func CanTranslate(source, target string) bool {
	return source != target && !unwired[target] && hub.CanTranslate(source, target)
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
		parent := ctx
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
		return chainError(err, <-done, parent)
	}
}

// chainError picks the error to report for two chained legs. The first leg's
// failure is the root cause (the upstream broke); the second leg's is usually
// its echo. What the first leg reports only because the second leg stopped, a
// closed pipe or the cancel chainStreams itself issued, is not a cause at all.
func chainError(second, first error, parent context.Context) error {
	ours := errors.Is(first, io.ErrClosedPipe) ||
		(errors.Is(first, context.Canceled) && (parent.Err() == nil || second != nil))
	if first != nil && !ours {
		return first
	}
	return second
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

// kiroEndpoint is the pin's transport path under each of its three hosts
// (providers/registry/kiro.js:19-23).
const kiroEndpoint = "generateAssistantResponse"

// ollamaEndpoint is Ollama's chat path under the provider's base URL.
const ollamaEndpoint = "api/chat"

func ollamaStream(model string) streamFunc {
	return func(ctx context.Context, src io.Reader, dst io.Writer) error {
		return ollama.OllamaStreamToChat(ctx, src, dst, model)
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
	case ollama.Protocol:
		parsed, err := concerns.WireObject(request.Raw)
		if err != nil {
			return ChatTranslation{}, err
		}
		body, err := ollama.ChatRequestToOllama(parsed, request.Model, request.Stream, true)
		if err != nil {
			return ChatTranslation{}, err
		}
		return ChatTranslation{Endpoint: ollamaEndpoint, Body: body, Response: ollama.OllamaResponseToChat, Stream: ollamaStream(request.Model)}, nil
	case geminiwire.Protocol:
		parsed, err := concerns.WireObject(request.Raw)
		if err != nil {
			return ChatTranslation{}, err
		}
		return geminiTranslation(parsed, request.Model, request.Scope, request.Stream, true, nil, nil)
	case kiroadapter.Protocol:
		parsed, err := concerns.WireObject(request.Raw)
		if err != nil {
			return ChatTranslation{}, err
		}
		body, names, info, err := kiroadapter.ChatRequestToKiro(parsed, kiroadapter.RequestOptions{Model: request.Model, Repair: true})
		if err != nil {
			return ChatTranslation{}, err
		}
		options := kiroadapter.ResponseOptions{Model: request.Model, Names: names, ThinkingTags: info.ThinkingTags}
		return ChatTranslation{Endpoint: kiroEndpoint, Body: body,
			Response: func(body []byte) ([]byte, error) { return kiroadapter.KiroResponseToChat(body, options) },
			Stream: func(ctx context.Context, src io.Reader, dst io.Writer) error {
				return kiroadapter.KiroStreamToChat(ctx, src, dst, options)
			}}, nil
	}
	return ChatTranslation{}, fmt.Errorf("no Chat translator for %q", target)
}

// TranslateResponses keeps custom-tool response metadata local to this request.
func TranslateResponses(request *openai.ResponsesRequest, target string) (ChatTranslation, error) {
	if request.Compact || (target != openai.ChatProtocol && target != anthropic.MessagesProtocol && target != ollama.Protocol && target != geminiwire.Protocol) {
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
	if target == geminiwire.Protocol {
		return geminiTranslation(chat, request.Model, request.Scope, request.Stream, false,
			func(first func([]byte) ([]byte, error)) func([]byte) ([]byte, error) {
				return chainBodies(first, toResponses)
			},
			func(first streamFunc) streamFunc { return chainStreams(first, toResponsesStream) })
	}
	if target == ollama.Protocol {
		ollamaBody, err := ollama.ChatRequestToOllama(chat, request.Model, request.Stream, false)
		if err != nil {
			return ChatTranslation{}, err
		}
		return ChatTranslation{Endpoint: ollamaEndpoint, Body: ollamaBody,
			Response: chainBodies(ollama.OllamaResponseToChat, toResponses),
			Stream:   chainStreams(ollamaStream(request.Model), toResponsesStream)}, nil
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
	if target != openai.ChatProtocol && target != openai.ResponsesProtocol && target != ollama.Protocol && target != geminiwire.Protocol {
		return ChatTranslation{}, fmt.Errorf("no Messages translator for %q", target)
	}
	if target == geminiwire.Protocol {
		// Messages>Chat>Gemini through the registry functions alone, as for
		// Ollama (translator/index.js:116-125).
		chat, err := anthropic.MessagesRequestToHubChat(request.Raw, request.Model, request.Stream)
		if err != nil {
			return ChatTranslation{}, err
		}
		return geminiTranslation(chat, request.Model, request.Scope, request.Stream, false,
			func(first func([]byte) ([]byte, error)) func([]byte) ([]byte, error) {
				return chainBodies(first, anthropic.ChatResponseToMessages)
			},
			func(first streamFunc) streamFunc { return chainStreams(first, anthropic.ChatStreamToMessages) })
	}
	if target == ollama.Protocol {
		// The pin pivots Messages>Chat>Ollama through the registry functions
		// alone (translator/index.js:116-125), so the Chat body is not filtered
		// for OpenAI-compatible upstreams.
		chat, err := anthropic.MessagesRequestToHubChat(request.Raw, request.Model, request.Stream)
		if err != nil {
			return ChatTranslation{}, err
		}
		body, err := ollama.ChatRequestToOllama(chat, request.Model, request.Stream, false)
		if err != nil {
			return ChatTranslation{}, err
		}
		return ChatTranslation{Endpoint: ollamaEndpoint, Body: body,
			Response: chainBodies(ollama.OllamaResponseToChat, anthropic.ChatResponseToMessages),
			Stream:   chainStreams(ollamaStream(request.Model), anthropic.ChatStreamToMessages)}, nil
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
