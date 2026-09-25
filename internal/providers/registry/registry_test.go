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
		"alicode-intl", "alicode", "alims-intl", "alitp-intl", "api-airforce", "baidu", "bazaarlink", "blackbox", "bluesminds", "byteplus",
		"cerebras", "chutes", "cohere", "featherless", "fireworks", "groq", "hyperbolic", "kilo-gateway", "llm7", "mistral", "morph", "nebius",
		"nvidia", "openrouter", "perplexity", "poolside", "sambanova", "siliconflow", "tencent", "together", "venice", "vercel-ai-gateway", "volcengine-ark",
	}
	if catalog.Len() != len(wantIDs) {
		t.Fatalf("catalog size=%d want=%d", catalog.Len(), len(wantIDs))
	}
	for _, id := range wantIDs {
		spec, ok := catalog.Lookup(id)
		if !ok || spec.Auth != AuthAPIKey || len(spec.Transports) != 1 || spec.Transports[0] != TransportOpenAIChat || spec.DefaultBaseURL == "" {
			t.Errorf("incomplete group member %q: %+v present=%v", id, spec, ok)
		}
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
	if spec, _ := catalog.Lookup("groq"); !spec.ReportsUsage {
		t.Fatalf("Groq usage capability missing: %+v", spec)
	}
	for _, id := range []string{"alicode-intl", "alitp-intl"} {
		spec, _ := catalog.Lookup(id)
		if len(spec.Quirks) != 1 || spec.Quirks[0] != QuirkCacheControl {
			t.Errorf("cache-control quirk missing for %s: %+v", id, spec)
		}
	}
}
