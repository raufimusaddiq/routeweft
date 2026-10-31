package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// Shared API-key and no-auth acceptance (#230). The matrix in
// docs/PROVIDER_ACCEPTANCE_MATRIX.md names these tests for every O01/O02
// provider: the admin lifecycle plus the failure answers a shared-key provider
// must produce without echoing the credential. Fixtures are in-process and the
// database is a per-test temp file, so a failed run leaves nothing behind.

func TestSharedAPIKeyProviderAcceptance(t *testing.T) {
	var mode atomic.Int32 // 0 ok, 1 401, 2 429, 3 timeout
	var mu sync.Mutex
	var seen []string
	upstream := probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		switch mode.Load() {
		case 1:
			return listingReply(http.StatusUnauthorized, `{"error":"bad key"}`), nil
		case 2:
			return listingReply(http.StatusTooManyRequests, `{"error":"slow down"}`), nil
		case 3:
			return nil, &net.DNSError{Err: "timeout", IsTimeout: true}
		}
		return listingReply(http.StatusOK, `{"data":[{"id":"accept-model"}]}`), nil
	})
	h, mux, store, _, cookie := newProvidersAPI(t, false, &discovery.Client{HTTP: upstream})
	defer store.Close()
	h.opts.Providers = registry.Builtins()

	created := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections", `{"providerId":"openai","name":"acceptance","identity":"acct-1","secret":{"accessToken":"accept-secret-key"}}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var saved struct{ ID, Name string }
	if err := json.Unmarshal(created.Body.Bytes(), &saved); err != nil || saved.ID == "" {
		t.Fatalf("create body: %s (%v)", created.Body, err)
	}

	// A wrong key, a rate limit and a timeout are ordinary failed discoveries:
	// 502 with the class named, never the credential echoed.
	for _, c := range []struct {
		mode int32
		want string
	}{
		{1, "HTTP 401"},
		{2, "HTTP 429"},
		{3, "model discovery request failed"},
	} {
		mode.Store(c.mode)
		discovered := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+saved.ID+"/discover", "")
		if discovered.Code != http.StatusBadGateway || !strings.Contains(discovered.Body.String(), c.want) {
			t.Fatalf("mode %d: %d %s", c.mode, discovered.Code, discovered.Body)
		}
		if strings.Contains(discovered.Body.String(), "accept-secret-key") {
			t.Fatalf("mode %d echoed the credential: %s", c.mode, discovered.Body)
		}
	}

	// A successful discovery stores the catalog and sends the key as a bearer.
	mode.Store(0)
	discovered := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+saved.ID+"/discover", "")
	if discovered.Code != http.StatusOK || !strings.Contains(discovered.Body.String(), "accept-model") {
		t.Fatalf("discover: %d %s", discovered.Code, discovered.Body)
	}
	mu.Lock()
	if len(seen) == 0 || seen[len(seen)-1] != "Bearer accept-secret-key" {
		t.Fatalf("discovery authorization = %v", seen)
	}
	mu.Unlock()

	// Edit replaces the credential in one write and returns no secret material.
	edited := doJSON(t, mux, cookie, http.MethodPatch, "/admin/v1/connections/"+saved.ID, `{"name":"acceptance-renamed","secret":{"accessToken":"rotated-secret-key"}}`)
	if edited.Code != http.StatusOK || !strings.Contains(edited.Body.String(), `"name":"acceptance-renamed"`) || strings.Contains(edited.Body.String(), "rotated-secret-key") {
		t.Fatalf("edit: %d %s", edited.Code, edited.Body)
	}
	listed := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/connections", "")
	if !strings.Contains(listed.Body.String(), "acceptance-renamed") || strings.Contains(listed.Body.String(), "rotated-secret-key") {
		t.Fatalf("list after edit: %s", listed.Body)
	}

	// Revoke removes it from every read.
	if revoked := doJSON(t, mux, cookie, http.MethodDelete, "/admin/v1/connections/"+saved.ID, ""); revoked.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", revoked.Code, revoked.Body)
	}
	if listed := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/connections", ""); strings.Contains(listed.Body.String(), saved.ID) {
		t.Fatalf("revoked connection is still listed: %s", listed.Body)
	}
}

func TestNoSecretProviderAcceptance(t *testing.T) {
	var mu sync.Mutex
	var authorizations []string
	upstream := probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		authorizations = append(authorizations, r.Header.Get("Authorization")+"|"+r.Header.Get("x-api-key"))
		mu.Unlock()
		return listingReply(http.StatusOK, `{"data":[{"id":"free-model"}]}`), nil
	})
	h, mux, store, _, cookie := newProvidersAPI(t, false, &discovery.Client{HTTP: upstream})
	defer store.Close()
	h.opts.Providers = registry.Builtins()

	created := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections", `{"providerId":"mimo-free","name":"public","identity":"public"}`)
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), `"credentialConfigured":false`) {
		t.Fatalf("no-auth create: %d %s", created.Code, created.Body)
	}
	var saved struct{ ID string }
	if err := json.Unmarshal(created.Body.Bytes(), &saved); err != nil || saved.ID == "" {
		t.Fatal(err)
	}
	// No placeholder key is invented for a no-auth connection.
	if listed := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/connections", ""); strings.Contains(listed.Body.String(), "apiKey") {
		t.Fatalf("no-auth connection exposes a key field: %s", listed.Body)
	}
	if discovered := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+saved.ID+"/discover", ""); discovered.Code != http.StatusOK || !strings.Contains(discovered.Body.String(), "free-model") {
		t.Fatalf("no-auth discovery: %d %s", discovered.Code, discovered.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(authorizations) != 1 || authorizations[0] != "|" {
		t.Fatalf("no-auth discovery sent credentials: %v", authorizations)
	}
}
