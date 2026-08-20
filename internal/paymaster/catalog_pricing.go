package paymaster

import (
	"sync"

	"github.com/crewship-ai/crewship/internal/modelcatalog"
)

// The models.dev snapshot as a second, lower-priority rate source.
//
// priceTable is hand-verified and stays authoritative (see lookupPrice): the
// point of this file is the models that are NOT in it. A provider ships a new
// tier, an agent gets pinned to it, and until someone edits priceTable that
// call is billed at the provider ceiling in providerFallback — safe, because it
// over-estimates, but wrong by a factor of ten on a cheap model, which is the
// kind of wrong that makes an operator stop trusting the budget line.
//
// The snapshot is embedded and offline. It is a fallback; a fallback that needs
// a network call to answer would invert its own purpose.

// catalogPrices is the snapshot flattened into the same "<provider>/<model>"
// lowercase key space as priceTable. Built once — the catalogue is a few
// thousand rows and every Estimate call would otherwise walk it.
var catalogPrices = sync.OnceValue(buildCatalogPrices)

// buildCatalogPrices flattens modelcatalog.Default() into modelPrice rows.
//
// Models the catalogue carries no cost for are SKIPPED, not written as a zero
// row. A hosted model with no published rate must fall through to
// providerFallback — the ceiling — because a zero row would bill it at $0,
// which is the one failure mode the whole rate card exists to prevent. Free is
// something only "ollama/*" and "local/*" get to say, and they say it in
// priceTable.
//
// TIERED MODELS ARE PRICED AT THEIR CEILING, and that is a deliberate
// over-estimate. Read this before "fixing" it.
//
// 76 of the snapshot's models publish a second rate card that kicks in above a
// context threshold — usually 200k or 272k tokens, and up to 6.7× the base
// rate (openrouter/qwen/qwen3.7-flash: 0.03 → 0.20 above 256k). Only 2 of the
// 76 are shadowed by priceTable, so the other 74 reach the ledger through this
// file. Picking the base rate would bill a long-context call on that model at
// 15% of the invoice.
//
// Picking the RIGHT tier needs the prompt size, and Estimate does not have an
// axis to carry it: Estimate(provider, model, inTok, outTok, cachedIn,
// cacheCreate) has no context parameter, and threading one through means
// changing Estimate, RateCard and lookupPrice in pricing.go plus their call
// sites in paymaster/middleware.go, api/internal_cost.go and the ledger. That
// is a real change and it should be made deliberately, not as a side effect of
// this one. Until it is, we bill the ceiling: a short call on a tiered model is
// over-estimated, which is the direction providerFallback already chose ("the
// most-expensive known tier for that provider, not the median") and the same
// call the hand-written table made for the one tiered model it carries —
// priceTable["google/gemini-2.5-pro"] is 2.50/15.00, the upper tier, with a
// comment saying so.
//
// Under-billing weakens the budget signal exactly when an operator needs it.
// Over-billing is visible and annoying. We take visible and annoying.
//
// modelcatalog.Model.CeilingRates() already applies the nil-mirror convention
// that priceTable encodes by hand — an absent cache_read or cache_write mirrors
// the input rate rather than reading as free — and applies it against the
// selected tier's own input, not the base block's.
func buildCatalogPrices() map[string]modelPrice {
	cat := modelcatalog.Default()
	out := make(map[string]modelPrice, 512)
	for _, prov := range cat.Providers() {
		for _, m := range cat.Models(prov) {
			in, outRate, cacheRead, cacheWrite, ok := m.CeilingRates()
			if !ok {
				continue
			}
			// An all-zero cost block is not a claim that the model is free —
			// it is a gap in the upstream catalogue, and the snapshot carries
			// 23 of them (mistral/labs-devstral-small-2512 and the google
			// lyria previews are hosted models priced 0/0). Writing the row
			// would bill them at $0, which is precisely what the comment above
			// says this file must never do. Skipping lets them fall through to
			// providerFallback and be over-estimated, which is the safe
			// direction. It also over-bills the genuinely free OpenRouter
			// ":free" tiers, and that trade is deliberate: free is a claim only
			// "ollama/*" and "local/*" get to make, and they make it in
			// priceTable where a human signed off on it.
			if in == 0 && outRate == 0 {
				continue
			}
			out[prov+"/"+m.ID] = modelPrice{
				InputPerM:       in,
				OutputPerM:      outRate,
				CachedInputPerM: cacheRead,
				CacheWritePerM:  cacheWrite,
			}
		}
	}
	return out
}

// catalogPrice resolves a normalized (provider, model) pair against the
// snapshot. Callers pass the already-lowercased/trimmed values lookupPrice
// computed, which is why this does no normalization of its own — the catalogue
// keys are normalized at decode time.
func catalogPrice(provider, model string) (modelPrice, bool) {
	p, ok := catalogPrices()[provider+"/"+model]
	return p, ok
}
