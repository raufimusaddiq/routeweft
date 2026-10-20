package wire

import (
	"fmt"
	"net/http"
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
		if For(id).Target != nil {
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
		if id == "azure" {
			continue // its own api-version query is covered by TestAzureTargetCrossProduct
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
					host := map[string]string{"sgp": "sgp", "cn": "cn", "ams": "ams"}[strings.TrimSpace(region)] // "cn " is cn, not the default region
					if host == "" {
						host = "sgp"
					}
					wantBase := "https://token-plan-" + host + ".xiaomimimo.com/v1"
					if !official {
						wantBase = strings.TrimRight(base, "/")
					}
					if protocol == chat {
						if !official {
							wantBase = strings.TrimRight(base, "/")
						}
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
