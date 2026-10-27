package api

import (
	"context"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
)

// codexProviderID is the one provider whose built-in node may hold several
// connections with one identity (migration v12, #241).
const codexProviderID = "codex"

// codexMatch mirrors the reference's Codex duplicate lookup
// (connectionsRepo.js:227-242): an oauth connection with an email updates the
// existing oauth connection with that email only when both carry the same
// non-empty chatgptAccountId. Any other case, including a missing account id on
// either side, inserts. The reference's access_token connections (the Codex
// token import and a raw token pasted as a sign-in code) are never an existing
// match (connectionsRepo.js:232,264; codexTokenOnly). The reference has one pool per provider, so every
// built-in Codex node is searched (a database may hold more than one from
// concurrent first sign-ins) and no Generic node is. When more than one row
// matches, the reference's all.find takes the first in table order, which is
// creation order here. Callers hold connectionMu across the lookup and the
// write it decides.
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
	matches := map[string]credentials.Connection{}
	for _, connection := range existing {
		if !builtin[connection.NodeID] || connection.AuthKind != credentials.AuthOAuth || codexTokenOnly(connection.Secret) {
			continue
		}
		if connection.Identity == identity && connection.Secret.ProviderData["chatgptAccountId"] == account {
			matches[connection.ID] = connection
		}
	}
	if len(matches) == 0 {
		return credentials.Connection{}, false, nil
	}
	order, err := h.opts.Credentials.ConnectionCreationOrder(ctx, codexProviderID)
	if err != nil {
		return credentials.Connection{}, false, err
	}
	for _, id := range order {
		if connection, ok := matches[id]; ok {
			return connection, true, nil
		}
	}
	return credentials.Connection{}, false, nil
}

// codexTokenOnly reports whether a stored Codex connection is the reference's
// access_token connection, which is never the existing side of a match
// (connectionsRepo.js:232,264). Onboarding records it (Secret.AuthType): a token
// import or pasted raw token is access_token, a sign-in or bulk item oauth even
// when the item's providerSpecificData says authMethod access_token
// (bulk-import/route.js:106-108). A connection stored before the record existed
// has none, and one with authMethod access_token could have come from either a
// token import or a bulk item, which store the same payload; it is treated as a
// token import, so a sign-in adds a row beside it rather than rewriting it.
func codexTokenOnly(secret credentials.Secret) bool {
	switch secret.AuthType {
	case credentials.AuthTypeAccessToken:
		return true
	case credentials.AuthTypeOAuth:
		return false
	}
	return secret.ProviderData["authMethod"] == "access_token"
}

// codexMergedSecret is {...existing, ...incoming} (connectionsRepo.js:283) over
// the credential. The access token is always sent and replaces the stored one;
// the refresh token, id token and expiry replace theirs when the incoming data
// carries the key, even with no value, which clears it; and providerSpecificData
// replaces the stored object whole rather than key by key. A sign-in carries
// every key (codex.js:51-55, [provider]/[action]/route.js:474-477), a bulk item
// only those it names. What it does not carry is kept.
func codexMergedSecret(existing, incoming credentials.Secret, carries onboarding.Carries) credentials.Secret {
	merged := existing
	merged.AccessToken = incoming.AccessToken
	if carries.RefreshToken {
		merged.RefreshToken = incoming.RefreshToken
	}
	if carries.IDToken {
		merged.IDToken = incoming.IDToken
	}
	if carries.Expiry {
		merged.Expiry = incoming.Expiry
	}
	if len(incoming.ProviderData) > 0 {
		merged.ProviderData = incoming.ProviderData
	}
	if incoming.AuthType != "" {
		merged.AuthType = incoming.AuthType
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

// codexLastPriority is a priority after every other Codex connection's, where
// the reference sorts a row whose priority is zero or null (connectionsRepo.js:159).
func (h *Handler) codexLastPriority(ctx context.Context, id string) (int, error) {
	existing, err := h.opts.Credentials.ListConnections(ctx, codexProviderID)
	if err != nil {
		return 0, err
	}
	last := 0
	for _, connection := range existing {
		if connection.ID != id && connection.Priority >= last {
			last = connection.Priority + 1
		}
	}
	return last, nil
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
