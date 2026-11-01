package ingress

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// P08 breadth (#76): the providers whose pinned entry is a plain API-key wire.
// The shared transport table (TestRegistryChatWires) proves the routing seam;
// this test proves the provider's own pinned facts through the real dispatch
// path: its wire URL and bearer form, its pinned static catalog, and its pinned
// models listing. Each row names the provider, so a table entry disappearing is
// a missing provider, not a shared-path pass.
//
// Values come from LiteRouter main at 4279093457458e168a2431604935f0145f2e76ec
// (open-sse/providers/registry/<id>.js and
// src/app/api/providers/[id]/models/route.js). No request leaves the process.
type plainProviderPin struct {
	id           string
	protocol     string
	wireURL      string
	responsesURL string // a second pinned native wire, when the pin declares one
	listingURL   string // the pin's models URL; empty when the pin lists nothing
	models       []string
	upstream     map[string]string // catalog id -> id the provider's API accepts
}

var plainProviderPins = []plainProviderPin{
	{
		id: "alicode-intl", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://coding-intl.dashscope.aliyuncs.com/v1/chat/completions",
		listingURL: "https://coding-intl.dashscope.aliyuncs.com/v1/models",
		models:     []string{"qwen3.5-plus", "kimi-k2.5", "glm-5", "MiniMax-M2.5", "qwen3-coder-next", "qwen3-coder-plus", "glm-4.7"},
	},
	{
		id: "alitp-intl", protocol: openaiadapter.ChatProtocol,
		wireURL: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1/chat/completions",
		models:  []string{"qwen3.8-max-preview", "qwen3.7-max", "qwen3.7-plus", "qwen3.6-flash", "glm-5.2", "deepseek-v4-pro"},
	},
	{
		id: "alicode", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://coding.dashscope.aliyuncs.com/v1/chat/completions",
		listingURL: "https://coding.dashscope.aliyuncs.com/v1/models",
		models:     []string{"qwen3.5-plus", "kimi-k2.5", "glm-5", "MiniMax-M2.5", "qwen3-max-2026-01-23", "qwen3-coder-next", "qwen3-coder-plus", "glm-4.7"},
	},
	{
		id: "alims-intl", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions",
		listingURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1/models",
		models:     []string{"qwen3.5-plus", "kimi-k2.5", "glm-5", "MiniMax-M2.5", "qwen3-coder-next", "qwen3-coder-plus", "glm-4.7"},
	},
	{
		id: "baidu", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://qianfan.baidubce.com/v2/chat/completions",
		listingURL: "https://qianfan.baidubce.com/v2/models",
		models:     []string{"deepseek-v4-pro", "deepseek-v4-flash", "glm-5.2", "glm-5.1", "kimi-k2.6", "qwen3.5-397b-a17b", "qwen3.5-27b"},
	},
	{
		id: "bazaarlink", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://bazaarlink.ai/api/v1/chat/completions",
		listingURL: "https://bazaarlink.ai/api/v1/models",
		models: []string{
			"auto:free", "claude-opus-4.7", "claude-sonnet-4.6", "claude-haiku-4.5", "gpt-5.5", "gpt-5.4",
			"gpt-5.4-mini", "gpt-5.4-nano", "grok-4.3", "grok-4.20", "gemini-3.1-pro-preview", "gemini-3-flash-preview",
			"gemini-3.1-flash-lite-preview", "kimi-k2.6", "kimi-k2.5", "glm-5.1", "glm-5", "mimo-v2.5-pro", "mimo-v2.5",
			"minimax-m3", "minimax-m2.7", "minimax-m2.5", "qwen3.6-plus", "nemotron-3-super-120b-a12b",
		},
	},
	{
		id: "blackbox", protocol: openaiadapter.ChatProtocol,
		wireURL: "https://api.blackbox.ai/v1/chat/completions",
		models: []string{
			"claude-fable-5", "claude-opus-4.8", "claude-sonnet-4.6", "gpt-5.5", "gpt-5.4-pro",
			"gpt-5.4", "gpt-5.3-codex", "gpt-5.4-nano", "deepseek-v4-flash", "grok-4.3",
		},
		upstream: map[string]string{
			"claude-fable-5": "blackboxai/anthropic/claude-fable-5",
			"gpt-5.5":        "blackboxai/openai/gpt-5.5",
			"grok-4.3":       "blackboxai/x-ai/grok-4.3",
		},
	},
	{
		id: "bluesminds", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.bluesminds.com/v1/chat/completions",
		listingURL: "https://api.bluesminds.com/v1/models",
		models: []string{
			"gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "claude-sonnet-4-5", "claude-haiku-4-5",
			"gemini-2.0-flash", "gemini-2.0-flash-exp", "qwen-turbo", "kimi-k2", "kimi-k2-thinking",
			"glm-4.7", "minimax-m2.5", "claude-opus-4-5", "gemini-2.5-pro",
		},
	},
	{
		id: "byteplus", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://ark.ap-southeast.bytepluses.com/api/coding/v3/chat/completions",
		listingURL: "https://ark.ap-southeast.bytepluses.com/api/coding/v3/models",
		models:     []string{"seed-2-0-pro-260328", "seed-2-0-code-preview-260328", "seed-2-0-mini-260215", "seed-2-0-lite-260228", "kimi-k2-thinking-251104", "glm-4-7-251222", "gpt-oss-120b-250805"},
	},
	{
		id: "cerebras", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.cerebras.ai/v1/chat/completions",
		listingURL: "https://api.cerebras.ai/v1/models",
		models:     []string{"gpt-oss-120b", "zai-glm-4.7", "llama-3.3-70b", "llama-4-scout-17b-16e-instruct", "qwen-3-235b-a22b-instruct-2507", "qwen-3-32b", "qwen-3.8-27b"},
	},
	{
		id: "chutes", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://llm.chutes.ai/v1/chat/completions",
		listingURL: "https://llm.chutes.ai/v1/models",
	},
	{
		id: "cohere", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.cohere.ai/v1/chat/completions",
		listingURL: "https://api.cohere.ai/v1/models",
		models:     []string{"command-r-plus-08-2024", "command-r-08-2024", "command-a-03-2025"},
	},
	{
		id: "featherless", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.featherless.ai/v1/chat/completions",
		listingURL: "https://api.featherless.ai/v1/models",
		models:     []string{"deepseek-ai/DeepSeek-V4-Pro", "deepseek-ai/DeepSeek-V4-Flash", "zai-org/GLM-5.2", "zai-org/GLM-5.1", "moonshotai/Kimi-K2.7-Code", "moonshotai/Kimi-K2.6", "moonshotai/Kimi-K2.5"},
	},
	{
		id: "fireworks", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.fireworks.ai/inference/v1/chat/completions",
		listingURL: "https://api.fireworks.ai/inference/v1/models",
		models:     []string{"accounts/fireworks/models/deepseek-v3p1", "accounts/fireworks/models/llama-v3p3-70b-instruct", "accounts/fireworks/models/qwen3-235b-a22b"},
	},
	{
		id: "hyperbolic", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.hyperbolic.xyz/v1/chat/completions",
		listingURL: "https://api.hyperbolic.xyz/v1/models",
		models:     []string{"Qwen/QwQ-32B", "deepseek-ai/DeepSeek-R1", "deepseek-ai/DeepSeek-V3", "meta-llama/Llama-3.3-70B-Instruct", "meta-llama/Llama-3.2-3B-Instruct", "Qwen/Qwen2.5-72B-Instruct", "Qwen/Qwen2.5-Coder-32B-Instruct", "NousResearch/Hermes-3-Llama-3.1-70B"},
	},
	{
		id: "kilo-gateway", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.kilo.ai/api/gateway/chat/completions",
		listingURL: "https://api.kilo.ai/api/gateway/models",
		models:     []string{"kilo-auto/free", "nvidia/nemotron-3-super-120b-a12b:free", "nvidia/nemotron-3-ultra-550b-a55b:free", "kwaipilot/kat-coder-pro-v2.5:free", "kilo-auto/frontier", "kilo-auto/balanced"},
	},
	{
		id: "llm7", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.llm7.io/v1/chat/completions",
		listingURL: "https://api.llm7.io/v1/models",
		models:     []string{"gpt-5.5", "claude-opus-5", "deepseek-v4-flash", "grok-4.5", "kimi-k3"},
	},
	{
		id: "morph", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.morphllm.com/v1/chat/completions",
		listingURL: "https://api.morphllm.com/v1/models",
		models:     []string{"morph-v3-large", "morph-v3-fast", "morph-qwen35-397b", "morph-minimax27-230b", "morph-qwen36-27b", "morph-dsv4flash"},
	},
	{
		id: "nebius", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.studio.nebius.ai/v1/chat/completions",
		listingURL: "https://api.studio.nebius.ai/v1/models",
		models:     []string{"meta-llama/Llama-3.3-70B-Instruct"},
	},
	{
		id: "nvidia", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://integrate.api.nvidia.com/v1/chat/completions",
		listingURL: "https://integrate.api.nvidia.com/v1/models",
		models:     []string{"minimaxai/minimax-m2.7", "minimaxai/minimax-m3", "z-ai/glm-5.2", "deepseek-ai/deepseek-v4-pro", "deepseek-ai/deepseek-v4-flash", "moonshotai/kimi-k2.6", "nvidia/nemotron-3-ultra-550b-a55b"},
	},
	{
		id: "poolside", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://inference.poolside.ai/v1/chat/completions",
		listingURL: "https://inference.poolside.ai/v1/models",
		models:     []string{"poolside/laguna-s-2.1", "poolside/laguna-xs-2.1", "poolside/laguna-m.1"},
	},
	{
		id: "sambanova", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.sambanova.ai/v1/chat/completions",
		listingURL: "https://api.sambanova.ai/v1/models",
		models:     []string{"MiniMax-M2.7"},
	},
	{
		id: "siliconflow", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.siliconflow.com/v1/chat/completions",
		listingURL: "https://api.siliconflow.com/v1/models",
		models: []string{
			"deepseek-ai/DeepSeek-V4-Pro", "deepseek-ai/DeepSeek-V4-Flash", "deepseek-ai/DeepSeek-V3.2",
			"deepseek-ai/DeepSeek-V3.2-Exp", "deepseek-ai/DeepSeek-V3.1", "deepseek-ai/DeepSeek-V3.1-Terminus",
			"deepseek-ai/DeepSeek-R1", "Qwen/Qwen3.5-397B-A17B", "Qwen/Qwen3.5-122B-A10B", "zai-org/GLM-5.1",
			"zai-org/GLM-5", "moonshotai/Kimi-K2.6", "moonshotai/Kimi-K2.5", "openai/gpt-oss-120b",
			"MiniMaxAI/MiniMax-M2.5", "inclusionAI/Ling-flash-2.0",
		},
	},
	{
		id: "tencent", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.hunyuan.cloud.tencent.com/v1/chat/completions",
		listingURL: "https://api.hunyuan.cloud.tencent.com/v1/models",
		models:     []string{"hunyuan-turbos-latest", "hunyuan-t1-latest"},
	},
	{
		id: "together", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.together.xyz/v1/chat/completions",
		listingURL: "https://api.together.xyz/v1/models",
		models:     []string{"meta-llama/Llama-3.3-70B-Instruct-Turbo", "deepseek-ai/DeepSeek-R1", "Qwen/Qwen3-235B-A22B", "meta-llama/Llama-4-Maverick-17B-128E-Instruct-FP8"},
	},
	{
		id: "venice", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.venice.ai/api/v1/chat/completions",
		listingURL: "https://api.venice.ai/api/v1/models",
		models:     []string{"venice-uncensored-1-2", "zai-org-glm-5", "qwen3-235b-a22b-instruct-2507", "qwen3-coder-480b-a35b-instruct-turbo", "qwen3-vl-235b-a22b", "deepseek-v4-pro", "llama-3.3-70b", "hermes-3-llama-3.1-405b", "mistral-small-3-2-24b-instruct"},
	},
	{
		id: "volcengine-ark", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions",
		listingURL: "https://ark.cn-beijing.volces.com/api/coding/v3/models",
		models:     []string{"Doubao-Seed-2.0-Code", "Doubao-Seed-2.0-pro", "Doubao-Seed-2.0-lite", "Doubao-Seed-Code", "DeepSeek-V4-Flash", "DeepSeek-V4-Pro", "GLM-5.1", "MiniMax-M2.7", "Kimi-K2.6"},
	},
	{
		id: "openai", protocol: openaiadapter.ChatProtocol,
		wireURL:      "https://api.openai.com/v1/chat/completions",
		responsesURL: "https://api.openai.com/v1/responses",
		listingURL:   "https://api.openai.com/v1/models",
		models: []string{
			"gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna", "gpt-6-astra", "gpt-5.6", "gpt-5.5", "gpt-5.5-pro",
			"gpt-5.4-pro", "gpt-5.2-pro", "gpt-5.3-codex", "gpt-5.3-codex-spark", "gpt-5.4", "gpt-5.4-mini",
			"gpt-5.4-nano", "gpt-5.2", "gpt-5.1", "gpt-5", "gpt-5-mini", "gpt-5-nano", "gpt-4o", "gpt-4o-mini",
			"gpt-4-turbo", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano", "o3", "o3-mini", "o3-pro", "o4-mini",
			"o1", "o1-mini",
		},
	},
	{
		id: "tokenrouter", protocol: openaiadapter.ChatProtocol,
		wireURL:    "https://api.tokenrouter.com/v1/chat/completions",
		listingURL: "https://api.tokenrouter.com/v1/models",
		models: []string{
			"anthropic/claude-haiku-4.5", "anthropic/claude-sonnet-4.6", "anthropic/claude-opus-4.8",
			"anthropic/claude-opus-4.8-fast", "openai/gpt-5.4", "openai/gpt-5.4-mini", "openai/gpt-5.4-pro",
			"openai/gpt-5.5", "openai/gpt-5.6-sol", "google/gemini-3.5-flash", "google/gemini-3.6-flash",
			"deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-pro", "qwen/qwen3-coder-next", "qwen/qwen3.7-max",
			"qwen/qwen3.8-max", "moonshotai/kimi-k2.7-code", "moonshotai/kimi-k3-free", "z-ai/glm-5.3-free",
			"z-ai/glm-5.2", "z-ai/glm-5-turbo", "x-ai/grok-4.5",
		},
	},
	{
		id: "muse", protocol: openaiadapter.ResponsesProtocol,
		wireURL: "https://api.meta.ai/v1/responses",
		models:  []string{"muse-spark-1.2-contributor", "muse-spark-1.3-contributor"},
	},
	{
		id: "xai", protocol: openaiadapter.ChatProtocol,
		wireURL:      "https://api.x.ai/v1/chat/completions",
		responsesURL: "https://api.x.ai/v1/responses",
		listingURL:   "https://api.x.ai/v1/models",
		models:       []string{"grok-4.7", "grok-4.3", "grok-build-0.1", "grok-4.6", "grok-4.5", "grok-4", "grok-4-fast-reasoning", "grok-code-fast-1", "grok-3"},
	},
}

func TestPinnedPlainProviderWires(t *testing.T) {
	specs := map[string]registry.Spec{}
	for _, spec := range registry.Builtins() {
		specs[spec.ID] = spec
	}
	if len(plainProviderPins) != 31 {
		t.Fatalf("plain-provider table has %d rows, want the 31 shared-path providers", len(plainProviderPins))
	}
	for _, tc := range plainProviderPins {
		t.Run(tc.id, func(t *testing.T) {
			spec, ok := specs[tc.id]
			if !ok {
				t.Fatalf("%s is not a built-in provider", tc.id)
			}
			if spec.Auth != registry.AuthAPIKey {
				t.Errorf("auth = %q, want the pin's API key", spec.Auth)
			}
			if _, ok := spec.NativeBinding(tc.protocol); !ok {
				t.Errorf("transports = %v, missing %s", spec.Transports, tc.protocol)
			}
			endpoint := "chat/completions"
			if tc.protocol == openaiadapter.ResponsesProtocol {
				endpoint = "responses"
			}
			if declared, ok := spec.EndpointFor(registry.Protocol(tc.protocol)); ok {
				endpoint = declared
			}
			if got := strings.TrimRight(spec.DefaultBaseURL, "/") + "/" + endpoint; got != tc.wireURL {
				t.Fatalf("wire URL %s, pin %s", got, tc.wireURL)
			}
			if tc.responsesURL != "" {
				if got := strings.TrimRight(spec.DefaultBaseURL, "/") + "/responses"; got != tc.responsesURL {
					t.Errorf("Responses URL %s, pin %s", got, tc.responsesURL)
				}
				if _, ok := spec.NativeBinding(openaiadapter.ResponsesProtocol); !ok {
					t.Errorf("transports = %v, missing the pinned Responses wire", spec.Transports)
				}
			}
			if got := seedIDs(spec); !sameStrings(got, tc.models) {
				t.Errorf("seeded catalog %v, pin %v", got, tc.models)
			}
			if tc.listingURL != "" {
				got, err := wire.ListingURL(url.Parse, spec.DefaultBaseURL, "", spec.DiscoveryPath, "")
				if err != nil || got != tc.listingURL {
					t.Errorf("listing URL %s (%v), pin %s", got, err, tc.listingURL)
				}
			}
			for catalog, upstream := range tc.upstream {
				if got := registry.UpstreamModelID(tc.id, catalog); got != upstream {
					t.Errorf("upstream id for %s = %s, pin %s", catalog, got, upstream)
				}
			}
			model := "pin-model"
			if len(tc.models) > 0 {
				model = tc.models[0]
			}
			for _, stream := range []bool{false, true} {
				name := "nonstream"
				if stream {
					name = "stream"
				}
				t.Run(name, func(t *testing.T) {
					upstreamBody, upstreamPath, upstreamAuth := "", "", ""
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						raw, _ := io.ReadAll(r.Body)
						upstreamBody, upstreamPath, upstreamAuth = string(raw), r.URL.Path, r.Header.Get("Authorization")
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, streamAnswer(tc.protocol))
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, bufferedAnswer(tc.protocol))
					}))
					defer upstream.Close()
					path, body := "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"future_field":"keep","stream":%v}`, model, stream)
					if tc.protocol == openaiadapter.ResponsesProtocol {
						path, body = "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hi","future_field":"keep","stream":%v}`, model, stream)
					}
					wantPath, _ := url.Parse(tc.wireURL)
					ref := routing.ProviderRef{
						ProviderID: tc.id, Protocol: tc.protocol, BaseURL: upstream.URL,
						Endpoint: strings.TrimPrefix(wantPath.Path, "/"), UpstreamModel: model, APIToken: "fixture-key",
					}
					out := send(t, ref, path, body)
					if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), "hello") {
						t.Fatalf("status %d body %s", out.Code, out.Body)
					}
					if upstreamPath != wantPath.Path {
						t.Errorf("upstream path %s, pin %s", upstreamPath, wantPath.Path)
					}
					if upstreamAuth != "Bearer fixture-key" {
						t.Errorf("auth %q, want the pin's bearer", upstreamAuth)
					}
					if !strings.Contains(upstreamBody, `"model":"`+model+`"`) || !strings.Contains(upstreamBody, `"future_field":"keep"`) {
						t.Errorf("upstream body %s", upstreamBody)
					}
				})
			}
		})
	}
}

func seedIDs(spec registry.Spec) []string {
	ids := make([]string, len(spec.SeedModels))
	for i, model := range spec.SeedModels {
		ids[i] = model.ID
	}
	return ids
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func streamAnswer(protocol string) string {
	if protocol == openaiadapter.ResponsesProtocol {
		return "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
	}
	return "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"
}

func bufferedAnswer(protocol string) string {
	if protocol == openaiadapter.ResponsesProtocol {
		return `{"id":"r1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}`
	}
	return `{"id":"c1","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
}
