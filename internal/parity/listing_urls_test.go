package parity

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

const listingRoute = "src/app/api/providers/[id]/models/route.js"

// listingURLGaps are plain-bearer listing URLs of the pinned dashboard route that
// Routeweft does not request. Each is tracked work and the list only shrinks.
var listingURLGaps = map[string]string{}

// pinnedPlainListings reads the pin's authenticated listing table for the
// providers it lists with a bearer GET to one literal URL: the
// createOpenAIModelsConfig(url) entries and the literal `url:` entries whose
// auth is Authorization: Bearer.
func pinnedPlainListings(source string) map[string]string {
	out := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^  "?([a-z0-9-]+)"?: createOpenAIModelsConfig\("([^"]+)"\)`).FindAllStringSubmatch(source, -1) {
		out[m[1]] = m[2]
	}
	for _, m := range regexp.MustCompile(`(?ms)^  "?([a-z0-9-]+)"?: \{\n    url: "([^"]+)",\n    method: "GET",\n    headers: \{ "Content-Type": "application/json" \},\n    authHeader: "Authorization",\n    authPrefix: "Bearer ",`).FindAllStringSubmatch(source, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// For every provider the pin lists with a plain bearer GET, the URL Routeweft
// builds for the shared authenticated listing (admin discover, providers.go
// fetchConnectionModelEntries) must be the pinned URL. This is the per-provider
// evidence for the "authenticated listing" cell of the shared-listing rows in
// docs/P08_TEST_MATRIX.md.
func TestEveryPinnedPlainListingURLMatches(t *testing.T) {
	o := pinnedOracle(t)
	pinned := pinnedPlainListings(o.file(t, listingRoute))
	if len(pinned) < 20 {
		t.Fatalf("parsed only %d plain listing entries from %s; the pattern no longer reads the pin", len(pinned), listingRoute)
	}
	catalog, err := registry.NewBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	allow := func(raw string) (*url.URL, error) { return url.Parse(raw) }
	found := map[string]string{}
	var checked []string
	for id, want := range pinned {
		spec, ok := catalog.Lookup(id)
		if !ok {
			t.Errorf("pinned listing entry %q is not a Routeweft provider", id)
			continue
		}
		route := routing.ProviderRef{ProviderID: id, BaseURL: spec.DefaultBaseURL}
		if _, own, _ := wire.ListingFor(route); own {
			continue // the provider's own listing request has its own pin-shaped test
		}
		base, listable := wire.DiscoveryBase(route)
		if !listable {
			found[id] = "Routeweft lists nothing for it; the pin lists " + want
			continue
		}
		path := spec.DiscoveryPath
		for _, transport := range spec.Transports {
			if transport == registry.TransportOllama {
				path = "api/tags"
			}
		}
		got, err := wire.ListingURL(allow, base, "", path, "")
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		checked = append(checked, id)
		if got != want {
			found[id] = got + " (pin " + want + ")"
		}
	}
	ratchet(t, "plain-bearer listing URLs", found, listingURLGaps)
	sort.Strings(checked)
	t.Logf("compared %d listing URLs: %s", len(checked), strings.Join(checked, " "))
}
