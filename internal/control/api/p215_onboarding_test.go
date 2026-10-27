package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// #215: the Codex golden path. A sign-in says whether it added an account or
// updated the stored one (the reference's duplicate rule), Reauthenticate renews
// exactly the connection it was started for, and bulk results say the same per
// item. No response carries a token.

// reauthenticate starts a Reauthenticate of id and pastes back the redirect for
// the reply the fake token endpoint will give.
func reauthenticate(t *testing.T, env *codexEnv, id, reply string) (map[string]any, *http.Response, startedFlow) {
	t.Helper()
	env.exchange.body = reply
	recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/codex/start", `{"connectionId":"`+id+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("reauthenticate start status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var flow startedFlow
	if err := json.Unmarshal(recorder.Body.Bytes(), &flow); err != nil {
		t.Fatal(err)
	}
	redirect := "http://localhost:1455/auth/callback?code=" + oauthTestCode + "&state=" + url.QueryEscape(flow.State)
	recorder = callback(t, env.mux, env.cookie, "codex", flow.State, redirect)
	assertNoTokens(t, recorder.Body.String())
	var saved map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &saved)
	return saved, recorder.Result(), flow
}

// assertNoTokens fails when a response carries any credential the fixtures mint.
func assertNoTokens(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{"access-", "refresh-", "eyJ", "rt_", "id-token"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response carries credential material %q: %s", secret, body)
		}
	}
}

func codexConnections(t *testing.T, env *codexEnv) []credentials.Connection {
	t.Helper()
	connections, err := env.creds.ListConnections(context.Background(), codexProviderID)
	if err != nil {
		t.Fatal(err)
	}
	return connections
}

// apiErrorCode reads the error code of a decoded API error body.
func apiErrorCode(body map[string]any) string {
	failure, _ := body["error"].(map[string]any)
	code, _ := failure["code"].(string)
	return code
}

// Two accounts make two connections; the same account again updates its row in
// place and says so. Each answer names the account and its plan, never a token.
func TestCodexSignInReportsAddedOrUpdatedWithPlan(t *testing.T) {
	env := newCodexEnv(t)
	first := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReplyPlan("a@example.invalid", "acct-a", "plus", "access-a1", "refresh-a1"))
	second := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReplyPlan("b@example.invalid", "acct-b", "pro", "access-b1", "refresh-b1"))
	again := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReplyPlan("a@example.invalid", "acct-a", "team", "access-a2", "refresh-a2"))
	for _, saved := range []map[string]any{first, second, again} {
		raw, _ := json.Marshal(saved)
		assertNoTokens(t, string(raw))
	}
	if first["outcome"] != onboardedAdded || first["identity"] != "a@example.invalid" || first["plan"] != "plus" {
		t.Fatalf("first account: %v", first)
	}
	if second["outcome"] != onboardedAdded || second["id"] == first["id"] || second["plan"] != "pro" {
		t.Fatalf("second account: %v", second)
	}
	if again["outcome"] != onboardedUpdated || again["id"] != first["id"] || again["plan"] != "team" {
		t.Fatalf("same account again: %v", again)
	}
	if n := len(codexConnections(t, env)); n != 2 {
		t.Fatalf("connections=%d, want 2", n)
	}
}

// Reauthenticate renews the chosen connection: new tokens, its name, priority
// and enabled state kept, the other account untouched.
func TestCodexReauthenticateRenewsOnlyItsConnection(t *testing.T) {
	env := newCodexEnv(t)
	a := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply("a@example.invalid", "acct-a", "access-a1", "refresh-a1"))
	b := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply("b@example.invalid", "acct-b", "access-b1", "refresh-b1"))
	id := a["id"].(string)
	if recorder := doJSON(t, env.mux, env.cookie, http.MethodPatch, "/admin/v1/connections/"+id, `{"name":"Work","enabled":false,"priority":5}`); recorder.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", recorder.Code, recorder.Body.String())
	}
	saved, response, _ := reauthenticate(t, env, id, codexReply("a@example.invalid", "acct-a", "access-a2", "refresh-a2"))
	if response.StatusCode != http.StatusOK || saved["outcome"] != onboardedReauthenticated || saved["id"] != id {
		t.Fatalf("reauthenticate: %d %v", response.StatusCode, saved)
	}
	got := connectionByID(t, env.creds, id)
	if got.Secret.AccessToken != "access-a2" || got.Secret.RefreshToken != "refresh-a2" {
		t.Fatalf("credential not renewed: %+v", got.Secret)
	}
	if got.Name != "Work" || got.Enabled || got.Priority != 5 || got.Identity != "a@example.invalid" {
		t.Fatalf("kept fields changed: %+v", got)
	}
	if other := connectionByID(t, env.creds, b["id"].(string)); other.Secret.AccessToken != "access-b1" {
		t.Fatalf("the other account changed: %+v", other.Secret)
	}
	if n := len(codexConnections(t, env)); n != 2 {
		t.Fatalf("connections=%d, want 2", n)
	}
}

// Signing in as another account, stored or new, during a Reauthenticate saves
// nothing: no row is added and neither stored credential changes. The attempt is
// used up, so the same paste cannot be replayed.
func TestCodexReauthenticateRefusesAnotherAccount(t *testing.T) {
	env := newCodexEnv(t)
	a := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply("a@example.invalid", "acct-a", "access-a1", "refresh-a1"))
	codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply("b@example.invalid", "acct-b", "access-b1", "refresh-b1"))
	id := a["id"].(string)
	for name, reply := range map[string]string{
		"stored account":       codexReply("b@example.invalid", "acct-b", "access-b2", "refresh-b2"),
		"new account":          codexReply("c@example.invalid", "acct-c", "access-c1", "refresh-c1"),
		"same email, new team": codexReply("a@example.invalid", "acct-other", "access-a3", "refresh-a3"),
		"no account claims":    codexReply("", "", "access-x1", "refresh-x1"),
	} {
		saved, response, flow := reauthenticate(t, env, id, reply)
		if response.StatusCode != http.StatusConflict || apiErrorCode(saved) != "oauth_account_mismatch" {
			t.Fatalf("%s: %d %v", name, response.StatusCode, saved)
		}
		replay := callback(t, env.mux, env.cookie, "codex", flow.State, "http://localhost:1455/auth/callback?code="+oauthTestCode+"&state="+url.QueryEscape(flow.State))
		if replay.Code != http.StatusBadRequest || !strings.Contains(replay.Body.String(), "oauth_state_invalid") {
			t.Fatalf("%s: replay was not refused: %d %s", name, replay.Code, replay.Body.String())
		}
	}
	connections := codexConnections(t, env)
	if len(connections) != 2 {
		t.Fatalf("connections=%d, want 2", len(connections))
	}
	for _, connection := range connections {
		if connection.Secret.AccessToken != "access-a1" && connection.Secret.AccessToken != "access-b1" {
			t.Fatalf("a stored credential changed: %+v", connection.Secret)
		}
	}
}

// A Codex row stored without an email (its id token named none) is still one
// account by its ChatGPT account id, so it can be reauthenticated.
func TestCodexReauthenticateRowWithoutEmail(t *testing.T) {
	env := newCodexEnv(t)
	saved := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply("", "acct-n", "access-n1", "refresh-n1"))
	id := saved["id"].(string)
	renewed, response, _ := reauthenticate(t, env, id, codexReply("", "acct-n", "access-n2", "refresh-n2"))
	if response.StatusCode != http.StatusOK || renewed["outcome"] != onboardedReauthenticated {
		t.Fatalf("reauthenticate: %d %v", response.StatusCode, renewed)
	}
	if got := connectionByID(t, env.creds, id); got.Secret.AccessToken != "access-n2" {
		t.Fatalf("credential not renewed: %+v", got.Secret)
	}
}

// A pasted bare access token is not taken as a Reauthenticate: the row would
// lose its refresh token and stop being matched.
func TestCodexReauthenticateRefusesBareAccessToken(t *testing.T) {
	env := newCodexEnv(t)
	saved := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply("a@example.invalid", "acct-a", "access-a1", "refresh-a1"))
	id := saved["id"].(string)
	recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/codex/start", `{"connectionId":"`+id+`"}`)
	var flow startedFlow
	if err := json.Unmarshal(recorder.Body.Bytes(), &flow); err != nil {
		t.Fatal(err)
	}
	token := unsignedJWT(codexClaims("a@example.invalid", "acct-a", "plus"))
	recorder = callback(t, env.mux, env.cookie, "codex", flow.State, token)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "oauth_account_mismatch") {
		t.Fatalf("bare token reauthenticate: %d %s", recorder.Code, recorder.Body.String())
	}
	if got := connectionByID(t, env.creds, id); got.Secret.AccessToken != "access-a1" || got.Secret.RefreshToken != "refresh-a1" {
		t.Fatalf("credential changed: %+v", got.Secret)
	}
}

// Reauthenticate names a connection of this provider; anything else is refused
// before an attempt exists. A connection deleted while its attempt is open saves
// nothing and is not recreated.
func TestCodexReauthenticateTargetMustExist(t *testing.T) {
	env := newCodexEnv(t)
	for _, id := range []string{"missing-connection"} {
		recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/codex/start", `{"connectionId":"`+id+`"}`)
		if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "connection_not_found") {
			t.Fatalf("%s: %d %s", id, recorder.Code, recorder.Body.String())
		}
	}
	grok := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/grok-cli/bulk-import", `[{"access_token":"access-g1","email":"g@example.invalid"}]`)
	var imported struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	if err := json.Unmarshal(grok.Body.Bytes(), &imported); err != nil || len(imported.Results) != 1 || imported.Results[0].ID == "" {
		t.Fatalf("grok-cli import: %d %s", grok.Code, grok.Body.String())
	}
	foreign := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/codex/start", `{"connectionId":"`+imported.Results[0].ID+`"}`)
	if foreign.Code != http.StatusNotFound || !strings.Contains(foreign.Body.String(), "connection_not_found") {
		t.Fatalf("another provider's connection: %d %s", foreign.Code, foreign.Body.String())
	}

	saved := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply("a@example.invalid", "acct-a", "access-a1", "refresh-a1"))
	id := saved["id"].(string)
	env.exchange.body = codexReply("a@example.invalid", "acct-a", "access-a2", "refresh-a2")
	recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/codex/start", `{"connectionId":"`+id+`"}`)
	var flow startedFlow
	if err := json.Unmarshal(recorder.Body.Bytes(), &flow); err != nil {
		t.Fatal(err)
	}
	if deleted := doJSON(t, env.mux, env.cookie, http.MethodDelete, "/admin/v1/connections/"+id, ""); deleted.Code/100 != 2 {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	recorder = callback(t, env.mux, env.cookie, "codex", flow.State, "http://localhost:1455/auth/callback?code="+oauthTestCode+"&state="+url.QueryEscape(flow.State))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "connection_not_found") {
		t.Fatalf("deleted target: %d %s", recorder.Code, recorder.Body.String())
	}
	if n := len(codexConnections(t, env)); n != 0 {
		t.Fatalf("a deleted target was recreated: %d connections", n)
	}
}

// A bulk batch reports each item: a new account added, a duplicate of a stored
// one updated in place, a bad item failed with its index. No token is echoed.
func TestCodexBulkImportReportsOutcomePerItem(t *testing.T) {
	env := newCodexEnv(t)
	idToken := func(email, account string) string { return unsignedJWT(codexClaims(email, account, "plus")) }
	stored := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply("a@example.invalid", "acct-a", "access-a1", "refresh-a1"))
	body := `[` +
		`{"accessToken":"access-a2","refreshToken":"refresh-a2","idToken":"` + idToken("a@example.invalid", "acct-a") + `","email":"a@example.invalid"},` +
		`{"refreshToken":"refresh-orphan"},` +
		`{"accessToken":"access-b1","refreshToken":"refresh-b1","idToken":"` + idToken("b@example.invalid", "acct-b") + `","email":"b@example.invalid"}` +
		`]`
	recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/codex/bulk-import", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("bulk: %d %s", recorder.Code, recorder.Body.String())
	}
	assertNoTokens(t, recorder.Body.String())
	var response struct {
		Total, Success, Failed int
		Results                []struct {
			Index   int    `json:"index"`
			OK      bool   `json:"ok"`
			ID      string `json:"id"`
			Outcome string `json:"outcome"`
			Error   string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Total != 3 || response.Success != 2 || response.Failed != 1 || len(response.Results) != 3 {
		t.Fatalf("totals: %+v", response)
	}
	if r := response.Results[0]; !r.OK || r.Outcome != onboardedUpdated || r.ID != stored["id"] {
		t.Fatalf("duplicate item: %+v", r)
	}
	if r := response.Results[1]; r.OK || r.Index != 1 || r.Error == "" || r.Outcome != "" {
		t.Fatalf("bad item: %+v", r)
	}
	if r := response.Results[2]; !r.OK || r.Outcome != onboardedAdded || r.ID == stored["id"] {
		t.Fatalf("new item: %+v", r)
	}
	if n := len(codexConnections(t, env)); n != 2 {
		t.Fatalf("connections=%d, want 2", n)
	}
}

// The dashboard offers bulk import where the endpoint takes it, and nowhere else.
func TestProvidersAdvertiseBulkImport(t *testing.T) {
	env := newCodexEnv(t)
	recorder := doJSON(t, env.mux, env.cookie, http.MethodGet, "/admin/v1/providers?pageSize=200", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("providers: %d %s", recorder.Code, recorder.Body.String())
	}
	var page struct {
		Items []struct {
			ID         string `json:"id"`
			BulkImport bool   `json:"bulkImport"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range page.Items {
		seen[item.ID] = item.BulkImport
		bulk := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/"+item.ID+"/bulk-import", `[{}]`)
		if supported := bulk.Code != http.StatusNotFound; supported != item.BulkImport {
			t.Fatalf("%s: bulkImport=%v but the endpoint answered %d", item.ID, item.BulkImport, bulk.Code)
		}
	}
	if !seen["codex"] {
		t.Fatalf("codex does not advertise bulk import: %v", seen)
	}
}
