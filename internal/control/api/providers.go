package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/cloudflare"
	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

type nodeRequest struct {
	Kind       string   `json:"kind"`
	ProviderID string   `json:"providerId"`
	Name       string   `json:"name"`
	Prefix     string   `json:"prefix"`
	BaseURL    string   `json:"baseUrl"`
	Transports []string `json:"transports"`
}

type connectionRequest struct {
	NodeID     string             `json:"nodeId"`
	ProviderID string             `json:"providerId"`
	Name       string             `json:"name"`
	AuthKind   string             `json:"authKind"`
	Identity   string             `json:"identity"`
	Secret     credentials.Secret `json:"secret"`
	Enabled    *bool              `json:"enabled"`
	Priority   *int               `json:"priority"`
	ProxyPool  *string            `json:"proxyPoolId"`
	AccountID  *string            `json:"accountId"`
}

type modelRequest struct {
	ProviderID string   `json:"providerId"`
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Context    int      `json:"contextWindow"`
	Caps       []string `json:"capabilities"`
	Kind       string   `json:"kind"`
}

func (h *Handler) handlePutProviderNode(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "configure ROUTEWEFT_CREDENTIAL_KEY to manage providers")
		return
	}
	var body nodeRequest
	if !decodeJSON(w, r, 32<<10, &body) {
		return
	}
	if r.Method == http.MethodPatch {
		existing, err := h.opts.Credentials.GetNode(r.Context(), r.PathValue("id"))
		if err != nil {
			providerNotFound(w, err)
			return
		}
		if existing.Kind != credentials.NodeGeneric {
			writeError(w, http.StatusForbidden, "builtin_immutable", "built-in provider definitions cannot be edited")
			return
		}
		if body.ProviderID != "" && body.ProviderID != existing.ProviderID || body.Prefix != "" && body.Prefix != existing.Prefix || body.Kind != "" && credentials.NodeKind(body.Kind) != credentials.NodeGeneric {
			writeError(w, http.StatusBadRequest, "generic_identity_immutable", "Generic Provider prefix and kind cannot change after creation")
			return
		}
		if body.Kind == "" {
			body.Kind = string(existing.Kind)
		}
		if body.ProviderID == "" {
			body.ProviderID = existing.ProviderID
		}
		if body.Name == "" {
			body.Name = existing.Name
		}
		if body.Prefix == "" {
			body.Prefix = existing.Prefix
		}
		if body.BaseURL == "" {
			body.BaseURL = existing.BaseURL
		}
		if len(body.Transports) == 0 {
			body.Transports = existing.Transports
		}
	}
	kind := credentials.NodeKind(body.Kind)
	if kind == "" && r.Method == http.MethodPatch {
		if existing, err := h.opts.Credentials.GetNode(r.Context(), r.PathValue("id")); err == nil {
			kind = existing.Kind
		}
	}
	if kind == credentials.NodeGeneric {
		parsed, err := h.validateOutboundURL(body.BaseURL)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_base_url", "provider base URL must be a valid allowed HTTP(S) origin")
			return
		}
		body.BaseURL = parsed.String()
		if strings.TrimSpace(body.Prefix) == "" || len(body.Transports) == 0 {
			writeError(w, http.StatusBadRequest, "invalid_provider", "Generic Provider requires a prefix and at least one transport")
			return
		}
		seen := map[string]bool{}
		for _, item := range body.Transports {
			if item != "openai-chat" && item != "openai-responses" && item != "anthropic-messages" && item != "systemone" || seen[item] || item == "systemone" && len(body.Transports) != 1 {
				writeError(w, http.StatusBadRequest, "invalid_transport", "Generic Provider transport is unsupported or duplicated")
				return
			}
			seen[item] = true
		}
		body.Prefix = strings.TrimSpace(body.Prefix)
		body.ProviderID = body.Prefix
	} else if kind == credentials.NodeBuiltin {
		spec, ok := h.providerSpec(body.ProviderID)
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown_provider", "provider is not in the built-in catalog")
			return
		}
		if body.Name == "" {
			body.Name = spec.ID
		}
		if body.BaseURL == "" {
			body.BaseURL = spec.DefaultBaseURL
		}
		if len(body.Transports) == 0 {
			for _, value := range spec.Transports {
				body.Transports = append(body.Transports, string(value))
			}
		}
	} else {
		writeError(w, http.StatusBadRequest, "invalid_provider", "provider node kind must be builtin or generic")
		return
	}
	if strings.TrimSpace(body.BaseURL) != "" {
		parsed, err := h.validateOutboundURL(body.BaseURL)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_base_url", "provider base URL must be a valid allowed HTTP(S) origin")
			return
		}
		body.BaseURL = parsed.String()
	}
	node, err := h.opts.Credentials.PutNode(r.Context(), credentials.Node{ID: r.PathValue("id"), Kind: kind, ProviderID: body.ProviderID, Name: body.Name, Prefix: body.Prefix, BaseURL: body.BaseURL, Transports: body.Transports})
	if err != nil {
		writeError(w, http.StatusBadRequest, "provider_save_failed", "provider definition could not be saved")
		return
	}
	if h.opts.Events != nil {
		h.opts.Events.Publish("config.updated", map[string]any{"resource": "provider", "providerId": node.ProviderID})
	}
	writeJSON(w, http.StatusOK, nodeJSON(node))
}

func (h *Handler) handleDeleteProviderNode(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "provider credential storage is not configured")
		return
	}
	id := r.PathValue("id")
	connectionIDs := []string{}
	if h.opts.DB != nil {
		rows, queryErr := h.opts.DB.QueryContext(r.Context(), "SELECT id FROM provider_connections WHERE node_id=?", id)
		if queryErr == nil {
			for rows.Next() {
				var connectionID string
				if rows.Scan(&connectionID) == nil {
					connectionIDs = append(connectionIDs, connectionID)
				}
			}
			_ = rows.Close()
		}
	}
	err := h.opts.Credentials.DeleteGenericNode(r.Context(), id)
	if errors.Is(err, credentials.ErrNotFound) {
		writeError(w, http.StatusNotFound, "provider_not_found", "Generic Provider was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "provider_delete_failed", "Generic Provider could not be deleted")
		return
	}
	if h.opts.CredentialRegistry != nil {
		for _, connectionID := range connectionIDs {
			h.opts.CredentialRegistry.Forget(connectionID)
		}
	}
	if h.opts.PoolBindings != nil {
		if _, err := h.opts.PoolBindings.RefreshPoolBindings(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "provider_publish_failed", "provider deleted but runtime proxy bindings could not be refreshed")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

func (h *Handler) handlePutConnection(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "configure ROUTEWEFT_CREDENTIAL_KEY to manage provider connections")
		return
	}
	var body connectionRequest
	if !decodeJSON(w, r, 1<<20, &body) {
		return
	}
	if r.Method == http.MethodPatch {
		old, err := h.opts.Credentials.GetConnection(r.Context(), r.PathValue("id"))
		if err != nil {
			providerNotFound(w, err)
			return
		}
		if body.NodeID == "" {
			body.NodeID = old.NodeID
		}
		if body.ProviderID == "" {
			body.ProviderID = old.ProviderID
		}
		if body.Name == "" {
			body.Name = old.Name
		}
		if body.AuthKind == "" {
			body.AuthKind = string(old.AuthKind)
		}
		if body.Identity == "" {
			body.Identity = old.Identity
		}
		if body.Enabled == nil {
			body.Enabled = &old.Enabled
		}
		if body.Priority == nil {
			body.Priority = &old.Priority
		}
		if body.ProxyPool == nil {
			body.ProxyPool = &old.ProxyPoolID
		}
		if body.AccountID == nil {
			body.AccountID = &old.AccountID
		}
	}
	accountID := ""
	if body.AccountID != nil {
		accountID = *body.AccountID
	}
	if accountID != "" && !cloudflare.ValidAccountID(accountID) {
		writeError(w, http.StatusBadRequest, "invalid_account_id", "account ID must be a single alphanumeric, hyphen or underscore path segment")
		return
	}
	var node credentials.Node
	var err error
	if body.NodeID != "" {
		node, err = h.opts.Credentials.GetNode(r.Context(), body.NodeID)
	} else if body.ProviderID != "" {
		node, err = h.builtinNode(r.Context(), body.ProviderID)
		if errors.Is(err, errUnknownProvider) {
			writeError(w, http.StatusBadRequest, "unknown_provider", "provider is not in the built-in catalog")
			return
		}
	} else {
		writeError(w, http.StatusBadRequest, "node_required", "provider node is required")
		return
	}
	if err != nil {
		providerNotFound(w, err)
		return
	}
	if body.ProviderID != "" && body.ProviderID != node.ProviderID {
		writeError(w, http.StatusBadRequest, "provider_mismatch", "connection provider must match its node")
		return
	}
	authKind := credentials.AuthKind(body.AuthKind)
	if node.Kind == credentials.NodeGeneric {
		if authKind == "" {
			authKind = credentials.AuthAPIKey
		}
		if authKind != credentials.AuthAPIKey && authKind != credentials.AuthNone {
			writeError(w, http.StatusBadRequest, "invalid_auth_kind", "Generic Provider supports API key or no-auth connections")
			return
		}
	} else {
		spec, _ := h.providerSpec(node.ProviderID)
		if authKind == "" {
			authKind = credentials.AuthKind(spec.Auth)
		}
		if !authKindPermitted(spec, authKind) {
			writeError(w, http.StatusBadRequest, "invalid_auth_kind", "authentication mode is not supported by this provider")
			return
		}
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	priority := 0
	if body.Priority != nil {
		priority = *body.Priority
	}
	proxyPool := ""
	if body.ProxyPool != nil {
		proxyPool = *body.ProxyPool
	}
	connection, _, err := h.publishConnection(r.Context(), credentials.Connection{ID: r.PathValue("id"), NodeID: node.ID, ProviderID: node.ProviderID, Name: body.Name, AuthKind: authKind, Identity: body.Identity, Secret: body.Secret, Enabled: enabled, Priority: priority, ProxyPoolID: proxyPool, AccountID: accountID})
	if err != nil {
		writeError(w, http.StatusBadRequest, "connection_save_failed", "provider connection could not be saved; check its identity, auth mode, and proxy pool")
		return
	}
	writeJSON(w, http.StatusOK, connectionJSON(connection))
}

func (h *Handler) handleDeleteConnection(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "provider credential storage is not configured")
		return
	}
	id := r.PathValue("id")
	err := h.opts.Credentials.DeleteConnection(r.Context(), id)
	if errors.Is(err, credentials.ErrNotFound) {
		writeError(w, http.StatusNotFound, "connection_not_found", "provider connection was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "connection_delete_failed", "provider connection could not be deleted")
		return
	}
	if h.opts.CredentialRegistry != nil {
		h.opts.CredentialRegistry.Forget(id)
	}
	if h.opts.PoolBindings != nil {
		if _, err := h.opts.PoolBindings.RefreshPoolBindings(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "connection_publish_failed", "connection deleted but runtime proxy bindings could not be refreshed")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

func (h *Handler) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ModelID   string `json:"modelId"`
		Transport string `json:"transport"`
	}
	if r.Body != nil && r.ContentLength != 0 && !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	connection, node, err := h.workflowConnection(r.Context(), r.PathValue("id"))
	if err != nil {
		providerNotFound(w, err)
		return
	}
	if !validProbeModel(body.ModelID) || body.Transport != "" && !slicesContain(node.Transports, body.Transport) {
		writeError(w, http.StatusBadRequest, "invalid_probe", "model ID or transport is invalid")
		return
	}
	result := h.validateConnection(r.Context(), connection, node, body.Transport, body.ModelID)
	if !result.OK {
		status := http.StatusBadGateway
		if result.Classification == "slow" || result.Classification == "canceled" {
			status = result.Status
		}
		code := "connection_test_failed"
		if result.Classification == "slow" {
			code = "connection_test_timeout"
		} else if result.Classification == "canceled" {
			code = "connection_test_canceled"
		}
		writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": result.Error, "upstreamStatus": result.UpstreamStatus}, "result": result})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type modelTestResult struct {
	ModelID         string `json:"modelId"`
	UpstreamModelID string `json:"upstreamModelId,omitempty"`
	ServedModelID   string `json:"servedModelId,omitempty"`
	Classification  string `json:"classification,omitempty"`
	LatencyMS       int64  `json:"latencyMs,omitempty"`
	OK              bool   `json:"ok"`
	Status          int    `json:"status,omitempty"`
	UpstreamStatus  int    `json:"upstreamStatus,omitempty"`
	Error           string `json:"error,omitempty"`
}

func (h *Handler) handleTestModels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Transport string   `json:"transport"`
		ModelIDs  []string `json:"modelIds"`
	}
	if !decodeJSON(w, r, 16<<10, &body) {
		return
	}
	if len(body.ModelIDs) == 0 || len(body.ModelIDs) > 20 {
		writeError(w, http.StatusBadRequest, "invalid_models", "select between 1 and 20 models")
		return
	}
	seen := make(map[string]struct{}, len(body.ModelIDs))
	for i := range body.ModelIDs {
		body.ModelIDs[i] = strings.TrimSpace(body.ModelIDs[i])
		if body.ModelIDs[i] == "" || !validProbeModel(body.ModelIDs[i]) {
			writeError(w, http.StatusBadRequest, "invalid_models", "model ids must be non-empty and at most 512 bytes")
			return
		}
		if _, exists := seen[body.ModelIDs[i]]; exists {
			writeError(w, http.StatusBadRequest, "invalid_models", "duplicate model ids are not allowed")
			return
		}
		seen[body.ModelIDs[i]] = struct{}{}
	}
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "provider credential storage is not configured")
		return
	}
	connection, err := h.opts.Credentials.GetConnection(r.Context(), r.PathValue("id"))
	if err != nil {
		providerNotFound(w, err)
		return
	}
	if !connection.Enabled {
		writeError(w, http.StatusConflict, "connection_disabled", "enable the connection before testing models")
		return
	}
	node, err := h.opts.Credentials.GetNode(r.Context(), connection.NodeID)
	if err != nil {
		providerNotFound(w, err)
		return
	}
	transportName := body.Transport
	if transportName == "" && len(node.Transports) > 0 {
		transportName = node.Transports[0]
	}
	if !slicesContain(node.Transports, transportName) {
		writeError(w, http.StatusBadRequest, "unsupported_transport", "selected transport is not enabled on this provider")
		return
	}
	spec, _ := h.providerSpec(node.ProviderID)
	baseURL := node.BaseURL
	if baseURL == "" {
		baseURL = spec.DefaultBaseURL
	}
	ctx, cancel := context.WithTimeout(r.Context(), modelBatchTimeout)
	defer cancel()
	results := make([]modelTestResult, 0, len(body.ModelIDs))
	for _, modelID := range body.ModelIDs {
		if ctx.Err() != nil {
			writeProbeInterrupted(w, ctx, "probe", results)
			return
		}
		result := h.testProviderModel(ctx, connection, node, spec, baseURL, transportName, modelID, modelProbeTimeout)
		results = append(results, result)
		if ctx.Err() != nil {
			writeProbeInterrupted(w, ctx, "probe", results)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"connectionId": connection.ID, "transport": transportName, "results": results})
}

func (h *Handler) testProviderModel(ctx context.Context, connection credentials.Connection, node credentials.Node, spec registry.Spec, baseURL, transportName, modelID string, timeout time.Duration) (result modelTestResult) {
	start := time.Now()
	requestCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result = modelTestResult{ModelID: modelID, Classification: "failed"}
	defer func() {
		result.LatencyMS = time.Since(start).Milliseconds()
		if !result.OK {
			if requestCtx.Err() != nil {
				result.Status, result.Classification, result.Error = http.StatusRequestTimeout, "canceled", "model test was canceled by the enclosing request"
			}
			result.ServedModelID = ""
		}
	}()
	if h.opts.Runtime != nil {
		if snapshot, err := h.opts.Runtime.Load(); err == nil {
			modelID, err = snapshot.ProbeModel(node.ProviderID, modelID)
			if err != nil {
				result.Error = err.Error()
				return result
			}
		}
	}
	if !validProbeModel(modelID) || modelID == "" {
		result.Error = "model ID is invalid"
		return result
	}
	result.UpstreamModelID = modelID
	var endpoint string
	var body any
	switch transportName {
	case string(registry.TransportOpenAIChat):
		endpoint = "chat/completions"
		body = map[string]any{"model": modelID, "messages": []any{map[string]string{"role": "user", "content": "Reply with OK."}}, "max_tokens": modelProbeTokens, "stream": true}
	case string(registry.TransportOpenAIResponses):
		endpoint = "responses"
		body = map[string]any{"model": modelID, "input": "Reply with OK.", "max_output_tokens": modelProbeTokens, "stream": true}
	case string(registry.TransportAnthropic):
		endpoint = "messages"
		body = map[string]any{"model": modelID, "max_tokens": modelProbeTokens, "stream": true, "messages": []any{map[string]string{"role": "user", "content": "Reply with OK."}}}
	case string(registry.TransportGemini):
		endpoint = "models/" + strings.TrimPrefix(modelID, "models/") + ":streamGenerateContent"
		body = map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]string{"text": "Reply with OK."}}}}, "generationConfig": map[string]any{"maxOutputTokens": modelProbeTokens}}
	case string(registry.TransportOllama):
		endpoint = "api/chat"
		body = map[string]any{"model": modelID, "messages": []any{map[string]string{"role": "user", "content": "Reply with OK."}}, "stream": true, "options": map[string]int{"num_predict": modelProbeTokens}}
	case string(registry.TransportSystemOne):
		// System One uses the typed endpoint and state/questions envelope (PRD section 6).
		body = map[string]any{
			"model":  modelID,
			"stream": true,
			"state":  map[string]any{"message": "Routeweft System One probe."},
			"questions": map[string]any{
				"probe": map[string]string{"type": "noul", "instructions": "Reply with OK."},
			},
		}
	default:
		result.Error = "model probe is not implemented for this specialized provider transport"
		return result
	}
	if custom, ok := spec.EndpointFor(registry.Protocol(transportName)); ok {
		endpoint = custom
	}
	if node.Kind == credentials.NodeBuiltin && node.ProviderID == "cloudflare-ai" {
		var valid bool
		endpoint, valid = cloudflare.Endpoint(transportName, connection.AccountID, modelID)
		if !valid {
			result.Error = "Cloudflare account ID or model transport is not configured"
			return result
		}
	}
	if connection.ProviderID == "openai" && transportName == string(registry.TransportOpenAIChat) {
		endpoint = "chat/completions"
	}
	if node.Kind == credentials.NodeGeneric {
		switch transportName {
		case string(registry.TransportOpenAIChat):
			endpoint = "chat/completions"
		case string(registry.TransportOpenAIResponses):
			endpoint = "responses"
		case string(registry.TransportAnthropic):
			endpoint = "messages"
		}
	}
	parsed, err := h.validateOutboundURL(baseURL)
	if err != nil {
		result.Error = "provider base URL is not allowed by outbound network policy"
		return result
	}
	endpoint = nativeEndpointPath(parsed.Path, transportName, endpoint)
	if transportName == string(registry.TransportSystemOne) {
		// Base URL already points at the typed endpoint; only a spec-declared
		// override appends a path, matching ingress dispatch.
		if endpoint != "" {
			parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(endpoint, "/")
		}
		parsed.RawPath = ""
	} else {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(endpoint, "/")
		parsed.RawPath = ""
	}
	if transportName == string(registry.TransportGemini) {
		query := parsed.Query()
		query.Set("alt", "sse")
		parsed.RawQuery = query.Encode()
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		result.Error = "model probe request could not be encoded"
		return result
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(encoded))
	if err != nil {
		result.Error = "model probe request could not be created"
		return result
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream, application/x-ndjson, application/json")
	credential, err := h.probeCredential(ctx, connection)
	if err != nil {
		result.Error = "provider credentials are unavailable"
		return result
	}
	switch connection.AuthKind {
	case credentials.AuthNone:
	case credentials.AuthCookie:
		request.Header.Set("Cookie", credential)
	case credentials.AuthAPIKey, credentials.AuthOAuth:
		for name, values := range modelWireHeaders(node, transportName, credential) {
			request.Header[name] = values
		}
	default:
		result.Error = "provider authentication mode is not supported for model probes"
		return result
	}
	client := http.Client{Timeout: timeout, Transport: h.probeTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if _, native := h.opts.DiscoveryClient.HTTP.(*http.Client); !native {
		client.Transport = roundTripperFromClient(h.opts.DiscoveryClient.HTTP)
	}
	response, err := client.Do(request)
	if err != nil {
		result.probeFailure(ctx, err)
		return result
	}
	defer response.Body.Close()
	result.Status = response.StatusCode
	result.UpstreamStatus = response.StatusCode
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.Error = "upstream rejected the model test"
		return result
	}
	result.OK, err = readProbe(response, transportName, &result.ServedModelID)
	if err != nil {
		result.probeFailure(ctx, err)
	} else if result.OK {
		result.Classification = "reachable"
	} else {
		result.Status = http.StatusBadGateway
		result.Error = "provider returned no usable output for this model"
	}
	return result
}

func roundTripperFromClient(client discovery.HTTPClient) http.RoundTripper {
	if roundTripper, ok := client.(http.RoundTripper); ok {
		return roundTripper
	}
	return discoveryRoundTripper{client: client}
}

type discoveryRoundTripper struct{ client discovery.HTTPClient }

func (d discoveryRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return d.client.Do(request)
}

func (h *Handler) handleDiscoverModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.fetchModels(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadGateway, "model_discovery_failed", err.Error())
		return
	}
	if h.opts.ProviderCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, "catalog_unavailable", "runtime model catalog is not configured")
		return
	}
	items := make([]runtime.Model, 0, len(models))
	providerID := h.connectionProvider(r.Context(), r.PathValue("id"))
	for _, id := range models {
		items = append(items, runtime.Model{ProviderID: providerID, ID: id, Name: id, Source: "discovered"})
	}
	if err := h.opts.ProviderCatalog.ReplaceDiscoveredModels(r.Context(), providerID, items); err != nil {
		writeError(w, http.StatusInternalServerError, "model_discovery_save_failed", "discovered models could not be saved")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providerId": providerID, "models": models, "count": len(models), "activeConfigRevision": h.activeRevision()})
}

func (h *Handler) fetchModels(ctx context.Context, connectionID string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if h.opts.Credentials == nil {
		return nil, errors.New("provider credential storage is not configured")
	}
	connection, err := h.opts.Credentials.GetConnection(ctx, connectionID)
	if err != nil {
		return nil, errors.New("provider connection was not found")
	}
	if !connection.Enabled {
		return nil, errors.New("provider connection is disabled")
	}
	node, err := h.opts.Credentials.GetNode(ctx, connection.NodeID)
	if err != nil {
		return nil, errors.New("provider node was not found")
	}
	return h.fetchConnectionModels(ctx, connection, node)
}

func (h *Handler) fetchConnectionModels(ctx context.Context, connection credentials.Connection, node credentials.Node) ([]string, error) {
	models, err := h.fetchConnectionModelEntries(ctx, connection, node)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids, nil
}

func (h *Handler) fetchConnectionModelEntries(ctx context.Context, connection credentials.Connection, node credentials.Node) ([]discovery.Model, error) {
	baseURL := node.BaseURL
	path := "models"
	if node.Kind == credentials.NodeBuiltin {
		spec, ok := h.providerSpec(node.ProviderID)
		if !ok {
			return nil, errors.New("provider is not in the built-in catalog")
		}
		if baseURL == "" {
			baseURL = spec.DefaultBaseURL
		}
		path = spec.DiscoveryPath
	}
	if slicesContain(node.Transports, string(registry.TransportOllama)) {
		path = "api/tags"
	}
	style := discovery.AuthBearer
	if slicesContain(node.Transports, string(registry.TransportGemini)) {
		style = discovery.AuthGoogleAPIKey
	} else if slicesContain(node.Transports, string(registry.TransportAnthropic)) && !wire.For(modelWireProviderID(node)).BearerMessages {
		style = discovery.AuthXApiKey
	}
	client := discovery.Client{AllowPrivateUpstreams: h.opts.AllowPrivateUpstreams}
	if h.opts.DiscoveryClient != nil {
		client.HTTP = h.opts.DiscoveryClient.HTTP
	}
	credential, err := h.probeCredential(ctx, connection)
	if err != nil {
		return nil, errors.New("provider credentials are unavailable")
	}
	switch connection.AuthKind {
	case credentials.AuthNone:
		style = discovery.AuthNone
	case credentials.AuthCookie:
		style = discovery.AuthCookie
	}
	headers := make(map[string]string)
	protocol := string(registry.TransportOpenAIChat)
	if slicesContain(node.Transports, string(registry.TransportAnthropic)) {
		protocol = string(registry.TransportAnthropic)
	}
	for name, values := range modelWireHeaders(node, protocol, "") {
		headers[name] = values[0]
	}
	return client.ModelEntries(ctx, discovery.Request{ProviderID: node.ProviderID, BaseURL: baseURL, Path: path, Credential: credential, AuthStyle: style, Headers: headers})
}

func modelWireProviderID(node credentials.Node) string {
	if node.Kind == credentials.NodeBuiltin {
		return node.ProviderID
	}
	return ""
}

func modelWireHeaders(node credentials.Node, protocol, credential string) http.Header {
	provider := routing.ProviderRef{ProviderID: modelWireProviderID(node), APIToken: credential}
	if protocol == string(registry.TransportAnthropic) {
		return wire.MessagesHeaders(provider, "", "")
	}
	if protocol == string(registry.TransportGemini) {
		headers := http.Header{}
		if credential != "" {
			headers.Set("x-goog-api-key", credential)
		}
		return headers
	}
	return wire.OpenAIHeaders(provider)
}

func (h *Handler) handlePutModel(w http.ResponseWriter, r *http.Request) {
	if h.opts.ProviderCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, "catalog_unavailable", "runtime model catalog is not configured")
		return
	}
	var body modelRequest
	if !decodeJSON(w, r, 32<<10, &body) {
		return
	}
	body.ProviderID, body.ID = strings.TrimSpace(body.ProviderID), strings.TrimSpace(body.ID)
	if body.ProviderID == "" || body.ID == "" || !validProbeModel(body.ID) || body.Context < 0 || body.Kind != "" && body.Kind != "llm" && body.Kind != "systemone" {
		writeError(w, http.StatusBadRequest, "invalid_model", "provider id and model id are required; context window cannot be negative")
		return
	}
	if h.opts.Runtime != nil && body.Kind != "" {
		snapshot, err := h.opts.Runtime.Load()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "catalog_unavailable", "runtime model catalog is unavailable")
			return
		}
		if snapshot.ModelKind(runtime.Model{ProviderID: body.ProviderID, ID: body.ID, Kind: body.Kind}) != body.Kind {
			writeError(w, http.StatusBadRequest, "intrinsic_model_kind", "model kind is fixed by its provider transport")
			return
		}
	}
	for i, capability := range body.Caps {
		if capability == "imageToText" {
			capability = "vision"
			body.Caps[i] = capability
		}
		_, adapter := capacityAdapterSetting(capability)
		if !adapter && !slicesContain([]string{"reasoning", "webSearch", "webFetch", "imageToText", "tools"}, capability) {
			writeError(w, 400, "invalid_capability", "unsupported model capability")
			return
		}
	}
	if err := h.opts.ProviderCatalog.PutModel(r.Context(), runtime.Model{ProviderID: body.ProviderID, ID: body.ID, Name: strings.TrimSpace(body.Name), ContextWindow: body.Context, Capabilities: body.Caps, Kind: body.Kind, Source: "custom"}); err != nil {
		writeError(w, http.StatusBadRequest, "model_save_failed", "custom model could not be saved")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providerId": body.ProviderID, "id": body.ID, "source": "custom", "kind": body.Kind, "activeConfigRevision": h.activeRevision()})
}

func (h *Handler) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	if h.opts.ProviderCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, "catalog_unavailable", "runtime model catalog is not configured")
		return
	}
	var body struct {
		ProviderID string `json:"providerId"`
		ModelID    string `json:"modelId"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	if body.ProviderID == "" || body.ModelID == "" || !validProbeModel(body.ModelID) {
		writeError(w, http.StatusBadRequest, "invalid_model", "provider id and model id are required")
		return
	}
	err := h.opts.ProviderCatalog.DeleteModel(r.Context(), body.ProviderID, body.ModelID)
	var inUse *runtime.ModelInUseError
	if errors.As(err, &inUse) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "model_in_use", "message": inUse.Error() + "; remove it from them first"}, "combos": inUse.Combos, "adapters": inUse.Adapters})
		return
	}
	if errors.Is(err, runtime.ErrModelNotCustom) {
		writeError(w, http.StatusConflict, "model_not_custom", "model exists but is not a custom model; discovered and built-in models cannot be deleted (disable it instead)")
		return
	}
	if errors.Is(err, runtime.ErrModelNotFound) {
		writeError(w, http.StatusNotFound, "model_not_found", "model was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "model_delete_failed", "custom model could not be deleted")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providerId": body.ProviderID, "modelId": body.ModelID, "deleted": true})
}

func (h *Handler) handleDisableModel(w http.ResponseWriter, r *http.Request) {
	if h.opts.ProviderCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, "catalog_unavailable", "runtime model catalog is not configured")
		return
	}
	var body struct {
		ProviderID string `json:"providerId"`
		ModelID    string `json:"modelId"`
		Disabled   bool   `json:"disabled"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	if body.ProviderID == "" || body.ModelID == "" || !validProbeModel(body.ModelID) {
		writeError(w, http.StatusBadRequest, "invalid_model", "provider id and model id are required")
		return
	}
	if err := h.opts.ProviderCatalog.SetModelDisabled(r.Context(), body.ProviderID, body.ModelID, body.Disabled); err != nil {
		writeError(w, http.StatusBadRequest, "model_update_failed", "model availability could not be updated")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providerId": body.ProviderID, "modelId": body.ModelID, "disabled": body.Disabled})
}

func (h *Handler) handlePutAlias(w http.ResponseWriter, r *http.Request) {
	if h.opts.ProviderCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, "catalog_unavailable", "runtime model catalog is not configured")
		return
	}
	var body struct {
		Alias      string `json:"alias"`
		ProviderID string `json:"providerId"`
		ModelID    string `json:"modelId"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	if strings.TrimSpace(body.Alias) == "" || strings.TrimSpace(body.ProviderID) == "" || strings.TrimSpace(body.ModelID) == "" {
		writeError(w, http.StatusBadRequest, "invalid_alias", "alias and target model are required")
		return
	}
	if err := h.opts.ProviderCatalog.PutAlias(r.Context(), strings.TrimSpace(body.Alias), runtime.ModelRef{ProviderID: body.ProviderID, ModelID: body.ModelID}); err != nil {
		writeError(w, http.StatusBadRequest, "alias_save_failed", "alias target must reference a configured model")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alias": body.Alias, "providerId": body.ProviderID, "modelId": body.ModelID})
}

func (h *Handler) handleDeleteAlias(w http.ResponseWriter, r *http.Request) {
	if h.opts.ProviderCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, "catalog_unavailable", "runtime model catalog is not configured")
		return
	}
	alias := r.PathValue("alias")
	err := h.opts.ProviderCatalog.DeleteAlias(r.Context(), alias)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "alias_not_found", "model alias was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "alias_delete_failed", "model alias could not be deleted")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alias": alias, "deleted": true})
}

func (h *Handler) handlePutPricing(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProviderID string   `json:"providerId"`
		ModelID    string   `json:"modelId"`
		Input      *float64 `json:"inputPerMTok"`
		Output     *float64 `json:"outputPerMTok"`
		CacheRead  *float64 `json:"cacheReadPerMTok"`
		CacheWrite *float64 `json:"cacheWritePerMTok"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	if strings.TrimSpace(body.ProviderID) == "" || strings.TrimSpace(body.ModelID) == "" || body.Input == nil && body.Output == nil && body.CacheRead == nil && body.CacheWrite == nil {
		writeError(w, http.StatusBadRequest, "invalid_pricing", "provider, model, and at least one price are required")
		return
	}
	for _, value := range []*float64{body.Input, body.Output, body.CacheRead, body.CacheWrite} {
		if value != nil && (*value < 0 || *value != *value || *value > 1e9) {
			writeError(w, http.StatusBadRequest, "invalid_pricing", "prices must be finite non-negative numbers")
			return
		}
	}
	_, err := h.opts.DB.ExecContext(r.Context(), `INSERT INTO pricing_overrides(provider_id,model_id,input_per_mtok,output_per_mtok,cache_read_per_mtok,cache_write_per_mtok) VALUES(?,?,?,?,?,?) ON CONFLICT(provider_id,model_id) DO UPDATE SET input_per_mtok=excluded.input_per_mtok,output_per_mtok=excluded.output_per_mtok,cache_read_per_mtok=excluded.cache_read_per_mtok,cache_write_per_mtok=excluded.cache_write_per_mtok,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, body.ProviderID, body.ModelID, body.Input, body.Output, body.CacheRead, body.CacheWrite)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pricing_save_failed", "model pricing could not be saved")
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (h *Handler) handleDeletePricing(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProviderID string `json:"providerId"`
		ModelID    string `json:"modelId"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	if body.ProviderID == "" || body.ModelID == "" {
		writeError(w, http.StatusBadRequest, "invalid_pricing", "provider and model are required")
		return
	}
	result, err := h.opts.DB.ExecContext(r.Context(), "DELETE FROM pricing_overrides WHERE provider_id=? AND model_id=?", body.ProviderID, body.ModelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pricing_delete_failed", "model pricing could not be deleted")
		return
	}
	affected, err := result.RowsAffected()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pricing_delete_failed", "model pricing could not be deleted")
		return
	}
	if affected == 0 {
		writeError(w, http.StatusNotFound, "pricing_not_found", "model pricing was not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providerId": body.ProviderID, "modelId": body.ModelID, "deleted": true})
}

func (h *Handler) handleSetConnectionProxy(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "provider credential storage is not configured")
		return
	}
	var body struct {
		ProxyPoolID string `json:"proxyPoolId"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	err := h.opts.Credentials.SetConnectionProxy(r.Context(), r.PathValue("id"), body.ProxyPoolID)
	if errors.Is(err, credentials.ErrNotFound) {
		writeError(w, http.StatusNotFound, "connection_not_found", "provider connection was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "proxy_assignment_failed", "proxy pool assignment could not be saved")
		return
	}
	if h.opts.PoolBindings != nil {
		if _, err := h.opts.PoolBindings.RefreshPoolBindings(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "proxy_publish_failed", "proxy assignment saved but runtime publication failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "proxyPoolId": body.ProxyPoolID})
}

func (h *Handler) handleMoveConnection(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "provider credential storage is not configured")
		return
	}
	var body struct {
		Direction string `json:"direction"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	direction := 0
	switch body.Direction {
	case "up":
		direction = -1
	case "down":
		direction = 1
	}
	if direction == 0 {
		writeError(w, http.StatusBadRequest, "invalid_direction", "direction must be up or down")
		return
	}
	err := h.opts.Credentials.MoveConnection(r.Context(), r.PathValue("id"), direction)
	if errors.Is(err, credentials.ErrNotFound) {
		writeError(w, http.StatusNotFound, "connection_not_found", "provider connection was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "connection_reorder_failed", "provider connection order could not be updated")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "direction": body.Direction})
}

func (h *Handler) validateOutboundURL(raw string) (*url.URL, error) {
	if h.opts.AllowPrivateUpstreams {
		return transport.ValidateTrustedLocalURL(raw)
	}
	return transport.ValidatePublicURL(raw)
}

func (h *Handler) providerSpec(id string) (registry.Spec, bool) {
	for _, spec := range h.opts.Providers {
		if spec.ID == id {
			return spec, true
		}
	}
	return registry.Spec{}, false
}

var errUnknownProvider = errors.New("provider is not in the built-in catalog")

// builtinNode returns the built-in provider's node, creating it from the
// catalog entry on first use.
func (h *Handler) builtinNode(ctx context.Context, providerID string) (credentials.Node, error) {
	for _, candidate := range h.nodes(ctx) {
		if candidate.Kind == credentials.NodeBuiltin && candidate.ProviderID == providerID {
			return candidate, nil
		}
	}
	spec, ok := h.providerSpec(providerID)
	if !ok {
		return credentials.Node{}, errUnknownProvider
	}
	var transports []string
	for _, value := range spec.Transports {
		transports = append(transports, string(value))
	}
	return h.opts.Credentials.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: spec.ID, Name: spec.ID, BaseURL: spec.DefaultBaseURL, Transports: transports})
}

func (h *Handler) nodes(ctx context.Context) []credentials.Node {
	if h.opts.Credentials == nil {
		return nil
	}
	nodes, _ := h.opts.Credentials.ListNodes(ctx)
	return nodes
}

func (h *Handler) connectionProvider(ctx context.Context, id string) string {
	if h.opts.Credentials == nil {
		return ""
	}
	connection, err := h.opts.Credentials.GetConnection(ctx, id)
	if err != nil {
		return ""
	}
	return connection.ProviderID
}

func providerNotFound(w http.ResponseWriter, err error) {
	if errors.Is(err, credentials.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "provider_not_found", "provider node was not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "provider_read_failed", "provider node could not be loaded")
}

func nodeJSON(node credentials.Node) map[string]any {
	return map[string]any{"id": node.ID, "kind": node.Kind, "providerId": node.ProviderID, "name": node.Name, "prefix": node.Prefix, "baseUrl": safeURL(node.BaseURL), "transports": node.Transports}
}

func connectionJSON(connection credentials.Connection) map[string]any {
	return map[string]any{"id": connection.ID, "nodeId": connection.NodeID, "providerId": connection.ProviderID, "name": connection.Name, "authKind": connection.AuthKind, "identity": connection.Identity, "enabled": connection.Enabled, "priority": connection.Priority, "proxyPoolId": connection.ProxyPoolID, "accountId": connection.AccountID, "credentialConfigured": !connection.Secret.Empty()}
}

func slicesContain(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

// authKindPermitted reports whether a provider accepts one credential mode. It is
// the single rule for every path that stores a connection: the manual form, an
// onboarding flow's result, and the Generic Provider restriction. Duplicating it is
// how the import path wrote a cookie connection for iflow that the PATCH path then
// refused as invalid_auth_kind.
func authKindPermitted(spec registry.Spec, kind credentials.AuthKind) bool {
	allowed := spec.AuthModes
	if len(allowed) == 0 {
		allowed = []registry.AuthKind{spec.Auth}
	}
	for _, item := range allowed {
		if string(item) == string(kind) {
			return true
		}
	}
	return false
}
