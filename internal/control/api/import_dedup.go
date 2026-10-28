package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
)

// errImportConflict means an import names an account whose identity is already
// stored on the provider's built-in node, but the reference would have kept both
// rows: another credential mode, another username, or a route that never dedups
// (#218). The built-in node holds one connection per identity outside Codex
// (migration v12), so the import is refused instead of written over that row.
var errImportConflict = errors.New("an account with this identity is already connected with a different credential mode or username")

// importConflictMessage is what the operator is told about errImportConflict.
const importConflictMessage = "this account is already connected, with a different credential mode or username; edit or delete that connection first"

// importMatch mirrors the reference's createProviderConnection duplicate lookup
// for every provider but Codex, which has its own rule (codexMatch):
//
//   - an oauth credential with an email updates the stored oauth connection with
//     that email, when the usernames agree: both absent, or both present and
//     equal (connectionsRepo.js:228-261);
//   - a Xiaomi MiMo API key updates the stored key connection with the same
//     account (xiaomi-mimo/api-key/route.js:66-95);
//   - any other mode never dedups there (a Kiro API key, an iFlow cookie), and an
//     oauth row with another username stays a second row.
//
// The last two keep a second row in the reference. Routeweft's built-in node
// holds one connection per identity, so they answer errImportConflict instead of
// the store's UNIQUE error. Only the built-in node is searched: a Generic
// Provider node is a separate pool.
func (h *Handler) importMatch(ctx context.Context, providerID, identity string, authKind credentials.AuthKind, data map[string]string) (credentials.Connection, bool, error) {
	if identity == "" || providerID == codexProviderID {
		return credentials.Connection{}, false, nil
	}
	node, err := h.builtinNode(ctx, providerID)
	if err != nil {
		return credentials.Connection{}, false, err
	}
	existing, err := h.opts.Credentials.ListConnections(ctx, providerID)
	if err != nil {
		return credentials.Connection{}, false, err
	}
	for _, connection := range existing {
		if connection.NodeID != node.ID || connection.Identity != identity {
			continue
		}
		if connection.AuthKind != authKind {
			return credentials.Connection{}, false, errImportConflict
		}
		switch {
		case authKind == credentials.AuthOAuth:
			incoming, stored := strings.TrimSpace(data["username"]), strings.TrimSpace(connection.Secret.ProviderData["username"])
			if incoming != stored {
				return credentials.Connection{}, false, errImportConflict
			}
			return connection, true, nil
		case authKind == credentials.AuthAPIKey && providerID == "xiaomi-mimo":
			return connection, true, nil
		}
		return credentials.Connection{}, false, errImportConflict
	}
	return credentials.Connection{}, false, nil
}

// importedSecret is the stored credential after a duplicate import. The
// reference's oauth update is {...existing, ...item}: the item's tokens, expiry
// and providerSpecificData replace the stored ones whole, so a re-import without
// a refresh token does not keep a stale one. Xiaomi's key route merges its
// providerSpecificData key by key instead, keeping a stored passToken the new
// import does not carry (route.js:76-88).
func importedSecret(providerID string, existing, update credentials.Secret) credentials.Secret {
	if providerID == "xiaomi-mimo" {
		return mergeConnectionSecret(existing, update)
	}
	if update.AuthType == "" {
		update.AuthType = existing.AuthType
	}
	return update
}

// saveImportedDuplicate stores an import that matches a connection already on
// the provider (importMatch) onto that connection, keeping its id, name,
// priority, enabled flag and proxy pool. handled is false when the import names
// no stored account, and the caller adds it; a failure is answered here, and a
// save (ok) returns the body for the caller to send. The caller holds
// connectionMu, so a concurrent import of the same account cannot insert between
// the lookup and the write.
func (h *Handler) saveImportedDuplicate(w http.ResponseWriter, r *http.Request, providerID, identity string, result onboarding.Result) (saved map[string]any, handled, ok bool) {
	authKind := result.AuthKind
	if authKind == "" {
		authKind = credentials.AuthOAuth
	}
	match, found, err := h.importMatch(r.Context(), providerID, identity, authKind, result.Secret.ProviderData)
	if errors.Is(err, errImportConflict) {
		writeError(w, http.StatusConflict, "identity_conflict", importConflictMessage)
		return nil, true, false
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "connection_save_failed", "provider connection could not be saved; check its identity")
		return nil, true, false
	}
	if !found {
		return nil, false, false
	}
	if result.Secret.Empty() {
		writeError(w, http.StatusBadGateway, "oauth_exchange_failed", "the provider returned no usable credential; start again")
		return nil, true, false
	}
	if spec, _ := h.providerSpec(providerID); !authKindPermitted(spec, authKind) {
		writeError(w, http.StatusBadRequest, "invalid_auth_kind", "this provider does not accept the credential mode this sign-in produced")
		return nil, true, false
	}
	match.Secret, match.Lifecycle = result.Secret, onboardedLifecycle(oauthFlow{imported: true}, result, true)
	connection, persisted, err := h.publishMergedConnection(r.Context(), match, func(existing, update credentials.Secret) credentials.Secret {
		return importedSecret(providerID, existing, update)
	})
	if err != nil {
		if persisted {
			writeError(w, http.StatusInternalServerError, "connection_publish_failed", "connection saved but could not be published to the runtime")
		} else {
			writeError(w, http.StatusBadRequest, "connection_save_failed", "provider connection could not be saved; check its identity")
		}
		return nil, true, false
	}
	h.clearConnectionHealth(connection.ID)
	saved = onboardedJSON(connection, onboardedUpdated)
	h.reportLifecycle(r.Context(), saved)
	return saved, true, true
}

// importExpiry describes a stored credential that had already expired when it
// was imported: the reference stores it as given (bulk-import/route.js:78-82), so
// the import succeeds, and the answer says so. refreshable is whether a refresh
// token came with it, without which the account must sign in again.
func importExpiry(secret credentials.Secret, now time.Time) (expired, refreshable bool) {
	if secret.Expiry.IsZero() || secret.Expiry.After(now) {
		return false, false
	}
	return true, strings.TrimSpace(secret.RefreshToken) != ""
}
