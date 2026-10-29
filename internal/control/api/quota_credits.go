package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/codex"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

func (h *Handler) handleCodexResetCredits(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil || h.opts.CredentialRegistry == nil {
		writeError(w, 503, "quota_unavailable", "provider credentials are not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	// Reset credits leave through the connection's proxy
	// (codex-reset-credits/route.js:66-72).
	ctx = transport.WithConnection(ctx, r.PathValue("id"))
	connection, err := h.opts.Credentials.GetConnection(ctx, r.PathValue("id"))
	if errors.Is(err, credentials.ErrNotFound) {
		writeError(w, 404, "connection_not_found", "connection not found")
		return
	}
	if err != nil {
		writeError(w, 500, "quota_unavailable", "connection could not be read")
		return
	}
	if connection.ProviderID != "codex" || connection.AuthKind != credentials.AuthOAuth {
		writeError(w, 400, "invalid_connection", "reset credits require a Codex OAuth connection")
		return
	}
	if r.Method == http.MethodPost {
		var body struct {
			Confirm bool `json:"confirm"`
		}
		if !decodeJSON(w, r, 1024, &body) {
			return
		}
		if !body.Confirm {
			writeError(w, 400, "confirmation_required", "confirm must be true to spend one reset credit")
			return
		}
	}
	if _, err := h.opts.CredentialRegistry.Load(ctx, "codex"); err != nil {
		writeError(w, 503, "quota_unavailable", "credential registry could not be loaded")
		return
	}
	secret, err := h.opts.CredentialRegistry.Secret(ctx, connection.ID)
	if err != nil {
		writeError(w, 502, "credential_unavailable", "Codex credential could not be refreshed")
		return
	}
	client := codex.UsageClient{Client: h.opts.OAuthClient, AccountID: codexAccount(secret, connection)}
	if r.Method == http.MethodGet {
		result, err := client.ResetCredits(ctx, secret.AccessToken)
		if err != nil {
			writeError(w, 502, "quota_unavailable", "Codex reset credits could not be read")
			return
		}
		writeJSON(w, 200, result)
		return
	}
	// Never retry an ambiguous spend; the upstream may already have consumed it.
	// The one retry is after a credential refusal, which is definite (nothing was
	// spent): same redeem id, forced token refresh (codex-reset-credits/route.js:
	// 136-147). The id makes a replay the same spend, never a second one.
	redeemID := uuid.NewString()
	result, err := client.ConsumeResetCredit(ctx, secret.AccessToken, redeemID)
	if errors.Is(err, codex.ErrReauthRequired) {
		if refreshed, refreshErr := h.opts.CredentialRegistry.ForceRefresh(ctx, connection.ID, secret.AccessToken); refreshErr == nil {
			client.AccountID = codexAccount(refreshed, connection)
			result, err = client.ConsumeResetCredit(ctx, refreshed.AccessToken, redeemID)
		}
	}
	if errors.Is(err, codex.ErrReauthRequired) {
		// Codex refused the credential, so nothing was spent. 401 is the admin
		// session's status here, so the refusal is a 403.
		writeError(w, http.StatusForbidden, "reauth_required", "Codex rejected the credential; no reset credit was spent. Reconnect the account")
		return
	}
	if err != nil {
		writeError(w, 502, "reset_failed", "Codex reset credit outcome is unavailable; refresh credits before trying again")
		return
	}
	// A 4xx answer is Codex refusing the spend and keeps its status; any other
	// failure is ambiguous and is a 502 (codex-reset-credits/route.js:42-47).
	status := http.StatusOK
	switch {
	case result.NoCredit:
		status = http.StatusConflict
	case result.OK:
	case result.Status >= 400 && result.Status < 500:
		status = result.Status
	default:
		status = http.StatusBadGateway
	}
	if result.OK && h.opts.QuotaForget != nil {
		// The cached windows and credit count are pre-spend; drop them so the
		// next read does not show stale numbers.
		h.opts.QuotaForget(connection.ID)
	}
	result.Message = ""
	writeJSON(w, status, result)
}

func codexAccount(secret credentials.Secret, connection credentials.Connection) string {
	for _, key := range []string{"workspaceId", "chatgptAccountId", "accountId"} {
		if id := secret.ProviderData[key]; id != "" {
			return id
		}
	}
	return connection.AccountID
}
