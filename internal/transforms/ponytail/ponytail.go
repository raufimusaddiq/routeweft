// Package ponytail owns Ponytail request transformation (SPEC §17.3): inject a
// deterministic answer-shape policy at the configured level, idempotently.
package ponytail

import (
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/transforms"
)

// Marker identifies a Ponytail policy block so injection stays idempotent.
const Marker = "[routeweft:ponytail]"

// Transform injects the Ponytail policy into the system/instruction head.
type Transform struct {
	Enabled bool
	// Level selects the policy strength. Empty is treated as "full".
	Level string
}

func (Transform) Name() string { return "ponytail" }

// Apply prepends the policy block once and never duplicates an existing one.
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
	index := 0
	for index < len(request.System) && strings.HasPrefix(request.System[index], "[routeweft:") {
		index++
	}
	updated := make([]string, 0, len(request.System)+1)
	updated = append(updated, request.System[:index]...)
	updated = append(updated, Marker+" "+policy)
	request.System = append(updated, request.System[index:]...)
	return request, nil
}

// Policy returns the deterministic policy text for a level, or empty for an
// unknown level so the transform stays a no-op.
func Policy(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "full":
		return "Prefer the smallest correct answer. No preamble, no recap, no repetition."
	case "lite":
		return "Prefer shorter answers; skip preamble and summary unless asked."
	case "ultra":
		return "Answer with the minimum viable output. Code or facts only when sufficient."
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
