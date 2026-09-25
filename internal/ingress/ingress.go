// Package ingress owns public inference HTTP endpoints.
package ingress

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	geminiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	ollamaadapter "github.com/raufimusaddiq/routeweft/internal/protocol/ollama"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	systemoneadapter "github.com/raufimusaddiq/routeweft/internal/protocol/systemone"
	anthropicprovider "github.com/raufimusaddiq/routeweft/internal/providers/anthropic"
	claudeprovider "github.com/raufimusaddiq/routeweft/internal/providers/claude"
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
	State                     *runtime.RuntimeState
	Strategy                  routing.Strategy
	StickyLimit               uint64
	// OnUsage receives upstream-reported token counts, including cache counts.
	// The later telemetry PR wires this callback to the bounded Usage queue.
	OnUsage func(providerID, model string, usage promptcache.Usage)
	// TransformFinalBody runs Routeweft token savers on the final outbound body
	// before cache anchors are applied (BDR-012). Errors leave the body unchanged.
	TransformFinalBody func(protocol string, body []byte) ([]byte, error)
	// AllowPrivateUpstreams is the explicit trusted-local operator policy. It is
	// off by default so operator-supplied provider URLs cannot reach loopback,
	// LAN, or metadata addresses.
	AllowPrivateUpstreams bool
	// EndpointFor returns the provider-specific relative dispatch path for a
	// native transport, so multi-transport providers that serve a different path
	// per protocol (SPEC §11 base endpoint rules) do not reuse one shared path.
	// It returns ok=false to keep the caller's shared default endpoint.
	EndpointFor func(providerID string, transport string) (string, bool)
	// ClientFor returns the pooled outbound client for one connection's compiled
	// proxy policy, so a per-connection proxy pool is honored on the request path
	// without building a transport per request (SPEC §20, PRD-ROUTE-005). It
	// returns nil to use the handler's default SSRF-protected client.
	ClientFor func(connectionID string) *http.Client
}

// ProviderResolver is request-time code that uses only the already-loaded
// snapshot to resolve the requested model/provider.
type ProviderResolver func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool)

// ChatTranslation describes one translated request relative to the provider
// base URL. Target adapters own endpoint and protocol-specific headers.
type ChatTranslation struct {
	Endpoint string
	Body     []byte
	Headers  http.Header
}

// ChatTranslator is the later-adapter hook for a different target protocol.
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

// Attach registers the public inference routes on the shared mux.
func (h *Handler) Attach(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1", h.withCORS(h.handleIndex))
	mux.HandleFunc("OPTIONS /v1", h.withCORS(h.handlePreflight))
	mux.HandleFunc("GET /v1/models", h.withCORS(h.handleModels))
	mux.HandleFunc("GET /v1/models/info", h.withCORS(h.handleModelsInfo))
	mux.HandleFunc("GET /v1/models/{provider}/{model...}", h.withCORS(h.handleModel))
	mux.HandleFunc("OPTIONS /v1/{rest...}", h.withCORS(h.handlePreflight))
	mux.HandleFunc("POST /v1/chat/completions", h.withCORS(h.handleChatCompletions))
	mux.HandleFunc("POST /v1/responses", h.withCORS(h.handleResponses))
	mux.HandleFunc("POST /v1/responses/compact", h.withCORS(h.handleResponsesCompact))
	mux.HandleFunc("POST /responses", h.withCORS(h.handleResponses))
	mux.HandleFunc("POST /codex/{path...}", h.withCORS(h.handleResponses))
	mux.HandleFunc("POST /codex", h.withCORS(h.handleResponses))
	mux.HandleFunc("POST /v1/v1/responses", h.withCORS(h.handleResponses))
	mux.HandleFunc("POST /v1/v1/responses/compact", h.withCORS(h.handleResponsesCompact))
	mux.HandleFunc("OPTIONS /responses", h.withCORS(h.handlePreflight))
	mux.HandleFunc("OPTIONS /codex/{path...}", h.withCORS(h.handlePreflight))
	mux.HandleFunc("OPTIONS /codex", h.withCORS(h.handlePreflight))
	mux.HandleFunc("POST /v1/messages", h.withCORS(h.handleMessages))
	mux.HandleFunc("POST /v1/messages/count_tokens", h.withCORS(h.handleCountTokens))
	mux.HandleFunc("POST /messages", h.withCORS(h.handleMessages))
	mux.HandleFunc("POST /messages/count_tokens", h.withCORS(h.handleCountTokens))
	mux.HandleFunc("POST /v1/v1/messages", h.withCORS(h.handleMessages))
	mux.HandleFunc("POST /v1/v1/messages/count_tokens", h.withCORS(h.handleCountTokens))
	mux.HandleFunc("OPTIONS /messages", h.withCORS(h.handlePreflight))
	mux.HandleFunc("OPTIONS /messages/count_tokens", h.withCORS(h.handlePreflight))
	mux.HandleFunc("GET /v1beta/models", h.withCORS(h.handleGeminiModels))
	mux.HandleFunc("OPTIONS /v1beta/{rest...}", h.withCORS(h.handlePreflight))
	mux.HandleFunc("POST /v1beta/models/{rest...}", h.withCORS(h.handleGeminiRoute))
	mux.HandleFunc("POST /v1/api/chat", h.withCORS(h.handleOllamaChat))
	mux.HandleFunc("POST /v1/systemone", h.withCORS(h.handleSystemOne))
	mux.HandleFunc("OPTIONS /v1/systemone", h.withCORS(h.handlePreflight))
}

func (h *Handler) handleMessages(w http.ResponseWriter, r *http.Request) {
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
	if h.opts.ProviderResolver == nil {
		h.writeError(w, http.StatusServiceUnavailable, "no_provider", "no Anthropic Messages provider is configured")
		return
	}
	provider, ok := h.opts.ProviderResolver(snapshot, request.Model)
	if !ok {
		h.writeError(w, http.StatusNotFound, "model_not_found", fmt.Sprintf("model %q is not configured for inference", request.Model))
		return
	}
	plan, err := routing.BuildPlan(routing.PlanOptions{SourceProtocol: anthropicadapter.MessagesProtocol, TargetProtocol: provider.Protocol, Provider: provider})
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "route_unavailable", err.Error())
		return
	}
	endpoint := "messages"
	var outbound []byte
	var headers http.Header
	if plan.NativePath() {
		if h.opts.EndpointFor != nil {
			if override, ok := h.opts.EndpointFor(provider.ProviderID, provider.Protocol); ok {
				endpoint = override
			}
		}
		// Sprint 4 owns Routeweft-side token savers and cache anchoring. Until
		// those transforms exist, preserve the native body (including client
		// cache_control markers) rather than normalize or drop fields here.
		outbound, err = request.MarshalBody(provider.UpstreamModel)
		headers = anthropicHeaders(r, provider)
	} else if h.opts.TranslateMessages != nil {
		var translated ChatTranslation
		translated, err = h.opts.TranslateMessages(request, plan.TargetProtocol)
		endpoint, outbound, headers = translated.Endpoint, translated.Body, translated.Headers
	} else {
		err = fmt.Errorf("no Messages translator registered for target protocol %q", plan.TargetProtocol)
	}
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "request_translation_failed", err.Error())
		return
	}
	if h.opts.TransformFinalBody != nil {
		if transformed, transformErr := h.opts.TransformFinalBody(provider.Protocol, outbound); transformErr == nil {
			outbound = transformed
		}
	}
	if provider.Protocol == anthropicadapter.MessagesProtocol {
		if anchored, anchorErr := promptcache.Anchor(outbound, promptcache.DefaultBudget); anchorErr == nil {
			outbound = anchored
		}
	}
	upstreamRequest, err := h.newUpstreamRequest(r, provider, endpoint, outbound, headers)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "upstream_configuration_error", err.Error())
		return
	}
	response, err := h.clientFor(provider).Do(upstreamRequest)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		h.writeError(w, http.StatusBadGateway, "upstream_request_failed", "upstream request failed")
		return
	}
	defer response.Body.Close()
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		prefix, _ := io.ReadAll(io.LimitReader(response.Body, errorBodyPeek))
		h.recordUpstreamFailure(provider, response.StatusCode, response.Header, prefix)
		_, _ = w.Write(prefix)
		_, _ = io.Copy(w, response.Body)
		return
	}
	if request.Stream {
		if h.opts.OnUsage != nil {
			scanner := &usageScanner{family: shared.FamilyAnthropic}
			response.Body = struct {
				io.Reader
				io.Closer
			}{Reader: io.TeeReader(response.Body, scanner), Closer: response.Body}
			h.copyMessagesStream(w, r, response)
			if usage, ok := scanner.usage(); ok && scanner.reportsUsage(r.Context().Err() == nil) {
				h.opts.OnUsage(provider.ProviderID, provider.UpstreamModel, usage)
			}
			return
		}
		h.copyMessagesStream(w, r, response)
		return
	}
	if h.opts.OnUsage != nil {
		responseBody, readErr := io.ReadAll(response.Body)
		if readErr == nil {
			if usage, ok := shared.ParseUsage(shared.FamilyAnthropic, responseBody); ok {
				h.opts.OnUsage(provider.ProviderID, provider.UpstreamModel, usage)
			}
		}
		_, _ = w.Write(responseBody)
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
	return headers
}

func (h *Handler) handleResponses(w http.ResponseWriter, r *http.Request) {
	h.handleResponsesRequest(w, r, false)
}
func (h *Handler) handleResponsesCompact(w http.ResponseWriter, r *http.Request) {
	h.handleResponsesRequest(w, r, true)
}

func (h *Handler) handleResponsesRequest(w http.ResponseWriter, r *http.Request, compact bool) {
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
	request, err := openaiadapter.ParseResponsesRequest(body, compact)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	if h.opts.ProviderResolver == nil {
		h.writeError(w, http.StatusServiceUnavailable, "no_provider", "no OpenAI Responses provider is configured")
		return
	}
	provider, ok := h.opts.ProviderResolver(snapshot, request.Model)
	if !ok {
		h.writeError(w, http.StatusNotFound, "model_not_found", fmt.Sprintf("model %q is not configured for inference", request.Model))
		return
	}
	plan, err := routing.BuildPlan(routing.PlanOptions{SourceProtocol: openaiadapter.ResponsesProtocol, TargetProtocol: provider.Protocol, Provider: provider})
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "route_unavailable", err.Error())
		return
	}
	endpoint := "responses"
	if compact {
		endpoint += "/compact"
	}
	var outbound []byte
	var headers http.Header
	if plan.NativePath() {
		if h.opts.EndpointFor != nil && !compact {
			if override, ok := h.opts.EndpointFor(provider.ProviderID, provider.Protocol); ok {
				endpoint = override
			}
		}
		outbound, err = request.MarshalBody(provider.UpstreamModel)
		headers = openAIProviderHeaders(provider)
	} else if compact && h.opts.TranslateResponsesCompact != nil {
		var translated ChatTranslation
		translated, err = h.opts.TranslateResponsesCompact(request, plan.TargetProtocol)
		endpoint, outbound, headers = translated.Endpoint, translated.Body, translated.Headers
	} else if !compact && h.opts.TranslateResponses != nil {
		var translated ChatTranslation
		translated, err = h.opts.TranslateResponses(request, plan.TargetProtocol)
		endpoint, outbound, headers = translated.Endpoint, translated.Body, translated.Headers
	} else {
		err = fmt.Errorf("no Responses translator registered for target protocol %q", plan.TargetProtocol)
	}
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "request_translation_failed", err.Error())
		return
	}
	upstreamRequest, err := h.newUpstreamRequest(r, provider, endpoint, outbound, headers)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "upstream_configuration_error", err.Error())
		return
	}
	response, err := h.clientFor(provider).Do(upstreamRequest)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		h.writeError(w, http.StatusBadGateway, "upstream_request_failed", "upstream request failed")
		return
	}
	defer response.Body.Close()
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	if request.Stream {
		h.copyResponsesStream(w, r, response)
		return
	}
	_, _ = io.Copy(w, response.Body)
}

func (h *Handler) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
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
	request, err := openaiadapter.ParseChatRequest(body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	if h.opts.ProviderResolver == nil {
		h.writeError(w, http.StatusServiceUnavailable, "no_provider", "no OpenAI Chat provider is configured")
		return
	}
	provider, ok := h.opts.ProviderResolver(snapshot, request.Model)
	if !ok {
		h.writeError(w, http.StatusNotFound, "model_not_found", fmt.Sprintf("model %q is not configured for inference", request.Model))
		return
	}
	plan, err := routing.BuildPlan(routing.PlanOptions{SourceProtocol: openaiadapter.ChatProtocol, TargetProtocol: provider.Protocol, Provider: provider})
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "route_unavailable", err.Error())
		return
	}
	endpoint := "chat/completions"
	var outbound []byte
	var headers http.Header
	if plan.NativePath() {
		if h.opts.EndpointFor != nil {
			if override, ok := h.opts.EndpointFor(provider.ProviderID, provider.Protocol); ok {
				endpoint = override
			}
		}
		outbound, err = request.MarshalBody(provider.UpstreamModel)
		headers = openAIProviderHeaders(provider)
	} else if h.opts.TranslateChat != nil {
		var translated ChatTranslation
		translated, err = h.opts.TranslateChat(request, plan.TargetProtocol)
		endpoint, outbound, headers = translated.Endpoint, translated.Body, translated.Headers
	} else {
		err = fmt.Errorf("no Chat Completions translator registered for target protocol %q", plan.TargetProtocol)
	}
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "request_translation_failed", err.Error())
		return
	}
	upstreamRequest, err := h.newUpstreamRequest(r, provider, endpoint, outbound, headers)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, "upstream_configuration_error", err.Error())
		return
	}
	response, err := h.clientFor(provider).Do(upstreamRequest)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		h.writeError(w, http.StatusBadGateway, "upstream_request_failed", "upstream request failed")
		return
	}
	defer response.Body.Close()
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	if request.Stream {
		h.copyStream(w, r, response)
		return
	}
	_, _ = io.Copy(w, response.Body)
}

func (h *Handler) newUpstreamRequest(r *http.Request, provider routing.ProviderRef, endpoint string, body []byte, headers http.Header) (*http.Request, error) {
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

// usageScanner observes a streamed SSE body for one protocol family and merges
// the usage fields the upstream reports. It only latches completion on the
// family's terminal event so a cancelled stream never reports partial
// accounting (SPEC §21 bounded Usage, PRD §13).
type usageScanner struct {
	family   string
	buffer   []byte
	merged   promptcache.Usage
	found    bool
	complete bool
}

func (s *usageScanner) Write(chunk []byte) (int, error) {
	s.buffer = append(s.buffer, chunk...)
	for {
		index := bytes.IndexByte(s.buffer, '\n')
		if index < 0 {
			break
		}
		line := bytes.TrimSpace(s.buffer[:index])
		s.buffer = s.buffer[index+1:]
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
	return len(chunk), nil
}

func (s *usageScanner) usage() (promptcache.Usage, bool) { return s.merged, s.found }

// reportsUsage reports whether a scanner observed a terminal marker or, for
// Gemini streams that end by closing the response with no terminal event, a
// clean stream end. A cancelled or errored relay passes clean=false so partial
// accounting is never reported (SPEC §21).
func (s *usageScanner) reportsUsage(clean bool) bool {
	if !s.found {
		return false
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
	return eventType == "response.completed" || eventType == "response.done"
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
	data := make([]map[string]any, 0)
	for _, model := range snapshot.Models() {
		data = append(data, modelObject(model))
	}
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
	models := snapshot.Models()
	providers := map[string]int{}
	for _, model := range models {
		providers[model.ProviderID]++
	}
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		data = append(data, modelObject(model))
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "routeweft.models.info", "count": len(models), "providers": providers, "data": data})
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
	resolved, ok := snapshot.ResolveModel(provider, model)
	if !ok {
		h.writeError(w, http.StatusNotFound, "model_not_found", fmt.Sprintf("model %s/%s is not available", provider, model))
		return
	}
	writeJSON(w, http.StatusOK, modelObject(resolved))
}

func (h *Handler) handlePreflight(w http.ResponseWriter, r *http.Request) {
	if origin := h.allowedOrigin(r); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, x-goog-api-key")
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
	if _, ok := snapshot.APIKeys().Lookup(presented); !ok {
		h.writeError(w, http.StatusUnauthorized, "invalid_api_key", "the Routeweft API key is invalid")
		return false
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
