package registry

import "testing"

func TestProviderIdentityBindsNativeProtocolsAndCopiesTransports(t *testing.T) {
	catalog, err := NewCatalog([]Spec{{ID: "deepseek", Transports: []Protocol{TransportOpenAIChat, TransportAnthropic}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.deepseek.com", ModelCatalog: CatalogStatic, Quirks: []Quirk{QuirkCacheControl}}})
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := catalog.Lookup("deepseek")
	if !ok || catalog.Len() != 1 {
		t.Fatalf("catalog lookup %v %d", ok, catalog.Len())
	}
	if transport, ok := spec.NativeBinding("anthropic-messages"); !ok || transport != TransportAnthropic {
		t.Fatalf("native binding %q %v", transport, ok)
	}
	spec.Transports[0] = "mutated"
	again, _ := catalog.Lookup("deepseek")
	if again.Transports[0] != TransportOpenAIChat {
		t.Fatal("catalog transport slice leaked")
	}
	spec.Quirks[0] = "mutated"
	again, _ = catalog.Lookup("deepseek")
	if again.Quirks[0] != QuirkCacheControl {
		t.Fatal("catalog quirk slice leaked")
	}
}

func TestProviderCatalogRejectsInvalidAndDuplicateEntries(t *testing.T) {
	if _, err := NewCatalog([]Spec{{ID: "bad", Auth: AuthAPIKey}}); err == nil {
		t.Fatal("accepted provider without transport")
	}
	spec := Spec{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthNone, ModelCatalog: CatalogStatic}
	if _, err := NewCatalog([]Spec{spec, spec}); err == nil {
		t.Fatal("accepted duplicate provider")
	}
	for _, invalid := range []Spec{
		{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthNone},
		{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthNone, ModelCatalog: "bogus"},
		{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthNone, ModelCatalog: CatalogStatic, PassthroughModels: true},
	} {
		if _, err := NewCatalog([]Spec{invalid}); err == nil {
			t.Errorf("accepted invalid spec %+v", invalid)
		}
	}
}

func TestBuiltinOpenAIAPIKeyProviderGroupMatchesBaselineCapabilities(t *testing.T) {
	catalog, err := NewBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{
		"antigravity", "gemini-cli", "cursor", "qoder", "kiro", "vertex", "commandcode", "grok-cli", "grok-web", "perplexity-web", "kenari", "zed", "kimchi", "azure",
		"xai", "github", "gitlab", "iflow", "kimi", "xiaomi-mimo", "cline", "clinepass", "kilocode", "codebuddy-cn", "codebuddy-intl",
		"vertex-partner", "tokenrouter", "perplexity-agent", "opencode-go", "cloudflare-ai",
		"mimo-free", "mmf", "opencode", "ollama", "ollama-local", "typesafe",
		"deepseek", "glm", "glm-cn", "minimax", "minimax-cn", "xiaomi-tokenplan",
		"claude",
		"gemini",
		"anthropic",
		"alicode-intl", "alicode", "alims-intl", "alitp-intl", "api-airforce", "baidu", "bazaarlink", "blackbox", "bluesminds", "byteplus",
		"cerebras", "chutes", "cohere", "featherless", "fireworks", "groq", "hyperbolic", "kilo-gateway", "llm7", "mistral", "morph", "nebius",
		"codex", "nvidia", "openai", "openrouter", "perplexity", "poolside", "sambanova", "siliconflow", "tencent", "together", "venice", "vercel-ai-gateway", "volcengine-ark",
	}
	if catalog.Len() != len(wantIDs) {
		t.Fatalf("catalog size=%d want=%d", catalog.Len(), len(wantIDs))
	}
	for _, id := range wantIDs {
		spec, ok := catalog.Lookup(id)
		// azure resolves its endpoint from operator connection data, so its
		// seeded origin is intentionally empty.
		if !ok || len(spec.Transports) == 0 || (spec.DefaultBaseURL == "" && id != "azure") {
			t.Errorf("incomplete group member %q: %+v present=%v", id, spec, ok)
		}
	}
	if spec, _ := catalog.Lookup("codex"); spec.Auth != AuthOAuth || spec.Transports[0] != TransportOpenAIResponses || !spec.ReportsUsage || len(spec.StaticModels) != 18 {
		t.Fatalf("codex spec=%+v", spec)
	}
	if spec, _ := catalog.Lookup("openrouter"); spec.ModelCatalog != CatalogDynamic || !spec.PassthroughModels {
		t.Fatalf("openrouter catalog semantics=%+v", spec)
	}
	if spec, _ := catalog.Lookup("llm7"); spec.ModelCatalog != CatalogPassthrough || !spec.PassthroughModels {
		t.Fatalf("llm7 catalog semantics=%+v", spec)
	}
	if spec, _ := catalog.Lookup("vercel-ai-gateway"); !spec.ReportsUsage {
		t.Fatalf("Vercel usage capability missing: %+v", spec)
	}
	openai, _ := catalog.Lookup("openai")
	if len(openai.Transports) != 2 || openai.Transports[0] != TransportOpenAIChat || openai.Transports[1] != TransportOpenAIResponses || len(openai.StaticModels) != 20 {
		t.Fatalf("OpenAI native capabilities=%+v", openai)
	}
	if transport, ok := openai.NativeBinding("openai-responses"); !ok || transport != TransportOpenAIResponses {
		t.Fatalf("OpenAI Responses binding=%q ok=%v", transport, ok)
	}
	if transport, ok := openai.NativeBinding("openai-chat"); !ok || transport != TransportOpenAIChat {
		t.Fatalf("OpenAI Chat binding=%q ok=%v", transport, ok)
	}
	if spec, _ := catalog.Lookup("groq"); !spec.ReportsUsage {
		t.Fatalf("Groq usage capability missing: %+v", spec)
	}
	if spec, _ := catalog.Lookup("anthropic"); spec.Auth != AuthAPIKey || spec.Transports[0] != TransportAnthropic || len(spec.StaticModels) != 3 {
		t.Fatalf("anthropic spec=%+v", spec)
	}
	if spec, _ := catalog.Lookup("gemini"); spec.Auth != AuthAPIKey || spec.Transports[0] != TransportGemini || len(spec.StaticModels) != 11 {
		t.Fatalf("gemini spec=%+v", spec)
	}
	if spec, _ := catalog.Lookup("claude"); spec.Auth != AuthOAuth || spec.Transports[0] != TransportAnthropic || !spec.ReportsUsage || len(spec.StaticModels) != 5 {
		t.Fatalf("claude spec=%+v", spec)
	}
	for _, id := range []string{"deepseek", "glm", "minimax", "minimax-cn", "xiaomi-tokenplan"} {
		spec, _ := catalog.Lookup(id)
		if len(spec.Transports) != 2 || spec.Transports[0] != TransportOpenAIChat || spec.Transports[1] != TransportAnthropic {
			t.Errorf("multi-transport %s spec=%+v", id, spec)
		}
		if transport, ok := spec.NativeBinding("anthropic-messages"); !ok || transport != TransportAnthropic {
			t.Errorf("%s messages binding=%q ok=%v", id, transport, ok)
		}
		if transport, ok := spec.NativeBinding("openai-chat"); !ok || transport != TransportOpenAIChat {
			t.Errorf("%s chat binding=%q ok=%v", id, transport, ok)
		}
	}
	if spec, _ := catalog.Lookup("deepseek"); len(spec.Quirks) != 1 || spec.Quirks[0] != QuirkCacheControl {
		t.Fatalf("deepseek quirks=%+v", spec)
	}
	if spec, _ := catalog.Lookup("glm-cn"); len(spec.Transports) != 1 || spec.Transports[0] != TransportOpenAIChat {
		t.Fatalf("glm-cn spec=%+v", spec)
	}
	for _, id := range []string{"mimo-free", "opencode"} {
		spec, _ := catalog.Lookup(id)
		if spec.Auth != AuthNone || spec.ModelCatalog != CatalogDynamic || !spec.PassthroughModels {
			t.Errorf("no-auth passthrough %s spec=%+v", id, spec)
		}
	}
	if spec, _ := catalog.Lookup("ollama-local"); spec.Auth != AuthNone || spec.Transports[0] != TransportOllama || spec.DefaultBaseURL != "http://localhost:11434" {
		t.Fatalf("ollama-local spec=%+v", spec)
	}
	if spec, _ := catalog.Lookup("ollama"); spec.Auth != AuthAPIKey || spec.Transports[0] != TransportOllama {
		t.Fatalf("ollama spec=%+v", spec)
	}
	if spec, _ := catalog.Lookup("typesafe"); spec.Transports[0] != TransportSystemOne || spec.Auth != AuthAPIKey {
		t.Fatalf("typesafe spec=%+v", spec)
	}
	for _, id := range []string{"tokenrouter", "perplexity-agent"} {
		spec, _ := catalog.Lookup(id)
		if spec.ModelCatalog != CatalogDynamic || !spec.PassthroughModels || spec.Auth != AuthAPIKey {
			t.Errorf("dynamic gateway %s spec=%+v", id, spec)
		}
	}
	if spec, _ := catalog.Lookup("perplexity-agent"); spec.Transports[0] != TransportOpenAIResponses {
		t.Fatalf("perplexity-agent transports=%v", spec.Transports)
	}
	if spec, _ := catalog.Lookup("opencode-go"); len(spec.Transports) != 3 || !spec.ReportsUsage {
		t.Fatalf("opencode-go spec=%+v", spec)
	}
	opencodeGo, _ := catalog.Lookup("opencode-go")
	if transport, ok := opencodeGo.NativeBinding("anthropic-messages"); !ok || transport != TransportAnthropic {
		t.Fatalf("opencode-go messages binding=%q ok=%v", transport, ok)
	}
	if spec, _ := catalog.Lookup("vertex-partner"); spec.Transports[0] != TransportOpenAIChat || spec.DefaultBaseURL != "https://aiplatform.googleapis.com" {
		t.Fatalf("vertex-partner spec=%+v", spec)
	}
	if spec, _ := catalog.Lookup("github"); spec.Auth != AuthOAuth || len(spec.Transports) != 3 || !spec.ReportsUsage {
		t.Fatalf("github spec=%+v", spec)
	}
	github, _ := catalog.Lookup("github")
	if transport, ok := github.NativeBinding("anthropic-messages"); !ok || transport != TransportAnthropic {
		t.Fatalf("github messages binding=%q ok=%v", transport, ok)
	}
	if spec, _ := catalog.Lookup("xai"); len(spec.Transports) != 2 || spec.Transports[1] != TransportOpenAIResponses || spec.Auth != AuthAPIKey {
		t.Fatalf("xai spec=%+v", spec)
	}
	xai, _ := catalog.Lookup("xai")
	if transport, ok := xai.NativeBinding("openai-responses"); !ok || transport != TransportOpenAIResponses {
		t.Fatalf("xai responses binding=%q ok=%v", transport, ok)
	}
	for _, id := range []string{"kimi", "xiaomi-mimo"} {
		spec, _ := catalog.Lookup(id)
		if len(spec.Transports) != 2 || spec.Transports[0] != TransportOpenAIChat || spec.Transports[1] != TransportAnthropic || !spec.ReportsUsage {
			t.Errorf("dual-transport %s spec=%+v", id, spec)
		}
	}
	if spec, _ := catalog.Lookup("kilocode"); spec.ModelCatalog != CatalogDynamic || !spec.PassthroughModels || spec.Auth != AuthOAuth {
		t.Fatalf("kilocode spec=%+v", spec)
	}
	for _, id := range []string{"codebuddy-cn", "codebuddy-intl"} {
		spec, _ := catalog.Lookup(id)
		if !spec.ReportsUsage || spec.Transports[0] != TransportOpenAIChat || len(spec.StaticModels) == 0 {
			t.Errorf("codebuddy %s spec=%+v", id, spec)
		}
	}
	for _, id := range []string{"cline", "clinepass", "gitlab", "iflow"} {
		spec, _ := catalog.Lookup(id)
		if spec.Transports[0] != TransportOpenAIChat {
			t.Errorf("oauth-specialized %s transports=%v", id, spec.Transports)
		}
	}
	if spec, _ := catalog.Lookup("cline"); spec.Auth != AuthOAuth {
		t.Fatalf("cline auth=%v", spec.Auth)
	}
	// Dual-auth rows must expose both approved credential modes.
	for _, id := range []string{"xai", "kimi", "xiaomi-mimo", "clinepass", "codebuddy-cn", "codebuddy-intl"} {
		spec, _ := catalog.Lookup(id)
		if len(spec.AuthModes) != 2 || spec.AuthModes[0] != spec.Auth || spec.AuthModes[1] != AuthOAuth {
			t.Errorf("dual-auth modes %s = %v (default %v)", id, spec.AuthModes, spec.Auth)
		}
	}
	if spec, _ := catalog.Lookup("gitlab"); spec.Auth != AuthOAuth {
		t.Fatalf("gitlab auth=%v", spec.Auth)
	}
	// Specialized-wire providers must carry their provider-specific protocol and
	// preserve the baseline auth/catalog semantics.
	for id, transport := range map[string]Protocol{
		"antigravity": TransportAntigravity, "gemini-cli": TransportGeminiCLI, "cursor": TransportCursor,
		"qoder": TransportQoder, "kiro": TransportKiro, "vertex": TransportVertex, "commandcode": TransportCommandCode,
		"grok-web": TransportGrokWeb, "perplexity-web": TransportPerplexityWeb,
	} {
		spec, _ := catalog.Lookup(id)
		if len(spec.Transports) != 1 || spec.Transports[0] != transport {
			t.Errorf("specialized %s transports=%v want=%q", id, spec.Transports, transport)
		}
	}
	if spec, _ := catalog.Lookup("grok-cli"); spec.Auth != AuthOAuth || spec.Transports[0] != TransportOpenAIResponses || !spec.ReportsUsage {
		t.Fatalf("grok-cli spec=%+v", spec)
	}
	if spec, _ := catalog.Lookup("grok-web"); spec.Auth != AuthCookie || spec.ModelCatalog != CatalogPassthrough || !spec.PassthroughModels {
		t.Fatalf("grok-web spec=%+v", spec)
	}
	if spec, _ := catalog.Lookup("perplexity-web"); spec.Auth != AuthCookie || spec.Transports[0] != TransportPerplexityWeb {
		t.Fatalf("perplexity-web spec=%+v", spec)
	}
	if spec, _ := catalog.Lookup("zed"); spec.Auth != AuthOAuth || !spec.ReportsUsage || !spec.PassthroughModels || spec.ModelCatalog != CatalogPassthrough {
		t.Fatalf("zed spec=%+v", spec)
	}
	if spec, _ := catalog.Lookup("kimchi"); len(spec.AuthModes) != 2 || spec.AuthModes[1] != AuthOAuth || !spec.PassthroughModels {
		t.Fatalf("kimchi spec=%+v", spec)
	}
	if spec, _ := catalog.Lookup("qoder"); len(spec.AuthModes) != 2 || spec.AuthModes[0] != AuthAPIKey || spec.AuthModes[1] != AuthOAuth {
		t.Fatalf("qoder dual-auth spec=%+v", spec)
	}
	if spec, _ := catalog.Lookup("kiro"); len(spec.AuthModes) != 2 || spec.AuthModes[0] != AuthOAuth || spec.AuthModes[1] != AuthAPIKey {
		t.Fatalf("kiro dual-auth spec=%+v", spec)
	}
	kenari, _ := catalog.Lookup("kenari")
	if len(kenari.Transports) != 3 || kenari.ModelCatalog != CatalogDynamic || !kenari.PassthroughModels || !kenari.ReportsUsage {
		t.Fatalf("kenari spec=%+v", kenari)
	}
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic-messages"} {
		if _, ok := kenari.NativeBinding(protocol); !ok {
			t.Errorf("kenari missing native binding %q", protocol)
		}
	}
	if spec, _ := catalog.Lookup("azure"); spec.DefaultBaseURL != "" || spec.Auth != AuthAPIKey || spec.Transports[0] != TransportOpenAIChat {
		t.Fatalf("azure spec=%+v", spec)
	}
	for _, id := range []string{"alicode-intl", "alitp-intl"} {
		spec, _ := catalog.Lookup(id)
		if len(spec.Quirks) != 1 || spec.Quirks[0] != QuirkCacheControl {
			t.Errorf("cache-control quirk missing for %s: %+v", id, spec)
		}
	}
}

func TestCatalogLookupCopiesStaticModels(t *testing.T) {
	catalog, err := NewCatalog([]Spec{{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, ModelCatalog: CatalogStatic, StaticModels: []string{"m"}}})
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := catalog.Lookup("p")
	spec.StaticModels[0] = "mutated"
	again, _ := catalog.Lookup("p")
	if again.StaticModels[0] != "m" {
		t.Fatal("catalog static model slice leaked")
	}
}

func TestDualAuthModesValidationAndCopy(t *testing.T) {
	for _, invalid := range []Spec{
		{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, AuthModes: []AuthKind{AuthOAuth, AuthAPIKey}, ModelCatalog: CatalogStatic},
		{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, AuthModes: []AuthKind{AuthAPIKey, AuthAPIKey}, ModelCatalog: CatalogStatic},
		{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, AuthModes: []AuthKind{AuthAPIKey, "bogus"}, ModelCatalog: CatalogStatic},
	} {
		if _, err := NewCatalog([]Spec{invalid}); err == nil {
			t.Errorf("accepted invalid auth modes %+v", invalid.AuthModes)
		}
	}
	catalog, err := NewCatalog([]Spec{{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, AuthModes: []AuthKind{AuthAPIKey, AuthOAuth}, ModelCatalog: CatalogStatic}})
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := catalog.Lookup("p")
	spec.AuthModes[1] = "mutated"
	if again, _ := catalog.Lookup("p"); again.AuthModes[1] != AuthOAuth {
		t.Fatal("auth modes slice leaked")
	}
}

func TestTransportEndpointsResolvePerProtocolAndCopy(t *testing.T) {
	catalog, err := NewBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	// Providers whose native transports do not share one base path expose a
	// per-protocol endpoint, and the shared catalog resolves it by id.
	cases := []struct {
		provider  string
		transport string
		path      string
	}{
		{"kenari", "openai-chat", "v1/chat/completions"},
		{"kenari", "openai-responses", "v1/responses"},
		{"kenari", "anthropic-messages", "v1/messages"},
		{"github", "openai-chat", "chat/completions"},
		{"github", "openai-responses", "responses"},
		{"github", "anthropic-messages", "v1/messages"},
		{"kimi", "anthropic-messages", "messages"},
		{"xiaomi-mimo", "openai-chat", "v1/chat/completions"},
		{"xiaomi-mimo", "anthropic-messages", "anthropic/v1/messages"},
	}
	for _, tc := range cases {
		path, ok := catalog.EndpointFor(tc.provider, tc.transport)
		if !ok || path != tc.path {
			t.Errorf("endpoint %s/%s = %q ok=%v want %q", tc.provider, tc.transport, path, ok, tc.path)
		}
	}
	// A single-transport provider with no declared override keeps the shared
	// default (ok=false).
	if _, ok := catalog.EndpointFor("openai", "openai-chat"); ok {
		t.Fatal("openai should not declare a transport endpoint override")
	}
	if _, ok := catalog.EndpointFor("unknown", "openai-chat"); ok {
		t.Fatal("unknown provider should not resolve an endpoint")
	}
	// Lookup must not leak the endpoint map to callers.
	spec, _ := catalog.Lookup("kenari")
	spec.TransportEndpoints["openai-chat"] = "mutated"
	if path, _ := catalog.EndpointFor("kenari", "openai-chat"); path != "v1/chat/completions" {
		t.Fatalf("endpoint map leaked: %q", path)
	}
}

func TestTransportEndpointsValidation(t *testing.T) {
	for _, invalid := range []Spec{
		{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthNone, ModelCatalog: CatalogStatic, TransportEndpoints: map[Protocol]string{TransportAnthropic: "messages"}},
		{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthNone, ModelCatalog: CatalogStatic, TransportEndpoints: map[Protocol]string{TransportOpenAIChat: "/abs"}},
		{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthNone, ModelCatalog: CatalogStatic, TransportEndpoints: map[Protocol]string{TransportOpenAIChat: "a/../b"}},
		{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthNone, ModelCatalog: CatalogStatic, TransportEndpoints: map[Protocol]string{TransportOpenAIChat: "a?b"}},
		{ID: "p", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthNone, ModelCatalog: CatalogStatic, TransportEndpoints: map[Protocol]string{TransportOpenAIChat: "  "}},
	} {
		if _, err := NewCatalog([]Spec{invalid}); err == nil {
			t.Errorf("accepted invalid transport endpoint %+v", invalid.TransportEndpoints)
		}
	}
}
