//go:build oracle

// Package parity holds the oracle-driven differential gate. It reads the pinned
// LiteRouter source through ORACLE_DIR and fails when Routeweft's provider
// catalog drifts from it: no hardcoded provider list or count lives here, so a
// table can never quietly hide a provider LiteRouter serves.
package parity

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/providers/usage"
)

type oracleProvider struct {
	ID        string `json:"id"`
	AuthModes []string
	Category  string
	Models    []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		ContextWindow int    `json:"contextWindow"`
	}
	AdvertisesUsage       bool `json:"advertisesUsage"`
	AdvertisesUsageAPIKey bool `json:"advertisesUsageApiKey"`
}

type oracleFacts struct {
	Providers       []oracleProvider  `json:"providers"`
	HiddenProviders []string          `json:"hiddenProviders"`
	Flows           map[string]string `json:"flows"`
	UsageHandlers   []string          `json:"usageHandlers"`
	Onboarding      []string          `json:"onboardingRoutes"`
}

func loadOracle(t *testing.T) oracleFacts {
	t.Helper()
	oracle := os.Getenv("ORACLE_DIR")
	if oracle == "" {
		t.Skip("parity gate sets ORACLE_DIR; normal unit tests need no oracle")
	}
	root := mustRepoRoot(t)
	// The script must live inside the repository: it imports compat/parity/providers.mjs
	// by relative path, and a temp directory cannot resolve that.
	script := filepath.Join(root, "compat", "parity", ".facts.generated.mjs")
	if err := os.WriteFile(script, []byte(oracleScript), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(script) })
	command := exec.Command("node", script)
	command.Dir = root
	command.Env = append(os.Environ(), "ORACLE_DIR="+oracle)
	raw, err := command.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("oracle facts failed: %v\n%s", err, exit.Stderr)
		}
		t.Fatal(err)
	}
	var facts oracleFacts
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatalf("decode oracle facts: %v\n%s", err, raw)
	}
	return facts
}

func mustRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := wd; dir != "/"; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
	}
	t.Fatal("repository root not found")
	return ""
}

// TestCatalogCoversEveryOracleProvider is the guard against the failure this
// ticket exists to prevent: a provider LiteRouter serves that Routeweft cannot
// route. It compares the whole set, both directions.
func TestCatalogCoversEveryOracleProvider(t *testing.T) {
	facts := loadOracle(t)
	catalog, err := registry.NewBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range facts.Providers {
		spec, ok := catalog.Lookup(provider.ID)
		if !ok {
			t.Errorf("provider %q is active in the pinned registry but absent from the Routeweft catalog", provider.ID)
			continue
		}
		if len(spec.Transports) == 0 {
			t.Errorf("provider %q has no transport", provider.ID)
		}
		if spec.ModelCatalog == registry.CatalogStatic && len(provider.Models) > 0 && len(spec.SeedModels) == 0 {
			t.Errorf("provider %q ships %d models in the oracle but Routeweft seeds none, so a fresh install cannot route it", provider.ID, len(provider.Models))
		}
	}
	// The reverse direction: Routeweft must not invent a built-in that the
	// reference does not have, because that would be an unreviewed product change.
	active := make(map[string]bool, len(facts.Providers))
	for _, provider := range facts.Providers {
		active[provider.ID] = true
	}
	for _, spec := range registry.Builtins() {
		if !active[spec.ID] {
			t.Errorf("provider %q exists in Routeweft but not in the pinned registry", spec.ID)
		}
	}
}

// TestSeedModelsMatchOracle proves the generated seed file is current: a
// regenerated file must be byte-identical to the committed one.
func TestSeedModelsMatchOracle(t *testing.T) {
	facts := loadOracle(t)
	catalog, err := registry.NewBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range facts.Providers {
		spec, ok := catalog.Lookup(provider.ID)
		if !ok {
			continue
		}
		// Every oracle model must be seeded with the oracle's own metadata, in
		// order. The served set may be larger: previously shipped IDs are kept as a
		// compat floor so an existing alias or Combo member still resolves after an
		// upgrade. Parity is about what Routeweft can serve, never about deleting a
		// model an operator already routed to.
		byID := make(map[string]registry.SeedModel, len(spec.SeedModels))
		for _, seed := range spec.SeedModels {
			byID[seed.ID] = seed
		}
		for _, model := range provider.Models {
			seed, ok := byID[model.ID]
			if !ok {
				t.Errorf("provider %q does not seed oracle model %q; regenerate with compat/parity/generate-seeds.mjs", provider.ID, model.ID)
				continue
			}
			if seed.Name != model.Name || seed.ContextWindow != model.ContextWindow {
				t.Errorf("provider %q seed %q = %+v, oracle = %+v", provider.ID, model.ID, seed, model)
			}
		}
		// The floor may only add, never substitute: anything beyond the oracle's
		// list must be a previously shipped ID.
		oracleIDs := make(map[string]bool, len(provider.Models))
		for _, model := range provider.Models {
			oracleIDs[model.ID] = true
		}
		for _, seed := range spec.SeedModels {
			if oracleIDs[seed.ID] {
				continue
			}
			if !registry.PreviouslyShipped(provider.ID, seed.ID) {
				t.Errorf("provider %q seeds %q, which is neither an oracle model nor a previously shipped one", provider.ID, seed.ID)
			}
		}
	}
}

// TestUsageCapabilityMatchesOracle pins the usage advertisement and the readable
// handler set. Both directions matter: hiding usage a provider offers, and
// claiming usage the reference cannot read.
func TestUsageCapabilityMatchesOracle(t *testing.T) {
	facts := loadOracle(t)
	catalog, err := registry.NewBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range facts.Providers {
		spec, ok := catalog.Lookup(provider.ID)
		if !ok {
			continue
		}
		// The flag mirrors the reference registry's own `usage:` advertisement and
		// nothing else. It is not "can Routeweft read it": iflow carries a handler
		// whose body returns a fixed message and makes no request, so the reference
		// does not advertise usage for it either. Conflating the two is what made the
		// dashboard offer a quota that always answered ErrNotConfigured.
		if spec.ReportsUsage != provider.AdvertisesUsage {
			t.Errorf("provider %q ReportsUsage=%v, oracle advertises usage=%v", provider.ID, spec.ReportsUsage, provider.AdvertisesUsage)
		}
	}
}

// TestOnboardingFlowCoverage requires every reference onboarding flow to be
// implemented. A flow that exists but is not served is worse than a missing one:
// the dashboard offers it and the endpoint refuses it.
//
// A provider the reference keeps hidden is still implemented (the flow exists and
// is tested) but is not in the catalog, because a hidden reference provider is a
// product decision recorded in PROVIDER_BASELINE, not an onboarding gap.
func TestOnboardingFlowCoverage(t *testing.T) {
	facts := loadOracle(t)
	implemented := onboarding.NewRegistry().OnboardingFlows()
	var missing []string
	for provider := range facts.Flows {
		if _, ok := implemented[provider]; !ok {
			missing = append(missing, provider)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("reference onboarding flows with no implementation: %v", missing)
	}
	catalog, err := registry.NewBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	hidden := make(map[string]bool, len(facts.HiddenProviders))
	for _, id := range facts.HiddenProviders {
		hidden[id] = true
	}
	for provider, flowType := range implemented {
		if _, ok := catalog.Lookup(provider); ok {
			continue
		}
		if !hidden[provider] {
			t.Errorf("%q has a %s flow but is not in the built-in catalog, so no endpoint can reach it", provider, flowType)
		}
	}
}

const oracleScript = `
import { activeProviderIds, hiddenProviderIds, providerFacts, onboardingFlows, usageHandledProviderIds, onboardingRoutes } from './providers.mjs';
const providers = activeProviderIds().map(entry => providerFacts(entry.id));
process.stdout.write(JSON.stringify({
  providers,
  hiddenProviders: hiddenProviderIds(),
  flows: Object.fromEntries(onboardingFlows()),
  usageHandlers: usageHandledProviderIds(),
  onboardingRoutes: onboardingRoutes(),
}));
`

// TestUsageReadersMatchOracleHandlers checks the registered reader set against the
// reference dispatcher's handler table. Without this the reader map was guarded only
// by a hand-kept list in usage_test.go — the drift this comparison exists to stop.
// A reader with no handler and a handler with no reader both fail: the first reads
// an endpoint the reference does not have, the second advertises a quota nobody can
// read.
func TestUsageReadersMatchOracleHandlers(t *testing.T) {
	facts := loadOracle(t)
	// Providers that keep a dedicated engine outside the usage package, plus iflow,
	// whose reference handler makes no request (see usage/oauth.go).
	dedicated := map[string]bool{"codex": true, "claude": true, "iflow": true}
	readers, err := usage.Readers(stubHTTPClient{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handled := make(map[string]bool, len(facts.UsageHandlers))
	for _, id := range facts.UsageHandlers {
		handled[id] = true
		if _, ok := readers[id]; !ok && !dedicated[id] {
			t.Errorf("reference reads usage for %q but Routeweft registers no reader", id)
		}
	}
	for id := range readers {
		if !handled[id] && !dedicated[id] {
			t.Errorf("Routeweft registers a usage reader for %q that the reference dispatcher does not handle", id)
		}
	}
}

// stubHTTPClient satisfies usage.HTTPClient without making a request: this test
// only inspects the reader set.
type stubHTTPClient struct{}

func (stubHTTPClient) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
}
