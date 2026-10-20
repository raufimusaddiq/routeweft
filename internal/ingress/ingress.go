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
	ollamaadapter "github.com/raufimusaddiq/routeweft/internal/protocol/ollama"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	systemoneadapter "github.com/raufimusaddiq/routeweft/internal/protocol/systemone"
	anthropicprovider "github.com/raufimusaddiq/routeweft/internal/providers/anthropic"
	claudeprovider "github.com/raufimusaddiq/routeweft/internal/providers/claude"
	clineprovider "github.com/raufimusaddiq/routeweft/internal/providers/cline"
	codexprovider "github.com/raufimusaddiq/routeweft/internal/providers/codex"
	"github.com/raufimusaddiq/routeweft/internal/providers/oauthheaders"
	"github.com/raufimusaddiq/routeweft/internal/providers/shared"
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
}

const (
	TerminationClientAbandoned   = "client_abandoned"
	TerminationUpstreamTruncated = "upstream_truncated"
)

// attribution carries the authenticated key id from authorize to the completion
// record without re-running key lookup, so a key revoked mid-request is still
// attributed correctly.
type attribution struct{ keyID atomic.Pointer[string] }

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

// emitOutcome publishes one accounting record when the telemetry hook is wired
// and the client did not cancel. requestID is the correlation id the app
// middleware assigned and echoed on the request.
func (h *Handler) emitOutcome(r *http.Request, provider routing.ProviderRef, status int, stream bool, usage promptcache.Usage, started time.Time) {
	if r != nil && r.Context().Err() != nil {
		return
	}
	h.publishOutcome(r, provider, status, stream, usage, started, "", false)
}

// publishOutcome builds and publishes one record without the cancellation
// guard, so a request the client abandoned can still be accounted for.
func (h *Handler) publishOutcome(r *http.Request, provider routing.ProviderRef, status int, stream bool, usage promptcache.Usage, started time.Time, termination string, unobserved bool) {
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
	response, provider, err := h.dispatchAttempts(r, snapshot, request.Model, anthropicadapter.MessagesProtocol, false, func(provider routing.ProviderRef) (ChatTranslation, error) {
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
		} else if h.opts.TranslateMessages != nil {
			attempt := *request
			attempt.Model = provider.UpstreamModel
			if attempt.Model == "" {
				attempt.Model = request.Model
			}
			translated, err = h.opts.TranslateMessages(&attempt, plan.TargetProtocol)
			if translated.Headers == nil {
				translated.Headers = http.Header{}
			}
			for name, values := range openAIProviderHeaders(provider) {
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
		h.emitOutcome(r, provider, response.StatusCode, request.Stream, promptcache.Usage{}, started)
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
			if !scanner.reportsUsage(r.Context().Err() == nil) {
				usage = promptcache.Usage{}
			}
			h.emitOutcome(r, provider, response.StatusCode, true, usage, started)
			return
		}
		h.copyMessagesStream(w, r, response)
		return
	}
	if h.opts.OnRequestComplete != nil {
		responseBody, readErr := io.ReadAll(response.Body)
		usage := promptcache.Usage{}
		if readErr == nil {
			if parsed, ok := shared.ParseUsage(shared.FamilyAnthropic, responseBody); ok {
				usage = parsed
			}
		}
		_, _ = w.Write(responseBody)
		h.emitOutcome(r, provider, response.StatusCode, false, usage, started)
		return
	}
	_, _ = io.Copy(w, response.Body)
}

// anthropicHeaders sets the provider credential and version headers. Routeweft
// forwards the client's anthropic-beta opt-in but never the client credential.
// Claude and GitHub Copilot OAuth connections authenticate with a bearer token
// and identify as their native client; API-key connections use x-api-key.
func anthropicHeaders(r *http.Request, provider routing.ProviderRef) http.Header {
	headers := http.Header{}
	// Claude and GitHub Copilot advertise Anthropic Messages natively but their
	// credential is a bearer token, not an x-api-key. Copilot additionally needs
	// its identity fingerprint on the native Messages route, not only on the
	// OpenAI Chat/Responses path.
	bearer := provider.ProviderID == "claude" || provider.ProviderID == "github"
	if provider.APIToken != "" {
		if bearer {
			headers.Set("Authorization", "Bearer "+provider.APIToken)
		} else {
			headers.Set("X-Api-Key", provider.APIToken)
		}
	}
	if provider.ProviderID == "claude" {
		for name, value := range claudeprovider.Headers() {
			headers.Set(name, value)
		}
	}
	for name, value := range oauthheaders.Headers(provider.ProviderID) {
		headers.Set(name, value)
	}
	if version := r.Header.Get("Anthropic-Version"); version != "" {
		headers.Set("Anthropic-Version", version)
	} else {
		headers.Set("Anthropic-Version", anthropicprovider.Version)
	}
	if beta := r.Header.Get("Anthropic-Beta"); beta != "" {
		headers.Set("Anthropic-Beta", beta)
	} else if provider.ProviderID == "anthropic" {
		headers.Set("Anthropic-Beta", anthropicprovider.Beta)
	}
	return headers
}

// openAIProviderHeaders applies the provider credential plus any provider
// identity headers, such as the Codex CLI fingerprint on Codex Responses.
func openAIProviderHeaders(provider routing.ProviderRef) http.Header {
	headers := http.Header{}
	if provider.APIToken != "" {
		headers.Set("Authorization", "Bearer "+provider.APIToken)
	}
	if provider.ProviderID == "codex" {
		for name, value := range codexprovider.Headers() {
			headers.Set(name, value)
		}
	}
	for name, value := range oauthheaders.Headers(provider.ProviderID) {
		headers.Set(name, value)
	}
	if clineprovider.Applies(provider.ProviderID) {
		// The full clineHeaders set replaces the plain bearer credential: a WorkOS
		// JWT is sent as `Bearer workos:<jwt>`, any other token verbatim.
		for name, value := range clineprovider.Headers(provider.APIToken) {
			headers.Set(name, value)
		}
	}
	return headers
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

func (h *Handler) newUpstreamRequest(r *http.Request, snapshot *runtime.RuntimeSnapshot, provider routing.ProviderRef, endpoint string, body []byte, headers http.Header) (*http.Request, error) {
	validate := transport.ValidatePublicURL
	if h.opts.AllowPrivateUpstreams {
		validate = transport.ValidateTrustedLocalURL
	}
	base, err := validate(provider.BaseURL)
	if err != nil {
		return nil, err
	}
	if endpoint != "" {
		if strings.HasPrefix(endpoint, "/") || strings.ContainsAny(endpoint, "?#") {
			return nil, errors.New("provider endpoint must be a relative path without query or fragment")
		}
		for _, segment := range strings.Split(endpoint, "/") {
			if segment == "." || segment == ".." {
				return nil, errors.New("provider endpoint must not traverse paths")
			}
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/" + endpoint
	}
	base.RawPath = ""
	body = h.finalBody(r, snapshot, provider.Protocol, body)
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
}

func (s *usageScanner) Write(chunk []byte) (int, error) {
	n := len(chunk)
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
			if s.family == shared.FamilySystemOne && len(s.buffer) > 4<<20 {
				s.buffer = nil
				s.skipping = true
			}
			break
		}
		line := bytes.TrimSpace(s.buffer[:index])
		s.buffer = s.buffer[index+1:]
		if s.family == shared.FamilySystemOne && len(line) > 4<<20 {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
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
	if s.family == shared.FamilySystemOne && len(s.buffer) > 0 {
		s.Write([]byte("\n"))
	}
	return s.merged, s.found
}

// System One has no gateway-defined terminal marker; accounting needs clean EOF.
func (s *usageScanner) reportsUsage(clean bool) bool {
	if !s.found {
		return false
	}
	if s.family == shared.FamilySystemOne {
		return clean
	}
	return s.complete || (clean && s.family == shared.FamilyGemini)
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
		r = r.WithContext(context.WithValue(r.Context(), attributionKey{}, &attribution{}))
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
