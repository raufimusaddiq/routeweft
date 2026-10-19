package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func seedComboCatalog(t *testing.T, handler *Handler) {
	t.Helper()
	ctx := context.Background()
	if err := handler.opts.ProviderCatalog.PutModel(ctx, runtime.Model{ProviderID: "openai", ID: "gpt-5", Source: "custom"}); err != nil {
		t.Fatal(err)
	}
	if err := handler.opts.ProviderCatalog.PutModel(ctx, runtime.Model{ProviderID: "openai", ID: "gpt-5-mini", Source: "custom"}); err != nil {
		t.Fatal(err)
	}
}

func TestComboMutationsRequireSession(t *testing.T) {
	_, mux, store, _, _ := newProvidersAPI(t, false, nil)
	defer store.Close()
	for _, target := range []struct{ method, path string }{
		{http.MethodPost, "/admin/v1/combos"},
		{http.MethodPut, "/admin/v1/combos/combo-1"},
		{http.MethodDelete, "/admin/v1/combos/combo-1"},
	} {
		recorder := doJSON(t, mux, nil, target.method, target.path, `{}`)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d want 401", target.method, target.path, recorder.Code)
		}
	}
}

func TestComboCreateReadAndDelete(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	seedComboCatalog(t, handler)

	created := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/combos", `{"name":"daily","strategy":"sticky-round-robin","stickyLimit":3,"fusionEnabled":true,"judgeModel":"gpt-5","members":[{"providerId":"openai","modelId":"gpt-5"},{"providerId":"openai","modelId":"gpt-5-mini","selected":false}]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var combo struct {
		ID          string `json:"id"`
		Strategy    string `json:"strategy"`
		StickyLimit uint64 `json:"stickyLimit"`
		Fusion      bool   `json:"fusionEnabled"`
		ConfigRev   uint64 `json:"configRevision"`
		Members     []struct {
			ProviderID string `json:"providerId"`
			ModelID    string `json:"modelId"`
			Position   int    `json:"position"`
			Selected   bool   `json:"selected"`
		} `json:"members"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &combo); err != nil {
		t.Fatal(err)
	}
	if combo.Strategy != "sticky-round-robin" || combo.StickyLimit != 3 || !combo.Fusion || len(combo.Members) != 2 || combo.Members[1].Selected {
		t.Fatalf("unexpected combo: %+v", combo)
	}

	list := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/combos", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"name":"daily"`) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}

	bad := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/combos", `{"name":"broken","members":[{"providerId":"openai","modelId":"ghost"}]}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown member status=%d", bad.Code)
	}

	updated := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/combos/"+combo.ID, `{"name":"daily","strategy":"fallback","stickyLimit":1,"members":[{"providerId":"openai","modelId":"gpt-5-mini"}]}`)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"strategy":"fallback"`) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}

	deleted := doJSON(t, mux, cookie, http.MethodDelete, "/admin/v1/combos/"+combo.ID, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	missing := doJSON(t, mux, cookie, http.MethodDelete, "/admin/v1/combos/"+combo.ID, "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("second delete status=%d", missing.Code)
	}
}

// PRD-COMBO-001 requires aliases to be usable as Combo members.
func TestComboAcceptsAliasMember(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	seedComboCatalog(t, handler)
	ctx := context.Background()
	if err := handler.opts.ProviderCatalog.PutAlias(ctx, "smart", runtime.ModelRef{ProviderID: "openai", ModelID: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	created := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/combos", `{"name":"aliased","members":[{"providerId":"openai","modelId":"smart"}]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("alias member status=%d body=%s", created.Code, created.Body.String())
	}
	if !strings.Contains(created.Body.String(), `"modelId":"gpt-5"`) {
		t.Fatalf("alias not resolved in response: %s", created.Body.String())
	}
}
