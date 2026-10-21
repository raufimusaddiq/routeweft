package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/codex"
)

func (h *Handler) handleCodexResetCredits(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil || h.opts.CredentialRegistry == nil {
		writeError(w, 503, "quota_unavailable", "provider credentials are not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	connection, err := h.opts.Credentials.GetConnection(ctx, r.PathValue("id"))
	if errors.Is(err, credentials.ErrNotFound) {
		writeError(w, 404, "connection_not_found", "connection not found")
		return
	}
	if err != nil {
		writeError(w, 500, "quota_unavailable", "connection could not be read")
		return
	}
	if connection.ProviderID != "codex" || connection.AuthKind != credentials.AuthOAuth && connection.AuthKind != credentials.AuthAPIKey {
		writeError(w, 400, "invalid_connection", "reset credits require a Codex token connection")
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
	account := secret.ProviderData["workspaceId"]
	if account == "" {
		account = secret.ProviderData["chatgptAccountId"]
	}
	if account == "" {
		account = secret.ProviderData["accountId"]
	}
	if account == "" {
		account = connection.AccountID
	}
	client := codex.UsageClient{Client: h.opts.OAuthClient, AccountID: account}
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
	result, err := client.ConsumeResetCredit(ctx, secret.AccessToken, uuid.NewString())
	if err != nil {
		writeError(w, 502, "reset_failed", "Codex reset credit outcome is unavailable; refresh credits before trying again")
		return
	}
	status := http.StatusOK
	if result.NoCredit {
		status = http.StatusConflict
	} else if !result.OK {
		status = http.StatusBadGateway
	}
	result.Message = ""
	writeJSON(w, status, result)
}
