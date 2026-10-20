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
	// Every decode below either yields a value, adds a note or fails the import;
	// none is discarded (TestC04ImportNeverDropsWrongTypedFieldSilently).
	limit := func(where string, raw json.RawMessage, def uint64) (uint64, bool) {
		value, ok := pinUint(raw)
		if !ok {
			note("%s: %s is not a non-negative integer, not carried", where, strings.TrimSpace(string(raw)))
			return def, false
		}
		return value, true
	}
	providerSticky, comboSticky := uint64(3), uint64(1)
	if raw, ok := pin["stickyRoundRobinLimit"]; ok {
		if value, valid := limit("stickyRoundRobinLimit", raw, 3); valid {
			providerSticky = value
			if providerSticky == 0 {
				providerSticky = 3
			}
			settings["stickyRoundRobinLimit"] = formatUint(providerSticky)
		}
	}
	if raw, ok := pin["comboStickyRoundRobinLimit"]; ok {
		if value, valid := limit("comboStickyRoundRobinLimit", raw, 1); valid {
			comboSticky = max(value, 1)
			settings["comboStickyRoundRobinLimit"] = formatUint(comboSticky)
		}
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
			setting, _ := CapacityAdapterSetting(name) // name comes from pinCaps, always a known capability
			encoded, carried, entryNotes := importCapacityAdapter(pinName, entry)
			notes = append(notes, entryNotes...)
			if carried {
				settings[setting] = encoded
			}
		}
	}

	// 2. outboundNoProxy "a.com, .b.com" -> noProxy ["a.com",".b.com"].
	if raw, ok := pin["outboundNoProxy"]; ok {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return nil, nil, fmt.Errorf("outboundNoProxy must be a string")
		}
		var list []string
		for _, entry := range canonicalNoProxy(strings.Split(text, ",")) {
			// The pin has no wildcard-prefix form (proxyFetch.js:19-23), so a
			// "*.host" entry never matched there either; carrying it would store
			// a pattern that can never match.
			if strings.HasPrefix(entry, "*") && entry != "*" {
				note("outboundNoProxy: %q never matches (use %q), not carried", entry, strings.TrimPrefix(entry, "*"))
				continue
			}
			list = append(list, entry)
		}
		if list == nil {
			list = []string{}
		}
		encoded, _ := json.Marshal(list) // []string cannot fail to marshal
		settings["noProxy"] = string(encoded)
	}

	// 3. observabilityMaxJsonSize is KiB in the pin (requestDetailsRepo.js:27,44
	// multiply by 1024); Routeweft stores bytes. A zero/absent value means the
	// pin used its default, so Routeweft's own default (5 MiB, PRD 15) is kept.
	if raw, ok := pin["observabilityMaxJsonSize"]; ok {
		kib, valid := limit("observabilityMaxJsonSize", raw, 0)
		if !valid {
			// noted above; the Routeweft default stays
		} else if kib == 0 {
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
			encoded, _ := json.Marshal(entries) // map[string]bool cannot fail to marshal
			settings["dnsToolEnabled"] = string(encoded)
		}
	}

	// 5. strategies. The pin's "round-robin" always honours a sticky limit.
	if raw, ok := pin["fallbackStrategy"]; ok {
		var strategy string
		if json.Unmarshal(raw, &strategy) != nil {
			note("fallbackStrategy: %s is not a string, not carried", strings.TrimSpace(string(raw)))
		} else {
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
		if json.Unmarshal(raw, &strategy) != nil {
			note("comboStrategy: %s is not a string, not carried", strings.TrimSpace(string(raw)))
		} else {
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

// importCapacityAdapter converts one pin capability entry. carried is false when
// the entry cannot be read as the pin reads it; the capability then keeps its
// Routeweft default and the reason is in notes, rather than storing a disabled
// or emptied adapter.
func importCapacityAdapter(pinName string, raw json.RawMessage) (encoded string, carried bool, notes []string) {
	bad := func(format string, args ...any) (string, bool, []string) {
		return "", false, append(notes, fmt.Sprintf("capacityAdapter.%s: "+format+", capability not carried", append([]any{pinName}, args...)...))
	}
	enabled, roundRobin := false, false
	var models []string
	addModel := func(item json.RawMessage) {
		var text string
		var object struct {
			Model string `json:"model"`
		}
		switch {
		case json.Unmarshal(item, &text) == nil && text != "":
			models = append(models, text)
		case json.Unmarshal(item, &object) == nil && object.Model != "":
			models = append(models, object.Model)
		default:
			notes = append(notes, fmt.Sprintf("capacityAdapter.%s: model entry %s is not a model string, not carried", pinName, strings.TrimSpace(string(item))))
		}
	}
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) > 0 && raw[0] == '[':
		// Legacy array form: treated as enabled, fallback (capacityAdapter.js:19-21).
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return bad("legacy array is malformed (%v)", err)
		}
		enabled = true
		for _, item := range items {
			addModel(item)
		}
	case len(raw) > 0 && raw[0] == '{':
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return bad("entry is malformed (%v)", err)
		}
		// enabled !== false: a missing flag means enabled (capacityAdapter.js:24).
		enabled = true
		if value, ok := fields["enabled"]; ok {
			if err := json.Unmarshal(value, &enabled); err != nil {
				return bad("enabled %s is not a boolean", strings.TrimSpace(string(value)))
			}
		}
		if value, ok := fields["roundRobin"]; ok {
			if err := json.Unmarshal(value, &roundRobin); err != nil {
				return bad("roundRobin %s is not a boolean", strings.TrimSpace(string(value)))
			}
		}
		if value, ok := fields["models"]; ok {
			var items []json.RawMessage
			if err := json.Unmarshal(value, &items); err != nil {
				return bad("models %s is not an array", strings.TrimSpace(string(value)))
			}
			for _, item := range items {
				addModel(item)
			}
		}
	default:
		return bad("entry %s is neither an object nor an array", string(raw))
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
		notes = append(notes, fmt.Sprintf("capacityAdapter.%s: provider prefixes kept verbatim and NOT resolved against the catalog (the admin PUT rejects unresolved members); correct any pin alias or unknown model after import", pinName))
	}
	out, _ := json.Marshal(map[string]any{"enabled": enabled, "roundRobin": roundRobin, "pool": pool})
	return string(out), true, notes
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
		if json.Unmarshal(value, &fields) != nil || fields == nil {
			notes = append(notes, fmt.Sprintf("%s.%s: not an object, not carried", key, name))
			continue
		}
		entry := map[string]any{}
		limit := globalSticky
		if !combo {
			if raw, ok := fields["stickyRoundRobinLimit"]; ok {
				if own, valid := pinUint(raw); !valid {
					notes = append(notes, fmt.Sprintf("%s.%s: stickyRoundRobinLimit %s is not a non-negative integer, not carried", key, name, strings.TrimSpace(string(raw))))
				} else if own > 0 {
					limit = own
					entry["stickyRoundRobinLimit"] = own
				}
			}
		}
		var strategy string
		_, hasStrategy := fields["fallbackStrategy"]
		strategyErr := json.Unmarshal(fields["fallbackStrategy"], &strategy)
		switch {
		case hasStrategy && (strategyErr != nil || string(bytes.TrimSpace(fields["fallbackStrategy"])) == "null"):
			notes = append(notes, fmt.Sprintf("%s.%s: fallbackStrategy %s is not a string, not carried", key, name, bytes.TrimSpace(fields["fallbackStrategy"])))
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
	encoded, _ := json.Marshal(out) // built from strings and integers above
	return string(encoded), notes, nil
}

// pinUint reads a JS number or numeric string. ok is false when the value is
// present but is neither (null, a fraction, a negative, text, an object).
func pinUint(raw json.RawMessage) (value uint64, ok bool) {
	if string(bytes.TrimSpace(raw)) == "null" {
		return 0, false // Unmarshal would accept null as 0
	}
	var number float64
	if json.Unmarshal(raw, &number) == nil {
		if number < 0 || number != float64(uint64(number)) {
			return 0, false
		}
		return uint64(number), true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if parsed, err := strconv.ParseUint(strings.TrimSpace(text), 10, 64); err == nil {
			return parsed, true
		}
	}
	return 0, false
}
