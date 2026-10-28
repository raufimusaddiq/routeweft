package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/quota"
)

type creditsHTTP func(*http.Request) (*http.Response, error)

func (f creditsHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestCodexCreditsOperatorAction(t *testing.T) {
	h, mux, store, creds, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	ctx := context.Background()
	node, err := creds.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "codex", Name: "Codex", Transports: []string{"openai-responses"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := creds.PutConnection(ctx, credentials.Connection{NodeID: node.ID, Name: "fixture", Identity: "fixture", AuthKind: credentials.AuthOAuth, Enabled: true, Secret: credentials.Secret{AccessToken: "fixture-secret", ProviderData: map[string]string{"workspaceId": "fixture-workspace"}}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/admin/v1/quota/" + c.ID + "/codex-reset-credits"
	calls := 0
	noCredit := false
	h.opts.OAuthClient = creditsHTTP(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer fixture-secret" || r.Header.Get("ChatGPT-Account-ID") != "fixture-workspace" {
			t.Fatal("missing account auth")
		}
		// Pin tests/unit/codex-reset-credits.test.js:45-62 (first credit).
		body := `{"available_count":2,"credits":[{"status":"available","granted_at":"2026-06-18T00:25:18Z","expires_at":"2026-07-18T00:25:18Z"}]}`
		if r.Method == "POST" {
			if !strings.HasSuffix(r.URL.Path, "/consume") {
				t.Fatal("wrong consume path")
			}
			var payload map[string]string
			if json.NewDecoder(r.Body).Decode(&payload) != nil {
				t.Fatal("bad body")
			}
			if _, err := uuid.Parse(payload["redeem_request_id"]); err != nil {
				t.Fatal("missing server id")
			}
			body = `{"code":"reset","windows_reset":1}`
			if noCredit {
				body = `{"code":"no_credit","windows_reset":0}`
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	forgotten := []string{}
	h.opts.QuotaForget = func(id string) { forgotten = append(forgotten, id) }
	for _, method := range []string{"GET", "POST"} {
		if got := doJSON(t, mux, nil, method, path, `{"confirm":true}`); got.Code != 401 {
			t.Fatalf("anonymous=%d", got.Code)
		}
	}
	if got := doJSON(t, mux, cookie, "POST", path, `{"confirm":false}`); got.Code != 400 {
		t.Fatal("missing confirmation accepted")
	}
	if calls != 0 {
		t.Fatal("network before validation")
	}
	got := doJSON(t, mux, cookie, "GET", path, "")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"availableCount":2`) || strings.Contains(got.Body.String(), "fixture-secret") {
		t.Fatalf("GET: %d %s", got.Code, got.Body)
	}
	got = doJSON(t, mux, cookie, "POST", path, `{"confirm":true}`)
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"ok":true`) {
		t.Fatalf("POST: %d %s", got.Code, got.Body)
	}
	if len(forgotten) != 1 || forgotten[0] != c.ID {
		t.Fatalf("cached quota not invalidated after spend: %v", forgotten)
	}
	apiKey, err := creds.PutConnection(ctx, credentials.Connection{NodeID: node.ID, Name: "key", Identity: "key", AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: "fixture-key"}})
	if err != nil {
		t.Fatal(err)
	}
	before := calls
	for _, method := range []string{"GET", "POST"} {
		if got := doJSON(t, mux, cookie, method, "/admin/v1/quota/"+apiKey.ID+"/codex-reset-credits", `{"confirm":true}`); got.Code != 400 {
			t.Fatalf("api-key %s=%d", method, got.Code)
		}
	}
	if calls != before {
		t.Fatal("API key sent upstream as bearer")
	}
	noCredit = true
	if got := doJSON(t, mux, cookie, "POST", path, `{"confirm":true}`); got.Code != 409 {
		t.Fatalf("no credit=%d", got.Code)
	}
	c.ProviderID = ""
	c.NodeID = node.ID
	other, err := creds.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "openai", Name: "other", Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	c.ID = ""
	c.NodeID = other.ID
	c, err = creds.PutConnection(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	before = calls
	if got := doJSON(t, mux, cookie, "POST", "/admin/v1/quota/"+c.ID+"/codex-reset-credits", `{"confirm":true}`); got.Code != 400 {
		t.Fatalf("wrong provider=%d", got.Code)
	}
	if calls != before {
		t.Fatal("called wrong provider")
	}
}

func TestQuotaReadIncludesNamedWindowsAndCredits(t *testing.T) {
	h, mux, store, creds, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	ctx := context.Background()
	node, err := creds.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "codex", Name: "Codex", Transports: []string{"openai-responses"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := creds.PutConnection(ctx, credentials.Connection{NodeID: node.ID, Name: "fixture", Identity: "fixture", AuthKind: credentials.AuthOAuth, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	credits := 2.0
	h.opts.QuotaState = func(id string) (quota.State, bool) {
		return quota.State{Plan: "plus", AvailableReset: &credits, Windows: []quota.Window{{Name: "session"}}, Err: "secret must not be serialized"}, id == c.ID
	}
	got := doJSON(t, mux, cookie, "GET", "/admin/v1/quota", "")
	if got.Code != 200 || !containsAll(got.Body.String(), `"windows"`, `"name":"session"`, `"availableResetCredits":2`, `"plan":"plus"`) || strings.Contains(got.Body.String(), "secret must") {
		t.Fatalf("quota: %d %s", got.Code, got.Body)
	}
}

// TestQuotaReadKeepsDeepSeekCurrenciesApart is the #212 read-model check: each
// currency's balance and state reach the API separately, a zero balance is a
// zero and not unlimited, and the provider's availability flag is explicit.
func TestQuotaReadKeepsDeepSeekCurrenciesApart(t *testing.T) {
	h, mux, store, creds, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	ctx := context.Background()
	node, err := creds.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "deepseek", Name: "DeepSeek", Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := creds.PutConnection(ctx, credentials.Connection{NodeID: node.ID, Name: "fixture", Identity: "fixture", AuthKind: credentials.AuthAPIKey, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	usd, cny, unavailable := 12.5, 0.0, false
	h.opts.QuotaState = func(id string) (quota.State, bool) {
		return quota.State{Plan: "DeepSeek (Insufficient Balance)", CreditAvailable: &unavailable, Windows: []quota.Window{
			{Name: "Balance (USD)", Credit: &quota.Credit{Currency: "USD", State: quota.CreditPositive, Total: &usd}},
			{Name: "Balance (CNY)", Credit: &quota.Credit{Currency: "CNY", State: quota.CreditZero, Total: &cny}},
			{Name: "Balance (EUR)", Credit: &quota.Credit{Currency: "EUR", State: quota.CreditMissing}},
		}}, id == c.ID
	}
	got := doJSON(t, mux, cookie, "GET", "/admin/v1/quota", "")
	var page struct {
		Items []struct {
			ConnectionID    string         `json:"connectionId"`
			CreditAvailable *bool          `json:"creditAvailable"`
			Windows         []quota.Window `json:"windows"`
		} `json:"items"`
	}
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &page) != nil {
		t.Fatalf("quota: %d %s", got.Code, got.Body)
	}
	for _, item := range page.Items {
		if item.ConnectionID != c.ID {
			continue
		}
		if item.CreditAvailable == nil || *item.CreditAvailable || len(item.Windows) != 3 {
			t.Fatalf("item=%+v", item)
		}
		for i, want := range []struct {
			currency string
			state    quota.CreditState
			total    *float64
		}{{"USD", quota.CreditPositive, &usd}, {"CNY", quota.CreditZero, &cny}, {"EUR", quota.CreditMissing, nil}} {
			w := item.Windows[i]
			if w.Unlimited || w.Remaining != nil || w.Credit == nil || w.Credit.Currency != want.currency || w.Credit.State != want.state {
				t.Fatalf("window %d=%+v want %+v", i, w, want)
			}
			if (want.total == nil) != (w.Credit.Total == nil) || (want.total != nil && *w.Credit.Total != *want.total) {
				t.Fatalf("%s total=%v want %v", want.currency, w.Credit.Total, want.total)
			}
		}
		return
	}
	t.Fatalf("connection missing from %s", got.Body)
}

// TestQuotaReadKeepsProviderMetadata is the #222 read-model check: Copilot
// absolute amounts, a zero allowance that is neither unlimited nor exhausted, a
// Claude scoped window with its extra usage, a Codex limit flag, and a reader's
// unsupported-quota notice all reach the API; the error text still does not.
func TestQuotaReadKeepsProviderMetadata(t *testing.T) {
	h, mux, store, creds, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	ctx := context.Background()
	ids := map[string]string{}
	for _, provider := range []string{"github", "claude", "codex"} {
		node, err := creds.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: provider, Name: provider, Transports: []string{"openai-chat"}})
		if err != nil {
			t.Fatal(err)
		}
		c, err := creds.PutConnection(ctx, credentials.Connection{NodeID: node.ID, Name: provider, Identity: provider, AuthKind: credentials.AuthOAuth, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		ids[c.ID] = provider
	}
	used, total, left, zero, fraction := 179.5, 300.0, 120.5, 0.0, 0.4
	h.opts.QuotaState = func(id string) (quota.State, bool) {
		switch ids[id] {
		case "github":
			return quota.State{Plan: "individual", Metadata: map[string]string{"resetDate": "2026-11-01"}, Err: "secret must not be serialized", Windows: []quota.Window{
				{Name: "premium_interactions", Remaining: &fraction, Count: &quota.Count{Used: &used, Total: &total, Remaining: &left}},
				{Name: "chat", Count: &quota.Count{Used: &zero, Total: &zero, Remaining: &zero}},
			}}, true
		case "claude":
			return quota.State{Plan: "Claude Code", ExtraUsage: json.RawMessage(`{"is_enabled":true,"monthly_limit":5000}`), Windows: []quota.Window{
				{Name: "weekly fable (7d)", Remaining: &fraction, Scope: &quota.Scope{Kind: "weekly_scoped", Model: "Fable"}},
			}}, true
		case "codex":
			return quota.State{Windows: []quota.Window{{Name: "spark_session", DisplayName: "Spark (5h)", Remaining: &zero, LimitReached: true}}}, true
		}
		return quota.State{Message: "GitHub Copilot connected. Unable to parse quota data."}, false
	}
	got := doJSON(t, mux, cookie, "GET", "/admin/v1/quota", "")
	body := got.Body.String()
	if got.Code != 200 || strings.Contains(body, "secret must") || !containsAll(body,
		`"count":{"used":179.5,"total":300,"remaining":120.5}`, `"count":{"used":0,"total":0,"remaining":0}`, `"metadata":{"resetDate":"2026-11-01"}`,
		`"scope":{"kind":"weekly_scoped","model":"Fable"}`, `"extraUsage":{"is_enabled":true,"monthly_limit":5000}`,
		`"displayName":"Spark (5h)"`, `"limitReached":true`) {
		t.Fatalf("quota: %d %s", got.Code, body)
	}
	var page struct {
		Items []struct {
			ProviderID string         `json:"providerId"`
			Status     string         `json:"status"`
			Windows    []quota.Window `json:"windows"`
		} `json:"items"`
	}
	if json.Unmarshal(got.Body.Bytes(), &page) != nil {
		t.Fatal(body)
	}
	for _, item := range page.Items {
		if item.ProviderID != "github" {
			continue
		}
		chat := item.Windows[1]
		if chat.Unlimited || chat.Remaining != nil || chat.Count == nil || *chat.Count.Total != 0 {
			t.Fatalf("zero allowance=%+v", chat)
		}
	}

	h.opts.QuotaState = func(id string) (quota.State, bool) {
		return quota.State{Message: "GitHub Copilot connected. Unable to parse quota data."}, ids[id] == "github"
	}
	got = doJSON(t, mux, cookie, "GET", "/admin/v1/quota?provider=github", "")
	if !containsAll(got.Body.String(), `"message":"GitHub Copilot connected. Unable to parse quota data."`, `"status":"unknown"`) {
		t.Fatalf("unsupported quota: %s", got.Body)
	}
}

// TestQuotaReadKeepsRegionalPackageMetadata is the #223 read-model check: a
// CodeBuddy pack's amounts and recurring flag, a GLM window's raw limit, and a
// reader's no-quota notice reach the API.
func TestQuotaReadKeepsRegionalPackageMetadata(t *testing.T) {
	h, mux, store, creds, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	ctx := context.Background()
	ids := map[string]string{}
	for _, provider := range []string{"codebuddy-cn", "glm", "minimax"} {
		node, err := creds.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: provider, Name: provider, Transports: []string{"openai-chat"}})
		if err != nil {
			t.Fatal(err)
		}
		c, err := creds.PutConnection(ctx, credentials.Connection{NodeID: node.ID, Name: provider, Identity: provider, AuthKind: credentials.AuthAPIKey, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		ids[c.ID] = provider
	}
	used, total, fraction, refill, oneShot := 6.54, 500.0, 0.98692, true, false
	h.opts.QuotaState = func(id string) (quota.State, bool) {
		switch ids[id] {
		case "codebuddy-cn":
			return quota.State{Plan: "Pro", Windows: []quota.Window{
				{Name: "Monthly", Remaining: &fraction, Count: &quota.Count{Used: &used, Total: &total}, Recurring: &refill},
				{Name: "Bonus Pack 1", Remaining: &fraction, Recurring: &oneShot},
			}}, true
		case "glm":
			return quota.State{Plan: "Pro", Windows: []quota.Window{{Name: "Session (5h)", Remaining: &fraction,
				Limit: &quota.Limit{Type: "TOKENS_LIMIT", Unit: json.RawMessage(`3`), Number: json.RawMessage(`"5"`)}}}}, true
		case "minimax":
			return quota.State{Message: "MiniMax connected. No quota data was returned."}, true
		}
		return quota.State{}, false
	}
	got := doJSON(t, mux, cookie, "GET", "/admin/v1/quota", "")
	if body := got.Body.String(); got.Code != 200 || !containsAll(body,
		`"count":{"used":6.54,"total":500},"recurring":true`, `"recurring":false`,
		`"limit":{"type":"TOKENS_LIMIT","unit":3,"number":"5"}`, `"message":"MiniMax connected. No quota data was returned."`) {
		t.Fatalf("quota: %d %s", got.Code, body)
	}
}
