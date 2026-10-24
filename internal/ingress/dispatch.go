package ingress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/transforms/promptcache"
)

// Bound all wire attempts, including one same-account retry for 408/409.
const maxAccountAttempts = 10

// retryDelayCap bounds how long one request waits in-process before its
// same-account retry, regardless of a huge upstream Retry-After. The full
// value still drives the RuntimeState cooldown; the client is not parked.
const retryDelayCap = 5 * time.Second

// statusClientClosedRequest is the accounting status for a request the client
// abandoned before any upstream status existed (nginx's non-standard 499). It is
// recorded, never written: the client is already gone.
const statusClientClosedRequest = 499

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

// foldForcedStream turns the SSE a stream-only gateway answered into one JSON
// body for a client that did not ask to stream, so every consumer past
// dispatchAttempts (native relay, usage capture, response translation) sees an
// ordinary non-stream response. A response that is not an event stream (the
// gateway answered JSON, or the client is streaming) is left alone. The fold
// fails rather than drop a field that could carry text (FoldChatStream); a
// Responses endpoint folds to its terminal response (FoldResponsesStream).
func foldForcedStream(ctx context.Context, response *http.Response, provider routing.ProviderRef, stream bool) error {
	if stream || !wire.ForcesStream(provider) {
		return nil
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(response.Header.Get("Content-Type"), ";", 2)[0]))
	if mediaType != "text/event-stream" {
		return nil
	}
	var folded openaiadapter.Folded
	var err error
	switch provider.Protocol {
	case openaiadapter.ResponsesProtocol:
		folded, err = openaiadapter.FoldResponsesStream(response.Body)
	case anthropicadapter.MessagesProtocol:
		folded.Body, err = anthropicadapter.FoldMessagesStream(ctx, response.Body)
	default:
		folded, err = openaiadapter.FoldChatStream(response.Body, provider.UpstreamModel, time.Now)
	}
	if err != nil {
		var inband *openaiadapter.StreamError
		var messagesInband *anthropicadapter.StreamError
		switch {
		case errors.As(err, &inband):
			return &dispatchError{http.StatusBadGateway, "upstream_stream_error", inband.Message()}
		case errors.As(err, &messagesInband):
			return &dispatchError{http.StatusBadGateway, "upstream_stream_error", messagesInband.Message()}
		}
		return &dispatchError{http.StatusBadGateway, "upstream_response_invalid", "upstream stream could not be returned as one response: " + err.Error()}
	}
	if len(folded.Ignored) > 0 {
		slog.Debug("forced-stream fold ignored transport metadata", "provider", provider.ProviderID, "fields", folded.Ignored)
	}
	response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(folded.Body))
	response.Header = response.Header.Clone()
	response.Header.Set("Content-Type", "application/json")
	response.Header.Del("Content-Length")
	response.ContentLength = int64(len(folded.Body))
	return nil
}

// Dispatch completes retries before exposing upstream headers. Once a stream
// starts it is never replayed, including after a partial response or disconnect.
func (h *Handler) dispatchAttempts(r *http.Request, snapshot *runtime.RuntimeSnapshot, model, source string, compact, stream bool, prepare func(routing.ProviderRef) (ChatTranslation, error)) (response *http.Response, chosen routing.ProviderRef, resultErr error) {
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
	// What to tell the client when no candidate answers. An attempt that reached
	// an upstream and failed is the real cause and outranks a candidate that
	// never got that far (credentials, a request its wire cannot express); among
	// the failed attempts the latest one stands.
	var attemptErr, preAttemptErr, translationErr error
	var attemptResponse *http.Response
	var attemptProvider routing.ProviderRef
	defer func() {
		if attemptResponse != nil {
			attemptResponse.Body.Close()
		}
	}()
	failedAttempt := func(err error, response *http.Response, provider routing.ProviderRef) {
		if attemptResponse != nil {
			attemptResponse.Body.Close()
		}
		attemptErr, attemptResponse, attemptProvider = err, response, provider
	}
	used := 0
	// The abandoned record's duration starts where every other emitter's does:
	// the request's start, not after candidate resolution and translation.
	requestStart := time.Now()
	if holder := attributionFrom(r); holder != nil && !holder.begun.IsZero() {
		requestStart = holder.begun
	}
	// A client that leaves after an upstream request was issued still cost the
	// upstream work, so the request is accounted once as client_abandoned with
	// unobserved usage (the response never arrived) rather than vanishing. The
	// relay paths do the same for responses that did arrive (native_usage.go).
	defer func() {
		if response == nil && resultErr != nil && used > 0 && r.Context().Err() != nil {
			// Candidate exhaustion returns an empty ref; attribute the record to
			// the last attempted provider instead of an empty provider/model.
			if chosen.ProviderID == "" && chosen.ConnectionID == "" {
				chosen = attemptProvider
			}
			h.publishOutcomeFlagged(r, chosen, statusClientClosedRequest, stream, promptcache.Usage{}, requestStart, TerminationClientAbandoned, true, true)
		}
	}()
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
				preAttemptErr = &dispatchError{503, "credentials_unavailable", "provider credentials are unavailable"}
				if errors.Is(err, credentials.ErrKeyRequired) {
					return nil, provider, preAttemptErr
				}
				continue
			}
			provider.APIToken = token
		}
		// A provider that signs every request with its credential must never be
		// sent unsigned: an empty HMAC key would produce a signature the gateway
		// rejects with an error that names nothing useful (wire.Hook
		// RequireCredential). This is a configuration problem with this account,
		// so the next candidate is tried and, if none serves, the client sees a
		// credential error.
		if wire.RequiresCredential(provider.ProviderID) && strings.TrimSpace(provider.APIToken) == "" {
			h.recordClassification(provider.ConnectionID, routing.Classification{Outcome: routing.OutcomeAuthRefreshRequired}, time.Now())
			preAttemptErr = &dispatchError{503, "credentials_unavailable", "provider credentials are unavailable"}
			continue
		}
		// A model that was escalated before goes straight to the wire that served
		// it (github.js:137-140). That is the one attempt: a refusal there is the
		// answer, as in the pin, so the replay below stays off.
		escalated := false
		if h.opts.State != nil {
			if protocol, endpoint := wire.ReplayWire(provider); protocol != "" && h.opts.State.PreferredWire(provider.ProviderID, provider.UpstreamModel) == protocol {
				provider.Protocol, provider.Endpoint = protocol, endpoint
				escalated = true
			}
		}
		outbound, err := prepare(provider)
		if err != nil {
			// The request cannot be expressed for this candidate's wire, which says
			// nothing about the account's health: try the next candidate rather
			// than let candidate order decide whether the request succeeds.
			if translationErr == nil {
				translationErr = &dispatchError{502, "request_translation_failed", err.Error()}
			}
			continue
		}
		// A refused wire is replayed on the other one at most once per candidate
		// (wire.Hook.Escalate); the replay's own answer, whatever it is, stands.
		for retry, endpointAttempt := 0, 0; retry < 2 && used < maxAccountAttempts; {
			if err := r.Context().Err(); err != nil {
				return nil, provider, err
			}
			target, err := wire.ResolveTarget(provider, outbound.Endpoint, endpointAttempt)
			var request *http.Request
			if err == nil {
				request, err = h.newUpstreamRequest(r, snapshot, provider, outbound.Endpoint, outbound.Body, outbound.Headers, target)
			}
			if err != nil {
				if errors.Is(err, wire.ErrKiroEndpointsExhausted) {
					if attemptResponse == nil {
						failedAttempt(&dispatchError{502, "kiro_endpoints_exhausted", err.Error()}, nil, provider)
					}
					break
				}
				var credentialErr *wire.CredentialError
				if errors.As(err, &credentialErr) {
					h.recordClassification(provider.ConnectionID, routing.Classification{Outcome: routing.OutcomeAuthRefreshRequired}, time.Now())
					preAttemptErr = &dispatchError{503, "credentials_unavailable", credentialErr.Error()}
					break
				}
				var refused *wire.TargetError
				if errors.As(err, &refused) {
					// The provider's hook refused this route (a missing setting, a model
					// served on another wire): the next candidate may still serve.
					if translationErr == nil {
						translationErr = &dispatchError{502, "request_translation_failed", refused.Error()}
					}
					break
				}
				return nil, provider, &dispatchError{502, "upstream_configuration_error", "provider URL or endpoint is not allowed"}
			}
			used++
			response, err := wire.Send(provider, h.clientFor(provider), request)
			if err != nil {
				if r.Context().Err() != nil {
					return nil, provider, r.Context().Err()
				}
				h.recordClassification(provider.ConnectionID, routing.Classification{Outcome: routing.OutcomeFallbackAccount}, time.Now())
				failedAttempt(err, nil, provider)
				break
			}
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				if err := foldForcedStream(r.Context(), response, provider, stream); err != nil {
					response.Body.Close()
					if r.Context().Err() != nil {
						return nil, provider, r.Context().Err()
					}
					// A 2xx that cannot be folded is this account's failure, not the
					// request's: cool it and let the next candidate serve.
					h.recordClassification(provider.ConnectionID, routing.Classification{Outcome: routing.OutcomeFallbackAccount}, time.Now())
					failedAttempt(err, nil, provider)
					break
				}
				return response, provider, nil
			}
			if provider.ProviderID == "kiro" && (response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 404) {
				endpointAttempt++
				if endpointAttempt < target.EndpointCount && used < maxAccountAttempts {
					response.Body.Close()
					// A host's auth rejection does not prove the credential is invalid on another surface.
					continue
				}
				if endpointAttempt == target.EndpointCount && !authFailureSeen && (response.StatusCode == 401 || response.StatusCode == 403) {
					authFailureSeen = true
					h.recordClassification(provider.ConnectionID, routing.Classification{Outcome: routing.OutcomeAuthRefreshRequired}, time.Now())
				}
				kept := *response
				kept.Header = response.Header.Clone()
				failedAttempt(nil, &kept, provider)
				break
			}
			prefix, readErr := io.ReadAll(io.LimitReader(response.Body, errorBodyPeek))
			if r.Context().Err() != nil {
				response.Body.Close()
				return nil, provider, r.Context().Err()
			}
			if !escalated && readErr == nil && used < maxAccountAttempts {
				if protocol, endpoint := wire.Escalation(provider, response.StatusCode, prefix); protocol != "" {
					next := provider
					next.Protocol, next.Endpoint = protocol, endpoint
					// Remembered before the replay is tried, as the pin does
					// (github.js:159): a replay that fails still counts.
					if h.opts.State != nil {
						h.opts.State.PreferWire(provider.ProviderID, provider.UpstreamModel, protocol)
					}
					if replay, err := prepare(next); err == nil {
						slog.Warn("provider refused the wire; replaying once on another", "provider", provider.ProviderID, "model", provider.UpstreamModel, "from", provider.Protocol, "to", protocol)
						response.Body.Close()
						escalated, provider, outbound = true, next, replay
						continue
					}
					// prepare owns the caller's translation state; put the refused
					// wire's back so the answer surfaced below is read on its own wire.
					_, _ = prepare(provider)
				}
			}
			classification := classifyResponse(response.StatusCode, response.Header)
			info := shared.ParseError(provider.Protocol, response.StatusCode, prefix)
			if mapped := routing.ClassifyError(info.Kind.String(), response.StatusCode); mapped.Outcome != "" {
				classification.Outcome = mapped.Outcome
			}
			now := time.Now()
			classification = providerClassification(provider, response.StatusCode, prefix, classification, now)
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
				retry++
				continue
			}
			if classification.AllowsFallback() && readErr == nil && index+1 < len(candidates) && used < maxAccountAttempts {
				// Keep the unread tail too, in case later candidates cannot serve.
				kept := *response
				kept.Header = response.Header.Clone()
				kept.Body = struct {
					io.Reader
					io.Closer
				}{io.MultiReader(bytes.NewReader(prefix), response.Body), response.Body}
				failedAttempt(nil, &kept, provider)
				break
			}
			response.Body = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(bytes.NewReader(prefix), response.Body), response.Body}
			return response, provider, nil
		}
	}
	switch {
	case attemptResponse != nil:
		response := attemptResponse
		attemptResponse = nil
		return response, attemptProvider, nil
	case attemptErr != nil:
		return nil, routing.ProviderRef{}, attemptErr
	case translationErr != nil:
		return nil, routing.ProviderRef{}, translationErr
	case preAttemptErr != nil:
		return nil, routing.ProviderRef{}, preAttemptErr
	}
	return nil, routing.ProviderRef{}, &dispatchError{503, "no_eligible_accounts", routing.ErrNoEligibleAccounts.Error()}
}
