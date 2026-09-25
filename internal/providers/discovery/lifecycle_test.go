package discovery

import (
	"context"
	"net/http"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

type recordingStore struct {
	providerID string
	models     []Model
	calls      int
}

func (s *recordingStore) ReplaceDiscoveredModels(_ context.Context, providerID string, models []Model) error {
	s.calls++
	s.providerID = providerID
	s.models = append([]Model(nil), models...)
	return nil
}

func TestSyncSpecFetchesAndPersistsDeclaredCatalog(t *testing.T) {
	var seenPath, seenAuth string
	client := &Client{HTTP: fakeClient{do: func(request *http.Request) (*http.Response, error) {
		seenPath = request.URL.Path
		seenAuth = request.Header.Get("Authorization")
		return response(200, `{"data":[{"id":"anthropic/claude-sonnet-4"},{"id":"openai/gpt-4.1"}]}`), nil
	}}}
	catalog, err := registry.NewBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	store := &recordingStore{}
	count, err := SyncSpec(context.Background(), client, store, catalog, "kilocode", "kilo-key", false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || store.calls != 1 || store.providerID != "kilocode" || len(store.models) != 2 {
		t.Fatalf("count=%d store=%+v", count, store)
	}
	// The declared discovery path is honored and the credential is bearer.
	if seenPath != "/api/gateway/models" || seenAuth != "Bearer kilo-key" {
		t.Fatalf("path=%q auth=%q", seenPath, seenAuth)
	}
}

func TestSyncSpecRejectsUnknownAndUndiscoverable(t *testing.T) {
	catalog, err := registry.NewBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	store := &recordingStore{}
	if _, err := SyncSpec(context.Background(), nil, store, catalog, "does-not-exist", "", false); err == nil {
		t.Fatal("accepted unknown provider")
	}
	// azure seeds an empty base URL: discovery must fail rather than guess.
	if _, err := SyncSpec(context.Background(), nil, store, catalog, "azure", "", false); err == nil {
		t.Fatal("accepted provider without a discoverable base URL")
	}
	if _, err := SyncSpec(context.Background(), nil, nil, catalog, "kilocode", "", false); err == nil {
		t.Fatal("accepted nil store")
	}
	if store.calls != 0 {
		t.Fatalf("store mutated on rejected call: %+v", store)
	}
}

func TestSyncResolvesBuiltinCatalog(t *testing.T) {
	client := &Client{HTTP: fakeClient{do: func(*http.Request) (*http.Response, error) {
		return response(200, `{"data":[{"id":"m"}]}`), nil
	}}}
	store := &recordingStore{}
	count, err := Sync(context.Background(), client, store, "openrouter", "key", false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || store.providerID != "openrouter" {
		t.Fatalf("count=%d store=%+v", count, store)
	}
}
