package runtime

import "github.com/raufimusaddiq/routeweft/internal/auth"

// keyGrants is one restricted key's allowlist compiled against the catalog.
// Matching is on resolved identities, so every spelling that routes to the same
// target is one grant and no spelling can route somewhere the list does not
// name (SPEC §12.1).
type keyGrants struct {
	combos  map[string]struct{} // Combo names: a Combo is granted as a unit
	targets map[string]struct{} // resolved "provider/model" of listed models and aliases
	names   map[string]struct{} // listed names that resolve to nothing; matched literally
}

func (s *RuntimeSnapshot) compileGrants(allow []string) keyGrants {
	grants := keyGrants{combos: map[string]struct{}{}, targets: map[string]struct{}{}, names: map[string]struct{}{}}
	for _, name := range allow {
		if name == "" {
			continue
		}
		if _, isCombo := s.combos[name]; isCombo {
			grants.combos[name] = struct{}{}
		} else if model, ok := s.ServingModel(name); ok {
			grants.targets[model.ProviderID+"/"+model.ID] = struct{}{}
		} else {
			grants.names[name] = struct{}{}
		}
	}
	return grants
}

// compileKeyAccess resolves every restricted key's allowlist once per snapshot
// so a request pays map lookups, not a scan of the list.
func (s *RuntimeSnapshot) compileKeyAccess(keys []auth.Entry) {
	for _, key := range keys {
		if !key.Restricted || key.Hash == "" {
			continue
		}
		if s.keyAccess == nil {
			s.keyAccess = make(map[string]keyGrants)
		}
		s.keyAccess[key.Hash] = s.compileGrants(key.Allow)
	}
}

// KeyAllows reports whether key may request the model string exactly as the
// router will route it. A requested Combo needs its own grant (a Combo overrides
// a same-named model, and granting it does not grant its members for direct
// use); anything else is compared by the model it resolves to. An empty request
// is never allowed to a restricted key.
func (s *RuntimeSnapshot) KeyAllows(key auth.Entry, requested string) bool {
	if !key.Restricted {
		return true
	}
	if requested == "" {
		return false
	}
	grants, compiled := s.keyAccess[key.Hash]
	if !compiled || key.Hash == "" {
		grants = s.compileGrants(key.Allow)
	}
	if _, isCombo := s.combos[requested]; isCombo {
		_, ok := grants.combos[requested]
		return ok
	}
	if model, ok := s.ServingModel(requested); ok {
		_, granted := grants.targets[model.ProviderID+"/"+model.ID]
		return granted
	}
	_, ok := grants.names[requested]
	return ok
}
