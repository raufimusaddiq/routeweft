package capabilities_test

import (
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/capabilities"
)

func TestResolution(t *testing.T) {
	c := capabilities.New()
	for _, tc := range []struct {
		provider, model, format string
		output                  int64
		vision                  bool
	}{
		{"", "claude-opus-4.8", "claude-adaptive", 128000, true},
		{"", "vendor/CLAUDE-haiku-new", "claude-budget", 64000, true},
		{"", "unknown", "", 64000, false},
		{"", "new-vl-model", "", 64000, true},
		{"commandcode", "deepseek/deepseek-v4-pro", "commandcode", 384000, false},
		{"commandcode", "future-model", "commandcode", 384000, true},
	} {
		got := c.Resolve(tc.provider, tc.model)
		if got.ThinkingFormat != tc.format || got.MaxOutput != tc.output || got.Vision != tc.vision {
			t.Errorf("%s/%s: %+v", tc.provider, tc.model, got)
		}
	}
	if c.Resolve("cx", "vendor/gpt-6-astra").ContextWindow != 272000 {
		t.Fatal("provider alias/full-base override lost to canonical or pattern")
	}
	got := c.Resolve("", "gemini-2.5-future")
	got.ThinkingRange.Max = 1
	if c.Resolve("", "gemini-2.5-future").ThinkingRange.Max == 1 {
		t.Fatal("result mutation reached catalog")
	}
}

func TestProviderAliasesNameTheirCanonicalProvider(t *testing.T) {
	for alias, want := range map[string]string{"kmc": "kimi", "kimi-coding": "kimi", "ag": "antigravity"} {
		if got, ok := capabilities.ProviderAlias(alias); !ok || got != want {
			t.Errorf("ProviderAlias(%q) = %q,%v, want %q", alias, got, ok, want)
		}
	}
	if got, ok := capabilities.ProviderAlias("kimi"); ok || got != "kimi" {
		t.Fatalf("a canonical id is not an alias: %q,%v", got, ok)
	}
}

func BenchmarkResolve(b *testing.B) {
	c := capabilities.New()
	for _, model := range []string{"claude-opus-4.8", "claude-haiku-future", "unknown"} {
		b.Run(model, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				c.Resolve("anthropic", model)
			}
		})
	}
}
