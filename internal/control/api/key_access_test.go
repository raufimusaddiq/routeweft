package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func newAccessAPI(t *testing.T) (*http.ServeMux, *http.Cookie, *runtime.Manager, func()) {
	t.Helper()
	handler, mux, store := newReadAPI(t)
	manager := handler.opts.Runtime.(*runtime.Manager)
	handler.opts.Keys = manager
	return mux, adminCookie(t, handler), manager, func() { store.Close() }
}

func keyList(t *testing.T, mux *http.ServeMux, cookie *http.Cookie) []map[string]any {
	t.Helper()
	rec := callAdmin(mux, cookie, http.MethodGet, "/admin/v1/keys", "")
	var got listPage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got.Items
}

func TestKeyAccessPolicyLifecycleThroughAdminAPI(t *testing.T) {
	mux, cookie, manager, done := newAccessAPI(t)
	defer done()
	rec := callAdmin(mux, cookie, http.MethodPost, "/admin/v1/keys", `{"name":"scoped","access":{"restricted":true,"allow":[" fast ","team","fast"]}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var created struct {
		Key struct {
			ID     string         `json:"id"`
			Access map[string]any `json:"access"`
		} `json:"key"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Key.Access["restricted"] != true {
		t.Fatalf("created key access = %v", created.Key.Access)
	}
	items := keyList(t, mux, cookie)
	access := items[0]["access"].(map[string]any)
	allow := access["allow"].([]any)
	if access["restricted"] != true || len(allow) != 2 || allow[0] != "fast" || allow[1] != "team" {
		t.Fatalf("listed access = %v, want restricted [fast team]", access)
	}
	// The compiled key index (what ingress enforces) carries the same policy.
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, entry := range snapshot.APIKeys().Records() {
		if entry.ID == created.Key.ID {
			found = entry.Restricted && len(entry.Allow) == 2
		}
	}
	if !found {
		t.Fatal("compiled key index does not carry the policy the API reported")
	}

	// Edit the allowlist without touching pause state.
	if rec := callAdmin(mux, cookie, http.MethodPatch, "/admin/v1/keys/"+created.Key.ID, `{"access":{"restricted":true,"allow":["openai/gpt-4o"]}}`); rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	items = keyList(t, mux, cookie)
	if items[0]["paused"] != false || len(items[0]["access"].(map[string]any)["allow"].([]any)) != 1 {
		t.Fatalf("after patch: %v", items[0])
	}
	// Pausing alone keeps the policy.
	if rec := callAdmin(mux, cookie, http.MethodPatch, "/admin/v1/keys/"+created.Key.ID, `{"paused":true}`); rec.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", rec.Code, rec.Body)
	}
	if items = keyList(t, mux, cookie); items[0]["access"].(map[string]any)["restricted"] != true {
		t.Fatalf("pause dropped the policy: %v", items[0])
	}
	// Switching to unrestricted clears the stored list.
	if rec := callAdmin(mux, cookie, http.MethodPatch, "/admin/v1/keys/"+created.Key.ID, `{"access":{"restricted":false,"allow":["stale"]}}`); rec.Code != http.StatusOK {
		t.Fatalf("unrestrict: %d %s", rec.Code, rec.Body)
	}
	access = keyList(t, mux, cookie)[0]["access"].(map[string]any)
	if access["restricted"] != false || len(access["allow"].([]any)) != 0 {
		t.Fatalf("unrestricted key kept a stale list: %v", access)
	}
}

func TestKeyAccessValidationAndNotFound(t *testing.T) {
	mux, cookie, _, done := newAccessAPI(t)
	defer done()
	for _, body := range []string{
		`{"name":"x","access":{"restricted":true,"allow":[""]}}`,
		`{"name":"x","access":{"restricted":true,"allow":["` + strings.Repeat("a", 300) + `"]}}`,
	} {
		if rec := callAdmin(mux, cookie, http.MethodPost, "/admin/v1/keys", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid access %.40s: %d, want 400", body, rec.Code)
		}
	}
	if rec := callAdmin(mux, cookie, http.MethodPatch, "/admin/v1/keys/missing", `{"access":{"restricted":true,"allow":["a"]}}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing key: %d, want 404", rec.Code)
	}
	if rec := callAdmin(mux, cookie, http.MethodPatch, "/admin/v1/keys/missing", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: %d, want 400", rec.Code)
	}
}
