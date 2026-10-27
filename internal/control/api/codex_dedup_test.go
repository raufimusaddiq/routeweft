package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// codexClaims is an OpenAI JWT payload naming email, ChatGPT account and plan;
// an empty value leaves its claim out.
func codexClaims(email, account, plan string) string {
	auth := map[string]any{}
	if account != "" {
		auth["chatgpt_account_id"] = account
	}
	if plan != "" {
		auth["chatgpt_plan_type"] = plan
	}
	claims := map[string]any{"https://api.openai.com/auth": auth}
	if email != "" {
		claims["email"] = email
	}
	raw, _ := json.Marshal(claims)
	return string(raw)
}

// codexReply is a Codex token reply whose id token names email and account.
func codexReply(email, account, access, refresh string) string {
	return codexReplyPlan(email, account, "plus", access, refresh)
}

func codexReplyPlan(email, account, plan, access, refresh string) string {
	return `{"access_token":"` + access + `","refresh_token":"` + refresh + `","id_token":"` + unsignedJWT(codexClaims(email, account, plan)) + `","expires_in":3600}`
}

// codexEnv is one API under test and the ways a Codex connection reaches it.
type codexEnv struct {
	h        *Handler
	mux      *http.ServeMux
	creds    *credentials.Store
	cookie   *http.Cookie
	exchange *oauthExchange
}

func newCodexEnv(t *testing.T) *codexEnv {
	t.Helper()
	exchange := &oauthExchange{}
	h, mux, creds, cookie := newOAuthAPI(t, exchange)
	for _, spec := range registry.Builtins() {
		if spec.ID == "grok-cli" {
			h.opts.Providers = append(h.opts.Providers, spec)
		}
	}
	return &codexEnv{h: h, mux: mux, creds: creds, cookie: cookie, exchange: exchange}
}

// codexOp stores one credential and answers the connection id it saved, or ""
// with the HTTP status when it saved none.
type codexOp func(t *testing.T, env *codexEnv) (id string, status int)

func signInOp(email, account, access string) codexOp {
	return func(t *testing.T, env *codexEnv) (string, int) {
		saved := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply(email, account, access, "refresh-"+access))
		return saved["id"].(string), http.StatusOK
	}
}

func bulkOp(provider, item string) codexOp {
	return func(t *testing.T, env *codexEnv) (string, int) {
		recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/bulk-import", `[`+item+`]`)
		var response struct {
			Results []struct {
				OK bool   `json:"ok"`
				ID string `json:"id"`
			} `json:"results"`
		}
		if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil || len(response.Results) != 1 {
			t.Fatalf("bulk status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		if !response.Results[0].OK {
			return "", http.StatusBadRequest
		}
		return response.Results[0].ID, http.StatusOK
	}
}

func codexBulkOp(email, account, access string) codexOp {
	return bulkOp("codex", `{"accessToken":"`+unsignedJWT(codexClaims(email, account, "plus"))+`","refreshToken":"refresh-`+access+`"}`)
}

// exportedTokenRowOp bulk-imports a token-import row as the reference exports
// it: providerSpecificData still says authMethod access_token.
func exportedTokenRowOp(email, account string) codexOp {
	return bulkOp("codex", `{"accessToken":"`+unsignedJWT(codexClaims(email, account, "plus"))+`","name":"ChatGPT Access Token","providerSpecificData":{"authMethod":"access_token","chatgptAccountId":"`+account+`","chatgptPlanType":"plus"}}`)
}

func tokenImportOp(email, account string) codexOp {
	return func(t *testing.T, env *codexEnv) (string, int) {
		recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/codex/import", `{"input":"`+unsignedJWT(codexClaims(email, account, "plus"))+`"}`)
		if recorder.Code != http.StatusOK {
			return "", recorder.Code
		}
		var saved map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &saved); err != nil {
			t.Fatal(err)
		}
		return saved["id"].(string), recorder.Code
	}
}

// rawTokenSignInOp pastes a raw Codex access token where the sign-in code goes.
func rawTokenSignInOp(email, account string) codexOp {
	return func(t *testing.T, env *codexEnv) (string, int) {
		saved, status := codexPasteSignIn(t, env, unsignedJWT(codexClaims(email, account, "plus")))
		if saved == nil {
			return "", status
		}
		return saved["id"].(string), status
	}
}

// codexPasteSignIn starts a Codex sign-in and pastes input as its completion.
func codexPasteSignIn(t *testing.T, env *codexEnv, input string) (map[string]any, int) {
	t.Helper()
	recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/codex/start", `{}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var flow startedFlow
	if err := json.Unmarshal(recorder.Body.Bytes(), &flow); err != nil {
		t.Fatal(err)
	}
	recorder = callback(t, env.mux, env.cookie, "codex", flow.State, input)
	if recorder.Code != http.StatusOK {
		return nil, recorder.Code
	}
	var saved map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	return saved, recorder.Code
}

func manualOp(provider, identity string) codexOp {
	return func(t *testing.T, env *codexEnv) (string, int) {
		recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/connections",
			`{"providerId":"`+provider+`","name":"manual","identity":"`+identity+`","authKind":"oauth","secret":{"accessToken":"manual-access"}}`)
		if recorder.Code != http.StatusOK {
			return "", recorder.Code
		}
		var saved map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &saved); err != nil {
			t.Fatal(err)
		}
		return saved["id"].(string), recorder.Code
	}
}

func (env *codexEnv) count(t *testing.T, provider string) int {
	t.Helper()
	connections, err := env.creds.ListConnections(context.Background(), provider)
	if err != nil {
		t.Fatal(err)
	}
	return len(connections)
}

// TestCodexDuplicateHandlingMatchesReference is the drift guard for #241. Each
// row is one case of the PR table: two credentials reach the store and the
// second either updates the first (one connection, same id), inserts beside it
// (two connections), or is refused. A change to the Codex duplicate rule, or to
// the providers that keep refusing, fails a row here.
func TestCodexDuplicateHandlingMatchesReference(t *testing.T) {
	const mail = "shared@example.invalid"
	cases := []struct {
		name, reference string
		provider        string
		first, second   codexOp
		want            string
	}{
		{"sign-in same email same account", "connectionsRepo.js:239-242,266-286", "codex",
			signInOp(mail, "acct-1", "a1"), signInOp(mail, "acct-1", "a2"), "update"},
		{"bulk same email same account", "connectionsRepo.js:239-242,266-286", "codex",
			codexBulkOp(mail, "acct-1", "a1"), codexBulkOp(mail, "acct-1", "a2"), "update"},
		{"sign-in then bulk, same email same account", "connectionsRepo.js:239-242", "codex",
			signInOp(mail, "acct-1", "a1"), codexBulkOp(mail, "acct-1", "a2"), "update"},
		{"sign-in same email different account", "connectionsRepo.js:241", "codex",
			signInOp(mail, "acct-1", "a1"), signInOp(mail, "acct-2", "a2"), "insert"},
		{"bulk same email different account", "connectionsRepo.js:241", "codex",
			codexBulkOp(mail, "acct-1", "a1"), codexBulkOp(mail, "acct-2", "a2"), "insert"},
		{"same email, incoming has no account id", "connectionsRepo.js:241 (!!incomingWs)", "codex",
			signInOp(mail, "acct-1", "a1"), signInOp(mail, "", "a2"), "insert"},
		{"same email, existing has no account id", "connectionsRepo.js:241 (!!existingWs)", "codex",
			signInOp(mail, "", "a1"), signInOp(mail, "acct-1", "a2"), "insert"},
		{"same email, neither has an account id", "connectionsRepo.js:241", "codex",
			codexBulkOp(mail, "", "a1"), codexBulkOp(mail, "", "a2"), "insert"},
		{"email-less items, same account", "connectionsRepo.js:228", "codex",
			codexBulkOp("", "acct-1", "a1"), codexBulkOp("", "acct-1", "a2"), "insert"},
		{"email-less sign-ins, same account", "connectionsRepo.js:228", "codex",
			signInOp("", "acct-1", "a1"), signInOp("", "acct-1", "a2"), "insert"},
		{"token import twice, same email same account", "import-token/route.js:73, connectionsRepo.js:264", "codex",
			tokenImportOp(mail, "acct-1"), tokenImportOp(mail, "acct-1"), "insert"},
		{"token import then sign-in, same email same account", "connectionsRepo.js:232", "codex",
			tokenImportOp(mail, "acct-1"), signInOp(mail, "acct-1", "a2"), "insert"},
		{"sign-in then token import, same email same account", "connectionsRepo.js:264", "codex",
			signInOp(mail, "acct-1", "a1"), tokenImportOp(mail, "acct-1"), "insert"},
		{"raw token pasted at sign-in twice, same email same account", "[provider]/[action]/route.js:438, connectionsRepo.js:264", "codex",
			rawTokenSignInOp(mail, "acct-1"), rawTokenSignInOp(mail, "acct-1"), "insert"},
		{"raw token sign-in then sign-in, same email same account", "connectionsRepo.js:232", "codex",
			rawTokenSignInOp(mail, "acct-1"), signInOp(mail, "acct-1", "a2"), "insert"},
		{"bulk re-import of an exported token-import row, then sign-in", "bulk-import/route.js:106-108 stores authType oauth; connectionsRepo.js:239-242", "codex",
			exportedTokenRowOp(mail, "acct-1"), signInOp(mail, "acct-1", "a2"), "update"},
		{"bulk re-import of an exported token-import row, twice", "bulk-import/route.js:106-108, connectionsRepo.js:239-242", "codex",
			exportedTokenRowOp(mail, "acct-1"), exportedTokenRowOp(mail, "acct-1"), "update"},
		{"token import then bulk, same email same account", "connectionsRepo.js:232", "codex",
			tokenImportOp(mail, "acct-1"), codexBulkOp(mail, "acct-1", "a2"), "insert"},
		{"manual form, same codex identity", "Routeweft-only form; providers/route.js:179-181", "codex",
			signInOp(mail, "acct-1", "a1"), manualOp("codex", mail), "refuse"},
		{"other provider, manual form duplicate", "unchanged: UNIQUE kept off the Codex node", "claude",
			manualOp("claude", mail), manualOp("claude", mail), "refuse"},
		{"other provider, bulk duplicate", "unchanged: UNIQUE kept off the Codex node", "grok-cli",
			bulkOp("grok-cli", `{"access_token":"g1","email":"`+mail+`"}`), bulkOp("grok-cli", `{"access_token":"g2","email":"`+mail+`"}`), "refuse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newCodexEnv(t)
			first, status := tc.first(t, env)
			if first == "" {
				t.Fatalf("first save status=%d", status)
			}
			second, status := tc.second(t, env)
			count := env.count(t, tc.provider)
			got := "insert"
			switch {
			case second == "":
				got = fmt.Sprintf("refuse(%d)", status)
				if status == http.StatusBadRequest && count == 1 {
					got = "refuse"
				}
			case second == first && count == 1:
				got = "update"
			case second == first || count != 2:
				got = fmt.Sprintf("unexpected: same id %v, %d connections", second == first, count)
			}
			if got != tc.want {
				t.Fatalf("%s: got %s, want %s (reference %s)", tc.name, got, tc.want, tc.reference)
			}
		})
	}
}

// A sign-in that matches keeps the row's id, name, priority, enabled flag and
// identity, replaces its tokens and its providerSpecificData whole, and clears its
// health (connectionsRepo.js:64-82,283; a sign-in sends none of the kept fields,
// codex.js:48-66 and [provider]/[action]/route.js:471-479).
func TestCodexSignInUpdateKeepsRowAndReplacesCredential(t *testing.T) {
	env := newCodexEnv(t)
	const mail = "shared@example.invalid"
	other := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply("other@example.invalid", "acct-9", "o1", "o1r"))
	first := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReplyPlan(mail, "acct-1", "plus", "a1", "r1"))
	id := first["id"].(string)
	recorder := doJSON(t, env.mux, env.cookie, http.MethodPatch, "/admin/v1/connections/"+id, `{"name":"Renamed","enabled":false,"priority":7}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	env.h.opts.Runtime.State().Cooldown(id, time.Now().Add(time.Hour))

	second := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReplyPlan(mail, "acct-1", "", "a2", "r2"))
	if second["id"] != id {
		t.Fatalf("matching sign-in inserted: %v", second)
	}
	got := connectionByID(t, env.creds, id)
	if got.Name != "Renamed" || got.Enabled || got.Priority != 7 || got.Identity != mail {
		t.Fatalf("kept fields changed: %+v", got)
	}
	if got.Secret.AccessToken != "a2" || got.Secret.RefreshToken != "r2" || got.Secret.IDToken == "" {
		t.Fatalf("credential not replaced: %+v", got.Secret)
	}
	if len(got.Secret.ProviderData) != 1 || got.Secret.ProviderData["chatgptAccountId"] != "acct-1" {
		t.Fatalf("providerSpecificData not replaced whole: %v", got.Secret.ProviderData)
	}
	if _, active := env.h.opts.Runtime.State().CooldownUntil(id, time.Now()); active {
		t.Fatal("health state not cleared on update")
	}
	if unchanged := connectionByID(t, env.creds, other["id"].(string)); unchanged.Secret.AccessToken != "o1" {
		t.Fatalf("an unrelated connection changed: %+v", unchanged)
	}
}

// A bulk item that matches spreads over the row (connectionsRepo.js:283): its
// own name replaces the label, its isActive (true unless it says false,
// bulk-import/route.js:103) sets enabled, a token it leaves out is kept, and the
// id, priority and identity stay.
func TestCodexBulkUpdateSpreadsItemOverRow(t *testing.T) {
	env := newCodexEnv(t)
	const mail = "shared@example.invalid"
	token := unsignedJWT(codexClaims(mail, "acct-1", "plus"))
	id, _ := bulkOp("codex", `{"accessToken":"`+token+`","refreshToken":"r1","displayName":"Shown"}`)(t, env)
	recorder := doJSON(t, env.mux, env.cookie, http.MethodPatch, "/admin/v1/connections/"+id, `{"enabled":false,"priority":4}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	// No isActive: re-enabled. displayName is not the reference's name: kept.
	if again, _ := bulkOp("codex", `{"accessToken":"`+token+`","displayName":"Other"}`)(t, env); again != id {
		t.Fatalf("matching item inserted")
	}
	got := connectionByID(t, env.creds, id)
	if !got.Enabled || got.Name != "Shown" || got.Priority != 4 || got.Identity != mail || got.Secret.RefreshToken != "r1" {
		t.Fatalf("after re-import: %+v", got)
	}
	// An item that names itself and says isActive false renames and disables.
	if again, _ := bulkOp("codex", `{"accessToken":"`+token+`","refreshToken":"r3","name":"Named","isActive":false}`)(t, env); again != id {
		t.Fatalf("matching item inserted")
	}
	got = connectionByID(t, env.creds, id)
	if got.Enabled || got.Name != "Named" || got.Priority != 4 || got.Secret.RefreshToken != "r3" {
		t.Fatalf("after named import: %+v", got)
	}
	if env.count(t, "codex") != 1 {
		t.Fatal("bulk re-imports added connections")
	}
}

// Connections stored before v12 are neither lost nor rewritten: one saved with
// the ChatGPT account id as its identity (before #239) is never matched by an
// email, and a stored email connection without an account id is never matched
// either, so a new sign-in for the same account adds a row beside each.
func TestCodexStoredConnectionsAreNotRewritten(t *testing.T) {
	env := newCodexEnv(t)
	seed := func(body string) credentials.Connection {
		recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/connections", body)
		if recorder.Code != http.StatusOK {
			t.Fatalf("seed status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var saved map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &saved); err != nil {
			t.Fatal(err)
		}
		return connectionByID(t, env.creds, saved["id"].(string))
	}
	legacy := seed(`{"providerId":"codex","name":"acct-legacy","identity":"acct-legacy","authKind":"oauth","secret":{"accessToken":"legacy-access","refreshToken":"legacy-refresh","providerData":{"chatgptAccountId":"acct-legacy"}}}`)
	bare := seed(`{"providerId":"codex","name":"bare","identity":"bare@example.invalid","authKind":"oauth","secret":{"accessToken":"bare-access","refreshToken":"bare-refresh"}}`)

	codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply("legacy@example.invalid", "acct-legacy", "n1", "n1r"))
	codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply("bare@example.invalid", "acct-bare", "n2", "n2r"))
	codexBulkOp("bare@example.invalid", "acct-bare", "n3")(t, env)

	for _, before := range []credentials.Connection{legacy, bare} {
		after := connectionByID(t, env.creds, before.ID)
		if after.Identity != before.Identity || after.Name != before.Name || after.Secret.AccessToken != before.Secret.AccessToken ||
			after.Secret.RefreshToken != before.Secret.RefreshToken || after.Enabled != before.Enabled || after.Priority != before.Priority {
			t.Fatalf("stored connection rewritten: before=%+v after=%+v", before, after)
		}
	}
	// legacy, bare, the legacy account's sign-in, and one bare@ row with acct-bare
	// that the bulk item then updated.
	if got := env.count(t, "codex"); got != 4 {
		t.Fatalf("connections=%d, want 4", got)
	}
}

// Two Codex connections may share an email, so an edit that leaves the identity
// alone (a toggle) succeeds on either, while one that moves a connection onto an
// identity another holds is refused as before.
func TestCodexManualEditOfDuplicate(t *testing.T) {
	env := newCodexEnv(t)
	const mail = "shared@example.invalid"
	first := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply(mail, "acct-1", "a1", "r1"))
	second := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply(mail, "acct-2", "a2", "r2"))
	third := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply("third@example.invalid", "acct-3", "a3", "r3"))
	for _, id := range []string{first["id"].(string), second["id"].(string)} {
		if recorder := doJSON(t, env.mux, env.cookie, http.MethodPatch, "/admin/v1/connections/"+id, `{"enabled":false}`); recorder.Code != http.StatusOK {
			t.Fatalf("toggle %s status=%d body=%s", id, recorder.Code, recorder.Body.String())
		}
	}
	recorder := doJSON(t, env.mux, env.cookie, http.MethodPatch, "/admin/v1/connections/"+third["id"].(string), `{"identity":"`+mail+`"}`)
	if recorder.Code != http.StatusBadRequest || errorCode(t, recorder.Body.Bytes()) != "connection_save_failed" {
		t.Fatalf("identity move status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// Bulk imports racing on one account leave one connection: the lookup and the
// write it decides are one step, as in the reference's transaction
// (connectionsRepo.js:210).
func TestCodexConcurrentImportsOfOneAccount(t *testing.T) {
	env := newCodexEnv(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/codex/bulk-import",
				`[{"accessToken":"`+unsignedJWT(codexClaims("shared@example.invalid", "acct-1", "plus"))+`"}]`)
			if recorder.Code != http.StatusOK {
				t.Errorf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		}()
	}
	wg.Wait()
	if got := env.count(t, "codex"); got != 1 {
		t.Fatalf("connections=%d, want 1", got)
	}
}

// Manual creates of one Codex identity, racing each other and onboarding writes
// of the same email, save at most one manual connection: the form's identity
// check and its save are one step with every other connection write
// (connectionMu).
func TestCodexConcurrentManualCreatesOfOneIdentity(t *testing.T) {
	env := newCodexEnv(t)
	const mail = "shared@example.invalid"
	var wg sync.WaitGroup
	var mu sync.Mutex
	saved := 0
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/connections",
				`{"providerId":"codex","name":"manual","identity":"`+mail+`","authKind":"oauth","secret":{"accessToken":"manual-access"}}`)
			if recorder.Code == http.StatusOK {
				mu.Lock()
				saved++
				mu.Unlock()
			} else if recorder.Code != http.StatusBadRequest {
				t.Errorf("manual status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		}()
		go func() {
			defer wg.Done()
			recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/codex/bulk-import",
				`[{"accessToken":"`+unsignedJWT(codexClaims(mail, "acct-1", "plus"))+`"}]`)
			if recorder.Code != http.StatusOK {
				t.Errorf("bulk status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		}()
	}
	wg.Wait()
	if saved > 1 {
		t.Fatalf("%d manual creates saved one identity", saved)
	}
	connections, err := env.creds.ListConnections(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	manual, bulk := 0, 0
	for _, connection := range connections {
		if connection.Name == "manual" {
			manual++
		} else {
			bulk++
		}
	}
	if manual != saved || bulk != 1 {
		t.Fatalf("manual=%d (saved %d) bulk=%d, want at most one manual and one bulk row", manual, saved, bulk)
	}
}

// A sign-in replaces the credential whole (codex.js:51-55,
// [provider]/[action]/route.js:474-477): a reply without a refresh token or
// expires_in clears the stored ones, as the spread of undefined and null does.
func TestCodexSignInWithoutRefreshTokenClearsIt(t *testing.T) {
	env := newCodexEnv(t)
	const mail = "shared@example.invalid"
	first := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply(mail, "acct-1", "a1", "r1"))
	id := first["id"].(string)
	if got := connectionByID(t, env.creds, id); got.Secret.RefreshToken != "r1" || got.Secret.Expiry.IsZero() {
		t.Fatalf("first sign-in: %+v", got.Secret)
	}
	bare := `{"access_token":"a2","id_token":"` + unsignedJWT(codexClaims(mail, "acct-1", "plus")) + `"}`
	if second := codexSignIn(t, env.mux, env.cookie, env.exchange, bare); second["id"] != id {
		t.Fatalf("matching sign-in inserted: %v", second)
	}
	got := connectionByID(t, env.creds, id)
	if got.Secret.AccessToken != "a2" || got.Secret.RefreshToken != "" || !got.Secret.Expiry.IsZero() {
		t.Fatalf("credential not replaced whole: %+v", got.Secret)
	}
}

// A bulk item spreads only the keys it has (connectionsRepo.js:283): a refresh
// token it sets to null is cleared, one it leaves out is kept, and an expiresIn
// sets the expiry (bulk-import/route.js:96-99).
func TestCodexBulkUpdateReplacesOnlyCarriedKeys(t *testing.T) {
	env := newCodexEnv(t)
	token := unsignedJWT(codexClaims("shared@example.invalid", "acct-1", "plus"))
	id, _ := bulkOp("codex", `{"accessToken":"`+token+`","refreshToken":"r1","idToken":"`+token+`"}`)(t, env)
	if again, _ := bulkOp("codex", `{"accessToken":"`+token+`","refreshToken":null,"expiresIn":3600}`)(t, env); again != id {
		t.Fatal("matching item inserted")
	}
	got := connectionByID(t, env.creds, id)
	if got.Secret.RefreshToken != "" || got.Secret.IDToken != token || got.Secret.Expiry.IsZero() {
		t.Fatalf("after re-import: %+v", got.Secret)
	}
}

// A bulk item's priority places a new connection when it is non-zero, replaces a
// matched one's, and a zero there sorts the row after the rest, as the
// reference's (priority || 999) does (connectionsRepo.js:159,293-294,283).
func TestCodexBulkPriority(t *testing.T) {
	env := newCodexEnv(t)
	item := func(email, priority string) string {
		return `{"accessToken":"` + unsignedJWT(codexClaims(email, "acct-"+email, "plus")) + `","priority":` + priority + `}`
	}
	other, _ := bulkOp("codex", item("other@example.invalid", "0"))(t, env)
	id, _ := bulkOp("codex", item("shared@example.invalid", "5"))(t, env)
	if got := connectionByID(t, env.creds, other); got.Priority != 0 {
		t.Fatalf("zero priority insert placed at %d, want appended at 0", got.Priority)
	}
	if got := connectionByID(t, env.creds, id); got.Priority != 5 {
		t.Fatalf("insert priority=%d, want 5", got.Priority)
	}
	bulkOp("codex", item("shared@example.invalid", "2"))(t, env)
	if got := connectionByID(t, env.creds, id); got.Priority != 2 {
		t.Fatalf("update priority=%d, want 2", got.Priority)
	}
	bulkOp("codex", item("other@example.invalid", "0"))(t, env)
	if got := connectionByID(t, env.creds, other); got.Priority != 3 {
		t.Fatalf("zero priority update=%d, want 3 (after the rest)", got.Priority)
	}
	if env.count(t, "codex") != 2 {
		t.Fatal("bulk re-imports added connections")
	}
}

// When more than one stored row matches, the oldest is updated, as the
// reference's all.find over the table does, not the first in routing order.
func TestCodexMatchTakesOldestRow(t *testing.T) {
	env := newCodexEnv(t)
	ctx := context.Background()
	node, err := env.h.builtinNode(ctx, "codex")
	if err != nil {
		t.Fatal(err)
	}
	put := func(priority int, access string) string {
		stored, err := env.creds.PutConnection(ctx, credentials.Connection{
			NodeID: node.ID, Name: access, AuthKind: credentials.AuthOAuth, Identity: "shared@example.invalid", Enabled: true, Priority: priority,
			Secret: credentials.Secret{AccessToken: access, ProviderData: map[string]string{"chatgptAccountId": "acct-1"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return stored.ID
	}
	older := put(9, "older")
	newer := put(0, "newer")
	if saved := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply("shared@example.invalid", "acct-1", "a2", "r2")); saved["id"] != older {
		t.Fatalf("sign-in updated %v, want the older row %s", saved["id"], older)
	}
	if got := connectionByID(t, env.creds, newer); got.Secret.AccessToken != "newer" {
		t.Fatalf("newer row changed: %+v", got.Secret)
	}
}

// A raw access token pasted as the sign-in code is stored without an exchange,
// as an access_token connection named by its email
// ([provider]/[action]/route.js:414-454).
func TestCodexRawTokenSignIn(t *testing.T) {
	env := newCodexEnv(t)
	token := unsignedJWT(codexClaims("raw@example.invalid", "acct-1", "plus"))
	saved, status := codexPasteSignIn(t, env, token)
	if saved == nil {
		t.Fatalf("raw token sign-in status=%d", status)
	}
	if len(env.exchange.requests) != 0 {
		t.Fatalf("raw token was sent to the token endpoint: %d requests", len(env.exchange.requests))
	}
	got := connectionByID(t, env.creds, saved["id"].(string))
	want := map[string]string{"authMethod": "access_token", "chatgptAccountId": "acct-1", "chatgptPlanType": "plus"}
	if got.Identity != "raw@example.invalid" || got.Name != "raw@example.invalid" || got.Secret.AccessToken != token || got.Secret.RefreshToken != "" || fmt.Sprint(got.Secret.ProviderData) != fmt.Sprint(want) {
		t.Fatalf("raw token connection: %+v", got)
	}
}

// Connections stored before Secret.AuthType existed carry no record of how they
// were signed in. One whose providerSpecificData says authMethod access_token
// may be a token import or a bulk re-import of an exported one; both stored the
// same payload, so it is never matched and a sign-in or bulk item adds a row
// beside it (codexTokenOnly). One without that authMethod is matched as before.
func TestCodexLegacyAccessTokenRowsAreNotMatched(t *testing.T) {
	env := newCodexEnv(t)
	ctx := context.Background()
	const mail = "shared@example.invalid"
	node, err := env.h.builtinNode(ctx, "codex")
	if err != nil {
		t.Fatal(err)
	}
	put := func(name string, secret credentials.Secret) credentials.Connection {
		stored, err := env.creds.PutConnection(ctx, credentials.Connection{NodeID: node.ID, Name: name, AuthKind: credentials.AuthOAuth, Identity: mail, Enabled: true, Secret: secret})
		if err != nil {
			t.Fatal(err)
		}
		return connectionByID(t, env.creds, stored.ID)
	}
	legacy := map[string]string{"authMethod": "access_token", "chatgptAccountId": "acct-1", "chatgptPlanType": "plus"}
	tokenOnly := put("legacy token", credentials.Secret{AccessToken: "legacy-1", ProviderData: legacy})
	withRefresh := put("legacy bulk", credentials.Secret{AccessToken: "legacy-2", RefreshToken: "legacy-r", ProviderData: legacy})

	signedIn := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply(mail, "acct-1", "a3", "r3"))
	if bulk, _ := codexBulkOp(mail, "acct-1", "a4")(t, env); bulk != signedIn["id"] {
		t.Fatalf("bulk item did not update the sign-in row: %s vs %v", bulk, signedIn["id"])
	}
	for _, before := range []credentials.Connection{tokenOnly, withRefresh} {
		after := connectionByID(t, env.creds, before.ID)
		if after.Name != before.Name || after.Secret.AccessToken != before.Secret.AccessToken || after.Secret.RefreshToken != before.Secret.RefreshToken ||
			fmt.Sprint(after.Secret.ProviderData) != fmt.Sprint(before.Secret.ProviderData) || after.Secret.AuthType != "" {
			t.Fatalf("legacy connection rewritten: before=%+v after=%+v", before, after)
		}
	}
	if got := env.count(t, "codex"); got != 3 {
		t.Fatalf("connections=%d, want 3", got)
	}
}

// The manual form can neither set nor clear how a connection was signed in: a
// token import stays one after an edit that claims oauth, and a bulk row whose
// exported data says authMethod access_token stays matchable after an edit that
// replaces its token.
func TestCodexManualEditKeepsAuthType(t *testing.T) {
	env := newCodexEnv(t)
	const mail = "shared@example.invalid"
	tokenRow, _ := tokenImportOp(mail, "acct-1")(t, env)
	bulkRow, _ := exportedTokenRowOp("other@example.invalid", "acct-2")(t, env)
	for id, body := range map[string]string{
		tokenRow: `{"secret":{"accessToken":"edited-1","authType":"oauth"}}`,
		bulkRow:  `{"secret":{"accessToken":"edited-2","authType":"access_token"}}`,
	} {
		if recorder := doJSON(t, env.mux, env.cookie, http.MethodPatch, "/admin/v1/connections/"+id, body); recorder.Code != http.StatusOK {
			t.Fatalf("patch status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	if got := connectionByID(t, env.creds, tokenRow).Secret; got.AuthType != credentials.AuthTypeAccessToken || got.AccessToken != "edited-1" {
		t.Fatalf("token import after edit: authType=%q", got.AuthType)
	}
	if got := connectionByID(t, env.creds, bulkRow).Secret; got.AuthType != credentials.AuthTypeOAuth || got.AccessToken != "edited-2" {
		t.Fatalf("bulk row after edit: authType=%q", got.AuthType)
	}
	if saved := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply(mail, "acct-1", "a3", "r3")); saved["id"] == tokenRow {
		t.Fatal("sign-in matched the token import")
	}
	if saved := codexSignIn(t, env.mux, env.cookie, env.exchange, codexReply("other@example.invalid", "acct-2", "a4", "r4")); saved["id"] != bulkRow {
		t.Fatalf("sign-in did not update the bulk row: %v", saved["id"])
	}
}

// Every Codex onboarding path records how it signed in: sign-in and bulk oauth,
// token import and a pasted raw token access_token (connectionsRepo.js:232,264;
// bulk-import/route.js:106-108; import-token/route.js:73; [provider]/[action]/route.js:438).
func TestCodexOnboardingRecordsAuthType(t *testing.T) {
	env := newCodexEnv(t)
	for name, tc := range map[string]struct {
		op   codexOp
		want string
	}{
		"sign-in":        {signInOp("a@example.invalid", "acct-a", "a1"), credentials.AuthTypeOAuth},
		"bulk":           {codexBulkOp("b@example.invalid", "acct-b", "b1"), credentials.AuthTypeOAuth},
		"exported token": {exportedTokenRowOp("c@example.invalid", "acct-c"), credentials.AuthTypeOAuth},
		"token import":   {tokenImportOp("d@example.invalid", "acct-d"), credentials.AuthTypeAccessToken},
		"raw token":      {rawTokenSignInOp("e@example.invalid", "acct-e"), credentials.AuthTypeAccessToken},
	} {
		id, status := tc.op(t, env)
		if id == "" {
			t.Fatalf("%s: status=%d", name, status)
		}
		if got := connectionByID(t, env.creds, id).Secret.AuthType; got != tc.want {
			t.Fatalf("%s: authType=%q, want %q", name, got, tc.want)
		}
	}
}
