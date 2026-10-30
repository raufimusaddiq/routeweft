package pricing

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

type pin struct {
	SHA    string `json:"sha"`
	Lookup []struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Pricing  *struct {
			Input, Output, Cached, Reasoning *float64
			CacheCreation                    *float64 `json:"cache_creation"`
		} `json:"pricing"`
	} `json:"lookup"`
	Cost []struct {
		Name    string `json:"name"`
		Pricing struct {
			Input, Output, Cached, Reasoning float64
			CacheCreation                    float64 `json:"cache_creation"`
		} `json:"pricing"`
		Tokens struct {
			PromptTokens             int64 `json:"prompt_tokens"`
			InputTokens              int64 `json:"input_tokens"`
			CompletionTokens         int64 `json:"completion_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CachedTokens             int64 `json:"cached_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			ReasoningTokens          int64 `json:"reasoning_tokens"`
		} `json:"tokens"`
		Cost float64 `json:"cost"`
	} `json:"cost"`
}

func loadPin(t *testing.T) pin {
	t.Helper()
	raw, err := os.ReadFile("testdata/pricing_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var p pin
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.SHA) != 40 || len(p.Lookup) < 500 || len(p.Cost) < 30 {
		t.Fatalf("pin is incomplete: sha=%q lookups=%d cost=%d", p.SHA, len(p.Lookup), len(p.Cost))
	}
	return p
}

func orZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// Every answer the oracle's getPricingForModel gave for the probe set, including
// vendor-prefixed names, other cases, glob hits and misses.
func TestLookupMatchesTheOracle(t *testing.T) {
	for _, c := range loadPin(t).Lookup {
		got, ok := Lookup([]string{c.Provider}, c.Model)
		if (c.Pricing != nil) != ok {
			t.Errorf("%q/%q: found=%v, oracle %v", c.Provider, c.Model, ok, c.Pricing != nil)
			continue
		}
		if !ok {
			continue
		}
		want := Rates{orZero(c.Pricing.Input), orZero(c.Pricing.Output), orZero(c.Pricing.Cached), orZero(c.Pricing.Reasoning), orZero(c.Pricing.CacheCreation)}
		if got != want {
			t.Errorf("%q/%q: %+v, oracle %+v", c.Provider, c.Model, got, want)
		}
	}
}

func TestCostMatchesTheOracle(t *testing.T) {
	for _, c := range loadPin(t).Cost {
		input := c.Tokens.PromptTokens + c.Tokens.InputTokens
		output := c.Tokens.CompletionTokens + c.Tokens.OutputTokens
		cached := c.Tokens.CachedTokens + c.Tokens.CacheReadInputTokens
		got := Cost(Tokens{Input: input, Output: output, Cached: cached, CacheCreation: c.Tokens.CacheCreationInputTokens, Reasoning: c.Tokens.ReasoningTokens},
			Rates{c.Pricing.Input, c.Pricing.Output, c.Pricing.Cached, c.Pricing.Reasoning, c.Pricing.CacheCreation})
		if math.Abs(got-c.Cost) > 1e-12 {
			t.Errorf("%s: %v, oracle %v", c.Name, got, c.Cost)
		}
	}
}

func TestGlob(t *testing.T) {
	for _, tc := range []struct {
		pattern, text string
		want          bool
	}{
		{"grok-*", "GROK-4", true}, {"*-codex-high", "gpt-5-codex-high", true}, {"*-codex-high", "gpt-5-codex-higher", false},
		{"a*b*c", "aXXbYYc", true}, {"a*b*c", "abc", true}, {"a*b*c", "acb", false}, {"a**", "a", true}, {"*", "", true}, {"x", "x", true}, {"x", "xy", false},
		{"ab*ab", "ab", false}, {"ab*ab", "abab", true},
	} {
		if got := matchPattern(tc.pattern, tc.text); got != tc.want {
			t.Errorf("%q vs %q: %v, want %v", tc.pattern, tc.text, got, tc.want)
		}
	}
}
