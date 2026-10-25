package registry

import "fmt"

// ReadinessStatus is what this build can do for a provider (SPEC §36, PRD-PROV-004).
//
// It is an immutable release fact, declared here and nowhere else. It is not the
// operator's enable switch and not the presence of a credential:
//
//   - enabled is a per-connection preference stored in SQLite; enabling an
//     unavailable provider is allowed, and the request fails honestly;
//   - hasCredential is runtime state of an account; a ready provider is ready
//     with none, and an unavailable one stays unavailable with a valid one.
//
// The zero value means "not declared". Consumers treat it as unknown, never as
// ready and never as unavailable.
type ReadinessStatus string

const (
	// ProviderReady: the serving path for every declared transport is wired.
	ProviderReady ReadinessStatus = "ready"
	// ProviderExperimental: requests are served, but PARITY tracks an open gap on
	// the serving path or the wire has no live-gateway evidence yet.
	ProviderExperimental ReadinessStatus = "experimental"
	// ProviderUnavailable: no client wire reaches this provider; a request routed
	// to it would fail at the last hop. internal/ingress tests tie this to the
	// ingress unwired list so the two cannot drift.
	ProviderUnavailable ReadinessStatus = "unavailable"
)

// Readiness is one provider's build-time capability fact.
type Readiness struct {
	Status ReadinessStatus
	// Reason says why the provider is not ready, with a pointer to the tracking
	// row. Empty for ready providers.
	Reason string
}

// Served reports whether this build can send a request to the provider at all.
func (r Readiness) Served() bool {
	return r.Status == ProviderReady || r.Status == ProviderExperimental
}

func (s ReadinessStatus) valid() bool {
	switch s {
	case ProviderReady, ProviderExperimental, ProviderUnavailable:
		return true
	}
	return false
}

func (r Readiness) validate() error {
	if r.Status == "" {
		if r.Reason != "" {
			return fmt.Errorf("readiness reason without a status")
		}
		return nil
	}
	if !r.Status.valid() {
		return fmt.Errorf("unsupported provider readiness %q", r.Status)
	}
	if r.Status != ProviderReady && r.Reason == "" {
		return fmt.Errorf("%s readiness requires a reason", r.Status)
	}
	return nil
}

func unavailable(reason string) Readiness  { return Readiness{ProviderUnavailable, reason} }
func experimental(reason string) Readiness { return Readiness{ProviderExperimental, reason} }

// notReady lists every built-in provider that is not ready. A provider absent
// from this map is ready; Builtins stamps that onto its Spec so the value is
// explicit on every served row, and TestEveryBuiltinDeclaresReadiness fails if a
// provider carries none. The unavailable set must equal the ingress unwired /
// unreachable list (internal/ingress/readiness_test.go).
var notReady = map[string]Readiness{
	"cursor":         unavailable("public enablement of the Connect-RPC transport is a separate decision (#115 PR-L)"),
	"typesafe":       unavailable("no conversion leg exists at the pin for the systemone format (registry/typesafe.js:22)"),
	"qoder":          unavailable("the pin converts in the executor, not a translator leg (executors/qoder.js:210-335,426-597): signing is wired, the envelope and response codec are not (PARITY 6.19, #194)"),
	"perplexity-web": unavailable("session cookie, browser identity and codec are built (wire.PerplexityWeb*) but not routable: the pin converts in the executor (no hub leg, internal/parity), live acceptance is unproven, and routability awaits owner approval (PARITY 6.19, #195)"),
	"grok-web":       unavailable("session cookie, browser identity and codec are built (wire.GrokWeb*) but not routable: the pin converts in the executor (no hub leg, internal/parity), live acceptance is unproven, and routability awaits owner approval (PARITY 6.19, #195)"),

	"azure":          experimental("deployment, API-version and endpoint provider data and specialized URL rules are open (PARITY P02/C06)"),
	"vertex-partner": experimental("project-id probe, service-account/ADC minting and raw-key auth are not built; unverified against a real gateway (PARITY #115 PR-E2)"),
	"cloudflare-ai":  experimental("account-scoped URLs are implemented from the pin and unverified against a live Workers AI gateway (PARITY #115 PR-H)"),
	"github":         experimental("Copilot token exchange and live models/limits are open; the Claude Messages path and Chat-to-Responses escalation are unverified against a live gateway (PARITY P02-P05)"),
	"ollama-local":   experimental("local model discovery, private-URL policy and native codecs need contracts (PARITY P02/P03)"),
	"opencode-go":    experimental("multi-protocol model dispatch and envelopes are open (PARITY P02/P03)"),
}

// applyReadiness stamps the explicit readiness onto every built-in spec.
func applyReadiness(specs []Spec) []Spec {
	for i := range specs {
		if declared, ok := notReady[specs[i].ID]; ok {
			specs[i].Readiness = declared
		} else {
			specs[i].Readiness = Readiness{Status: ProviderReady}
		}
	}
	return specs
}
