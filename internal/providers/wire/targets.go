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
	base := strings.TrimRight(provider.BaseURL, "/")
	if parsed, err := url.Parse(base); err == nil && strings.HasSuffix(parsed.Hostname(), ".xiaomimimo.com") {
		base = tokenplanRegions[tokenplanDefaultRegion]
		if regional, ok := tokenplanRegions[provider.Connection.Settings.Region]; ok {
			base = regional
		}
	}
	if provider.Protocol == anthropicadapter.MessagesProtocol {
		return Target{BaseURL: strings.TrimSuffix(base, "/v1"), Endpoint: "anthropic/v1/messages"}, nil
	}
	return Target{BaseURL: base, Endpoint: "chat/completions"}, nil
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
