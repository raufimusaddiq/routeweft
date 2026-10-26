package api

import (
	"context"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// codexProviderID is the one provider whose built-in node may hold several
// connections with one identity (migration v12, #241).
const codexProviderID = "codex"

// codexMatch mirrors the reference's Codex duplicate lookup
// (connectionsRepo.js:227-242): an oauth connection with an email updates the
// existing oauth connection with that email only when both carry the same
// non-empty chatgptAccountId. Any other case, including a missing account id on
// either side, inserts. The reference's access_token connections (the Codex
// token import) are never an existing match (connectionsRepo.js:232,264); here
// they are the rows import-token stores with authMethod "access_token". The
// reference has one pool per provider, so every built-in Codex node is searched
// (a database may hold more than one from concurrent first sign-ins) and no
// Generic node is. Callers hold codexSaveMu across the lookup and the write it
// decides.
func (h *Handler) codexMatch(ctx context.Context, identity string, data map[string]string) (credentials.Connection, bool, error) {
	account := data["chatgptAccountId"]
	if identity == "" || account == "" {
		return credentials.Connection{}, false, nil
	}
	nodes, err := h.opts.Credentials.ListNodes(ctx)
	if err != nil {
		return credentials.Connection{}, false, err
	}
	builtin := map[string]bool{}
	for _, node := range nodes {
		if node.Kind == credentials.NodeBuiltin && node.ProviderID == codexProviderID {
			builtin[node.ID] = true
		}
	}
	existing, err := h.opts.Credentials.ListConnections(ctx, codexProviderID)
	if err != nil {
		return credentials.Connection{}, false, err
	}
	for _, connection := range existing {
		if !builtin[connection.NodeID] || connection.AuthKind != credentials.AuthOAuth || connection.Secret.ProviderData["authMethod"] == "access_token" {
			continue
		}
		if connection.Identity == identity && connection.Secret.ProviderData["chatgptAccountId"] == account {
			return connection, true, nil
		}
	}
	return credentials.Connection{}, false, nil
}

// codexMergedSecret is {...existing, ...incoming} (connectionsRepo.js:283) over
// the credential: each token, the id token and the expiry the incoming
// credential carries replace the stored ones, and its providerSpecificData
// replaces the stored object whole rather than key by key. What it leaves out
// is kept.
func codexMergedSecret(existing, incoming credentials.Secret) credentials.Secret {
	merged := existing
	if incoming.AccessToken != "" {
		merged.AccessToken = incoming.AccessToken
	}
	if incoming.RefreshToken != "" {
		merged.RefreshToken = incoming.RefreshToken
	}
	if incoming.IDToken != "" {
		merged.IDToken = incoming.IDToken
	}
	if !incoming.Expiry.IsZero() {
		merged.Expiry = incoming.Expiry
	}
	if len(incoming.ProviderData) > 0 {
		merged.ProviderData = incoming.ProviderData
	}
	return merged
}

// clearConnectionHealth is the reference's health reset on a row updated with
// testStatus "active", which every Codex sign-in and bulk item sends
// (connectionsRepo.js:64-82): lastError, rateLimitedUntil, backoff and model
// locks are cleared. Routeweft keeps that state as the connection's cooldown.
func (h *Handler) clearConnectionHealth(connectionID string) {
	if h.opts.Runtime != nil {
		if state := h.opts.Runtime.State(); state != nil {
			state.ClearCooldown(connectionID)
		}
	}
}

// codexIdentityTaken reports whether a connection other than id on the built-in
// Codex node holds identity. The manual connection form is Routeweft's own (the
// reference's manual create stores only apikey and cookie rows,
// providers/route.js:179-181), so it keeps the refusal it had before v12.
func (h *Handler) codexIdentityTaken(ctx context.Context, nodeID, id, identity string) (bool, error) {
	existing, err := h.opts.Credentials.ListConnections(ctx, codexProviderID)
	if err != nil {
		return false, err
	}
	for _, connection := range existing {
		if connection.NodeID == nodeID && connection.ID != id && connection.Identity == identity {
			return true, nil
		}
	}
	return false, nil
}
