package ingress

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	geminiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	ollamaadapter "github.com/raufimusaddiq/routeweft/internal/protocol/ollama"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/transforms/promptcache"
)

func (h *Handler) handleOpenAI(w http.ResponseWriter, r *http.Request, source string, compact bool) {
	if !h.authorize(w, r) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.opts.MaxBodyBytes))
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			h.writeError(w, 413, "request_too_large", "request body exceeds configured limit")
		} else if r.Context().Err() == nil {
			h.writeError(w, 400, "request_read_failed", "request body could not be read")
		}
		return
	}
	var chat *openaiadapter.ChatRequest
	var responses *openaiadapter.ResponsesRequest
	var model string
	var stream bool
	if source == openaiadapter.ChatProtocol {
		chat, err = openaiadapter.ParseChatRequest(body)
		if err == nil {
			model, stream = chat.Model, chat.Stream
		}
	} else {
		responses, err = openaiadapter.ParseResponsesRequest(body, compact)
		if err == nil {
			model, stream = responses.Model, responses.Stream
		}
	}
	if err != nil {
		h.writeError(w, 400, "invalid_request_error", err.Error())
		return
	}
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, 503, "snapshot_unavailable", err.Error())
		return
	}
	started := time.Now()
	var translation ChatTranslation
	response, provider, err := h.dispatchAttempts(r, snapshot, model, source, compact, func(provider routing.ProviderRef) (ChatTranslation, error) {
		translation = ChatTranslation{}
		plan, err := routing.BuildPlan(routing.PlanOptions{SourceProtocol: source, Provider: provider})
		if err != nil {
			return ChatTranslation{}, err
		}
		if !plan.NativePath() {
			var translated ChatTranslation
			var err error
			upstreamModel := provider.UpstreamModel
			if upstreamModel == "" {
				upstreamModel = model
			}
			if chat != nil && h.opts.TranslateChat != nil {
				request := *chat
				request.Model = upstreamModel
				request.Scope = translationScope(r, provider)
				translated, err = h.opts.TranslateChat(&request, plan.TargetProtocol)
			} else if responses != nil && compact && h.opts.TranslateResponsesCompact != nil {
				request := *responses
				request.Model = upstreamModel
				request.Scope = translationScope(r, provider)
				translated, err = h.opts.TranslateResponsesCompact(&request, plan.TargetProtocol)
			} else if responses != nil && !compact && h.opts.TranslateResponses != nil {
				request := *responses
				request.Model = upstreamModel
				request.Scope = translationScope(r, provider)
				translated, err = h.opts.TranslateResponses(&request, plan.TargetProtocol)
			} else {
				return ChatTranslation{}, fmt.Errorf("no translator registered for target protocol %q", plan.TargetProtocol)
			}
			if plan.TargetProtocol == openaiadapter.ChatProtocol || plan.TargetProtocol == openaiadapter.ResponsesProtocol || plan.TargetProtocol == ollamaadapter.Protocol {
				if translated.Headers == nil {
					translated.Headers = http.Header{}
				}
				for name, values := range openAIProviderHeaders(provider) {
					translated.Headers[name] = values
				}
			} else if plan.TargetProtocol == geminiadapter.Protocol {
				// Gemini authenticates with its own header, never a bearer token.
				if translated.Headers == nil {
					translated.Headers = http.Header{}
				}
				for name, values := range geminiHeaders(provider) {
					translated.Headers[name] = values
				}
			} else if plan.TargetProtocol == anthropicadapter.MessagesProtocol {
				if translated.Headers == nil {
					translated.Headers = http.Header{}
				}
				if err == nil {
					// A provider may rewrite the converted body (the Claude OAuth
					// identity: the legs add no system prompt of their own, PARITY V14).
					translated.Body, err = wire.PrepareMessages(provider.ProviderID, translated.Body)
				}
				for name, values := range anthropicHeaders(r, provider) {
					translated.Headers[name] = values
				}
			}
			if provider.Endpoint != "" {
				translated.Endpoint = provider.Endpoint
			}
			translation = translated
			return translated, err
		}
		endpoint := "responses"
		if chat != nil {
			endpoint = "chat/completions"
		}
		if provider.Endpoint != "" {
			endpoint = provider.Endpoint
		}
		if compact {
			endpoint += "/compact"
		}
		var outbound []byte
		if chat != nil {
			outbound, err = chat.MarshalBody(provider.UpstreamModel)
		} else {
			outbound, err = responses.MarshalBody(provider.UpstreamModel)
		}
		return ChatTranslation{Endpoint: endpoint, Body: outbound, Headers: openAIProviderHeaders(provider)}, err
	})
	if err != nil {
		h.dispatchError(w, r, err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 && translation.Response != nil {
		h.writeOpenAITranslation(w, r, response, provider, translation, stream, started)
		return
	}
	if !stream && response.StatusCode >= 200 && response.StatusCode < 300 && wire.UnwrapResponse(provider.ProviderID) != nil {
		h.writeUnwrapped(w, r, response, provider, started)
		return
	}
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		io.Copy(w, response.Body)
		h.emitOutcome(r, provider, response.StatusCode, stream, promptcache.Usage{}, started)
		return
	}
	h.relayNative(w, r, response, provider, source, stream, started)
}

// writeUnwrapped relays a non-stream response with the provider's gateway
// envelope removed (Cline's {"success":true,"data":{...}}), so the client gets a
// bare OpenAI body and usage is read from the inner body (the reference unwraps
// before any consumer reads choices or usage). Streams never reach here.
func (h *Handler) writeUnwrapped(w http.ResponseWriter, r *http.Request, response *http.Response, provider routing.ProviderRef, started time.Time) {
	body, err := io.ReadAll(io.LimitReader(response.Body, openaiadapter.MaxTranslatedResponseBytes+1))
	if r.Context().Err() != nil {
		return
	}
	if err != nil || len(body) > openaiadapter.MaxTranslatedResponseBytes {
		h.writeError(w, 502, "upstream_response_invalid", "upstream response could not be read")
		return
	}
	body = wire.UnwrapResponse(provider.ProviderID)(body)
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(body)
	usage, _ := shared.ParseUsage(provider.Protocol, body)
	h.emitOutcome(r, provider, response.StatusCode, false, usage, started)
}

// emitTranslated records a translated request. Counts the upstream never
// reported are not recorded as zero: usage_observed is false, as for a native
// stream (SPEC 21.1; PARITY V8). That covers a Chat provider that omits its usage
// chunk, a Gemini stream framed as a JSON array (which the scanner cannot read,
// 6.6 G11) and a stream that broke.
func (h *Handler) emitTranslated(r *http.Request, provider routing.ProviderRef, status int, stream bool, usage promptcache.Usage, observed bool, started time.Time) {
	if r != nil && r.Context().Err() != nil {
		return
	}
	h.publishOutcome(r, provider, status, stream, usage, started, "", !observed)
}

func (h *Handler) writeOpenAITranslation(w http.ResponseWriter, r *http.Request, response *http.Response, provider routing.ProviderRef, translation ChatTranslation, stream bool, started time.Time) {
	if stream {
		if translation.Stream == nil {
			h.writeError(w, 502, "upstream_response_invalid", "no stream translator is configured")
			return
		}
		copyResponseHeaders(w.Header(), response.Header)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(response.StatusCode)
		scanner := &usageScanner{family: provider.Protocol}
		// The body stays closable so a chained conversion can stop a read that no
		// context check could interrupt.
		source := struct {
			io.Reader
			io.Closer
		}{io.TeeReader(response.Body, scanner), response.Body}
		err := translation.Stream(r.Context(), source, w)
		usage, _ := scanner.usage()
		status := response.StatusCode
		observed := err == nil && scanner.reportsUsage(r.Context().Err() == nil)
		if !observed {
			usage = promptcache.Usage{}
		}
		if err != nil {
			status = http.StatusBadGateway
			if r.Context().Err() == nil {
				h.recordClassification(provider.ConnectionID, classifyResponse(status, nil), time.Now())
			}
		}
		h.emitTranslated(r, provider, status, true, usage, observed, started)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, openaiadapter.MaxTranslatedResponseBytes+1))
	if r.Context().Err() != nil {
		return
	}
	if err != nil || len(body) > openaiadapter.MaxTranslatedResponseBytes {
		h.writeError(w, 502, "upstream_response_invalid", "upstream response could not be translated")
		return
	}
	if unwrap := wire.UnwrapResponse(provider.ProviderID); unwrap != nil {
		body = unwrap(body)
	}
	translated, err := translation.Response(body)
	if err != nil {
		h.writeError(w, 502, "upstream_response_invalid", "upstream response could not be translated")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	copyResponseHeaders(w.Header(), response.Header)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(translated)
	usage, observed := shared.ParseUsage(provider.Protocol, body)
	h.emitTranslated(r, provider, response.StatusCode, false, usage, observed, started)
}
