package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/proxy"
	"github.com/raufimusaddiq/routeweft/internal/proxy/netlify"
)

// handleNetlifyDeploy provisions a Netlify relay and records it as a proxy pool
// (src/app/api/proxy-pools/netlify-deploy/route.js; PARITY 6.38). The request
// carries the operator's Netlify token. It is used for the calls to Netlify and
// goes nowhere else: it is not stored, logged, or part of any answer, and the pool
// that is created holds only the relay's public address.
func (h *Handler) handleNetlifyDeploy(w http.ResponseWriter, r *http.Request) {
	if h.proxyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy_pools_unavailable", "proxy pool management is not configured")
		return
	}
	var body netlify.Request
	if !decodeJSON(w, r, 1<<16, &body) {
		return
	}
	token := body.Token()
	if token == "" {
		writeError(w, http.StatusBadRequest, "netlify_token_required", "Netlify API token is required")
		return
	}
	client := netlify.Client{API: h.opts.NetlifyAPI, HTTP: h.opts.NetlifyHTTP}
	if client.HTTP == nil {
		client.HTTP = h.saverClient
	}
	name := netlify.SiteName(body.RequestedName(), time.Now())
	result, failure := client.Provision(r.Context(), token, name)
	if failure != nil {
		writeError(w, failure.Status, netlifyErrorCode(failure.Status), failure.Message)
		return
	}
	relay, err := proxy.ValidateRelayURL(result.RelayURL)
	if err != nil || h.checkProxyHost(relay) != nil {
		writeError(w, http.StatusBadGateway, "netlify_relay_unusable", "Netlify returned a site URL that cannot be used as a relay")
		return
	}
	pool := proxy.Pool{Name: result.Name, Enabled: true, Strategy: proxy.StrategyRoundRobin, Kind: proxy.KindNetlify, Members: []proxy.Member{{URL: relay, Enabled: true}}}
	saved, err := h.proxyStore.PutPool(r.Context(), pool)
	if errors.Is(err, proxy.ErrConflict) {
		// Pool names are unique here; the relay is live, so say where.
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "proxy_pool_name_conflict", "message": "the relay was deployed, but a proxy pool named " + result.Name + " already exists; add the relay to a pool by hand"}, "deployUrl": relay})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "proxy_pool_save_failed", "the relay was deployed but its proxy pool could not be saved; deployed at "+relay)
		return
	}
	revision, ok := h.publishProxyPools(w, r.Context(), "relay deployed and pool saved but the runtime snapshot could not be refreshed")
	if !ok {
		return
	}
	slog.Info("netlify relay deployed", "pool", saved.ID, "name", saved.Name)
	writeJSON(w, http.StatusCreated, map[string]any{"proxyPool": h.proxyPoolResponse(r.Context(), saved, revision), "deployUrl": relay})
}

func netlifyErrorCode(status int) string {
	switch {
	case status == http.StatusConflict:
		return "netlify_site_name_taken"
	case status == http.StatusBadGateway:
		return "netlify_bad_answer"
	case strings.HasPrefix(http.StatusText(status), "Unauth") || status == http.StatusForbidden:
		return "netlify_denied"
	}
	return "netlify_failed"
}
