package parity

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// excludedURLProviders are skipped on purpose, not tracked: the oracle URL is a
// template ({accountId}, {model}) that only exists once a connection supplies the
// account, so a literal comparison cannot hold. internal/providers/cloudflare
// builds that path from the connection and has its own tests.
var excludedURLProviders = map[string]string{
	"cloudflare-ai": "templated URL ({accountId}); built per connection by internal/providers/cloudflare",
}

// trackedURLGaps are "provider/protocol" pairs whose effective Routeweft upstream
// URL differs from the pinned registry's transport.baseUrl. Each is tracked work;
// the list only shrinks (ratchet).
var trackedURLGaps = map[string]string{
	// Missing wires, not URL defects: Routeweft declares no such transport.
	"qoder/openai-chat": "#115 cluster G: qoder has its own envelope wire; the registry URL is not an OpenAI Chat endpoint",
	// The registry baseUrl is only a base; the oracle executor builds the sent URL.
	"opencode/openai-chat":       "#115 PR-E2: registry base is the host only; wire.opencodeTarget adds zen/v1/chat/completions (executors/opencode.js:471-476)",
	"vertex-partner/openai-chat": "#115 PR-E2: registry base is the host only; wire.vertexPartnerTarget adds the settings-supplied project path (executors/vertex.js:76-81)",
	"zed/openai-chat":            "#115 cluster B: executor builds the URL (zed.js:25)",
}

// trackedUnverifiedURLs are providers whose registry file the regex parse cannot
// read, so their URL is NOT compared. They are listed rather than skipped: a file
// that becomes parseable, or a new one that is not, fails the test.
var trackedUnverifiedURLs = map[string]string{
	"antigravity":      "#115 cluster C: baseUrl is a shared constant; Google wire",
	"azure":            "#115 cluster B: baseUrl is operator-supplied/computed, no literal",
	"commandcode":      "#115 cluster G: format has no Routeweft transport comparable by URL",
	"cursor":           "#115 cluster E: format has no Routeweft transport comparable by URL",
	"gemini":           "#115 cluster C: native Gemini path is model-templated, not a literal",
	"gemini-cli":       "#115 cluster C: Google wire",
	"grok-cli":         "#115 cluster B: baseUrl is a shared constant; Responses wire",
	"grok-web":         "#115 cluster F: cookie web wire",
	"kiro":             "#115 cluster D: AWS event-stream wire",
	"perplexity-web":   "#115 cluster F: cookie web wire",
	"typesafe":         "SystemOne wire; not an OpenAI/Messages URL",
	"vertex":           "#115 cluster C: Google wire",
	"xiaomi-tokenplan": "#115 cluster B: region-dynamic URL resolved in the executor",
}

// oracleFormatProtocols maps the registry `format` of a transport to the
// Routeweft transport it is compared against. A format with no entry cannot be
// compared and is reported as unverified. An absent format means "openai".
var oracleFormatProtocols = map[string]registry.Protocol{
	"openai":           registry.TransportOpenAIChat,
	"openai-responses": registry.TransportOpenAIResponses,
	"claude":           registry.TransportAnthropic,
	"ollama":           registry.TransportOllama,
}

// defaultEndpoints are the shared dispatch paths used when a Spec declares no
// TransportEndpoints override (internal/ingress: openai.go, ingress.go, compat.go).
var defaultEndpoints = map[registry.Protocol]string{
	registry.TransportOpenAIChat:      "chat/completions",
	registry.TransportOpenAIResponses: "responses",
	registry.TransportAnthropic:       "messages",
	registry.TransportOllama:          "api/chat",
}

type oracleTransport struct {
	format, baseURL string
}

var (
	entryFormat  = regexp.MustCompile(`format: "([^"]+)"`)
	entryBaseURL = regexp.MustCompile(`baseUrl: "([^"]+)"`)
)

// block returns the text of the top-level `key: {` / `key: [` value of a registry
// file by matching brackets, or "" when absent.
func block(source, key string) string {
	loc := regexp.MustCompile(`(?m)^  ` + key + `: [\{\[]`).FindStringIndex(source)
	if loc == nil {
		return ""
	}
	open := loc[1] - 1
	depth := 0
	for i := open; i < len(source); i++ {
		switch source[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return source[open : i+1]
			}
		}
	}
	return ""
}

// topLevelEntries splits an array block into its direct `{...}` elements.
func topLevelEntries(array string) []string {
	var entries []string
	depth, start := 0, 0
	for i := 1; i < len(array); i++ {
		switch array[i] {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 {
				entries = append(entries, array[start:i+1])
			}
		}
	}
	return entries
}

// parseTransports reads a registry file's literal (format, baseUrl) pairs. With a
// `transports` array those entries are the contract; otherwise the single
// `transport` block is. The reason is non-empty when something could not be read
// literally, so the provider is unverified rather than silently skipped.
func parseTransports(source string) (found []oracleTransport, unverified string) {
	if array := block(source, "transports"); array != "" {
		for _, entry := range topLevelEntries(array) {
			url := entryBaseURL.FindStringSubmatch(entry)
			if url == nil {
				return nil, "a transports[] entry has no literal baseUrl (resolved by an executor)"
			}
			format := "openai"
			if m := entryFormat.FindStringSubmatch(entry); m != nil {
				format = m[1]
			}
			found = append(found, oracleTransport{format, url[1]})
		}
		if len(found) == 0 {
			return nil, "transports[] has no parseable entry"
		}
		return found, ""
	}
	single := block(source, "transport")
	if single == "" {
		return nil, "no transport block"
	}
	url := entryBaseURL.FindStringSubmatch(single)
	if url == nil {
		return nil, "transport.baseUrl is not a string literal (shared constant or computed)"
	}
	format := "openai"
	if m := entryFormat.FindStringSubmatch(single); m != nil {
		format = m[1]
	}
	return []oracleTransport{{format, url[1]}}, ""
}

func joinURL(base, endpoint string) string {
	return strings.TrimRight(base, "/") + "/" + endpoint
}

// effectiveURL is what Routeweft sends upstream for a transport: the base URL plus
// the declared endpoint, else the shared default (internal/ingress).
func effectiveURL(spec registry.Spec, protocol registry.Protocol) string {
	endpoint, ok := spec.EndpointFor(protocol)
	if !ok {
		endpoint = defaultEndpoints[protocol]
	}
	return joinURL(spec.DefaultBaseURL, endpoint)
}

// Every active provider's literal transport URL in the pinned registry must equal
// the URL Routeweft builds. The oracle sends transport.baseUrl verbatim for
// ordinary providers (open-sse/executors/default.js, the final
// `const url = this.config.baseUrl` branch), so a Routeweft base plus endpoint
// that differs is a wrong upstream URL.
func TestEveryOracleTransportURLMatches(t *testing.T) {
	o := pinnedOracle(t)
	active, _ := oracleProviders(t, o)
	byID := map[string]registry.Spec{}
	for _, spec := range registry.Builtins() {
		byID[spec.ID] = spec
	}
	// provider id -> registry file name
	files := map[string]string{}
	index := o.file(t, registryDir+"/index.js")
	for _, m := range regexp.MustCompile(`(?m)^import \w+ from "\./(.+)\.js";`).FindAllStringSubmatch(index, -1) {
		source := o.file(t, registryDir+"/"+m[1]+".js")
		if id := regexp.MustCompile(`(?m)^  id: "([^"]+)"`).FindStringSubmatch(source); id != nil {
			files[id[1]] = m[1]
		}
	}

	gaps, unverified := map[string]string{}, map[string]string{}
	compared := 0
	for id := range active {
		if _, skip := excludedURLProviders[id]; skip {
			continue
		}
		path := registryDir + "/" + files[id] + ".js"
		source := o.file(t, path)
		transports, reason := parseTransports(source)
		if reason != "" {
			unverified[id] = reason
			continue
		}
		spec, ok := byID[id]
		if !ok {
			continue // a missing provider is TestEveryOracleProviderIsABuiltin's gap
		}
		for _, transport := range transports {
			protocol, known := oracleFormatProtocols[transport.format]
			if !known {
				unverified[id] = fmt.Sprintf("transport format %q has no comparable Routeweft transport", transport.format)
				continue
			}
			declared := false
			for _, have := range spec.Transports {
				declared = declared || have == protocol
			}
			if !declared {
				gaps[id+"/"+string(protocol)] = fmt.Sprintf("%s: oracle serves %s at %s; Routeweft declares no such transport", lineOf(source, path, `baseUrl:`), protocol, transport.baseURL)
				continue
			}
			compared++
			if got := effectiveURL(spec, protocol); got != transport.baseURL {
				gaps[id+"/"+string(protocol)] = fmt.Sprintf("%s: oracle %s, Routeweft %s", lineOf(source, path, `baseUrl: "`+regexp.QuoteMeta(transport.baseURL)), transport.baseURL, got)
			}
		}
	}
	if compared == 0 {
		t.Fatal("compared no URL; the registry parse is broken")
	}
	ratchet(t, "upstream URLs", gaps, trackedURLGaps)
	ratchet(t, "unverified registry files (URL not compared)", unverified, trackedUnverifiedURLs)

	names := keys(unverified)
	sort.Strings(names)
	t.Logf("oracle %s: compared %d provider transports, %d URL gaps, %d providers UNVERIFIED (not compared): %v", o.pin[:8], compared, len(gaps), len(unverified), names)
	for _, id := range names {
		t.Logf("  unverified %s: %s", id, unverified[id])
	}
	for _, key := range keys(gaps) {
		t.Logf("  gap %s: %s", key, gaps[key])
	}
}
