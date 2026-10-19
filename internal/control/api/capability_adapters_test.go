package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func TestCapabilityAdapterMutationsRequireSession(t *testing.T) {
	_, mux, store, _, _ := newProvidersAPI(t, false, nil)
	defer store.Close()
	if recorder := doJSON(t, mux, nil, http.MethodGet, "/admin/v1/capability-adapters", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("get status=%d want 401", recorder.Code)
	}
	if recorder := doJSON(t, mux, nil, http.MethodPut, "/admin/v1/capability-adapters/vision", `{}`); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("put status=%d want 401", recorder.Code)
	}
}

func TestCapabilityAdapterReadDefaultsToNoOp(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/capability-adapters", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Items []struct {
			Capability string `json:"capability"`
			Enabled    bool   `json:"enabled"`
			Pool       []struct {
				ProviderID string `json:"providerId"`
				ModelID    string `json:"modelId"`
			} `json:"pool"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 4 {
		t.Fatalf("items=%d want 4", len(body.Items))
	}
	for _, item := range body.Items {
		if item.Enabled || len(item.Pool) != 0 {
			t.Fatalf("adapter %s should default to disabled empty pool: %+v", item.Capability, item)
		}
	}
}

func TestCapabilityAdapterPutValidatesAndPersists(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	ctx := context.Background()
	if err := handler.opts.ProviderCatalog.PutModel(ctx, runtime.Model{ProviderID: "openai", ID: "gpt-5", Source: "custom"}); err != nil {
		t.Fatal(err)
	}

	unknown := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/smell", `{"enabled":true}`)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown capability status=%d", unknown.Code)
	}
	badRef := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"enabled":true,"pool":[{"providerId":"openai","modelId":"ghost"}]}`)
	if badRef.Code != http.StatusBadRequest {
		t.Fatalf("unknown model status=%d body=%s", badRef.Code, badRef.Body.String())
	}

	saved := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"enabled":true,"pool":[{"providerId":"openai","modelId":"gpt-5"}]}`)
	if saved.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", saved.Code, saved.Body.String())
	}
	read := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/capability-adapters", "")
	if !containsAll(read.Body.String(), `"capability":"vision"`, `"enabled":true`, `"modelId":"gpt-5"`) {
		t.Fatalf("read body=%s", read.Body.String())
	}

	// Enabled with an empty pool is a deliberate no-op and must be accepted.
	cleared := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"enabled":true,"pool":[]}`)
	if cleared.Code != http.StatusOK || !containsAll(cleared.Body.String(), `"pool":[]`) {
		t.Fatalf("empty pool status=%d body=%s", cleared.Code, cleared.Body.String())
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}
