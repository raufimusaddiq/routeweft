package ingress

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// denyAll is the policy for a key that cannot be resolved: restricted with an
// empty allowlist, so it permits nothing.
var denyAll = auth.Entry{Restricted: true}

// callerKey returns the authenticated client key for r. It is the zero Entry
// (unrestricted) only when API keys are not required. Every other miss fails
// closed: authorize and dispatch load snapshots separately, so a key paused,
// revoked or reloaded between the two must deny, never widen to unrestricted.
func callerKey(r *http.Request, snapshot *runtime.RuntimeSnapshot) auth.Entry {
	if snapshot.Settings()["requireApiKey"] == "false" {
		return auth.Entry{}
	}
	presented, ok := clientKey(r)
	if !ok {
		return denyAll
	}
	entry, ok := snapshot.APIKeys().Lookup(presented)
	if !ok {
		return denyAll
	}
	return entry
}

// modelNotAllowed is the one 403 message for a denied model request.
func modelNotAllowed(model string) string {
	return fmt.Sprintf("this API key may not use model %q", model)
}

// requestPermitted applies the key's allowlist to one requested model name.
// A Combo is matched by its own name only (it overrides a same-named model, and
// allowing a Combo does not require allowing each member). Otherwise the name
// and the "provider/model" it resolves to are both accepted.
func requestPermitted(entry auth.Entry, snapshot *runtime.RuntimeSnapshot, requested string) bool {
	if !entry.Restricted {
		return true
	}
	requested = strings.TrimSpace(requested)
	if _, isCombo := snapshot.ComboByName(requested); isCombo {
		return entry.Permits(requested)
	}
	if resolved, ok := snapshot.ServingModel(requested); ok {
		return entry.Permits(requested, resolved.ProviderID+"/"+resolved.ID)
	}
	return entry.Permits(requested)
}

// accessFilter returns the predicate that hides models the caller's key cannot
// use from discovery responses. It is nil-free: unrestricted keys allow all.
func accessFilter(r *http.Request, snapshot *runtime.RuntimeSnapshot) func(string) bool {
	entry := callerKey(r, snapshot)
	if !entry.Restricted {
		return func(string) bool { return true }
	}
	return func(id string) bool { return requestPermitted(entry, snapshot, id) }
}
