package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
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
		case "models/disabled":
			err = h.disabledModels(w, r)
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
		{&requests, "SELECT COUNT(*) FROM usage_events WHERE termination = ''"},
		{&failed, "SELECT COUNT(*) FROM usage_events WHERE termination = '' AND status >= 400"},
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
	// StaticModels keeps the established wire name and shape: an array of model
	// ids. The seed entries carry names and context windows internally, but the
	// catalog read model is not the place to change that contract.
	StaticModels []string `json:"staticModels,omitempty"`
	ReportsUsage bool     `json:"reportsUsage"`
	// FlowType and SignInModes tell the dashboard which sign-in affordance to
	// offer. Without them the UI cannot know whether a provider uses a browser
	// redirect, a device code, or import only, and offering both panels made half
	// the buttons 404.
	FlowType    string   `json:"flowType,omitempty"`
	SignInModes []string `json:"signInModes,omitempty"`
	// MetaKeys lists the operator input the provider's flow reads, so the
	// dashboard renders only fields that flow consumes.
	MetaKeys []string `json:"metaKeys,omitempty"`
	// SignInMethods describes each sign-in mode on its own, so the dashboard can
	// offer them as separate choices with only that mode's fields. SignInModes and
	// MetaKeys are the earlier union view of the same tables.
	SignInMethods []signInMethodReadModel `json:"signInMethods,omitempty"`
	// BulkImport means POST /oauth/{id}/bulk-import accepts this provider, by the
	// rule that endpoint applies, so the dashboard offers it only where it works.
	BulkImport bool `json:"bulkImport,omitempty"`
	// HostDiscovery means POST /oauth/{id}/discover may read this provider's
	// desktop credentials on this host and a sign-in may attach its account
	// session: MiMo Desktop, when the host allows it (#296).
	HostDiscovery   bool `json:"hostDiscovery,omitempty"`
	ConfiguredNodes int  `json:"configuredNodes"`
	EnabledAccounts int  `json:"enabledAccounts"`
	// Readiness is the build's immutable capability for this provider (SPEC §36).
	// It is not derived from ConfiguredNodes or EnabledAccounts: enabling is a
	// preference, and a stored credential is account state.
	Readiness readinessReadModel `json:"readiness"`
}

// signInMethodReadModel is one way to connect a provider through an onboarding
// flow, as the endpoint for that mode will treat it.
type signInMethodReadModel struct {
	// Mode is "browser", "device" or "import": the endpoint family to call.
	Mode string `json:"mode"`
	// AuthKind is the credential mode the method stores.
	AuthKind string `json:"authKind"`
	// ResolvesIdentity means the provider or credential names the account, so
	// the method needs no identity from the operator.
	ResolvesIdentity bool `json:"resolvesIdentity"`
	// InfersIdentity means a browser method usually learns the account from the
	// provider but cannot promise to, so the identity is optional up front; a
	// sign-in that learns none is refused (oauth_identity_unresolved) and the
	// next attempt asks for it.
	InfersIdentity bool `json:"infersIdentity,omitempty"`
	// Variant is the auth method a browser method is fixed to (Kiro's "google" or
	// "github"); the dashboard sends it as meta.authMethod and offers no field for
	// it. Empty means the method has no variants.
	Variant string `json:"variant,omitempty"`
	// MetaKeys is the operator input this method reads, and only this method.
	MetaKeys []string `json:"metaKeys,omitempty"`
	// Completion is what a browser method's operator pastes back.
	Completion string `json:"completion,omitempty"`
	// LoopbackRedirect is the provider-registered loopback redirect a browser
	// method sends. It names a port on the operator's own computer, not on this
	// server, so the page it lands on is expected not to load and its address is
	// pasted back.
	LoopbackRedirect string `json:"loopbackRedirect,omitempty"`
	// RedirectURI is where a browser method that sends this server's public
	// origin returns the operator's browser (Google, GitLab, Cline, iFlow, Kimchi,
	// Xiaomi): the address a GitLab application registers, and the one whose
	// query is pasted back. RedirectUnconfigured says no public origin is set, so
	// the method cannot start yet (oauth_redirect_unconfigured).
	RedirectURI          string `json:"redirectUri,omitempty"`
	RedirectUnconfigured bool   `json:"redirectUnconfigured,omitempty"`
}

// readinessReadModel reports registry.Readiness. A provider with no declared
// readiness reads "unknown" with no served flag, never "ready" or "unavailable".
type readinessReadModel struct {
	Status string `json:"status"`
	Served *bool  `json:"served,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func readinessOf(r registry.Readiness) readinessReadModel {
	if r.Status == "" {
		return readinessReadModel{Status: "unknown"}
	}
	served := r.Served()
	return readinessReadModel{Status: string(r.Status), Served: &served, Reason: r.Reason}
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
		item := providerReadModel{ID: spec.ID, Auth: string(spec.Auth), DefaultBaseURL: safeURL(spec.DefaultBaseURL), ModelCatalog: string(spec.ModelCatalog), PassthroughModels: spec.PassthroughModels, StaticModels: spec.StaticModels(), ReportsUsage: spec.ReportsUsage, ConfiguredNodes: count[0], EnabledAccounts: count[1], Readiness: readinessOf(spec.Readiness)}
		item.FlowType, item.SignInModes, item.MetaKeys, item.SignInMethods = h.signInModes(spec.ID)
		if flow, ok := h.importFlowFor(spec.ID); ok && flow.Bulk != nil && specAllowsOAuth(spec) {
			item.BulkImport = true
		}
		item.HostDiscovery = spec.ID == mimoProviderID && h.opts.HostCredentialDiscovery && specAllowsOAuth(spec)
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

// providerFilter returns an optional WHERE clause and argument for the
// ?providerId= query so detail views page one provider's rows, not the table.
func providerFilter(r *http.Request, column string) (string, []any) {
	if id := r.URL.Query().Get("providerId"); id != "" {
		return " WHERE " + column + "=?", []any{id}
	}
	return "", nil
}

func (h *Handler) providerNodes(w http.ResponseWriter, r *http.Request) error {
	where, args := providerFilter(r, "provider_id")
	return h.sqlList(w, r, `SELECT id,kind,provider_id,name,COALESCE(prefix,''),COALESCE(base_url,''),COALESCE(transports,'[]'),created_at,updated_at FROM provider_nodes`+where+` ORDER BY provider_id,name,id`, `SELECT COUNT(*) FROM provider_nodes`+where, func(rows *sql.Rows) (any, error) {
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
	}, args...)
}

func (h *Handler) connections(w http.ResponseWriter, r *http.Request) error {
	where, args := providerFilter(r, "n.provider_id")
	// The per-connection proxy lives in the sealed provider data; the compiled
	// snapshot holds the effective one, so the list never opens a secret.
	var snapshot *runtime.RuntimeSnapshot
	if h.opts.Runtime != nil {
		snapshot, _ = h.opts.Runtime.Load()
	}
	return h.sqlList(w, r, `SELECT c.id,c.node_id,n.provider_id,c.name,c.auth_kind,COALESCE(c.identity,''),c.enabled,c.priority,COALESCE(c.proxy_pool_id,''),c.secret_blob IS NOT NULL AND c.secret_blob!='',c.created_at,c.updated_at,c.account_id,
c.test_status,COALESCE(c.last_tested_at,''),COALESCE(c.last_error,''),COALESCE(c.last_error_at,''),c.models_available,c.auth_origin,COALESCE(c.credential_expires_at,'')
FROM provider_connections c JOIN provider_nodes n ON n.id=c.node_id`+where+` ORDER BY n.provider_id,c.priority,c.name,c.id`, `SELECT COUNT(*) FROM provider_connections c JOIN provider_nodes n ON n.id=c.node_id`+where, func(rows *sql.Rows) (any, error) {
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
			AccountID            string `json:"accountId,omitempty"`
			// ConnectionProxy is the LiteRouter per-connection proxy in force when
			// no usable pool is bound, its URL without credentials.
			ConnectionProxy *connectionProxyView `json:"connectionProxy,omitempty"`
			// The validation lifecycle (#220), kept apart so the dashboard can tell
			// a credential the provider accepted (testStatus) from a model that
			// answered (modelsAvailable, lastTestedAt) and from quota observed
			// (plan, quotaObservedAt). Provider readiness is the catalog's. None of
			// these is credential material; lastError was redacted when recorded.
			TestStatus      string `json:"testStatus"`
			LastTestedAt    string `json:"lastTestedAt,omitempty"`
			LastError       string `json:"lastError,omitempty"`
			LastErrorAt     string `json:"lastErrorAt,omitempty"`
			ModelsAvailable *int   `json:"modelsAvailable,omitempty"`
			AuthOrigin      string `json:"authOrigin,omitempty"`
			ExpiresAt       string `json:"expiresAt,omitempty"`
			Plan            string `json:"plan,omitempty"`
			QuotaObservedAt string `json:"quotaObservedAt,omitempty"`
		}
		var enabled, credential int
		if err := rows.Scan(&item.ID, &item.NodeID, &item.ProviderID, &item.Name, &item.AuthKind, &item.Identity, &enabled, &item.Priority, &item.ProxyPoolID, &credential, &item.CreatedAt, &item.UpdatedAt, &item.AccountID,
			&item.TestStatus, &item.LastTestedAt, &item.LastError, &item.LastErrorAt, &item.ModelsAvailable, &item.AuthOrigin, &item.ExpiresAt); err != nil {
			return nil, err
		}
		if h.opts.QuotaState != nil {
			if state, ok := h.opts.QuotaState(item.ID); ok {
				item.Plan = state.Plan
				if !state.ObservedAt.IsZero() {
					item.QuotaObservedAt = state.ObservedAt.UTC().Format(time.RFC3339)
				}
			}
		}
		item.Enabled = enabled != 0
		item.CredentialConfigured = credential != 0
		if legacy, ok := snapshot.LegacyConnectionProxy(item.ID); ok {
			item.ConnectionProxy = &connectionProxyView{URL: safeURL(legacy.URL), NoProxy: strings.Join(legacy.NoProxy, ",")}
		}
		return item, nil
	}, args...)
}

type connectionProxyView struct {
	URL     string `json:"url"`
	NoProxy string `json:"noProxy,omitempty"`
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request, systemOne bool) error {
	const selectModels = `SELECT provider_id,model_id,COALESCE(display_name,''),COALESCE(context_window,0),capabilities,
 kind,
EXISTS(SELECT 1 FROM disabled_models d WHERE d.provider_id=provider_models.provider_id AND d.model_id=provider_models.model_id),
EXISTS(SELECT 1 FROM custom_models c WHERE c.provider_id=provider_models.provider_id AND c.model_id=provider_models.model_id),updated_at
FROM provider_models`
	const orderModels = " ORDER BY provider_id,model_id"
	where := ""
	var args []any
	if systemOne {
		if h.opts.Runtime == nil || h.opts.DB == nil {
			writeError(w, http.StatusServiceUnavailable, "read_models_unavailable", "runtime model catalog is unavailable")
			return nil
		}
		snapshot, err := h.opts.Runtime.Load()
		if err != nil {
			return err
		}
		rows, err := h.opts.DB.QueryContext(r.Context(), "SELECT provider_id,model_id,kind FROM provider_models")
		if err != nil {
			return err
		}
		defer rows.Close()
		// Classify before pagination; transport-only filters misclassify mixed nodes.
		pairs := [][2]string{}
		for rows.Next() {
			var model runtime.Model
			if err := rows.Scan(&model.ProviderID, &model.ID, &model.Kind); err != nil {
				return err
			}
			if snapshot.ModelKind(model) == string(registry.TransportSystemOne) {
				pairs = append(pairs, [2]string{model.ProviderID, model.ID})
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		encoded, err := json.Marshal(pairs)
		if err != nil {
			return err
		}
		where = " WHERE (provider_id,model_id) IN (SELECT json_extract(value,'$[0]'),json_extract(value,'$[1]') FROM json_each(?))"
		args = []any{string(encoded)}
	} else {
		where, args = providerFilter(r, "provider_id")
	}
	query := selectModels + where + orderModels
	countQuery := "SELECT COUNT(*) FROM provider_models" + where
	return h.sqlList(w, r, query, countQuery, func(rows *sql.Rows) (any, error) {
		var item struct {
			ProviderID string   `json:"providerId"`
			ID         string   `json:"id"`
			Name       string   `json:"name,omitempty"`
			Context    int      `json:"contextWindow,omitempty"`
			CapsRaw    string   `json:"-"`
			Caps       []string `json:"capabilities"`
			Kind       string   `json:"kind,omitempty"`
			Disabled   bool     `json:"disabled"`
			Custom     bool     `json:"custom"`
			UpdatedAt  string   `json:"updatedAt"`
		}
		if err := rows.Scan(&item.ProviderID, &item.ID, &item.Name, &item.Context, &item.CapsRaw, &item.Kind, &item.Disabled, &item.Custom, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(item.CapsRaw), &item.Caps); err != nil {
			return nil, err
		}
		if systemOne {
			item.Kind = string(registry.TransportSystemOne)
		}
		return item, nil
	}, args...)
}

// disabledModels lists disabled provider/model pairs. Built-in (seed) models
// have no provider_models row, so this is the only read path that reports their
// disabled state.
func (h *Handler) disabledModels(w http.ResponseWriter, r *http.Request) error {
	where, args := providerFilter(r, "provider_id")
	return h.sqlList(w, r, "SELECT provider_id,model_id FROM disabled_models"+where+" ORDER BY provider_id,model_id", "SELECT COUNT(*) FROM disabled_models"+where, func(rows *sql.Rows) (any, error) {
		var item struct {
			ProviderID string `json:"providerId"`
			ModelID    string `json:"modelId"`
		}
		if err := rows.Scan(&item.ProviderID, &item.ModelID); err != nil {
			return nil, err
		}
		return item, nil
	}, args...)
}

func (h *Handler) aliases(w http.ResponseWriter, r *http.Request) error {
	where, args := providerFilter(r, "provider_id")
	return h.sqlList(w, r, "SELECT alias,provider_id,model_id,updated_at FROM model_aliases"+where+" ORDER BY alias", "SELECT COUNT(*) FROM model_aliases"+where, func(rows *sql.Rows) (any, error) {
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
	}, args...)
}

func (h *Handler) pricing(w http.ResponseWriter, r *http.Request) error {
	where, args := providerFilter(r, "provider_id")
	return h.sqlList(w, r, `SELECT provider_id,model_id,input_per_mtok,output_per_mtok,cache_read_per_mtok,cache_write_per_mtok,updated_at FROM pricing_overrides`+where+` ORDER BY provider_id,model_id`, "SELECT COUNT(*) FROM pricing_overrides"+where, func(rows *sql.Rows) (any, error) {
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
	}, args...)
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
	return h.sqlList(w, r, "SELECT id,name,key_prefix,enabled,paused,created_at,last_used_at,access_restricted,access_allow FROM api_keys ORDER BY created_at DESC,id", "SELECT COUNT(*) FROM api_keys", func(rows *sql.Rows) (any, error) {
		var item struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Prefix    string `json:"prefix"`
			Enabled   bool   `json:"enabled"`
			Paused    bool   `json:"paused"`
			CreatedAt string `json:"createdAt"`
			LastUsed  sql.NullString
		}
		var enabled, paused, restricted int
		var allowRaw string
		if err := rows.Scan(&item.ID, &item.Name, &item.Prefix, &enabled, &paused, &item.CreatedAt, &item.LastUsed, &restricted, &allowRaw); err != nil {
			return nil, err
		}
		item.Enabled, item.Paused = enabled != 0, paused != 0
		var allow []string
		if err := json.Unmarshal([]byte(allowRaw), &allow); err != nil {
			return nil, fmt.Errorf("decode access allowlist for API key %q: %w", item.ID, err)
		}
		result := map[string]any{"id": item.ID, "name": item.Name, "prefix": item.Prefix, "enabled": item.Enabled, "paused": item.Paused, "createdAt": item.CreatedAt, "access": accessJSON(restricted != 0, allow)}
		if item.LastUsed.Valid {
			result["lastUsedAt"] = item.LastUsed.String
		}
		return result, nil
	})
}

const usageColumns = "id,request_id,provider_id,model_id,connection_id,api_key_id,status,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,duration_ms,ttft_ms,created_at,termination,usage_observed"

func (h *Handler) usage(w http.ResponseWriter, r *http.Request) error {
	where, args, err := usageFilter(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return nil
	}
	return h.sqlList(w, r, "SELECT "+usageColumns+" FROM usage_events"+where+" ORDER BY created_at DESC,id DESC", "SELECT COUNT(*) FROM usage_events"+where, func(rows *sql.Rows) (any, error) {
		return scanUsageEvent(rows)
	}, args...)
}

// scanUsageEvent reads one usageColumns row into its API shape; absent
// attribution and timings are omitted rather than rendered as zero.
func scanUsageEvent(rows *sql.Rows) (map[string]any, error) {
	var item struct {
		ID          int64
		RequestID   string
		Provider    sql.NullString
		Model       sql.NullString
		Connection  sql.NullString
		APIKey      sql.NullString
		Status      int
		Input       int64
		Output      int64
		CacheRead   int64
		CacheWrite  int64
		Duration    sql.NullInt64
		TTFT        sql.NullInt64
		CreatedAt   string
		Termination string
		Observed    bool
	}
	if err := rows.Scan(&item.ID, &item.RequestID, &item.Provider, &item.Model, &item.Connection, &item.APIKey, &item.Status, &item.Input, &item.Output, &item.CacheRead, &item.CacheWrite, &item.Duration, &item.TTFT, &item.CreatedAt, &item.Termination, &item.Observed); err != nil {
		return nil, err
	}
	result := map[string]any{"id": item.ID, "requestId": item.RequestID, "status": item.Status, "inputTokens": item.Input, "outputTokens": item.Output, "cacheReadTokens": item.CacheRead, "cacheWriteTokens": item.CacheWrite, "createdAt": item.CreatedAt}
	if !item.Observed {
		// Zero means measured zero; a request whose upstream never reported
		// usage carries no token counts at all.
		for _, key := range []string{"inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens"} {
			delete(result, key)
		}
		result["usageUnobserved"] = true
	}
	if item.Termination != "" {
		result["termination"] = item.Termination
	}
	putNull(result, "providerId", item.Provider)
	putNull(result, "modelId", item.Model)
	putNull(result, "connectionId", item.Connection)
	putNull(result, "apiKeyId", item.APIKey)
	putNullInt(result, "durationMs", item.Duration)
	putNullInt(result, "ttftMs", item.TTFT)
	return result, nil
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

// signInModes reports how a provider can be connected, so the dashboard offers
// exactly the affordances that exist: "browser", "device" and/or "import".
//
// It reads the same flow tables the endpoints use, so the UI cannot drift from
// what the server accepts. An empty result means the provider has no onboarding
// flow at all and keeps the manual credential form.
func (h *Handler) signInModes(providerID string) (string, []string, []string, []signInMethodReadModel) {
	flowType := ""
	var metaKeys []string
	var methods []signInMethodReadModel
	if flow, ok := h.oauthFlows.Lookup(providerID); ok {
		flowType = string(flow.Type)
		metaKeys = append(metaKeys, flow.MetaKeys...)
		mode := ""
		switch flow.Type {
		case onboarding.FlowDeviceCode:
			mode = "device"
		case onboarding.FlowImportToken, onboarding.FlowManualToken:
			// Import only; no browser or device step exists.
		default:
			mode = "browser"
		}
		if mode == "browser" && flow.Startable() {
			methods = append(methods, h.browserMethods(flow)...)
		} else if mode != "" && flow.Startable() {
			methods = append(methods, signInMethodFor(mode, flow, flow.MetaKeys))
		}
		// A browser sign-in offered beside the device flow (Kiro's Google and
		// GitHub login) is listed after it, from the same table the endpoint reads.
		if flow.Browser != nil && flow.Browser.Startable() {
			methods = append(methods, h.browserMethods(*flow.Browser)...)
		}
	}
	if manual, ok := h.importFlowFor(providerID); ok {
		// An import mode's own operator input is additive with the browser flow's.
		metaKeys = append(metaKeys, manual.MetaKeys...)
		methods = append(methods, signInMethodFor("import", manual, manual.ImportKeys()))
	}
	modes := make([]string, 0, len(methods))
	for _, method := range methods {
		modes = append(modes, method.Mode)
	}
	return flowType, modes, dedupeStrings(metaKeys), methods
}

// browserMethods describes a browser flow's sign-in methods. A flow whose auth
// method has no default (Kiro's social login) is one method per auth method,
// each fixed to it (Variant), so the operator picks "Google" or "GitHub" rather
// than typing a value; the start still checks what is sent.
func (h *Handler) browserMethods(flow onboarding.Flow) []signInMethodReadModel {
	describe := func(variant string, metaKeys []string) signInMethodReadModel {
		method := signInMethodFor("browser", flow, metaKeys)
		method.Variant = variant
		method.Completion = string(flow.Completion)
		if flow.FixedPort > 0 {
			method.LoopbackRedirect, _ = onboarding.DefaultRedirectURI(flow, "")
		} else if flow.NeedsRedirectURI() {
			redirect, err := onboarding.DefaultRedirectURI(flow, h.dashboardOrigin())
			method.RedirectURI, method.RedirectUnconfigured = redirect, err != nil
		}
		method.InfersIdentity = flow.InfersIdentity && !flow.ResolvesIdentity
		return method
	}
	if len(flow.AuthMethods) == 0 || flow.DefaultAuthMethod != "" {
		return []signInMethodReadModel{describe("", flow.MetaKeys)}
	}
	var operator []string
	for _, key := range flow.MetaKeys {
		if key != "authMethod" {
			operator = append(operator, key)
		}
	}
	methods := make([]signInMethodReadModel, 0, len(flow.AuthMethods))
	for _, variant := range flow.AuthMethods {
		methods = append(methods, describe(variant, operator))
	}
	return methods
}

func signInMethodFor(mode string, flow onboarding.Flow, metaKeys []string) signInMethodReadModel {
	authKind := flow.AuthKind
	if authKind == "" {
		authKind = credentials.AuthOAuth
	}
	return signInMethodReadModel{Mode: mode, AuthKind: string(authKind), ResolvesIdentity: flow.ResolvesIdentity, MetaKeys: dedupeStrings(metaKeys)}
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
