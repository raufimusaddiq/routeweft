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
		if !ok || len(spec.Transports) == 0 || spec.DefaultBaseURL == "" {
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
