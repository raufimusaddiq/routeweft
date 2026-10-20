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

// #78/C04: the API carries the pin's roundRobin flag and derives strategy from
// it; a PUT that omits roundRobin keeps the stored policy.
func TestCapabilityAdapterRoundRobinRoundTrips(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	ctx := context.Background()
	for _, id := range []string{"gpt-a", "gpt-b"} {
		if err := handler.opts.ProviderCatalog.PutModel(ctx, runtime.Model{ProviderID: "openai", ID: id, Source: "custom"}); err != nil {
			t.Fatal(err)
		}
	}
	pool := `"pool":[{"providerId":"openai","modelId":"gpt-a"},{"providerId":"openai","modelId":"gpt-b"}]`
	saved := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"enabled":true,"roundRobin":true,`+pool+`}`)
	if saved.Code != http.StatusOK || !containsAll(saved.Body.String(), `"roundRobin":true`, `"strategy":"round-robin"`) {
		t.Fatalf("save status=%d body=%s", saved.Code, saved.Body.String())
	}
	// Omitting roundRobin must not reset it.
	kept := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"enabled":true,`+pool+`}`)
	if kept.Code != http.StatusOK || !containsAll(kept.Body.String(), `"roundRobin":true`) {
		t.Fatalf("omitted roundRobin reset the policy: %d %s", kept.Code, kept.Body.String())
	}
	// A disabled pool reports fallback even with roundRobin stored (pin capacityAdapter.js:59).
	off := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"enabled":false,"roundRobin":true,`+pool+`}`)
	if off.Code != http.StatusOK || !containsAll(off.Body.String(), `"roundRobin":true`, `"strategy":"fallback"`) {
		t.Fatalf("disabled body=%s", off.Body.String())
	}
	read := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/capability-adapters", "")
	if !containsAll(read.Body.String(), `"capability":"vision"`, `"roundRobin":true`, `"capability":"pdf"`) {
		t.Fatalf("read body=%s", read.Body.String())
	}
	snapshot, err := handler.opts.Runtime.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.CapacityAdapter("vision"); got.Enabled || !got.RoundRobin || len(got.Pool) != 2 {
		t.Fatalf("compiled=%+v", got)
	}
}

// Values written before C04 have no roundRobin key; GET and PUT must treat them
// as fallback instead of dereferencing a nil flag.
func TestCapabilityAdapterPreC04StoredValueDoesNotPanic(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	ctx := context.Background()
	if err := handler.opts.ProviderCatalog.PutModel(ctx, runtime.Model{ProviderID: "openai", ID: "gpt-a", Source: "custom"}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.opts.Settings.SetSettings(ctx, map[string]string{"capacityAdapterVision": `{"enabled":true,"pool":[{"providerId":"openai","modelId":"gpt-a"}]}`}, nil); err != nil {
		t.Fatal(err)
	}
	read := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/capability-adapters", "")
	if read.Code != http.StatusOK || !containsAll(read.Body.String(), `"roundRobin":false`, `"strategy":"fallback"`, `"modelId":"gpt-a"`) {
		t.Fatalf("get status=%d body=%s", read.Code, read.Body.String())
	}
	put := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"enabled":true,"pool":[{"providerId":"openai","modelId":"gpt-a"}]}`)
	if put.Code != http.StatusOK || !containsAll(put.Body.String(), `"roundRobin":false`) {
		t.Fatalf("put status=%d body=%s", put.Code, put.Body.String())
	}
}

// A partial PUT keeps every omitted field; it must not disable the adapter or
// empty its pool.
func TestCapabilityAdapterPartialPutKeepsStoredState(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	if err := handler.opts.ProviderCatalog.PutModel(context.Background(), runtime.Model{ProviderID: "openai", ID: "gpt-a", Source: "custom"}); err != nil {
		t.Fatal(err)
	}
	seed := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"enabled":true,"roundRobin":false,"pool":[{"providerId":"openai","modelId":"gpt-a"}]}`)
	if seed.Code != http.StatusOK {
		t.Fatalf("seed %d %s", seed.Code, seed.Body.String())
	}
	partial := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"roundRobin":true}`)
	if partial.Code != http.StatusOK || !containsAll(partial.Body.String(), `"enabled":true`, `"roundRobin":true`, `"strategy":"round-robin"`, `"modelId":"gpt-a"`) {
		t.Fatalf("partial PUT lost state: %d %s", partial.Code, partial.Body.String())
	}
	toggle := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"enabled":false}`)
	if toggle.Code != http.StatusOK || !containsAll(toggle.Body.String(), `"roundRobin":true`, `"modelId":"gpt-a"`, `"strategy":"fallback"`) {
		t.Fatalf("toggle lost state: %d %s", toggle.Code, toggle.Body.String())
	}
	cleared := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"enabled":true,"pool":[]}`)
	if cleared.Code != http.StatusOK || !containsAll(cleared.Body.String(), `"pool":[]`, `"strategy":"fallback"`) {
		t.Fatalf("explicit empty pool: %d %s", cleared.Code, cleared.Body.String())
	}
}

// An imported pool can hold a member that no longer resolves. Only members the
// body supplies are checked, so the adapter stays editable.
func TestCapabilityAdapterStoredUnresolvedMemberStaysEditable(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	ctx := context.Background()
	if err := handler.opts.ProviderCatalog.PutModel(ctx, runtime.Model{ProviderID: "openai", ID: "gpt-a", Source: "custom"}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.opts.Settings.SetSettings(ctx, map[string]string{"capacityAdapterVision": `{"enabled":true,"roundRobin":false,"pool":[{"providerId":"openai","modelId":"gpt-pin-alias"}]}`}, nil); err != nil {
		t.Fatal(err)
	}
	partial := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"roundRobin":true}`)
	if partial.Code != http.StatusOK || !containsAll(partial.Body.String(), `"roundRobin":true`, `"modelId":"gpt-pin-alias"`) {
		t.Fatalf("partial PUT on imported pool: %d %s", partial.Code, partial.Body.String())
	}
	// The UI resends the whole stored pool when adding a member.
	add := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"enabled":true,"pool":[{"providerId":"openai","modelId":"gpt-pin-alias"},{"providerId":"openai","modelId":"gpt-a"}]}`)
	if add.Code != http.StatusOK || !containsAll(add.Body.String(), `"modelId":"gpt-pin-alias"`, `"modelId":"gpt-a"`) {
		t.Fatalf("resend with new member: %d %s", add.Code, add.Body.String())
	}
	// A member that is neither stored nor resolvable is still refused.
	bad := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"pool":[{"providerId":"openai","modelId":"ghost"}]}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("new unresolved member accepted: %d", bad.Code)
	}
	remove := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/capability-adapters/vision", `{"pool":[{"providerId":"openai","modelId":"gpt-a"}]}`)
	if remove.Code != http.StatusOK || containsAll(remove.Body.String(), `gpt-pin-alias`) {
		t.Fatalf("remove: %d %s", remove.Code, remove.Body.String())
	}
}
