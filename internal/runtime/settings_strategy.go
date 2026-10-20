package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// strategyOverride is one providerStrategies / comboStrategies entry. The pin
// stores an object per key (src/sse/services/auth.js:81,172,186 and
// src/sse/handlers/chat.js:110-112); Routeweft also keeps accepting the earlier
// bare-string shorthand so existing stored values keep their meaning.
type strategyOverride struct {
	// Strategy is empty when the entry inherits the global strategy.
	Strategy string
	// StickyLimit is 0 when the entry inherits the global sticky limit.
	StickyLimit uint64
}

// parseStrategyOverrides decodes and validates a providerStrategies (combo
// false) or comboStrategies (combo true) setting. Fields Routeweft does not
// apply are rejected by name instead of being stored and ignored: the pin's
// proxyPoolId/rotateStrategy (no-auth proxy rotation), judgeModel and
// fusionTuning are owned by other items (PARITY 5.2).
func parseStrategyOverrides(raw string, combo bool) (map[string]strategyOverride, error) {
	out, errs, err := parseStrategyOverridesEach(raw, combo)
	if err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(errs) {
		return nil, errs[name]
	}
	return out, nil
}

func sortedKeys(errs map[string]error) []string {
	keys := make([]string, 0, len(errs))
	for key := range errs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// parseStrategyOverridesEach validates entry by entry: out holds the valid
// entries, errs the rejected ones, and err is set only when raw is not an object.
func parseStrategyOverridesEach(raw string, combo bool) (out map[string]strategyOverride, errs map[string]error, err error) {
	var entries map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &entries) != nil || entries == nil {
		return nil, nil, fmt.Errorf("must be a JSON object")
	}
	out = make(map[string]strategyOverride, len(entries))
	errs = make(map[string]error)
	for name, value := range entries {
		override, entryErr := parseStrategyOverride(name, value, combo)
		if entryErr != nil {
			errs[name] = entryErr
			continue
		}
		out[name] = override
	}
	return out, errs, nil
}

func parseStrategyOverride(name string, value json.RawMessage, combo bool) (strategyOverride, error) {
	var override strategyOverride
	value = bytes.TrimSpace(value)
	if len(value) > 0 && value[0] == '"' {
		if err := json.Unmarshal(value, &override.Strategy); err != nil {
			return strategyOverride{}, fmt.Errorf("%q: invalid strategy", name)
		}
	} else {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(value, &fields); err != nil || fields == nil {
			return strategyOverride{}, fmt.Errorf("%q: must be a strategy string or object", name)
		}
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			switch {
			case key == "fallbackStrategy":
				if err := json.Unmarshal(fields[key], &override.Strategy); err != nil {
					return strategyOverride{}, fmt.Errorf("%q: fallbackStrategy must be a string", name)
				}
			case key == "stickyRoundRobinLimit" && !combo:
				var limit int64
				if err := json.Unmarshal(fields[key], &limit); err != nil || limit < 0 {
					return strategyOverride{}, fmt.Errorf("%q: stickyRoundRobinLimit must be a non-negative integer", name)
				}
				override.StickyLimit = uint64(limit)
			default:
				return strategyOverride{}, fmt.Errorf("%q: field %q is not supported", name, key)
			}
		}
	}
	if combo {
		if _, err := parseComboStrategy(override.Strategy); err != nil {
			return strategyOverride{}, fmt.Errorf("%q: unsupported strategy %q", name, override.Strategy)
		}
	} else if _, err := routing.ParseStrategy(override.Strategy); err != nil {
		return strategyOverride{}, fmt.Errorf("%q: unsupported strategy %q", name, override.Strategy)
	}
	return override, nil
}

// strategyOverridesTolerant is the snapshot-side reader: configuration already
// passed validation, but one malformed historical entry is skipped on its own
// instead of discarding every other override.
func strategyOverridesTolerant(raw string, combo bool) map[string]strategyOverride {
	out, _, err := parseStrategyOverridesEach(raw, combo)
	if err != nil {
		return map[string]strategyOverride{}
	}
	return out
}

// pinRoundRobinLimit maps a pin round-robin selection onto Routeweft's split
// vocabulary. In the pin "round-robin" always rotates after `limit` consecutive
// uses (auth.js:185-187 for accounts, combo.js:208-237 for combos), so it is
// Routeweft's sticky-round-robin; a limit of 1 is plain round-robin.
func pinRoundRobinStrategy(limit uint64) string {
	if limit <= 1 {
		return "round-robin"
	}
	return "sticky-round-robin"
}

func formatUint(v uint64) string { return strconv.FormatUint(v, 10) }

func trimmedLower(v string) string { return strings.ToLower(strings.TrimSpace(v)) }
