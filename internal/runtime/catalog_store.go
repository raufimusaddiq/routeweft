package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/auth"
)

// persistCatalog replaces the compiled model/credential surface inside the
// caller's configuration transaction so commit order matches SPEC section 7.
func persistCatalog(ctx context.Context, tx *sql.Tx, config Config) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM api_keys"); err != nil {
		return fmt.Errorf("replace api keys: %w", err)
	}
	for _, key := range config.APIKeys {
		const insert = "INSERT INTO api_keys (id,name,key_hash,key_prefix,enabled,paused) VALUES (?,?,?,?,?,?)"
		enabled := 0
		if !key.Disabled {
			enabled = 1
		}
		paused := 0
		if key.Paused {
			paused = 1
		}
		if _, err := tx.ExecContext(ctx, insert, key.ID, key.Name, key.Hash, key.Prefix, enabled, paused); err != nil {
			return fmt.Errorf("persist api key %q: %w", key.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM custom_models"); err != nil {
		return fmt.Errorf("replace custom models: %w", err)
	}
	for _, model := range config.Models {
		if model.Source == "custom" {
			const insert = "INSERT INTO custom_models (id,provider_id,model_id,display_name,context_window) VALUES (?,?,?,?,?)"
			var contextWindow any
			if model.ContextWindow > 0 {
				contextWindow = model.ContextWindow
			}
			if _, err := tx.ExecContext(ctx, insert, modelKey(model.ProviderID, model.ID), model.ProviderID, model.ID, model.Name, contextWindow); err != nil {
				return fmt.Errorf("persist model %q: %w", modelKey(model.ProviderID, model.ID), err)
			}
		}
		capabilities, err := json.Marshal(model.Capabilities)
		if err != nil {
			return err
		}
		const upsertModel = `INSERT INTO provider_models(provider_id,model_id,display_name,context_window,capabilities) VALUES(?,?,?,?,?)
ON CONFLICT(provider_id,model_id) DO UPDATE SET display_name=excluded.display_name,context_window=excluded.context_window,capabilities=excluded.capabilities,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`
		var contextWindow any
		if model.ContextWindow > 0 {
			contextWindow = model.ContextWindow
		}
		if _, err := tx.ExecContext(ctx, upsertModel, model.ProviderID, model.ID, model.Name, contextWindow, string(capabilities)); err != nil {
			return fmt.Errorf("persist provider model %q: %w", modelKey(model.ProviderID, model.ID), err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM model_aliases"); err != nil {
		return fmt.Errorf("replace aliases: %w", err)
	}
	for alias, target := range config.Aliases {
		const insert = "INSERT INTO model_aliases (alias,provider_id,model_id) VALUES (?,?,?)"
		if _, err := tx.ExecContext(ctx, insert, alias, target.ProviderID, target.ModelID); err != nil {
			return fmt.Errorf("persist alias %q: %w", alias, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM disabled_models"); err != nil {
		return fmt.Errorf("replace disabled models: %w", err)
	}
	for key := range config.DisabledModels {
		provider, model, ok := strings.Cut(key, ":")
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO disabled_models (provider_id,model_id) VALUES (?,?)", provider, model); err != nil {
			return fmt.Errorf("persist disabled model %q: %w", key, err)
		}
	}
	return nil
}

func loadCatalog(ctx context.Context, db *sql.DB, config *Config) error {
	keyRows, err := db.QueryContext(ctx, "SELECT id,name,key_hash,key_prefix,enabled,paused FROM api_keys ORDER BY id")
	if err != nil {
		return fmt.Errorf("load api keys: %w", err)
	}
	defer keyRows.Close()
	for keyRows.Next() {
		var entry auth.Entry
		var enabled, paused int
		if err := keyRows.Scan(&entry.ID, &entry.Name, &entry.Hash, &entry.Prefix, &enabled, &paused); err != nil {
			return err
		}
		entry.Disabled = enabled == 0
		entry.Paused = paused == 1
		config.APIKeys = append(config.APIKeys, entry)
	}
	if err := keyRows.Err(); err != nil {
		return err
	}
	modelRows, err := db.QueryContext(ctx, "SELECT provider_id,model_id,COALESCE(display_name,''),COALESCE(context_window,0),capabilities FROM provider_models ORDER BY provider_id,model_id")
	if err != nil {
		return fmt.Errorf("load provider models: %w", err)
	}
	defer modelRows.Close()
	modelIndexes := make(map[string]int)
	for modelRows.Next() {
		var model Model
		var capabilities string
		if err := modelRows.Scan(&model.ProviderID, &model.ID, &model.Name, &model.ContextWindow, &capabilities); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(capabilities), &model.Capabilities); err != nil {
			return fmt.Errorf("decode model capabilities for %s: %w", modelKey(model.ProviderID, model.ID), err)
		}
		model.Source = "discovered"
		modelIndexes[modelKey(model.ProviderID, model.ID)] = len(config.Models)
		config.Models = append(config.Models, model)
	}
	if err := modelRows.Err(); err != nil {
		return err
	}
	customRows, err := db.QueryContext(ctx, "SELECT provider_id,model_id,COALESCE(display_name,''),COALESCE(context_window,0) FROM custom_models ORDER BY provider_id,model_id")
	if err != nil {
		return fmt.Errorf("load custom models: %w", err)
	}
	defer customRows.Close()
	for customRows.Next() {
		var model Model
		if err := customRows.Scan(&model.ProviderID, &model.ID, &model.Name, &model.ContextWindow); err != nil {
			return err
		}
		model.Source = "custom"
		key := modelKey(model.ProviderID, model.ID)
		if index, ok := modelIndexes[key]; ok {
			if model.Name == "" {
				model.Name = config.Models[index].Name
			}
			if model.ContextWindow == 0 {
				model.ContextWindow = config.Models[index].ContextWindow
			}
			model.Capabilities = append([]string(nil), config.Models[index].Capabilities...)
			config.Models[index] = model
		} else {
			modelIndexes[key] = len(config.Models)
			config.Models = append(config.Models, model)
		}
	}
	if err := customRows.Err(); err != nil {
		return err
	}
	aliasRows, err := db.QueryContext(ctx, "SELECT alias,provider_id,model_id FROM model_aliases ORDER BY alias")
	if err != nil {
		return fmt.Errorf("load model aliases: %w", err)
	}
	defer aliasRows.Close()
	for aliasRows.Next() {
		var alias string
		var target ModelRef
		if err := aliasRows.Scan(&alias, &target.ProviderID, &target.ModelID); err != nil {
			return err
		}
		config.Aliases[alias] = target
	}
	if err := aliasRows.Err(); err != nil {
		return err
	}
	disabledRows, err := db.QueryContext(ctx, "SELECT provider_id,model_id FROM disabled_models")
	if err != nil {
		return fmt.Errorf("load disabled models: %w", err)
	}
	defer disabledRows.Close()
	for disabledRows.Next() {
		var provider, model string
		if err := disabledRows.Scan(&provider, &model); err != nil {
			return err
		}
		config.DisabledModels[modelKey(provider, model)] = struct{}{}
	}
	return disabledRows.Err()
}
