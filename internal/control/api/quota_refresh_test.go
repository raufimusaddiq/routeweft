package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

func TestQuotaRefreshRequiresSession(t *testing.T) {
	_, mux, store, _, _ := newProvidersAPI(t, false, nil)
	defer store.Close()
	if recorder := doJSON(t, mux, nil, http.MethodPost, "/admin/v1/quota/refresh", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", recorder.Code)
	}
}

func TestQuotaRefreshRunsDetachedAndReturnsAccepted(t *testing.T) {
	called := make(chan struct{}, 1)
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	handler.opts.QuotaRefresher = func(ctx context.Context) error {
		called <- struct{}{}
		return nil
	}
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/quota/refresh", "")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("quota refresher was not invoked")
	}
}

// The quota read model reflects RuntimeState observations: remaining, reset and
// cooldown state are visible to the operator.
func TestQuotaReadModelSurfacesState(t *testing.T) {
	handler, mux, store, credStore, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	ctx := context.Background()
	node, err := credStore.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "openai", Name: "openai", Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := credStore.PutConnection(ctx, credentials.Connection{NodeID: node.ID, Name: "primary", Identity: "acct", AuthKind: credentials.AuthAPIKey, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	remaining := 42.0
	reset := time.Now().Add(time.Hour).UTC()
	handler.opts.Runtime.State().ObserveQuotaResult(connection.ID, &remaining, reset, time.Now().UTC(), "")
	recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/quota", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !containsAll(recorder.Body.String(), `"status":"available"`, `"remaining":42`, `"resetAt"`, `"observedAt"`) {
		t.Fatalf("quota body=%s", recorder.Body.String())
	}
}
