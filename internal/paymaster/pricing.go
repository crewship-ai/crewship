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
// Sources verified 2026-04-30: Anthropic platform docs, OpenAI rate card,
// Google AI pricing, xAI docs, DeepSeek API pricing, Mistral pricing.
// Where a 2026 number is in flux (Gemini 3.x, GPT-5.5 nano), we picked the
// nearest published tier and noted the assumption; tighten on next sweep.
var priceTable = map[string]modelPrice{
	// Anthropic — Claude 5 / 4.x family. Opus 4.7 corrected from $15/$75 to
	// $5/$25 on 2026-04-30 (provider repriced in early 2026; ledger was 3× over).
	// Fable 5 is the premium flagship (above Opus tier). Sonnet 5 carries the
	// standard $3/$15 sticker here — the $2/$10 intro promo (through 2026-08-31)
	// is deliberately not encoded so the ledger never under-bills once it lapses.
	"anthropic/claude-fable-5":    {InputPerM: 10.00, OutputPerM: 50.00, CachedInputPerM: 1.00, CacheWritePerM: 12.50},
	"anthropic/claude-opus-4-8":   {InputPerM: 5.00, OutputPerM: 25.00, CachedInputPerM: 0.50, CacheWritePerM: 6.25},
	"anthropic/claude-opus-4-7":   {InputPerM: 5.00, OutputPerM: 25.00, CachedInputPerM: 0.50, CacheWritePerM: 6.25},
	"anthropic/claude-sonnet-5":   {InputPerM: 3.00, OutputPerM: 15.00, CachedInputPerM: 0.30, CacheWritePerM: 3.75},
	"anthropic/claude-sonnet-4-6": {InputPerM: 3.00, OutputPerM: 15.00, CachedInputPerM: 0.30, CacheWritePerM: 3.75},
	"anthropic/claude-haiku-4-5":  {InputPerM: 1.00, OutputPerM: 5.00, CachedInputPerM: 0.10, CacheWritePerM: 1.25},

	// OpenAI — GPT-5.x family. Cached-input ratio mirrors OpenAI's published
	// 0.10–0.25× prompt-cache discount. OpenAI does not charge a separate
	// write fee, so cache_write equals input rate.
	"openai/gpt-5.5":      {InputPerM: 4.00, OutputPerM: 24.00, CachedInputPerM: 0.40, CacheWritePerM: 4.00},
	"openai/gpt-5.4-mini": {InputPerM: 0.75, OutputPerM: 4.50, CachedInputPerM: 0.075, CacheWritePerM: 0.75},
	"openai/gpt-5.4-nano": {InputPerM: 0.10, OutputPerM: 0.40, CachedInputPerM: 0.01, CacheWritePerM: 0.10},
	"openai/o3-pro":       {InputPerM: 20.00, OutputPerM: 80.00, CachedInputPerM: 5.00, CacheWritePerM: 20.00},
	// Aliases so older agents pinned to "gpt-5", "gpt-5-mini", "gpt-5-nano" still
	// resolve to the right tier. Keeps the rate card honest without churning
	// every config file the day OpenAI bumps the version suffix.
	"openai/gpt-5":      {InputPerM: 4.00, OutputPerM: 24.00, CachedInputPerM: 0.40, CacheWritePerM: 4.00},
	"openai/gpt-5-mini": {InputPerM: 0.75, OutputPerM: 4.50, CachedInputPerM: 0.075, CacheWritePerM: 0.75},
	"openai/gpt-5-nano": {InputPerM: 0.10, OutputPerM: 0.40, CachedInputPerM: 0.01, CacheWritePerM: 0.10},

	// Google Gemini. Pro is tiered by context size (>200K context doubles
	// rates); we use the upper tier as default to slightly overestimate
	// rather than under. Refactor Estimate signature later if precision
	// matters.
	"google/gemini-2.5-pro":        {InputPerM: 2.50, OutputPerM: 15.00, CachedInputPerM: 0.625, CacheWritePerM: 2.50},
	"google/gemini-2.5-flash":      {InputPerM: 0.10, OutputPerM: 0.40, CachedInputPerM: 0.025, CacheWritePerM: 0.10},
	"google/gemini-2.5-flash-lite": {InputPerM: 0.05, OutputPerM: 0.20, CachedInputPerM: 0.0125, CacheWritePerM: 0.05},

	// xAI — Grok 4 family. No separate cached-input pricing in xAI docs as of
	// 2026-04, so cached/cache_write mirror input as a conservative default.
	"xai/grok-4.20":     {InputPerM: 2.00, OutputPerM: 6.00, CachedInputPerM: 2.00, CacheWritePerM: 2.00},
	"xai/grok-4.1-fast": {InputPerM: 0.20, OutputPerM: 0.50, CachedInputPerM: 0.20, CacheWritePerM: 0.20},

	// DeepSeek. Cache-hit pricing dropped to 1/10 of input on 2026-04-26 —
	// reflected here so cost-aware crews using V3 see the discount.
	"deepseek/deepseek-chat":     {InputPerM: 0.252, OutputPerM: 0.378, CachedInputPerM: 0.0252, CacheWritePerM: 0.252},
	"deepseek/deepseek-reasoner": {InputPerM: 0.70, OutputPerM: 2.50, CachedInputPerM: 0.07, CacheWritePerM: 0.70},

	// Mistral — code-completion focus. Other Mistral tiers added on demand.
	"mistral/codestral-2508": {InputPerM: 0.30, OutputPerM: 0.90, CachedInputPerM: 0.30, CacheWritePerM: 0.30},

	// Local / self-hosted runtimes. Always free at the call site; the
	// hardware/electricity cost is amortized elsewhere in finance, not in the
	// per-call ledger.
	"ollama/*": {},
	"local/*":  {},
}

// providerFallback is consulted when (provider, model) misses the table. It
// keeps the system functional when a new model rolls out before the table is
// updated — better to over-estimate than to silently bill $0.
//
// Each entry deliberately tracks the most-expensive known tier for that
// provider, not the median. When an unknown model name shows up, we don't
// know whether it's a budget Haiku or a flagship reasoning model — picking
// the upper tier means budgets warn / exceed correctly even on premium
// reasoning models, at the cost of mild overestimate for cheap ones. The
// alternative (median tier) silently undercharges for top-tier models,
// which defeats the warn/exceed signal exactly when operators need it.
var providerFallback = map[string]modelPrice{
	"anthropic": {InputPerM: 10.00, OutputPerM: 50.00, CachedInputPerM: 1.00, CacheWritePerM: 12.50}, // Fable-tier (premium ceiling)
	"openai":    {InputPerM: 20.00, OutputPerM: 80.00, CachedInputPerM: 5.00, CacheWritePerM: 20.00}, // o3-pro tier (reasoning ceiling)
	"google":    {InputPerM: 2.50, OutputPerM: 15.00, CachedInputPerM: 0.625, CacheWritePerM: 2.50},  // gemini-2.5-pro upper tier
	"xai":       {InputPerM: 2.00, OutputPerM: 6.00, CachedInputPerM: 2.00, CacheWritePerM: 2.00},    // grok-4-equivalent
	"deepseek":  {InputPerM: 0.70, OutputPerM: 2.50, CachedInputPerM: 0.07, CacheWritePerM: 0.70},    // reasoner tier (ceiling)
	"mistral":   {InputPerM: 2.00, OutputPerM: 6.00, CachedInputPerM: 2.00, CacheWritePerM: 2.00},    // mistral-large estimate (ceiling above codestral)
	// Gateways. These ceilings are the highest rate the embedded snapshot
	// actually publishes for the provider (including tier rates), computed from
	// the snapshot rather than guessed. OpenRouter's is startlingly high because
	// it resells 353 models and a few are exotic — and that is the right number
	// anyway: this row fires ONLY for a model the catalogue does not know, i.e.
	// a renamed or brand-new slug. A loud over-estimate on a slug nobody
	// recognises is the signal; billing it at $0 was the bug (a call to an
	// unknown openrouter model priced at zero until this row existed).
	// All four channels are the per-channel maximum over every cost block and
	// every tier in the snapshot, taken AFTER the nil-mirror convention is
	// applied — a model with no cache_read charges its input rate for cache
	// reads, so it counts as a cache-read candidate. The first version of this
	// row read raw JSON keys instead and derived the two cache channels from
	// the input rate by Anthropic's ratios, which billed a cache-heavy unknown
	// openrouter slug at $7.50 against a real ceiling of $150 — 20x under, the
	// same failure this row was added to prevent, one order of magnitude down.
	// A per-channel max is the right shape precisely BECAUSE this row does not
	// describe a real model: it is the bound below which no known model of this
	// provider can be under-billed.
	"openrouter":     {InputPerM: 150.00, OutputPerM: 600.00, CachedInputPerM: 150.00, CacheWritePerM: 150.00},
	"amazon-bedrock": {InputPerM: 16.50, OutputPerM: 82.50, CachedInputPerM: 2.50, CacheWritePerM: 20.625},

	"ollama": {},
	"local":  {},
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

// RateCard returns the per-million rates that Estimate would use for the
// given (provider, model) tuple. Snapshotting these onto the ledger row at
// write time means a later rate-card change doesn't retroactively alter
// historical cost rollups — what was billed stays billed.
func RateCard(provider, model string) modelPrice {
	return lookupPrice(provider, model)
}

// lookupPrice resolves (provider, model) to a rate. Lookup order:
//  1. exact "<provider>/<model>" match in priceTable
//  2. provider-wildcard "<provider>/*" match (used by ollama/local)
//  3. exact match in the embedded models.dev snapshot (catalog_pricing.go)
//  4. providerFallback for the provider
//  5. zero (returned if even the provider is unknown — we never invent a rate
//     for a totally unknown vendor; the operator should see $0 and notice)
//
// The snapshot's position in that list is the whole design. It sits AFTER the
// exact hand-written match, because priceTable carries verified corrections a
// bulk import must never overwrite — Opus 4.7 was billed 3× over until someone
// checked, and alias rows like "openai/gpt-5" have no catalogue equivalent at
// all.
//
// It also sits AFTER the wildcard, so "ollama/*" and "local/*" keep zeroing out
// every local model no matter what a catalogue thinks a same-named hosted model
// costs. The trim happens to exclude ollama today, but embed.go documents how to
// widen it — and a refresh that pulled in an ollama or local provider would
// otherwise start billing every local call. A promise that only holds because of
// what a snapshot currently omits is not a promise.
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
	if p, ok := catalogPrice(prov, mod); ok {
		return p
	}
	if p, ok := providerFallback[prov]; ok {
		return p
	}
	return modelPrice{}
}
