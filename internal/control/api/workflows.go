package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

const probeTimeout = 20 * time.Second

func (h *Handler) probeSecret(ctx context.Context, c credentials.Connection) (credentials.Secret, error) {
	if h.opts.CredentialRegistry != nil && c.ID != "" {
		secret, err := h.opts.CredentialRegistry.Secret(ctx, c.ID)
		if !errors.Is(err, credentials.ErrNotFound) {
			return secret, err
		}
	}
	return c.Secret, nil
}

type validationResult struct {
	ConnectionID    string `json:"connectionId,omitempty"`
	ProviderID      string `json:"providerId"`
	OK              bool   `json:"ok"`
	Method          string `json:"method"`
	ModelsAvailable int    `json:"modelsAvailable"`
	ModelID         string `json:"modelId,omitempty"`
	Status          int    `json:"status,omitempty"`
	Error           string `json:"error,omitempty"`
}

func (h *Handler) activeRevision() uint64 {
	if h.opts.Runtime != nil {
		if snapshot, err := h.opts.Runtime.Load(); err == nil {
			return snapshot.ConfigRevision()
		}
	}
	return 0
}

func validProbeModel(id string) bool {
	if len(id) > 512 || strings.ContainsAny(id, "\r\n\x00?#\\%") || strings.Contains(id, "://") {
		return false
	}
	for _, segment := range strings.Split(id, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func (h *Handler) workflowConnection(ctx context.Context, id string) (credentials.Connection, credentials.Node, error) {
	if h.opts.Credentials == nil {
		return credentials.Connection{}, credentials.Node{}, errors.New("credentials unavailable")
	}
	c, err := h.opts.Credentials.GetConnection(ctx, id)
	if err != nil {
		return c, credentials.Node{}, err
	}
	n, err := h.opts.Credentials.GetNode(ctx, c.NodeID)
	return c, n, err
}

func (h *Handler) validateConnection(ctx context.Context, c credentials.Connection, node credentials.Node, protocol, model string) validationResult {
	result := validationResult{ConnectionID: c.ID, ProviderID: node.ProviderID, Method: "inference"}
	if !c.Enabled {
		result.Error = "enable the connection before validation"
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	models, err := h.fetchConnectionModels(ctx, c, node)
	result.ModelsAvailable = len(models)
	var status *discovery.StatusError
	if errors.As(err, &status) && (status.Status == 401 || status.Status == 403) {
		result.Status, result.Error = status.Status, "provider rejected credentials"
		return result
	}
	model = strings.TrimSpace(model)
	if model == "" && len(models) > 0 {
		model = models[0]
	}
	if model == "" && h.opts.Runtime != nil {
		if snapshot, loadErr := h.opts.Runtime.Load(); loadErr == nil {
			for _, known := range snapshot.Models() {
				if known.ProviderID == node.ProviderID && known.Source != "alias" {
					model = known.ID
					break
				}
			}
			if model == "" && node.Kind == credentials.NodeBuiltin {
				if spec, ok := h.providerSpec(node.ProviderID); ok && len(spec.StaticModels) > 0 {
					model = spec.StaticModels[0]
				}
			}
		}
	}
	if model == "" {
		result.Error = "model listing unavailable or empty; supply a manual modelId for inference validation"
		return result
	}
	if !validProbeModel(model) {
		result.Error = "catalog returned an unsafe model ID; supply a manual modelId"
		return result
	}
	if protocol == "" && len(node.Transports) > 0 {
		protocol = node.Transports[0]
	}
	spec, _ := h.providerSpec(node.ProviderID)
	base := node.BaseURL
	if base == "" {
		base = spec.DefaultBaseURL
	}
	probe := h.testProviderModel(ctx, c, node, spec, base, protocol, model, probeTimeout)
	result.OK, result.Status, result.Error, result.ModelID = probe.OK, probe.Status, probe.Error, model
	return result
}

func (h *Handler) handleValidateNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		nodeRequest
		Secret   credentials.Secret   `json:"secret"`
		AuthKind credentials.AuthKind `json:"authKind"`
		ModelID  string               `json:"modelId"`
	}
	if !decodeJSON(w, r, 32<<10, &body) {
		return
	}
	if _, err := h.validateOutboundURL(body.BaseURL); err != nil {
		writeError(w, 400, "invalid_base_url", "provider URL is not allowed by outbound network policy")
		return
	}
	if !validProbeModel(body.ModelID) || len(body.Transports) != 1 {
		writeError(w, 400, "invalid_probe", "select one native transport and a valid model ID")
		return
	}
	switch registry.Protocol(body.Transports[0]) {
	case registry.TransportOpenAIChat, registry.TransportOpenAIResponses, registry.TransportAnthropic, registry.TransportSystemOne:
	default:
		writeError(w, 400, "invalid_transport", "unsupported validation transport")
		return
	}
	if body.AuthKind == "" {
		body.AuthKind = credentials.AuthAPIKey
	}
	if body.AuthKind != credentials.AuthAPIKey && body.AuthKind != credentials.AuthNone || body.AuthKind == credentials.AuthAPIKey && body.Secret.AccessToken == "" {
		writeError(w, 400, "invalid_credentials", "supply an API key or explicitly select no-auth")
		return
	}
	node := credentials.Node{Kind: credentials.NodeGeneric, ProviderID: "validation", BaseURL: body.BaseURL, Transports: body.Transports}
	c := credentials.Connection{Enabled: true, AuthKind: body.AuthKind, Secret: body.Secret}
	writeJSON(w, 200, h.validateConnection(r.Context(), c, node, body.Transports[0], body.ModelID))
}

func (h *Handler) workflowIDs(ctx context.Context) ([]string, error) {
	if h.opts.DB == nil || h.opts.Credentials == nil {
		return nil, errors.New("credentials unavailable")
	}
	rows, err := h.opts.DB.QueryContext(ctx, "SELECT id FROM provider_connections WHERE enabled=1 ORDER BY priority,name,id LIMIT 201")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if len(ids) > 200 {
		return nil, errors.New("workflow supports at most 200 enabled connections; disable or scope connections first")
	}
	return ids, rows.Err()
}

func batchMatch(mode, provider string, c credentials.Connection, node credentials.Node) bool {
	switch mode {
	case "all":
		return true
	case "provider":
		return c.ProviderID == provider
	case "compatible":
		return node.Kind == credentials.NodeGeneric
	case "oauth":
		return c.AuthKind == credentials.AuthOAuth
	case "free":
		return c.AuthKind == credentials.AuthNone
	case "systemone":
		return slicesContain(node.Transports, string(registry.TransportSystemOne))
	case "apikey":
		return c.AuthKind == credentials.AuthAPIKey && !slicesContain(node.Transports, string(registry.TransportSystemOne))
	default:
		return false
	}
}

func (h *Handler) handleTestBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode       string `json:"mode"`
		ProviderID string `json:"providerId"`
		ModelID    string `json:"modelId"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	if !slicesContain([]string{"all", "provider", "compatible", "oauth", "free", "systemone", "apikey"}, body.Mode) || body.Mode == "provider" && body.ProviderID == "" || !validProbeModel(body.ModelID) {
		writeError(w, 400, "invalid_batch", "select a supported mode and providerId for provider mode")
		return
	}
	if h.opts.DB == nil || h.opts.Credentials == nil {
		writeError(w, 503, "batch_unavailable", "connection storage unavailable")
		return
	}
	config, err := credentials.LoadConfiguration(r.Context(), h.opts.DB)
	if err != nil {
		writeError(w, 503, "batch_unavailable", "enabled connections could not be loaded (maximum 200)")
		return
	}
	nodes := map[string]credentials.Node{}
	for _, node := range config.Nodes {
		nodes[node.ID] = node
	}
	ids := []string{}
	for _, c := range config.Connections {
		if c.Enabled && batchMatch(body.Mode, body.ProviderID, c, nodes[c.NodeID]) {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) > 200 {
		writeError(w, 400, "batch_too_large", "select at most 200 connections using a narrower mode")
		return
	}
	results := []validationResult{}
	passed := 0
	for _, id := range ids {
		if r.Context().Err() != nil {
			return
		}
		c, node, err := h.workflowConnection(r.Context(), id)
		if err != nil {
			results = append(results, validationResult{ConnectionID: id, Error: "connection credentials unavailable"})
			continue
		}
		result := h.validateConnection(r.Context(), c, node, "", body.ModelID)
		results = append(results, result)
		if result.OK {
			passed++
		}
	}
	writeJSON(w, 200, map[string]any{"results": results, "summary": map[string]int{"total": len(results), "passed": passed, "failed": len(results) - passed}})
}

func (h *Handler) handleAvailability(w http.ResponseWriter, r *http.Request) {
	if h.opts.Runtime == nil {
		writeError(w, 503, "runtime_unavailable", "routing state unavailable")
		return
	}
	var body struct {
		Action   string `json:"action"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if r.Method == http.MethodPost {
		if !decodeJSON(w, r, 8<<10, &body) {
			return
		}
		if body.Action != "clearCooldown" || body.Provider == "" || body.Model != "__all" {
			writeError(w, 400, "invalid_reset", "account-wide cooldowns require provider and model __all")
			return
		}
	}
	if h.opts.DB == nil {
		writeError(w, 503, "connections_unavailable", "connection metadata unavailable")
		return
	}
	p, err := readPage(r)
	if err != nil {
		writeError(w, 400, "invalid_page", err.Error())
		return
	}
	config, err := credentials.LoadConfiguration(r.Context(), h.opts.DB)
	if err != nil {
		writeError(w, 500, "availability_failed", "connection metadata unavailable")
		return
	}
	items := []map[string]any{}
	state := h.opts.Runtime.State()
	for _, c := range config.Connections {
		if r.Method == http.MethodPost && c.ProviderID == body.Provider {
			state.ClearCooldown(c.ID)
		}
		if until, active := state.CooldownUntil(c.ID, time.Now()); active {
			items = append(items, map[string]any{"provider": c.ProviderID, "connectionId": c.ID, "connectionName": c.Name, "model": "__all", "status": "cooldown", "until": until})
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "models": slice(items, p), "unavailableCount": len(items), "page": p.Number, "pageSize": p.Size})
}

type catalogSyncState struct {
	Running   bool      `json:"running"`
	LastSync  time.Time `json:"lastSync"`
	LastError string    `json:"lastError,omitempty"`
}

func (h *Handler) handleCatalogSync(w http.ResponseWriter, r *http.Request) {
	h.workflowMu.Lock()
	if r.Method == http.MethodGet {
		state := h.syncState
		h.workflowMu.Unlock()
		models := 0
		if h.opts.Runtime != nil {
			if s, err := h.opts.Runtime.Load(); err == nil {
				models = len(s.Models())
			}
		}
		writeJSON(w, 200, map[string]any{"state": state, "models": models})
		return
	}
	if h.syncState.Running {
		h.workflowMu.Unlock()
		writeError(w, 503, "sync_in_progress", "catalog sync is already running")
		return
	}
	h.syncState.Running = true
	h.workflowMu.Unlock()
	failed := false
	defer func() {
		h.workflowMu.Lock()
		defer h.workflowMu.Unlock()
		h.syncState.Running = false
		h.syncState.LastSync = time.Now().UTC()
		h.syncState.LastError = ""
		if failed {
			h.syncState.LastError = "one or more catalogs could not be synced; known models retained"
		}
	}()
	ids, err := h.workflowIDs(r.Context())
	if err != nil || h.opts.ProviderCatalog == nil {
		failed = true
		writeError(w, 503, "catalog_unavailable", "catalog sync requires credential and model storage (maximum 200 connections)")
		return
	}
	merged := map[string][]runtime.Model{}
	results := []validationResult{}
	for _, id := range ids {
		if r.Context().Err() != nil {
			failed = true
			return
		}
		c, node, err := h.workflowConnection(r.Context(), id)
		result := validationResult{ConnectionID: id, ProviderID: node.ProviderID, Method: "models"}
		if err != nil {
			result.Error = "connection credentials unavailable"
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
			var models []string
			models, err = h.fetchConnectionModels(ctx, c, node)
			cancel()
			if err != nil {
				result.Error = "model discovery failed; known catalog retained"
			} else {
				result.OK, result.ModelsAvailable = true, len(models)
				for _, id := range models {
					merged[node.ProviderID] = append(merged[node.ProviderID], runtime.Model{ID: id})
				}
			}
		}
		if !result.OK {
			failed = true
		}
		results = append(results, result)
	}
	for provider, models := range merged {
		if err := h.opts.ProviderCatalog.MergeDiscoveredModels(r.Context(), provider, models); err != nil {
			failed = true
			results = append(results, validationResult{ProviderID: provider, Method: "publish", Error: "catalog could not be committed; active configuration retained"})
		}
	}
	writeJSON(w, 200, map[string]any{"success": !failed, "results": results, "activeConfigRevision": h.activeRevision()})
}

func (h *Handler) handleNodeRoutes(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, 503, "credentials_unavailable", "provider storage unavailable")
		return
	}
	node, err := h.opts.Credentials.GetNode(r.Context(), r.PathValue("id"))
	if err != nil {
		providerNotFound(w, err)
		return
	}
	routes := map[string]string{}
	spec, _ := h.providerSpec(node.ProviderID)
	base := node.BaseURL
	if base == "" {
		base = spec.DefaultBaseURL
	}
	for _, protocol := range node.Transports {
		path, supported := map[string]string{"openai-chat": "chat/completions", "openai-responses": "responses", "anthropic-messages": "messages", "systemone": "", "gemini": "models/{model}:generateContent", "ollama": "api/chat"}[protocol]
		if node.Kind == credentials.NodeBuiltin {
			if custom, ok := spec.EndpointFor(registry.Protocol(protocol)); ok {
				path = custom
				supported = true
			}
			if node.ProviderID == "cloudflare-ai" {
				switch protocol {
				case "systemone":
					path = "{accountId}/ai/run/{model}"
				case "openai-chat":
					path = "{accountId}/ai/v1/chat/completions"
				}
			}
		}
		if !supported {
			continue
		}
		routes[protocol] = strings.TrimRight(base, "/")
		if path != "" {
			routes[protocol] += "/" + path
		}
	}
	writeJSON(w, 200, map[string]any{"id": node.ID, "routes": routes})
}
