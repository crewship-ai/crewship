package paymaster

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/modelcatalog"
)

// The catalogue is a fallback for models nobody has hand-priced yet. The tests
// that matter are about its POSITION in lookupPrice — a bulk import that
// outranked a verified correction, or that outranked the local-runtime
// wildcard, would silently change what the ledger bills.

func TestBuildCatalogPrices_IsPopulated(t *testing.T) {
	prices := catalogPrices()
	if len(prices) == 0 {
		t.Fatal("the embedded catalogue produced no rates at all")
	}
	for key, p := range prices {
		if !strings.Contains(key, "/") {
			t.Errorf("key %q is not <provider>/<model>", key)
		}
		if p.InputPerM < 0 || p.OutputPerM < 0 || p.CachedInputPerM < 0 || p.CacheWritePerM < 0 {
			t.Errorf("%s: negative rate %+v", key, p)
		}
	}
}

// A model the catalogue carries no usable cost for must produce NO row, so the
// lookup falls through to providerFallback — the ceiling. A zero row would bill
// a hosted model at $0, which is the failure the rate card exists to prevent.
//
// "No usable cost" is two cases, not one: an absent cost block, and a cost
// block of all zeros. The snapshot carries 23 of the latter, and they are not
// free models — mistral/labs-devstral-small-2512 and the google lyria previews
// are hosted. Asserting only the absent case is what let the zero rows through.
func TestBuildCatalogPrices_SkipsCostlessModels(t *testing.T) {
	cat := modelcatalog.Default()
	prices := catalogPrices()

	var checkedCostless, checkedZero, checkedPriced int
	for _, prov := range cat.Providers() {
		for _, m := range cat.Models(prov) {
			key := prov + "/" + m.ID
			in, out, cacheRead, cacheWrite, ok := m.CeilingRates()
			got, present := prices[key]
			if !ok {
				checkedCostless++
				if present {
					t.Errorf("%s has no cost in the catalogue but got a rate row %+v", key, got)
				}
				continue
			}
			if in == 0 && out == 0 {
				checkedZero++
				if present {
					t.Errorf("%s is priced 0/0 in the catalogue but got a rate row %+v — it must fall through to providerFallback", key, got)
				}
				continue
			}
			checkedPriced++
			if !present {
				t.Errorf("%s has a cost in the catalogue but no rate row", key)
				continue
			}
			// The nil-mirror convention lives in CeilingRates(); the bridge
			// must copy it through unchanged rather than re-deriving it.
			want := modelPrice{
				InputPerM:       in,
				OutputPerM:      out,
				CachedInputPerM: cacheRead,
				CacheWritePerM:  cacheWrite,
			}
			if got != want {
				t.Errorf("%s: rate row = %+v, want %+v", key, got, want)
			}
		}
	}
	if checkedPriced == 0 {
		t.Error("no priced model in the embedded catalogue — the snapshot looks empty")
	}
	if checkedZero == 0 {
		t.Error("no 0/0-priced model in the embedded catalogue — this test's whole point is that they exist and must be skipped; if a refresh really removed them all, delete this guard deliberately")
	}
	t.Logf("checked %d priced, %d costless and %d zero-priced catalogue models", checkedPriced, checkedCostless, checkedZero)
}

// An absent cache_read or cache_write mirrors the input rate, never zero. Zero
// would mean "cached input is free", which under-bills every cache hit.
//
// The base block is the exact witness only for untiered models — a tiered
// model's row comes from whichever block CeilingRates selected, so the mirror
// is taken against THAT block's input. The tiered half of the check is
// therefore looser by necessity: the rate must be either a rate the catalogue
// published somewhere on that model, or the row's own input. The precise
// per-block rule is pinned in modelcatalog's own tests.
func TestBuildCatalogPrices_NilCacheRatesMirrorInput(t *testing.T) {
	cat := modelcatalog.Default()
	prices := catalogPrices()

	for _, prov := range cat.Providers() {
		for _, m := range cat.Models(prov) {
			if m.Cost == nil {
				continue
			}
			p, ok := prices[prov+"/"+m.ID]
			if !ok {
				continue
			}
			if len(m.Cost.Tiers) == 0 {
				if m.Cost.CacheRead == nil && p.CachedInputPerM != p.InputPerM {
					t.Errorf("%s/%s: no cache_read in the catalogue, CachedInputPerM = %v, want it to mirror InputPerM %v",
						prov, m.ID, p.CachedInputPerM, p.InputPerM)
				}
				if m.Cost.CacheWrite == nil && p.CacheWritePerM != p.InputPerM {
					t.Errorf("%s/%s: no cache_write in the catalogue, CacheWritePerM = %v, want it to mirror InputPerM %v",
						prov, m.ID, p.CacheWritePerM, p.InputPerM)
				}
				continue
			}

			published := func(pick func(modelcatalog.CostTier) *float64, base *float64) []float64 {
				var out []float64
				if base != nil {
					out = append(out, *base)
				}
				for _, tier := range m.Cost.Tiers {
					if v := pick(tier); v != nil {
						out = append(out, *v)
					}
				}
				return out
			}
			readRates := published(func(t modelcatalog.CostTier) *float64 { return t.CacheRead }, m.Cost.CacheRead)
			writeRates := published(func(t modelcatalog.CostTier) *float64 { return t.CacheWrite }, m.Cost.CacheWrite)

			if !containsFloat(readRates, p.CachedInputPerM) && p.CachedInputPerM != p.InputPerM {
				t.Errorf("%s/%s: CachedInputPerM = %v is neither a published cache_read %v nor the row's input %v",
					prov, m.ID, p.CachedInputPerM, readRates, p.InputPerM)
			}
			if !containsFloat(writeRates, p.CacheWritePerM) && p.CacheWritePerM != p.InputPerM {
				t.Errorf("%s/%s: CacheWritePerM = %v is neither a published cache_write %v nor the row's input %v",
					prov, m.ID, p.CacheWritePerM, writeRates, p.InputPerM)
			}
		}
	}
}

func containsFloat(haystack []float64, needle float64) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

// Tiered models are billed at their CEILING, deliberately. This is the test
// that says so out loud, because the alternative reading — "the catalogue rate
// is cost.input" — is what a reader assumes and what the code did before.
//
// Estimate has no context-size argument, so nothing here can know which tier a
// given call actually landed in. Given that, the only two options are the base
// rate (under-bills a long-context call, by up to 6.7× in this snapshot) and
// the ceiling (over-bills a short one). Over-billing keeps the budget
// warn/exceed signal firing; under-billing spends money the operator was told
// they had. See buildCatalogPrices for the full argument.
func TestBuildCatalogPrices_TieredModelsBillAtTheCeiling(t *testing.T) {
	cat := modelcatalog.Default()
	prices := catalogPrices()

	var tiered, dearer int
	for _, prov := range cat.Providers() {
		for _, m := range cat.Models(prov) {
			if m.Cost == nil || len(m.Cost.Tiers) == 0 {
				continue
			}
			key := prov + "/" + m.ID
			p, ok := prices[key]
			if !ok {
				continue
			}
			tiered++

			ceilIn, ceilOut, ceilRead, ceilWrite, _ := m.CeilingRates()
			want := modelPrice{
				InputPerM:       ceilIn,
				OutputPerM:      ceilOut,
				CachedInputPerM: ceilRead,
				CacheWritePerM:  ceilWrite,
			}
			if p != want {
				t.Errorf("%s: rate row = %+v, want the ceiling %+v", key, p, want)
			}

			baseIn, _, _, _, _ := m.Rates()
			if p.InputPerM < baseIn {
				t.Errorf("%s: InputPerM %v is below the base rate %v — that is an under-estimate on every call",
					key, p.InputPerM, baseIn)
			}
			if p.InputPerM > baseIn {
				dearer++
			}
		}
	}

	if tiered == 0 {
		t.Fatal("no tiered model reached a rate row; either the snapshot lost cost.tiers or the bridge stopped seeing them")
	}
	if dearer == 0 {
		t.Error("no tiered model priced above its base rate — the ceiling rule is untested by data, which means a regression to base rates would pass this suite")
	}
	t.Logf("%d tiered models billed at the ceiling, %d of them above their base rate", tiered, dearer)
}

// The concrete row, spelled out. openrouter/qwen/qwen3-coder-flash publishes
// three prices for the same model — 0.195 base, 0.325 above 32k, 0.52 above
// 128k — and no priceTable key shadows it, so the catalogue is what the ledger
// bills. Before tier support it billed 0.195 at every context length, which is
// 37% of the invoice on a long call.
func TestLookupPrice_TieredCatalogModelUsesTheCeiling(t *testing.T) {
	const provider, model = "openrouter", "qwen/qwen3-coder-flash"

	m, ok := modelcatalog.Default().Lookup(provider, model)
	if !ok {
		t.Fatalf("%s/%s is not in this snapshot — this test uses it as a fixture, so pick a model the snapshot carries rather than letting the check disappear", provider, model)
	}
	// Lookup succeeding says the model is IN the snapshot, not that it carries
	// a cost block — 23 of them do not. Dereferencing m.Cost on that assumption
	// panics the moment a refresh drops this model's pricing; failing with the
	// fixture named is the useful outcome, because a refresh that removes it is
	// a reason to repoint this test rather than to report ok.
	if m.Cost == nil || len(m.Cost.Tiers) != 2 {
		t.Fatalf("%s/%s no longer has the 2 tiers this test was written against — repoint it at a tiered model instead of skipping, or the tier logic stops being covered", provider, model)
	}

	got := lookupPrice(provider, model)
	want := modelPrice{
		InputPerM:       0.52,
		OutputPerM:      2.6,
		CachedInputPerM: 0.104,
		CacheWritePerM:  0.65,
	}
	if got != want {
		t.Fatalf("lookupPrice(%q, %q) = %+v, want the top tier %+v (0.195/0.975 is the base rate — that is the bug)",
			provider, model, got, want)
	}

	// And the money. A 1M-token prompt above the 128k threshold costs $0.52 on
	// the invoice; billing the base rate would put $0.195 on the ledger.
	if cost := Estimate(provider, model, 1_000_000, 0, 0, 0); cost != 0.52 {
		t.Fatalf("Estimate over 1M input tokens = %v, want 0.52", cost)
	}
}

// RateCard is what the ledger snapshots onto the row at write time, so it must
// carry the same ceiling Estimate charged. A divergence here would produce a
// ledger row whose stored rates do not explain its own cost column.
func TestRateCard_CarriesTheCeilingForTieredModels(t *testing.T) {
	const provider, model = "openrouter", "qwen/qwen3-coder-flash"
	if _, ok := modelcatalog.Default().Lookup(provider, model); !ok {
		t.Fatalf("%s/%s is not in this snapshot — this test uses it as a fixture, so pick a model the snapshot carries rather than letting the check disappear", provider, model)
	}
	card := RateCard(provider, model)
	if card != lookupPrice(provider, model) {
		t.Fatalf("RateCard = %+v, lookupPrice = %+v; they must be the same rate", card, lookupPrice(provider, model))
	}
	if card.InputPerM != 0.52 {
		t.Fatalf("RateCard(%q, %q).InputPerM = %v, want the top tier 0.52", provider, model, card.InputPerM)
	}
}

// The hand-written table still wins over a tiered catalogue row. gemini-2.5-pro
// is the only model where the two sources overlap AND the catalogue is tiered,
// and priceTable independently chose the same upper tier — so this asserts both
// that precedence holds and that the two sources now agree on the number.
func TestLookupPrice_HandWrittenTableStillWinsOnATieredModel(t *testing.T) {
	const key = "google/gemini-2.5-pro"
	want, ok := priceTable[key]
	if !ok {
		t.Fatalf("%s is no longer in priceTable — this test needs a hand-priced tiered model as its fixture; repoint it", key)
	}
	m, inCatalog := modelcatalog.Default().Lookup("google", "gemini-2.5-pro")
	if !inCatalog || m.Cost == nil || len(m.Cost.Tiers) == 0 {
		t.Fatalf("%s is not tiered in this snapshot — repoint this test at one that is, rather than letting the shadowing check disappear", key)
	}

	if got := lookupPrice("google", "gemini-2.5-pro"); got != want {
		t.Fatalf("lookupPrice = %+v, want the hand-written %+v", got, want)
	}
	// Not a requirement, but a drift alarm worth having: the hand table's
	// comment says it picked the upper tier on purpose. If the catalogue's
	// ceiling ever disagrees, one of the two repriced.
	if ceilIn, ceilOut, _, _, _ := m.CeilingRates(); ceilIn != want.InputPerM || ceilOut != want.OutputPerM {
		t.Errorf("catalogue ceiling %v/%v disagrees with priceTable %v/%v; one of the two sources repriced",
			ceilIn, ceilOut, want.InputPerM, want.OutputPerM)
	}
}

// A rate published as an explicit zero stays zero — the mirror rule applies to
// ABSENT rates, not cheap ones.
func TestCatalogPrice_ExplicitZeroIsNotMirrored(t *testing.T) {
	cat := modelcatalog.Default()
	prices := catalogPrices()
	for _, prov := range cat.Providers() {
		for _, m := range cat.Models(prov) {
			if m.Cost == nil || m.Cost.CacheRead == nil || *m.Cost.CacheRead != 0 {
				continue
			}
			p, ok := prices[prov+"/"+m.ID]
			if !ok {
				continue
			}
			if p.CachedInputPerM != 0 {
				t.Errorf("%s/%s: cache_read is an explicit 0, CachedInputPerM = %v", prov, m.ID, p.CachedInputPerM)
			}
		}
	}
}

// The hand-written table wins. priceTable carries verified corrections — Opus
// 4.7 was billed 3× over until someone checked — and an embedded snapshot must
// never silently overwrite one.
func TestLookupPrice_HandWrittenTableBeatsTheCatalogue(t *testing.T) {
	var overlap int
	for key, want := range priceTable {
		if strings.HasSuffix(key, "/*") {
			continue
		}
		prov, mod, found := strings.Cut(key, "/")
		if !found {
			t.Errorf("priceTable key %q is not <provider>/<model>", key)
			continue
		}
		if _, alsoInCatalog := catalogPrices()[key]; !alsoInCatalog {
			continue
		}
		overlap++
		if got := lookupPrice(prov, mod); got != want {
			t.Errorf("%s: lookupPrice = %+v, want the hand-written %+v", key, got, want)
		}
	}
	// Vacuity guard. This test used to t.Log here and pass: a snapshot refresh
	// removing every priceTable/catalogue overlap would leave the precedence
	// rule — the thing the whole file is about — untested, with the suite still
	// green. That is the same silently-disappearing coverage the nine skips in
	// this file were converted away from.
	if overlap == 0 {
		t.Fatal("no priceTable key has a catalogue counterpart — the precedence rule this test exists for is then untested. Repoint the fixtures rather than reporting ok")
	}
}

// The order of the five steps, one row per step.
func TestLookupPrice_Order(t *testing.T) {
	// A model only the catalogue knows about, discovered rather than named, so
	// this does not pin the test to one snapshot's contents.
	var catalogOnlyProv, catalogOnlyMod string
	var catalogOnlyWant modelPrice
	for key, p := range catalogPrices() {
		if _, hand := priceTable[key]; hand {
			continue
		}
		prov, mod, found := strings.Cut(key, "/")
		if !found {
			continue
		}
		// Skip providers whose wildcard would have answered anyway — the point
		// of the row is that step 2 fired, not step 3.
		if _, wild := priceTable[prov+"/*"]; wild {
			continue
		}
		catalogOnlyProv, catalogOnlyMod, catalogOnlyWant = prov, mod, p
		break
	}

	tests := []struct {
		name     string
		provider string
		model    string
		want     modelPrice
		skip     bool
	}{
		{
			name:     "step 1: an exact hand-written match",
			provider: "anthropic", model: "claude-haiku-4-5",
			want: priceTable["anthropic/claude-haiku-4-5"],
		},
		{
			name:     "step 2: a catalogue-only model gets the catalogue rate",
			provider: catalogOnlyProv, model: catalogOnlyMod,
			want: catalogOnlyWant,
			skip: catalogOnlyProv == "",
		},
		{
			// Load-bearing: the catalogue sits BEFORE the wildcard, so a local
			// runtime must have no catalogue entry that could outrank its zero.
			name:     "step 3: a local model is still free via the wildcard",
			provider: "ollama", model: "llama3",
			want: modelPrice{},
		},
		{
			name:     "step 3: an openai-compat local endpoint is still free",
			provider: "local", model: "qwen2.5-7b-instruct",
			want: modelPrice{},
		},
		{
			name:     "step 4: an unknown model on a known provider gets the ceiling",
			provider: "anthropic", model: "claude-not-a-real-model-zzz",
			want: providerFallback["anthropic"],
		},
		{
			name:     "step 4: case and whitespace normalize before the fallback",
			provider: "  OpenAI ", model: " GPT-Not-Real-ZZZ ",
			want: providerFallback["openai"],
		},
		{
			name:     "step 5: a fully unknown provider stays zero",
			provider: "not-a-vendor-zzz", model: "whatever",
			want: modelPrice{},
		},
		{
			name:     "step 5: an empty provider stays zero",
			provider: "", model: "claude-haiku-4-5",
			want: modelPrice{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skip {
				t.Fatal("the embedded catalogue has no model outside priceTable — step 3 of the lookup — the catalogue — is then untestable, which is a fixture problem to fix, not a result to report as ok")
			}
			if got := lookupPrice(tt.provider, tt.model); got != tt.want {
				t.Errorf("lookupPrice(%q, %q) = %+v, want %+v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

// The wildcard rows are the ones the catalogue must never reach past. Spelled
// out separately because "a local call costs nothing" is a promise the ledger
// makes to the operator, not an implementation detail.
func TestLookupPrice_LocalRuntimesStayFree(t *testing.T) {
	models := []string{"llama3", "qwen2.5:7b", "gpt-oss:20b", "claude-haiku-4-5", "gpt-5.5"}
	for _, prov := range []string{"ollama", "local"} {
		for _, m := range models {
			t.Run(prov+"/"+m, func(t *testing.T) {
				if got := lookupPrice(prov, m); got != (modelPrice{}) {
					t.Errorf("lookupPrice(%q, %q) = %+v, want free", prov, m, got)
				}
			})
		}
	}
}

// No fallback row may sit under the highest rate the snapshot publishes for
// that provider, on ANY of the four channels.
//
// This exists because the first version of those rows got it wrong in a way
// review caught and tests did not: input and output were computed from the
// snapshot, but the two cache channels were derived from the input rate by
// Anthropic's ratios. That billed a cache-read-heavy unknown openrouter slug at
// $7.50 against a real ceiling of $150. The row had been added precisely to
// stop unknown models billing too little, and it under-billed by 20x on a
// channel nobody checked.
//
// The check walks the shipped snapshot rather than hardcoding the maxima, so a
// refresh that raises a rate fails here instead of silently lowering the floor.
// Note it compares against CeilingRates(), which applies the nil-mirror
// convention — a model with no cache_read charges its input rate for cache
// reads and is therefore a cache-read candidate. Reading raw cost keys is the
// exact mistake this test was written to prevent recurring.
func TestProviderFallback_NoRowIsUnderTheSnapshotCeiling(t *testing.T) {
	cat := modelcatalog.Default()
	// Every provider with a fallback row, not just the two gateways. Scoping the
	// invariant to gateways is how four stale rows survived — openai's ceiling
	// was 7.5x below what the snapshot already published.
	for prov := range providerFallback {
		if providerFallback[prov] == (modelPrice{}) {
			continue // ollama/local: deliberately free, no ceiling to hold
		}
		row, ok := providerFallback[prov]
		if !ok {
			t.Errorf("%s has no providerFallback row — an unknown model of this provider bills at $0", prov)
			continue
		}
		var maxIn, maxOut, maxCacheRead, maxCacheWrite float64
		var seen int
		for _, m := range cat.Models(prov) {
			in, out, cacheRead, cacheWrite, has := m.CeilingRates()
			if !has {
				continue
			}
			seen++
			maxIn = maxFloat(maxIn, in)
			maxOut = maxFloat(maxOut, out)
			maxCacheRead = maxFloat(maxCacheRead, cacheRead)
			maxCacheWrite = maxFloat(maxCacheWrite, cacheWrite)
		}
		if seen == 0 {
			t.Errorf("%s: snapshot carries no priced model — the trim no longer covers this provider, so this test is vacuous", prov)
			continue
		}
		for _, c := range []struct {
			channel string
			got     float64
			want    float64
		}{
			{"input", row.InputPerM, maxIn},
			{"output", row.OutputPerM, maxOut},
			{"cached input", row.CachedInputPerM, maxCacheRead},
			{"cache write", row.CacheWritePerM, maxCacheWrite},
		} {
			if c.got < c.want {
				t.Errorf("%s %s ceiling = $%g, below the snapshot maximum $%g — an unknown %s model would be under-billed on this channel",
					prov, c.channel, c.got, c.want, prov)
			}
		}
	}
}

func maxFloat(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}

// staleTableRows are hand-written rows the snapshot prices HIGHER than the
// table. The table wins the lookup, so each one under-bills until a human
// re-checks it against the provider's published price and either corrects the
// row or records why the table is deliberately lower.
//
// This list is not an excuse. Adding an entry to silence a failure is the wrong
// move: a table row below the catalogue is under-billing, which is the one
// direction this whole rate card refuses to be wrong in. An exception belongs
// here only after the provider's published price has been checked and the
// reason the catalogue is higher is recorded. There are currently none.
var staleTableRows = map[string]string{}

func TestCorrectedPriceRowsMatchPublishedRates(t *testing.T) {
	tests := map[string]modelPrice{
		"google/gemini-2.5-flash":      {InputPerM: 0.30, OutputPerM: 2.50, CachedInputPerM: 0.03, CacheWritePerM: 0.30},
		"google/gemini-2.5-flash-lite": {InputPerM: 0.10, OutputPerM: 0.40, CachedInputPerM: 0.01, CacheWritePerM: 0.10},
		"openai/gpt-5.4-nano":          {InputPerM: 0.20, OutputPerM: 1.25, CachedInputPerM: 0.02, CacheWritePerM: 0.20},
		// GPT-5.5's >272K context tier. RateCard cannot choose by context
		// length yet, so the conservative ceiling is the only safe row.
		"openai/gpt-5.5": {InputPerM: 10.00, OutputPerM: 45.00, CachedInputPerM: 1.00, CacheWritePerM: 10.00},
	}
	for key, want := range tests {
		if got := priceTable[key]; got != want {
			t.Errorf("priceTable[%q] = %+v, want %+v", key, got, want)
		}
	}
}

// A hand-written row must not be cheaper than what the snapshot says the model
// costs. priceTable wins the lookup by design (it carries verified corrections
// a bulk import must not overwrite), which means a stale row silently
// under-bills for as long as it sits there — and the table is dated months
// before the snapshot.
//
// The existing drift guard only watched google/gemini-2.5-pro. This walks every
// non-wildcard row.
func TestPriceTable_IsNotCheaperThanTheCatalogue(t *testing.T) {
	cat := modelcatalog.Default()
	var checked int
	for key := range priceTable {
		prov, mod, found := strings.Cut(key, "/")
		if !found || mod == "*" {
			continue
		}
		m, ok := cat.Lookup(prov, mod)
		if !ok {
			continue // no catalogue opinion; providerFallback covers the unknown case
		}
		catIn, catOut, _, _, has := m.CeilingRates()
		if !has || (catIn == 0 && catOut == 0) {
			continue
		}
		checked++
		row := priceTable[key]
		if row.InputPerM >= catIn && row.OutputPerM >= catOut {
			if why, listed := staleTableRows[key]; listed {
				t.Errorf("%s is listed as stale (%q) but no longer under-bills — delete the entry", key, why)
			}
			continue
		}
		why, listed := staleTableRows[key]
		if !listed {
			t.Errorf("%s under-bills: table $%g/$%g is below the catalogue's $%g/$%g. Verify the provider's published price and correct the row, or add it to staleTableRows with a reason and a tracking issue.",
				key, row.InputPerM, row.OutputPerM, catIn, catOut)
			continue
		}
		t.Logf("known stale: %s (%s)", key, why)
	}
	if checked < 10 {
		t.Fatalf("only %d table rows had a catalogue counterpart — the trim or the key format changed and this guard has gone vacuous", checked)
	}
}

// ExplainRate must resolve to exactly what lookupPrice bills. Two functions
// walking the same list is the obvious way for a label to drift off the number
// it describes, so this walks a spread of pairs covering every step.
func TestExplainRate_AgreesWithLookupPrice(t *testing.T) {
	pairs := []struct {
		provider, model string
		want            RateSource
	}{
		{"anthropic", "claude-opus-4-7", SourceTable},
		{"ollama", "anything-at-all", SourceWildcard},
		{"local", "qwen2.5-7b-instruct", SourceWildcard},
		{"openrouter", "qwen/qwen3-coder-flash", SourceCatalog},
		{"openrouter", "totally/made-up-slug", SourceFallback},
		{"anthropic", "claude-not-a-real-model-zzz", SourceFallback},
		{"nosuchvendor", "nosuchmodel", SourceNone},
		{"", "", SourceNone},
	}
	for _, tc := range pairs {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			got, src := ExplainRate(tc.provider, tc.model)
			if want := lookupPrice(tc.provider, tc.model); got != want {
				t.Errorf("ExplainRate rate = %+v, lookupPrice = %+v — the label and the bill disagree", got, want)
			}
			if src != tc.want {
				t.Errorf("source = %q, want %q", src, tc.want)
			}
		})
	}
}

// Every row in priceTable is reported as table-sourced. This is what the old
// inference layer could not guarantee: it compared VALUES, so a hand-written row
// that happened to equal the step below it was indistinguishable from that step
// and got the wrong label — openai/o3-pro read as `fallback` because it matched
// the openai ceiling exactly.
//
// The first version of this test pinned that one pair. That was a mistake of the
// same kind it was testing: it depended on a coincidence in the shipped data, and
// raising the openai ceiling to its true snapshot maximum dissolved the
// collision and the test with it. Walking every row asserts the structural
// property instead — ExplainRate consults priceTable FIRST, so membership alone
// decides the label, whatever the rates happen to equal.
func TestExplainRate_EveryTableRowIsReportedAsTable(t *testing.T) {
	var checked int
	for key := range priceTable {
		prov, mod, found := strings.Cut(key, "/")
		if !found || mod == "*" {
			continue // wildcards are their own source
		}
		checked++
		if _, src := ExplainRate(prov, mod); src != SourceTable {
			t.Errorf("%s: source = %q, want %q — a row that exists in the table is table-sourced no matter what its rates equal", key, src, SourceTable)
		}
	}
	if checked < 10 {
		t.Fatalf("only %d non-wildcard rows in priceTable — this guard has gone vacuous", checked)
	}
}
