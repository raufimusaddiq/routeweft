package ingress

import (
	"fmt"
	"net/http"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// denyAll is the policy for a key that cannot be resolved: restricted with an
// empty allowlist, so it permits nothing.
var denyAll = auth.Entry{Restricted: true}

// callerKey returns the client key's access policy for r. Every miss fails
// closed: authorize and dispatch load snapshots separately, so a key paused,
// revoked or reloaded between the two must deny, never widen to unrestricted.
//
// When API keys are not required, a request with no key or an unknown key is
// unrestricted, but a presented key that is a known restricted key keeps its
// restriction whatever its pause/enabled state (the pin applies a key's access
// independently of authentication).
func callerKey(r *http.Request, snapshot *runtime.RuntimeSnapshot) auth.Entry {
	presented, ok := clientKey(r)
	if snapshot.Settings()["requireApiKey"] == "false" {
		if ok {
			if entry, known := snapshot.APIKeys().LookupAny(presented); known && entry.Restricted {
				return entry
			}
		}
		return auth.Entry{}
	}
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

// requestPermitted applies the key's compiled grants to the model string
// exactly as the router will route it (no trimming: the check and routing must
// see the same string). Identities are resolved, not spelled: see
// runtime.RuntimeSnapshot.KeyAllows.
func requestPermitted(entry auth.Entry, snapshot *runtime.RuntimeSnapshot, requested string) bool {
	return snapshot.KeyAllows(entry, requested)
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
