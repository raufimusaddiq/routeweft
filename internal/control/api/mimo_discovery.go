package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
)

// MiMo Desktop credential discovery (#296). The pin reads MiMo Desktop's key file
// and its account cookie store from the server user's home directory whenever its
// dashboard opens the Xiaomi modal, and again on every quota read of a connection
// without a stored passToken (auto-import/route.js:52-131,
// [provider]/[action]/route.js:335-361, mimoAccount.js:36-94,181-185). Routeweft
// reads them only with two consents: the host's, given by starting the server
// with host credential discovery allowed (Options.HostCredentialDiscovery), and
// the operator's, given in the request that reads them. What is read is sealed
// into the connection on the server and never returned to the browser. The
// pin's quota-read fallback (mimoAccount.js:181-185) is the per-connection
// renewal below (#299): the operator turns it on for one connection with the
// same two consents, and onboarding.MimoDesktopRenewal does the reading.

const mimoProviderID = "xiaomi-mimo"

// mimoDesktopSource reads MiMo Desktop's files; onboarding.MimoDesktop in
// production, a fixture home in tests.
type mimoDesktopSource interface {
	AuthFilePaths() []string
	ReadAuthFile(limit int64) (string, []byte, error)
	ReadSession(context.Context) map[string]string
}

// hostDiscoveryRefused answers a request that would read host credentials
// without both consents, and reports whether it did. Nothing is read before it
// returns false.
func (h *Handler) hostDiscoveryRefused(w http.ResponseWriter, consent bool) bool {
	if !h.opts.HostCredentialDiscovery {
		writeError(w, http.StatusForbidden, "host_discovery_disabled", "reading MiMo Desktop credentials on this server is turned off; start Routeweft with ROUTEWEFT_ALLOW_HOST_CREDENTIAL_DISCOVERY=true, or import the file instead")
		return true
	}
	if !consent {
		writeError(w, http.StatusBadRequest, "consent_required", "confirm that Routeweft may read MiMo Desktop's credentials on this server")
		return true
	}
	return false
}

// handleOAuthDiscover is the pin's auto-import followed by its api-key import, in
// one server-side step: the key file is found and checked as auto-import does
// (route.js:54-104), Desktop's account session is attached when it can be read
// (:106-121), and the result goes through the same import as a pasted file. The
// answer is the saved connection and where the key came from, never the key or
// the session.
func (h *Handler) handleOAuthDiscover(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "configure ROUTEWEFT_CREDENTIAL_KEY to manage provider connections")
		return
	}
	providerID := r.PathValue("provider")
	flow, ok := h.importFlowFor(providerID)
	spec, inCatalog := h.providerSpec(providerID)
	if providerID != mimoProviderID || !ok || !inCatalog || !specAllowsOAuth(spec) {
		writeError(w, http.StatusNotFound, "oauth_unsupported", "this provider has no host discovery")
		return
	}
	var body struct {
		Name     string `json:"name"`
		Identity string `json:"identity"`
		Consent  bool   `json:"consent"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	// The key file names its account (metadata.uid), so the identity the pasted
	// import asks for up front is learned here, as the pin's email is
	// (api-key/route.js:102).
	asks := flow
	asks.InfersIdentity = true
	name, identity, ok := onboardingAccount(w, asks, body.Name, body.Identity)
	if !ok {
		return
	}
	if _, ok := h.sessionFor(r); !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "admin session is required")
		return
	}
	if h.hostDiscoveryRefused(w, body.Consent) {
		return
	}
	source, raw, err := h.mimoDesktop.ReadAuthFile(oauthMaxImportInput)
	switch {
	case errors.Is(err, onboarding.ErrMimoAuthFileNotFound):
		writeError(w, http.StatusNotFound, "discovery_not_found", "Xiaomi MiMo Desktop auth file not found. Checked:\n"+strings.Join(h.mimoDesktop.AuthFilePaths(), "\n")+"\n\nMake sure Xiaomi MiMo Desktop is installed and you are signed in.")
		return
	case errors.Is(err, onboarding.ErrMimoAuthFileTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "import_too_large", fmt.Sprintf("auth.json is larger than %d KiB, so it is not MiMo's key file", oauthMaxImportInput>>10))
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "discovery_failed", "auth.json could not be read")
		return
	}
	account, problem := mimoAuthFileAccount(raw)
	if problem != "" {
		writeError(w, http.StatusBadRequest, "import_invalid", problem)
		return
	}
	if account["uid"] == "" && identity == "" {
		writeError(w, http.StatusBadRequest, "oauth_identity_unresolved", "auth.json names no account uid; enter the account's identity and try again")
		return
	}
	// Desktop's cookie store holds the account session that reads the weekly
	// quota; without it the key is imported alone, as the pin does while Desktop
	// runs and holds the store locked (route.js:106-108).
	session := h.mimoDesktop.ReadSession(r.Context())
	for key, value := range session {
		account[key] = value
	}
	input, err := json.Marshal(account)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "discovery_failed", "auth.json could not be read")
		return
	}
	saved, ok := h.importCredential(w, r, providerID, flow, name, identity, string(input), nil)
	if !ok {
		return
	}
	saved["discovery"] = map[string]any{"source": source, "accountSession": session != nil}
	writeJSON(w, http.StatusOK, saved)
}

// mimoAuthFileAccount reads auth.json as the pin's auto-import does
// (route.js:74-104) into the body its api-key route takes (XiaomiMimoAuthModal.js
// 63-74), or names what is wrong with it in the pin's words.
func mimoAuthFileAccount(raw []byte) (map[string]string, string) {
	var auth struct {
		Xiaomi *struct {
			Key      json.RawMessage `json:"key"`
			Metadata struct {
				UID      json.RawMessage `json:"uid"`
				Endpoint json.RawMessage `json:"base_url"`
			} `json:"metadata"`
		} `json:"xiaomi"`
	}
	if json.Unmarshal(raw, &auth) != nil {
		return nil, "auth.json is not valid JSON. Please sign in to Xiaomi MiMo Desktop again."
	}
	if auth.Xiaomi == nil || jsonText(auth.Xiaomi.Key) == "" {
		return nil, "No Xiaomi credentials found in auth.json. Please sign in to Xiaomi MiMo Desktop."
	}
	key := strings.TrimSpace(jsonText(auth.Xiaomi.Key))
	if !strings.HasPrefix(key, "sk-") {
		return nil, "Xiaomi key does not appear to be a valid API key (expected sk- prefix)."
	}
	account := map[string]string{"apiKey": key}
	if uid := jsonText(auth.Xiaomi.Metadata.UID); uid != "" {
		account["uid"] = uid
	}
	if endpoint := jsonText(auth.Xiaomi.Metadata.Endpoint); endpoint != "" {
		account["baseUrl"] = endpoint
	}
	// /import refuses a control character in what was pasted; a file's values
	// arrive here decoded, so the same check is made on each of them.
	for _, value := range account {
		if strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return nil, "the credential contains control characters; sign in to Xiaomi MiMo Desktop again"
		}
	}
	return account, ""
}

// jsonText is a JSON string or number as JavaScript's template literal and
// String() render it, or "" for anything else (null, false, an object): the pin
// tests these values for truthiness before it uses them.
func jsonText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) == nil && number != "0" {
		return number.String()
	}
	return ""
}

// attachDesktopSession adds MiMo Desktop's account session to a Xiaomi sign-in
// the operator started with consent, as the pin's exchange attaches it to every
// one ([provider]/[action]/route.js:335-361). A store that cannot be read leaves
// the sign-in as it is, as there.
func (h *Handler) attachDesktopSession(ctx context.Context, flow oauthFlow, result *onboarding.Result) {
	if !flow.desktopSession || !h.opts.HostCredentialDiscovery {
		return
	}
	session := h.mimoDesktop.ReadSession(ctx)
	if session == nil {
		return
	}
	if result.Secret.ProviderData == nil {
		result.Secret.ProviderData = map[string]string{}
	}
	for key, value := range session {
		result.Secret.ProviderData[key] = value
	}
}

// handleDesktopSessionRenewal turns a Xiaomi MiMo connection's quota-read
// renewal on or off (#299). On, a quota read of this connection that has no
// account session, or whose session just failed, may read MiMo Desktop's
// session on this host and seal it into the connection: the pin does this for
// every connection without a stored passToken (mimoAccount.js:181-185). Turning
// it on needs both consents, as a discovery does; turning it off needs neither
// and stops the next read. The answer is the connection's view, never a value.
func (h *Handler) handleDesktopSessionRenewal(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "configure ROUTEWEFT_CREDENTIAL_KEY to manage provider connections")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
		Consent bool  `json:"consent"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	if body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "enabled must be true or false")
		return
	}
	id := r.PathValue("id")
	existing, err := h.opts.Credentials.GetConnection(r.Context(), id)
	if err != nil {
		providerNotFound(w, err)
		return
	}
	if existing.ProviderID != mimoProviderID {
		writeError(w, http.StatusBadRequest, "desktop_session_unsupported", "only a Xiaomi MiMo connection can renew its session from MiMo Desktop")
		return
	}
	if *body.Enabled && h.hostDiscoveryRefused(w, body.Consent) {
		return
	}
	h.connectionMu.Lock()
	saved, err := h.updateConnection(r.Context(), id, func(current credentials.Connection) (credentials.Connection, error) {
		data := make(map[string]string, len(current.Secret.ProviderData)+1)
		for key, value := range current.Secret.ProviderData {
			data[key] = value
		}
		if *body.Enabled {
			data[onboarding.MimoDesktopRenewalKey] = onboarding.MimoDesktopRenewalOn
		} else {
			delete(data, onboarding.MimoDesktopRenewalKey)
		}
		current.Secret.ProviderData = data
		current.Lifecycle = credentials.Lifecycle{}
		return current, nil
	})
	h.connectionMu.Unlock()
	if err != nil {
		providerNotFound(w, err)
		return
	}
	writeJSON(w, http.StatusOK, connectionJSON(saved))
}

// withoutDesktopRenewal drops the #299 fields from provider data a request
// sent, so only handleDesktopSessionRenewal (with its consents) and a renewal
// itself can set them; an edit keeps the stored ones (mergeConnectionSecret).
func withoutDesktopRenewal(data map[string]string) {
	delete(data, onboarding.MimoDesktopRenewalKey)
	delete(data, onboarding.MimoDesktopReadAtKey)
}
