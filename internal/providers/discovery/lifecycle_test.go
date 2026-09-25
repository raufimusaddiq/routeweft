package discovery

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

type recordingStore struct {
	mu         sync.Mutex
	providerID string
	models     []Model
	calls      int
}

func (s *recordingStore) ReplaceDiscoveredModels(_ context.Context, providerID string, models []Model) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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

func TestSyncSpecDoesNotMutateSharedClientPolicy(t *testing.T) {
	client := &Client{HTTP: fakeClient{do: func(*http.Request) (*http.Response, error) {
		return response(200, `{"data":[{"id":"m"}]}`), nil
	}}}
	store := &recordingStore{}
	if _, err := Sync(context.Background(), client, store, "openrouter", "key", true); err != nil {
		t.Fatal(err)
	}
	if client.AllowPrivateUpstreams {
		t.Fatal("shared client policy was mutated by an allowPrivate call")
	}
	// A later strict call must not inherit trusted-local access.
	if _, err := Sync(context.Background(), client, store, "openrouter", "key", false); err != nil {
		t.Fatal(err)
	}
	if client.AllowPrivateUpstreams {
		t.Fatal("shared client policy retained trusted-local access")
	}
}

func TestSyncSpecConcurrentPolicyIsolation(t *testing.T) {
	client := &Client{HTTP: fakeClient{do: func(*http.Request) (*http.Response, error) {
		return response(200, `{"data":[{"id":"m"}]}`), nil
	}}}
	store := &recordingStore{}
	var wait sync.WaitGroup
	for i := 0; i < 16; i++ {
		allowPrivate := i%2 == 0
		wait.Add(1)
		go func(private bool) {
			defer wait.Done()
			_, _ = Sync(context.Background(), client, store, "openrouter", "key", private)
		}(allowPrivate)
	}
	wait.Wait()
	if client.AllowPrivateUpstreams {
		t.Fatal("concurrent calls leaked trusted-local policy onto shared client")
	}
}
