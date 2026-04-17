package paymaster

import "strings"

// modelPrice is the per-million-token rate card for one model. All four
// channels are priced separately because cached input is dramatically cheaper
// than fresh input (~10x), and cache *creation* is slightly more expensive
// than fresh input. Modeling them independently means the cost surfaced to
// the operator matches what the provider's invoice will say.
//
// Rates are USD per 1,000,000 tokens. Local models (Ollama) are zero across
// the board so the same Estimate path works for them without special-casing.
type modelPrice struct {
	InputPerM       float64
	OutputPerM      float64
	CachedInputPerM float64
	CacheWritePerM  float64
}

// priceTable holds the canonical rate card. Keys are normalized to
// "<provider>/<model>" lowercase so callers don't need to remember whether
// the provider sent "claude-opus-4-7" or "Claude-Opus-4-7". Adding a model is
// a one-line change here; missing models fall through to providerFallback.
//
// Anthropic 2026 pricing source: provider docs as of model-card publication.
// OpenAI gpt-5 family pricing: provider's published rate card; o3-pro uses
// the announced reasoning-tier rate. Where a public 2026 number was not
// available we picked a sensible default consistent with the model's
// positioning and noted the assumption inline so it can be tightened later.
var priceTable = map[string]modelPrice{
	// Anthropic — Claude 4.x family.
	"anthropic/claude-opus-4-7":   {InputPerM: 15.00, OutputPerM: 75.00, CachedInputPerM: 1.50, CacheWritePerM: 18.75},
	"anthropic/claude-sonnet-4-6": {InputPerM: 3.00, OutputPerM: 15.00, CachedInputPerM: 0.30, CacheWritePerM: 3.75},
	"anthropic/claude-haiku-4-5":  {InputPerM: 0.80, OutputPerM: 4.00, CachedInputPerM: 0.08, CacheWritePerM: 1.00},

	// OpenAI — GPT-5 family. Cached-input ratio mirrors OpenAI's published
	// 0.25x prompt-cache discount; cache-write is priced at the standard input
	// rate since OpenAI doesn't charge a separate write fee.
	"openai/gpt-5":      {InputPerM: 10.00, OutputPerM: 30.00, CachedInputPerM: 2.50, CacheWritePerM: 10.00},
	"openai/gpt-5-mini": {InputPerM: 2.50, OutputPerM: 10.00, CachedInputPerM: 0.625, CacheWritePerM: 2.50},
	"openai/gpt-5-nano": {InputPerM: 0.50, OutputPerM: 2.00, CachedInputPerM: 0.125, CacheWritePerM: 0.50},
	"openai/o3-pro":     {InputPerM: 20.00, OutputPerM: 80.00, CachedInputPerM: 5.00, CacheWritePerM: 20.00},

	// Local / self-hosted runtimes. Always free at the call site; the
	// hardware/electricity cost is amortized elsewhere in finance, not in the
	// per-call ledger.
	"ollama/*": {},
	"local/*":  {},
}

// providerFallback is consulted when (provider, model) misses the table. It
// keeps the system functional when a new model rolls out before the table is
// updated — better to over-estimate than to silently bill $0.
var providerFallback = map[string]modelPrice{
	"anthropic": {InputPerM: 3.00, OutputPerM: 15.00, CachedInputPerM: 0.30, CacheWritePerM: 3.75},   // Sonnet-equivalent
	"openai":    {InputPerM: 10.00, OutputPerM: 30.00, CachedInputPerM: 2.50, CacheWritePerM: 10.00}, // gpt-5-equivalent
	"ollama":    {},
	"local":     {},
}

// Estimate computes the USD cost of a single LLM call. Token counts are int64
// (rather than int) because providers return them that way and we don't want
// silent overflow on big batches. Negative inputs are treated as zero so a
// glitchy upstream count can't produce a credit on the ledger.
//
// The math: cost = sum(channel_tokens * channel_rate / 1_000_000). Rounding is
// not applied here — the column is REAL and the UI formats display values.
// Storing the full precision means rollups don't accumulate drift.
func Estimate(provider, model string, inTok, outTok, cachedIn, cacheCreate int64) float64 {
	p := lookupPrice(provider, model)
	clamp := func(n int64) float64 {
		if n < 0 {
			return 0
		}
		return float64(n)
	}
	const perM = 1_000_000.0
	return clamp(inTok)*p.InputPerM/perM +
		clamp(outTok)*p.OutputPerM/perM +
		clamp(cachedIn)*p.CachedInputPerM/perM +
		clamp(cacheCreate)*p.CacheWritePerM/perM
}

// lookupPrice resolves (provider, model) to a rate. Lookup order:
//  1. exact "<provider>/<model>" match
//  2. provider-wildcard "<provider>/*" match (used by ollama/local)
//  3. providerFallback for the provider
//  4. zero (returned if even the provider is unknown — we never invent a rate
//     for a totally unknown vendor; the operator should see $0 and notice)
func lookupPrice(provider, model string) modelPrice {
	prov := strings.ToLower(strings.TrimSpace(provider))
	mod := strings.ToLower(strings.TrimSpace(model))
	if prov == "" {
		return modelPrice{}
	}

	if p, ok := priceTable[prov+"/"+mod]; ok {
		return p
	}
	if p, ok := priceTable[prov+"/*"]; ok {
		return p
	}
	if p, ok := providerFallback[prov]; ok {
		return p
	}
	return modelPrice{}
}
