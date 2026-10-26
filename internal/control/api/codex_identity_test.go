package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// codexSignIn runs one Codex browser sign-in whose token reply is body.
func codexSignIn(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, exchange *oauthExchange, body string) map[string]any {
	t.Helper()
	exchange.body = body
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/codex/start", `{}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var flow startedFlow
	if err := json.Unmarshal(recorder.Body.Bytes(), &flow); err != nil {
		t.Fatal(err)
	}
	redirect := "http://localhost:1455/auth/callback?code=" + oauthTestCode + "&state=" + url.QueryEscape(flow.State)
	recorder = callback(t, mux, cookie, "codex", flow.State, redirect)
	if recorder.Code != http.StatusOK {
		t.Fatalf("callback status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var saved map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}

func connectionByID(t *testing.T, creds *credentials.Store, id string) credentials.Connection {
	t.Helper()
	connection, err := creds.GetConnection(context.Background(), id)
	if err != nil {
		t.Fatalf("connection %s: %v", id, err)
	}
	return connection
}

// A Codex sign-in whose tokens name no email is stored without an identity, as
// the reference stores it without an email (codex.js:57-58), labelled "Account N"
// (connectionsRepo.js:289-291), and never matched to another: the same account
// signing in again adds a second connection (connectionsRepo.js:228 dedups only
// a connection with an email). The ChatGPT account id is kept as provider data,
// never promoted to the identity.
func TestCodexSignInWithoutEmailStoresNoIdentity(t *testing.T) {
	exchange := &oauthExchange{}
	_, mux, creds, cookie := newOAuthAPI(t, exchange)
	idToken := unsignedJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1","chatgpt_plan_type":"plus"}}`)
	reply := `{"access_token":"` + oauthTestAccess + `","refresh_token":"` + oauthTestRefresh + `","id_token":"` + idToken + `","expires_in":3600}`
	first := codexSignIn(t, mux, cookie, exchange, reply)
	second := codexSignIn(t, mux, cookie, exchange, reply)
	if first["identity"] != "" || first["name"] != "Account 1" || second["identity"] != "" || second["name"] != "Account 2" || first["id"] == second["id"] {
		t.Fatalf("first=%v second=%v", first, second)
	}
	stored := connectionByID(t, creds, first["id"].(string))
	if stored.Identity != "" || stored.Secret.ProviderData["chatgptAccountId"] != "acct-1" || stored.Settings.ChatGPTAccountID != "acct-1" {
		t.Fatalf("stored=%+v", stored)
	}
	// An access token that names its subject is the identity when the id token
	// has no email (providerHelpers.js:47-51).
	access := unsignedJWT(`{"sub":"auth0|fixture"}`)
	named := codexSignIn(t, mux, cookie, exchange, `{"access_token":"`+access+`","refresh_token":"`+oauthTestRefresh+`","id_token":"`+idToken+`","expires_in":3600}`)
	if named["identity"] != "auth0|fixture" || named["name"] != "auth0|fixture" {
		t.Fatalf("named=%v", named)
	}
}

// A connection stored before #239 with the ChatGPT account id as its identity is
// neither rewritten nor lost. The same account signing in, bulk-importing or
// importing its token again no longer resolves that identity, so each adds its
// own connection beside the old one, which keeps its identity, name, secret and
// settings until the operator deletes it, as the reference leaves duplicates to
// the operator (connectionsRepo.js:264).
func TestCodexAccountIDConnectionIsKept(t *testing.T) {
	exchange := &oauthExchange{}
	_, mux, creds, cookie := newOAuthAPI(t, exchange)
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections",
		`{"providerId":"codex","name":"acct-legacy","identity":"acct-legacy","authKind":"oauth","secret":{"accessToken":"legacy-access","refreshToken":"legacy-refresh","providerData":{"chatgptAccountId":"acct-legacy"}}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("seed status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var seeded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &seeded); err != nil {
		t.Fatal(err)
	}
	legacyID := seeded["id"].(string)
	before := connectionByID(t, creds, legacyID)

	claims := `{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-legacy"}}`
	idToken := unsignedJWT(claims)
	signedIn := codexSignIn(t, mux, cookie, exchange, `{"access_token":"`+oauthTestAccess+`","refresh_token":"`+oauthTestRefresh+`","id_token":"`+idToken+`","expires_in":3600}`)
	recorder = doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/codex/bulk-import", `[{"accessToken":"`+unsignedJWT(claims)+`","refreshToken":"bulk-refresh"}]`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("bulk status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/codex/import", `{"input":"`+unsignedJWT(claims)+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	after := connectionByID(t, creds, legacyID)
	if after.Identity != "acct-legacy" || after.Name != before.Name || after.Secret.AccessToken != "legacy-access" || after.Secret.RefreshToken != "legacy-refresh" ||
		after.Settings != before.Settings || after.Enabled != before.Enabled || after.Priority != before.Priority {
		t.Fatalf("legacy connection changed: before=%+v after=%+v", before, after)
	}
	connections, err := creds.ListConnections(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 4 {
		t.Fatalf("want the legacy connection plus three new ones, got %+v", connections)
	}
	for _, connection := range connections {
		if connection.ID != legacyID && connection.Identity != "" {
			t.Fatalf("a new connection took an identity from the account id: %+v", connection)
		}
	}
	if signedIn["id"] == legacyID {
		t.Fatal("the sign-in overwrote the legacy connection")
	}

	// Editing either kind keeps its identity: a blank identity in a PATCH neither
	// clears the legacy one nor gives the new one any.
	for _, id := range []string{legacyID, signedIn["id"].(string)} {
		want := connectionByID(t, creds, id).Identity
		recorder = doJSON(t, mux, cookie, http.MethodPatch, "/admin/v1/connections/"+id, `{"identity":"  ","enabled":false}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("patch %s status=%d body=%s", id, recorder.Code, recorder.Body.String())
		}
		if got := connectionByID(t, creds, id); got.Identity != want || got.Enabled {
			t.Fatalf("patch %s: identity=%q enabled=%v, want %q false", id, got.Identity, got.Enabled, want)
		}
	}
}

// Bulk items without an email are imported, each as its own connection
// (bulk-import/route.js:76-110), labelled "Account N" after every connection the
// provider already has (connectionsRepo.js:223-225,289-291). grok-cli, which the
// reference handles on its own route, still refuses such an item.
func TestCodexBulkImportsItemsWithoutEmail(t *testing.T) {
	h, mux, creds, cookie := newOAuthAPI(t, &oauthExchange{})
	for _, spec := range registry.Builtins() {
		if spec.ID == "grok-cli" {
			h.opts.Providers = append(h.opts.Providers, spec)
		}
	}
	token := unsignedJWT(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1"}}`)
	body := `[{"accessToken":"` + token + `"},{"accessToken":"` + token + `"},{"accessToken":"` + token + `","email":"named@example.invalid"},{"accessToken":"` + token + `","name":"Given"}]`
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/codex/bulk-import", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Success int `json:"success"`
		Failed  int `json:"failed"`
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Success != 4 || response.Failed != 0 {
		t.Fatalf("response=%s", recorder.Body.String())
	}
	want := [][2]string{{"Account 1", ""}, {"Account 2", ""}, {"named@example.invalid", "named@example.invalid"}, {"Given", ""}}
	for index, result := range response.Results {
		got := connectionByID(t, creds, result.ID)
		if got.Name != want[index][0] || got.Identity != want[index][1] || got.Secret.ProviderData["chatgptAccountId"] != "acct-1" {
			t.Fatalf("item %d: name=%q identity=%q data=%v, want %v", index, got.Name, got.Identity, got.Secret.ProviderData, want[index])
		}
	}

	recorder = doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/grok-cli/bulk-import", `[{"access_token":"fixture-access"}]`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("grok status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var grok struct {
		Success int `json:"success"`
		Failed  int `json:"failed"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &grok); err != nil || grok.Success != 0 || grok.Failed != 1 {
		t.Fatalf("grok-cli stored an item without identity: %s", recorder.Body.String())
	}
}

// A token import that names no email is stored without an identity under the
// operator's name, or "ChatGPT Access Token" (import-token/route.js:68,71-79).
func TestCodexTokenImportWithoutEmail(t *testing.T) {
	_, mux, creds, cookie := newOAuthAPI(t, &oauthExchange{})
	for _, tc := range []struct{ body, name string }{
		{`{"input":"opaque-not-a-jwt"}`, "ChatGPT Access Token"},
		{`{"name":"Team token","input":"opaque-not-a-jwt"}`, "Team token"},
	} {
		recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/codex/import", tc.body)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var saved map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &saved); err != nil {
			t.Fatal(err)
		}
		assertNoSecrets(t, recorder.Body.String(), "opaque-not-a-jwt")
		got := connectionByID(t, creds, saved["id"].(string))
		if got.Name != tc.name || got.Identity != "" || got.Secret.ProviderData["authMethod"] != "access_token" {
			t.Fatalf("stored=%+v, want name %q", got, tc.name)
		}
	}
}

// The manual connection form still requires an identity: only an onboarding flow
// that declares IdentityOptional stores a connection without one.
func TestManualConnectionStillRequiresIdentity(t *testing.T) {
	_, mux, creds, cookie := newOAuthAPI(t, &oauthExchange{})
	for _, identity := range []string{"", "   "} {
		body, _ := json.Marshal(map[string]any{"providerId": "codex", "name": "manual", "identity": identity, "authKind": "oauth", "secret": map[string]string{"accessToken": "manual-access"}})
		recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections", string(body))
		if recorder.Code != http.StatusBadRequest || errorCode(t, recorder.Body.Bytes()) != "connection_save_failed" {
			t.Fatalf("identity %q: status=%d body=%s", identity, recorder.Code, recorder.Body.String())
		}
	}
	if connections, _ := creds.ListConnections(context.Background(), "codex"); len(connections) != 0 {
		t.Fatalf("stored=%+v", connections)
	}
}
