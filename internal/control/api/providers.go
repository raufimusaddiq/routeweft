package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/cloudflare"
	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/proxy"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/telemetry"
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
	// How a connection was signed in is recorded by onboarding only; the form can
	// neither set it nor clear it (mergeConnectionSecret keeps the stored one).
	body.Secret.AuthType = ""
	// The Codex identity check below and the save it allows are one step, as are
	// an edit's read of the stored row and its write (connectionMu).
	h.connectionMu.Lock()
	defer h.connectionMu.Unlock()
	oldIdentity := ""
	if r.Method == http.MethodPatch {
		old, err := h.opts.Credentials.GetConnection(r.Context(), r.PathValue("id"))
		if err != nil {
			providerNotFound(w, err)
			return
		}
		oldIdentity = old.Identity
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
		// Blank keeps the stored identity: an edit cannot clear one, now that the
		// store accepts a connection without.
		if strings.TrimSpace(body.Identity) == "" {
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
		body.Secret = relinkedSecret(old.Secret, body.Secret)
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
	// The manual form always names its account. Only a connection onboarded
	// without one (a Codex credential that names no email) has none, and an edit
	// that sends no identity keeps it that way rather than inventing one.
	if r.Method != http.MethodPatch && strings.TrimSpace(body.Identity) == "" {
		writeError(w, http.StatusBadRequest, "connection_save_failed", "provider connection could not be saved; check its identity, auth mode, and proxy pool")
		return
	}
	// v12 lets the built-in Codex node hold one identity twice for onboarding
	// (#241); a manual create, or an edit that changes the identity, still may not
	// take one another Codex connection holds.
	if node.Kind == credentials.NodeBuiltin && node.ProviderID == codexProviderID && (r.Method != http.MethodPatch || body.Identity != oldIdentity) {
		taken, err := h.codexIdentityTaken(r.Context(), node.ID, r.PathValue("id"), body.Identity)
		if err != nil || taken {
			writeError(w, http.StatusBadRequest, "connection_save_failed", "provider connection could not be saved; check its identity, auth mode, and proxy pool")
			return
		}
	}
	// A credential typed into the form is new to every provider: a create, or an
	// edit that replaces the credential (a relink), is saved untested, in the
	// same write as the credential. A relink resets even when the operator typed
	// the stored key again, so the dashboard never shows the previous result
	// beside "Credential replaced". An edit of name, order, proxy or state keeps
	// the stored result (#220).
	var lifecycle credentials.Lifecycle
	if r.Method != http.MethodPatch || replacesCredential(body.Secret) {
		lifecycle = credentials.Lifecycle{Status: credentials.StatusUntested, Origin: credentials.OriginManual, Reset: true}
	}
	connection, _, err := h.publishConnection(r.Context(), credentials.Connection{ID: r.PathValue("id"), NodeID: node.ID, ProviderID: node.ProviderID, Name: body.Name, AuthKind: authKind, Identity: body.Identity, Secret: body.Secret, Enabled: enabled, Priority: priority, ProxyPoolID: proxyPool, AccountID: accountID, Lifecycle: lifecycle})
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
	h.connectionMu.Lock()
	err := h.opts.Credentials.DeleteConnection(r.Context(), id)
	h.connectionMu.Unlock()
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
	if h.opts.Runtime != nil {
		h.opts.Runtime.State().ForgetAccount(id)
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
	h.recordValidation(r.Context(), connection, &result)
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
	ctx, cancel := context.WithTimeout(r.Context(), modelBatchTimeout)
	defer cancel()
	results := make([]modelTestResult, 0, len(body.ModelIDs))
	for _, modelID := range body.ModelIDs {
		if ctx.Err() != nil {
			writeProbeInterrupted(w, ctx, "probe", results)
			return
		}
		result := h.testProviderModel(ctx, connection, node, spec, transportName, modelID, modelProbeTimeout)
		results = append(results, result)
		if ctx.Err() != nil {
			writeProbeInterrupted(w, ctx, "probe", results)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"connectionId": connection.ID, "transport": transportName, "results": results})
}

func (h *Handler) testProviderModel(ctx context.Context, connection credentials.Connection, node credentials.Node, spec registry.Spec, transportName, modelID string, timeout time.Duration) (result modelTestResult) {
	ctx = transport.WithConnection(ctx, connection.ID)
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
	if forced := wire.ForcedTransport(node.ProviderID, modelID); forced != "" {
		if !slicesContain(node.Transports, forced) {
			result.Error = "model requires the Responses transport"
			return result
		}
		transportName = forced
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
	// The credential is loaded first: a target can depend on it, as serving's
	// does (Vertex's project comes from a service account or ADC JSON, and a
	// partner raw key's is probed for), so a probe resolves what a request would.
	credential, err := h.probeCredential(ctx, connection)
	if err != nil {
		result.Error = "provider credentials are unavailable"
		return result
	}
	// Probe the first serving target through the shared URL rules.
	// Probes do not exercise serving's response-driven Kiro surface fallback.
	route := probeRef(node, connection, spec, transportName, modelID, "")
	if connection.AuthKind == credentials.AuthAPIKey || connection.AuthKind == credentials.AuthOAuth {
		route.APIToken = credential
	}
	target, err := wire.ResolveTarget(route, nativeEndpointPath(route.BaseURL, transportName, endpoint), 0)
	if err != nil {
		var refused *wire.TargetError
		if errors.As(err, &refused) {
			result.Error = refused.Error()
		} else {
			result.Error = "provider endpoint is not configured for this connection"
		}
		return result
	}
	parsed, err := target.URL(h.validateOutboundURL)
	if err != nil {
		result.Error = "provider base URL is not allowed by outbound network policy"
		return result
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
	// Apply serving's body quirks to the probe too (github, kimchi, deepseek, minimax...).
	encoded = wire.FinalizeBodyAt(route, target.Endpoint, encoded)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(encoded))
	if err != nil {
		result.Error = "model probe request could not be created"
		return result
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream, application/x-ndjson, application/json")
	switch connection.AuthKind {
	case credentials.AuthNone:
		// A provider with no account still has the headers serving sends (OpenCode
		// Zen's public key), so the probe tests the same request.
		for name, values := range modelWireHeaders(route) {
			request.Header[name] = values
		}
	case credentials.AuthCookie:
		request.Header.Set("Cookie", credential)
	case credentials.AuthAPIKey, credentials.AuthOAuth:
		for name, values := range modelWireHeaders(route) {
			request.Header[name] = values
		}
	default:
		result.Error = "provider authentication mode is not supported for model probes"
		return result
	}
	if err := wire.ApplyRequestHeaders(route, request, encoded); err != nil {
		result.Error = err.Error()
		return result
	}
	client := http.Client{Timeout: timeout, Transport: h.probeTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if _, native := h.opts.DiscoveryClient.HTTP.(*http.Client); !native {
		client.Transport = roundTripperFromClient(h.opts.DiscoveryClient.HTTP)
	}
	// A provider whose credential is exchanged for the request's (Zed, MiMo Free)
	// is probed through that exchange; the others keep the plain request they had.
	var response *http.Response
	if wire.Exchanges(route.ProviderID) {
		response, err = wire.Send(route, &client, request)
	} else {
		response, err = client.Do(request)
	}
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
	// A provider can answer an account failure as a 2xx whose text the client is
	// shown as it is (Zed's failed status). That is not a reachable model: the
	// failure is the result, and the account is cooled as serving would cool it.
	if failure, failed := wire.AcceptedFailure(route, response); failed {
		now := time.Now()
		if h.opts.Runtime != nil {
			if deadline := routing.CooldownDeadline(failure.Classification, now, routing.DefaultFallbackBackoff); !deadline.IsZero() {
				h.opts.Runtime.State().Cooldown(connection.ID, deadline)
			}
		}
		result.OK, result.ServedModelID = false, ""
		result.Status, result.Classification = http.StatusBadGateway, "failed"
		result.Error = "upstream reported a failure: " + probeErrorText(failure.Message)
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
	entries, err := h.fetchModelEntries(r.Context(), r.PathValue("id"))
	if err != nil {
		var tracked *wire.ListingError
		if errors.As(err, &tracked) && tracked.Unsupported {
			writeError(w, http.StatusNotImplemented, "model_discovery_unsupported", err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "model_discovery_failed", err.Error())
		return
	}
	if h.opts.ProviderCatalog == nil {
		writeError(w, http.StatusServiceUnavailable, "catalog_unavailable", "runtime model catalog is not configured")
		return
	}
	items := make([]runtime.Model, 0, len(entries))
	models := make([]string, 0, len(entries))
	providerID := h.connectionProvider(r.Context(), r.PathValue("id"))
	for _, entry := range entries {
		// The listing's own names, windows and capabilities are kept: Kiro's
		// variants and Codex's review twins are named by the listing, not the id.
		name := entry.Name
		if name == "" {
			name = entry.ID
		}
		items = append(items, runtime.Model{ProviderID: providerID, ID: entry.ID, Name: name, ContextWindow: entry.ContextWindow, Capabilities: entry.Capabilities, Source: "discovered"})
		models = append(models, entry.ID)
	}
	if err := h.opts.ProviderCatalog.ReplaceDiscoveredModels(r.Context(), providerID, items); err != nil {
		writeError(w, http.StatusInternalServerError, "model_discovery_save_failed", "discovered models could not be saved")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providerId": providerID, "models": models, "count": len(models), "activeConfigRevision": h.activeRevision()})
}

func (h *Handler) fetchModelEntries(ctx context.Context, connectionID string) ([]discovery.Model, error) {
	// Listing leaves through the connection's proxy (models/route.js:463).
	ctx = transport.WithConnection(ctx, connectionID)
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
	return h.fetchConnectionModelEntries(ctx, connection, node)
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
	var spec registry.Spec
	path := "models"
	if node.Kind == credentials.NodeBuiltin {
		var ok bool
		spec, ok = h.providerSpec(node.ProviderID)
		if !ok {
			return nil, errors.New("provider is not in the built-in catalog")
		}
		path = spec.DiscoveryPath
	}
	if slicesContain(node.Transports, string(registry.TransportOllama)) {
		path = "api/tags"
	}
	protocol := string(registry.TransportOpenAIChat)
	if slicesContain(node.Transports, string(registry.TransportAnthropic)) {
		protocol = string(registry.TransportAnthropic)
	}
	// The listing is requested from the base serving would use (wire.NodeBase,
	// then the provider's own host such as a Token Plan region). A provider with
	// no model listing (Azure) lists nothing: no credential is read and nothing is
	// sent.
	route := probeRef(node, connection, spec, protocol, "", "")
	listBase, listable := wire.DiscoveryBase(route)
	if !listable {
		return nil, nil
	}
	style := discovery.AuthBearer
	if slicesContain(node.Transports, string(registry.TransportGemini)) {
		style = discovery.AuthGoogleAPIKey
	} else if slicesContain(node.Transports, string(registry.TransportAnthropic)) && !wire.For(modelWireProviderID(node)).BearerMessages {
		style = discovery.AuthXApiKey
		if node.Kind != credentials.NodeBuiltin {
			// An Anthropic-compatible node is listed from its base without a trailing
			// /messages, with the key as x-api-key and as a bearer (route.js:554-574).
			style = discovery.AuthAnthropicCompat
			listBase = strings.TrimSuffix(strings.TrimRight(listBase, "/"), "/messages")
		}
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
	for name, values := range modelWireHeaders(route) {
		headers[name] = values[0]
	}
	// Where the provider lists differently from a bearer GET {base}/models (a
	// POST, another host, a gate query, a signature, a filtered shape) the listing
	// is the provider's own. Its query and body are its constants and the
	// connection's settings, never the admin request's.
	listingRoute := route
	listingRoute.APIToken = credential
	listing, own, err := wire.ListingFor(listingRoute)
	if err != nil {
		return nil, err
	}
	if own && listing.Path != "" {
		path = listing.Path
	}
	request := discovery.Request{ProviderID: node.ProviderID, BaseURL: listBase, Path: path, Credential: credential, AuthStyle: style, Headers: headers}
	if own {
		request.Method, request.URL, request.Query, request.Body = listing.Method, listing.URL, listing.Query, listing.Body
		request.Override, request.Shape = listing.Override, listing.Shape
		for name, value := range listing.Headers {
			request.Headers[name] = value
		}
		if listing.OwnAuth {
			request.AuthStyle = discovery.AuthNone
		}
		if listing.Signed {
			sign := wire.For(node.ProviderID).RequestHeaders
			if sign == nil {
				return nil, errors.New("provider listing is signed but its wire has no signer")
			}
			request.Sign = func(rawURL string, body []byte) (map[string]string, error) {
				return sign(listingRoute, body, rawURL)
			}
		}
	}
	return client.ModelEntries(ctx, request)
}

func modelWireProviderID(node credentials.Node) string {
	if node.Kind == credentials.NodeBuiltin {
		return node.ProviderID
	}
	return ""
}

// probeRef is the route a probe or listing for this connection takes: the
// ProviderRef serving would build, with the connection's own settings, so the
// wire hooks (headers, target, base) see what they see at request time.
func probeRef(node credentials.Node, connection credentials.Connection, spec registry.Spec, protocol, model, credential string) routing.ProviderRef {
	return routing.ProviderRef{
		ProviderID: modelWireProviderID(node), Protocol: protocol, UpstreamModel: model, APIToken: credential, ConnectionID: connection.ID,
		BaseURL: wire.NodeBase(node.BaseURL, spec.DefaultBaseURL, connection.Settings),
		Connection: routing.ConnectionMetadata{Name: connection.Name, AuthKind: string(connection.AuthKind), Identity: connection.Identity,
			AccountID: connection.AccountID, Settings: connection.Settings},
	}
}

func modelWireHeaders(provider routing.ProviderRef) http.Header {
	credential := provider.APIToken
	if provider.Protocol == string(registry.TransportAnthropic) {
		return wire.MessagesHeaders(provider, "", "")
	}
	if provider.Protocol == string(registry.TransportGemini) {
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
	var body map[string]json.RawMessage
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	poolRaw, hasPool := body["proxyPoolId"]
	var poolID string
	if hasPool && string(poolRaw) != "null" && json.Unmarshal(poolRaw, &poolID) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "proxyPoolId must be a string")
		return
	}
	legacy, hasLegacy := connectionProxyUpdate(body)
	if !hasLegacy {
		// A body without the per-connection fields is the pool assignment it has
		// always been: a missing proxyPoolId unbinds.
		hasPool = true
	}
	if hasLegacy && legacy.Enabled {
		if legacy.URL == "" {
			writeError(w, http.StatusBadRequest, "invalid_connection_proxy", "Connection proxy URL is required when connection proxy is enabled")
			return
		}
		normalized, err := proxy.ValidateURL(runtime.LegacyProxyURL(legacy.URL))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_connection_proxy", err.Error())
			return
		}
		if err := h.checkProxyHost(normalized); err != nil {
			writeError(w, http.StatusBadRequest, "proxy_host_blocked", "proxy host is not allowed by the outbound network policy")
			return
		}
	}
	id := r.PathValue("id")
	h.connectionMu.Lock()
	var err error
	if !hasLegacy {
		err = h.opts.Credentials.SetConnectionProxy(r.Context(), id, poolID)
	} else {
		// The binding and the connection's own proxy are one write, as the pin's
		// route makes them (providers/[id]/route.js:135-158): one transaction,
		// one published snapshot, so no request sees one without the other and a
		// failed write (an unknown pool fails its foreign key) changes neither.
		_, err = h.updateConnection(r.Context(), id, func(existing credentials.Connection) (credentials.Connection, error) {
			if hasPool {
				existing.ProxyPoolID = poolID
			}
			data := make(map[string]string, len(existing.Secret.ProviderData)+3)
			for key, value := range existing.Secret.ProviderData {
				data[key] = value
			}
			data["connectionProxyEnabled"] = strconv.FormatBool(legacy.Enabled)
			data["connectionProxyUrl"] = legacy.URL
			data["connectionNoProxy"] = legacy.NoProxy
			existing.Secret.ProviderData = data
			return existing, nil
		})
	}
	h.connectionMu.Unlock()
	if errors.Is(err, credentials.ErrNotFound) {
		writeError(w, http.StatusNotFound, "connection_not_found", "provider connection was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "proxy_assignment_failed", "proxy assignment could not be saved")
		return
	}
	// A store with a publisher has published the write, bindings included
	// (runtime.Manager.UpdateProviders); only a standalone store needs the
	// bindings republished.
	if h.opts.PoolBindings != nil && !h.opts.Credentials.Publishes() {
		if _, err := h.opts.PoolBindings.RefreshPoolBindings(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "proxy_publish_failed", "proxy assignment saved but runtime publication failed")
			return
		}
	}
	response := map[string]any{"id": id}
	if hasPool {
		response["proxyPoolId"] = poolID
	}
	if hasLegacy {
		response["connectionProxyEnabled"] = legacy.Enabled
		response["connectionProxyUrl"] = safeURL(legacy.URL)
		response["connectionNoProxy"] = legacy.NoProxy
	}
	writeJSON(w, http.StatusOK, response)
}

// connectionProxySettings is a LiteRouter per-connection proxy as the pin's
// provider routes accept it.
type connectionProxySettings struct {
	Enabled      bool
	URL, NoProxy string
}

// connectionProxyUpdate reads the per-connection proxy fields the way the pin's
// normalizeProxyConfig does (src/app/api/providers/[id]/route.js:9-34): any of
// the three keys sets all three, only a JSON true enables, and a non-string
// URL or no-proxy list reads as empty.
func connectionProxyUpdate(body map[string]json.RawMessage) (connectionProxySettings, bool) {
	enabledRaw, hasEnabled := body["connectionProxyEnabled"]
	urlRaw, hasURL := body["connectionProxyUrl"]
	noProxyRaw, hasNoProxy := body["connectionNoProxy"]
	if !hasEnabled && !hasURL && !hasNoProxy {
		return connectionProxySettings{}, false
	}
	text := func(raw json.RawMessage) string {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return ""
		}
		return strings.TrimSpace(value)
	}
	return connectionProxySettings{
		Enabled: strings.TrimSpace(string(enabledRaw)) == "true",
		URL:     text(urlRaw),
		NoProxy: text(noProxyRaw),
	}, true
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
	h.connectionMu.Lock()
	err := h.opts.Credentials.MoveConnection(r.Context(), r.PathValue("id"), direction)
	h.connectionMu.Unlock()
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
	view := map[string]any{"id": connection.ID, "nodeId": connection.NodeID, "providerId": connection.ProviderID, "name": connection.Name, "authKind": connection.AuthKind, "identity": connection.Identity, "enabled": connection.Enabled, "priority": connection.Priority, "proxyPoolId": connection.ProxyPoolID, "accountId": connection.AccountID, "credentialConfigured": !connection.Secret.Empty()}
	// The method that made the credential (Kiro's google, github, builder-id, …)
	// is a label, not a secret. The dashboard offers Reauthenticate only where the
	// browser sign-in can renew that method (#219).
	if method := strings.TrimSpace(connection.Secret.ProviderData["authMethod"]); method != "" {
		view["signInMethod"] = method
	}
	return view
}

// relinkedSecret makes a relink replace the credential fields it names and only
// those. The edit merge takes an update's token fields whole once it carries any
// credential (mergeConnectionSecret), so without this a cookie-only relink of a
// connection that also holds an API key (iflow keeps the key minted from its
// cookie as the access token, onboarding/manual.go:251-260) would drop the key
// and break the connection, an access-token relink would drop a stored refresh
// token, ID token and cookie, and an ID-token edit would drop the access token.
// Here every stored access, refresh and ID token and cookie the edit does not
// name is kept, and the stored expiry with the access token it belongs to.
//
// An access-token relink also replaces every stored copy of the old key.
// Onboarding keeps the key twice for some providers, as the access token and as
// ProviderData["apiKey"] (kimi, onboarding/providers.go:98-101; iflow,
// manual.go:251,260; xiaomi-mimo, manual.go:342), and usage readers prefer the
// provider-data copy (usage/oauth.go:21-26), so the relink names apiKey, and
// any other credential entry that held the old access token, with the new key.
// Other entries (a Copilot or session token minted separately, metadata) are
// kept by the merge.
func relinkedSecret(stored, edit credentials.Secret) credentials.Secret {
	if !replacesCredential(edit) {
		return edit
	}
	key := strings.TrimSpace(edit.AccessToken)
	if key == "" {
		edit.AccessToken = stored.AccessToken
		if edit.Expiry.IsZero() {
			edit.Expiry = stored.Expiry
		}
	}
	if strings.TrimSpace(edit.RefreshToken) == "" {
		edit.RefreshToken = stored.RefreshToken
	}
	if strings.TrimSpace(edit.IDToken) == "" {
		edit.IDToken = stored.IDToken
	}
	if strings.TrimSpace(edit.Cookie) == "" {
		edit.Cookie = stored.Cookie
	}
	if key == "" {
		return edit
	}
	for name, value := range stored.ProviderData {
		if _, named := edit.ProviderData[name]; named || value == "" {
			continue
		}
		if name == "apiKey" || value == stored.AccessToken && telemetry.SensitiveKey(name) {
			if edit.ProviderData == nil {
				edit.ProviderData = map[string]string{}
			}
			edit.ProviderData[name] = key
		}
	}
	return edit
}

// replacesCredential reports whether a form edit carries credential material,
// which replaces the stored credential (mergeConnectionSecret keeps the stored
// one for an edit that carries none).
func replacesCredential(secret credentials.Secret) bool {
	return !secret.Empty() || secret.IDToken != ""
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

// probeErrorText bounds and cleans a message an upstream wrote before it is shown
// as a probe result.
func probeErrorText(message string) string {
	message = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, message)
	if runes := []rune(message); len(runes) > 200 {
		message = string(runes[:200]) + "…"
	}
	return strings.TrimSpace(message)
}
