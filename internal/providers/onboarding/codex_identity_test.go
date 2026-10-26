package onboarding

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

// TestCodexIdentityMatchesReference pins Codex connection identity to the
// reference's observed behaviour at 9router 1c3b9f47 (#239). Each row names the
// reference lines it reproduces; a change to how Routeweft reads an identity or
// the ChatGPT account claims out of a Codex credential fails a row. Identity is
// what the reference stores as the connection's email; empty means it stores
// none. There is deliberately no row where the ChatGPT account id becomes the
// identity: the reference has no such fallback.
func TestCodexIdentityMatchesReference(t *testing.T) {
	type want struct {
		identity string
		data     map[string]string
	}
	opaque := "opaque-not-a-jwt"

	signIn := []struct {
		name, ref        string
		idToken, access  string
		want             want
		noIDTokenInReply bool
	}{
		{name: "id token email", ref: "codex.js:49,57 providerHelpers.js:78",
			idToken: fixtureJWT(t, `{"email":"id@example.invalid","https://api.openai.com/auth":{"chatgpt_account_id":"acct-1","chatgpt_plan_type":"plus"}}`), access: opaque,
			want: want{"id@example.invalid", map[string]string{"chatgptAccountId": "acct-1", "chatgptPlanType": "plus"}}},
		{name: "profile claim is not read on sign-in", ref: "providerHelpers.js:73-82",
			idToken: fixtureJWT(t, `{"https://api.openai.com/profile":{"email":"profile@example.invalid"}}`), access: opaque,
			want: want{"", nil}},
		{name: "access token email", ref: "codex.js:57 providerHelpers.js:50",
			idToken: fixtureJWT(t, `{}`), access: fixtureJWT(t, `{"email":"access@example.invalid","preferred_username":"user","sub":"auth0|sub"}`),
			want: want{"access@example.invalid", nil}},
		{name: "access token preferred_username", ref: "providerHelpers.js:50",
			idToken: fixtureJWT(t, `{}`), access: fixtureJWT(t, `{"preferred_username":"user","sub":"auth0|sub"}`),
			want: want{"user", nil}},
		{name: "access token sub", ref: "providerHelpers.js:50",
			idToken: fixtureJWT(t, `{}`), access: fixtureJWT(t, `{"sub":"auth0|sub"}`),
			want: want{"auth0|sub", nil}},
		{name: "account id alone names no email", ref: "codex.js:57-64",
			idToken: fixtureJWT(t, `{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1"}}`), access: opaque,
			want: want{"", map[string]string{"chatgptAccountId": "acct-1"}}},
		{name: "account claims come from the id token only", ref: "codex.js:49,59",
			noIDTokenInReply: true, access: fixtureJWT(t, `{"sub":"auth0|sub","https://api.openai.com/auth":{"chatgpt_account_id":"acct-access"}}`),
			want: want{"auth0|sub", nil}},
		{name: "top-level account_id and plan_type", ref: "providerHelpers.js:79-80",
			idToken: fixtureJWT(t, `{"account_id":"acct-top","plan_type":"pro"}`), access: opaque,
			want: want{"", map[string]string{"chatgptAccountId": "acct-top", "chatgptPlanType": "pro"}}},
	}
	for _, row := range signIn {
		reply := `{"access_token":"` + row.access + `","refresh_token":"fixture-refresh"`
		if !row.noIDTokenInReply {
			reply += `,"id_token":"` + row.idToken + `"`
		}
		reply += `}`
		stub := &stubClient{handler: func(*http.Request) (int, string) { return http.StatusOK, reply }}
		result, err := flowFor(t, "codex").Exchange(context.Background(), Params{Verifier: "fixture-verifier", Input: "fixture-code", Client: stub})
		if err != nil {
			t.Fatalf("sign-in %q: %v", row.name, err)
		}
		if result.Identity != row.want.identity || !sameData(result.Secret.ProviderData, row.want.data) {
			t.Errorf("sign-in %q (%s): identity=%q data=%v, want %q %v", row.name, row.ref, result.Identity, result.Secret.ProviderData, row.want.identity, row.want.data)
		}
	}

	bulk := []struct {
		name, ref string
		item      map[string]any
		want      want
	}{
		{name: "exported email wins", ref: "bulk-import/route.js:78,84",
			item: map[string]any{"email": "given@example.invalid", "accessToken": fixtureJWT(t, `{"email":"jwt@example.invalid"}`)},
			want: want{"given@example.invalid", map[string]string{}}},
		{name: "id token email", ref: "bulk-import/route.js:83-84",
			item: map[string]any{"idToken": fixtureJWT(t, `{"email":"id@example.invalid"}`), "accessToken": fixtureJWT(t, `{"email":"access@example.invalid"}`)},
			want: want{"id@example.invalid", map[string]string{}}},
		{name: "an id token without email hides the access token", ref: "bulk-import/route.js:83",
			item: map[string]any{"idToken": fixtureJWT(t, `{}`), "accessToken": fixtureJWT(t, `{"email":"access@example.invalid"}`)},
			want: want{"", map[string]string{}}},
		{name: "access token email without id token", ref: "bulk-import/route.js:83",
			item: map[string]any{"accessToken": fixtureJWT(t, `{"email":"access@example.invalid"}`)},
			want: want{"access@example.invalid", map[string]string{}}},
		{name: "account id alone names no email", ref: "bulk-import/route.js:76-94",
			item: map[string]any{"accessToken": fixtureJWT(t, `{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1","chatgpt_plan_type":"plus"}}`)},
			want: want{"", map[string]string{"chatgptAccountId": "acct-1", "chatgptPlanType": "plus"}}},
		{name: "preferred_username and sub are not read", ref: "bulk-import/route.js:83 providerHelpers.js:78",
			item: map[string]any{"accessToken": fixtureJWT(t, `{"preferred_username":"user","sub":"auth0|sub"}`)},
			want: want{"", map[string]string{}}},
		{name: "exported account data wins", ref: "bulk-import/route.js:77-91",
			item: map[string]any{"accessToken": fixtureJWT(t, `{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-jwt","chatgpt_plan_type":"plus"}}`), "providerSpecificData": map[string]any{"chatgptAccountId": "acct-exported"}},
			want: want{"", map[string]string{"chatgptAccountId": "acct-exported", "chatgptPlanType": "plus"}}},
	}
	for _, row := range bulk {
		account, err := codexBulkAccount(row.item)
		if err != nil {
			t.Fatalf("bulk %q: %v", row.name, err)
		}
		if account.Identity != row.want.identity || !sameData(account.Secret.ProviderData, row.want.data) {
			t.Errorf("bulk %q (%s): identity=%q data=%v, want %q %v", row.name, row.ref, account.Identity, account.Secret.ProviderData, row.want.identity, row.want.data)
		}
	}

	imports := []struct {
		name, ref string
		token     string
		want      want
	}{
		{name: "profile email", ref: "import-token/route.js:40-41",
			token: fixtureJWT(t, `{"https://api.openai.com/profile":{"email":"profile@example.invalid"},"email":"top@example.invalid"}`),
			want:  want{"profile@example.invalid", map[string]string{"authMethod": "access_token"}}},
		{name: "top-level email", ref: "import-token/route.js:41",
			token: fixtureJWT(t, `{"email":"top@example.invalid","preferred_username":"user"}`),
			want:  want{"top@example.invalid", map[string]string{"authMethod": "access_token"}}},
		{name: "preferred_username", ref: "import-token/route.js:41",
			token: fixtureJWT(t, `{"preferred_username":"user","sub":"auth0|sub"}`),
			want:  want{"user", map[string]string{"authMethod": "access_token"}}},
		{name: "sub is not read", ref: "import-token/route.js:41,61-66",
			token: fixtureJWT(t, `{"sub":"auth0|sub"}`),
			want:  want{"", map[string]string{"authMethod": "access_token"}}},
		{name: "account id alone names no email", ref: "import-token/route.js:43-48,61-66",
			token: fixtureJWT(t, `{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-1","chatgpt_plan_type":"plus"}}`),
			want:  want{"", map[string]string{"authMethod": "access_token", "chatgptAccountId": "acct-1", "chatgptPlanType": "plus"}}},
		{name: "top-level account_id only when no email", ref: "import-token/route.js:61-66 providerHelpers.js:79",
			token: fixtureJWT(t, `{"account_id":"acct-top"}`),
			want:  want{"", map[string]string{"authMethod": "access_token", "chatgptAccountId": "acct-top"}}},
		{name: "top-level account_id ignored beside an email", ref: "import-token/route.js:43-48",
			token: fixtureJWT(t, `{"email":"top@example.invalid","account_id":"acct-top"}`),
			want:  want{"top@example.invalid", map[string]string{"authMethod": "access_token"}}},
		{name: "not a JWT", ref: "import-token/route.js:55-57",
			token: opaque,
			want:  want{"", map[string]string{"authMethod": "access_token"}}},
	}
	importFlow := ManualFlows()["codex"]
	for _, row := range imports {
		result, err := importFlow.Import(context.Background(), Params{Input: row.token})
		if err != nil {
			t.Fatalf("import %q: %v", row.name, err)
		}
		if result.Identity != row.want.identity || !sameData(result.Secret.ProviderData, row.want.data) {
			t.Errorf("import %q (%s): identity=%q data=%v, want %q %v", row.name, row.ref, result.Identity, result.Secret.ProviderData, row.want.identity, row.want.data)
		}
		if result.FallbackName != "ChatGPT Access Token" {
			t.Errorf("import %q (import-token/route.js:68): fallback name=%q", row.name, result.FallbackName)
		}
	}
}

// Only Codex stores a connection without an identity; every other flow still
// refuses one, so the store's optional identity cannot leak to them unnoticed.
func TestOnlyCodexStoresWithoutIdentity(t *testing.T) {
	flows := append(builtinFlows(), func() []Flow {
		var manual []Flow
		for _, flow := range ManualFlows() {
			manual = append(manual, flow)
		}
		return manual
	}()...)
	optional := 0
	for _, flow := range flows {
		if !flow.IdentityOptional {
			continue
		}
		if flow.Provider != "codex" {
			t.Errorf("%s declares IdentityOptional; the reference stores no-email connections only for Codex in #239's scope", flow.Provider)
		}
		if !flow.ResolvesIdentity {
			t.Errorf("%s declares IdentityOptional without ResolvesIdentity, so the dashboard would ask for an identity it may not need", flow.Provider)
		}
		optional++
	}
	if optional != 2 {
		t.Fatalf("want the Codex browser and import flows to declare IdentityOptional, got %d flows", optional)
	}
}

func sameData(got, want map[string]string) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	return reflect.DeepEqual(got, want)
}
