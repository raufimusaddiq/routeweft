package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/proxy"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

const (
	maxProxyPoolMembers  = 50
	proxyTestTimeout     = 15 * time.Second
	proxyTestConcurrency = 4
	// defaultProxyTestTarget is a tiny public endpoint that answers 204. The
	// destination goes through the same SSRF guard as every other outbound URL.
	defaultProxyTestTarget = "https://www.gstatic.com/generate_204"
)

type proxyPoolRequest struct {
	Name     string             `json:"name"`
	Enabled  *bool              `json:"enabled"`
	Strategy string             `json:"strategy"`
	Members  []proxyMemberInput `json:"members"`
}

// proxyMemberInput identifies a member either by a new url or, on update, by the
// id of a stored member. Responses redact credentials, so an id with no url keeps
// the stored URL (and its credentials) instead of forcing the client to resend it.
type proxyMemberInput struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Enabled *bool  `json:"enabled"`
}

func (h *Handler) handlePutProxyPool(w http.ResponseWriter, r *http.Request) {
	if h.proxyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy_pools_unavailable", "proxy pool management is not configured")
		return
	}
	var body proxyPoolRequest
	if !decodeJSON(w, r, 1<<20, &body) {
		return
	}
	if len(body.Members) > maxProxyPoolMembers {
		writeError(w, http.StatusBadRequest, "invalid_proxy_pool", "a proxy pool holds at most 50 members")
		return
	}
	id := r.PathValue("id")
	created := id == ""
	pool := proxy.Pool{ID: id, Name: body.Name, Enabled: true, Strategy: proxy.Strategy(strings.TrimSpace(body.Strategy))}
	stored := map[string]proxy.Member{}
	if !created {
		existing, err := h.proxyStore.GetPool(r.Context(), id)
		if errors.Is(err, proxy.ErrNotFound) {
			writeError(w, http.StatusNotFound, "proxy_pool_not_found", "proxy pool was not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "proxy_pool_read_failed", "proxy pool could not be read")
			return
		}
		pool.Enabled = existing.Enabled
		if pool.Strategy == "" {
			pool.Strategy = existing.Strategy
		}
		for _, member := range existing.Members {
			stored[member.ID] = member
		}
	}
	if body.Enabled != nil {
		pool.Enabled = *body.Enabled
	}
	seenIDs := map[string]bool{}
	for position, input := range body.Members {
		member := proxy.Member{ID: strings.TrimSpace(input.ID), URL: strings.TrimSpace(input.URL), Position: position, Enabled: true}
		if member.ID != "" {
			previous, known := stored[member.ID]
			if !known || seenIDs[member.ID] {
				writeError(w, http.StatusBadRequest, "invalid_proxy_pool", "member id is unknown or repeated")
				return
			}
			seenIDs[member.ID] = true
			member.Enabled = previous.Enabled
			member.URL = resolveMemberURL(member.URL, previous.URL)
		} else if member.URL == "" {
			writeError(w, http.StatusBadRequest, "invalid_proxy_pool", "every member needs a url or the id of a stored member")
			return
		}
		if input.Enabled != nil {
			member.Enabled = *input.Enabled
		}
		normalized, err := proxy.ValidateURL(member.URL)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_proxy_pool", err.Error())
			return
		}
		if err := h.checkProxyHost(normalized); err != nil {
			writeError(w, http.StatusBadRequest, "proxy_host_blocked", "proxy host is not allowed by the outbound network policy")
			return
		}
		pool.Members = append(pool.Members, member)
	}
	saved, err := h.proxyStore.PutPool(r.Context(), pool)
	switch {
	case errors.Is(err, proxy.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_proxy_pool", err.Error())
		return
	case errors.Is(err, proxy.ErrConflict):
		writeError(w, http.StatusConflict, "proxy_pool_name_conflict", "a proxy pool with this name already exists")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "proxy_pool_save_failed", "proxy pool could not be saved")
		return
	}
	revision, ok := h.publishProxyPools(w, r.Context(), "proxy pool saved but the runtime snapshot could not be refreshed")
	if !ok {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, h.proxyPoolResponse(r.Context(), saved, revision))
}

func (h *Handler) handleGetProxyPool(w http.ResponseWriter, r *http.Request) {
	if h.proxyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy_pools_unavailable", "proxy pool management is not configured")
		return
	}
	pool, err := h.proxyStore.GetPool(r.Context(), r.PathValue("id"))
	if errors.Is(err, proxy.ErrNotFound) {
		writeError(w, http.StatusNotFound, "proxy_pool_not_found", "proxy pool was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "proxy_pool_read_failed", "proxy pool could not be read")
		return
	}
	writeJSON(w, http.StatusOK, h.proxyPoolResponse(r.Context(), pool, 0))
}

func (h *Handler) handleDeleteProxyPool(w http.ResponseWriter, r *http.Request) {
	if h.proxyStore == nil || h.opts.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy_pools_unavailable", "proxy pool management is not configured")
		return
	}
	id := r.PathValue("id")
	// Deleting a bound pool would silently send those connections direct (or via
	// the global proxy), so the store refuses atomically until they are rebound.
	switch bound, err := h.proxyStore.DeletePoolIfUnbound(r.Context(), id); {
	case errors.Is(err, proxy.ErrInUse):
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "proxy_pool_in_use", "message": "proxy pool is bound to provider connections; rebind them first", "connections": bound}})
		return
	case errors.Is(err, proxy.ErrNotFound):
		writeError(w, http.StatusNotFound, "proxy_pool_not_found", "proxy pool was not found")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "proxy_pool_delete_failed", "proxy pool could not be deleted")
		return
	}
	revision, ok := h.publishProxyPools(w, r.Context(), "proxy pool deleted but the runtime snapshot could not be refreshed")
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true, "configRevision": revision})
}

// publishProxyPools republishes compiled pools after a durable mutation and
// emits the config event. It writes the error response itself.
func (h *Handler) publishProxyPools(w http.ResponseWriter, ctx context.Context, failure string) (uint64, bool) {
	var revision uint64
	if h.opts.PoolBindings == nil {
		return 0, true
	}
	snapshot, err := h.opts.PoolBindings.RefreshPoolBindings(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "proxy_pool_publish_failed", failure)
		return 0, false
	}
	revision = snapshot.ConfigRevision()
	if h.opts.Events != nil {
		h.opts.Events.Publish("config.updated", map[string]any{"resource": "proxy-pool", "configRevision": revision})
	}
	return revision, true
}

// proxyPoolResponse is the read model for one pool. Member URLs are redacted
// the same way as the list endpoint: no userinfo, query, or fragment.
func (h *Handler) proxyPoolResponse(ctx context.Context, pool proxy.Pool, revision uint64) map[string]any {
	members := make([]map[string]any, 0, len(pool.Members))
	for _, member := range pool.Members {
		members = append(members, map[string]any{"id": member.ID, "position": member.Position, "url": safeURL(member.URL), "enabled": member.Enabled})
	}
	response := map[string]any{"id": pool.ID, "name": pool.Name, "enabled": pool.Enabled, "strategy": string(pool.Strategy), "members": members}
	if h.opts.DB != nil {
		var createdAt, updatedAt string
		if err := h.opts.DB.QueryRowContext(ctx, "SELECT created_at,updated_at FROM proxy_pools WHERE id=?", pool.ID).Scan(&createdAt, &updatedAt); err == nil {
			response["createdAt"] = createdAt
			response["updatedAt"] = updatedAt
		}
	}
	if revision != 0 {
		response["configRevision"] = revision
	}
	return response
}

// proxyTestResult is a classified connectivity outcome. It never carries raw
// error text, because transport errors can embed proxy addresses.
type proxyTestResult struct {
	OK         bool   `json:"ok"`
	Class      string `json:"class,omitempty"`
	Message    string `json:"message,omitempty"`
	StatusCode int    `json:"statusCode,omitempty"`
	LatencyMs  int64  `json:"latencyMs"`
}

type proxyMemberTestResult struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
	URL      string `json:"url"`
	Enabled  bool   `json:"enabled"`
	proxyTestResult
}

var proxyFailureMessages = map[string]string{
	"blocked":     "proxy or destination is not allowed by the outbound network policy",
	"dns":         "proxy host name could not be resolved",
	"refused":     "proxy refused the connection",
	"timeout":     "proxy did not respond in time",
	"tls":         "TLS handshake with the proxy or destination failed",
	"proxy_auth":  "proxy rejected the credentials",
	"proxy_error": "proxy could not reach the destination",
	"unreachable": "proxy could not be reached",
}

func (h *Handler) handleTestProxyPool(w http.ResponseWriter, r *http.Request) {
	if h.proxyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy_pools_unavailable", "proxy pool management is not configured")
		return
	}
	var body struct {
		MemberID string `json:"memberId"`
	}
	if err := decodeOptionalJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is not valid JSON")
		return
	}
	pool, err := h.proxyStore.GetPool(r.Context(), r.PathValue("id"))
	if errors.Is(err, proxy.ErrNotFound) {
		writeError(w, http.StatusNotFound, "proxy_pool_not_found", "proxy pool was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "proxy_pool_read_failed", "proxy pool could not be read")
		return
	}
	members := pool.Members
	if body.MemberID != "" {
		members = nil
		for _, member := range pool.Members {
			if member.ID == body.MemberID {
				members = []proxy.Member{member}
			}
		}
		if members == nil {
			writeError(w, http.StatusNotFound, "proxy_member_not_found", "proxy pool member was not found")
			return
		}
	}
	// One deadline for the whole fan-out, scaled to the number of waves so a
	// member waiting for a slot is never cut off before it is dialed.
	waves := (len(members) + proxyTestConcurrency - 1) / proxyTestConcurrency
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(waves+1)*proxyTestTimeout)
	defer cancel()
	results := make([]proxyMemberTestResult, len(members))
	var wg sync.WaitGroup
	slots := make(chan struct{}, proxyTestConcurrency)
	for i, member := range members {
		results[i] = proxyMemberTestResult{ID: member.ID, Position: member.Position, URL: safeURL(member.URL), Enabled: member.Enabled}
		wg.Add(1)
		slots <- struct{}{}
		go func(i int, member proxy.Member) {
			defer wg.Done()
			defer func() { <-slots }()
			results[i].proxyTestResult = h.testProxy(ctx, member.URL)
		}(i, member)
	}
	wg.Wait()
	healthy, enabled := 0, 0
	for _, result := range results {
		if result.Enabled {
			enabled++
			if result.OK {
				healthy++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"poolId": pool.ID, "ok": enabled > 0 && healthy == enabled, "members": results})
}

// handleProxyTest tests one proxy: the url in the body, or the configured global
// outbound proxy when none is given.
func (h *Handler) handleProxyTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := decodeOptionalJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is not valid JSON")
		return
	}
	raw := strings.TrimSpace(body.URL)
	if raw == "" {
		if h.opts.Runtime == nil {
			writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "runtime snapshot is not available")
			return
		}
		snapshot, err := h.opts.Runtime.Load()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "runtime snapshot is not available")
			return
		}
		var enabled bool
		enabled, raw, _ = snapshot.OutboundProxy()
		if strings.TrimSpace(raw) == "" {
			writeError(w, http.StatusBadRequest, "no_proxy_configured", "no proxy url was given and no global outbound proxy is configured")
			return
		}
		if !enabled {
			writeError(w, http.StatusBadRequest, "global_proxy_disabled", "the global outbound proxy is configured but disabled")
			return
		}
	}
	normalized, err := proxy.ValidateURL(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_proxy_url", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.testProxy(r.Context(), normalized))
}

// testProxy sends one GET through the proxy using a shared client keyed by its
// transport policy and releases it afterwards.
func (h *Handler) testProxy(ctx context.Context, proxyURL string) proxyTestResult {
	if err := h.checkProxyHost(proxyURL); err != nil {
		return failedProxyTest("blocked", 0)
	}
	policy := transport.ProxyPolicy{Enabled: true, ConnectionProxyURL: proxyURL, AllowPrivate: h.opts.AllowPrivateUpstreams}
	client, err := h.proxyClients.Client(policy)
	if err != nil {
		return failedProxyTest("unreachable", 0)
	}
	// Release on the way out: stored and caller-supplied URLs alike must not
	// pin a transport for the process lifetime.
	defer h.proxyClients.Release(policy)
	ctx, cancel := context.WithTimeout(ctx, proxyTestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, h.proxyTestTarget, nil)
	if err != nil {
		return failedProxyTest("unreachable", 0)
	}
	started := time.Now()
	// The client never follows redirects, so a redirect cannot steer the test
	// to a destination the SSRF guard has not seen.
	response, err := client.Do(request)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		return failedProxyTest(classifyProxyError(err), elapsed)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	switch response.StatusCode {
	case http.StatusProxyAuthRequired:
		result := failedProxyTest("proxy_auth", elapsed)
		result.StatusCode = response.StatusCode
		return result
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		result := failedProxyTest("proxy_error", elapsed)
		result.StatusCode = response.StatusCode
		return result
	}
	return proxyTestResult{OK: true, StatusCode: response.StatusCode, LatencyMs: elapsed}
}

func failedProxyTest(class string, latency int64) proxyTestResult {
	return proxyTestResult{Class: class, Message: proxyFailureMessages[class], LatencyMs: latency}
}

func classifyProxyError(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "non-public address"):
		return "blocked"
	case strings.Contains(message, "resolution failed"):
		return "dns"
	case strings.Contains(message, "Proxy Authentication Required"):
		return "proxy_auth"
	}
	var dnsErr *net.DNSError
	var verifyErr *tls.CertificateVerificationError
	var recordErr tls.RecordHeaderError
	var netErr net.Error
	switch {
	case errors.As(err, &dnsErr):
		return "dns"
	case errors.As(err, &verifyErr), errors.As(err, &recordErr):
		return "tls"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	}
	return "unreachable"
}

// resolveMemberURL decides what URL a stored member keeps on update. Responses
// redact credentials, so a client echoing the redacted URL back means
// "unchanged" even if it reformatted it (trailing slash, explicit default
// port, host case). A URL with userinfo or a query is an explicit replacement,
// and an empty userinfo section (http://@host:3128) explicitly removes the
// stored credentials.
func resolveMemberURL(submitted, stored string) string {
	if submitted == "" {
		return stored
	}
	parsed, err := url.Parse(submitted)
	if err != nil {
		return submitted
	}
	if parsed.User != nil && parsed.User.Username() == "" {
		if _, hasPassword := parsed.User.Password(); !hasPassword {
			parsed.User = nil
			return parsed.String()
		}
	}
	if parsed.User != nil || parsed.RawQuery != "" {
		return submitted
	}
	previous, err := url.Parse(stored)
	if err == nil && sameEndpoint(parsed, previous) {
		return stored
	}
	return submitted
}

// sameEndpoint compares what the redacted form exposes: scheme, host, effective
// port, and path.
func sameEndpoint(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) &&
		effectivePort(a) == effectivePort(b) && strings.TrimRight(a.EscapedPath(), "/") == strings.TrimRight(b.EscapedPath(), "/")
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "socks5", "socks5h":
		return "1080"
	}
	return "80"
}

// checkProxyHost applies the shared outbound SSRF policy to a proxy's address.
// Only scheme and host are validated, because the shared validator rejects
// userinfo and proxy URLs may legitimately carry credentials.
func (h *Handler) checkProxyHost(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	_, err = h.validateOutboundURL((&url.URL{Scheme: "http", Host: parsed.Host}).String())
	return err
}

// decodeOptionalJSON decodes a bounded JSON body, accepting an empty one.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, target any) error {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(target)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
