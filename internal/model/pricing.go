package model

// Pricing is the per-model token price table (USD per million tokens).
// Used as an estimate when Devin's own credit/ACU fields are zero
// (e.g. free models). Credit/ACU from sessions.metadata is authoritative
// when non-zero; this is only a fallback.
type Pricing struct {
	Model       string
	InputPerM   float64 // USD per 1M input tokens
	OutputPerM  float64 // USD per 1M output tokens
	CacheReadPerM  float64
	CacheWritePerM float64
	Free        bool
}

// Built-in pricing table. Extend as new models are added.
// Sources: provider public pricing pages. Values are estimates.
var builtinPricing = []Pricing{
	// Devin free models
	{Model: "glm-5-2", Free: true},
	{Model: "glm-5-2-high", Free: true},

	// Anthropic (approximate, USD per 1M tokens)
	{Model: "claude-sonnet-4-5", InputPerM: 3.0, OutputPerM: 15.0, CacheReadPerM: 0.30, CacheWritePerM: 3.75},
	{Model: "claude-sonnet-4", InputPerM: 3.0, OutputPerM: 15.0, CacheReadPerM: 0.30, CacheWritePerM: 3.75},
	{Model: "claude-opus-4-1", InputPerM: 15.0, OutputPerM: 75.0, CacheReadPerM: 1.50, CacheWritePerM: 18.75},
	{Model: "claude-opus-4", InputPerM: 15.0, OutputPerM: 75.0, CacheReadPerM: 1.50, CacheWritePerM: 18.75},
	{Model: "claude-3-7-sonnet", InputPerM: 3.0, OutputPerM: 15.0, CacheReadPerM: 0.30, CacheWritePerM: 3.75},
	{Model: "claude-3-5-haiku", InputPerM: 0.80, OutputPerM: 4.0, CacheReadPerM: 0.08, CacheWritePerM: 1.0},

	// OpenAI (approximate)
	{Model: "gpt-4o", InputPerM: 2.50, OutputPerM: 10.0, CacheReadPerM: 1.25},
	{Model: "gpt-4.1", InputPerM: 2.0, OutputPerM: 8.0, CacheReadPerM: 0.5},
	{Model: "gpt-4o-mini", InputPerM: 0.15, OutputPerM: 0.60, CacheReadPerM: 0.075},

	// Google
	{Model: "gemini-2.5-pro", InputPerM: 1.25, OutputPerM: 10.0, CacheReadPerM: 0.315},
	{Model: "gemini-2.5-flash", InputPerM: 0.075, OutputPerM: 0.30, CacheReadPerM: 0.0188},
}

// LookupPricing returns pricing for a model name, with a fuzzy match fallback.
// Unknown models return a zero-value Pricing (caller treats as free/unknown).
func LookupPricing(model string) Pricing {
	for _, p := range builtinPricing {
		if p.Model == model {
			return p
		}
	}
	// Fuzzy: prefix match (e.g. "claude-sonnet-4-5-20250929" -> "claude-sonnet-4-5").
	for _, p := range builtinPricing {
		if len(p.Model) > 0 && len(model) >= len(p.Model) && model[:len(p.Model)] == p.Model {
			return p
		}
	}
	return Pricing{Model: model}
}

// EstimateCost computes an estimated USD cost from token counts.
// Only meaningful when Devin's credit/ACU is zero (free models).
func EstimateCost(p Pricing, input, output, cacheRead, cacheWrite int64) float64 {
	if p.Free || (p.InputPerM == 0 && p.OutputPerM == 0) {
		return 0
	}
	return float64(input)/1e6*p.InputPerM +
		float64(output)/1e6*p.OutputPerM +
		float64(cacheRead)/1e6*p.CacheReadPerM +
		float64(cacheWrite)/1e6*p.CacheWritePerM
}

// AllPricing returns the built-in pricing table (for display/export).
func AllPricing() []Pricing { return builtinPricing }
