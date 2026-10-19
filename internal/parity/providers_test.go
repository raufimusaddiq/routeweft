package parity

import (
	"regexp"
	"sort"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// trackedProviderGaps are LiteRouter providers Routeweft does not serve yet.
// They are tracked work, never a reason to hide anything LiteRouter serves, and
// the list only shrinks (ratchet).
var trackedProviderGaps = map[string]string{
	"muse": "#115: Responses, API key, two seed models; needs the 80->81 baseline change in PRD/PROVIDER_BASELINE",
}

const registryDir = "open-sse/providers/registry"

// oracleProviders returns the active and hidden provider ids of the pinned
// registry, derived from index.js: an entry is active when both its import and
// its array element are uncommented, hidden when commented out.
func oracleProviders(t *testing.T, o oracle) (active, hidden map[string]string) {
	t.Helper()
	index := o.file(t, registryDir+"/index.js")
	imports := regexp.MustCompile(`(?m)^(//\s*)?import (\w+) from "\./(.+)\.js";`).FindAllStringSubmatch(index, -1)
	listed := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^  (//\s*)?(\w+),`).FindAllStringSubmatch(index, -1) {
		if m[1] == "" {
			listed[m[2]] = true
		}
	}
	idPattern := regexp.MustCompile(`(?m)^  id: "([^"]+)"`)
	active, hidden = map[string]string{}, map[string]string{}
	for _, m := range imports {
		commented, name, file := m[1] != "", m[2], m[3]
		source := o.file(t, registryDir+"/"+file+".js")
		id := idPattern.FindStringSubmatch(source)
		if id == nil {
			t.Fatalf("%s/%s.js has no top-level id", registryDir, file)
		}
		if commented || !listed[name] {
			hidden[id[1]] = registryDir + "/index.js"
		} else {
			active[id[1]] = lineOf(source, registryDir+"/"+file+".js", `^  id: `)
		}
	}
	return active, hidden
}

// Every active LiteRouter provider must be a Routeweft built-in, no built-in may
// exist that LiteRouter does not have, and LiteRouter's hidden providers (the
// only legitimate basis for hiding one) stay hidden.
func TestEveryOracleProviderIsABuiltin(t *testing.T) {
	o := pinnedOracle(t)
	active, hidden := oracleProviders(t, o)
	if len(active) == 0 || len(hidden) == 0 {
		t.Fatalf("derived %d active and %d hidden providers; the registry parse is broken", len(active), len(hidden))
	}
	builtin := map[string]bool{}
	for _, spec := range registry.Builtins() {
		builtin[spec.ID] = true
	}
	missing := map[string]string{}
	for id, where := range active {
		if !builtin[id] {
			missing[id] = where
		}
	}
	ratchet(t, "providers", missing, trackedProviderGaps)

	var extra []string
	for id := range builtin {
		if _, ok := active[id]; !ok {
			if _, isHidden := hidden[id]; isHidden {
				t.Errorf("%s is hidden by LiteRouter (%s) but is a Routeweft built-in", id, hidden[id])
			} else {
				extra = append(extra, id)
			}
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("Routeweft built-ins LiteRouter does not have (adding is the owner's call): %v", extra)
	}
	t.Logf("oracle %s: %d active providers, %d hidden %v; Routeweft has %d built-ins", o.pin[:8], len(active), len(hidden), keys(hidden), len(builtin))
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
