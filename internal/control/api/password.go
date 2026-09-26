package api

import (
	"errors"
	"net/http"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
)

func (h *Handler) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if h.opts.Accounts == nil || h.opts.Sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "admin authentication is not configured")
		return
	}
	session, ok := h.sessionFor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "admin session is required")
		return
	}
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	if body.NewPassword == "" {
		writeError(w, http.StatusBadRequest, "invalid_password", "new password must not be empty")
		return
	}
	key := clientKey(r)
	if !h.throttler.allow(key) {
		writeError(w, http.StatusTooManyRequests, "too_many_attempts", "too many failed password attempts; try again later")
		return
	}
	account, valid, err := h.opts.Accounts.Authenticate(r.Context(), session.Username, body.CurrentPassword)
	if errors.Is(err, adminauth.ErrNoAdmin) || err == nil && (!valid || account.ID != session.AdminID) {
		h.throttler.fail(key)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "auth_error", "admin password could not be verified")
		return
	}
	if err := h.opts.Accounts.ChangePassword(r.Context(), session.AdminID, body.NewPassword); err != nil {
		writeError(w, http.StatusInternalServerError, "password_update_failed", "admin password could not be changed")
		return
	}
	h.throttler.succeed(key)
	h.opts.Sessions.RevokeAdmin(session.AdminID)
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/admin", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}
