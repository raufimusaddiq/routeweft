// Package runtime owns immutable request configuration and mutable live state.
package runtime

import "github.com/raufimusaddiq/routeweft/internal/auth"

// RuntimeSnapshot is immutable after publication. Accessors return copies of
// mutable values so consumers cannot modify the active configuration.
type RuntimeSnapshot struct {
	version        uint64
	configRevision uint64
	settings       map[string]string
	keys           *auth.KeyIndex
	models         map[string]Model
	aliases        map[string]ModelRef
	disabledModels map[string]struct{}
}

func (s *RuntimeSnapshot) Version() uint64        { return s.version }
func (s *RuntimeSnapshot) ConfigRevision() uint64 { return s.configRevision }
func (s *RuntimeSnapshot) Settings() map[string]string {
	return cloneSettings(s.settings)
}
func (s *RuntimeSnapshot) APIKeys() *auth.KeyIndex { return s.keys }

// Models returns the enabled catalog and compiled aliases as a defensive copy.
func (s *RuntimeSnapshot) Models() []Model { return listCatalog(s.models, s.aliases, s.disabledModels) }

// ResolveModel resolves a direct provider/model pair or a model alias.
func (s *RuntimeSnapshot) ResolveModel(provider, model string) (Model, bool) {
	if target, ok := s.aliases[model]; ok && (provider == "" || provider == target.ProviderID) {
		provider, model = target.ProviderID, target.ModelID
	}
	key := modelKey(provider, model)
	if _, disabled := s.disabledModels[key]; disabled {
		return Model{}, false
	}
	entry, ok := s.models[key]
	entry.Capabilities = append([]string(nil), entry.Capabilities...)
	return entry, ok
}
