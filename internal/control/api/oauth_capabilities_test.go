package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// unsignedJWT is a token whose payload is the given claims; the flows decode
// claims without verifying them.
func unsignedJWT(claims string) string {
	encode := base64.RawURLEncoding.EncodeToString
	return encode([]byte(`{"alg":"none"}`)) + "." + encode([]byte(claims)) + ".signature"
}

// errorCode reads the stable code from an error response.
func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("error body %s: %v", body, err)
	}
	return payload.Error.Code
}

// Codex starts with neither a name nor an identity, as the reference does
// (OAuthModal.js opens the provider page directly), and the id token supplies
// the identity. The nickname defaults to that identity.
func TestCodexSignInStartsWithoutNameOrIdentity(t *testing.T) {
	idToken := unsignedJWT(`{"https://api.openai.com/profile":{"email":"resolved@example.test"},"https://api.openai.com/auth":{"chatgpt_account_id":"acct-fixture"}}`)
	exchange := &oauthExchange{body: `{"access_token":"` + oauthTestAccess + `","refresh_token":"` + oauthTestRefresh + `","id_token":"` + idToken + `","expires_in":3600}`}
	_, mux, creds, cookie := newOAuthAPI(t, exchange)
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/codex/start", `{}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var flow startedFlow
	if err := json.Unmarshal(recorder.Body.Bytes(), &flow); err != nil {
		t.Fatal(err)
	}
	redirect := "http://localhost:1455/auth/callback?code=" + oauthTestCode + "&state=" + url.QueryEscape(flow.State)
	if recorder := callback(t, mux, cookie, "codex", flow.State, redirect); recorder.Code != http.StatusOK {
		t.Fatalf("callback status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	connections, _ := creds.ListConnections(context.Background(), "codex")
	if len(connections) != 1 || connections[0].Identity != "resolved@example.test" || connections[0].Name != "resolved@example.test" {
		t.Fatalf("stored=%+v", connections)
	}
}

// A flow that declares ResolvesIdentity but whose credential names nobody, with
// no operator fallback, is refused with its own code and saves nothing.
func TestUnresolvedIdentityIsRefusedBeforeSaving(t *testing.T) {
	_, mux, creds, cookie := newOAuthAPI(t, &oauthExchange{})
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/codex/import", `{"input":"opaque-not-a-jwt"}`)
	if recorder.Code != http.StatusBadGateway || errorCode(t, recorder.Body.Bytes()) != "oauth_identity_unresolved" {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assertNoSecrets(t, recorder.Body.String(), "opaque-not-a-jwt")
	if connections, _ := creds.ListConnections(context.Background(), "codex"); len(connections) != 0 {
		t.Fatalf("stored=%+v", connections)
	}
	// The operator's identity is still honoured as the fallback.
	recorder = doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/codex/import", `{"identity":"given@example.test","input":"opaque-not-a-jwt"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("fallback status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// Cancel ends the attempt server-side: the state cannot complete afterwards,
// another session cannot cancel it, and a replayed cancel is invalid.
func TestOAuthCancelEndsTheAttempt(t *testing.T) {
	exchange := &oauthExchange{body: tokenBody()}
	h, mux, _, cookie := newOAuthAPI(t, exchange)
	flow := startFlow(t, mux, cookie, "claude")
	cancel := func(cookie *http.Cookie, provider string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"state": flow.State})
		return doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/cancel", string(body))
	}
	if recorder := cancel(nil, "claude"); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous cancel status=%d", recorder.Code)
	}
	if recorder := cancel(secondSession(t, h), "claude"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("foreign cancel status=%d", recorder.Code)
	}
	if recorder := cancel(cookie, "codex"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("wrong-provider cancel status=%d", recorder.Code)
	}
	recorder := cancel(cookie, "claude")
	if recorder.Code != http.StatusOK || recorder.Body.String() == "" {
		t.Fatalf("cancel status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.Status != "cancelled" {
		t.Fatalf("cancel body=%s", recorder.Body.String())
	}
	if recorder := callback(t, mux, cookie, "claude", flow.State, oauthTestCode+"#"+flow.State); recorder.Code != http.StatusBadRequest || exchange.calls() != 0 {
		t.Fatalf("callback after cancel status=%d calls=%d", recorder.Code, exchange.calls())
	}
	if recorder := cancel(cookie, "claude"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("replayed cancel status=%d", recorder.Code)
	}
}

// A device attempt being saved is not torn down by a cancel that races it.
func TestOAuthCancelLeavesASavingDeviceAttempt(t *testing.T) {
	h, mux, _, cookie, _ := newDeviceAPI(t, &deviceUpstream{})
	started := startDevice(t, mux, cookie)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookie)
	session, _ := h.sessionFor(request)
	if _, reserved := h.oauth.reserve(started.State, session.ID, "kilocode"); reserved != reserveTaken {
		t.Fatal("could not reserve")
	}
	body, _ := json.Marshal(map[string]string{"state": started.State})
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/kilocode/cancel", string(body))
	if recorder.Code != http.StatusConflict || errorCode(t, recorder.Body.Bytes()) != "oauth_completing" {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, ok := h.oauth.pending[started.State]; !ok {
		t.Fatal("cancel removed an attempt that was being saved")
	}
	// A save that persisted nothing hands the attempt back, so the conflict above
	// was not a save: the attempt is open again and a cancel now ends it.
	h.oauth.release(started.State)
	recorder = doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/kilocode/cancel", string(body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("cancel after release status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, ok := h.oauth.pending[started.State]; ok {
		t.Fatal("confirmed cancel left the attempt pending")
	}
}

// Each sign-in method is described on its own, with only its own fields.
func TestProviderReadModelDescribesEachSignInMethod(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	handler.opts.Providers = registry.Builtins()
	if err := bootstrapOperator(context.Background(), store.DB()); err != nil {
		t.Fatal(err)
	}
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := doJSON(t, mux, &http.Cookie{Name: sessionCookieName, Value: session.ID}, http.MethodGet, "/admin/v1/providers?pageSize=200", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("providers status=%d", recorder.Code)
	}
	var payload struct {
		Items []struct {
			ID            string                  `json:"id"`
			SignInModes   []string                `json:"signInModes"`
			SignInMethods []signInMethodReadModel `json:"signInMethods"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	methods := map[string]map[string]signInMethodReadModel{}
	for _, item := range payload.Items {
		methods[item.ID] = map[string]signInMethodReadModel{}
		for index, method := range item.SignInMethods {
			if item.SignInModes[index] != method.Mode {
				t.Fatalf("%s signInModes=%v disagree with methods", item.ID, item.SignInModes)
			}
			methods[item.ID][method.Mode] = method
		}
	}
	codex := methods["codex"]
	if browser := codex["browser"]; !browser.ResolvesIdentity || browser.Completion != "redirect-url" || browser.LoopbackRedirect != "http://localhost:1455/auth/callback" || browser.AuthKind != "oauth" {
		t.Fatalf("codex browser=%+v", browser)
	}
	if !codex["import"].ResolvesIdentity {
		t.Fatalf("codex import=%+v", codex["import"])
	}
	if methods["xai"]["browser"].LoopbackRedirect != "http://127.0.0.1:56121/callback" {
		t.Fatalf("xai browser=%+v", methods["xai"]["browser"])
	}
	// Claude's redirect is the provider's own page, not a loopback, and its flow
	// does not name the account.
	if claude := methods["claude"]["browser"]; claude.ResolvesIdentity || claude.LoopbackRedirect != "" || claude.Completion != "code" {
		t.Fatalf("claude browser=%+v", claude)
	}
	// GitLab's PAT import does not show the OAuth application's fields.
	if gitlab := methods["gitlab"]; len(gitlab["import"].MetaKeys) != 1 || gitlab["import"].MetaKeys[0] != "baseUrl" || len(gitlab["browser"].MetaKeys) != 3 {
		t.Fatalf("gitlab=%+v", gitlab)
	}
	// Each import stores its own mode, so dual-auth providers are distinguishable.
	if methods["iflow"]["import"].AuthKind != "cookie" || methods["iflow"]["browser"].AuthKind != "oauth" || methods["kiro"]["import"].AuthKind != "api-key" {
		t.Fatalf("iflow=%+v kiro=%+v", methods["iflow"], methods["kiro"])
	}
	if github, ok := methods["github"]["device"]; !ok || github.Completion != "" || len(methods["github"]) != 1 {
		t.Fatalf("github=%+v", methods["github"])
	}
	if len(methods["openai"]) != 0 {
		t.Fatalf("openai=%+v", methods["openai"])
	}
}
