package ingress

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// wiredAccess serves a real upstream so an allowed request is a 200 and a denied
// one is provably a refusal, and counts every credential read and upstream hit.
type wiredAccess struct {
	accessFixture
	manager     *runtime.Manager
	scopedID    string
	credentials atomic.Int32
	upstream    atomic.Int32
}

func newWiredAccess(t *testing.T, allow []string) *wiredAccess {
	t.Helper()
	w := &wiredAccess{}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		w.upstream.Add(1)
		rw.Header().Set("Content-Type", "application/json")
		fmt.Fprint(rw, upstreamBodies[openai.ChatProtocol][false])
	}))
	t.Cleanup(server.Close)
	ctx := context.Background()
	manager := newManager(t)
	for _, id := range []string{"gpt-a", "gpt-b", "gpt-c"} {
		if err := manager.PutModel(ctx, runtime.Model{ProviderID: "openai", ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.PutAlias(ctx, "fast", runtime.ModelRef{ProviderID: "openai", ModelID: "gpt-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PutCombo(ctx, runtime.Combo{Name: "team", Members: []runtime.ComboMember{{ProviderID: "openai", ModelID: "gpt-c", Selected: true}}}); err != nil {
		t.Fatal(err)
	}
	_, open, err := manager.CreateAPIKey(ctx, "open")
	if err != nil {
		t.Fatal(err)
	}
	scopedEntry, scoped, err := manager.CreateAPIKeyWithAccess(ctx, "scoped", true, allow)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(manager, Options{
		AllowPrivateUpstreams: true,
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			return []routing.Account{{ID: "acct", Enabled: true}}, true
		},
		CredentialFor: func(context.Context, string) (string, error) {
			w.credentials.Add(1)
			return "provider-secret", nil
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, _ string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: "openai", ConnectionID: account, BaseURL: server.URL, Protocol: openai.ChatProtocol}, true
		},
	}).Attach(mux)
	w.accessFixture = accessFixture{handler: mux, open: open, scoped: scoped}
	w.manager = manager
	w.scopedID = scopedEntry.ID
	return w
}

func (w *wiredAccess) chat(key, model string) *httptest.ResponseRecorder {
	return w.do("POST", "/v1/chat/completions", key, `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
}

func TestC02RefusedKeyNeverTouchesCredentialsOrUpstream(t *testing.T) {
	w := newWiredAccess(t, []string{"fast", "team"})
	var bodies []string
	for _, model := range []string{"openai/gpt-b", "gpt-b", "openai/gpt-c", "no/such-model"} {
		got := w.chat(w.scoped, model)
		if got.Code != http.StatusForbidden {
			t.Fatalf("model %q: status %d, want 403: %s", model, got.Code, got.Body)
		}
		if !strings.Contains(got.Body.String(), "model_not_allowed") {
			t.Fatalf("model %q: 403 does not name the reason: %s", model, got.Body)
		}
		// The refusal says nothing about the key's grants or whether the target exists.
		for _, leak := range []string{"fast", "team", "scoped", "not configured", "not_found"} {
			if strings.Contains(got.Body.String(), leak) {
				t.Fatalf("model %q: 403 leaks %q: %s", model, leak, got.Body)
			}
		}
		bodies = append(bodies, got.Body.String())
	}
	if w.credentials.Load() != 0 || w.upstream.Load() != 0 {
		t.Fatalf("refused requests read %d credentials and made %d upstream calls; want 0 and 0", w.credentials.Load(), w.upstream.Load())
	}
	// Existing-but-ungranted and nonexistent targets are indistinguishable.
	if strings.ReplaceAll(bodies[0], "openai/gpt-b", "X") != strings.ReplaceAll(bodies[3], "no/such-model", "X") {
		t.Fatalf("refusal differs by target existence:\n%s\n%s", bodies[0], bodies[3])
	}
}

func TestC02GrantedAliasAndComboReachTheProvider(t *testing.T) {
	w := newWiredAccess(t, []string{"fast", "team"})
	for _, model := range []string{"fast", "team"} {
		if got := w.chat(w.scoped, model); got.Code != http.StatusOK {
			t.Fatalf("granted %q: status %d: %s", model, got.Code, got.Body)
		}
	}
	// An alias grant is its resolved target: the spelled-out identity is the same grant.
	if got := w.chat(w.scoped, "openai/gpt-a"); got.Code != http.StatusOK {
		t.Fatalf("alias target by identity: status %d: %s", got.Code, got.Body)
	}
	// A Combo grant covers the Combo as a unit, not its members for direct use.
	if got := w.chat(w.scoped, "openai/gpt-c"); got.Code != http.StatusForbidden {
		t.Fatalf("Combo member by name: status %d, want 403", got.Code)
	}
	if w.credentials.Load() != 3 || w.upstream.Load() != 3 {
		t.Fatalf("credentials %d upstream %d, want 3 and 3 (only the granted requests)", w.credentials.Load(), w.upstream.Load())
	}
}

func TestC02ComboNameNeverGrantsAModelSpelling(t *testing.T) {
	// A Combo named like a model must be granted as a Combo; listing only a
	// model that happens to share a Combo's name does not grant the Combo.
	w := newWiredAccess(t, []string{"openai/gpt-c"})
	if got := w.chat(w.scoped, "team"); got.Code != http.StatusForbidden {
		t.Fatalf("ungranted Combo: status %d, want 403", got.Code)
	}
	if got := w.chat(w.scoped, "openai/gpt-c"); got.Code != http.StatusOK {
		t.Fatalf("granted model: status %d", got.Code)
	}
	// Surrounding whitespace is a different string to the router; the gate
	// judges that string, so padding cannot borrow another name's grant.
	for _, padded := range []string{" openai/gpt-c", "openai/gpt-c "} {
		if got := w.chat(w.scoped, padded); got.Code != http.StatusForbidden {
			t.Fatalf("padded %q: status %d, want 403", padded, got.Code)
		}
	}
}

func TestC02EveryClientKeyFormIsGated(t *testing.T) {
	w := newWiredAccess(t, []string{"fast"})
	forms := map[string]func(*http.Request){
		"bearer":    func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+w.scoped) },
		"x-api-key": func(r *http.Request) { r.Header.Set("X-Api-Key", w.scoped) },
		"x-goog":    func(r *http.Request) { r.Header.Set("X-Goog-Api-Key", w.scoped) },
		"query":     func(r *http.Request) { r.URL.RawQuery = "key=" + w.scoped },
	}
	for name, apply := range forms {
		for _, tc := range []struct {
			path, model string
			want        int
		}{
			{"/v1/chat/completions", "openai/gpt-b", http.StatusForbidden},
			{"/v1/chat/completions", "fast", http.StatusOK},
		} {
			r := httptest.NewRequest("POST", tc.path, strings.NewReader(`{"model":"`+tc.model+`","messages":[{"role":"user","content":"hi"}]}`))
			apply(r)
			rec := httptest.NewRecorder()
			w.handler.ServeHTTP(rec, r)
			if rec.Code != tc.want {
				t.Fatalf("%s %s: status %d, want %d: %s", name, tc.model, rec.Code, tc.want, rec.Body)
			}
		}
		r := httptest.NewRequest("GET", "/v1/models", nil)
		apply(r)
		rec := httptest.NewRecorder()
		w.handler.ServeHTTP(rec, r)
		if ids := listedIDs(t, rec); ids["openai/gpt-b"] || ids["team"] || !ids["fast"] {
			t.Fatalf("%s: listing = %v", name, ids)
		}
	}
	// Two forms naming different keys never let the unrestricted one win.
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"openai/gpt-b","messages":[{"role":"user","content":"hi"}]}`))
	r.Header.Set("Authorization", "Bearer "+w.open)
	r.Header.Set("X-Api-Key", w.scoped)
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("conflicting key forms: status %d, want 401", rec.Code)
	}
}

func TestC02EveryEnumerationSurfaceIsFiltered(t *testing.T) {
	w := newWiredAccess(t, []string{"fast", "team"})
	for _, path := range []string{"/v1/models", "/v1/models/llm", "/v1/models/info?id=fast", "/v1/models/openai/gpt-a", "/api/tags", "/v1beta/models", "/v1/models?kind=llm"} {
		body := w.do("GET", path, w.scoped, "").Body.String()
		for _, hidden := range []string{"openai/gpt-b", "gpt-b", "openai/gpt-c"} {
			if strings.Contains(body, hidden) {
				t.Fatalf("%s leaks %q: %s", path, hidden, body)
			}
		}
	}
	// Single-model lookups of an ungranted and a nonexistent model are the same 404.
	hidden := w.do("GET", "/v1/models/openai/gpt-b", w.scoped, "")
	absent := w.do("GET", "/v1/models/openai/nope", w.scoped, "")
	if hidden.Code != http.StatusNotFound || absent.Code != http.StatusNotFound || hidden.Body.String() != absent.Body.String() {
		t.Fatalf("lookup: %d %q vs %d %q", hidden.Code, hidden.Body, absent.Code, absent.Body)
	}
	if ids := listedIDs(t, w.do("GET", "/v1/models", w.open, "")); !ids["openai/gpt-b"] || !ids["openai/gpt-c"] || !ids["team"] || !ids["fast"] {
		t.Fatalf("unrestricted key must list everything: %v", ids)
	}
}

func TestC02UnrestrictedKeyIsNotLimited(t *testing.T) {
	w := newWiredAccess(t, []string{"fast"})
	for _, model := range []string{"openai/gpt-a", "openai/gpt-b", "fast", "team"} {
		if got := w.chat(w.open, model); got.Code != http.StatusOK {
			t.Fatalf("unrestricted %q: status %d: %s", model, got.Code, got.Body)
		}
	}
}

func TestC02RestrictedKeyStaysRestrictedWhenKeysAreNotRequired(t *testing.T) {
	ctx := context.Background()
	w := newWiredAccess(t, []string{"fast"})
	if _, err := w.manager.SetSettings(ctx, map[string]string{"requireApiKey": "false"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := w.chat(w.scoped, "openai/gpt-b"); got.Code != http.StatusForbidden {
		t.Fatalf("restricted key with auth off: status %d, want 403", got.Code)
	}
	if got := w.chat(w.scoped, "fast"); got.Code != http.StatusOK {
		t.Fatalf("granted model with auth off: status %d", got.Code)
	}
	// A restricted key that has since been paused must not widen to unrestricted.
	paused := true
	if err := w.manager.UpdateAPIKey(ctx, w.scopedID, &paused, nil); err != nil {
		t.Fatal(err)
	}
	if got := w.chat(w.scoped, "openai/gpt-b"); got.Code != http.StatusForbidden {
		t.Fatalf("paused restricted key with auth off: status %d, want 403", got.Code)
	}
	// With no key at all, auth being off means unrestricted, as before.
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"openai/gpt-b","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("no key, auth off: status %d: %s", rec.Code, rec.Body)
	}
}
