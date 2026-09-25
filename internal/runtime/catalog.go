package runtime

import "sort"

// Model is a Routeweft-owned model catalog entry.
type Model struct {
	ProviderID    string   `json:"provider"`
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	ContextWindow int      `json:"contextWindow,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
	Source        string   `json:"source,omitempty"`
}

type ModelRef struct {
	ProviderID string
	ModelID    string
}

func modelKey(provider, model string) string { return provider + ":" + model }

func cloneModels(models map[string]Model) map[string]Model {
	cloned := make(map[string]Model, len(models))
	for key, model := range models {
		model.Capabilities = append([]string(nil), model.Capabilities...)
		cloned[key] = model
	}
	return cloned
}

func cloneAliases(aliases map[string]ModelRef) map[string]ModelRef {
	cloned := make(map[string]ModelRef, len(aliases))
	for key, value := range aliases {
		cloned[key] = value
	}
	return cloned
}

func cloneDisabled(disabled map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(disabled))
	for key := range disabled {
		cloned[key] = struct{}{}
	}
	return cloned
}

func cloneModelsForConfig(models map[string]Model) []Model {
	result := make([]Model, 0, len(models))
	for _, model := range models {
		model.Capabilities = append([]string(nil), model.Capabilities...)
		result = append(result, model)
	}
	sort.Slice(result, func(i, j int) bool {
		return modelKey(result[i].ProviderID, result[i].ID) < modelKey(result[j].ProviderID, result[j].ID)
	})
	return result
}

func compileCatalog(models []Model, aliases map[string]ModelRef, disabled map[string]struct{}) (map[string]Model, map[string]ModelRef, map[string]struct{}) {
	compiledModels := make(map[string]Model, len(models))
	for _, model := range models {
		model.Capabilities = append([]string(nil), model.Capabilities...)
		compiledModels[modelKey(model.ProviderID, model.ID)] = model
	}
	compiledAliases := cloneAliases(aliases)
	compiledDisabled := cloneDisabled(disabled)
	return compiledModels, compiledAliases, compiledDisabled
}

func listCatalog(models map[string]Model, aliases map[string]ModelRef, disabled map[string]struct{}) []Model {
	listed := make(map[string]Model, len(models)+len(aliases))
	for key, model := range models {
		if _, off := disabled[key]; off {
			continue
		}
		listed[key] = model
	}
	for alias, target := range aliases {
		key := modelKey(target.ProviderID, target.ModelID)
		if _, off := disabled[key]; off {
			continue
		}
		if model, ok := models[key]; ok {
			model.ID = alias
			model.Source = "alias"
			listed["alias:"+alias] = model
		}
	}
	result := make([]Model, 0, len(listed))
	for _, model := range listed {
		model.Capabilities = append([]string(nil), model.Capabilities...)
		result = append(result, model)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ProviderID == result[j].ProviderID {
			return result[i].ID < result[j].ID
		}
		return result[i].ProviderID < result[j].ProviderID
	})
	return result
}
