package ingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

type accessFixture struct {
	handler http.Handler
	open    string // unrestricted key
	scoped  string // restricted key
}

// newAccessFixture configures two models, an alias, and a Combo, plus one
// unrestricted key and one key limited to {alias "fast", Combo "team", openai/gpt-b}.
func newAccessFixture(t *testing.T, allow []string) accessFixture {
	t.Helper()
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
	_, scoped, err := manager.CreateAPIKeyWithAccess(ctx, "scoped", true, allow)
	if err != nil {
		t.Fatal(err)
	}
	return accessFixture{handler: newHandler(t, manager), open: open, scoped: scoped}
}

func (f accessFixture) do(method, path, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func listedIDs(t *testing.T, w *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, item := range body.Data {
		ids[item.ID] = true
	}
	return ids
}

func TestRestrictedKeyOnlySeesAllowedModelsAndCombos(t *testing.T) {
	f := newAccessFixture(t, []string{"fast", "team", "openai/gpt-b"})
	all := listedIDs(t, f.do("GET", "/v1/models", f.open, ""))
	for _, id := range []string{"openai/gpt-a", "openai/gpt-b", "openai/gpt-c", "fast", "team"} {
		if !all[id] {
			t.Fatalf("unrestricted key is missing %q: %v", id, all)
		}
	}
	got := listedIDs(t, f.do("GET", "/v1/models", f.scoped, ""))
	// Grants are resolved identities (pin keyAccess.js): an allowed alias is its
	// target, so the target is listed too. A Combo is granted without its members.
	want := map[string]bool{"fast": true, "openai/gpt-a": true, "team": true, "openai/gpt-b": true}
	if len(got) != len(want) {
		t.Fatalf("restricted listing = %v, want %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("restricted listing = %v, missing %q", got, id)
		}
	}
	if got["openai/gpt-c"] {
		t.Fatal("gpt-c is only reachable through the Combo and must stay hidden")
	}
	tags := listedTags(t, f.do("GET", "/api/tags", f.scoped, ""))
	if tags["openai/gpt-c"] {
		t.Fatal("/api/tags leaked a model the key cannot use")
	}
}

func listedTags(t *testing.T, w *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, m := range body.Models {
		out[m.Name] = true
	}
	return out
}

func TestRestrictedKeyIsDeniedBeforeModelResolution(t *testing.T) {
	f := newAccessFixture(t, []string{"fast", "team", "openai/gpt-b"})
	chat := func(key, model string) int {
		return f.do("POST", "/v1/chat/completions", key, `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`).Code
	}
	// Past the gate there is no provider wired in this fixture (503/404), which
	// is how an allowed request is told apart from a 403.
	for _, model := range []string{"fast", "openai/gpt-a", "team", "openai/gpt-b"} {
		if code := chat(f.scoped, model); code == http.StatusForbidden {
			t.Fatalf("model %q should be permitted, got 403", model)
		}
	}
	for _, model := range []string{"openai/gpt-c", "gpt-c", "does-not-exist"} {
		if code := chat(f.scoped, model); code != http.StatusForbidden {
			t.Fatalf("model %q: status %d, want 403 (no existence oracle)", model, code)
		}
	}
	if code := chat(f.open, "openai/gpt-c"); code == http.StatusForbidden {
		t.Fatal("unrestricted key must not be limited")
	}
	for _, path := range []string{"/v1/messages", "/v1/responses", "/v1/systemone", "/v1beta/models/openai/gpt-c:generateContent"} {
		body := `{"model":"openai/gpt-c","input":"x","messages":[{"role":"user","content":"x"}],"state":{},"questions":{"q":{"type":"text"}}}`
		if path == "/v1beta/models/openai/gpt-c:generateContent" {
			path = "/v1beta/models/gpt-c:generateContent"
			body = `{"contents":[{"parts":[{"text":"x"}]}]}`
		}
		if code := f.do("POST", path, f.scoped, body).Code; code != http.StatusForbidden {
			t.Fatalf("%s: status %d, want 403", path, code)
		}
	}
	if code := f.do("POST", "/v1/messages/count_tokens", f.scoped, `{"model":"openai/gpt-c","messages":[{"role":"user","content":"x"}]}`).Code; code != http.StatusForbidden {
		t.Fatalf("count_tokens: %d, want 403", code)
	}
	if code := f.do("POST", "/v1/messages/count_tokens", f.scoped, `{"model":"fast","messages":[{"role":"user","content":"x"}]}`).Code; code != http.StatusOK {
		t.Fatalf("count_tokens allowed model: %d, want 200", code)
	}
}

func TestRestrictedKeyHidesDisallowedModelLookups(t *testing.T) {
	f := newAccessFixture(t, []string{"openai/gpt-b"})
	if code := f.do("GET", "/v1/models/info?id=openai/gpt-b", f.scoped, "").Code; code != http.StatusOK {
		t.Fatalf("allowed info: %d", code)
	}
	if code := f.do("GET", "/v1/models/info?id=openai/gpt-c", f.scoped, "").Code; code != http.StatusNotFound {
		t.Fatalf("hidden info: %d, want 404", code)
	}
	if code := f.do("GET", "/v1/models/openai/gpt-c", f.scoped, "").Code; code != http.StatusNotFound {
		t.Fatalf("hidden model lookup: %d, want 404", code)
	}
	var gemini struct {
		Models []struct{ Name string } `json:"models"`
	}
	w := f.do("GET", "/v1beta/models", f.scoped, "")
	_ = json.Unmarshal(w.Body.Bytes(), &gemini)
	if len(gemini.Models) != 1 || gemini.Models[0].Name != "models/gpt-b" {
		t.Fatalf("gemini listing = %s, want only models/gpt-b", w.Body)
	}
}

func TestRestrictedKeyWithEmptyAllowlistCanUseNothing(t *testing.T) {
	f := newAccessFixture(t, nil)
	if ids := listedIDs(t, f.do("GET", "/v1/models", f.scoped, "")); len(ids) != 0 {
		t.Fatalf("empty allowlist lists %v", ids)
	}
	if code := f.do("POST", "/v1/chat/completions", f.scoped, `{"model":"fast","messages":[{"role":"user","content":"x"}]}`).Code; code != http.StatusForbidden {
		t.Fatalf("empty allowlist chat: %d, want 403", code)
	}
}

// Allowing a canonical model also admits the aliases that resolve to it.
func TestAllowedTargetAdmitsItsAlias(t *testing.T) {
	f := newAccessFixture(t, []string{"openai/gpt-a"})
	if code := f.do("POST", "/v1/chat/completions", f.scoped, `{"model":"fast","messages":[{"role":"user","content":"x"}]}`).Code; code == http.StatusForbidden {
		t.Fatal("alias of an allowed model must be permitted")
	}
	ids := listedIDs(t, f.do("GET", "/v1/models", f.scoped, ""))
	if !ids["fast"] || !ids["openai/gpt-a"] || ids["openai/gpt-b"] {
		t.Fatalf("listing = %v", ids)
	}
}

// A restricted key must not reach a capacity-adapter pool member outside its
// allowlist, while a pool member it is allowed keeps working.
func TestPlanComboFiltersAdapterPoolByCallerKey(t *testing.T) {
	ctx := context.Background()
	manager := newManager(t)
	for _, m := range []runtime.Model{
		{ProviderID: "openai", ID: "text", Name: "text"},
		{ProviderID: "pool", ID: "vision-ok", Name: "v1", Capabilities: []string{"vision"}},
		{ProviderID: "pool", ID: "vision-secret", Name: "v2", Capabilities: []string{"vision"}},
	} {
		if err := manager.PutModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.PutCombo(ctx, runtime.Combo{Name: "auto", Members: []runtime.ComboMember{{ProviderID: "openai", ModelID: "text", Selected: true}}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetSettings(ctx, map[string]string{"capacityAdapterVision": `{"enabled":true,"roundRobin":false,"pool":[{"providerId":"pool","modelId":"vision-ok"},{"providerId":"pool","modelId":"vision-secret"}]}`}, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err = manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	h := New(manager, Options{})
	requirements := []string{"vision"}
	used := func(access auth.Entry) map[string]bool {
		ordered, ok := h.PlanCombo(snapshot, access, "auto", requirements)
		if !ok {
			t.Fatal("combo not planned")
		}
		out := map[string]bool{}
		for _, member := range ordered {
			out[member.ProviderID+"/"+member.ModelID] = true
		}
		return out
	}
	if got := used(auth.Entry{}); !got["pool/vision-ok"] || !got["pool/vision-secret"] {
		t.Fatalf("unrestricted key must see the whole pool: %v", got)
	}
	scoped := auth.Entry{Restricted: true, Allow: []string{"auto", "pool/vision-ok"}}
	got := used(scoped)
	if got["pool/vision-secret"] {
		t.Fatalf("restricted key reached pool member outside its list: %v", got)
	}
	if !got["pool/vision-ok"] || !got["openai/text"] {
		t.Fatalf("allowed pool member and Combo member must remain: %v", got)
	}
	none := used(auth.Entry{Restricted: true, Allow: []string{"auto"}})
	if none["pool/vision-ok"] || none["pool/vision-secret"] {
		t.Fatalf("key with no pool grants must get no adapter tier: %v", none)
	}
}

// authorize and dispatch load snapshots separately. If the key is paused,
// revoked or reloaded away between them, dispatch must deny, not widen.
func TestKeyMissAfterAuthenticationFailsClosed(t *testing.T) {
	ctx := context.Background()
	manager := newManager(t)
	entry, secret, err := manager.CreateAPIKeyWithAccess(ctx, "scoped", true, []string{"team"})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.PutModel(ctx, runtime.Model{ProviderID: "openai", ID: "gpt-a", Name: "gpt-a"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	before, _ := manager.Load()
	if !callerKey(req, before).Restricted {
		t.Fatal("live restricted key must stay restricted")
	}
	paused := true
	// Ordered: revoking is terminal, so it must run after the pause case.
	for _, step := range []struct {
		name   string
		mutate func() error
	}{
		{"paused", func() error { return manager.UpdateAPIKey(ctx, entry.ID, &paused, nil) }},
		{"revoked", func() error { return manager.DeleteAPIKey(ctx, entry.ID) }},
	} {
		name, mutate := step.name, step.mutate
		if err := mutate(); err != nil {
			t.Fatal(err)
		}
		after, _ := manager.Load()
		got := callerKey(req, after)
		if !got.Restricted || got.Permits("openai/gpt-a", "team", "anything") {
			t.Fatalf("%s key mid-request widened to %+v; must deny everything", name, got)
		}
		if requestPermitted(got, after, "openai/gpt-a") {
			t.Fatalf("%s key mid-request reached a model", name)
		}
		if name == "paused" {
			unpaused := false
			if err := manager.UpdateAPIKey(ctx, entry.ID, &unpaused, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Keys not required and no key presented: unrestricted by design.
	if _, err := manager.SetSettings(ctx, map[string]string{"requireApiKey": "false"}, nil); err != nil {
		t.Fatal(err)
	}
	open, _ := manager.Load()
	if callerKey(httptest.NewRequest("POST", "/v1/chat/completions", nil), open).Restricted {
		t.Fatal("with API keys not required the caller is unrestricted")
	}
}
