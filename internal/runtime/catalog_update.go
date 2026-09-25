package runtime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/raufimusaddiq/routeweft/internal/auth"
)

// CreateAPIKey persists and publishes a newly created client key, returning its
// one-time plaintext value.
func (m *Manager) CreateAPIKey(ctx context.Context, name string) (auth.Entry, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return auth.Entry{}, "", errors.New("API key name is required")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return auth.Entry{}, "", fmt.Errorf("generate API key: %w", err)
	}
	plaintext := "rw_" + base64.RawURLEncoding.EncodeToString(secret[:])
	entry := auth.Entry{ID: uuid.NewString(), Name: name, Prefix: displayPrefix(plaintext), Hash: auth.Hash(plaintext)}
	_, err := m.Update(ctx, func(candidate *Candidate) error {
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

// SetAPIKeyPaused pauses or resumes a key and publishes the new key index.
func (m *Manager) SetAPIKeyPaused(ctx context.Context, id string, paused bool) error {
	return m.UpdateCatalog(ctx, func(candidate *Candidate) error {
		for i := range candidate.APIKeys {
			if candidate.APIKeys[i].ID == id {
				candidate.APIKeys[i].Paused = paused
				return nil
			}
		}
		return errors.New("API key not found")
	})
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
		return errors.New("API key not found")
	})
}

// PutModel adds or replaces one manually managed model.
func (m *Manager) PutModel(ctx context.Context, model Model) error {
	model.Source = "custom"
	return m.UpdateCatalog(ctx, func(candidate *Candidate) error { candidate.AddModel(model); return nil })
}

// PutAlias creates or replaces a model alias.
func (m *Manager) PutAlias(ctx context.Context, alias string, target ModelRef) error {
	return m.UpdateCatalog(ctx, func(candidate *Candidate) error { candidate.SetAlias(alias, target); return nil })
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
