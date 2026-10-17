package ingress

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// Bound all wire attempts, including one same-account retry for 408/409.
const maxAccountAttempts = 10

// retryDelayCap bounds how long one request waits in-process before its
// same-account retry, regardless of a huge upstream Retry-After. The full
// value still drives the RuntimeState cooldown; the client is not parked.
const retryDelayCap = 5 * time.Second

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
func (h *Handler) dispatchAttempts(r *http.Request, snapshot *runtime.RuntimeSnapshot, model, source string, compact bool, prepare func(routing.ProviderRef) (ChatTranslation, error)) (*http.Response, routing.ProviderRef, error) {
	// Key policy is checked before any resolution so a restricted key cannot
	// probe which models, aliases or Combos exist.
	if !requestPermitted(callerKey(r, snapshot), snapshot, model) {
		return nil, routing.ProviderRef{}, &dispatchError{403, "model_not_allowed", modelNotAllowed(model)}
	}
	var candidates []routing.ProviderRef
	if h.opts.Candidates != nil && h.opts.AccountProvider != nil {
		providerID := modelProviderID(model)
		if resolved, ok := snapshot.ServingModel(model); ok {
			providerID = resolved.ProviderID
		}
		var known bool
		candidates, known = h.planProviderAttempts(snapshot, providerID, model, source, compact, time.Now())
		if !known {
			return nil, routing.ProviderRef{}, &dispatchError{404, "model_not_found", fmt.Sprintf("model %q is not configured for inference", model)}
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
	used := 0
	authFailureSeen := false
	for index, provider := range candidates {
		if used == maxAccountAttempts {
			break
		}
		if err := r.Context().Err(); err != nil {
			return nil, provider, err
		}
		if h.opts.CredentialFor != nil {
			token, err := h.opts.CredentialFor(r.Context(), provider.ConnectionID)
			if err != nil {
				if r.Context().Err() != nil {
					return nil, provider, r.Context().Err()
				}
				h.recordClassification(provider.ConnectionID, routing.Classification{Outcome: routing.OutcomeAuthRefreshRequired}, time.Now())
				lastErr = &dispatchError{503, "credentials_unavailable", "provider credentials are unavailable"}
				if errors.Is(err, credentials.ErrKeyRequired) {
					return nil, provider, lastErr
				}
				continue
			}
			provider.APIToken = token
		}
		outbound, err := prepare(provider)
		if err != nil {
			return nil, provider, &dispatchError{502, "request_translation_failed", err.Error()}
		}
		for retry := 0; retry < 2 && used < maxAccountAttempts; retry++ {
			if err := r.Context().Err(); err != nil {
				return nil, provider, err
			}
			request, err := h.newUpstreamRequest(r, snapshot, provider, outbound.Endpoint, outbound.Body, outbound.Headers)
			if err != nil {
				return nil, provider, &dispatchError{502, "upstream_configuration_error", "provider URL or endpoint is not allowed"}
			}
			used++
			response, err := h.clientFor(provider).Do(request)
			if err != nil {
				if r.Context().Err() != nil {
					return nil, provider, r.Context().Err()
				}
				h.recordClassification(provider.ConnectionID, routing.Classification{Outcome: routing.OutcomeFallbackAccount}, time.Now())
				lastErr = err
				break
			}
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return response, provider, nil
			}
			prefix, readErr := io.ReadAll(io.LimitReader(response.Body, errorBodyPeek))
			if r.Context().Err() != nil {
				response.Body.Close()
				return nil, provider, r.Context().Err()
			}
			classification := classifyResponse(response.StatusCode, response.Header)
			info := shared.ParseError(provider.Protocol, response.StatusCode, prefix)
			if mapped := routing.ClassifyError(info.Kind.String(), response.StatusCode); mapped.Outcome != "" {
				classification.Outcome = mapped.Outcome
			}
			now := time.Now()
			// A request-scoped auth rejection must not cool the entire fallback chain.
			if classification.Outcome != routing.OutcomeAuthRefreshRequired || !authFailureSeen {
				h.recordClassification(provider.ConnectionID, classification, now)
			}
			if classification.Outcome == routing.OutcomeAuthRefreshRequired {
				authFailureSeen = true
			}
			if classification.Outcome == routing.OutcomeRetrySameAccount && retry == 0 && used < maxAccountAttempts && readErr == nil {
				response.Body.Close()
				delay := time.Until(routing.CooldownDeadline(classification, now, time.Second))
				if delay > retryDelayCap {
					delay = retryDelayCap
				}
				timer := time.NewTimer(delay)
				select {
				case <-r.Context().Done():
					timer.Stop()
					return nil, provider, r.Context().Err()
				case <-timer.C:
				}
				continue
			}
			if classification.AllowsFallback() && readErr == nil && index+1 < len(candidates) && used < maxAccountAttempts {
				response.Body.Close()
				break
			}
			response.Body = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(bytes.NewReader(prefix), response.Body), response.Body}
			return response, provider, nil
		}
	}
	if lastErr == nil {
		return nil, routing.ProviderRef{}, &dispatchError{503, "no_eligible_accounts", routing.ErrNoEligibleAccounts.Error()}
	}
	return nil, routing.ProviderRef{}, lastErr
}
