package ingress

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
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
	response, _, err := h.dispatchAttempts(r, snapshot, model, source, func(provider routing.ProviderRef) (ChatTranslation, error) {
		plan, err := routing.BuildPlan(routing.PlanOptions{SourceProtocol: source, Provider: provider})
		if err != nil {
			return ChatTranslation{}, err
		}
		if !plan.NativePath() {
			if chat != nil && h.opts.TranslateChat != nil {
				return h.opts.TranslateChat(chat, plan.TargetProtocol)
			}
			if responses != nil && compact && h.opts.TranslateResponsesCompact != nil {
				return h.opts.TranslateResponsesCompact(responses, plan.TargetProtocol)
			}
			if responses != nil && !compact && h.opts.TranslateResponses != nil {
				return h.opts.TranslateResponses(responses, plan.TargetProtocol)
			}
			return ChatTranslation{}, fmt.Errorf("no translator registered for target protocol %q", plan.TargetProtocol)
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
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		io.Copy(w, response.Body)
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
