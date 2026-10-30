package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/quota"
	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

// #297: the quota page's per-account warming switch. Everything the page does is
// an ordinary admin API call (the quota list, GET and PATCH /admin/v1/settings), so
// this drives those calls with the bodies the page sends, through the real handler
// and a real store, then lets the real AutoPinger run against an injected sender.

func warmingRequest(t *testing.T, a *App, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(cookie)
	return c03Do(a, request)
}

func warmingSettings(t *testing.T, a *App, cookie *http.Cookie) map[string]string {
	t.Helper()
	response := warmingRequest(t, a, cookie, http.MethodGet, "/admin/v1/settings", "")
	if response.Code != http.StatusOK {
		t.Fatalf("settings %d %s", response.Code, response.Body)
	}
	var data struct {
		Settings map[string]string `json:"settings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return data.Settings
}

func TestQuotaWarmingSwitchRoundTripsThroughTheAdminAPIAndTheWarmer(t *testing.T) {
	ctx := context.Background()
	dir := storetest.DataDirWithAdmin(t, "operator", c03Password)
	boot := func() *App {
		a := New(Config{DataDir: dir, CredentialKey: make([]byte, 32)}, nil)
		if err := a.Initialize(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = a.store.Close() })
		return a
	}
	a := boot()
	codexNode, err := a.creds.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "codex", Name: "Codex", Transports: []string{"openai-responses"}})
	if err != nil {
		t.Fatal(err)
	}
	claudeNode, err := a.creds.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "claude", Name: "Claude", Transports: []string{"anthropic-messages"}})
	if err != nil {
		t.Fatal(err)
	}
	put := func(nodeID, name string, kind credentials.AuthKind) credentials.Connection {
		secret := credentials.Secret{AccessToken: "fixture-" + name}
		connection, err := a.creds.PutConnection(ctx, credentials.Connection{NodeID: nodeID, Name: name, Identity: name, Enabled: true, AuthKind: kind, Secret: secret})
		if err != nil {
			t.Fatal(err)
		}
		return connection
	}
	codexOnly := put(codexNode.ID, "Codex work", credentials.AuthOAuth)
	work := put(claudeNode.ID, "Claude work", credentials.AuthOAuth)
	spare := put(claudeNode.ID, "Claude spare", credentials.AuthOAuth)
	apiKey := put(claudeNode.ID, "Claude key", credentials.AuthAPIKey)
	// Boot again so the compiled snapshot and the warmer see the connections.
	_ = a.store.Close()
	a = boot()
	cookie := c03Login(t, a)

	// The page's list carries each connection's credential kind: that is what
	// decides which rows get a switch (OAuth Claude and Codex only).
	listed := warmingRequest(t, a, cookie, http.MethodGet, "/admin/v1/quota", "")
	var page struct {
		Items []struct {
			ConnectionID string `json:"connectionId"`
			ProviderID   string `json:"providerId"`
			AuthKind     string `json:"authKind"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil || len(page.Items) != 4 {
		t.Fatalf("quota list %d %s", listed.Code, listed.Body)
	}
	kinds := map[string]string{}
	for _, item := range page.Items {
		kinds[item.ConnectionID] = item.ProviderID + "/" + item.AuthKind
	}
	if kinds[codexOnly.ID] != "codex/oauth" || kinds[work.ID] != "claude/oauth" || kinds[spare.ID] != "claude/oauth" || kinds[apiKey.ID] != "claude/api-key" {
		t.Fatalf("kinds %v", kinds)
	}

	// Off for everyone by default, and nothing is warmed.
	defaults := warmingSettings(t, a, cookie)
	if defaults["claudeAutoPing"] != `{"connections":{}}` || defaults["codexAutoPing"] != `{"connections":{}}` {
		t.Fatalf("defaults %q %q", defaults["claudeAutoPing"], defaults["codexAutoPing"])
	}
	pinged := map[string]int{}
	// A Claude window that reset three minutes ago: an opted-in account is warmed.
	a.quota.Observer.Clients["claude"] = quotaFixture{reset: time.Now().Add(-3 * time.Minute)}
	arm := func(app *App) {
		app.autoPing.Send = func(_ context.Context, provider string, credential quota.Credential) error {
			pinged[credential.ConnectionID]++
			return nil
		}
	}
	arm(a)
	if err := a.autoPing.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pinged) != 0 {
		t.Fatalf("warming defaults on: %v", pinged)
	}

	// The page turns Claude work on: the same body it sends.
	patch := func(app *App, provider string, ids ...string) *httptest.ResponseRecorder {
		connections := map[string]bool{}
		for _, id := range ids {
			connections[id] = true
		}
		inner, _ := json.Marshal(map[string]any{"connections": connections})
		body, _ := json.Marshal(map[string]any{"set": map[string]string{provider + "AutoPing": string(inner)}})
		return warmingRequest(t, app, cookie, http.MethodPatch, "/admin/v1/settings", string(body))
	}
	if response := patch(a, "claude", work.ID); response.Code != http.StatusOK {
		t.Fatalf("patch %d %s", response.Code, response.Body)
	}
	if err := a.autoPing.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if pinged[work.ID] != 1 || pinged[spare.ID] != 0 || pinged[apiKey.ID] != 0 || len(pinged) != 1 {
		t.Fatalf("only the opted-in account is warmed: %v", pinged)
	}

	// It survives a restart: a new process reads the same choice back from the store.
	_ = a.store.Close()
	a = boot()
	cookie = c03Login(t, a)
	after := warmingSettings(t, a, cookie)
	if after["claudeAutoPing"] != `{"connections":{"`+work.ID+`":true}}` || after["codexAutoPing"] != `{"connections":{}}` {
		t.Fatalf("after restart claudeAutoPing=%q codexAutoPing=%q", after["claudeAutoPing"], after["codexAutoPing"])
	}

	// Off stops it; on again resumes. A later reset is a new window, so the
	// persisted suppression of the earlier one does not hide the difference.
	a.quota.Observer.Clients["claude"] = quotaFixture{reset: time.Now().Add(-time.Second)}
	arm(a)
	before := pinged[work.ID]
	if response := patch(a, "claude"); response.Code != http.StatusOK {
		t.Fatalf("patch off %d %s", response.Code, response.Body)
	}
	if err := a.autoPing.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if pinged[work.ID] != before {
		t.Fatalf("a switched-off account was warmed: %v", pinged)
	}
	if response := patch(a, "claude", work.ID, spare.ID); response.Code != http.StatusOK {
		t.Fatalf("patch on %d %s", response.Code, response.Body)
	}
	if err := a.autoPing.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var warmed []string
	for id, count := range pinged {
		if count > 0 {
			warmed = append(warmed, id)
		}
	}
	sort.Strings(warmed)
	want := []string{work.ID, spare.ID}
	sort.Strings(want)
	if strings.Join(warmed, ",") != strings.Join(want, ",") || pinged[apiKey.ID] != 0 {
		t.Fatalf("warmed %v, want %v and never the API-key account: %v", warmed, want, pinged)
	}

	// Codex has its own setting: opting an account in stores under codexAutoPing,
	// not claudeAutoPing, and survives alongside it.
	if response := patch(a, "codex", codexOnly.ID); response.Code != http.StatusOK {
		t.Fatalf("patch codex %d %s", response.Code, response.Body)
	}
	both := warmingSettings(t, a, cookie)
	if both["codexAutoPing"] != `{"connections":{"`+codexOnly.ID+`":true}}` || !strings.Contains(both["claudeAutoPing"], work.ID) || !strings.Contains(both["claudeAutoPing"], spare.ID) {
		t.Fatalf("settings %q %q", both["claudeAutoPing"], both["codexAutoPing"])
	}

	// A value the warmer cannot use is refused, not stored.
	if response := warmingRequest(t, a, cookie, http.MethodPatch, "/admin/v1/settings", `{"set":{"codexAutoPing":"{\"connections\":{\"x\":\"yes\"}}"}}`); response.Code != http.StatusBadRequest {
		t.Fatalf("bad value %d %s", response.Code, response.Body)
	}
}
