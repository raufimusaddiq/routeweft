package wire

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func ref(id, protocol, base string, settings routing.ProviderSettings) routing.ProviderRef {
	return routing.ProviderRef{ProviderID: id, Protocol: protocol, BaseURL: base, UpstreamModel: "model-x", APIToken: "tok",
		Connection: routing.ConnectionMetadata{Settings: settings}}
}

// Without a hook a request goes exactly where its route said.
func TestResolveTargetWithoutHookIsTheRoute(t *testing.T) {
	for _, id := range providerIDs() {
		if For(id).Target != nil || For(id).Route != nil {
			continue
		}
		got, err := ResolveTarget(ref(id, chat, "https://x.example/v1", routing.ProviderSettings{}), "chat/completions")
		if err != nil || got != (Target{BaseURL: "https://x.example/v1", Endpoint: "chat/completions"}) {
			t.Errorf("%s: %+v %v", id, got, err)
		}
	}
}

// open-sse/providers/registry/{claude,glm,kimi,minimax,minimax-cn}.js declare
// urlSuffix "?beta=true" on their Messages transport and on nothing else.
func TestBetaQueryIsMessagesOnlyAndOnlyForTheDeclaringProviders(t *testing.T) {
	declaring := map[string]bool{"claude": true, "glm": true, "kimi": true, "minimax": true, "minimax-cn": true}
	for _, id := range providerIDs() {
		if id == "azure" || id == "vertex-partner" || id == "kiro" || id == "cloudflare-ai" || For(id).Route != nil {
			continue // These providers require settings covered by their own target tests.
		}
		for _, protocol := range []string{chat, responses, messages} {
			got, err := ResolveTarget(ref(id, protocol, "https://x.example", routing.ProviderSettings{}), "messages")
			if err != nil {
				t.Fatalf("%s/%s: %v", id, protocol, err)
			}
			want := ""
			if declaring[id] && protocol == messages {
				want = "beta=true"
			}
			if got.Query != want {
				t.Errorf("%s/%s: query %q want %q", id, protocol, got.Query, want)
			}
		}
	}
}

func TestAzureTargetCrossProduct(t *testing.T) {
	bases := map[string]string{"plain": "https://acme.openai.azure.com", "trailing slash": "https://acme.openai.azure.com/", "path prefix": "https://gw.example/azure/"}
	deployments := map[string]string{"setting": "gpt4-prod", "model fallback": "", "blank setting": "   "}
	versions := map[string]string{"setting": "2025-01-01", "default": "", "needs escaping": "2024 02&x=1"}
	for baseName, base := range bases {
		for depName, deployment := range deployments {
			for verName, version := range versions {
				t.Run(baseName+"/"+depName+"/"+verName, func(t *testing.T) {
					got, err := ResolveTarget(ref("azure", chat, base, routing.ProviderSettings{Deployment: deployment, APIVersion: version}), "chat/completions")
					if err != nil {
						t.Fatal(err)
					}
					name := "model-x"
					if strings.TrimSpace(deployment) != "" {
						name = "gpt4-prod"
					}
					wantVersion := "2024-10-01-preview"
					switch verName {
					case "setting":
						wantVersion = "2025-01-01"
					case "needs escaping":
						wantVersion = "2024 02&x=1"
					}
					if got.BaseURL != strings.TrimRight(base, "/") || got.Endpoint != "openai/deployments/"+name+"/chat/completions" {
						t.Fatalf("target %+v", got)
					}
					values, err := parseQuery(got.Query)
					if err != nil || len(values) != 1 || values["api-version"] != wantVersion {
						t.Fatalf("query %q -> %v (%v), want only api-version=%q", got.Query, values, err, wantVersion)
					}
				})
			}
		}
	}
	// No endpoint: refuse rather than send an Azure key somewhere unnamed.
	if _, err := ResolveTarget(ref("azure", chat, "  ", routing.ProviderSettings{}), "x"); err == nil {
		t.Error("azure with no endpoint was routed")
	}
	// A deployment that is not a name never reaches the path.
	for _, bad := range []string{"a/b", "..", "a..b", "x y", "a?b", "a#b", "%2e%2e", "../../v1", "-lead"} {
		if _, err := ResolveTarget(ref("azure", chat, "https://acme.openai.azure.com", routing.ProviderSettings{Deployment: bad}), "x"); err == nil {
			t.Errorf("deployment %q was accepted", bad)
		}
	}
	// An upstream model is the deployment fallback and is held to the same rule.
	odd := ref("azure", chat, "https://acme.openai.azure.com", routing.ProviderSettings{})
	odd.UpstreamModel = "vendor/model"
	if _, err := ResolveTarget(odd, "x"); err == nil {
		t.Error("a model with a slash became a deployment path")
	}
}

func parseQuery(raw string) (map[string]string, error) {
	out := map[string]string{}
	request, err := http.NewRequest(http.MethodGet, "https://x/?"+raw, nil)
	if err != nil {
		return nil, err
	}
	for key, values := range request.URL.Query() {
		if len(values) != 1 {
			return nil, fmt.Errorf("repeated %s", key)
		}
		out[key] = values[0]
	}
	return out, nil
}

func TestTokenplanTargetCrossProduct(t *testing.T) {
	regions := map[string]string{"sgp": "sgp", "cn": "cn", "ams": "ams", "unset": "", "unknown": "mars", "padded": "cn "}
	bases := map[string]string{
		"default": "https://token-plan-sgp.xiaomimimo.com/v1", "default no v1": "https://token-plan-sgp.xiaomimimo.com",
		"other official": "https://token-plan-ams.xiaomimimo.com/v1/", "custom": "http://127.0.0.1:9999/v1", "custom no v1": "http://127.0.0.1:9999",
	}
	for regionName, region := range regions {
		for baseName, base := range bases {
			for _, protocol := range []string{chat, messages} {
				t.Run(regionName+"/"+baseName+"/"+protocol, func(t *testing.T) {
					got, err := ResolveTarget(ref("xiaomi-tokenplan", protocol, base, routing.ProviderSettings{Region: region}), "ignored")
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(got.BaseURL+"/"+got.Endpoint, "/v1/v1") || strings.HasSuffix(got.BaseURL, "/") {
						t.Fatalf("malformed target %+v", got)
					}
					official := strings.Contains(base, "xiaomimimo.com")
					trimmed := strings.TrimSpace(region)
					var wantBase string
					switch {
					case !official:
						wantBase = strings.TrimRight(base, "/") // a custom base is kept as given
					case trimmed == "sgp" || trimmed == "cn" || trimmed == "ams":
						wantBase = "https://token-plan-" + trimmed + ".xiaomimimo.com/v1" // an explicit region overrides
					default:
						// No region, or one that names none: the connection's own host.
						parsed, _ := url.Parse(base)
						wantBase = "https://" + parsed.Host + "/v1"
					}
					if protocol == chat {
						if got.BaseURL != wantBase || got.Endpoint != "chat/completions" {
							t.Fatalf("chat target %+v want base %s", got, wantBase)
						}
						return
					}
					if got.Endpoint != "anthropic/v1/messages" || got.BaseURL != strings.TrimSuffix(wantBase, "/v1") {
						t.Fatalf("messages target %+v want base %s", got, strings.TrimSuffix(wantBase, "/v1"))
					}
				})
			}
		}
	}
}

// A model listing is requested from serving's base; a provider with no listing
// has none, and the pin never lists Azure models.
func TestDiscoveryBase(t *testing.T) {
	for _, c := range []struct {
		name     string
		ref      routing.ProviderRef
		wantBase string
		wantOK   bool
	}{
		{"azure has no listing", ref("azure", chat, "https://acme.openai.azure.com", routing.ProviderSettings{}), "", false},
		{"tokenplan region", ref("xiaomi-tokenplan", chat, "https://token-plan-sgp.xiaomimimo.com/v1", routing.ProviderSettings{Region: "cn"}), "https://token-plan-cn.xiaomimimo.com/v1", true},
		{"tokenplan default", ref("xiaomi-tokenplan", chat, "https://token-plan-sgp.xiaomimimo.com/v1", routing.ProviderSettings{}), "https://token-plan-sgp.xiaomimimo.com/v1", true},
		{"tokenplan cn base, no region, keeps its host", ref("xiaomi-tokenplan", chat, "https://token-plan-cn.xiaomimimo.com/v1", routing.ProviderSettings{}), "https://token-plan-cn.xiaomimimo.com/v1", true},
		{"tokenplan ams base, unknown region, keeps its host", ref("xiaomi-tokenplan", chat, "https://token-plan-ams.xiaomimimo.com/v1", routing.ProviderSettings{Region: "mars"}), "https://token-plan-ams.xiaomimimo.com/v1", true},
		{"tokenplan custom base", ref("xiaomi-tokenplan", chat, "http://127.0.0.1:9/v1/", routing.ProviderSettings{Region: "cn"}), "http://127.0.0.1:9/v1", true},
		{"anyone else", ref("kimi", chat, "https://api.kimi.com/coding/v1", routing.ProviderSettings{}), "https://api.kimi.com/coding/v1", true},
	} {
		base, ok := DiscoveryBase(c.ref)
		if base != c.wantBase || ok != c.wantOK {
			t.Errorf("%s: %q %v want %q %v", c.name, base, ok, c.wantBase, c.wantOK)
		}
	}
}

// A Token Plan key is issued for one region: a CN or AMS connection with no region
// setting keeps its own host rather than being sent to the default one.
func TestTokenplanNeverMovesAKeyToAnotherRegionUnlessAsked(t *testing.T) {
	for _, host := range []string{"sgp", "cn", "ams"} {
		base := "https://token-plan-" + host + ".xiaomimimo.com/v1"
		for _, region := range []string{"", "  ", "mars", "CN"} {
			for _, protocol := range []string{chat, messages} {
				got, err := ResolveTarget(ref("xiaomi-tokenplan", protocol, base, routing.ProviderSettings{Region: region}), "x")
				if err != nil || !strings.HasPrefix(got.BaseURL, "https://token-plan-"+host+".xiaomimimo.com") {
					t.Errorf("base %s region %q %s: %+v %v", host, region, protocol, got, err)
				}
			}
		}
	}
}

// executors/opencode.js:471-476: chat models post to {base}/zen/v1/chat/completions.
func TestOpenCodeTargetIsTheZenChatEndpoint(t *testing.T) {
	for _, model := range []string{"big-pickle", "grok-code", "some/future-model(high)"} {
		provider := ref("opencode", chat, "https://opencode.ai", routing.ProviderSettings{})
		provider.UpstreamModel = model
		got, err := ResolveTarget(provider, "chat/completions")
		if err != nil || got != (Target{BaseURL: "https://opencode.ai", Endpoint: "zen/v1/chat/completions"}) {
			t.Errorf("%s: %+v %v", model, got, err)
		}
		u, err := got.URL(func(raw string) (*url.URL, error) { return url.Parse(raw) })
		if err != nil || u.String() != "https://opencode.ai/zen/v1/chat/completions" {
			t.Errorf("%s: url %v %v", model, u, err)
		}
	}
}

// The models the pin serves on /responses (muse-spark, executors/opencode.js:36-39,
// helpers.js:22-27) and /messages (union-alpha) are refused, not posted a Chat body.
func TestOpenCodeRefusesModelsServedOnAnotherWire(t *testing.T) {
	for _, model := range []string{"muse-spark-1.2-contributor-free", "muse-spark-1.3-contributor-free", "Muse_Spark(high)", "vendor/muse-spark-2", "union-alpha", "union-alpha(low)"} {
		provider := ref("opencode", chat, "https://opencode.ai", routing.ProviderSettings{})
		provider.UpstreamModel = model
		if got, err := ResolveTarget(provider, "chat/completions"); err == nil {
			t.Errorf("%s: routed to %+v", model, got)
		}
	}
	provider := ref("opencode", chat, "https://opencode.ai", routing.ProviderSettings{})
	provider.UpstreamModel = "museum-guide"
	if _, err := ResolveTarget(provider, "chat/completions"); err != nil {
		t.Errorf("museum-guide is a chat model: %v", err)
	}
}

func TestOpenCodeForcesStream(t *testing.T) {
	if !ForcesStream(routing.ProviderRef{ProviderID: "opencode"}) {
		t.Fatal("registry/opencode.js:20 forceStream")
	}
	body := Body{"stream": []byte("false")}
	if !For("opencode").Finalize(chat, body) || string(body["stream"]) != "true" {
		t.Fatalf("stream not forced: %s", body["stream"])
	}
}

// executors/vertex.js:76-81.
func TestVertexPartnerTargetUsesTheConnectionProject(t *testing.T) {
	provider := ref("vertex-partner", chat, "https://aiplatform.googleapis.com", routing.ProviderSettings{ProjectID: " my-proj-123 "})
	got, err := ResolveTarget(provider, "chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	u, err := got.URL(func(raw string) (*url.URL, error) { return url.Parse(raw) })
	want := "https://aiplatform.googleapis.com/v1/projects/my-proj-123/locations/global/endpoints/openapi/chat/completions"
	if err != nil || u.String() != want {
		t.Fatalf("url %v %v, want %s", u, err, want)
	}
	if u.RawQuery != "" || strings.Contains(u.String(), "tok") {
		t.Fatalf("credential or query leaked into the URL: %s", u)
	}
}

// No project: refuse and name the setting. Nothing is probed or defaulted.
func TestVertexPartnerRefusesWithoutAProject(t *testing.T) {
	for _, project := range []string{"", "   "} {
		_, err := ResolveTarget(ref("vertex-partner", chat, "https://aiplatform.googleapis.com", routing.ProviderSettings{ProjectID: project}), "chat/completions")
		if err == nil || !strings.Contains(err.Error(), "projectId") {
			t.Errorf("%q: %v", project, err)
		}
	}
}

func TestVertexPartnerRejectsAProjectThatIsNotAPathSegment(t *testing.T) {
	for _, project := range []string{"../x", "a/b", "a?b", "a#b", "a..b", ".hidden", "a b", "a%2fb"} {
		if got, err := ResolveTarget(ref("vertex-partner", chat, "https://aiplatform.googleapis.com", routing.ProviderSettings{ProjectID: project}), "chat/completions"); err == nil {
			t.Errorf("%q accepted: %+v", project, got)
		}
	}
}

// A hook refusal is a *TargetError, so a caller can fall through to the next
// candidate; a bad base is not one.
func TestHookRefusalIsATargetError(t *testing.T) {
	_, err := ResolveTarget(ref("vertex-partner", chat, "https://aiplatform.googleapis.com", routing.ProviderSettings{}), "chat/completions")
	var refused *TargetError
	if !errors.As(err, &refused) {
		t.Fatalf("want *TargetError, got %T %v", err, err)
	}
}

func TestClientRequestHeaderHooksAreExactlyTheDeclaredProviders(t *testing.T) {
	var got []string
	for _, id := range Registered() {
		if For(id).ClientRequestHeaders != nil {
			got = append(got, id)
		}
	}
	sort.Strings(got)
	if want := []string{"opencode-go", "zed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("client request header hooks %v, want %v", got, want)
	}
}

func TestRequestHeaderHooksAreExactlyTheDeclaredProviders(t *testing.T) {
	var got []string
	for _, id := range Registered() {
		if For(id).RequestHeaders != nil {
			got = append(got, id)
		}
	}
	sort.Strings(got)
	if want := []string{"antigravity", "codex", "cursor", "gemini-cli", "grok-cli", "opencode-go", "qoder", "vertex", "zed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("request header hooks %v, want %v", got, want)
	}
}

// OpenCode Zen has no account: every request carries the public key the pin
// sends (executors/opencode.js:492); a token the connection holds is kept.
func TestOpenCodeSendsThePublicKey(t *testing.T) {
	for token, want := range map[string]string{"": "Bearer public", "  ": "Bearer public", "held-key": "Bearer held-key"} {
		provider := ref("opencode", chat, "https://opencode.ai", routing.ProviderSettings{})
		provider.APIToken = token
		if got := OpenAIHeaders(provider).Get("Authorization"); got != want {
			t.Errorf("token %q: Authorization %q, want %q", token, got, want)
		}
	}
	provider := ref("opencode-go", chat, "https://opencode.ai/zen/go/v1", routing.ProviderSettings{})
	provider.APIToken = "go-key"
	if got := OpenAIHeaders(provider).Get("Authorization"); got != "Bearer go-key" {
		t.Errorf("opencode-go must keep its own key: %q", got)
	}
}
