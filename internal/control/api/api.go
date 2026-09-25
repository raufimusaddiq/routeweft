// Package api owns the versioned operator control API (BDR-014). Every route is
// session-gated: an unauthenticated admin API must never be exposed (RUNBOOK).
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/buildinfo"
	controlevents "github.com/raufimusaddiq/routeweft/internal/control/events"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// SettingsStore reads and mutates durable settings through the runtime manager's
// compile-before-commit protocol. It is satisfied by the runtime manager.
type SettingsStore interface {
	Settings() map[string]string
	SetSettings(ctx context.Context, values map[string]string, remove []string) (uint64, error)
}

// Options configure the control API.
type Options struct {
	Accounts       *adminauth.Store
	Sessions       *adminauth.SessionManager
	Settings       SettingsStore
	DB             *sql.DB
	Runtime        RuntimeReader
	Providers      []registry.Spec
	Telemetry      TelemetryReader
	Events         *controlevents.Bus
	Logs           *LogBuffer
	ActiveRequests func() int64
	Ready          func() bool
	Build          buildinfo.Info
}

// RuntimeReader exposes immutable active configuration and process-local state.
type RuntimeReader interface {
	Load() (*runtime.RuntimeSnapshot, error)
	State() *runtime.RuntimeState
}

// TelemetryReader exposes read-only batcher health and counters.
type TelemetryReader interface {
	Enabled() bool
	Health() error
	Lost() uint64
	LostDiagnostics() uint64
	Written() uint64
}

// sessionCookieName is the dashboard session cookie. It is HttpOnly and
// SameSite=Strict; Secure is set when the request arrived over TLS.
const sessionCookieName = "routeweft_admin_session"

// Handler serves the authenticated control API.
type Handler struct {
	opts      Options
	throttler *throttler
}

// New builds the control API handler.
func New(opts Options) *Handler { return &Handler{opts: opts, throttler: newThrottler()} }

// Attach registers the admin routes on the shared mux.
func (h *Handler) Attach(mux *http.ServeMux) {
	mux.HandleFunc("POST /admin/v1/auth/login", h.requireSameOrigin(h.handleLogin))
	mux.HandleFunc("POST /admin/v1/auth/logout", h.requireSameOrigin(h.handleLogout))
	mux.HandleFunc("GET /admin/v1/auth/session", h.handleSession)
	mux.HandleFunc("GET /admin/v1/settings", h.requireSession(h.handleGetSettings))
	mux.HandleFunc("PATCH /admin/v1/settings", h.requireSession(h.handlePatchSettings))
	for _, path := range []string{"overview", "providers", "provider-nodes", "connections", "models", "aliases", "pricing", "combos", "proxy-pools", "keys", "usage", "requests", "quota", "token-saver", "systemone"} {
		mux.Handle("GET /admin/v1/"+path, h.requireSessionHandler(h.readModel(path)))
	}
	mux.Handle("GET /admin/v1/events", h.requireSessionHandler(http.HandlerFunc(h.handleEvents)))
	mux.Handle("GET /admin/v1/logs", h.requireSessionHandler(http.HandlerFunc(h.handleLogs)))
}

func (h *Handler) requireSessionHandler(next http.Handler) http.Handler {
	return h.requireSession(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, r) })
}

// requireSession wraps a handler with session authentication.
func (h *Handler) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && !sameOriginRequest(r) {
			writeError(w, http.StatusForbidden, "cross_origin", "cross-origin admin mutation rejected")
			return
		}
		if _, ok := h.sessionFor(r); !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "admin session is required")
			return
		}
		next(w, r)
	}
}

func (h *Handler) requireSameOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginRequest(r) {
			writeError(w, http.StatusForbidden, "cross_origin", "cross-origin admin request rejected")
			return
		}
		next(w, r)
	}
}

// sameOriginRequest rejects browser-marked cross-site requests and mismatched
// Origin hosts. Requests without browser origin metadata remain usable by local
// operator clients such as curl.
func sameOriginRequest(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && strings.EqualFold(parsed.Host, r.Host)
}

func (h *Handler) sessionFor(r *http.Request) (adminauth.Session, bool) {
	if h.opts.Sessions == nil {
		return adminauth.Session{}, false
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return adminauth.Session{}, false
	}
	return h.opts.Sessions.Lookup(cookie.Value)
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if h.opts.Accounts == nil || h.opts.Sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "admin authentication is not configured")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "login body is not valid JSON")
		return
	}
	throttleKey := clientKey(r)
	if !h.throttler.allow(throttleKey) {
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "too many failed login attempts; try again later")
		return
	}
	account, ok, err := h.opts.Accounts.Authenticate(r.Context(), body.Username, body.Password)
	if errors.Is(err, adminauth.ErrNoAdmin) {
		writeError(w, http.StatusConflict, "no_admin", "no admin account has been provisioned")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "auth_error", "admin authentication failed")
		return
	}
	if !ok {
		// Deliberately identical to a bad password so accounts cannot be enumerated.
		h.throttler.fail(throttleKey)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	h.throttler.succeed(throttleKey)
	session, err := h.opts.Sessions.Create(account)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", "could not create session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session.ID,
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// Always Secure: TLS may terminate at a reverse proxy, and forwarded
		// protocol headers are not trusted to decide cookie security.
		Secure:  true,
		Expires: session.ExpiresAt,
	})
	writeJSON(w, http.StatusOK, map[string]any{"username": session.Username, "expiresAt": session.ExpiresAt})
}

// clientKey identifies the throttle bucket for a request. Forwarded-IP headers
// are not trusted (PRD-SEC-002), so the socket address is used.
func clientKey(r *http.Request) string {
	return r.RemoteAddr
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && h.opts.Sessions != nil {
		h.opts.Sessions.Revoke(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/admin", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleSession(w http.ResponseWriter, r *http.Request) {
	session, ok := h.sessionFor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "admin session is required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": session.Username, "expiresAt": session.ExpiresAt})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// decodeJSON decodes a bounded JSON body into target.
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, target any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body is not valid JSON")
		return false
	}
	return true
}
