package runtime

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/raufimusaddiq/routeweft/internal/auth"
)

var ErrAPIKeyNotFound = errors.New("API key not found")

// CreateAPIKey persists and publishes a newly created client key, returning its
// one-time plaintext value.
func (m *Manager) CreateAPIKey(ctx context.Context, name string) (auth.Entry, string, error) {
	return m.CreateAPIKeyWithAccess(ctx, name, false, nil)
}

// CreateAPIKeyWithAccess creates a key and its access policy in one atomic
// mutation, so a restricted key is never briefly unrestricted.
func (m *Manager) CreateAPIKeyWithAccess(ctx context.Context, name string, restricted bool, allow []string) (auth.Entry, string, error) {
	allow, err := auth.NormalizeAllow(allow)
	if err != nil {
		return auth.Entry{}, "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return auth.Entry{}, "", errors.New("API key name is required")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return auth.Entry{}, "", fmt.Errorf("generate API key: %w", err)
	}
	plaintext := "rw_" + base64.RawURLEncoding.EncodeToString(secret[:])
	entry := auth.Entry{ID: uuid.NewString(), Name: name, Prefix: displayPrefix(plaintext), Hash: auth.Hash(plaintext), Restricted: restricted, Allow: allow}
	_, err = m.Update(ctx, func(candidate *Candidate) error {
		for _, existing := range candidate.APIKeys {
			if existing.Hash == entry.Hash {
				return errors.New("API key already exists")
			}
		}
		candidate.APIKeys = append(candidate.APIKeys, entry)
		return nil
	})
	if err != nil {
		return auth.Entry{}, "", err
	}
	entry.Hash = ""
	return entry, plaintext, nil
}

// UpdateAPIKey applies a pause change and/or an access-policy replacement in one
// atomic mutation, so a failure never leaves half of the change published.
func (m *Manager) UpdateAPIKey(ctx context.Context, id string, paused *bool, access *KeyAccess) error {
	var allow []string
	if access != nil {
		var err error
		if allow, err = auth.NormalizeAllow(access.Allow); err != nil {
			return err
		}
	}
	return m.UpdateCatalog(ctx, func(candidate *Candidate) error {
		for i := range candidate.APIKeys {
			if candidate.APIKeys[i].ID != id {
				continue
			}
			if paused != nil {
				candidate.APIKeys[i].Paused = *paused
			}
			if access != nil {
				candidate.APIKeys[i].Restricted = access.Restricted
				candidate.APIKeys[i].Allow = allow
			}
			return nil
		}
		return ErrAPIKeyNotFound
	})
}

// KeyAccess is a key's model/Combo allowlist policy.
type KeyAccess struct {
	Restricted bool
	Allow      []string
}

// DeleteAPIKey revokes a key immediately on snapshot publication.
func (m *Manager) DeleteAPIKey(ctx context.Context, id string) error {
	return m.UpdateCatalog(ctx, func(candidate *Candidate) error {
		for i, entry := range candidate.APIKeys {
			if entry.ID == id {
				candidate.APIKeys = append(candidate.APIKeys[:i], candidate.APIKeys[i+1:]...)
				return nil
			}
		}
		return ErrAPIKeyNotFound
	})
}

// PutModel adds or replaces one manually managed model.
func (m *Manager) PutModel(ctx context.Context, model Model) error {
	model.Source = "custom"
	return m.UpdateCatalog(ctx, func(candidate *Candidate) error { candidate.AddModel(model); return nil })
}

// MergeDiscoveredModels unions account catalogs without erasing known or disabled IDs.
func (m *Manager) MergeDiscoveredModels(ctx context.Context, provider string, models []Model) error {
	return m.UpdateCatalog(ctx, func(candidate *Candidate) error {
		known := map[string]bool{}
		for _, model := range candidate.Models {
			if model.ProviderID == provider {
				known[model.ID] = true
			}
		}
		for _, model := range models {
			if known[model.ID] {
				for i := range candidate.Models {
					if existing := &candidate.Models[i]; existing.ProviderID == provider && existing.ID == model.ID && existing.Source == "discovered" {
						if model.Name != "" {
							existing.Name = model.Name
						}
						if model.ContextWindow > 0 {
							existing.ContextWindow = model.ContextWindow
						}
						if len(model.Capabilities) > 0 {
							existing.Capabilities = append([]string(nil), model.Capabilities...)
						}
					}
				}
				continue
			}
			model.ProviderID, model.Source = provider, "discovered"
			if model.Name == "" {
				model.Name = model.ID
			}
			candidate.AddModel(model)
			known[model.ID] = true
		}
		return nil
	})
}

// ErrModelNotFound reports a delete that names no model at all.
var ErrModelNotFound = errors.New("model not found")

// ErrModelNotCustom reports a delete of a model that exists but is discovered
// or built in; those are refreshed or disabled, never deleted.
var ErrModelNotCustom = errors.New("model is not a custom model")

// ModelInUseError reports a delete refused because Combos still route to the
// model. Deleting would silently change those routes (or leave them empty).
type ModelInUseError struct {
	Combos   []string
	Adapters []string // capacity adapter settings whose pool lists the model
}

func (e *ModelInUseError) Error() string {
	var parts []string
	if len(e.Combos) > 0 {
		parts = append(parts, "combos: "+strings.Join(e.Combos, ", "))
	}
	if len(e.Adapters) > 0 {
		parts = append(parts, "capacity adapters: "+strings.Join(e.Adapters, ", "))
	}
	return "model is used by " + strings.Join(parts, "; ")
}

// DeleteModel removes one custom (operator-managed) model plus the aliases,
// pricing override and disabled marker that referenced it. It refuses while a
// Combo or capacity adapter pool references the model, directly or through one
// of its aliases.
func (m *Manager) DeleteModel(ctx context.Context, provider, model string) error {
	_, err := m.update(ctx, func(candidate *Candidate) error {
		index := -1
		for i, existing := range candidate.Models {
			if existing.ProviderID == provider && existing.ID == model && existing.Source == "custom" {
				index = i
				break
			}
		}
		if index < 0 {
			if snapshot, err := m.Load(); err == nil {
				if _, ok := snapshot.models[modelKey(provider, model)]; ok {
					return ErrModelNotCustom
				}
			}
			return ErrModelNotFound
		}
		names := map[string]struct{}{model: {}}
		for alias, target := range candidate.Aliases {
			if target.ProviderID == provider && target.ModelID == model {
				names[alias] = struct{}{}
			}
		}
		var users []string
		for _, combo := range candidate.Combos {
			for _, member := range combo.Members {
				if _, ok := names[member.ModelID]; ok && (member.ProviderID == "" || member.ProviderID == provider) {
					users = append(users, combo.Name)
					break
				}
			}
		}
		var adapters []string
		for key, value := range candidate.Settings {
			if !strings.HasPrefix(key, "capacityAdapter") {
				continue
			}
			var adapter struct {
				Pool []struct {
					ProviderID string `json:"providerId"`
					ModelID    string `json:"modelId"`
				} `json:"pool"`
			}
			if json.Unmarshal([]byte(value), &adapter) != nil {
				continue
			}
			for _, entry := range adapter.Pool {
				if _, ok := names[entry.ModelID]; ok && (entry.ProviderID == "" || entry.ProviderID == provider) {
					adapters = append(adapters, key)
					break
				}
			}
		}
		if len(users) > 0 || len(adapters) > 0 {
			sort.Strings(users)
			sort.Strings(adapters)
			return &ModelInUseError{Combos: users, Adapters: adapters}
		}
		candidate.Models = append(candidate.Models[:index], candidate.Models[index+1:]...)
		for alias, target := range candidate.Aliases {
			if target.ProviderID == provider && target.ModelID == model {
				delete(candidate.Aliases, alias)
			}
		}
		candidate.SetModelDisabled(provider, model, false)
		return nil
	}, func(tx *sql.Tx) error {
		// Pricing overrides are stored outside the candidate; drop the dead row.
		_, err := tx.ExecContext(ctx, "DELETE FROM pricing_overrides WHERE provider_id=? AND model_id=?", provider, model)
		return err
	})
	return err
}

// ReplaceDiscoveredModels atomically swaps one provider's discovered catalog
// for the supplied IDs while preserving custom (operator-managed) models and
// any alias/disabled decisions. Discovery failure is advisory, so an empty list
// is a no-op rather than a destructive delete (PROVIDER_BASELINE §7).
func (m *Manager) ReplaceDiscoveredModels(ctx context.Context, providerID string, models []Model) error {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return errors.New("provider id is required")
	}
	// Filter first: an empty or all-blank result is advisory (discovery
	// unavailable or failed) and must never delete the last known durable
	// catalog for the provider.
	usable := make([]Model, 0, len(models))
	for _, model := range models {
		if strings.TrimSpace(model.ID) == "" {
			continue
		}
		model.ProviderID = providerID
		model.Source = "discovered"
		if model.Name == "" {
			model.Name = model.ID
		}
		usable = append(usable, model)
	}
	if len(usable) == 0 {
		return nil
	}
	return m.UpdateCatalog(ctx, func(candidate *Candidate) error {
		kept := candidate.Models[:0]
		for _, model := range candidate.Models {
			// Remove only this provider's previously discovered entries. Custom
			// (operator-managed) and seeded/static models have other sources and
			// must survive a discovery refresh so the seed stays available offline
			// (PROVIDER_BASELINE §7).
			if model.ProviderID == providerID && model.Source == "discovered" {
				continue
			}
			kept = append(kept, model)
		}
		candidate.Models = kept
		protected := make(map[string]bool)
		for _, model := range kept {
			if model.ProviderID == providerID {
				protected[model.ID] = true
			}
		}
		for _, model := range usable {
			if !protected[model.ID] {
				candidate.AddModel(model)
			}
		}
		return nil
	})
}

// PutAlias creates or replaces a model alias.
func (m *Manager) PutAlias(ctx context.Context, alias string, target ModelRef) error {
	return m.UpdateCatalog(ctx, func(candidate *Candidate) error { candidate.SetAlias(alias, target); return nil })
}

// DeleteAlias removes one alias through the same compile/commit/publish path.
// Removing an absent alias is a no-op so callers can treat delete as idempotent.
func (m *Manager) DeleteAlias(ctx context.Context, alias string) error {
	return m.UpdateCatalog(ctx, func(candidate *Candidate) error {
		delete(candidate.Aliases, alias)
		return nil
	})
}

// SetModelDisabled changes model visibility/eligibility in one atomic update.
func (m *Manager) SetModelDisabled(ctx context.Context, provider, model string, disabled bool) error {
	return m.UpdateCatalog(ctx, func(candidate *Candidate) error { candidate.SetModelDisabled(provider, model, disabled); return nil })
}

// UpdateCatalog serializes a catalog mutation through the same compile/commit/publish protocol as settings.
func (m *Manager) UpdateCatalog(ctx context.Context, mutate func(*Candidate) error) error {
	_, err := m.Update(ctx, mutate)
	return err
}

func displayPrefix(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:12]
}
