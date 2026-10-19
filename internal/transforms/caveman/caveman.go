// Package caveman owns Caveman request transformation (SPEC §17.3): inject a
// deterministic terseness policy at the configured level without producing
// duplicate blocks across translated protocols.
package caveman

import (
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/transforms"
)

// Marker identifies a Caveman policy block so injection stays idempotent across
// protocol translations (SPEC §17.3).
const Marker = "[routeweft:caveman]"

// Transform injects the Caveman policy into the system/instruction head.
type Transform struct {
	Enabled bool
	// Level selects the policy strength. Empty is treated as "full".
	Level string
}

func (Transform) Name() string { return "caveman" }

// Apply prepends a policy block once. A request that already carries the marker
// is left untouched so re-running or translating cannot duplicate the block.
func (t Transform) Apply(request transforms.Request) (transforms.Request, error) {
	if !t.Enabled {
		return request, nil
	}
	policy := Policy(t.Level)
	if policy == "" {
		return request, nil
	}
	if hasMarker(request.System) {
		return request, nil
	}
	block := Marker + " " + policy
	request.System = insertPolicy(request.System, block)
	return request, nil
}

// insertPolicy places a Routeweft policy block after any existing Routeweft
// policy blocks but before the caller's own instructions, so pipeline order is
// preserved (SPEC §17.3).
func insertPolicy(system []string, block string) []string {
	index := 0
	for index < len(system) && strings.HasPrefix(system[index], "[routeweft:") {
		index++
	}
	updated := make([]string, 0, len(system)+1)
	updated = append(updated, system[:index]...)
	updated = append(updated, block)
	return append(updated, system[index:]...)
}

// Policy returns the deterministic policy text for a level, or empty when the
// level is unknown. Level names are the configured Caveman levels.
func Policy(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "full":
		return "Respond tersely. No filler, no restating the question, no pleasantries."
	case "lite":
		return "Respond concisely. Prefer short sentences and omit pleasantries."
	case "ultra":
		return "Respond in minimal telegraphic fragments. Drop all non-essential words."
	default:
		return ""
	}
}

func hasMarker(system []string) bool {
	for _, entry := range system {
		if strings.Contains(entry, Marker) {
			return true
		}
	}
	return false
}
