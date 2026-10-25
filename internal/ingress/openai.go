package ingress

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	geminiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
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
	r = withClientBody(r, body)
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
	response, provider, err := h.dispatchAttempts(r, snapshot, model, source, compact, stream, func(provider routing.ProviderRef) (ChatTranslation, error) {
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
			if wire.HasCodec(plan.TargetProtocol) {
				translated, err = h.codecTranslation(provider, chat, responses, nil, compact, upstreamModel)
			} else if chat != nil && h.opts.TranslateChat != nil {
				request := *chat
				request.Model = upstreamModel
				request.Scope = translationScope(r, provider)
				request.Project = provider.Connection.Settings.ProjectID
				translated, err = h.opts.TranslateChat(&request, plan.TargetProtocol)
			} else if responses != nil && compact && h.opts.TranslateResponsesCompact != nil {
				request := *responses
				request.Model = upstreamModel
				request.Scope = translationScope(r, provider)
				request.Project = provider.Connection.Settings.ProjectID
				translated, err = h.opts.TranslateResponsesCompact(&request, plan.TargetProtocol)
			} else if responses != nil && !compact && h.opts.TranslateResponses != nil {
				request := *responses
				request.Model = upstreamModel
				request.Scope = translationScope(r, provider)
				request.Project = provider.Connection.Settings.ProjectID
				translated, err = h.opts.TranslateResponses(&request, plan.TargetProtocol)
			} else {
				return ChatTranslation{}, fmt.Errorf("no translator registered for target protocol %q", plan.TargetProtocol)
			}
			if takesProviderHeaders(plan.TargetProtocol) {
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
	if responses != nil && translation.Response == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		// A provider whose Responses endpoint takes function tools only answers a
		// converted custom tool with a function_call; the client declared a custom
		// tool and gets a custom_tool_call back.
		if names := wire.ResponsesCustomToolNames(provider.ProviderID, responses.Raw); len(names) > 0 {
			if responseIsSSE(response, stream) {
				response.Body = wire.ResponsesStream(names, response.Body)
			} else {
				h.writeRewrittenResponses(w, r, response, provider, names, started)
				return
			}
		}
	}
	if !stream && response.StatusCode >= 200 && response.StatusCode < 300 && wire.UnwrapResponse(provider.ProviderID) != nil {
		h.writeUnwrapped(w, r, response, provider, started)
		return
	}
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		io.Copy(w, response.Body)
		h.emitOutcome(r, provider, response.StatusCode, stream, promptcache.Usage{}, true, started)
		return
	}
	h.relayNative(w, r, response, provider, source, stream, started)
}

// writeRewrittenResponses relays a non-stream Responses body with the function_call
// items of converted custom tools turned back into custom_tool_call items. A call
// whose arguments cannot be turned into freeform input is refused with 502 and the
// code wire.ErrCustomToolArguments, never relayed as a wrong custom call.
func (h *Handler) writeRewrittenResponses(w http.ResponseWriter, r *http.Request, response *http.Response, provider routing.ProviderRef, names map[string]bool, started time.Time) {
	body, err := io.ReadAll(io.LimitReader(response.Body, openaiadapter.MaxTranslatedResponseBytes+1))
	if r.Context().Err() != nil {
		h.abandon(r, provider, response.StatusCode, false, promptcache.Usage{}, started, true)
		return
	}
	if err != nil || len(body) > openaiadapter.MaxTranslatedResponseBytes {
		// Billed upstream, unreadable body: record the 502 with unobserved usage.
		h.writeError(w, 502, "upstream_response_invalid", "upstream response could not be read")
		h.emitTranslated(r, provider, 502, false, promptcache.Usage{}, false, started)
		return
	}
	rewritten, err := wire.RewriteResponsesBody(names, body)
	if err != nil {
		// The upstream answered 2xx and was billed for this body; the 502 is
		// Routeweft's refusal, not an upstream error, so it is recorded with the
		// usage the upstream reported to keep spend reconcilable.
		h.writeError(w, 502, wire.ErrCustomToolArguments.Error(), err.Error())
		usage, observed := shared.ParseUsage(provider.Protocol, body)
		h.emitTranslated(r, provider, 502, false, usage, observed, started)
		return
	}
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(rewritten)
	usage, observed := shared.ParseUsage(provider.Protocol, body)
	h.emitTranslated(r, provider, response.StatusCode, false, usage, observed, started)
}

// writeUnwrapped relays a non-stream response with the provider's gateway
// envelope removed (Cline's {"success":true,"data":{...}}), so the client gets a
// bare OpenAI body and usage is read from the inner body (the reference unwraps
// before any consumer reads choices or usage). Streams never reach here.
func (h *Handler) writeUnwrapped(w http.ResponseWriter, r *http.Request, response *http.Response, provider routing.ProviderRef, started time.Time) {
	body, err := io.ReadAll(io.LimitReader(response.Body, openaiadapter.MaxTranslatedResponseBytes+1))
	if r.Context().Err() != nil {
		h.abandon(r, provider, response.StatusCode, false, promptcache.Usage{}, started, true)
		return
	}
	if err != nil || len(body) > openaiadapter.MaxTranslatedResponseBytes {
		// Billed upstream, unreadable body: record the 502 with unobserved usage.
		h.writeError(w, 502, "upstream_response_invalid", "upstream response could not be read")
		h.emitTranslated(r, provider, 502, false, promptcache.Usage{}, false, started)
		return
	}
	body = wire.UnwrapResponse(provider.ProviderID)(body)
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(body)
	usage, observed := shared.ParseUsage(provider.Protocol, body)
	h.emitOutcome(r, provider, response.StatusCode, false, usage, observed, started)
}

// emitTranslated records a translated request. Counts the upstream never
// reported are not recorded as zero: usage_observed is false, as for a native
// stream (SPEC 21.1; PARITY V8). That covers a Chat provider that omits its usage
// chunk, a Gemini stream framed as a JSON array (which the scanner cannot read,
// 6.7 G11) and a stream that broke.
func (h *Handler) emitTranslated(r *http.Request, provider routing.ProviderRef, status int, stream bool, usage promptcache.Usage, observed bool, started time.Time) {
	if r != nil && r.Context().Err() != nil {
		h.abandon(r, provider, status, stream, usage, started, !observed)
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
		var answer io.Reader = response.Body
		if translation.Integrity != nil {
			// Held until it is known to be whole; the client is told it is waiting.
			gated, err := h.gateAnswer(r.Context(), response, provider, translation.Integrity, kiroHeartbeat(w))
			if err != nil {
				h.abandon(r, provider, response.StatusCode, true, promptcache.Usage{}, started, true)
				return
			}
			answer = gated
		}
		// The body stays closable so a chained conversion can stop a read that no
		// context check could interrupt.
		source := struct {
			io.Reader
			io.Closer
		}{io.TeeReader(answer, scanner), response.Body}
		err := translation.Stream(r.Context(), source, w)
		usage, _ := scanner.usage()
		status := response.StatusCode
		observed := err == nil && scanner.reportsUsage(r.Context().Err() == nil)
		if !observed {
			usage = promptcache.Usage{}
		}
		// A translator error after the client left is the client's cancellation,
		// not a gateway failure: keep the upstream status so abandons do not count
		// as upstream 5xx in the operator's error rate.
		if err != nil && r.Context().Err() == nil {
			status = http.StatusBadGateway
			h.recordClassification(provider.ConnectionID, classifyResponse(status, nil), time.Now())
		}
		h.emitTranslated(r, provider, status, true, usage, observed, started)
		return
	}
	var answer io.Reader = response.Body
	if translation.Integrity != nil {
		gated, err := h.gateAnswer(r.Context(), response, provider, translation.Integrity, nil)
		if err != nil {
			h.abandon(r, provider, response.StatusCode, false, promptcache.Usage{}, started, true)
			return
		}
		answer = gated
	}
	body, err := io.ReadAll(io.LimitReader(answer, openaiadapter.MaxTranslatedResponseBytes+1))
	if r.Context().Err() != nil {
		h.abandon(r, provider, response.StatusCode, false, promptcache.Usage{}, started, true)
		return
	}
	var gateFailure *concerns.StreamFailure
	if errors.As(err, &gateFailure) {
		// The gate's verdict, in the code the pin gives it (executors/kiro.js:493-504).
		h.writeError(w, 502, concerns.Text(gateFailure.Value["code"]), concerns.Text(gateFailure.Value["message"]))
		// The account is penalized as it is when the same failure ends a stream (above); a
		// client that left was handled before this point.
		h.recordClassification(provider.ConnectionID, classifyResponse(http.StatusBadGateway, nil), time.Now())
		h.emitTranslated(r, provider, 502, false, promptcache.Usage{}, false, started)
		return
	}
	if err != nil || len(body) > openaiadapter.MaxTranslatedResponseBytes {
		// Billed upstream, unreadable body: record the 502 with unobserved usage.
		h.writeError(w, 502, "upstream_response_invalid", "upstream response could not be translated")
		h.emitTranslated(r, provider, 502, false, promptcache.Usage{}, false, started)
		return
	}
	if unwrap := wire.UnwrapResponse(provider.ProviderID); unwrap != nil {
		body = unwrap(body)
	}
	translated, err := translation.Response(body)
	if err != nil {
		var failure *wire.WebAnswerError
		var kiroFailure *concerns.StreamFailure
		if translation.Integrity != nil && errors.As(err, &kiroFailure) {
			// A Kiro leg's failure keeps the pin's code (executors/kiro.js:690-700).
			h.writeError(w, 502, concerns.Text(kiroFailure.Value["code"]), concerns.Text(kiroFailure.Value["message"]))
			h.recordClassification(provider.ConnectionID, classifyResponse(http.StatusBadGateway, nil), time.Now())
		} else if errors.As(err, &failure) {
			// The site reported a failure inside its answer: the client gets the
			// pin's 502 document, and the attempt is classified as failed.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write(failure.Body())
			h.recordClassification(provider.ConnectionID, classifyResponse(http.StatusBadGateway, nil), time.Now())
		} else {
			h.writeError(w, 502, "upstream_response_invalid", "upstream response could not be translated")
		}
		usage, observed := shared.ParseUsage(provider.Protocol, body)
		h.emitTranslated(r, provider, 502, false, usage, observed, started)
		return
	}
	if r.Context().Err() != nil {
		usage, observed := shared.ParseUsage(provider.Protocol, body)
		h.abandon(r, provider, response.StatusCode, false, usage, started, !observed)
		return
	}
	copyResponseHeaders(w.Header(), response.Header)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(translated)
	usage, observed := shared.ParseUsage(provider.Protocol, body)
	h.emitTranslated(r, provider, response.StatusCode, false, usage, observed, started)
}
