package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ImportPinSettings maps the C04 settings of a pinned v1 settings document
// (oracle 1c3b9f47, src/lib/db/repos/settingsRepo.js) onto Routeweft's stored
// settings. It never touches a database: the caller (the #78/C01 importer)
// owns reading the oracle export and committing through SetSettings, so every
// value returned here still passes validateSettingValue there.
//
// Everything that cannot be carried exactly is listed in notes rather than
// dropped silently. Keys outside C04 are ignored.
func ImportPinSettings(pin map[string]json.RawMessage) (settings map[string]string, notes []string, err error) {
	settings = make(map[string]string)
	note := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }

	// A 0 limit falls through to the default in the pin (`|| 3`, auth.js:186;
	// normalizeStickyLimit, combo.js:186-189), so it is normalized the same way.
	providerSticky := pinUint(pin["stickyRoundRobinLimit"], 3)
	if providerSticky == 0 {
		providerSticky = 3
	}
	comboSticky := max(pinUint(pin["comboStickyRoundRobinLimit"], 1), 1)
	if _, ok := pin["stickyRoundRobinLimit"]; ok {
		settings["stickyRoundRobinLimit"] = formatUint(providerSticky)
	}
	if _, ok := pin["comboStickyRoundRobinLimit"]; ok {
		settings["comboStickyRoundRobinLimit"] = formatUint(comboSticky)
	}

	// 1. capacityAdapter {cap: {enabled, roundRobin, models}} -> four settings.
	if raw, ok := pin["capacityAdapter"]; ok {
		var caps map[string]json.RawMessage
		if json.Unmarshal(raw, &caps) != nil {
			return nil, nil, fmt.Errorf("capacityAdapter must be an object")
		}
		pinCaps := map[string]string{"vision": "vision", "pdf": "pdf", "audioInput": "audio-input", "videoInput": "video-input"}
		for pinName, entry := range caps {
			name, known := pinCaps[pinName]
			if !known {
				note("capacityAdapter.%s: unknown capability, not carried", pinName)
				continue
			}
			setting, _ := CapacityAdapterSetting(name)
			encoded, entryNotes := importCapacityAdapter(pinName, entry)
			notes = append(notes, entryNotes...)
			settings[setting] = encoded
		}
	}

	// 2. outboundNoProxy "a.com, .b.com" -> noProxy ["a.com",".b.com"].
	if raw, ok := pin["outboundNoProxy"]; ok {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return nil, nil, fmt.Errorf("outboundNoProxy must be a string")
		}
		list := canonicalNoProxy(strings.Split(text, ","))
		encoded, _ := json.Marshal(list)
		settings["noProxy"] = string(encoded)
	}

	// 3. observabilityMaxJsonSize is KiB in the pin (requestDetailsRepo.js:27,44
	// multiply by 1024); Routeweft stores bytes. A zero/absent value means the
	// pin used its default, so Routeweft's own default (5 MiB, PRD 15) is kept.
	if raw, ok := pin["observabilityMaxJsonSize"]; ok {
		kib := pinUint(raw, 0)
		if kib == 0 {
			note("observabilityMaxJsonSize: unset or 0 in the pin, keeping the Routeweft default")
		} else {
			settings["observabilityMaxJsonSize"] = formatUint(kib * 1024)
			note("observabilityMaxJsonSize: %d KiB converted to %d bytes (the pin truncates each field to a preview; Routeweft omits an over-cap document)", kib, kib*1024)
		}
	}

	// 4. dnsToolEnabled: provider -> bool map. The pin never reads it
	// (settingsRepo.js:40 is its only occurrence), so only the shape is kept.
	if raw, ok := pin["dnsToolEnabled"]; ok {
		var entries map[string]bool
		if json.Unmarshal(raw, &entries) != nil || entries == nil {
			note("dnsToolEnabled: not a provider->boolean object, not carried")
		} else {
			encoded, _ := json.Marshal(entries)
			settings["dnsToolEnabled"] = string(encoded)
		}
	}

	// 5. strategies. The pin's "round-robin" always honours a sticky limit.
	if raw, ok := pin["fallbackStrategy"]; ok {
		var strategy string
		if json.Unmarshal(raw, &strategy) == nil {
			switch strategy {
			case "fill-first":
				settings["providerStrategy"] = "fill-first"
			case "round-robin":
				settings["providerStrategy"] = pinRoundRobinStrategy(providerSticky)
			default:
				note("fallbackStrategy %q is not a Routeweft provider strategy, not carried", strategy)
			}
		}
	}
	if raw, ok := pin["comboStrategy"]; ok {
		var strategy string
		if json.Unmarshal(raw, &strategy) == nil {
			switch strategy {
			case "fallback":
				settings["comboStrategy"] = "fallback"
			case "round-robin":
				settings["comboStrategy"] = pinRoundRobinStrategy(comboSticky)
			default:
				note("comboStrategy %q is a per-combo fusion flag in Routeweft, not carried", strategy)
			}
		}
	}
	if raw, ok := pin["providerStrategies"]; ok {
		encoded, n, err := importStrategyOverrides("providerStrategies", raw, false, providerSticky)
		if err != nil {
			return nil, nil, err
		}
		notes = append(notes, n...)
		settings["providerStrategies"] = encoded
	}
	if raw, ok := pin["comboStrategies"]; ok {
		encoded, n, err := importStrategyOverrides("comboStrategies", raw, true, comboSticky)
		if err != nil {
			return nil, nil, err
		}
		notes = append(notes, n...)
		settings["comboStrategies"] = encoded
	}

	// 6. Flags with no Routeweft consumer are reported, not stored.
	for _, key := range []string{"ccFilterNaming", "headroomCodeAware", "headroomKompress"} {
		if _, ok := pin[key]; ok {
			note("%s: not carried, Routeweft has no consumer for it (see docs/PARITY.md)", key)
		}
	}
	sort.Strings(notes)
	return settings, notes, nil
}

func importCapacityAdapter(pinName string, raw json.RawMessage) (string, []string) {
	var notes []string
	enabled, roundRobin := false, false
	var models []string
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' {
		// Legacy array form: treated as enabled, fallback (capacityAdapter.js:19-21).
		var items []json.RawMessage
		_ = json.Unmarshal(raw, &items)
		enabled = true
		for _, item := range items {
			var text string
			var object struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(item, &text) == nil && text != "" {
				models = append(models, text)
			} else if json.Unmarshal(item, &object) == nil && object.Model != "" {
				models = append(models, object.Model)
			}
		}
	} else if len(raw) > 0 && raw[0] == '{' {
		var object struct {
			Enabled    *bool    `json:"enabled"`
			RoundRobin bool     `json:"roundRobin"`
			Models     []string `json:"models"`
		}
		_ = json.Unmarshal(raw, &object)
		// enabled !== false: a missing flag means enabled (capacityAdapter.js:24).
		enabled = object.Enabled == nil || *object.Enabled
		roundRobin = object.RoundRobin
		for _, model := range object.Models {
			if model != "" {
				models = append(models, model)
			}
		}
	}
	pool := make([]map[string]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		provider, id, ok := strings.Cut(model, "/")
		if !ok || provider == "" || id == "" {
			notes = append(notes, fmt.Sprintf("capacityAdapter.%s: %q has no provider prefix, not carried", pinName, model))
			continue
		}
		if _, dup := seen[model]; dup {
			continue
		}
		seen[model] = struct{}{}
		pool = append(pool, map[string]string{"providerId": provider, "modelId": id})
	}
	if len(pool) > 0 {
		notes = append(notes, fmt.Sprintf("capacityAdapter.%s: provider prefixes kept verbatim; they must be Routeweft provider IDs, not pin aliases", pinName))
	}
	encoded, _ := json.Marshal(map[string]any{"enabled": enabled, "roundRobin": roundRobin, "pool": pool})
	return string(encoded), notes
}

func importStrategyOverrides(key string, raw json.RawMessage, combo bool, globalSticky uint64) (string, []string, error) {
	var entries map[string]json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil {
		return "", nil, fmt.Errorf("%s must be an object", key)
	}
	var notes []string
	out := make(map[string]map[string]any, len(entries))
	for name, value := range entries {
		var fields map[string]json.RawMessage
		if json.Unmarshal(value, &fields) != nil {
			notes = append(notes, fmt.Sprintf("%s.%s: not an object, not carried", key, name))
			continue
		}
		entry := map[string]any{}
		limit := globalSticky
		if !combo {
			if own := pinUint(fields["stickyRoundRobinLimit"], 0); own > 0 {
				limit = own
				entry["stickyRoundRobinLimit"] = own
			}
		}
		var strategy string
		_ = json.Unmarshal(fields["fallbackStrategy"], &strategy)
		switch {
		case strategy == "":
		case strategy == "fill-first" && !combo, strategy == "fallback" && combo:
			entry["fallbackStrategy"] = strategy
		case strategy == "round-robin":
			entry["fallbackStrategy"] = pinRoundRobinStrategy(limit)
		default:
			notes = append(notes, fmt.Sprintf("%s.%s: fallbackStrategy %q has no Routeweft equivalent here (Combo fusion is a Combo flag), not carried", key, name, strategy))
		}
		for field := range fields {
			switch field {
			case "fallbackStrategy":
			case "stickyRoundRobinLimit":
				// Provider entries consume it above; the pin's combo entries have
				// no such field and Routeweft's per-combo limit is on the Combo.
				if combo {
					notes = append(notes, fmt.Sprintf("%s.%s: field %q is not supported by Routeweft, not carried", key, name, field))
				}
			default:
				notes = append(notes, fmt.Sprintf("%s.%s: field %q is not supported by Routeweft, not carried", key, name, field))
			}
		}
		if len(entry) > 0 {
			out[name] = entry
		}
	}
	encoded, _ := json.Marshal(out)
	return string(encoded), notes, nil
}

// pinUint reads a JS number or numeric string; absent/invalid yields def.
func pinUint(raw json.RawMessage, def uint64) uint64 {
	if len(raw) == 0 {
		return def
	}
	var number float64
	if json.Unmarshal(raw, &number) == nil {
		if number < 0 || number != float64(uint64(number)) {
			return def
		}
		return uint64(number)
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if parsed, err := strconv.ParseUint(strings.TrimSpace(text), 10, 64); err == nil {
			return parsed
		}
	}
	return def
}
