package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/telemetry"
)

const (
	defaultPageSize = 50
	maxPageSize     = 100
)

type page struct {
	Number int
	Size   int
	Offset int
}

func readPage(r *http.Request) (page, error) {
	result := page{Number: 1, Size: defaultPageSize}
	if raw := r.URL.Query().Get("page"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			return page{}, errors.New("page must be a positive integer")
		}
		result.Number = value
	}
	if raw := r.URL.Query().Get("pageSize"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			return page{}, errors.New("pageSize must be a positive integer")
		}
		result.Size = value
	}
	if result.Size > maxPageSize {
		result.Size = maxPageSize
	}
	maxInt := int(^uint(0) >> 1)
	if result.Number-1 > maxInt/result.Size {
		return page{}, errors.New("page is too large")
	}
	result.Offset = (result.Number - 1) * result.Size
	return result, nil
}

func pageResponse(items any, p page, total int64) map[string]any {
	return map[string]any{"items": items, "page": p.Number, "pageSize": p.Size, "total": total}
}

func (h *Handler) readModel(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		switch name {
		case "overview":
			err = h.overview(w, r)
		case "providers":
			err = h.providers(w, r)
		case "provider-nodes":
			err = h.providerNodes(w, r)
		case "connections":
			err = h.connections(w, r)
		case "models":
			err = h.models(w, r, false)
		case "aliases":
			err = h.aliases(w, r)
		case "pricing":
			err = h.pricing(w, r)
		case "combos":
			err = h.combos(w, r)
		case "proxy-pools":
			err = h.proxyPools(w, r)
		case "keys":
			err = h.keys(w, r)
		case "usage":
			err = h.usage(w, r)
		case "requests":
			err = h.requests(w, r)
		case "quota":
			err = h.quota(w, r)
		case "token-saver":
			err = h.tokenSaver(w, r)
		case "systemone":
			err = h.models(w, r, true)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_failed", "could not load admin read model")
		}
	})
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) error {
	if h.opts.DB == nil || h.opts.Runtime == nil {
		writeError(w, http.StatusServiceUnavailable, "read_models_unavailable", "admin read models are not configured")
		return nil
	}
	snapshot, err := h.opts.Runtime.Load()
	if err != nil {
		return err
	}
	var nodes, connections, keys, requests, failed int64
	var lastRequest sql.NullString
	queries := []struct {
		dst   any
		query string
	}{
		{&nodes, "SELECT COUNT(*) FROM provider_nodes"},
		{&connections, "SELECT COUNT(*) FROM provider_connections WHERE enabled=1"},
		{&keys, "SELECT COUNT(*) FROM api_keys WHERE enabled=1 AND paused=0"},
		{&requests, "SELECT COUNT(*) FROM usage_events"},
		{&failed, "SELECT COUNT(*) FROM usage_events WHERE status >= 400"},
	}
	for _, query := range queries {
		if err := h.opts.DB.QueryRowContext(r.Context(), query.query).Scan(query.dst); err != nil {
			return err
		}
	}
	if err := h.opts.DB.QueryRowContext(r.Context(), "SELECT MAX(created_at) FROM usage_events").Scan(&lastRequest); err != nil {
		return err
	}
	ready := h.opts.Ready != nil && h.opts.Ready()
	health := "ready"
	if !ready {
		health = "not_ready"
	}
	telemetryState := map[string]any{"enabled": false, "written": uint64(0), "lost": uint64(0), "lostDiagnostics": uint64(0), "degraded": false}
	if service := h.opts.Telemetry; service != nil {
		telemetryState["enabled"] = service.Enabled()
		telemetryState["written"] = service.Written()
		telemetryState["lost"] = service.Lost()
		telemetryState["lostDiagnostics"] = service.LostDiagnostics()
		telemetryState["degraded"] = service.Health() != nil
	}
	var last string
	if lastRequest.Valid {
		last = lastRequest.String
	}
	active := int64(0)
	if h.opts.ActiveRequests != nil {
		active = h.opts.ActiveRequests()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runtime":   map[string]any{"health": health, "version": h.opts.Build.Version, "commit": h.opts.Build.Commit, "configRevision": snapshot.ConfigRevision(), "snapshotVersion": snapshot.Version()},
		"providers": map[string]any{"catalog": len(h.opts.Providers), "nodes": nodes, "enabledConnections": connections},
		"apiKeys":   map[string]any{"active": keys},
		"traffic":   map[string]any{"active": active, "requests": requests, "errors": failed, "lastRequestAt": last},
		"telemetry": telemetryState,
	})
	return nil
}

type providerReadModel struct {
	ID                string   `json:"id"`
	Transports        []string `json:"transports"`
	Auth              string   `json:"auth"`
	AuthModes         []string `json:"authModes"`
	DefaultBaseURL    string   `json:"defaultBaseURL,omitempty"`
	ModelCatalog      string   `json:"modelCatalog"`
	PassthroughModels bool     `json:"passthroughModels"`
	StaticModels      []string `json:"staticModels,omitempty"`
	ReportsUsage      bool     `json:"reportsUsage"`
	ConfiguredNodes   int      `json:"configuredNodes"`
	EnabledAccounts   int      `json:"enabledAccounts"`
}

func (h *Handler) providers(w http.ResponseWriter, r *http.Request) error {
	p, err := readPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_page", err.Error())
		return nil
	}
	counts := map[string][2]int{}
	if h.opts.DB != nil {
		rows, err := h.opts.DB.QueryContext(r.Context(), `SELECT n.provider_id,COUNT(DISTINCT n.id),SUM(CASE WHEN c.enabled=1 THEN 1 ELSE 0 END)
FROM provider_nodes n LEFT JOIN provider_connections c ON c.node_id=n.id GROUP BY n.provider_id ORDER BY n.provider_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var nodes, accounts int
			if err := rows.Scan(&id, &nodes, &accounts); err != nil {
				return err
			}
			counts[id] = [2]int{nodes, accounts}
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	specs := append([]registry.Spec(nil), h.opts.Providers...)
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	items := make([]providerReadModel, 0, len(specs))
	for _, spec := range specs {
		count := counts[spec.ID]
		item := providerReadModel{ID: spec.ID, Auth: string(spec.Auth), DefaultBaseURL: safeURL(spec.DefaultBaseURL), ModelCatalog: string(spec.ModelCatalog), PassthroughModels: spec.PassthroughModels, StaticModels: spec.StaticModels, ReportsUsage: spec.ReportsUsage, ConfiguredNodes: count[0], EnabledAccounts: count[1]}
		for _, transport := range spec.Transports {
			item.Transports = append(item.Transports, string(transport))
		}
		for _, mode := range spec.AuthModes {
			item.AuthModes = append(item.AuthModes, string(mode))
		}
		if len(item.AuthModes) == 0 {
			item.AuthModes = []string{string(spec.Auth)}
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, pageResponse(slice(items, p), p, int64(len(items))))
	return nil
}

func (h *Handler) providerNodes(w http.ResponseWriter, r *http.Request) error {
	return h.sqlList(w, r, `SELECT id,kind,provider_id,name,COALESCE(prefix,''),COALESCE(base_url,''),COALESCE(transports,'[]'),created_at,updated_at FROM provider_nodes ORDER BY provider_id,name,id`, `SELECT COUNT(*) FROM provider_nodes`, func(rows *sql.Rows) (any, error) {
		var item struct {
			ID         string          `json:"id"`
			Kind       string          `json:"kind"`
			ProviderID string          `json:"providerId"`
			Name       string          `json:"name"`
			Prefix     string          `json:"prefix,omitempty"`
			BaseURL    string          `json:"baseUrl,omitempty"`
			Transports json.RawMessage `json:"transports"`
			CreatedAt  string          `json:"createdAt"`
			UpdatedAt  string          `json:"updatedAt"`
		}
		var baseURL, transports string
		if err := rows.Scan(&item.ID, &item.Kind, &item.ProviderID, &item.Name, &item.Prefix, &baseURL, &transports, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.BaseURL = safeURL(baseURL)
		item.Transports = json.RawMessage(transports)
		return item, nil
	})
}

func (h *Handler) connections(w http.ResponseWriter, r *http.Request) error {
	return h.sqlList(w, r, `SELECT c.id,c.node_id,n.provider_id,c.name,c.auth_kind,c.identity,c.enabled,c.priority,COALESCE(c.proxy_pool_id,''),c.secret_blob IS NOT NULL AND c.secret_blob!='',c.created_at,c.updated_at
FROM provider_connections c JOIN provider_nodes n ON n.id=c.node_id ORDER BY n.provider_id,c.priority,c.name,c.id`, `SELECT COUNT(*) FROM provider_connections`, func(rows *sql.Rows) (any, error) {
		var item struct {
			ID                   string `json:"id"`
			NodeID               string `json:"nodeId"`
			ProviderID           string `json:"providerId"`
			Name                 string `json:"name"`
			AuthKind             string `json:"authKind"`
			Identity             string `json:"identity"`
			Enabled              bool   `json:"enabled"`
			Priority             int    `json:"priority"`
			ProxyPoolID          string `json:"proxyPoolId,omitempty"`
			CredentialConfigured bool   `json:"credentialConfigured"`
			CreatedAt            string `json:"createdAt"`
			UpdatedAt            string `json:"updatedAt"`
		}
		var enabled, credential int
		if err := rows.Scan(&item.ID, &item.NodeID, &item.ProviderID, &item.Name, &item.AuthKind, &item.Identity, &enabled, &item.Priority, &item.ProxyPoolID, &credential, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Enabled = enabled != 0
		item.CredentialConfigured = credential != 0
		return item, nil
	})
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request, systemOne bool) error {
	query := `SELECT provider_id,model_id,COALESCE(display_name,''),COALESCE(context_window,0),capabilities,
EXISTS(SELECT 1 FROM disabled_models d WHERE d.provider_id=provider_models.provider_id AND d.model_id=provider_models.model_id),updated_at
FROM provider_models ORDER BY provider_id,model_id`
	countQuery := "SELECT COUNT(*) FROM provider_models"
	var args []any
	if systemOne {
		var allowed []string
		for _, spec := range h.opts.Providers {
			for _, transport := range spec.Transports {
				if transport == registry.TransportSystemOne {
					allowed = append(allowed, spec.ID)
					break
				}
			}
		}
		if len(allowed) == 0 {
			query = strings.TrimSuffix(query, " ORDER BY provider_id,model_id") + " WHERE 1=0 ORDER BY provider_id,model_id"
			countQuery = "SELECT 0"
		} else {
			placeholders := make([]string, len(allowed))
			for i, id := range allowed {
				placeholders[i] = "?"
				args = append(args, id)
			}
			where := " WHERE provider_id IN (" + strings.Join(placeholders, ",") + ")"
			query = strings.TrimSuffix(query, " ORDER BY provider_id,model_id") + where + " ORDER BY provider_id,model_id"
			countQuery += where
		}
	}
	return h.sqlList(w, r, query, countQuery, func(rows *sql.Rows) (any, error) {
		var item struct {
			ProviderID string   `json:"providerId"`
			ID         string   `json:"id"`
			Name       string   `json:"name,omitempty"`
			Context    int      `json:"contextWindow,omitempty"`
			CapsRaw    string   `json:"-"`
			Caps       []string `json:"capabilities"`
			Disabled   bool     `json:"disabled"`
			UpdatedAt  string   `json:"updatedAt"`
		}
		if err := rows.Scan(&item.ProviderID, &item.ID, &item.Name, &item.Context, &item.CapsRaw, &item.Disabled, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(item.CapsRaw), &item.Caps); err != nil {
			return nil, err
		}
		return item, nil
	}, args...)
}

func (h *Handler) aliases(w http.ResponseWriter, r *http.Request) error {
	return h.sqlList(w, r, "SELECT alias,provider_id,model_id,updated_at FROM model_aliases ORDER BY alias", "SELECT COUNT(*) FROM model_aliases", func(rows *sql.Rows) (any, error) {
		var item struct {
			Alias      string `json:"alias"`
			ProviderID string `json:"providerId"`
			ModelID    string `json:"modelId"`
			UpdatedAt  string `json:"updatedAt"`
		}
		if err := rows.Scan(&item.Alias, &item.ProviderID, &item.ModelID, &item.UpdatedAt); err != nil {
			return nil, err
		}
		return item, nil
	})
}

func (h *Handler) pricing(w http.ResponseWriter, r *http.Request) error {
	return h.sqlList(w, r, `SELECT provider_id,model_id,input_per_mtok,output_per_mtok,cache_read_per_mtok,cache_write_per_mtok,updated_at FROM pricing_overrides ORDER BY provider_id,model_id`, "SELECT COUNT(*) FROM pricing_overrides", func(rows *sql.Rows) (any, error) {
		var item struct {
			ProviderID string   `json:"providerId"`
			ModelID    string   `json:"modelId"`
			Input      *float64 `json:"inputPerMTok"`
			Output     *float64 `json:"outputPerMTok"`
			CacheRead  *float64 `json:"cacheReadPerMTok"`
			CacheWrite *float64 `json:"cacheWritePerMTok"`
			UpdatedAt  string   `json:"updatedAt"`
		}
		if err := rows.Scan(&item.ProviderID, &item.ModelID, &item.Input, &item.Output, &item.CacheRead, &item.CacheWrite, &item.UpdatedAt); err != nil {
			return nil, err
		}
		return item, nil
	})
}

func (h *Handler) combos(w http.ResponseWriter, r *http.Request) error {
	if h.opts.Runtime == nil {
		writeError(w, http.StatusServiceUnavailable, "read_models_unavailable", "runtime is not configured")
		return nil
	}
	snapshot, err := h.opts.Runtime.Load()
	if err != nil {
		return err
	}
	items := snapshot.Combos()
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	payload := make([]map[string]any, 0, len(items))
	for _, combo := range items {
		payload = append(payload, comboJSON(combo, snapshot.ConfigRevision()))
	}
	p, err := readPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_page", err.Error())
		return nil
	}
	writeJSON(w, http.StatusOK, pageResponse(slice(payload, p), p, int64(len(payload))))
	return nil
}

func (h *Handler) proxyPools(w http.ResponseWriter, r *http.Request) error {
	if h.opts.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "read_models_unavailable", "database read models are not configured")
		return nil
	}
	p, err := readPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_page", err.Error())
		return nil
	}
	var total int64
	if err := h.opts.DB.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM proxy_pools").Scan(&total); err != nil {
		return err
	}
	// Read pool rows and members in separate statements: fetching members while
	// the pool rows are still open would hold a second pooled connection and can
	// self-deadlock against SQLite's bounded pool.
	items, err := collectProxyPools(r.Context(), h.opts.DB, p)
	if err != nil {
		return err
	}
	for i := range items {
		members, err := h.proxyMembers(r.Context(), items[i].ID)
		if err != nil {
			return err
		}
		items[i].Members = members
	}
	writeJSON(w, http.StatusOK, pageResponse(items, p, total))
	return nil
}

type proxyPoolReadModel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Strategy  string `json:"strategy"`
	Members   []any  `json:"members"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func collectProxyPools(ctx context.Context, db *sql.DB, p page) ([]proxyPoolReadModel, error) {
	rows, err := db.QueryContext(ctx, "SELECT id,name,enabled,strategy,created_at,updated_at FROM proxy_pools ORDER BY name,id LIMIT ? OFFSET ?", p.Size, p.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]proxyPoolReadModel, 0, p.Size)
	for rows.Next() {
		var item proxyPoolReadModel
		var enabled int
		if err := rows.Scan(&item.ID, &item.Name, &enabled, &item.Strategy, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Enabled = enabled != 0
		items = append(items, item)
	}
	return items, rows.Err()
}

func (h *Handler) proxyMembers(ctx context.Context, poolID string) ([]any, error) {
	rows, err := h.opts.DB.QueryContext(ctx, "SELECT id,position,proxy_url,enabled FROM proxy_pool_members WHERE pool_id=? ORDER BY position,id", poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []any
	for rows.Next() {
		var id, raw string
		var position, enabled int
		if err := rows.Scan(&id, &position, &raw, &enabled); err != nil {
			return nil, err
		}
		members = append(members, map[string]any{"id": id, "position": position, "url": safeURL(raw), "enabled": enabled != 0})
	}
	return members, rows.Err()
}

func (h *Handler) keys(w http.ResponseWriter, r *http.Request) error {
	return h.sqlList(w, r, "SELECT id,name,key_prefix,enabled,paused,created_at,last_used_at FROM api_keys ORDER BY created_at DESC,id", "SELECT COUNT(*) FROM api_keys", func(rows *sql.Rows) (any, error) {
		var item struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Prefix    string `json:"prefix"`
			Enabled   bool   `json:"enabled"`
			Paused    bool   `json:"paused"`
			CreatedAt string `json:"createdAt"`
			LastUsed  sql.NullString
		}
		var enabled, paused int
		if err := rows.Scan(&item.ID, &item.Name, &item.Prefix, &enabled, &paused, &item.CreatedAt, &item.LastUsed); err != nil {
			return nil, err
		}
		item.Enabled, item.Paused = enabled != 0, paused != 0
		result := map[string]any{"id": item.ID, "name": item.Name, "prefix": item.Prefix, "enabled": item.Enabled, "paused": item.Paused, "createdAt": item.CreatedAt}
		if item.LastUsed.Valid {
			result["lastUsedAt"] = item.LastUsed.String
		}
		return result, nil
	})
}

func (h *Handler) usage(w http.ResponseWriter, r *http.Request) error {
	return h.sqlList(w, r, `SELECT id,request_id,provider_id,model_id,connection_id,api_key_id,status,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,duration_ms,ttft_ms,created_at FROM usage_events ORDER BY created_at DESC,id DESC`, "SELECT COUNT(*) FROM usage_events", func(rows *sql.Rows) (any, error) {
		var item struct {
			ID         int64  `json:"id"`
			RequestID  string `json:"requestId"`
			Provider   sql.NullString
			Model      sql.NullString
			Connection sql.NullString
			APIKey     sql.NullString
			Status     int   `json:"status"`
			Input      int64 `json:"inputTokens"`
			Output     int64 `json:"outputTokens"`
			CacheRead  int64 `json:"cacheReadTokens"`
			CacheWrite int64 `json:"cacheWriteTokens"`
			Duration   sql.NullInt64
			TTFT       sql.NullInt64
			CreatedAt  string `json:"createdAt"`
		}
		if err := rows.Scan(&item.ID, &item.RequestID, &item.Provider, &item.Model, &item.Connection, &item.APIKey, &item.Status, &item.Input, &item.Output, &item.CacheRead, &item.CacheWrite, &item.Duration, &item.TTFT, &item.CreatedAt); err != nil {
			return nil, err
		}
		result := map[string]any{"id": item.ID, "requestId": item.RequestID, "status": item.Status, "inputTokens": item.Input, "outputTokens": item.Output, "cacheReadTokens": item.CacheRead, "cacheWriteTokens": item.CacheWrite, "createdAt": item.CreatedAt}
		putNull(result, "providerId", item.Provider)
		putNull(result, "modelId", item.Model)
		putNull(result, "connectionId", item.Connection)
		putNull(result, "apiKeyId", item.APIKey)
		putNullInt(result, "durationMs", item.Duration)
		putNullInt(result, "ttftMs", item.TTFT)
		return result, nil
	})
}

func (h *Handler) requests(w http.ResponseWriter, r *http.Request) error {
	return h.sqlList(w, r, "SELECT request_id,route_mode,detail,created_at FROM request_details ORDER BY created_at DESC,request_id", "SELECT COUNT(*) FROM request_details", func(rows *sql.Rows) (any, error) {
		var id, mode, raw, created string
		if err := rows.Scan(&id, &mode, &raw, &created); err != nil {
			return nil, err
		}
		var detail any
		if json.Unmarshal([]byte(raw), &detail) != nil {
			detail = map[string]string{"omitted": "invalid stored detail"}
		}
		detail = telemetry.Redact(detail)
		return map[string]any{"requestId": id, "routeMode": mode, "detail": detail, "createdAt": created}, nil
	})
}

func (h *Handler) quota(w http.ResponseWriter, r *http.Request) error {
	if h.opts.Runtime == nil || h.opts.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "read_models_unavailable", "quota read model is not configured")
		return nil
	}
	p, err := readPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_page", err.Error())
		return nil
	}
	var total int64
	if err := h.opts.DB.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM provider_connections").Scan(&total); err != nil {
		return err
	}
	rows, err := h.opts.DB.QueryContext(r.Context(), `SELECT c.id,n.provider_id,c.name,c.enabled FROM provider_connections c JOIN provider_nodes n ON n.id=c.node_id ORDER BY n.provider_id,c.priority,c.name,c.id LIMIT ? OFFSET ?`, p.Size, p.Offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := make([]any, 0, p.Size)
	state := h.opts.Runtime.State()
	for rows.Next() {
		var id, provider, name string
		var enabled int
		if err := rows.Scan(&id, &provider, &name, &enabled); err != nil {
			return err
		}
		item := map[string]any{"connectionId": id, "providerId": provider, "name": name, "enabled": enabled != 0, "status": "unknown"}
		if state != nil {
			if observation, ok := state.Quota(id); ok {
				if observation.Err != "" {
					item["status"] = "error"
					item["error"] = true
				} else if observation.Remaining != nil {
					item["status"] = "available"
					if *observation.Remaining <= 0 && (observation.ResetAt.IsZero() || observation.ResetAt.After(time.Now())) {
						item["status"] = "exhausted"
					}
					item["remaining"] = *observation.Remaining
				}
				if !observation.ResetAt.IsZero() {
					item["resetAt"] = observation.ResetAt
				}
				if !observation.ObservedAt.IsZero() {
					item["observedAt"] = observation.ObservedAt
				}
			}
			if until, ok := state.CooldownUntil(id, time.Now()); ok {
				item["status"] = "cooldown"
				item["cooldownUntil"] = until
			}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, pageResponse(items, p, total))
	return nil
}

func (h *Handler) tokenSaver(w http.ResponseWriter, _ *http.Request) error {
	if h.opts.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings are not configured")
		return nil
	}
	all := safeSettings(h.opts.Settings.Settings())
	selected := make(map[string]string)
	for key, value := range all {
		normalized := strings.ToLower(key)
		if strings.Contains(normalized, "rtk") || strings.Contains(normalized, "headroom") || strings.Contains(normalized, "caveman") || strings.Contains(normalized, "ponytail") || strings.Contains(normalized, "pxpipe") {
			selected[key] = value
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": selected})
	return nil
}

func (h *Handler) sqlList(w http.ResponseWriter, r *http.Request, query, countQuery string, scan func(*sql.Rows) (any, error), args ...any) error {
	if h.opts.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "read_models_unavailable", "database read models are not configured")
		return nil
	}
	p, err := readPage(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_page", err.Error())
		return nil
	}
	var total int64
	if err := h.opts.DB.QueryRowContext(r.Context(), countQuery, args...).Scan(&total); err != nil {
		return err
	}
	queryArgs := append(append([]any(nil), args...), p.Size, p.Offset)
	rows, err := h.opts.DB.QueryContext(r.Context(), query+" LIMIT ? OFFSET ?", queryArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := make([]any, 0, p.Size)
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, pageResponse(items, p, total))
	return nil
}

func safeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		if raw == "" {
			return ""
		}
		return "[redacted]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func slice[T any](items []T, p page) []T {
	start := p.Offset
	if start >= len(items) {
		return []T{}
	}
	end := start + p.Size
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}
func putNull(dst map[string]any, key string, value sql.NullString) {
	if value.Valid {
		dst[key] = value.String
	}
}
func putNullInt(dst map[string]any, key string, value sql.NullInt64) {
	if value.Valid {
		dst[key] = value.Int64
	}
}
