package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func adminCookie(t *testing.T, h *Handler) *http.Cookie {
	t.Helper()
	if err := bootstrapOperator(context.Background(), h.opts.Accounts); err != nil {
		t.Fatal(err)
	}
	session, err := h.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: session.ID}
}

func callAdmin(mux *http.ServeMux, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.AddCookie(cookie)
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type listPage struct {
	Items []map[string]any `json:"items"`
	Page  int              `json:"page"`
	Total int64            `json:"total"`
}

// TestProviderListsPageBeyondFirstHundred proves the read models expose every
// row of a >100-row provider through successive pages, filtered by providerId.
func TestProviderListsPageBeyondFirstHundred(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	cookie := adminCookie(t, handler)
	db := store.DB()
	for i := 0; i < 250; i++ {
		id := fmt.Sprintf("m%03d", i)
		if _, err := db.Exec(`INSERT INTO provider_models(provider_id,model_id) VALUES('openai',?)`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO pricing_overrides(provider_id,model_id,input_per_mtok) VALUES('openai',?,1)`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO model_aliases(alias,provider_id,model_id) VALUES(?, 'openai',?)`, "a"+id, id); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := db.Exec(`INSERT INTO provider_models(provider_id,model_id) VALUES('other',?)`, fmt.Sprintf("o%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	for _, resource := range []string{"models", "pricing", "aliases"} {
		seen := map[string]bool{}
		for page := 1; page <= 3; page++ {
			rec := callAdmin(mux, cookie, http.MethodGet, fmt.Sprintf("/admin/v1/%s?providerId=openai&page=%d&pageSize=100", resource, page), "")
			if rec.Code != http.StatusOK {
				t.Fatalf("%s page %d: %d %s", resource, page, rec.Code, rec.Body)
			}
			var got listPage
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Total != 250 {
				t.Fatalf("%s total=%d want 250 (providerId filter must exclude other providers)", resource, got.Total)
			}
			wantLen := 100
			if page == 3 {
				wantLen = 50
			}
			if len(got.Items) != wantLen {
				t.Fatalf("%s page %d len=%d want %d", resource, page, len(got.Items), wantLen)
			}
			for _, item := range got.Items {
				key := fmt.Sprint(item["id"], item["modelId"], item["alias"])
				if seen[key] {
					t.Fatalf("%s: duplicate row %s across pages", resource, key)
				}
				seen[key] = true
			}
		}
		if len(seen) != 250 {
			t.Fatalf("%s: saw %d distinct rows across pages, want 250", resource, len(seen))
		}
	}
}

func TestDeleteCustomModelRemovesAliasesAndRejectsOthers(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	cookie := adminCookie(t, handler)
	if rec := callAdmin(mux, cookie, http.MethodPost, "/admin/v1/models", `{"providerId":"openai","id":"mine","name":"Mine"}`); rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	manager := handler.opts.Runtime.(*runtime.Manager)
	if _, err := manager.PutCombo(context.Background(), runtime.Combo{Name: "c1", Members: []runtime.ComboMember{{ProviderID: "openai", ModelID: "mine", Selected: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO pricing_overrides(provider_id,model_id,input_per_mtok) VALUES('openai','mine',2)`); err != nil {
		t.Fatal(err)
	}
	if rec := callAdmin(mux, cookie, http.MethodPost, "/admin/v1/aliases", `{"alias":"mine-alias","providerId":"openai","modelId":"mine"}`); rec.Code != http.StatusOK {
		t.Fatalf("alias: %d %s", rec.Code, rec.Body)
	}
	{
		rec := callAdmin(mux, cookie, http.MethodGet, "/admin/v1/models?providerId=openai", "")
		var got listPage
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if len(got.Items) != 1 || got.Items[0]["custom"] != true {
			t.Fatalf("custom flag missing before delete: %s", rec.Body)
		}
	}
	rec := callAdmin(mux, cookie, http.MethodDelete, "/admin/v1/models", `{"providerId":"openai","modelId":"mine"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "model_in_use") || !strings.Contains(rec.Body.String(), "c1") {
		t.Fatalf("delete while a Combo uses the model: %d %s, want 409 naming c1", rec.Code, rec.Body)
	}
	if snapshot, err := manager.Load(); err != nil || len(snapshot.Combos()) != 1 || len(snapshot.Combos()[0].Members) != 1 {
		t.Fatalf("refused delete must leave the Combo untouched: %v", err)
	}
	if rec := callAdmin(mux, cookie, http.MethodGet, "/admin/v1/models?providerId=openai", ""); !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Fatalf("refused delete removed the model: %s", rec.Body)
	}
	if _, err := manager.DeleteCombo(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	if rec := callAdmin(mux, cookie, http.MethodDelete, "/admin/v1/models", `{"providerId":"openai","modelId":"mine"}`); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	for _, resource := range []string{"models", "aliases"} {
		rec := callAdmin(mux, cookie, http.MethodGet, "/admin/v1/"+resource+"?providerId=openai", "")
		var got listPage
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if got.Total != 0 {
			t.Fatalf("%s still has %d rows after model delete", resource, got.Total)
		}
	}
	if rec := callAdmin(mux, cookie, http.MethodGet, "/admin/v1/pricing?providerId=openai", ""); !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("pricing row survived model delete: %s", rec.Body)
	}
	if rec := callAdmin(mux, cookie, http.MethodDelete, "/admin/v1/models", `{"providerId":"openai","modelId":"mine"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d want 404", rec.Code)
	}
}

// An unrelated catalog mutation must not move provider_models.updated_at.
func TestUnrelatedMutationPreservesModelUpdatedAt(t *testing.T) {
	handler, _, store := newReadAPI(t)
	defer store.Close()
	manager := handler.opts.Runtime.(*runtime.Manager)
	ctx := context.Background()
	if err := manager.PutModel(ctx, runtime.Model{ProviderID: "openai", ID: "stable"}); err != nil {
		t.Fatal(err)
	}
	const stamp = "2001-02-03T04:05:06.000Z"
	if _, err := store.DB().Exec(`UPDATE provider_models SET updated_at=? WHERE model_id='stable'`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.CreateAPIKey(ctx, "unrelated"); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := store.DB().QueryRow(`SELECT updated_at FROM provider_models WHERE model_id='stable'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != stamp {
		t.Fatalf("updated_at=%q, want preserved %q", got, stamp)
	}
}

func TestDeleteNonCustomModelIsConflictNotNotFound(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	cookie := adminCookie(t, handler)
	manager := handler.opts.Runtime.(*runtime.Manager)
	ctx := context.Background()
	if err := manager.ReplaceDiscoveredModels(ctx, "openai", []runtime.Model{{ID: "found"}, {ID: "stale"}}); err != nil {
		t.Fatal(err)
	}
	rec := callAdmin(mux, cookie, http.MethodDelete, "/admin/v1/models", `{"providerId":"openai","modelId":"found"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "model_not_custom") {
		t.Fatalf("discovered model delete: %d %s, want 409 model_not_custom", rec.Code, rec.Body)
	}
	if rec := callAdmin(mux, cookie, http.MethodDelete, "/admin/v1/models", `{"providerId":"openai","modelId":"absent"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing model delete: %d, want 404", rec.Code)
	}
	// A refresh that drops a discovered model removes its row without a table scan.
	if err := manager.ReplaceDiscoveredModels(ctx, "openai", []runtime.Model{{ID: "found"}}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM provider_models WHERE model_id='stale'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("stale discovered row count=%d err=%v, want 0", n, err)
	}
}

func TestDeleteModelRefusedWhileCapacityAdapterPoolUsesIt(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	cookie := adminCookie(t, handler)
	if rec := callAdmin(mux, cookie, http.MethodPost, "/admin/v1/models", `{"providerId":"openai","id":"vis"}`); rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec := callAdmin(mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"enabled":true,"pool":[{"providerId":"openai","modelId":"vis"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("adapter: %d %s", rec.Code, rec.Body)
	}
	rec := callAdmin(mux, cookie, http.MethodDelete, "/admin/v1/models", `{"providerId":"openai","modelId":"vis"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "model_in_use") || !strings.Contains(rec.Body.String(), "capacityAdapterVision") {
		t.Fatalf("delete with adapter pool member: %d %s, want 409 naming capacityAdapterVision", rec.Code, rec.Body)
	}
	if rec := callAdmin(mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"enabled":false,"pool":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("clear adapter: %d %s", rec.Code, rec.Body)
	}
	if rec := callAdmin(mux, cookie, http.MethodDelete, "/admin/v1/models", `{"providerId":"openai","modelId":"vis"}`); rec.Code != http.StatusOK {
		t.Fatalf("delete after clearing pool: %d %s", rec.Code, rec.Body)
	}
}

// Built-in models have no provider_models row; their disabled state must still
// be readable (and settable) so the dashboard can show Disable/Enable for them.
func TestDisabledModelsListCoversModelsWithoutCatalogRows(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	cookie := adminCookie(t, handler)
	if rec := callAdmin(mux, cookie, http.MethodPatch, "/admin/v1/models/disabled", `{"providerId":"openai","modelId":"builtin-only","disabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body)
	}
	rec := callAdmin(mux, cookie, http.MethodGet, "/admin/v1/models/disabled?providerId=openai", "")
	var got listPage
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got.Total != 1 || got.Items[0]["modelId"] != "builtin-only" {
		t.Fatalf("disabled list: %d %s", rec.Code, rec.Body)
	}
	if rec := callAdmin(mux, cookie, http.MethodGet, "/admin/v1/models/disabled?providerId=other", ""); !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("providerId filter not applied: %s", rec.Body)
	}
}
