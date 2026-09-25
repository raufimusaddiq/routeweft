package runtime

import (
	"context"

	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
)

// ReplaceDiscoveredCatalogFor persists a discovery client's model list for one
// provider, adapting discovery.Model to the runtime catalog model. It is the
// production bridge between the discovery client and the durable catalog.
func (m *Manager) ReplaceDiscoveredCatalogFor(ctx context.Context, providerID string, models []discovery.Model) error {
	runtimeModels := make([]Model, 0, len(models))
	for _, model := range models {
		runtimeModels = append(runtimeModels, Model{ID: model.ID, Name: model.Name})
	}
	return m.ReplaceDiscoveredModels(ctx, providerID, runtimeModels)
}
