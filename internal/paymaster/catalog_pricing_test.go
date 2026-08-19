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
			in, out, cacheRead, cacheWrite, ok := m.Rates()
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
			// The nil-mirror convention lives in Rates(); the bridge must copy
			// it through unchanged rather than re-deriving it.
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
			if m.Cost.CacheRead == nil && p.CachedInputPerM != p.InputPerM {
				t.Errorf("%s/%s: no cache_read in the catalogue, CachedInputPerM = %v, want it to mirror InputPerM %v",
					prov, m.ID, p.CachedInputPerM, p.InputPerM)
			}
			if m.Cost.CacheWrite == nil && p.CacheWritePerM != p.InputPerM {
				t.Errorf("%s/%s: no cache_write in the catalogue, CacheWritePerM = %v, want it to mirror InputPerM %v",
					prov, m.ID, p.CacheWritePerM, p.InputPerM)
			}
		}
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
	if overlap == 0 {
		t.Log("no priceTable key is also in the catalogue — the precedence rule is untested by data")
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
				t.Skip("the embedded catalogue has no model outside priceTable to exercise this step")
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
