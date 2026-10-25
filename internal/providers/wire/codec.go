package wire

import (
	"context"
	"io"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// A Codec is the whole conversion of one request for a provider whose pin converts
// in its executor, not in a translator leg (grok-web, perplexity-web; PARITY 6.19,
// #195). It is built per request from the Chat body, so it can carry per-request
// state (Perplexity's thread). internal/ingress turns it into a ChatTranslation;
// Responses and Messages clients pivot through Chat around it, as for CommandCode.
// Declaring a hub leg instead is refused by the oracle parity test.
type Codec struct {
	Endpoint string
	Body     []byte
	// Response reads the complete upstream answer as one Chat body. A failure the
	// site reported inside the answer is a *WebAnswerError.
	Response func([]byte) ([]byte, error)
	// Stream reads the upstream answer as a Chat stream. After a site failure it
	// has written the pin's error frame and returns a *WebAnswerError, so the
	// dispatcher records the attempt as failed while the client sees what the pin
	// shows.
	Stream func(context.Context, io.Reader, io.Writer) error
}

// CodecState is the mutable process-local state codecs keep between requests. It
// is owned by runtime.RuntimeState, beside PreferWire, never by the snapshot.
type CodecState struct{ Perplexity *PerplexitySessions }

// NewCodecState returns empty codec state.
func NewCodecState() *CodecState { return &CodecState{Perplexity: NewPerplexitySessions()} }

// HasCodec reports whether requests for the wire protocol are converted by a hook
// Codec. A codec provider's protocol is its id (TestCodecProvidersAreExactlyDeclared).
func HasCodec(protocol string) bool { return For(protocol).Codec != nil }

// CodecFor builds the codec for one request to provider. state may be nil (no
// thread is continued then). The model is the one the site is asked for.
func CodecFor(provider routing.ProviderRef, state *CodecState, chat map[string]any, model string) (Codec, bool, error) {
	build := For(provider.ProviderID).Codec
	if build == nil {
		return Codec{}, false, nil
	}
	codec, err := build(provider, state, chat, model)
	return codec, true, err
}

func grokCodec(_ routing.ProviderRef, _ *CodecState, chat map[string]any, model string) (Codec, error) {
	body, err := GrokWebRequest(chat, model)
	if err != nil {
		return Codec{}, err
	}
	return Codec{Endpoint: grokWebEndpoint, Body: body,
		Response: func(answer []byte) ([]byte, error) { return GrokWebResponseToChat(answer, model) },
		Stream: func(ctx context.Context, src io.Reader, dst io.Writer) error {
			return GrokWebStreamToChat(ctx, src, dst, model)
		}}, nil
}

func perplexityCodec(provider routing.ProviderRef, state *CodecState, chat map[string]any, model string) (Codec, error) {
	sessions := NewPerplexitySessions()
	if state != nil && state.Perplexity != nil {
		sessions = state.Perplexity
	}
	exchange, err := sessions.Begin(provider.ConnectionID, chat, model)
	if err != nil {
		return Codec{}, err
	}
	return Codec{Endpoint: perplexityWebEndpoint, Body: exchange.Body, Response: exchange.ResponseToChat, Stream: exchange.StreamToChat}, nil
}
