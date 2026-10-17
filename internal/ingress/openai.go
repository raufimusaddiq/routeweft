package ingress

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
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
				translated, err = h.opts.TranslateChat(&request, plan.TargetProtocol)
			} else if responses != nil && compact && h.opts.TranslateResponsesCompact != nil {
				request := *responses
				request.Model = upstreamModel
				translated, err = h.opts.TranslateResponsesCompact(&request, plan.TargetProtocol)
			} else if responses != nil && !compact && h.opts.TranslateResponses != nil {
				request := *responses
				request.Model = upstreamModel
				translated, err = h.opts.TranslateResponses(&request, plan.TargetProtocol)
			} else {
				return ChatTranslation{}, fmt.Errorf("no translator registered for target protocol %q", plan.TargetProtocol)
			}
			if plan.TargetProtocol == openaiadapter.ChatProtocol || plan.TargetProtocol == openaiadapter.ResponsesProtocol {
				if translated.Headers == nil {
					translated.Headers = http.Header{}
				}
				for name, values := range openAIProviderHeaders(provider) {
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
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		io.Copy(w, response.Body)
		h.emitOutcome(r, provider, response.StatusCode, stream, promptcache.Usage{}, started)
		return
	}
	if stream {
		if chat != nil {
			h.copyStream(w, r, response)
		} else {
			h.copyResponsesStream(w, r, response)
		}
		return
	}
	io.Copy(w, response.Body)
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
		err := translation.Stream(r.Context(), io.TeeReader(response.Body, scanner), w)
		usage, _ := scanner.usage()
		status := response.StatusCode
		if err != nil || !scanner.reportsUsage(r.Context().Err() == nil) {
			usage = promptcache.Usage{}
		}
		if err != nil {
			status = http.StatusBadGateway
			if r.Context().Err() == nil {
				h.recordClassification(provider.ConnectionID, classifyResponse(status, nil), time.Now())
			}
		}
		h.emitOutcome(r, provider, status, true, usage, started)
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
	usage, _ := shared.ParseUsage(provider.Protocol, body)
	h.emitOutcome(r, provider, response.StatusCode, false, usage, started)
}
