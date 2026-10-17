package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/auth"
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
	if rec := callAdmin(mux, cookie, http.MethodPatch, "/admin/v1/keys/"+created.Key.ID, `{"access":{"restricted":false}}`); rec.Code != http.StatusOK {
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

// Editing the list without restating `restricted` must never widen a key: a
// missing field is rejected instead of being read as false.
func TestKeyAccessRequiresExplicitRestrictedFlag(t *testing.T) {
	mux, cookie, manager, done := newAccessAPI(t)
	defer done()
	rec := callAdmin(mux, cookie, http.MethodPost, "/admin/v1/keys", `{"name":"scoped","access":{"restricted":true,"allow":["team"]}}`)
	var created struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	for _, body := range []string{
		`{"access":{"allow":["openai/gpt-4o"]}}`,
		`{"access":{}}`,
		`{"access":{"restricted":false,"allow":["openai/gpt-4o"]}}`,
	} {
		if rec := callAdmin(mux, cookie, http.MethodPatch, "/admin/v1/keys/"+created.Key.ID, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("PATCH %s: %d, want 400", body, rec.Code)
		}
	}
	if rec := callAdmin(mux, cookie, http.MethodPost, "/admin/v1/keys", `{"name":"x","access":{"allow":["a"]}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("POST without restricted: %d, want 400", rec.Code)
	}
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range snapshot.APIKeys().Records() {
		if entry.ID == created.Key.ID && (!entry.Restricted || len(entry.Allow) != 1 || entry.Allow[0] != "team") {
			t.Fatalf("rejected edits changed the key: %+v", entry)
		}
	}
}

func TestKeyAccessAcceptsMaximumLegalAllowlist(t *testing.T) {
	mux, cookie, _, done := newAccessAPI(t)
	defer done()
	entries := make([]string, auth.MaxAllowEntries)
	for i := range entries {
		entries[i] = fmt.Sprintf("p%04d/%s", i, strings.Repeat("m", auth.MaxAllowLength-6))
	}
	encoded, _ := json.Marshal(map[string]any{"name": "big", "access": map[string]any{"restricted": true, "allow": entries}})
	if rec := callAdmin(mux, cookie, http.MethodPost, "/admin/v1/keys", string(encoded)); rec.Code != http.StatusCreated {
		t.Fatalf("max legal allowlist (%d bytes): %d %.120s", len(encoded), rec.Code, rec.Body)
	}
}

func TestKeyPausedAndAccessChangeTogetherOrNotAtAll(t *testing.T) {
	mux, cookie, manager, done := newAccessAPI(t)
	defer done()
	ctx := context.Background()
	entry, _, err := manager.CreateAPIKeyWithAccess(ctx, "k", true, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if rec := callAdmin(mux, cookie, http.MethodPatch, "/admin/v1/keys/"+entry.ID, `{"paused":true,"access":{"restricted":true,"allow":["b"]}}`); rec.Code != http.StatusOK {
		t.Fatalf("combined patch: %d %s", rec.Code, rec.Body)
	}
	snapshot, _ := manager.Load()
	for _, e := range snapshot.APIKeys().Records() {
		if e.ID == entry.ID && (!e.Paused || len(e.Allow) != 1 || e.Allow[0] != "b") {
			t.Fatalf("combined patch not applied together: %+v", e)
		}
	}
	// An unknown key fails the whole request; nothing is half-applied.
	if rec := callAdmin(mux, cookie, http.MethodPatch, "/admin/v1/keys/missing", `{"paused":true,"access":{"restricted":true,"allow":["b"]}}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing key combined patch: %d", rec.Code)
	}
}

func TestCorruptStoredAllowlistFailsTheListInsteadOfShowingEmpty(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	cookie := adminCookie(t, handler)
	if _, err := store.DB().Exec(`INSERT INTO api_keys(id,name,key_hash,key_prefix,access_restricted,access_allow) VALUES('k','bad','h','rw_',1,'{not json')`); err != nil {
		t.Fatal(err)
	}
	if rec := callAdmin(mux, cookie, http.MethodGet, "/admin/v1/keys", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt allowlist listed as %d %s, want 500 instead of an empty allowlist", rec.Code, rec.Body)
	}
}
