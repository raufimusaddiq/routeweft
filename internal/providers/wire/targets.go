package wire

import (
	"errors"
	"net/url"
	"regexp"
	"strings"

	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/providers/oauthheaders"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Target is where a request goes. An empty field leaves what the route already
// decided; Query is encoded form values without the leading "?".
type Target struct {
	BaseURL, Endpoint, Query string
}

// NodeBase is the one rule for the base URL a connection is dialled at: the
// node's own, else the catalog default, else the base the connection names
// (azureEndpoint, then baseUrl; an Azure node declares none). Serving, probes,
// model discovery and route listings all take their base from here, so what a
// probe tests is what a request would reach. Empty means the connection is not
// dialable.
func NodeBase(nodeBase, specDefault string, settings routing.ProviderSettings) string {
	for _, candidate := range []string{nodeBase, specDefault, settings.AzureEndpoint, settings.BaseURL} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

// DiscoveryBase is the base a model listing is requested from: the route's base
// as the provider's Base hook sees it (a Token Plan region host), or the route's
// own. ok is false for a provider that has no model listing at all (Azure), so
// the caller lists nothing rather than request a URL the provider does not serve.
func DiscoveryBase(provider routing.ProviderRef) (base string, ok bool) {
	hook := For(provider.ProviderID)
	if hook.Unlisted {
		return "", false
	}
	if hook.Base != nil {
		return hook.Base(provider), true
	}
	return provider.BaseURL, true
}

// URL validates the target's base with validate (the SSRF policy of the caller),
// appends the endpoint, and sets the provider's query. It is the only place an
// upstream URL is assembled.
func (t Target) URL(validate func(string) (*url.URL, error)) (*url.URL, error) {
	base, err := validate(t.BaseURL)
	if err != nil {
		return nil, err
	}
	if t.Endpoint != "" {
		if strings.HasPrefix(t.Endpoint, "/") || strings.ContainsAny(t.Endpoint, "?#") {
			return nil, errors.New("provider endpoint must be a relative path without query or fragment")
		}
		for _, segment := range strings.Split(t.Endpoint, "/") {
			if segment == "." || segment == ".." {
				return nil, errors.New("provider endpoint must not traverse paths")
			}
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/" + t.Endpoint
	}
	base.RawPath = ""
	if t.Query != "" {
		// The provider's own query: a declared suffix such as ?beta=true, or
		// Azure's api-version. A base URL cannot carry one (the validators
		// reject it), so nothing here can be smuggled in from configuration.
		base.RawQuery = t.Query
	}
	return base, nil
}

// ResolveTarget applies the provider's Target hook to the route's own base URL
// and endpoint. A provider with no hook is sent exactly where the route says.
func ResolveTarget(provider routing.ProviderRef, endpoint string) (Target, error) {
	target := Target{BaseURL: provider.BaseURL, Endpoint: endpoint}
	hook := For(provider.ProviderID).Target
	if hook == nil {
		return target, nil
	}
	resolved, err := hook(provider)
	if err != nil {
		return Target{}, err
	}
	if resolved.BaseURL != "" {
		target.BaseURL = resolved.BaseURL
	}
	if resolved.Endpoint != "" {
		target.Endpoint = resolved.Endpoint
	}
	target.Query = resolved.Query
	return target, nil
}

// betaQuery: the reference declares urlSuffix "?beta=true" on every Anthropic
// Messages transport of claude, glm, kimi, minimax and minimax-cn
// (open-sse/providers/registry/claude.js:23, glm.js:21,42, kimi.js:29,51,
// minimax.js:21,52, minimax-cn.js:21,52; appended at executors/default.js:154).
func betaQuery(provider routing.ProviderRef) (Target, error) {
	if provider.Protocol == anthropicadapter.MessagesProtocol {
		return Target{Query: "beta=true"}, nil
	}
	return Target{}, nil
}

// Azure OpenAI (open-sse/executors/azure.js:8-52).
const azureDefaultAPIVersion = "2024-10-01-preview"

var azureDeployment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// azureTarget builds {endpoint}/openai/deployments/{deployment}/chat/completions
// ?api-version={version} (azure.js:8-24). The endpoint is the connection's own
// azureEndpoint (the runtime copies it into the route's base URL).
//
// Divergences from the pin, both so a credential is never sent somewhere the
// operator did not name: with no endpoint the pin falls back to AZURE_ENDPOINT
// and then to https://api.openai.com (azure.js:9-11), which would send an Azure
// key to OpenAI; Routeweft refuses. The deployment is a path segment, so it must
// look like a deployment name rather than be spliced in raw (azure.js:23).
func azureTarget(provider routing.ProviderRef) (Target, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	if endpoint == "" {
		return Target{}, errors.New("azure connection has no azureEndpoint")
	}
	settings := provider.Connection.Settings
	deployment := firstNonEmpty(settings.Deployment, provider.UpstreamModel)
	if !azureDeployment.MatchString(deployment) || strings.Contains(deployment, "..") {
		return Target{}, errors.New("azure deployment name is not valid")
	}
	version := firstNonEmpty(settings.APIVersion, azureDefaultAPIVersion)
	return Target{
		BaseURL:  endpoint,
		Endpoint: "openai/deployments/" + deployment + "/chat/completions",
		Query:    url.Values{"api-version": {version}}.Encode(),
	}, nil
}

// azureHeaders: the credential travels as `api-key`, not a bearer token, and
// OpenAI-Organization is sent when the connection names one (azure.js:32-45).
func azureHeaders(provider routing.ProviderRef) map[string]string {
	headers := map[string]string{}
	if provider.APIToken != "" {
		headers["api-key"] = provider.APIToken
	}
	if organization := strings.TrimSpace(provider.Connection.Settings.Organization); organization != "" {
		headers["OpenAI-Organization"] = organization
	}
	return headers
}

// Xiaomi Token Plan keys are region-specific (config/providers.js:13-19,
// registry/xiaomi-tokenplan.js regions/defaultRegion).
var tokenplanRegions = map[string]string{
	"sgp": "https://token-plan-sgp.xiaomimimo.com/v1",
	"cn":  "https://token-plan-cn.xiaomimimo.com/v1",
	"ams": "https://token-plan-ams.xiaomimimo.com/v1",
}

const tokenplanDefaultRegion = "sgp"

// tokenplanTarget: Chat posts to {base}/chat/completions and Messages to
// {base minus /v1}/anthropic/v1/messages (executors/xiaomi-tokenplan.js:13-19).
// The region picks the official host from a closed set (an unknown region falls
// back to the default region, as config/providers.js:18 does); a connection whose
// base URL is not an official Xiaomi host keeps its own base.
func tokenplanTarget(provider routing.ProviderRef) (Target, error) {
	base := tokenplanBase(provider)
	if provider.Protocol == anthropicadapter.MessagesProtocol {
		return Target{BaseURL: strings.TrimSuffix(base, "/v1"), Endpoint: "anthropic/v1/messages"}, nil
	}
	return Target{BaseURL: base, Endpoint: "chat/completions"}, nil
}

// tokenplanBase is the host a Token Plan key is served from.
func tokenplanBase(provider routing.ProviderRef) string {
	base := strings.TrimRight(provider.BaseURL, "/")
	if parsed, err := url.Parse(base); err == nil && strings.HasSuffix(parsed.Hostname(), ".xiaomimimo.com") {
		base = tokenplanRegions[tokenplanDefaultRegion]
		if regional, ok := tokenplanRegions[strings.TrimSpace(provider.Connection.Settings.Region)]; ok {
			base = regional
		}
	}
	return base
}

// kimiDeviceHeaders: every Kimi transport runs the kimiHeaders hook
// (registry/kimi.js:38,46,53,59; executors/default.js:66). The device id is the
// connection's own, stable for its OAuth session. Identity is Routeweft's, as in
// onboarding and usage (see oauthheaders.KimiDevice).
func kimiDeviceHeaders(provider routing.ProviderRef) map[string]string {
	return oauthheaders.KimiDevice(provider.Connection.Settings.DeviceID)
}

// kilocodeOrgHeaders: X-Kilocode-OrganizationID from the connection's orgId
// (executors/default.js:68, registry/kilocode.js:24-26).
func kilocodeOrgHeaders(provider routing.ProviderRef) map[string]string {
	if org := strings.TrimSpace(provider.Connection.Settings.OrgID); org != "" {
		return map[string]string{"X-Kilocode-OrganizationID": org}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
