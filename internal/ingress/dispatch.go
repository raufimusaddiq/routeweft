package ingress

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// Match the pinned v1 default cap; no failed account is repeated in this chain.
const maxAccountAttempts = 10

type dispatchError struct {
	status        int
	code, message string
}

func (e *dispatchError) Error() string { return e.message }

func (h *Handler) dispatchError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	var failure *dispatchError
	if errors.As(err, &failure) {
		h.writeError(w, failure.status, failure.code, failure.message)
		return
	}
	h.writeError(w, http.StatusBadGateway, "upstream_request_failed", "upstream request failed")
}

// Dispatch completes retries before exposing upstream headers. Once a stream
// starts it is never replayed, including after a partial response or disconnect.
func (h *Handler) dispatchAttempts(r *http.Request, snapshot *runtime.RuntimeSnapshot, model, source string, prepare func(routing.ProviderRef) (ChatTranslation, error)) (*http.Response, routing.ProviderRef, error) {
	var candidates []routing.ProviderRef
	if h.opts.Candidates != nil && h.opts.AccountProvider != nil {
		providerID := modelProviderID(model)
		if resolved, ok := snapshot.ServingModel(model); ok {
			providerID = resolved.ProviderID
		}
		accounts, known := h.PlanProviderAttempts(snapshot, providerID, model, time.Now())
		if !known {
			return nil, routing.ProviderRef{}, &dispatchError{404, "model_not_found", fmt.Sprintf("model %q is not configured for inference", model)}
		}
		for _, account := range accounts {
			if provider, ok := h.opts.AccountProvider(snapshot, model, account.ID, source); ok {
				candidates = append(candidates, provider)
			}
			if len(candidates) == maxAccountAttempts {
				break
			}
		}
	} else if h.opts.ProviderResolver != nil {
		if provider, ok := h.opts.ProviderResolver(snapshot, model); ok {
			candidates = append(candidates, provider)
		} else {
			return nil, routing.ProviderRef{}, &dispatchError{404, "model_not_found", fmt.Sprintf("model %q is not configured for inference", model)}
		}
	} else {
		return nil, routing.ProviderRef{}, &dispatchError{503, "no_provider", "no provider is configured"}
	}
	if len(candidates) == 0 {
		return nil, routing.ProviderRef{}, &dispatchError{503, "no_eligible_accounts", routing.ErrNoEligibleAccounts.Error()}
	}
	var lastErr error
	for index, provider := range candidates {
		if err := r.Context().Err(); err != nil {
			return nil, provider, err
		}
		if h.opts.CredentialFor != nil {
			token, err := h.opts.CredentialFor(r.Context(), provider.ConnectionID)
			if err != nil {
				lastErr = err
				continue
			}
			provider.APIToken = token
		}
		outbound, err := prepare(provider)
		if err != nil {
			return nil, provider, &dispatchError{502, "request_translation_failed", err.Error()}
		}
		request, err := h.newUpstreamRequest(r, provider, outbound.Endpoint, outbound.Body, outbound.Headers)
		if err != nil {
			return nil, provider, &dispatchError{502, "upstream_configuration_error", "provider URL or endpoint is not allowed"}
		}
		response, err := h.clientFor(provider).Do(request)
		if err != nil {
			if r.Context().Err() != nil {
				return nil, provider, r.Context().Err()
			}
			h.recordClassification(provider.ConnectionID, routing.Classification{Outcome: routing.OutcomeFallbackAccount}, time.Now())
			lastErr = err
			continue
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, provider, nil
		}
		prefix, readErr := io.ReadAll(io.LimitReader(response.Body, errorBodyPeek))
		classification := classifyResponse(response.StatusCode, response.Header)
		info := shared.ParseError(provider.Protocol, response.StatusCode, prefix)
		if mapped := routing.ClassifyError(info.Kind.String(), response.StatusCode); mapped.Outcome != "" {
			classification.Outcome = mapped.Outcome
		}
		h.recordClassification(provider.ConnectionID, classification, time.Now())
		fallback := classification.AllowsFallback() || classification.Outcome == routing.OutcomeQuotaLock || classification.Outcome == routing.OutcomeAuthRefreshRequired || classification.Outcome == routing.OutcomeRetrySameAccount
		if fallback && readErr == nil && index+1 < len(candidates) {
			response.Body.Close()
			continue
		}
		response.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(prefix), response.Body), response.Body}
		return response, provider, nil
	}
	return nil, routing.ProviderRef{}, lastErr
}
