package parity

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// The provider acceptance matrix (#230): every one of the 81 active providers
// mapped to its onboarding class (O01-O06), quota class (Q01/Q02), the tests
// that are its evidence, and the residual tickets that own the gaps. The census
// the owner fixed is asserted, not restated: 81 active, 3 hidden, 21
// special-auth families, 24 dedicated quota readers and 57 providers without a
// dedicated quota surface.
//
// docs/PROVIDER_ACCEPTANCE_MATRIX.md is generated from this table and compared
// byte for byte. A cell is evidence only when it names a Test function that
// exists; a class not in O01-O06 or Q01/Q02 fails the build; a provider missing
// from the map fails the build. "Ready" in the catalog is build wiring, never a
// live or production claim: supervised canaries need separate owner consent.

// onboardingClass is one provider's auth family and the O class it belongs to.
type onboardingClass struct {
	Class  string
	Family string
}

const (
	oAPIKey  = "O01" // shared API-key form (src/shared/constants/providers.js:67, src/app/api/providers/route.js:1-60)
	oNoAuth  = "O02" // no credential (src/shared/constants/providers.js:46)
	oBrowser = "O03" // browser OAuth with a pasted callback (src/app/api/oauth/[provider]/[action]/route.js:88-139,317-490)
	oDevice  = "O04" // device-code approval and poll (src/app/api/oauth/[provider]/[action]/route.js:88-98,238-292,492-540)
	oCookie  = "O05" // cookie/session credential (src/shared/constants/providers.js:70)
	oImport  = "O06" // specialized token/PAT/project import (src/app/api/oauth/*/import*, bulk-import, auto-import)

	qReader = "Q01" // provider has a dedicated quota reader (internal/providers/usage; PARITY row 75/§6.28-6.31)
	qNone   = "Q02" // no dedicated quota surface: "not reported", never zero/unlimited
)

var onboardingClasses = map[string]onboardingClass{
	"alicode-intl":      {oAPIKey, "alicode-intl"},
	"alicode":           {oAPIKey, "alicode"},
	"anthropic":         {oAPIKey, "anthropic"},
	"antigravity":       {oBrowser, "google"},
	"azure":             {oAPIKey, "azure"},
	"blackbox":          {oAPIKey, "blackbox"},
	"byteplus":          {oAPIKey, "byteplus"},
	"cerebras":          {oAPIKey, "cerebras"},
	"chutes":            {oAPIKey, "chutes"},
	"claude":            {oBrowser, "claude"},
	"cline":             {oBrowser, "cline"},
	"clinepass":         {oImport, "cline"},
	"cloudflare-ai":     {oAPIKey, "cloudflare-ai"},
	"codebuddy-cn":      {oDevice, "codebuddy"},
	"codex":             {oImport, "codex"},
	"cohere":            {oAPIKey, "cohere"},
	"commandcode":       {oAPIKey, "commandcode"},
	"cursor":            {oImport, "cursor"},
	"deepseek":          {oAPIKey, "deepseek"},
	"featherless":       {oAPIKey, "featherless"},
	"fireworks":         {oAPIKey, "fireworks"},
	"gemini-cli":        {oBrowser, "google"},
	"gemini":            {oAPIKey, "gemini"},
	"github":            {oDevice, "github"},
	"gitlab":            {oImport, "gitlab"},
	"glm-cn":            {oAPIKey, "glm-cn"},
	"glm":               {oAPIKey, "glm"},
	"grok-cli":          {oDevice, "grok"},
	"grok-web":          {oCookie, "grok"},
	"groq":              {oAPIKey, "groq"},
	"hyperbolic":        {oAPIKey, "hyperbolic"},
	"iflow":             {oImport, "iflow"},
	"kilocode":          {oDevice, "kilocode"},
	"kimchi":            {oImport, "kimchi"},
	"kimi":              {oDevice, "kimi"},
	"kiro":              {oImport, "kiro"},
	"mimo-free":         {oNoAuth, "mimo-free"},
	"minimax-cn":        {oAPIKey, "minimax-cn"},
	"minimax":           {oAPIKey, "minimax"},
	"mistral":           {oAPIKey, "mistral"},
	"mmf":               {oNoAuth, "mmf"},
	"nebius":            {oAPIKey, "nebius"},
	"nvidia":            {oAPIKey, "nvidia"},
	"ollama-local":      {oNoAuth, "ollama-local"},
	"ollama":            {oAPIKey, "ollama"},
	"openai":            {oAPIKey, "openai"},
	"opencode-go":       {oAPIKey, "opencode-go"},
	"opencode":          {oNoAuth, "opencode"},
	"openrouter":        {oAPIKey, "openrouter"},
	"perplexity-web":    {oCookie, "perplexity-web"},
	"perplexity":        {oAPIKey, "perplexity"},
	"perplexity-agent":  {oAPIKey, "perplexity-agent"},
	"qoder":             {oDevice, "qoder"},
	"siliconflow":       {oAPIKey, "siliconflow"},
	"together":          {oAPIKey, "together"},
	"venice":            {oAPIKey, "venice"},
	"vercel-ai-gateway": {oAPIKey, "vercel-ai-gateway"},
	"vertex-partner":    {oImport, "vertex-partner"},
	"vertex":            {oImport, "google"},
	"volcengine-ark":    {oAPIKey, "volcengine-ark"},
	"xai":               {oBrowser, "xai"},
	"xiaomi-mimo":       {oBrowser, "xiaomi-mimo"},
	"xiaomi-tokenplan":  {oAPIKey, "xiaomi-tokenplan"},
	"alims-intl":        {oAPIKey, "alims-intl"},
	"codebuddy-intl":    {oDevice, "codebuddy"},
	"zed":               {oBrowser, "zed"},
	"api-airforce":      {oAPIKey, "api-airforce"},
	"baidu":             {oAPIKey, "baidu"},
	"bazaarlink":        {oAPIKey, "bazaarlink"},
	"bluesminds":        {oAPIKey, "bluesminds"},
	"kilo-gateway":      {oAPIKey, "kilo-gateway"},
	"llm7":              {oAPIKey, "llm7"},
	"sambanova":         {oAPIKey, "sambanova"},
	"tencent":           {oAPIKey, "tencent"},
	"morph":             {oAPIKey, "morph"},
	"poolside":          {oAPIKey, "poolside"},
	"tokenrouter":       {oAPIKey, "tokenrouter"},
	"alitp-intl":        {oAPIKey, "alitp-intl"},
	"kenari":            {oAPIKey, "kenari"},
	"typesafe":          {oAPIKey, "typesafe"},
	"muse":              {oAPIKey, "muse"},
}

// settingsSensitive providers still own a provider-specific settings gap (#228):
// deployment/project/account/ARN or local-URL fields the shared form does not model.
var settingsSensitive = map[string]bool{
	"azure": true, "cloudflare-ai": true, "kimchi": true, "ollama-local": true,
	"vertex-partner": true, "vertex": true, "xiaomi-mimo": true, "gitlab": true,
}

var hiddenProviders = []string{"trae", "devin-cli", "windsurf"}

func acceptanceEvidence(c cell) string {
	if len(c.Refs) > 0 {
		parts := make([]string, 0, len(c.Refs))
		for _, ref := range c.Refs {
			parts = append(parts, "`"+ref+"`")
		}
		return strings.Join(parts, ", ")
	}
	switch c.Kind {
	case kindN:
		return "N (pin)"
	case kindG, kindU, kindC:
		note := c.Note
		if i := strings.Index(note, ";"); i > 0 {
			note = note[:i]
		}
		if len(note) > 140 {
			note = note[:137] + "…"
		}
		if note == "" {
			note = "no evidence"
		}
		return string(c.Kind) + ": " + note
	}
	return "—"
}

func residualsFor(id string, special bool, qClass string) string {
	out := []string{"#76"}
	if qClass == qReader {
		out = append(out, "#225")
	}
	if special {
		out = append(out, "#231")
	}
	if settingsSensitive[id] {
		out = append(out, "#228")
	}
	return strings.Join(out, ", ")
}

func onboardingEvidence(o onboardingClass) string {
	switch o.Class {
	case oAPIKey:
		return "`control/api:TestSharedAPIKeyProviderAcceptance`"
	case oNoAuth:
		return "`control/api:TestNoSecretProviderAcceptance`"
	default:
		return "gap (`#231`)"
	}
}

func renderAcceptanceMatrix(t *testing.T, specs []registry.Spec, rows map[string][columns]cell) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`# Provider acceptance matrix (#230)

Status: **source-evidence matrix**. It is not a live, credential, streaming or
production claim. "Ready" in the catalog is build wiring only (PARITY §6.15,
PROVIDER_BASELINE §3); a supervised live canary needs separate owner consent
and is never implied by a row here.

This file is generated from ` + "`internal/parity/provider_acceptance_test.go`" + `. ` +
		"`TestProviderAcceptanceMatrixIsCurrent` compares it byte for byte and fails " +
		"if a provider is missing, a class is unknown, a census count drifts, a named " +
		"test does not exist, or a residual is empty. Regenerate with " +
		"`ACCEPTANCE_WRITE=1 go test ./internal/parity -run TestProviderAcceptanceMatrixIsCurrent`.\n\n" +
		"Read with `docs/P08_TEST_MATRIX.md`: P08 is the per-behaviour evidence " +
		"(listing, quota, unavailability, import, streaming, non-streaming, errors, " +
		"cached tokens); this file is the per-provider onboarding/quota acceptance " +
		"and the residual owner.\n\n" +
		"The audit's historic 26-gap count was a snapshot of `3e3facd`, not a current " +
		"test result; the current per-behaviour counts are P08's \"Honest totals\", and " +
		"every gap here names its owner. This change delivers the matrix and the " +
		"shared O01/O02 acceptance; provider-family onboarding stays with the residual " +
		"tickets (#76, #225, #228, #231), not with a broad rewrite in this PR.\n\n")

	// Census, derived from the same inputs the rows use.
	classCount := map[string]int{}
	families := map[string]bool{}
	noAuth := 0
	special := 0
	for _, spec := range specs {
		o := onboardingClasses[spec.ID]
		classCount[o.Class]++
		if o.Class == oNoAuth {
			noAuth++
		}
		if o.Class != oAPIKey && o.Class != oNoAuth {
			special++
			families[o.Family] = true
		}
	}
	qCount := map[string]int{}
	var refused []string
	for _, spec := range specs {
		if rows[spec.ID][colQuota].Kind == kindN {
			qCount[qNone]++
		} else {
			qCount[qReader]++
		}
		if spec.Readiness.Status == registry.ProviderUnavailable {
			refused = append(refused, spec.ID)
		}
	}
	fmt.Fprintf(&b, "## Census\n\n")
	fmt.Fprintf(&b, "- **%d active** providers; **%d hidden** remain absent: %s (PROVIDER_BASELINE §3, \"hidden\" rows).\n", len(specs), len(hiddenProviders), strings.Join(hiddenProviders, ", "))
	fmt.Fprintf(&b, "- **%d** shared API-key (O01), **%d** no-auth (O02), **%d** providers with a non-shared auth method (O03-O06) in **%d** families.\n", classCount[oAPIKey], noAuth, special, len(families))
	fmt.Fprintf(&b, "- **%d** dedicated quota readers (Q01), **%d** providers with no dedicated quota surface (Q02; reported as *not reported*, never zero/unlimited).\n", qCount[qReader], qCount[qNone])
	fmt.Fprintf(&b, "- **%d** refused by immutable readiness: %s. Storing a credential never makes them routable or a Ready claim.\n\n", len(refused), codeList(refused))

	b.WriteString(`## Classes

| Class | Meaning | Oracle |
|---|---|---|
| O01 | Shared API-key form: create/edit/revoke, test, discovery | ` + "`src/shared/constants/providers.js:67`" + `, ` + "`src/app/api/providers/route.js:1-60`" + `, ` + "`[id]/route.js:63,87,175`" + ` |
| O02 | No-auth/free connection: no secret is required or invented | ` + "`src/shared/constants/providers.js:46`" + ` |
| O03 | Browser OAuth with a pasted callback | ` + "`src/app/api/oauth/[provider]/[action]/route.js:88-139,317-490`" + ` |
| O04 | Device-code approval and poll | ` + "`src/app/api/oauth/[provider]/[action]/route.js:238-292,492-540`" + ` |
| O05 | Cookie/session credential | ` + "`src/shared/constants/providers.js:70`" + ` |
| O06 | Specialized token/PAT/project import (incl. bulk and auto-import) | ` + "`src/app/api/oauth/codex/import-token/route.js`" + `, ` + "`src/app/api/oauth/grok-cli/bulk-import/route.js`" + `, ` + "`src/app/api/oauth/cursor/auto-import/route.js`" + ` |
| Q01 | Dedicated quota reader: a pinned response parser | ` + "`internal/providers/usage`, PARITY §6.28-6.31" + ` |
| Q02 | No dedicated quota surface | P08 quota cell ` + "`N`" + ` and the pin's registry entry |

The O class is the **acceptance workflow**, not a claim that the provider is
routable. Providers refused by immutable readiness are named in the census; a
stored credential never makes one routable, and no row here claims it serves.

## Acceptance methods → fixture/evidence

| Method | Evidence | Fixture / cleanup |
|---|---|---|
| Create, edit, revoke | ` + "`control/api:TestSharedAPIKeyProviderAcceptance`" + ` | in-process admin mux, per-test temp SQLite |
| Wrong key (401), 429, timeout | ` + "`control/api:TestSharedAPIKeyProviderAcceptance`" + ` (502, class named, credential never echoed) | injected ` + "`probeHTTPFunc`" + `; no network |
| Provider-native test and discovery | P08 listing/stream/non-stream cells per row | sanitized fixtures in ` + "`internal/ingress`, `internal/providers/wire`" + ` |
| No-secret create and discovery | ` + "`control/api:TestNoSecretProviderAcceptance`" + ` | injected upstream asserts no Authorization/key header |
| Passthrough and cached IDs survive a failed discovery | ` + "`control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels`" + ` | temp SQLite; 503 fixture then read-back |
| Quota read / no dedicated surface | P08 quota cells; reader tests under ` + "`internal/providers/usage`" + ` | pinned sanitized responses |
| Special-auth onboarding | P08 import cells; residual ` + "`#231`" + ` for workflows still short | sealed/sanitized fixtures only |

Every Go fixture is an in-process ` + "`httptest`" + `/` + "`probeHTTPFunc`" + ` with a ` + "`t.TempDir`" + `
database, so it is removed even when a test fails. Browser smokes are checked by
` + "`ui/scripts/smoke-cleanup-check.sh`" + `; a new smoke must be added there in the
same change.

## Matrix

| Provider | O | Family | Q | Lifecycle & errors | Discovery | Quota | Import | Residual |
|---|---|:-:|:-:|---|---|---|---|---|
`)
	for _, spec := range specs {
		o := onboardingClasses[spec.ID]
		row := rows[spec.ID]
		q := qReader
		if row[colQuota].Kind == kindN {
			q = qNone
		}
		special := o.Class != oAPIKey && o.Class != oNoAuth
		discovery := acceptanceEvidence(row[colList])
		if spec.PassthroughModels || spec.ModelCatalog == registry.CatalogDynamic {
			discovery += ", `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels`"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			spec.ID, o.Class, o.Family, q,
			onboardingEvidence(o),
			discovery,
			acceptanceEvidence(row[colQuota]),
			acceptanceEvidence(row[colImport]),
			residualsFor(spec.ID, special, q),
		)
	}
	return b.String()
}

func TestProviderAcceptanceMatrixIsCurrent(t *testing.T) {
	specs, rows := buildMatrix(t)
	if len(specs) != 81 {
		t.Fatalf("catalog has %d active providers, want 81", len(specs))
	}
	tests := testFunctions(t)
	families := map[string]bool{}
	classCount := map[string]int{}
	specialCount := 0
	qCount := map[string]int{}
	for _, spec := range specs {
		o, ok := onboardingClasses[spec.ID]
		if !ok {
			t.Errorf("%s has no onboarding class", spec.ID)
			continue
		}
		if o.Class != oAPIKey && o.Class != oNoAuth && o.Class != oBrowser && o.Class != oDevice && o.Class != oCookie && o.Class != oImport {
			t.Errorf("%s has unknown O class %q", spec.ID, o.Class)
		}
		classCount[o.Class]++
		if o.Class != oAPIKey && o.Class != oNoAuth {
			specialCount++
			families[o.Family] = true
		}
		cell := rows[spec.ID][colQuota]
		if cell.Kind == kindN {
			qCount[qNone]++
		} else {
			qCount[qReader]++
		}
	}
	for id := range onboardingClasses {
		if _, ok := rows[id]; !ok {
			t.Errorf("onboarding map names %q, which is not an active provider", id)
		}
	}
	// Census the catalog fixes: 52 shared API-key, 4 no-auth (mimo-free, mmf,
	// opencode, ollama-local), 25 special-auth providers in 20 families.
	if classCount[oAPIKey] != 52 || classCount[oNoAuth] != 4 || specialCount != 25 || len(families) != 20 {
		t.Errorf("onboarding census = %d O01 / %d O02 / %d special in %d families, want 52 / 4 / 25 / 20",
			classCount[oAPIKey], classCount[oNoAuth], specialCount, len(families))
	}
	if qCount[qReader] != 24 || qCount[qNone] != 57 {
		t.Errorf("quota census = %d readers / %d none, want 24 / 57", qCount[qReader], qCount[qNone])
	}
	body := renderAcceptanceMatrix(t, specs, rows)

	// Every test the document names must exist, so a claim cannot outlive its
	// fixture. This walks the rendered document, not a subset of inputs: class
	// evidence, the appended passthrough test and the methods table included.
	refPattern := regexp.MustCompile("`([a-z0-9]+(?:/[a-z0-9]+)*):(Test[A-Za-z0-9_]+)`")
	named := 0
	for _, match := range refPattern.FindAllStringSubmatch(body, -1) {
		dir, name := match[1], match[2]
		if !tests[dir][name] {
			t.Errorf("matrix names %q, which is not a Test function in internal/%s", match[0], dir)
		}
		named++
	}
	if named == 0 {
		t.Fatal("the matrix names no test at all")
	}
	// Every row carries at least one residual ticket, in the linked form #N.
	ticketPattern := regexp.MustCompile(`^#[0-9]+$`)
	for _, spec := range specs {
		o := onboardingClasses[spec.ID]
		q := qReader
		if rows[spec.ID][colQuota].Kind == kindN {
			q = qNone
		}
		residual := residualsFor(spec.ID, o.Class != oAPIKey && o.Class != oNoAuth, q)
		if residual == "" {
			t.Errorf("%s has no residual ticket", spec.ID)
			continue
		}
		for _, ticket := range strings.Split(residual, ", ") {
			if !ticketPattern.MatchString(ticket) {
				t.Errorf("%s residual %q is not a #N ticket", spec.ID, ticket)
			}
		}
	}
	// The three hidden providers are a documented census, not an omission.
	baseline, err := os.ReadFile("../../docs/PROVIDER_BASELINE.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range hiddenProviders {
		if !strings.Contains(string(baseline), "| "+hidden+" | hidden |") {
			t.Errorf("%s is not listed as hidden in PROVIDER_BASELINE", hidden)
		}
	}

	path := "../../docs/PROVIDER_ACCEPTANCE_MATRIX.md"
	if os.Getenv("ACCEPTANCE_WRITE") != "" {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (regenerate with ACCEPTANCE_WRITE=1)", err)
	}
	if string(current) != body {
		t.Fatal("docs/PROVIDER_ACCEPTANCE_MATRIX.md is stale; regenerate with ACCEPTANCE_WRITE=1 go test ./internal/parity -run TestProviderAcceptanceMatrixIsCurrent")
	}
}
