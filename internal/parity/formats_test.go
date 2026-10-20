package parity

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/hub"
)

const translatorDir = "open-sse/translator"

// formatNames maps each LiteRouter format identifier to the Routeweft protocol
// name. The test fails when LiteRouter defines a format missing from this map,
// so the map cannot fall behind silently. The entries are names, not a copy of
// the oracle's list.
var formatNames = map[string]string{
	"openai":           "openai-chat",
	"openai-responses": "openai-responses",
	"claude":           "anthropic-messages",
	"gemini":           "gemini",
	"gemini-cli":       "gemini-cli",
	"vertex":           "vertex",
	"antigravity":      "antigravity",
	"kiro":             "kiro",
	"cursor":           "cursor",
	"ollama":           "ollama",
	"commandcode":      "commandcode",
}

// formatAliases are LiteRouter identifiers with no translator of their own that
// the oracle treats as another format: openai-response (singular) and codex
// share the Responses handling in translator/concerns/modality.js (the
// "case OPENAI_RESPONSES / OPENAI_RESPONSE / CODEX: stripResponses" arm) and
// utils/usageTracking.js (OPENAI_RESPONSE uses the OPENAI_RESPONSES fields). If
// LiteRouter ever registers a leg for one, the test fails until it is reviewed.
var formatAliases = map[string]string{
	"openai-response": "openai-responses",
	"codex":           "openai-responses",
}

// trackedLegGaps are legs LiteRouter registers that Routeweft has not built yet,
// keyed "kind from>to". Each is tracked work; the list only shrinks (ratchet).
// Every leg is #119 scope; the value names the PR family that closes it. The
// gemini and vertex legs landed in PR 5a; the gemini-cli and antigravity legs,
// including the two the pin shares with gemini, are PR 5b.
var trackedLegGaps = map[string]string{
	"request anthropic-messages>kiro":  "#119 Kiro",
	"request antigravity>openai-chat":  "#119 PR 5b Gemini CLI and Antigravity",
	"request gemini-cli>openai-chat":   "#119 PR 5b Gemini CLI and Antigravity",
	"request openai-chat>antigravity":  "#119 PR 5b Gemini CLI and Antigravity",
	"request openai-chat>gemini-cli":   "#119 PR 5b Gemini CLI and Antigravity",
	"request openai-chat>kiro":         "#119 Kiro",
	"response antigravity>openai-chat": "#119 PR 5b Gemini CLI and Antigravity",
	"response gemini-cli>openai-chat":  "#119 PR 5b Gemini CLI and Antigravity",
	"response kiro>anthropic-messages": "#119 Kiro",
	"response kiro>openai-chat":        "#119 Kiro",
	"response openai-chat>antigravity": "#119 PR 5b Gemini CLI and Antigravity",
}

type oracleLeg struct {
	hub.Leg
	where string
}

func (l oracleLeg) key() string { return fmt.Sprintf("%s %s>%s", l.Kind, l.From, l.To) }

// oracleFormats parses translator/formats.js.
func oracleFormats(t *testing.T, o oracle) (byConstant map[string]string, where map[string]string) {
	t.Helper()
	path := translatorDir + "/formats.js"
	source := o.file(t, path)
	block := regexp.MustCompile(`(?s)export const FORMATS = \{(.*?)\};`).FindStringSubmatch(source)
	if block == nil {
		t.Fatalf("%s: no FORMATS block", path)
	}
	byConstant, where = map[string]string{}, map[string]string{}
	for _, m := range regexp.MustCompile(`(\w+):\s*"([^"]+)"`).FindAllStringSubmatch(block[1], -1) {
		byConstant[m[1]] = m[2]
		where[m[2]] = lineOf(source, path, `\b`+m[1]+`:`)
	}
	return byConstant, where
}

// oracleLegs parses every register() call in translator/request and
// translator/response. register(from, to, requestFn, responseFn): a null
// requestFn marks a response leg. Request and response files are read for the
// direction their registration declares, so a misplaced file cannot hide a leg.
func oracleLegs(t *testing.T, o oracle, constants map[string]string) []oracleLeg {
	t.Helper()
	call := regexp.MustCompile(`register\(\s*FORMATS\.(\w+),\s*FORMATS\.(\w+),\s*(\S+)`)
	var legs []oracleLeg
	for _, dir := range []string{"request", "response"} {
		for _, name := range o.files(t, translatorDir+"/"+dir) {
			path := translatorDir + "/" + dir + "/" + name
			source := o.file(t, path)
			for _, m := range call.FindAllStringSubmatchIndex(source, -1) {
				from, to := source[m[2]:m[3]], source[m[4]:m[5]]
				kind := hub.Request
				if strings.HasPrefix(source[m[6]:m[7]], "null") {
					kind = hub.Response
				}
				if (dir == "request") != (kind == hub.Request) {
					t.Errorf("%s registers a %s leg from the %s directory", path, kind, dir)
				}
				f, okFrom := constants[from]
				g, okTo := constants[to]
				if !okFrom || !okTo {
					t.Fatalf("%s registers unknown format constants %s/%s", path, from, to)
				}
				line := 1 + strings.Count(source[:m[0]], "\n")
				legs = append(legs, oracleLeg{hub.Leg{From: f, To: g, Kind: kind}, fmt.Sprintf("%s:%d", path, line)})
			}
		}
	}
	if len(legs) == 0 {
		t.Fatal("derived no conversion legs; the translator parse is broken")
	}
	return legs
}

// Every format LiteRouter defines is either mapped to a Routeweft protocol or a
// reviewed alias, and no alias secretly has a translator of its own.
func TestEveryOracleFormatIsAccountedFor(t *testing.T) {
	o := pinnedOracle(t)
	constants, where := oracleFormats(t, o)
	legs := oracleLegs(t, o, constants)
	used := map[string]bool{}
	for _, leg := range legs {
		used[leg.From], used[leg.To] = true, true
	}
	for _, id := range constants {
		_, mapped := formatNames[id]
		target, aliased := formatAliases[id]
		switch {
		case mapped && aliased:
			t.Errorf("format %q is both mapped and aliased", id)
		case !mapped && !aliased:
			t.Errorf("LiteRouter defines format %q (%s) that Routeweft does not account for", id, where[id])
		case aliased && used[id]:
			t.Errorf("format %q is aliased to %q but LiteRouter now registers a leg for it; review", id, target)
		case mapped && !used[id]:
			t.Errorf("format %q is mapped but LiteRouter registers no leg for it; review", id)
		}
	}
	for id := range formatNames {
		if _, ok := where[id]; !ok {
			t.Errorf("formatNames lists %q, which LiteRouter no longer defines", id)
		}
	}
	for id := range formatAliases {
		if _, ok := where[id]; !ok {
			t.Errorf("formatAliases lists %q, which LiteRouter no longer defines", id)
		}
	}
	t.Logf("oracle %s defines %d format identifiers (%s) and registers %d legs", o.pin[:8], len(constants), where["openai"][:strings.Index(where["openai"], ":")], len(legs))
}

// Every leg LiteRouter registers must be implemented in Routeweft's hub, and
// Routeweft may not declare a leg LiteRouter does not have.
func TestEveryOracleConversionLegExists(t *testing.T) {
	o := pinnedOracle(t)
	constants, _ := oracleFormats(t, o)
	wanted := map[string]oracleLeg{}
	for _, leg := range oracleLegs(t, o, constants) {
		from, okFrom := routeweftName(leg.From)
		to, okTo := routeweftName(leg.To)
		if !okFrom || !okTo {
			t.Fatalf("%s: leg %s>%s uses a format that is not accounted for", leg.where, leg.From, leg.To)
		}
		leg.From, leg.To = from, to
		wanted[leg.key()] = leg
	}
	have := map[string]bool{}
	for _, leg := range hub.Legs() {
		have[oracleLeg{Leg: leg}.key()] = true
		if _, ok := wanted[oracleLeg{Leg: leg}.key()]; !ok {
			t.Errorf("Routeweft declares leg %s which LiteRouter does not register", oracleLeg{Leg: leg}.key())
		}
	}
	missing := map[string]string{}
	for key, leg := range wanted {
		if !have[key] {
			missing[key] = leg.where
		}
	}
	ratchet(t, "conversion legs", missing, trackedLegGaps)
	var direct []string
	for _, leg := range wanted {
		if leg.From != hub.Hub && leg.To != hub.Hub {
			direct = append(direct, leg.key()+" ("+leg.where+")")
		}
	}
	sort.Strings(direct)
	t.Logf("%d oracle legs, %d implemented here; pairs that bypass the hub: %v", len(wanted), len(have), direct)
}

func routeweftName(oracleID string) (string, bool) {
	if name, ok := formatNames[oracleID]; ok {
		return name, true
	}
	if target, ok := formatAliases[oracleID]; ok {
		return target, true
	}
	return "", false
}
