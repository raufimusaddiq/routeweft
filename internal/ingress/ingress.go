// Package ingress owns public inference HTTP endpoints.
package ingress

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// Snapshot provides the immutable request-serving configuration.
type Snapshot interface {
	Load() (*runtime.RuntimeSnapshot, error)
}

// Options configure public ingress behavior such as limits and CORS policy.
type Options struct {
	MaxBodyBytes     int64
	CORSOrigins      []string
	ProviderResolver ProviderResolver
	TranslateChat    ChatTranslator
	// AllowPrivateUpstreams is the explicit trusted-local operator policy. It is
	// off by default so operator-supplied provider URLs cannot reach loopback,
	// LAN, or metadata addresses.
	AllowPrivateUpstreams bool
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

// Attach registers the public inference routes on the shared mux.
func (h *Handler) Attach(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1", h.withCORS(h.handleIndex))
	mux.HandleFunc("OPTIONS /v1", h.withCORS(h.handlePreflight))
	mux.HandleFunc("GET /v1/models", h.withCORS(h.handleModels))
	mux.HandleFunc("GET /v1/models/info", h.withCORS(h.handleModelsInfo))
	mux.HandleFunc("GET /v1/models/{provider}/{model...}", h.withCORS(h.handleModel))
	mux.HandleFunc("OPTIONS /v1/{rest...}", h.withCORS(h.handlePreflight))
	mux.HandleFunc("POST /v1/chat/completions", h.withCORS(h.handleChatCompletions))
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
		outbound, err = request.MarshalBody(provider.UpstreamModel)
		if provider.APIToken != "" {
			headers = http.Header{"Authorization": {"Bearer " + provider.APIToken}}
		}
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
	response, err := h.client.Do(upstreamRequest)
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
	if endpoint == "" || strings.HasPrefix(endpoint, "/") || strings.ContainsAny(endpoint, "?#") {
		return nil, errors.New("provider endpoint must be a relative path without query or fragment")
	}
	for _, segment := range strings.Split(endpoint, "/") {
		if segment == "." || segment == ".." {
			return nil, errors.New("provider endpoint must not traverse paths")
		}
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + endpoint
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
	buffer := make([]byte, 32*1024)
	for {
		if err := r.Context().Err(); err != nil {
			return
		}
		n, err := response.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			return
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for _, name := range []string{"Content-Type", "Cache-Control", "Retry-After", "Openai-Organization", "Openai-Processing-Ms", "X-Request-Id"} {
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
