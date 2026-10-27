package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// #218: specialized credential import. Pasted or file-chosen credentials are
// bounded and schema-checked, a malformed one is named without quoting it, the
// same account imported again updates its connection as the reference's insert
// does, an already-expired credential is stored and reported, and nothing the
// operator imported comes back in a response or a log line.

// importEnv is the onboarding API with every specialized import provider.
type importEnv struct {
	mux      *http.ServeMux
	creds    *credentials.Store
	cookie   *http.Cookie
	exchange *routeExchange
}

func newImportEnv(t *testing.T, routes map[string]string) *importEnv {
	t.Helper()
	exchange := &routeExchange{routes: routes}
	h, mux, creds, cookie := newOnboardingAPI(t, exchange)
	addBuiltinSpecs(h, "kiro", "xiaomi-mimo", "iflow", "grok-cli")
	return &importEnv{mux: mux, creds: creds, cookie: cookie, exchange: exchange}
}

// addBuiltinSpecs gives the handler the catalog spec of each provider it lacks.
func addBuiltinSpecs(h *Handler, ids ...string) {
	for _, spec := range registry.Builtins() {
		for _, id := range ids {
			if _, known := h.providerSpec(id); spec.ID == id && !known {
				h.opts.Providers = append(h.opts.Providers, spec)
			}
		}
	}
}

// importBody is an /import request carrying input as a JSON string.
func importBody(t *testing.T, identity, input string, meta map[string]string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"identity": identity, "input": input, "meta": meta})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (env *importEnv) importAs(t *testing.T, provider, identity, input string, meta map[string]string) (int, map[string]any) {
	t.Helper()
	recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/import", importBody(t, identity, input, meta))
	var body map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	return recorder.Code, body
}

func (env *importEnv) connections(t *testing.T, provider string) []credentials.Connection {
	t.Helper()
	connections, err := env.creds.ListConnections(context.Background(), provider)
	if err != nil {
		t.Fatal(err)
	}
	return connections
}

// assertNoSecret fails when text carries any of the secrets.
func assertNoSecret(t *testing.T, where, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("%s carries credential material %q: %s", where, secret, text)
		}
	}
}

// kiroCLIProxyExport is a sanitized CLIProxyAPI external_idp export, pretty-printed
// as the file is, with a token pair long enough to pass the old 4 KiB bound.
func kiroCLIProxyExport(access, refresh string) string {
	document := map[string]any{
		"type": "kiro", "auth_method": "external_idp", "email": "kiro@example.invalid",
		"access_token": access, "refresh_token": refresh, "client_id": "fixture-client",
		"token_endpoint": "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		"scopes": []string{"api://fixture/codewhisperer:completions", "offline_access"},
		"profile_arn": "arn:aws:codewhisperer:us-east-1:000000000000:profile/FIXTURE",
		"expires_in": 3600,
	}
	raw, _ := json.MarshalIndent(document, "", "  ")
	return string(raw)
}

// A whole exported file arrives with its line breaks, and may be larger than a
// pasted code; it imports. Line breaks outside a JSON document, any other control
// character, and a payload past the bound are refused before any provider call.
func TestImportAcceptsAMultilineFileAndBoundsTheInput(t *testing.T) {
	env := newImportEnv(t, map[string]string{})
	access, refresh := "fixture-ms-access-"+strings.Repeat("a", 3000), "fixture-ms-refresh-"+strings.Repeat("r", 2000)
	export := kiroCLIProxyExport(access, refresh)
	if len(export) <= oauthMaxInput || !strings.Contains(export, "\n") {
		t.Fatalf("fixture must be a multiline file past the pasted-code bound, got %d bytes", len(export))
	}
	status, body := env.importAs(t, "kiro", "kiro@example.invalid", export, nil)
	if status != http.StatusOK || body["outcome"] != onboardedAdded || body["identity"] != "kiro@example.invalid" {
		t.Fatalf("multiline export: %d %v", status, body)
	}
	raw, _ := json.Marshal(body)
	assertNoSecret(t, "import response", string(raw), access, refresh)
	stored := env.connections(t, "kiro")
	if len(stored) != 1 || stored[0].Secret.AccessToken != access || stored[0].Secret.RefreshToken != refresh || stored[0].Secret.ProviderData["authMethod"] != "external_idp" {
		t.Fatalf("stored: %+v", stored)
	}

	for _, tc := range []struct {
		name, input, code string
		status            int
	}{
		{"line break in a bare key", "sk-fixture\nsecond", "import_invalid", http.StatusBadRequest},
		{"NUL inside a JSON document", "{\"refreshToken\":\"x\u0000\"}", "import_invalid", http.StatusBadRequest},
		{"past the import bound", "{\"refreshToken\":\"" + strings.Repeat("r", oauthMaxImportInput) + "\"}", "import_too_large", http.StatusRequestEntityTooLarge},
		{"blank", "  \n ", "oauth_code_missing", http.StatusBadRequest},
	} {
		calls := len(env.exchange.seenURLs)
		status, body := env.importAs(t, "kiro", "kiro@example.invalid", tc.input, nil)
		if status != tc.status || apiErrorCode(body) != tc.code {
			t.Fatalf("%s: %d %v, want %d %s", tc.name, status, body, tc.status, tc.code)
		}
		if len(env.exchange.seenURLs) != calls {
			t.Fatalf("%s: a refused import reached the provider", tc.name)
		}
	}
}

// A paste or file of the wrong shape is named (400 import_invalid) with fixed
// text, never quoting it, and nothing is sent to the provider or stored. The
// method-specific required fields are the pin's.
func TestImportNamesTheMissingFieldWithoutQuotingTheInput(t *testing.T) {
	env := newImportEnv(t, map[string]string{})
	const marker = "fixture-secret-marker"
	cases := []struct {
		provider, input, message string
		meta                     map[string]string
	}{
		{"kiro", `{"refresh_token":"` + marker + `","token_endpoint":"https://login.microsoftonline.com/x","client_id":"c","scopes":"s","profile_arn":"arn"}`, "access_token is required", nil},
		{"kiro", `{"access_token":"` + marker + `","refresh_token":"r","client_id":"c","scopes":"s","profile_arn":"arn","token_endpoint":"https://attacker.example.invalid/token"}`, "token_endpoint must be a Microsoft login endpoint", nil},
		{"kiro", `{"accessToken":"` + marker + `"}`, "kiro refresh token is required", nil},
		{"kiro", `{"refreshToken":` + marker, "kiro import expects a JSON object", nil},
		{"xiaomi-mimo", `{"xiaomi":{"key":"` + marker + `"}}`, "xiaomi-mimo key must start with sk-", nil},
		{"xiaomi-mimo", `{"apiKey":"sk-` + marker + `","mimoPassToken":"bad;value"}`, "xiaomi-mimo account credential contains an invalid cookie value", nil},
		{"iflow", "session=" + marker, "iflow cookie must contain BXAuth", nil},
		{"grok-cli", `{"refresh_token":"` + marker + `"}`, "grok-cli import requires an access token", nil},
		{"cursor", marker, "cursor token appears too short", map[string]string{"machineId": fixtureCursorMachineID}},
		{"cursor", fixtureCursorToken, "cursor machine id must be a UUID", map[string]string{"machineId": "not-a-machine-id"}},
	}
	for _, tc := range cases {
		recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/"+tc.provider+"/import", importBody(t, "named@example.invalid", tc.input, tc.meta))
		var body map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		failure, _ := body["error"].(map[string]any)
		if recorder.Code != http.StatusBadRequest || apiErrorCode(body) != "import_invalid" || failure["message"] != tc.message {
			t.Fatalf("%s %q: %d %s", tc.provider, tc.message, recorder.Code, recorder.Body.String())
		}
		assertNoSecret(t, "error response", recorder.Body.String(), marker, fixtureCursorToken)
		if n := len(env.connections(t, tc.provider)); n != 0 {
			t.Fatalf("%s: a refused import stored %d connections", tc.provider, n)
		}
	}
	if len(env.exchange.seenURLs) != 0 {
		t.Fatalf("malformed imports reached the provider: %v", env.exchange.seenURLs)
	}
	// Cursor's machine id is a required field of its own, as in the pin.
	status, body := env.importAs(t, "cursor", "", fixtureCursorToken, nil)
	if status != http.StatusBadRequest || apiErrorCode(body) != "oauth_meta_required" {
		t.Fatalf("cursor without a machine id: %d %v", status, body)
	}
}

// A credential the provider refuses (an invalid, expired or revoked refresh
// token, a rejected cookie) is a 502 that says so without the provider's text,
// and stores nothing.
func TestImportReportsAProviderRejectionWithoutDetail(t *testing.T) {
	env := newImportEnv(t, map[string]string{
		"refreshToken": `{"message":"Invalid refresh token fixture-upstream-echo"}`,
		"apikey":       `{"success":false,"message":"cookie expired fixture-upstream-echo"}`,
	})
	env.exchange.status = http.StatusUnauthorized
	for _, tc := range []struct{ provider, input string }{
		{"kiro", `{"refreshToken":"fixture-expired-refresh"}`},
		{"iflow", "BXAuth=fixture-expired-cookie;"},
	} {
		status, body := env.importAs(t, tc.provider, "named@example.invalid", tc.input, nil)
		if status != http.StatusBadGateway || apiErrorCode(body) != "oauth_exchange_failed" {
			t.Fatalf("%s: %d %v", tc.provider, status, body)
		}
		raw, _ := json.Marshal(body)
		assertNoSecret(t, "rejection", string(raw), "fixture-upstream-echo", "fixture-expired-refresh", "fixture-expired-cookie")
		if n := len(env.connections(t, tc.provider)); n != 0 {
			t.Fatalf("%s: a rejected credential was stored", tc.provider)
		}
	}
}

// The same account imported again updates its connection in place, keeping the
// operator's name, priority and enabled state, as the reference's insert does
// for an oauth email; a Xiaomi key keeps a stored passToken the new one lacks.
func TestImportOfAStoredAccountUpdatesIt(t *testing.T) {
	env := newImportEnv(t, map[string]string{"/models": `{"data":[{"id":"mimo"}]}`})
	grok := func(access string) string {
		return `{"access_token":"` + access + `","refresh_token":"refresh-` + access + `","email":"grok@example.invalid"}`
	}
	status, first := env.importAs(t, "grok-cli", "grok@example.invalid", grok("g1"), nil)
	if status != http.StatusOK || first["outcome"] != onboardedAdded {
		t.Fatalf("first: %d %v", status, first)
	}
	id := first["id"].(string)
	if recorder := doJSON(t, env.mux, env.cookie, http.MethodPatch, "/admin/v1/connections/"+id, `{"name":"Work","enabled":false,"priority":4}`); recorder.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", recorder.Code, recorder.Body.String())
	}
	status, again := env.importAs(t, "grok-cli", "grok@example.invalid", grok("g2"), nil)
	if status != http.StatusOK || again["outcome"] != onboardedUpdated || again["id"] != id {
		t.Fatalf("again: %d %v", status, again)
	}
	got := connectionByID(t, env.creds, id)
	if got.Secret.AccessToken != "g2" || got.Secret.RefreshToken != "refresh-g2" || got.Name != "Work" || got.Enabled || got.Priority != 4 {
		t.Fatalf("updated row: %+v", got)
	}
	if n := len(env.connections(t, "grok-cli")); n != 1 {
		t.Fatalf("connections=%d, want 1", n)
	}

	xiaomi := func(key, pass string) string {
		return `{"apiKey":"` + key + `","uid":"7","mimoPassToken":"` + pass + `"}`
	}
	if status, body := env.importAs(t, "xiaomi-mimo", "7@xiaomi", xiaomi("sk-first", "pass-1"), nil); status != http.StatusOK || body["identity"] != "7@xiaomi" {
		t.Fatalf("xiaomi first: %d %v", status, body)
	}
	status, body := env.importAs(t, "xiaomi-mimo", "7@xiaomi", `{"apiKey":"sk-second","uid":"7"}`, nil)
	if status != http.StatusOK || body["outcome"] != onboardedUpdated {
		t.Fatalf("xiaomi again: %d %v", status, body)
	}
	rows := env.connections(t, "xiaomi-mimo")
	if len(rows) != 1 || rows[0].Secret.AccessToken != "sk-second" || rows[0].Secret.ProviderData["mimoPassToken"] != "pass-1" {
		t.Fatalf("xiaomi rows: %+v", rows)
	}
}

// An import the reference would keep as a second row is refused with a named
// conflict, and the stored connection is left untouched: the node holds one
// connection per identity. The reference keeps a second row for an iFlow cookie
// and a Kiro API key (neither route dedups) and for an oauth account whose
// username differs (connectionsRepo.js:250-259).
func TestImportIdentityCollisionIsRefusedNotOverwritten(t *testing.T) {
	env := newImportEnv(t, map[string]string{
		"apikey":              `{"success":true,"data":{"name":"flow-key","apiKey":"sk-iflow-1"}}`,
		"ListAvailableModels": `{"models":[{"modelId":"fixture"}]}`,
		"api/v4/user":         `{"username":"first","email":"lab@example.invalid","name":"Lab"}`,
	})
	steps := []struct{ provider, identity, first, second string }{
		{"iflow", "", "BXAuth=fixture-one;", "BXAuth=fixture-two;"},
		{"kiro", "kiro@example.invalid", "fixture-kiro-key-one", "fixture-kiro-key-two"},
	}
	for _, step := range steps {
		if status, body := env.importAs(t, step.provider, step.identity, step.first, nil); status != http.StatusOK {
			t.Fatalf("%s first: %d %v", step.provider, status, body)
		}
		status, body := env.importAs(t, step.provider, step.identity, step.second, nil)
		if status != http.StatusConflict || apiErrorCode(body) != "identity_conflict" {
			t.Fatalf("%s again: %d %v", step.provider, status, body)
		}
		rows := env.connections(t, step.provider)
		if len(rows) != 1 || (rows[0].Secret.Cookie != "BXAuth=fixture-one;" && rows[0].Secret.AccessToken != "fixture-kiro-key-one") {
			t.Fatalf("%s: stored credential changed: %+v", step.provider, rows)
		}
	}

	if status, body := env.importAs(t, "gitlab", "", "glpat-first", nil); status != http.StatusOK || body["outcome"] != onboardedAdded {
		t.Fatalf("gitlab first: %d %v", status, body)
	}
	// The same account and username again updates it.
	if status, body := env.importAs(t, "gitlab", "", "glpat-again", nil); status != http.StatusOK || body["outcome"] != onboardedUpdated {
		t.Fatalf("gitlab same username: %d %v", status, body)
	}
	env.exchange.routes["api/v4/user"] = `{"username":"second","email":"lab@example.invalid","name":"Lab"}`
	status, body := env.importAs(t, "gitlab", "", "glpat-other", nil)
	if status != http.StatusConflict || apiErrorCode(body) != "identity_conflict" {
		t.Fatalf("gitlab other username: %d %v", status, body)
	}
	if rows := env.connections(t, "gitlab"); len(rows) != 1 || rows[0].Secret.AccessToken != "glpat-again" {
		t.Fatalf("gitlab rows: %+v", rows)
	}
}

// Within one bulk paste: a stored account updates, a repeated account updates the
// row its first copy added, a malformed item fails with its index, an expired
// item is stored and flagged, and no item's error is the store's own text.
func TestGrokBulkImportReportsEachItem(t *testing.T) {
	env := newImportEnv(t, map[string]string{})
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if status, body := env.importAs(t, "grok-cli", "a@example.invalid", `{"access_token":"tok-stored","email":"a@example.invalid"}`, nil); status != http.StatusOK {
		t.Fatalf("seed: %d %v", status, body)
	}
	batch := `[
		{"access_token":"tok-a2","refresh_token":"tok-ra2","email":"a@example.invalid"},
		{"access_token":"tok-b1","email":"b@example.invalid"},
		{"access_token":"tok-b2","email":"b@example.invalid"},
		{"refresh_token":"tok-no-access"},
		"not an object",
		{"access_token":"tok-c1","email":"c@example.invalid","expires_at":"` + past + `"},
		{"access_token":"tok-d1","refresh_token":"tok-rd1","email":"d@example.invalid","expiresAt":"` + past + `"}
	]`
	recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/grok-cli/bulk-import", batch)
	if recorder.Code != http.StatusOK {
		t.Fatalf("bulk: %d %s", recorder.Code, recorder.Body.String())
	}
	assertNoSecret(t, "bulk response", recorder.Body.String(), "tok-")
	var response struct {
		Total, Success, Failed int
		Results                []struct {
			Index       int    `json:"index"`
			OK          bool   `json:"ok"`
			Outcome     string `json:"outcome"`
			Expired     bool   `json:"expired"`
			Refreshable *bool  `json:"refreshable"`
			Error       string `json:"error"`
		}
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Total != 7 || response.Success != 5 || response.Failed != 2 || len(response.Results) != 7 {
		t.Fatalf("totals: %+v", response)
	}
	want := []struct {
		ok      bool
		outcome string
		expired bool
	}{{true, onboardedUpdated, false}, {true, onboardedAdded, false}, {true, onboardedUpdated, false}, {false, "", false}, {false, "", false}, {true, onboardedAdded, true}, {true, onboardedAdded, true}}
	for index, item := range response.Results {
		if item.Index != index || item.OK != want[index].ok || item.Outcome != want[index].outcome || item.Expired != want[index].expired {
			t.Fatalf("item %d: %+v, want %+v", index, item, want[index])
		}
		if strings.Contains(strings.ToLower(item.Error), "constraint") || strings.Contains(strings.ToLower(item.Error), "sql") {
			t.Fatalf("item %d leaks the store's error: %q", index, item.Error)
		}
	}
	if response.Results[3].Error != "missing access_token / accessToken" || response.Results[4].Error != "item is not an object" {
		t.Fatalf("malformed item reasons: %q, %q", response.Results[3].Error, response.Results[4].Error)
	}
	if r := response.Results[5].Refreshable; r == nil || *r {
		t.Fatalf("expired item without a refresh token: refreshable=%v", r)
	}
	if r := response.Results[6].Refreshable; r == nil || !*r {
		t.Fatalf("expired item with a refresh token: refreshable=%v", r)
	}
	if n := len(env.connections(t, "grok-cli")); n != 4 {
		t.Fatalf("connections=%d, want 4 (a, b, c, d)", n)
	}
}

// A single import of an expired credential is stored, as the pin does, and the
// answer says it had expired and whether it can refresh.
func TestImportReportsAnExpiredCredential(t *testing.T) {
	env := newImportEnv(t, map[string]string{})
	past := time.Now().Add(-time.Hour).Unix()
	input, _ := json.Marshal(map[string]any{"access_token": "old", "email": "e@example.invalid", "expiresAt": past * 1000})
	status, body := env.importAs(t, "grok-cli", "e@example.invalid", string(input), nil)
	if status != http.StatusOK || body["expired"] != true || body["refreshable"] != false {
		t.Fatalf("expired import: %d %v", status, body)
	}
}

// Every import mode stores its credential sealed: no row of the database holds
// the plaintext, and no log line written during the imports names it.
func TestImportsAreSealedAndNeverLogged(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	exchange := &routeExchange{routes: map[string]string{
		"apikey":      `{"success":true,"data":{"name":"flow-key","apiKey":"sealed-iflow-key"}}`,
		"/models":     `{"data":[]}`,
		"api/v4/user": `{"username":"sealed","email":"sealed@example.invalid","name":"Sealed"}`,
	}}
	h, mux, sqlStore, creds, cookie := newProvidersAPI(t, false, nil)
	t.Cleanup(func() { sqlStore.Close() })
	h.opts.OAuthClient = exchange
	addBuiltinSpecs(h, "kiro", "xiaomi-mimo", "iflow", "grok-cli", "cursor", "gitlab")
	env := &importEnv{mux: mux, creds: creds, cookie: cookie, exchange: exchange}
	secrets := []string{"sealed-ms-access", "sealed-ms-refresh", "sk-sealed-xiaomi", "sealed-pass", "BXAuth=sealed-cookie", "sealed-iflow-key", "sealed-grok-access", "glpat-sealed", fixtureCursorToken}
	imports := []struct {
		provider, input string
		meta            map[string]string
	}{
		{"kiro", kiroCLIProxyExport("sealed-ms-access", "sealed-ms-refresh"), nil},
		{"xiaomi-mimo", `{"xiaomi":{"key":"sk-sealed-xiaomi","metadata":{"uid":"9"}},"mimoPassToken":"sealed-pass"}`, nil},
		{"iflow", "BXAuth=sealed-cookie; tracking=1", nil},
		{"grok-cli", `{"access_token":"sealed-grok-access","email":"g@example.invalid"}`, nil},
		{"gitlab", "glpat-sealed", nil},
		{"cursor", fixtureCursorToken, map[string]string{"machineId": fixtureCursorMachineID}},
	}
	for _, tc := range imports {
		recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/"+tc.provider+"/import", importBody(t, "sealed-"+tc.provider+"@example.invalid", tc.input, tc.meta))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.provider, recorder.Code, recorder.Body.String())
		}
		assertNoSecret(t, tc.provider+" response", recorder.Body.String(), secrets...)
		if n := len(env.connections(t, tc.provider)); n != 1 {
			t.Fatalf("%s: connections=%d", tc.provider, n)
		}
	}
	rows, err := sqlStore.DB().Query(`SELECT COALESCE(identity,''), name, COALESCE(secret_blob,'') FROM provider_connections`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var identity, name string
		var blob sql.NullString
		if err := rows.Scan(&identity, &name, &blob); err != nil {
			t.Fatal(err)
		}
		assertNoSecret(t, "stored row", identity+" "+name+" "+blob.String, secrets...)
	}
	assertNoSecret(t, "log output", logs.String(), secrets...)
}
