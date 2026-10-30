// Package pricing resolves a model's default price and prices a request, the way
// the oracle does (open-sse/providers/pricing.js, LiteRouter main fc8dd1fc). The
// catalog in data_gen.go is generated from the oracle and checked in CI; an
// operator's override replaces it for one provider and model.
package pricing

import "strings"

// Rates are dollars per million tokens. A zero field is absent, as in the oracle,
// where every use of an optional rate is `rate || fallback`.
type Rates struct {
	Input, Output, Cached, Reasoning, CacheCreation float64
}

type patternRates struct {
	Pattern string
	Rates   Rates
}

// Tokens are the counts a request is priced on. Input is cache-inclusive: Cached
// and CacheCreation are subsets of it (calculateCostFromTokens, pricing.js:455-457).
type Tokens struct {
	Input, Output, Cached, CacheCreation, Reasoning int64
}

// Lookup is getPricingForModel (pricing.js:400-425): a provider-specific entry, then
// the canonical entry for the model (its last path segment first, then as given),
// then the first glob that matches, case-insensitively. providerKeys are the names
// the provider is known by; the oracle keys its provider table by alias or id.
func Lookup(providerKeys []string, model string) (Rates, bool) {
	if model == "" {
		return Rates{}, false
	}
	for _, provider := range providerKeys {
		if provider == "" {
			continue
		}
		if rates, ok := providerPricing[provider][model]; ok {
			return rates, true
		}
	}
	base := model
	if index := strings.LastIndex(model, "/"); index >= 0 {
		base = model[index+1:]
	}
	if rates, ok := modelPricing[base]; ok {
		return rates, true
	}
	if rates, ok := modelPricing[model]; ok {
		return rates, true
	}
	for _, entry := range patternPricing {
		if matchPattern(entry.Pattern, base) || matchPattern(entry.Pattern, model) {
			return entry.Rates, true
		}
	}
	return Rates{}, false
}

// matchPattern is pricing.js:380-392: "*" matches any run of characters, the rest
// is literal, the whole model must match, case does not matter.
func matchPattern(pattern, model string) bool {
	return glob(strings.ToLower(pattern), strings.ToLower(model))
}

func glob(pattern, text string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == text
	}
	if !strings.HasPrefix(text, parts[0]) {
		return false
	}
	text = text[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		index := strings.Index(text, part)
		if index < 0 {
			return false
		}
		text = text[index+len(part):]
	}
	last := parts[len(parts)-1]
	return strings.HasSuffix(text, last) && len(text) >= len(last)
}

// Cost is calculateCostFromTokens (pricing.js:447-478) in dollars: input that is not
// cached or cache-creation at the input rate, cached at the cached rate, output at
// the output rate, reasoning tokens on top at the reasoning rate, cache creation at
// its own rate. An absent cached, reasoning or creation rate falls back to the input
// rate (the output rate for reasoning).
func Cost(tokens Tokens, rates Rates) float64 {
	nonCached := max(tokens.Input-tokens.Cached-tokens.CacheCreation, 0)
	cost := float64(nonCached) * rates.Input / 1e6
	if tokens.Cached > 0 {
		cost += float64(tokens.Cached) * orElse(rates.Cached, rates.Input) / 1e6
	}
	cost += float64(tokens.Output) * rates.Output / 1e6
	if tokens.Reasoning > 0 {
		cost += float64(tokens.Reasoning) * orElse(rates.Reasoning, rates.Output) / 1e6
	}
	if tokens.CacheCreation > 0 {
		cost += float64(tokens.CacheCreation) * orElse(rates.CacheCreation, rates.Input) / 1e6
	}
	return cost
}

func orElse(rate, fallback float64) float64 {
	if rate != 0 {
		return rate
	}
	return fallback
}
