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
	before := calls
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
