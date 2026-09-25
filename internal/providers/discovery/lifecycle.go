package discovery

import (
	"context"
	"errors"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// Model is one catalog entry produced by discovery, reduced to the fields the
// runtime catalog stores.
type Model struct {
	ID   string
	Name string
}

// Store persists a provider's discovered catalog. It is implemented by the
// runtime manager, so discovery stays decoupled from storage.
type Store interface {
	ReplaceDiscoveredModels(ctx context.Context, providerID string, models []Model) error
}

// SpecResolver resolves the provider identity a connection belongs to. It lets
// the caller supply connection credentials/settings without this package
// depending on the connection store.
type SpecResolver func(providerID string) (registry.Spec, bool)

// Sync fetches one provider's live catalog and persists it, honoring the
// provider's declared discovery path and auth style. It is the production
// entry point for built-in provider model discovery: callers invoke it from an
// admin action, and it returns the number of models persisted. A provider that
// does not declare a base URL or is unknown is rejected rather than guessed.
func Sync(ctx context.Context, client *Client, store Store, providerID, apiKey string, allowPrivate bool) (int, error) {
	if store == nil {
		return 0, errors.New("discovery store is required")
	}
	catalog, err := registry.NewBuiltinCatalog()
	if err != nil {
		return 0, err
	}
	return SyncSpec(ctx, client, store, catalog, providerID, apiKey, allowPrivate)
}

// SyncSpec is Sync against a caller-supplied catalog, so tests and future
// provider sets can resolve identities without rebuilding the built-in catalog.
func SyncSpec(ctx context.Context, client *Client, store Store, catalog *registry.Catalog, providerID, apiKey string, allowPrivate bool) (int, error) {
	if store == nil {
		return 0, errors.New("discovery store is required")
	}
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return 0, errors.New("provider id is required")
	}
	spec, ok := catalog.Lookup(providerID)
	if !ok {
		return 0, errors.New("unknown provider " + providerID)
	}
	if strings.TrimSpace(spec.DefaultBaseURL) == "" {
		return 0, errors.New("provider " + providerID + " has no discoverable base URL")
	}
	if client == nil {
		client = &Client{}
	}
	if allowPrivate {
		client.AllowPrivateUpstreams = true
	}
	request := Request{
		ProviderID: providerID,
		BaseURL:    spec.DefaultBaseURL,
		Path:       spec.DiscoveryPath,
		APIKey:     apiKey,
		AuthStyle:  authStyleFor(spec.Auth),
	}
	ids, err := client.Models(ctx, request)
	if err != nil {
		return 0, err
	}
	models := make([]Model, 0, len(ids))
	for _, id := range ids {
		models = append(models, Model{ID: id})
	}
	if err := store.ReplaceDiscoveredModels(ctx, providerID, models); err != nil {
		return 0, err
	}
	return len(models), nil
}

func authStyleFor(kind registry.AuthKind) AuthStyle {
	switch kind {
	case registry.AuthNone:
		return AuthNone
	case registry.AuthOAuth:
		// OAuth/rotating credentials are supplied as an access token by the
		// caller; the OpenAI-compatible catalog still uses bearer auth.
		return AuthBearer
	default:
		return AuthBearer
	}
}
