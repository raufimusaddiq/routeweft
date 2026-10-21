package ingress

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// URLs come from each registry file at 1c3b9f47; the comparison also checks the shipped catalog base and endpoint.
var registryChatWires = []struct{ id, url string }{
	{"alicode-intl", "https://coding-intl.dashscope.aliyuncs.com/v1/chat/completions"},
	{"alicode", "https://coding.dashscope.aliyuncs.com/v1/chat/completions"},
	{"alims-intl", "https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions"},
	{"alitp-intl", "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1/chat/completions"},
	{"api-airforce", "https://api.airforce/v1/chat/completions"},
	{"baidu", "https://qianfan.baidubce.com/v2/chat/completions"},
	{"bazaarlink", "https://bazaarlink.ai/api/v1/chat/completions"},
	{"blackbox", "https://api.blackbox.ai/v1/chat/completions"},
	{"bluesminds", "https://api.bluesminds.com/v1/chat/completions"},
	{"byteplus", "https://ark.ap-southeast.bytepluses.com/api/coding/v3/chat/completions"},
	{"cerebras", "https://api.cerebras.ai/v1/chat/completions"},
	{"chutes", "https://llm.chutes.ai/v1/chat/completions"},
	{"cohere", "https://api.cohere.ai/v1/chat/completions"},
	{"deepseek", "https://api.deepseek.com/chat/completions"},
	{"featherless", "https://api.featherless.ai/v1/chat/completions"},
	{"fireworks", "https://api.fireworks.ai/inference/v1/chat/completions"},
	{"gitlab", "https://gitlab.com/api/v4/chat/completions"},
	{"glm-cn", "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions"},
	{"groq", "https://api.groq.com/openai/v1/chat/completions"},
	{"hyperbolic", "https://api.hyperbolic.xyz/v1/chat/completions"},
	{"kilo-gateway", "https://api.kilo.ai/api/gateway/chat/completions"},
	{"llm7", "https://api.llm7.io/v1/chat/completions"},
	{"mistral", "https://api.mistral.ai/v1/chat/completions"},
	{"morph", "https://api.morphllm.com/v1/chat/completions"},
	{"nebius", "https://api.studio.nebius.ai/v1/chat/completions"},
	{"nvidia", "https://integrate.api.nvidia.com/v1/chat/completions"},
	{"perplexity", "https://api.perplexity.ai/chat/completions"},
	{"poolside", "https://inference.poolside.ai/v1/chat/completions"},
	{"sambanova", "https://api.sambanova.ai/v1/chat/completions"},
	{"siliconflow", "https://api.siliconflow.com/v1/chat/completions"},
	{"tencent", "https://api.hunyuan.cloud.tencent.com/v1/chat/completions"},
	{"together", "https://api.together.xyz/v1/chat/completions"},
	{"tokenrouter", "https://api.tokenrouter.com/v1/chat/completions"},
	{"venice", "https://api.venice.ai/api/v1/chat/completions"},
	{"vercel-ai-gateway", "https://ai-gateway.vercel.sh/v1/chat/completions"},
	{"volcengine-ark", "https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions"},
	{"xai", "https://api.x.ai/v1/chat/completions"},
}

func TestRegistryChatWires(t *testing.T) {
	specs := map[string]registry.Spec{}
	for _, spec := range registry.Builtins() {
		specs[spec.ID] = spec
	}
	for _, tc := range registryChatWires {
		t.Run(tc.id, func(t *testing.T) {
			spec := specs[tc.id]
			endpoint := spec.TransportEndpoints[registry.TransportOpenAIChat]
			if endpoint == "" {
				endpoint = "chat/completions"
			}
			if got := strings.TrimRight(spec.DefaultBaseURL, "/") + "/" + endpoint; got != tc.url {
				t.Fatalf("catalog URL %s, pin %s", got, tc.url)
			}
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprint(stream), func(t *testing.T) {
					expected, _ := url.Parse(tc.url)
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, _ := io.ReadAll(r.Body)
						if r.URL.Path != expected.Path || r.Header.Get("Authorization") != "Bearer fixture-key" || !strings.Contains(string(body), `"model":"wire-model"`) || !strings.Contains(string(body), `"content":"hi"`) || !strings.Contains(string(body), `"future_field":"keep"`) {
							t.Errorf("wrong wire: %s %v %s", r.URL, r.Header, body)
						}
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n")
						} else {
							w.Header().Set("Content-Type", "application/json")
							io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`)
						}
					}))
					defer upstream.Close()
					catalogBase, _ := url.Parse(spec.DefaultBaseURL)
					ref := routing.ProviderRef{ProviderID: tc.id, Protocol: "openai-chat", APIToken: "fixture-key", BaseURL: upstream.URL + catalogBase.Path, Endpoint: endpoint, UpstreamModel: "wire-model"}
					body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"hi"}],"future_field":"keep","stream":%v}`, stream)
					out := send(t, ref, "/v1/chat/completions", body)
					if out.Code != 200 || !strings.Contains(out.Body.String(), "hello") {
						t.Fatalf("%d %s", out.Code, out.Body)
					}
					if stream && strings.Count(out.Body.String(), "[DONE]") != 1 {
						t.Fatalf("terminal: %s", out.Body)
					}
				})
			}
		})
	}
}
