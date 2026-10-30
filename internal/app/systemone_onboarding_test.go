package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

// #278 (U09): the System One page onboards a custom typed endpoint, its models and an account through
// the admin API, then the live serving path uses them. This makes the very calls the page makes
// (ui/src/SystemOne.tsx addProvider/addAccount, and the paged reads) against the real handler and
// store, and then sends a typed request to /v1/systemone with an injected upstream.

func onboardingApp(t *testing.T, allowPrivate bool) (*App, *http.Cookie) {
	t.Helper()
	ctx := context.Background()
	a := New(Config{DataDir: storetest.DataDirWithAdmin(t, "operator", c03Password), CredentialKey: []byte("01234567890123456789012345678901"), AllowPrivateUpstreams: allowPrivate}, nil)
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.store.Close() })
	return a, c03Login(t, a)
}

func adminCall(t *testing.T, a *App, cookie *http.Cookie, method, path string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	request := httptest.NewRequest(method, path, reader)
	request.AddCookie(cookie)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := c03Do(a, request)
	return response.Code, response.Body.Bytes()
}

func TestSystemOneOnboardingFromThePageServesTheLivePath(t *testing.T) {
	var calls atomic.Int32
	var gotPath, gotAuth, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		gotPath, gotAuth, gotBody = r.URL.Path, r.Header.Get("Authorization"), string(raw)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"answers":{"refund_requested":{"value":true}},"usage":{"input_tokens":7,"output_tokens":3}}`)
	}))
	defer upstream.Close()
	a, cookie := onboardingApp(t, true)

	// 1. The custom typed endpoint, exactly as the page posts it.
	status, body := adminCall(t, a, cookie, http.MethodPost, "/admin/v1/provider-nodes", map[string]any{"kind": "generic", "name": "System One (Prod)", "prefix": "s1-prod", "baseUrl": upstream.URL + "/typed/endpoint", "transports": []string{"systemone"}})
	var node struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerId"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &node) != nil || node.ID == "" || node.ProviderID != "s1-prod" {
		t.Fatalf("create node %d %s", status, body)
	}
	// 2. Its models, one request each.
	for _, id := range []string{"jev-latest", "jev-mini"} {
		if status, body = adminCall(t, a, cookie, http.MethodPost, "/admin/v1/models", map[string]any{"providerId": "s1-prod", "id": id, "name": id}); status != http.StatusOK {
			t.Fatalf("add model %s: %d %s", id, status, body)
		}
	}
	// 3. An account: name, identity, API key. The secret goes in; it never comes back out.
	status, body = adminCall(t, a, cookie, http.MethodPost, "/admin/v1/connections", map[string]any{"providerId": "s1-prod", "nodeId": node.ID, "name": "Prod account", "identity": "acct-prod", "authKind": "api-key", "secret": map[string]string{"accessToken": "sk-onboarded"}, "enabled": true, "priority": 0})
	if status != http.StatusOK || strings.Contains(string(body), "sk-onboarded") {
		t.Fatalf("create connection %d %s", status, body)
	}

	// The page's reads: System One models and the provider's accounts, filtered by provider, paged.
	status, body = adminCall(t, a, cookie, http.MethodGet, "/admin/v1/systemone?providerId=s1-prod&page=1&pageSize=100", nil)
	var models struct {
		Items []struct {
			ID         string `json:"id"`
			ProviderID string `json:"providerId"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &models) != nil || models.Total != 2 || len(models.Items) != 2 {
		t.Fatalf("models %d %s", status, body)
	}
	status, body = adminCall(t, a, cookie, http.MethodGet, "/admin/v1/connections?providerId=s1-prod&page=1&pageSize=100", nil)
	if status != http.StatusOK || strings.Contains(string(body), "sk-onboarded") || !strings.Contains(string(body), `"identity":"acct-prod"`) || !strings.Contains(string(body), `"credentialConfigured":true`) {
		t.Fatalf("connections %d %s", status, body)
	}

	// The live path: a client key, the prefixed model, the account's key to the endpoint.
	_, key, err := a.runtime.CreateAPIKey(context.Background(), "typed-client")
	if err != nil {
		t.Fatal(err)
	}
	request := `{"model":"s1-prod/jev-latest","state":{"message":"My card was charged twice."},"questions":{"refund_requested":{"type":"noul","instructions":"Does the customer request a refund?"}}}`
	w := serveRequest(a.Handler(), key, "/v1/systemone", request)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"refund_requested"`) || calls.Load() != 1 {
		t.Fatalf("typed request status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body)
	}
	want := `{"model":"jev-latest","state":{"message":"My card was charged twice."},"questions":{"refund_requested":{"type":"noul","instructions":"Does the customer request a refund?"}}}`
	var gotJSON, wantJSON any
	if json.Unmarshal([]byte(gotBody), &gotJSON) != nil || json.Unmarshal([]byte(want), &wantJSON) != nil || fmt.Sprint(gotJSON) != fmt.Sprint(wantJSON) {
		t.Fatalf("the endpoint got %s, want the request with the model stripped of its prefix", gotBody)
	}
	if gotPath != "/typed/endpoint" || gotAuth != "Bearer sk-onboarded" {
		t.Fatalf("endpoint path=%q auth=%q", gotPath, gotAuth)
	}
}

func TestSystemOneOnboardingRefusesWhatTheServerMustRefuse(t *testing.T) {
	a, cookie := onboardingApp(t, false)
	post := func(prefix, url string) int {
		status, _ := adminCall(t, a, cookie, http.MethodPost, "/admin/v1/provider-nodes", map[string]any{"kind": "generic", "name": "x", "prefix": prefix, "baseUrl": url, "transports": []string{"systemone"}})
		return status
	}
	// The same outbound policy as every provider: no private, loopback or metadata addresses by default.
	for _, url := range []string{"http://127.0.0.1:9/x", "http://10.0.0.5/x", "http://169.254.169.254/latest", "ftp://example.com/x", "not a url"} {
		if status := post("blocked", url); status != http.StatusBadRequest {
			t.Errorf("endpoint %q accepted with %d", url, status)
		}
	}
	// A System One node is typed only: it cannot also claim a chat transport.
	status, _ := adminCall(t, a, cookie, http.MethodPost, "/admin/v1/provider-nodes", map[string]any{"kind": "generic", "name": "x", "prefix": "mixed", "baseUrl": "https://api.typesafe.ai/v1/systemone", "transports": []string{"systemone", "openai-chat"}})
	if status != http.StatusBadRequest {
		t.Errorf("mixed transports accepted with %d", status)
	}
	if status := post("s1-ok", "https://api.typesafe.ai/v1/systemone"); status != http.StatusOK {
		t.Fatalf("a public endpoint was refused with %d", status)
	}
	// A prefix already in use is refused rather than creating a second provider with the same id.
	if status := post("s1-ok", "https://api.typesafe.ai/v1/systemone"); status != http.StatusBadRequest {
		t.Errorf("a repeated prefix was accepted with %d", status)
	}
	// A built-in provider id is reserved: a custom endpoint must not shadow a built-in route
	// (oracle provider-nodes/route.js:9-14,95).
	for _, builtin := range []string{"typesafe", "openai", "anthropic", "cloudflare-ai"} {
		status, body := adminCall(t, a, cookie, http.MethodPost, "/admin/v1/provider-nodes", map[string]any{"kind": "generic", "name": "x", "prefix": builtin, "baseUrl": "https://api.typesafe.ai/v1/systemone", "transports": []string{"systemone"}})
		if status != http.StatusBadRequest || !strings.Contains(string(body), "reserved_prefix") {
			t.Errorf("built-in prefix %q: %d %s", builtin, status, body)
		}
	}
}

func TestSystemOneAccountIDIsOnboardedAndValidated(t *testing.T) {
	a, cookie := onboardingApp(t, true)
	status, body := adminCall(t, a, cookie, http.MethodPost, "/admin/v1/provider-nodes", map[string]any{"kind": "builtin", "providerId": "cloudflare-ai", "baseUrl": "http://127.0.0.1:9/client/v4/accounts"})
	var node struct {
		ID string `json:"id"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &node) != nil || node.ID == "" {
		t.Fatalf("cloudflare node %d %s", status, body)
	}
	connection := func(accountID string) (int, []byte) {
		payload := map[string]any{"providerId": "cloudflare-ai", "nodeId": node.ID, "name": "cf", "identity": "cf-" + accountID, "authKind": "api-key", "secret": map[string]string{"accessToken": "sk-cf"}, "enabled": true, "priority": 0}
		if accountID != "" {
			payload["accountId"] = accountID
		}
		return adminCall(t, a, cookie, http.MethodPost, "/admin/v1/connections", payload)
	}
	// An account ID is one path segment; anything that could change the URL is refused.
	for _, bad := range []string{"a/b", "a b", "../x", "a?b=1", "a#b"} {
		if status, body := connection(bad); status != http.StatusBadRequest || !strings.Contains(string(body), "invalid_account_id") {
			t.Errorf("account ID %q: %d %s", bad, status, body)
		}
	}
	status, body = connection("acct_123-Prod")
	if status != http.StatusOK || !strings.Contains(string(body), `"accountId":"acct_123-Prod"`) {
		t.Fatalf("valid account ID %d %s", status, body)
	}
	status, body = adminCall(t, a, cookie, http.MethodGet, "/admin/v1/connections?providerId=cloudflare-ai&page=1&pageSize=100", nil)
	if status != http.StatusOK || !strings.Contains(string(body), `"accountId":"acct_123-Prod"`) {
		t.Fatalf("the page's account list does not show the account ID: %d %s", status, body)
	}
}
