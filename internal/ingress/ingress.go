// Package ingress owns public inference HTTP endpoints.
package ingress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	geminiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	kiroadapter "github.com/raufimusaddiq/routeweft/internal/protocol/kiro"
	ollamaadapter "github.com/raufimusaddiq/routeweft/internal/protocol/ollama"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	systemoneadapter "github.com/raufimusaddiq/routeweft/internal/protocol/systemone"
	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/transforms/promptcache"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// Snapshot provides the immutable request-serving configuration.
type Snapshot interface {
	Load() (*runtime.RuntimeSnapshot, error)
}

// Options configure public ingress behavior such as limits and CORS policy.
type Options struct {
	MaxBodyBytes              int64
	CORSOrigins               []string
	ProviderResolver          ProviderResolver
	TranslateChat             ChatTranslator
	TranslateResponses        ResponsesTranslator
	TranslateResponsesCompact ResponsesCompactTranslator
	TranslateMessages         MessagesTranslator
	TranslateGemini           GeminiTranslator
	TranslateOllama           OllamaTranslator
	TranslateSystemOne        SystemOneTranslator
	Candidates                CandidateResolver
	AccountProvider           AccountProvider
	CredentialFor             func(context.Context, string) (string, error)
	State                     *runtime.RuntimeState
	Strategy                  routing.Strategy
	StickyLimit               uint64
	// OnRequestComplete receives one bounded accounting record per finished
	// upstream request: attribution, status, route timing and any upstream-reported
	// token usage (including cache counts). The telemetry PR wires it to the
	// bounded Usage queue; it is never called on the client-cancelled path.
	OnRequestComplete func(RequestOutcome)
	// TransformFinalBody uses the attempt's snapshot; failures keep the original body.
	TransformFinalBody func(context.Context, *runtime.RuntimeSnapshot, string, []byte) ([]byte, error)
	// AllowPrivateUpstreams is the explicit trusted-local operator policy. It is
	// off by default so operator-supplied provider URLs cannot reach loopback,
	// LAN, or metadata addresses.
	AllowPrivateUpstreams bool
	// ClientFor returns the pooled outbound client for one connection's compiled
	// proxy policy, so a per-connection proxy pool is honored on the request path
	// without building a transport per request (SPEC §20, PRD-ROUTE-005). It
	// returns nil to use the handler's default SSRF-protected client.
	ClientFor func(connectionID string) *http.Client
}

// RequestOutcome is one completed-request accounting record (SPEC §21 critical
// class). Token fields are zero when the upstream reported no usage.
type RequestOutcome struct {
	RequestID    string
	ProviderID   string
	ModelID      string
	ConnectionID string
	// APIKeyID is the opaque id of the authenticated client key, never the key
	// or its prefix. It is empty when the deployment does not require keys.
	APIKeyID     string
	Status       int
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CacheWrite   int64
	Duration     time.Duration
	Stream       bool
	// Termination is empty for a request that ran to its natural end. Otherwise
	// it says why not: TerminationClientAbandoned when the client went away
	// mid-flight (the upstream was still billed), TerminationUpstreamTruncated
	// when the upstream ended without its terminal event.
	Termination string
	// UsageUnobserved means the upstream never reported usage for this request
	// (an abandoned stream that ended before the usage chunk, or an upstream
	// that omits it). The token fields then hold no measurement: zero is not
	// "measured zero". It is false on every path that predates this field.
	UsageUnobserved bool
	// Unanswered is true when the client left after an upstream request was
	// issued but before the response was delivered, so no request completed:
	// the outcome is accounting only. The status is a synthetic 499 when no
	// upstream response existed, otherwise the upstream's own status. Consumers gate live "completed" signals on
	// this flag, never on the status number.
	Unanswered bool
}

const (
	TerminationClientAbandoned   = "client_abandoned"
	TerminationUpstreamTruncated = "upstream_truncated"
)

// attribution carries the authenticated key id from authorize to the completion
// record without re-running key lookup, so a key revoked mid-request is still
// attributed correctly.
// attribution is per-request state shared with the accounting emitters. begun
// is the request's start (set where the request enters the handler chain), so
// emitters that have no handler-local started use the same origin.
type attribution struct {
	keyID atomic.Pointer[string]
	begun time.Time
}

type attributionKey struct{}

func attributionFrom(r *http.Request) *attribution {
	if r == nil {
		return nil
	}
	holder, _ := r.Context().Value(attributionKey{}).(*attribution)
	return holder
}

// ProviderResolver is request-time code that uses only the already-loaded
// snapshot to resolve the requested model/provider.
type ProviderResolver func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool)

// ChatTranslation describes one translated request relative to the provider
// base URL and its success-response converters. Native paths leave converters
// nil; non-2xx responses always retain the upstream error body.
type ChatTranslation struct {
	Endpoint string
	Body     []byte
	Headers  http.Header
	Response func([]byte) ([]byte, error)
	Stream   func(context.Context, io.Reader, io.Writer) error
	// Integrity holds a Kiro answer until it is known to be whole and usable, with
	// one repair retry (kiroadapter.Gate; executors/kiro.js:370-627). Nil for every
	// other wire.
	Integrity *kiroadapter.ResponseOptions
	// WholeAnswer says the upstream answers a streaming client with one JSON
	// document, not a stream (an Antigravity image model, PARITY 6.26), so its
	// usage is read from that document rather than from SSE lines.
	WholeAnswer bool
}

// ChatTranslator selects the adapter for a different target protocol.
type ChatTranslator func(*openaiadapter.ChatRequest, string) (ChatTranslation, error)
type ResponsesTranslator func(*openaiadapter.ResponsesRequest, string) (ChatTranslation, error)
type ResponsesCompactTranslator func(*openaiadapter.ResponsesRequest, string) (ChatTranslation, error)
type MessagesTranslator func(*anthropicadapter.MessagesRequest, string) (ChatTranslation, error)
type GeminiTranslator func(*geminiadapter.Request, string) (ChatTranslation, error)
type OllamaTranslator func(*ollamaadapter.Request, string) (ChatTranslation, error)
type SystemOneTranslator func(*systemoneadapter.Request, string) (ChatTranslation, error)

// DefaultMaxBodyBytes matches the 128 MB compatibility target in PRD-API-005.
const DefaultMaxBodyBytes int64 = 128 << 20

// Handler serves the read-only public ingress contract delivered by this PR.
type Handler struct {
	snapshot Snapshot
	opts     Options
	client   *http.Client
}

// New builds the public ingress handler.
func New(snapshot Snapshot, opts Options) *Handler {
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	client := transport.NewSSRFProtectedClient()
	if opts.AllowPrivateUpstreams {
		client = transport.NewTrustedLocalSSRFProtectedClient()
	}
	return &Handler{snapshot: snapshot, opts: opts, client: client}
}

// clientFor resolves the outbound client for one provider connection, honoring a
// per-connection proxy pool when configured and falling back to the handler's
// default SSRF-protected client otherwise (SPEC §20).
func (h *Handler) clientFor(provider routing.ProviderRef) *http.Client {
	if h.opts.ClientFor != nil && provider.ConnectionID != "" {
		if client := h.opts.ClientFor(provider.ConnectionID); client != nil {
			return client
		}
	}
	return h.client
}

// emitOutcome publishes one accounting record when the telemetry hook is wired.
// A client that already left is recorded as client_abandoned rather than
// dropped. observed says whether the caller actually measured usage (a parsed
// body, a stream that reported it, or an error response with nothing to
// report); it decides usage_observed identically for completed and abandoned
// requests, exactly as emitTranslated does. requestID is the correlation id the
// app middleware assigned and echoed on the request.
func (h *Handler) emitOutcome(r *http.Request, provider routing.ProviderRef, status int, stream bool, usage promptcache.Usage, observed bool, started time.Time) {
	if r != nil && r.Context().Err() != nil {
		h.abandon(r, provider, status, stream, usage, started, !observed)
		return
	}
	h.publishOutcome(r, provider, status, stream, usage, started, "", !observed)
}

// publishOutcome builds and publishes one record without the cancellation
// guard, so a request the client abandoned can still be accounted for.
func (h *Handler) publishOutcome(r *http.Request, provider routing.ProviderRef, status int, stream bool, usage promptcache.Usage, started time.Time, termination string, unobserved bool) {
	h.publishOutcomeFlagged(r, provider, status, stream, usage, started, termination, unobserved, false)
}

// abandon accounts a request whose client left before the response was
// delivered. The upstream was billed, so the record is kept (client_abandoned,
// with whatever usage had arrived) but marked Unanswered: it is not a completion
// and must not drive live "completed" signals.
func (h *Handler) abandon(r *http.Request, provider routing.ProviderRef, status int, stream bool, usage promptcache.Usage, started time.Time, unobserved bool) {
	h.publishOutcomeFlagged(r, provider, status, stream, usage, started, TerminationClientAbandoned, unobserved, true)
}

// publishOutcomeFlagged is publishOutcome plus the Unanswered marker for the one
// caller (dispatchAttempts) that accounts a request with no upstream response.
func (h *Handler) publishOutcomeFlagged(r *http.Request, provider routing.ProviderRef, status int, stream bool, usage promptcache.Usage, started time.Time, termination string, unobserved, unanswered bool) {
	// One decision for every emitter (emitOutcome, emitTranslated, the native
	// relay, dispatch): an abandoned request never completed, whichever path
	// recorded it.
	unanswered = unanswered || termination == TerminationClientAbandoned
	if h.opts.OnRequestComplete == nil {
		return
	}
	requestID := ""
	if r != nil {
		requestID = r.Header.Get("X-Request-Id")
	}
	duration := time.Duration(0)
	if !started.IsZero() {
		duration = time.Since(started)
	}
	apiKeyID := ""
	if holder := attributionFrom(r); holder != nil {
		if id := holder.keyID.Load(); id != nil {
			apiKeyID = *id
		}
	}
	if unobserved {
		usage = promptcache.Usage{}
	}
	h.opts.OnRequestComplete(RequestOutcome{
		APIKeyID:        apiKeyID,
		RequestID:       requestID,
		ProviderID:      provider.ProviderID,
		ModelID:         provider.UpstreamModel,
		ConnectionID:    provider.ConnectionID,
		Status:          status,
		InputTokens:     usage.InputTokens,
		OutputTokens:    usage.OutputTokens,
		CacheRead:       usage.CacheReadTokens,
		CacheWrite:      usage.CacheWriteTokens,
		Duration:        duration,
		Stream:          stream,
		Termination:     termination,
		UsageUnobserved: unobserved,
		Unanswered:      unanswered,
	})
}

// Attach registers the public inference routes on the shared mux.
func (h *Handler) Attach(mux *http.ServeMux) {
	for _, prefix := range []string{"/v1", "/v1/v1", "/api/v1"} {
		mux.HandleFunc("GET "+prefix, h.withCORS(h.handleIndex))
		mux.HandleFunc("OPTIONS "+prefix, h.withCORS(h.handlePreflight))
		mux.HandleFunc("OPTIONS "+prefix+"/{rest...}", h.withCORS(h.handlePreflight))
		mux.HandleFunc("GET "+prefix+"/models", h.withCORS(h.handleModels))
		mux.HandleFunc("GET "+prefix+"/models/info", h.withCORS(h.handleModelsInfo))
		mux.HandleFunc("GET "+prefix+"/models/{kind}", h.withCORS(h.handleModelKind))
		mux.HandleFunc("GET "+prefix+"/models/{provider}/{model...}", h.withCORS(h.handleModel))
		mux.HandleFunc("POST "+prefix+"/chat/completions", h.withCORS(h.handleChatCompletions))
		mux.HandleFunc("POST "+prefix+"/responses", h.withCORS(h.handleResponses))
		mux.HandleFunc("POST "+prefix+"/responses/compact", h.withCORS(h.handleResponsesCompact))
		mux.HandleFunc("POST "+prefix+"/messages", h.withCORS(h.handleMessages))
		mux.HandleFunc("POST "+prefix+"/messages/count_tokens", h.withCORS(h.handleCountTokens))
		mux.HandleFunc("POST "+prefix+"/api/chat", h.withCORS(h.handleOllamaChat))
		mux.HandleFunc("POST "+prefix+"/systemone", h.withCORS(h.handleSystemOne))
	}
	mux.HandleFunc("POST /responses", h.withCORS(h.handleResponses))
	mux.HandleFunc("POST /codex/{path...}", h.withCORS(h.handleResponses))
	mux.HandleFunc("POST /codex", h.withCORS(h.handleResponses))
	mux.HandleFunc("OPTIONS /responses", h.withCORS(h.handlePreflight))
	mux.HandleFunc("OPTIONS /codex/{path...}", h.withCORS(h.handlePreflight))
	mux.HandleFunc("OPTIONS /codex", h.withCORS(h.handlePreflight))
	mux.HandleFunc("POST /messages", h.withCORS(h.handleMessages))
	mux.HandleFunc("POST /messages/count_tokens", h.withCORS(h.handleCountTokens))
	mux.HandleFunc("OPTIONS /messages", h.withCORS(h.handlePreflight))
	mux.HandleFunc("OPTIONS /messages/count_tokens", h.withCORS(h.handlePreflight))
	for _, prefix := range []string{"/v1beta", "/api/v1beta"} {
		mux.HandleFunc("GET "+prefix+"/models", h.withCORS(h.handleGeminiModels))
		mux.HandleFunc("OPTIONS "+prefix+"/{rest...}", h.withCORS(h.handlePreflight))
		mux.HandleFunc("POST "+prefix+"/models/{rest...}", h.withCORS(h.handleGeminiRoute))
	}
	mux.HandleFunc("GET /api/tags", h.withCORS(h.handleOllamaTags))
	mux.HandleFunc("OPTIONS /api/tags", h.withCORS(h.handlePreflight))
}

func (h *Handler) handleMessages(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if !h.authorize(w, r) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.opts.MaxBodyBytes))
	if err != nil {
		var limitErr *http.MaxBytesError
		if errors.As(err, &limitErr) {
			h.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds configured limit")
		} else if r.Context().Err() == nil {
			h.writeError(w, http.StatusBadRequest, "request_read_failed", "request body could not be read")
		}
		return
	}
	r = withClientBody(r, body)
	request, err := anthropicadapter.ParseMessagesRequest(body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	var translation ChatTranslation
	response, provider, err := h.dispatchAttempts(r, snapshot, request.Model, anthropicadapter.MessagesProtocol, false, request.Stream, func(provider routing.ProviderRef) (ChatTranslation, error) {
		translation = ChatTranslation{}
		plan, err := routing.BuildPlan(routing.PlanOptions{SourceProtocol: anthropicadapter.MessagesProtocol, Provider: provider})
		if err != nil {
			return ChatTranslation{}, err
		}
		var translated ChatTranslation
		if plan.NativePath() {
			translated.Endpoint = "messages"
			if provider.Endpoint != "" {
				translated.Endpoint = provider.Endpoint
			}
			translated.Body, err = request.MarshalBody(provider.UpstreamModel)
			translated.Headers = anthropicHeaders(r, provider)
		} else if wire.HasCodec(plan.TargetProtocol) {
			model := provider.UpstreamModel
			if model == "" {
				model = request.Model
			}
			translated, err = h.codecTranslation(provider, nil, nil, request, false, model)
			translated.Headers = openAIProviderHeaders(provider)
			if provider.Endpoint != "" {
				translated.Endpoint = provider.Endpoint
			}
		} else if h.opts.TranslateMessages != nil {
			attempt := *request
			attempt.Model = provider.UpstreamModel
			if attempt.Model == "" {
				attempt.Model = request.Model
			}
			attempt.Scope = translationScope(r, provider)
			attempt.Project = provider.Connection.Settings.ProjectID
			attempt.AnthropicBeta = strings.Join(r.Header.Values("Anthropic-Beta"), ",")
			translated, err = h.opts.TranslateMessages(&attempt, plan.TargetProtocol)
			if translated.Headers == nil {
				translated.Headers = http.Header{}
			}
			credentials := openAIProviderHeaders(provider)
			if plan.TargetProtocol == geminiadapter.Protocol {
				credentials = geminiHeaders(provider)
			}
			for name, values := range credentials {
				translated.Headers[name] = values
			}
			if provider.Endpoint != "" {
				translated.Endpoint = provider.Endpoint
			}
		} else {
			err = fmt.Errorf("no Messages translator registered for target protocol %q", plan.TargetProtocol)
		}
		if err != nil {
			return ChatTranslation{}, err
		}
		translation = translated
		return translated, nil
	})
	if err != nil {
		h.dispatchError(w, r, err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 && translation.Response != nil {
		h.writeOpenAITranslation(w, r, response, provider, translation, request.Stream, started)
		return
	}
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(w, response.Body)
		h.emitOutcome(r, provider, response.StatusCode, request.Stream, promptcache.Usage{}, true, started)
		return
	}
	if request.Stream {
		if h.opts.OnRequestComplete != nil {
			scanner := &usageScanner{family: shared.FamilyAnthropic}
			response.Body = struct {
				io.Reader
				io.Closer
			}{Reader: io.TeeReader(response.Body, scanner), Closer: response.Body}
			h.copyMessagesStream(w, r, response)
			usage, _ := scanner.usage()
			observed := scanner.reportsUsage(r.Context().Err() == nil)
			if !observed {
				usage = promptcache.Usage{}
			}
			h.emitOutcome(r, provider, response.StatusCode, true, usage, observed, started)
			return
		}
		h.copyMessagesStream(w, r, response)
		return
	}
	if h.opts.OnRequestComplete != nil {
		responseBody, readErr := io.ReadAll(response.Body)
		usage, observed := promptcache.Usage{}, false
		if readErr == nil {
			if parsed, ok := shared.ParseUsage(shared.FamilyAnthropic, responseBody); ok {
				usage, observed = parsed, true
			}
		}
		_, _ = w.Write(responseBody)
		h.emitOutcome(r, provider, response.StatusCode, false, usage, observed, started)
		return
	}
	_, _ = io.Copy(w, response.Body)
}

// anthropicHeaders sets the provider credential and version headers. Routeweft
// forwards the client's anthropic-beta opt-in but never the client credential.
// Which providers use a bearer credential, an identity fingerprint or a default
// beta is declared by their wire hook (internal/providers/wire).
func anthropicHeaders(r *http.Request, provider routing.ProviderRef) http.Header {
	return wire.MessagesHeaders(provider, r.Header.Get("Anthropic-Version"), r.Header.Get("Anthropic-Beta"))
}

// openAIProviderHeaders applies the provider credential plus any provider
// identity headers declared by its wire hook.
func openAIProviderHeaders(provider routing.ProviderRef) http.Header {
	return wire.OpenAIHeaders(provider)
}

func (h *Handler) handleResponses(w http.ResponseWriter, r *http.Request) {
	h.handleResponsesRequest(w, r, false)
}
func (h *Handler) handleResponsesCompact(w http.ResponseWriter, r *http.Request) {
	h.handleResponsesRequest(w, r, true)
}

func (h *Handler) handleResponsesRequest(w http.ResponseWriter, r *http.Request, compact bool) {
	h.handleOpenAI(w, r, openaiadapter.ResponsesProtocol, compact)
}

func (h *Handler) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	h.handleOpenAI(w, r, openaiadapter.ChatProtocol, false)
}

type clientBodyKey struct{}

// withClientBody records the body the client sent. A provider that reads the
// client's identity from it needs the original: a conversion leg drops fields
// such as metadata.user_id, and the pin resolves the session before translating
// (handlers/chatCore.js:69-74).
func withClientBody(r *http.Request, body []byte) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), clientBodyKey{}, body))
}

// clientBodyOf is the client's original body, or fallback when none was recorded.
func clientBodyOf(r *http.Request, fallback []byte) []byte {
	if body, ok := r.Context().Value(clientBodyKey{}).([]byte); ok {
		return body
	}
	return fallback
}

func (h *Handler) newUpstreamRequest(r *http.Request, snapshot *runtime.RuntimeSnapshot, provider routing.ProviderRef, endpoint string, body []byte, headers http.Header, resolved ...wire.Target) (*http.Request, error) {
	validate := transport.ValidatePublicURL
	if h.opts.AllowPrivateUpstreams {
		validate = transport.ValidateTrustedLocalURL
	}
	var target wire.Target
	if len(resolved) > 0 {
		target = resolved[0]
	} else {
		var err error
		target, err = wire.ResolveTarget(provider, endpoint)
		if err != nil {
			return nil, err
		}
	}
	base, err := target.URL(validate)
	if err != nil {
		return nil, err
	}
	body = h.finalBody(r, snapshot, provider.Protocol, body)
	// Registry quirks are provider normalization, step 8 of AGENTS §6: they run on
	// what the savers and prompt-cache anchoring (steps 2-7) left, so nothing after
	// them can put back a field a quirk removed.
	clientBody := clientBodyOf(r, body)
	body = wire.FinalizeBodyAt(provider, target.Endpoint, body)
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, base.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Routeweft")
	if accept := r.Header.Get("Accept"); accept != "" {
		request.Header.Set("Accept", accept)
	}
	for name, values := range headers {
		request.Header.Del(name)
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	for _, name := range wire.For(provider.ProviderID).ClientHeaders {
		if value := r.Header.Get(name); value != "" {
			request.Header.Set(name, value)
		}
	}
	if provider.ProviderID == "api-airforce" && provider.Protocol == openaiadapter.ChatProtocol {
		request.Header.Set("Accept", "text/event-stream")
	}
	if err := wire.ApplyRequestHeaders(provider, request, body); err != nil {
		return nil, err
	}
	wire.ApplyClientRequestHeaders(provider, request, r.Header, clientBody)
	return request, nil
}

func (h *Handler) copyStream(w http.ResponseWriter, r *http.Request, response *http.Response) {
	flusher, canFlush := w.(http.Flusher)
	reader := bufio.NewReaderSize(response.Body, 32*1024)
	lineCandidate := make([]byte, 0, len("data: [DONE]\r\n"))
	passLine := false
	skipTerminalBlank := false
	eventHasData := false
	eventHasContent := false
	write := func(chunk []byte) bool {
		if len(chunk) == 0 {
			return true
		}
		if _, err := w.Write(chunk); err != nil {
			return false
		}
		if canFlush {
			flusher.Flush()
		}
		return true
	}
	for {
		if err := r.Context().Err(); err != nil {
			return
		}
		line, err := reader.ReadSlice('\n')
		completeLine := !errors.Is(err, bufio.ErrBufferFull)
		bypassedLine := false
		if passLine {
			if !write(line) {
				return
			}
			if completeLine {
				passLine = false
			}
		} else {
			if len(lineCandidate)+len(line) > cap(lineCandidate) {
				eventHasData = eventHasData || isSSEDataLine(lineCandidate)
				eventHasContent = true
				if !write(lineCandidate) || !write(line) {
					return
				}
				lineCandidate = lineCandidate[:0]
				bypassedLine = true
				passLine = !completeLine
			} else {
				lineCandidate = append(lineCandidate, line...)
			}
		}
		if completeLine && !bypassedLine && !passLine {
			switch {
			case isTerminalLine(lineCandidate) && !eventHasData:
				skipTerminalBlank = !eventHasContent
			case skipTerminalBlank && isBlankSSELine(lineCandidate):
				skipTerminalBlank = false
				eventHasData, eventHasContent = false, false
			default:
				skipTerminalBlank = false
				if !write(lineCandidate) {
					return
				}
				if isSSEDataLine(lineCandidate) {
					eventHasData = true
				}
				if isBlankSSELine(lineCandidate) {
					eventHasData, eventHasContent = false, false
				} else {
					eventHasContent = true
				}
			}
			lineCandidate = lineCandidate[:0]
		}
		if err == nil || errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if !errors.Is(err, io.EOF) {
			return
		}
		if !passLine && !isTerminalLine(lineCandidate) && !write(lineCandidate) {
			return
		}
		if r.Context().Err() == nil {
			_ = write([]byte("data: [DONE]\n\n"))
		}
		return
	}
}

func isTerminalLine(line []byte) bool {
	text := strings.TrimSuffix(string(line), "\n")
	text = strings.TrimSuffix(text, "\r")
	return text == "data: [DONE]" || text == "data:[DONE]"
}

func isBlankSSELine(line []byte) bool {
	return bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n"))
}

func isSSEDataLine(line []byte) bool {
	return bytes.HasPrefix(line, []byte("data:"))
}

// usageScanner tracks reported usage without changing relayed stream bytes.
type usageScanner struct {
	family   string
	buffer   []byte
	merged   promptcache.Usage
	found    bool
	complete bool
	skipping bool
	// kiro reads Kiro's event stream, which has no lines.
	kiro shared.KiroUsageScanner
	// commandCode follows CommandCode's finish report, which replaces any other.
	commandCode shared.CommandCodeUsageReader
	// whole reads the body as one JSON document, kept up to the translated
	// response limit; past it nothing is read and the usage is unobserved.
	whole bool
}

// linesOf reports the families that stream bare JSON lines rather than SSE.
func linesOf(family string) bool {
	return family == shared.FamilyOllama || family == shared.FamilyCommandCode
}

func (s *usageScanner) Write(chunk []byte) (int, error) {
	if s.family == shared.FamilyKiro {
		return s.kiro.Write(chunk)
	}
	n := len(chunk)
	if s.whole {
		if !s.skipping {
			if len(s.buffer)+n > openaiadapter.MaxTranslatedResponseBytes {
				s.buffer, s.skipping = nil, true
			} else {
				s.buffer = append(s.buffer, chunk...)
			}
		}
		return n, nil
	}
	if s.skipping {
		index := bytes.IndexByte(chunk, '\n')
		if index < 0 {
			return n, nil
		}
		chunk = chunk[index+1:]
		s.skipping = false
	}
	s.buffer = append(s.buffer, chunk...)
	for {
		index := bytes.IndexByte(s.buffer, '\n')
		if index < 0 {
			if (s.family == shared.FamilySystemOne || linesOf(s.family)) && len(s.buffer) > 4<<20 {
				s.buffer = nil
				s.skipping = true
			}
			break
		}
		line := bytes.TrimSpace(s.buffer[:index])
		s.buffer = s.buffer[index+1:]
		if (s.family == shared.FamilySystemOne || linesOf(s.family)) && len(line) > 4<<20 {
			continue
		}
		var payload []byte
		if s.family == shared.FamilyCommandCode {
			// AI SDK events, one per line; the finish event reports, wholesale.
			s.commandCode.Line(line)
			continue
		}
		if s.family == shared.FamilyOllama {
			// Ollama streams bare JSON lines, not SSE; done:true ends the stream
			// and carries the counts.
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			var last struct {
				Done bool `json:"done"`
			}
			if json.Unmarshal(line, &last) == nil && last.Done {
				s.complete = true
			}
			payload = line
		} else if bytes.HasPrefix(line, []byte("data:")) {
			payload = bytes.TrimSpace(line[len("data:"):])
		} else {
			continue
		}
		if bytes.Equal(payload, []byte("[DONE]")) {
			s.complete = true
			continue
		}
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(payload, &event) == nil && streamTerminalEvent(s.family, event.Type) {
			s.complete = true
		}
		if usage, ok := shared.ParseUsage(s.family, payload); ok {
			s.merged = s.merged.Merge(usage)
			s.found = true
		}
	}
	return n, nil
}

func (s *usageScanner) usage() (promptcache.Usage, bool) {
	if s.family == shared.FamilyKiro {
		return s.kiro.Usage()
	}
	// A line-delimited stream's last line may arrive without its newline; for
	// Ollama that line is the done:true one that holds the counts.
	if (s.family == shared.FamilySystemOne || linesOf(s.family)) && len(s.buffer) > 0 {
		s.Write([]byte("\n"))
	}
	if s.family == shared.FamilyCommandCode {
		return s.commandCode.Usage()
	}
	if s.whole && !s.found && !s.skipping && len(s.buffer) > 0 {
		if usage, ok := shared.ParseUsage(s.family, s.buffer); ok {
			s.merged, s.found, s.complete = usage, true, true
		}
		s.buffer = nil
	}
	return s.merged, s.found
}

// System One has no gateway-defined terminal marker; accounting needs clean EOF.
func (s *usageScanner) reportsUsage(clean bool) bool {
	if s.family == shared.FamilyKiro {
		_, found := s.kiro.Usage()
		return found && s.kiro.Complete()
	}
	if s.family == shared.FamilyCommandCode {
		_, found := s.commandCode.Usage()
		return found && s.commandCode.Complete()
	}
	if !s.found {
		return false
	}
	if s.family == shared.FamilySystemOne {
		return clean
	}
	return s.complete || (clean && shared.GeminiShaped(s.family))
}

// streamTerminalEvent reports whether one decoded SSE event type ends a family's
// stream. Anthropic uses message_stop; OpenAI families end on the [DONE]
// sentinel, which the scanner advances on a blank/generic terminal instead.
func streamTerminalEvent(family, eventType string) bool {
	if family == shared.FamilyAnthropic {
		return eventType == "message_stop"
	}
	switch eventType {
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		// incomplete and failed are terminal Responses events and still report
		// the usage the upstream billed.
		return true
	}
	return false
}

// copyResponsesStream relays Responses SSE events incrementally. It holds one
// terminal event until EOF, filters duplicates/non-final terminals, and reports
// an incomplete upstream stream as response.failed.
func (h *Handler) copyResponsesStream(w http.ResponseWriter, r *http.Request, response *http.Response) {
	copyTerminalSSE(w, r, response, responsesEventTerminal, responsesIncompleteEvent())
}

func (h *Handler) copyMessagesStream(w http.ResponseWriter, r *http.Request, response *http.Response) {
	copyTerminalSSE(w, r, response, messagesEventTerminal, messagesIncompleteEvent())
}

func copyTerminalSSE(w http.ResponseWriter, r *http.Request, response *http.Response, isTerminal func([]byte) bool, incomplete []byte) {
	flusher, canFlush := w.(http.Flusher)
	reader := bufio.NewReaderSize(response.Body, 32*1024)
	event := make([]byte, 0, 4096)
	var terminal []byte
	write := func(chunk []byte) bool {
		if len(chunk) == 0 {
			return true
		}
		if _, err := w.Write(chunk); err != nil {
			return false
		}
		if canFlush {
			flusher.Flush()
		}
		return true
	}
	emit := func() bool {
		if len(event) == 0 {
			return true
		}
		if isTerminal(event) {
			terminal = append(terminal[:0], event...)
			event = event[:0]
			return true
		}
		writeErr := write(event)
		event = event[:0]
		return writeErr
	}
	for {
		if err := r.Context().Err(); err != nil {
			return
		}
		line, err := reader.ReadSlice('\n')
		if len(event)+len(line) > 4<<20 {
			return
		}
		event = append(event, line...)
		if !errors.Is(err, bufio.ErrBufferFull) && isBlankSSELine(line) {
			if !emit() {
				return
			}
		}
		if err == nil || errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if !errors.Is(err, io.EOF) {
			return
		}
		if !emit() {
			return
		}
		if r.Context().Err() != nil {
			return
		}
		if len(terminal) > 0 {
			_ = write(terminal)
		} else {
			_ = write(incomplete)
		}
		return
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for _, name := range []string{"Content-Type", "Cache-Control", "Retry-After", "Openai-Organization", "Openai-Processing-Ms", "X-Request-Id", "Anthropic-Version", "Anthropic-Beta", "Request-Id"} {
		for _, value := range src.Values(name) {
			dst.Add(name, value)
		}
	}
	if dst.Get("Content-Type") == "" {
		dst.Set("Content-Type", "application/json")
	}
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "routeweft.endpoint", "version": 1})
}

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "llm"
	}
	data := publicModels(snapshot, kind, accessFilter(r, snapshot))
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (h *Handler) handleModelsInfo(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", "query parameter id is required")
		return
	}
	for _, model := range publicModels(snapshot, r.URL.Query().Get("kind"), accessFilter(r, snapshot)) {
		if model["id"] == id {
			model["endpoint"] = "/v1/chat/completions"
			if model["kind"] == "systemone" {
				model["endpoint"] = "/v1/systemone"
			}
			writeJSON(w, http.StatusOK, model)
			return
		}
	}
	h.writeError(w, http.StatusNotFound, "model_not_found", "model does not exist or is not accessible")
}

func (h *Handler) handleModel(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	provider := r.PathValue("provider")
	model := r.PathValue("model")
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	if r.URL.Query().Get("kind") != "" {
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": publicModels(snapshot, r.URL.Query().Get("kind"), accessFilter(r, snapshot))})
		return
	}
	for _, entry := range publicModels(snapshot, "llm", accessFilter(r, snapshot)) {
		if entry["id"] == provider+"/"+model {
			writeJSON(w, http.StatusOK, entry)
			return
		}
	}
	h.writeError(w, http.StatusNotFound, "model_not_found", "model does not exist or is not accessible")
}

func (h *Handler) handlePreflight(w http.ResponseWriter, r *http.Request) {
	if origin := h.allowedOrigin(r); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, x-goog-api-key, x-9router-token-saver, x-literouter-token-saver")
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) bool {
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return false
	}
	presented, ok := clientKey(r)
	if snapshot.Settings()["requireApiKey"] == "false" {
		return true
	}
	if !ok {
		h.writeError(w, http.StatusUnauthorized, "missing_api_key", "a Routeweft API key is required")
		return false
	}
	entry, ok := snapshot.APIKeys().Lookup(presented)
	if !ok {
		h.writeError(w, http.StatusUnauthorized, "invalid_api_key", "the Routeweft API key is invalid")
		return false
	}
	if holder := attributionFrom(r); holder != nil {
		id := entry.ID
		holder.keyID.Store(&id)
	}
	return true
}

func clientKey(r *http.Request) (string, bool) {
	var keys []string
	if key, ok := auth.FromAuthorization(r.Header.Get("Authorization")); ok {
		keys = append(keys, key)
	}
	for _, value := range r.Header.Values("X-Api-Key") {
		if key := strings.TrimSpace(value); key != "" {
			keys = append(keys, key)
		}
	}
	if key := strings.TrimSpace(r.Header.Get("X-Goog-Api-Key")); key != "" {
		keys = append(keys, key)
	}
	for _, key := range r.URL.Query()["key"] {
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return "", false
	}
	for _, key := range keys[1:] {
		if subtle.ConstantTimeCompare([]byte(keys[0]), []byte(key)) != 1 {
			return "", false
		}
	}
	return keys[0], true
}

func (h *Handler) withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), attributionKey{}, &attribution{begun: time.Now()}))
		if origin := h.allowedOrigin(r); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			if origin != "*" {
				w.Header().Add("Vary", "Origin")
			}
		}
		next(w, r)
	}
}

func (h *Handler) allowedOrigin(r *http.Request) string {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return ""
	}
	for _, allowed := range h.opts.CORSOrigins {
		if allowed == "*" {
			return "*"
		}
		if strings.EqualFold(allowed, origin) {
			return origin
		}
	}
	return ""
}

func (h *Handler) writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"type": code, "code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func modelObject(model runtime.Model) map[string]any {
	object := map[string]any{
		"id":       model.ID,
		"object":   "model",
		"created":  0,
		"owned_by": model.ProviderID,
		"provider": model.ProviderID,
	}
	if model.Name != "" {
		object["name"] = model.Name
	}
	if model.ContextWindow > 0 {
		object["context_window"] = model.ContextWindow
	}
	if len(model.Capabilities) > 0 {
		object["capabilities"] = model.Capabilities
	}
	if model.Source != "" {
		object["source"] = model.Source
	}
	return object
}
