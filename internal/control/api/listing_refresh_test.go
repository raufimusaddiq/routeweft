package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/capabilities"
	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func listingReply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

// The admin listing for Zed is the pin's customResolver (route.js:293-326):
// /client/llm_tokens exchanges the account credential for an LLM token, and
// /models is read with that token, not with the account bearer.
func TestZedListingExchangesItsAccountCredential(t *testing.T) {
	h := New(Options{Providers: registry.Builtins()})
	var mu sync.Mutex
	var seen []string
	h.opts.DiscoveryClient.HTTP = probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/client/llm_tokens":
			return listingReply(http.StatusOK, `{"token":"llm-token"}`), nil
		case "/models":
			if r.Header.Get("Authorization") != "Bearer llm-token" {
				t.Errorf("models bearer = %q", r.Header.Get("Authorization"))
			}
			if r.Header.Get(zedHeaderClientXAI) != "true" {
				t.Errorf("models request lacks the x-ai capability header")
			}
			return listingReply(http.StatusOK, `{"models":[
				{"id":"claude-sonnet-4.5","display_name":"Claude Sonnet 4.5","max_token_count":200000,"supports_images":true},
				{"id":"disabled","is_disabled":true}]}`), nil
		}
		return listingReply(http.StatusNotFound, `{}`), nil
	})
	node, connection := builtinConnection("zed", routing.ProviderSettings{UserID: "user-1", OrganizationID: "org-1", SystemID: "sys-1"}, credentials.AuthOAuth)
	connection.Secret.AccessToken = "account-token"
	models, err := h.fetchConnectionModelEntries(context.Background(), connection, node)
	if err != nil || ids(models) != "claude-sonnet-4.5" || models[0].Name != "Claude Sonnet 4.5" || models[0].ContextWindow != 200000 {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "POST /client/llm_tokens user-1 account-token" || seen[1] != "GET /models Bearer llm-token" {
		t.Fatalf("requests = %v", seen)
	}
}

const zedHeaderClientXAI = "x-zed-client-supports-x-ai"

// setListingUpstream installs the shared 401-then-200 listing fixture and
// returns the credentials durably stored and registered with one counter.
func setListingUpstream(t *testing.T, h *Handler, credStore *credentials.Store, provider string, refreshes *atomic.Int32, refreshErr error) (credentials.Node, credentials.Connection, *[]string) {
	t.Helper()
	var mu sync.Mutex
	seen := []string{}
	h.opts.DiscoveryClient.HTTP = probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		seen = append(seen, auth)
		mu.Unlock()
		if auth != "Bearer new" {
			return listingReply(http.StatusUnauthorized, `{}`), nil
		}
		return listingReply(http.StatusOK, `{"data":[{"id":"gpt-4.1","capabilities":{"type":"chat"},"policy":{"state":"enabled"}}]}`), nil
	})
	node, err := credStore.PutNode(context.Background(), credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: provider, Name: provider, Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := credStore.PutConnection(context.Background(), credentials.Connection{
		NodeID: node.ID, Name: provider, Identity: provider, AuthKind: credentials.AuthOAuth, Enabled: true,
		Secret: credentials.Secret{AccessToken: "old", RefreshToken: "refresh-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.opts.CredentialRegistry.Register(connection, func(context.Context, credentials.Secret) (credentials.Secret, error) {
		refreshes.Add(1)
		if refreshErr != nil {
			return credentials.Secret{}, refreshErr
		}
		return credentials.Secret{AccessToken: "new", RefreshToken: "refresh-2", Expiry: time.Now().Add(time.Hour)}, nil
	})
	return node, connection, &seen
}

// A listing 401 refreshes the rotating credential once, retries once and
// persists the rotated payload (route.js:93-128).
func TestListingRefreshesOAuthOnceAndPersistsTheRotation(t *testing.T) {
	h, _, store, credStore, _ := newProvidersAPI(t, false, nil)
	defer store.Close()
	h.opts.Providers = registry.Builtins()
	var refreshes atomic.Int32
	node, connection, seen := setListingUpstream(t, h, credStore, "github", &refreshes, nil)
	models, err := h.fetchConnectionModelEntries(context.Background(), connection, node)
	if err != nil || ids(models) != "gpt-4.1" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes.Load())
	}
	if strings.Join(*seen, ",") != "Bearer old,Bearer new" {
		t.Fatalf("listing authorizations = %v", *seen)
	}
	persisted, err := credStore.GetConnection(context.Background(), connection.ID)
	if err != nil || persisted.Secret.AccessToken != "new" || persisted.Secret.RefreshToken != "refresh-2" {
		t.Fatalf("persisted = %+v err=%v", persisted.Secret, err)
	}
}

// A refresh that fails leaves the listing's own failure and the old credential.
func TestListingRefreshFailureKeepsTheListingError(t *testing.T) {
	h, _, store, credStore, _ := newProvidersAPI(t, false, nil)
	defer store.Close()
	h.opts.Providers = registry.Builtins()
	var refreshes atomic.Int32
	node, connection, seen := setListingUpstream(t, h, credStore, "github", &refreshes, errors.New("refresh grant rejected"))
	_, err := h.fetchConnectionModelEntries(context.Background(), connection, node)
	var status *discovery.StatusError
	if !errors.As(err, &status) || status.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v", err)
	}
	if refreshes.Load() != 1 || len(*seen) != 1 {
		t.Fatalf("refreshes=%d listings=%d; a failed refresh must not retry", refreshes.Load(), len(*seen))
	}
	persisted, _ := credStore.GetConnection(context.Background(), connection.ID)
	if persisted.Secret.AccessToken != "old" {
		t.Fatalf("failed refresh replaced the credential: %+v", persisted.Secret)
	}
}

// Concurrent listings that are all refused share one refresh: the registry's
// singleflight is the bound, so a refresh token is never used twice.
func TestConcurrentListingsRefreshOnce(t *testing.T) {
	h, _, store, credStore, _ := newProvidersAPI(t, false, nil)
	defer store.Close()
	h.opts.Providers = registry.Builtins()
	var refreshes atomic.Int32
	node, connection, seen := setListingUpstream(t, h, credStore, "github", &refreshes, nil)
	// Widen the window so every caller reaches the 401 while the refresh is in
	// flight. The count, not the timing, is the assertion.
	reg := h.opts.CredentialRegistry
	reg.Register(connection, func(context.Context, credentials.Secret) (credentials.Secret, error) {
		refreshes.Add(1)
		time.Sleep(20 * time.Millisecond)
		return credentials.Secret{AccessToken: "new", RefreshToken: "refresh-2", Expiry: time.Now().Add(time.Hour)}, nil
	})
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, errs[index] = h.fetchConnectionModelEntries(context.Background(), connection, node)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent listing: %v", err)
		}
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes.Load())
	}
	// Every listing that succeeded used the rotated token; the refused ones used
	// the old one. Fewer than eight old attempts is fine: a caller that read the
	// registry after the refresh never saw the refused token.
	var fresh int
	for _, auth := range *seen {
		switch auth {
		case "Bearer old":
		case "Bearer new":
			fresh++
		default:
			t.Fatalf("unexpected authorization %q", auth)
		}
	}
	if fresh != 8 {
		t.Fatalf("successful authorizations = %d, want 8", fresh)
	}
}

// A refused Zed exchange is the account credential's, so the same refresh-once
// policy retries the whole exchange with the rotated token.
func TestZedListingRetriesOnceAfterAnAccountRefresh(t *testing.T) {
	h, _, store, credStore, _ := newProvidersAPI(t, false, nil)
	defer store.Close()
	h.opts.Providers = registry.Builtins()
	var mu sync.Mutex
	var seen []string
	h.opts.DiscoveryClient.HTTP = probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/client/llm_tokens":
			if r.Header.Get("Authorization") == "user-1 old" {
				return listingReply(http.StatusUnauthorized, `{}`), nil
			}
			return listingReply(http.StatusOK, `{"token":"llm-new"}`), nil
		case "/models":
			return listingReply(http.StatusOK, `{"models":[{"id":"claude-sonnet-4.5"}]}`), nil
		}
		return listingReply(http.StatusNotFound, `{}`), nil
	})
	node, err := credStore.PutNode(context.Background(), credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "zed", Name: "zed", Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := credStore.PutConnection(context.Background(), credentials.Connection{
		NodeID: node.ID, Name: "zed", Identity: "zed", AuthKind: credentials.AuthOAuth, Enabled: true,
		Settings: routing.ProviderSettings{UserID: "user-1", OrganizationID: "org-1"},
		Secret:   credentials.Secret{AccessToken: "old", RefreshToken: "refresh-1", ProviderData: map[string]string{"userId": "user-1", "organizationId": "org-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	h.opts.CredentialRegistry.Register(connection, func(context.Context, credentials.Secret) (credentials.Secret, error) {
		refreshes.Add(1)
		return credentials.Secret{AccessToken: "new", RefreshToken: "refresh-2", Expiry: time.Now().Add(time.Hour)}, nil
	})
	models, err := h.fetchConnectionModelEntries(context.Background(), connection, node)
	if err != nil || ids(models) != "claude-sonnet-4.5" || refreshes.Load() != 1 {
		t.Fatalf("models=%+v err=%v refreshes=%d", models, err, refreshes.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"/client/llm_tokens user-1 old", "/client/llm_tokens user-1 new", "/models Bearer llm-new"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("requests = %v", seen)
	}
	persisted, _ := credStore.GetConnection(context.Background(), connection.ID)
	if persisted.Secret.AccessToken != "new" {
		t.Fatalf("rotation was not persisted: %+v", persisted.Secret)
	}
}

// A Gemini API key travels as x-goog-api-key; the URL carries no key, so it
// cannot reach a log or a proxy (route.js:142-148 sends ?key=; same effect).
func TestGeminiListingKeepsTheKeyOutOfTheURL(t *testing.T) {
	models, call, err := listProvider(t, "gemini", credentials.AuthAPIKey, routing.ProviderSettings{}, "gemini-key", `{"models":[{"name":"models/gemini-3-pro"}]}`)
	if err != nil || ids(models) != "gemini-3-pro" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	if strings.Contains(call.URL, "gemini-key") || strings.Contains(call.URL, "key=") {
		t.Fatalf("key leaked into the URL %s", call.URL)
	}
	if call.URL != "https://generativelanguage.googleapis.com/v1beta/models" {
		t.Fatalf("gemini listing URL %s", call.URL)
	}
	if call.Header.Get("x-goog-api-key") != "gemini-key" || call.Header.Get("Authorization") != "" {
		t.Fatalf("gemini headers %v", call.Header)
	}
}

// A temporary discovery failure keeps the durable catalog, including the
// passthrough ids an earlier successful listing stored.
func TestDiscoveryFailureKeepsTheSavedPassthroughModels(t *testing.T) {
	var fail atomic.Bool
	upstream := probeHTTPFunc(func(*http.Request) (*http.Response, error) {
		if fail.Load() {
			return listingReply(http.StatusServiceUnavailable, `{}`), nil
		}
		return listingReply(http.StatusOK, `{"data":[{"id":"model-a"},{"id":"model-b"}]}`), nil
	})
	h, mux, store, creds, cookie := newProvidersAPI(t, false, &discovery.Client{HTTP: upstream})
	defer store.Close()
	h.opts.Providers = registry.Builtins()
	node, err := creds.PutNode(context.Background(), credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "openrouter", Name: "openrouter", Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := creds.PutConnection(context.Background(), credentials.Connection{NodeID: node.ID, Name: "or", Identity: "or", AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: "tok"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+connection.ID+"/discover", ""); w.Code != http.StatusOK {
		t.Fatalf("first discovery: %d %s", w.Code, w.Body)
	}
	fail.Store(true)
	if w := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+connection.ID+"/discover", ""); w.Code != http.StatusBadGateway {
		t.Fatalf("failed discovery: %d %s", w.Code, w.Body)
	}
	stored := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/models?providerId=openrouter", "")
	if stored.Code != http.StatusOK || !strings.Contains(stored.Body.String(), "model-a") || !strings.Contains(stored.Body.String(), "model-b") {
		t.Fatalf("saved catalog was lost: %d %s", stored.Code, stored.Body)
	}
}

// The three aliases the issue names resolve to the canonical providers whose
// listings store the models; prefix routing itself stays PARITY 6.15's open
// item, not a listing change.
func TestListingAliasesResolveToTheCanonicalProvider(t *testing.T) {
	for alias, canonical := range map[string]string{"kmc": "kimi", "kimi-coding": "kimi", "ag": "antigravity"} {
		keys := capabilities.ProviderKeys(alias)
		if !strings.Contains(strings.Join(keys, ","), canonical) {
			t.Fatalf("%s keys = %v, want %s", alias, keys, canonical)
		}
		body := `{"data":[{"id":"alias-model"}]}`
		if canonical == "antigravity" {
			body = `{"models":[{"id":"alias-model"}]}`
		}
		models, _, err := listProvider(t, canonical, credentials.AuthOAuth, routing.ProviderSettings{}, "tok", body)
		if err != nil || ids(models) != "alias-model" {
			t.Fatalf("%s listing = %+v %v", canonical, models, err)
		}
	}
}
