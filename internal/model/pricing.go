package model

import "sync"

// Pricing is the per-model token price table (USD per million tokens).
// Used as an estimate when Devin's own credit/ACU fields are zero
// (e.g. free models). Credit/ACU from sessions.metadata is authoritative
// when non-zero; this is only a fallback.
type Pricing struct {
	Model          string
	InputPerM      float64 // USD per 1M input tokens
	OutputPerM     float64 // USD per 1M output tokens
	CacheReadPerM  float64
	CacheWritePerM float64
	Free           bool
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
//
// Three sources are consulted, in order: a user override installed with
// SetPricingOverrides, then the built-in table, then a zero value. The model
// name is canonicalised through SetModelAliases first.
//
// Overrides live here, in the one function every cost path already calls,
// rather than being threaded through the callers: a previous attempt put the
// merge in a helper that nothing called, which left the whole override feature
// silently inert.
func LookupPricing(name string) Pricing {
	name = canonicalName(name)
	if p, ok := lookupOverride(name); ok {
		return p
	}
	for _, p := range builtinPricing {
		if p.Model == name {
			return p
		}
	}
	// Fuzzy: prefix match (e.g. "claude-sonnet-4-5-20250929" -> "claude-sonnet-4-5").
	for _, p := range builtinPricing {
		if len(p.Model) > 0 && len(name) >= len(p.Model) && name[:len(p.Model)] == p.Model {
			return p
		}
	}
	return Pricing{Model: name}
}

var (
	pricingMu        sync.RWMutex
	pricingOverrides map[string]Pricing
	modelAliases     map[string]string
)

// SetPricingOverrides installs user-supplied pricing that LookupPricing
// consults before the built-in table. Passing an empty map clears them.
//
// This is process-global on purpose: pricing is read from the report layer,
// the live TUI, trends, MCP and the web dashboard, so one installation at
// startup is what keeps every surface consistent.
func SetPricingOverrides(m map[string]Pricing) {
	pricingMu.Lock()
	defer pricingMu.Unlock()
	pricingOverrides = clonePricing(m)
}

// PricingOverrides returns the installed overrides, or nil when there are none.
func PricingOverrides() map[string]Pricing {
	pricingMu.RLock()
	defer pricingMu.RUnlock()
	return clonePricing(pricingOverrides)
}

// SetModelAliases installs user-defined model aliases (alias -> canonical
// name) applied by LookupPricing. Passing an empty map clears them.
func SetModelAliases(m map[string]string) {
	cp := make(map[string]string, len(m))
	for k, v := range m {
		if k == "" || v == "" || k == v {
			continue
		}
		cp[k] = v
	}
	pricingMu.Lock()
	defer pricingMu.Unlock()
	if len(cp) == 0 {
		modelAliases = nil
		return
	}
	modelAliases = cp
}

// ModelAliases returns the installed aliases, or nil when there are none.
func ModelAliases() map[string]string {
	pricingMu.RLock()
	defer pricingMu.RUnlock()
	if len(modelAliases) == 0 {
		return nil
	}
	cp := make(map[string]string, len(modelAliases))
	for k, v := range modelAliases {
		cp[k] = v
	}
	return cp
}

// canonicalName resolves an alias, following chains but stopping if one loops.
func canonicalName(name string) string {
	pricingMu.RLock()
	defer pricingMu.RUnlock()
	if len(modelAliases) == 0 {
		return name
	}
	seen := map[string]bool{name: true}
	for i := 0; i < 8; i++ {
		next, ok := modelAliases[name]
		if !ok || seen[next] {
			return name
		}
		name = next
		seen[name] = true
	}
	return name
}

// lookupOverride finds a user override for name: an exact key first, then the
// longest matching prefix so an override for "swe-1-7" also covers
// "swe-1-7-medium", mirroring the built-in table's prefix rule.
func lookupOverride(name string) (Pricing, bool) {
	pricingMu.RLock()
	defer pricingMu.RUnlock()
	if len(pricingOverrides) == 0 {
		return Pricing{}, false
	}
	if p, ok := pricingOverrides[name]; ok {
		p.Model = name
		return p, true
	}
	best := ""
	for k := range pricingOverrides {
		if k == "" || len(name) <= len(k) || name[:len(k)] != k {
			continue
		}
		if len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return Pricing{}, false
	}
	p := pricingOverrides[best]
	p.Model = name
	return p, true
}

func clonePricing(m map[string]Pricing) map[string]Pricing {
	if len(m) == 0 {
		return nil
	}
	cp := make(map[string]Pricing, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
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
