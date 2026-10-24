package wire

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	codexprovider "github.com/raufimusaddiq/routeweft/internal/providers/codex"
	"github.com/raufimusaddiq/routeweft/internal/providers/oauthheaders"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Target is where a request goes. An empty field leaves what the route already
// decided; Query is encoded form values without the leading "?".
type Target struct {
	BaseURL, Endpoint, Query string
	EndpointCount            int // Number of Kiro surfaces; zero for providers without endpoint fallback.
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

// TargetError is a provider hook refusing a route: a required setting is
// missing, or the model has no endpoint on the wire the route carries. It says
// nothing about the account's health or the request, so a caller falls through to
// the next candidate and, if none serves, reports Err to the operator.
type TargetError struct{ Err error }

func (e *TargetError) Error() string { return e.Err.Error() }
func (e *TargetError) Unwrap() error { return e.Err }

// ResolveTarget applies a provider hook; without one it preserves the route.
// The optional attempt selects a Kiro surface, never an account retry.
func ResolveTarget(provider routing.ProviderRef, endpoint string, attempt ...int) (Target, error) {
	target := Target{BaseURL: provider.BaseURL, Endpoint: endpoint}
	hook := For(provider.ProviderID).Target
	if hook == nil {
		return target, nil
	}
	resolved, err := hook(provider)
	if err != nil {
		return Target{}, &TargetError{Err: err}
	}
	if resolved.BaseURL != "" {
		target.BaseURL = resolved.BaseURL
	}
	if resolved.Endpoint != "" {
		target.Endpoint = resolved.Endpoint
	}
	target.Query = resolved.Query
	if provider.ProviderID == "kiro" {
		index := 0
		if len(attempt) > 0 {
			index = attempt[0]
		}
		return kiroEndpoint(provider, index)
	}
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

// tokenplanTarget: Chat posts to {base}/chat/completions and Messages to
// {base minus /v1}/anthropic/v1/messages (executors/xiaomi-tokenplan.js:13-19).
// The region picks the official host from a closed set, and only when it is set
// (see tokenplanBase); a connection whose base URL is not an official Xiaomi host
// keeps its own base.
func tokenplanTarget(provider routing.ProviderRef) (Target, error) {
	base := tokenplanBase(provider)
	if provider.Protocol == anthropicadapter.MessagesProtocol {
		return Target{BaseURL: strings.TrimSuffix(base, "/v1"), Endpoint: "anthropic/v1/messages"}, nil
	}
	return Target{BaseURL: base, Endpoint: "chat/completions"}, nil
}

// tokenplanBase is the host a Token Plan key is served from. A Token Plan key is
// issued for one region, so the connection's own host is never replaced by a
// default: a region only overrides when it is set and names a known region
// (sgp, cn, ams) on an official Xiaomi host. With no region, or one that names
// none of them, the host the connection was configured with is kept (as
// {scheme}://{host}/v1); a base that is not an official Xiaomi host is kept as
// given.
//
// Divergence from the pin: config/providers.js:16-19 falls back to the default
// region for an unset or unknown one, which would send a CN or AMS key to the SGP
// host. An unset region on the catalog's own default base is the SGP host, as in
// the pin.
func tokenplanBase(provider routing.ProviderRef) string {
	base := strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
	parsed, err := url.Parse(base)
	if err != nil || !strings.HasSuffix(parsed.Hostname(), ".xiaomimimo.com") {
		return base
	}
	if regional, ok := tokenplanRegions[strings.TrimSpace(provider.Connection.Settings.Region)]; ok {
		return regional
	}
	return parsed.Scheme + "://" + parsed.Host + "/v1"
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

// codexHeaders adds the account binding Codex needs when several ChatGPT
// accounts are configured: ChatGPT-Account-ID from workspaceId, else
// chatgptAccountId, else the connection's account ID (executors/codex.js:234-240).
// Without it a request can bind to the wrong OpenAI account and surface as
// token_invalid after a second account is added.
func codexHeaders(provider routing.ProviderRef) map[string]string {
	headers := codexprovider.Headers()
	settings := provider.Connection.Settings
	if account := firstNonEmpty(settings.WorkspaceID, settings.ChatGPTAccountID, provider.Connection.AccountID); account != "" {
		headers["ChatGPT-Account-ID"] = account
	}
	return headers
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

var opencodeMuseSpark = regexp.MustCompile(`(?i)^muse[-_]?spark(?:$|[-_:.\s])`)

var opencodeThinkingSuffix = regexp.MustCompile(`\([^()]+\)\s*$`)

// OpenCode Zen (open-sse/executors/opencode.js:471-476). Every model is served
// under {base}/zen/v1, but the endpoint depends on the model: the Muse Spark
// models are on /responses and union-alpha is on /messages
// (executors/opencode.js:35-40,271-278, registry/opencode.js:35-37), the rest on
// /chat/completions.
//
// A Routeweft route carries one wire per request, picked from the node's
// transports and the client's protocol, never from the model, and opencode
// declares Chat only. A model that the pin serves on another wire would be sent
// a Chat body at the wrong endpoint, so it is refused here rather than guessed
// at; serving it needs a per-model transport, which is a registry change.
func opencodeTarget(provider routing.ProviderRef) (Target, error) {
	model := strings.TrimSpace(opencodeThinkingSuffix.ReplaceAllString(provider.UpstreamModel, ""))
	bare := model
	if i := strings.LastIndex(bare, "/"); i >= 0 {
		bare = bare[i+1:]
	}
	if opencodeMuseSpark.MatchString(bare) || model == "union-alpha" {
		return Target{}, errors.New("opencode model " + model + " is served on a Responses or Messages endpoint that needs a per-model transport; only Chat Completions models are routable")
	}
	return Target{Endpoint: "zen/v1/chat/completions"}, nil
}

// opencodeHeaders is the credential of OpenCode Zen. The provider has no
// account: the pin sends the literal public key on every request
// (executors/opencode.js:492) and on its own model listing and validation
// (src/app/api/providers/[id]/test/testUtils.js:767), and its no-auth connection
// carries accessToken "public" (src/sse/services/auth.js:94). Without it a
// request to the gateway has no Authorization header at all. A connection that
// does hold a token keeps it: the pin has no such connection, so there is
// nothing to match, and replacing a credential the operator set would be a guess. The session, request and project
// headers (:417-426) and the free-tier fingerprint tools (:455-464) are not carried
// (PARITY 6.19).
func opencodeHeaders(provider routing.ProviderRef) map[string]string {
	if strings.TrimSpace(provider.APIToken) != "" {
		return map[string]string{}
	}
	return map[string]string{"Authorization": "Bearer public"}
}

// vertexProject is a GCP project id as it appears in a path: a letter-led id, or
// a domain-scoped one ("example.com:project"). It is a path segment, so it is
// matched rather than spliced in raw.
var vertexProject = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// vertexPartnerTarget builds
// {base}/v1/projects/{projectId}/locations/global/endpoints/openapi/chat/completions
// (executors/vertex.js:76-81). The pin takes the project from the service
// account JSON, the ADC quota project or the connection's projectId
// (vertex.js:71-74); Routeweft reads only the last, the connection's projectId.
//
// Divergences from the pin: it probes the API with a dummy request to discover
// the project of a raw key (vertex.js:35-49,158-164), which Routeweft never
// does, and it appends a raw key as ?key= (vertex.js:80), which would put a
// credential in a URL. With no projectId the request is refused naming the
// setting.
func vertexPartnerTarget(provider routing.ProviderRef) (Target, error) {
	project := strings.TrimSpace(provider.Connection.Settings.ProjectID)
	if project == "" {
		return Target{}, errors.New("vertex-partner connection has no projectId: add a Project ID to the connection (Providers > New connection) or set providerData.projectId when creating it through the API")
	}
	if !vertexProject.MatchString(project) || strings.Contains(project, "..") {
		return Target{}, errors.New("vertex-partner projectId is not a valid GCP project id")
	}
	return Target{Endpoint: "v1/projects/" + project + "/locations/global/endpoints/openapi/chat/completions"}, nil
}

// CredentialError requires refreshing or reconnecting the selected account.
type CredentialError struct{ Err error }

func (e *CredentialError) Error() string { return e.Err.Error() }
func (e *CredentialError) Unwrap() error { return e.Err }

// ApplyClientRequestHeaders sets the headers a provider derives from the client's
// request. body is the one the client's identity is read from, before the
// provider's own body rewrites.
func ApplyClientRequestHeaders(provider routing.ProviderRef, request *http.Request, client http.Header, body []byte) {
	if hook := For(provider.ProviderID).ClientRequestHeaders; hook != nil {
		set(request.Header, hook(provider, client, body))
	}
}

// ApplyRequestHeaders runs after body normalization and URL resolution so signatures cover the wire bytes.
func ApplyRequestHeaders(provider routing.ProviderRef, request *http.Request, body []byte) error {
	if provider.ProviderID == "kiro" {
		request.Header.Del("X-Amz-Target")
		if strings.HasPrefix(request.URL.Hostname(), "codewhisperer.") {
			request.Header.Set("X-Amz-Target", "AmazonCodeWhispererStreamingService.GenerateAssistantResponse")
		}
	}
	if hook := For(provider.ProviderID).RequestHeaders; hook != nil {
		headers, err := hook(provider, body, request.URL.String())
		if err != nil {
			return err
		}
		set(request.Header, headers)
	}
	return nil
}
