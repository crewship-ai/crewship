package modelcatalog

import (
	"testing"
)

// TestDefault_DecodesCleanly is the guard on the committed snapshot itself. A
// refresh that truncates the file, or a merge that mangles it, produces an
// empty catalog at runtime with nowhere to report the error — Default is
// deliberately silent. This is the only place that failure becomes visible,
// so it must stay in the suite.
func TestDefault_DecodesCleanly(t *testing.T) {
	if err := DefaultErr(); err != nil {
		t.Fatalf("DefaultErr() = %v, want nil (data/models.dev.json is corrupt)", err)
	}
	c := Default()
	if c == nil {
		t.Fatal("Default() = nil; it must never be nil, even on a decode failure")
	}
	if len(c) == 0 {
		t.Fatal("Default() is empty; the embedded snapshot decoded to nothing")
	}
	// Cached, not re-decoded: same map on every call.
	if again := Default(); len(again) != len(c) {
		t.Fatalf("Default() len = %d then %d; the sync.OnceValues cache is not holding", len(c), len(again))
	}
}

// TestDefault_ProviderCoverage pins the trim. The snapshot is 8 of models.dev's
// ~190 providers, and which 8 is a decision, not an accident: everything that
// appears in paymaster's price table, plus the two gateways (openrouter,
// amazon-bedrock) a Phase-2 provider would reach through the codecs we already
// have. A refresh that silently drops one shows up here.
func TestDefault_ProviderCoverage(t *testing.T) {
	want := []string{
		"amazon-bedrock",
		"anthropic",
		"deepseek",
		"google",
		"mistral",
		"openai",
		"openrouter",
		"xai",
	}

	c := Default()
	for _, id := range want {
		t.Run(id, func(t *testing.T) {
			p, ok := c[id]
			if !ok {
				t.Fatalf("provider %q missing from the snapshot", id)
			}
			if p.ID != id {
				t.Errorf("provider ID = %q, want %q", p.ID, id)
			}
			if len(p.Models) == 0 {
				t.Errorf("provider %q has no models", id)
			}
		})
	}

	// Providers() is sorted, so the trim set is comparable element-wise.
	got := c.Providers()
	if len(got) != len(want) {
		t.Fatalf("Providers() = %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Providers()[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestDefault_MustExistModels pins the ids other packages already hard-code:
// every model in paymaster's priceTable that models.dev also publishes, plus
// the ids llm.curatedModels offers as the fallback picker list. If upstream
// renames one, this is where we find out — not in a $0 ledger row.
//
// Deliberately absent, because models.dev has no equivalent entry: the
// openai/gpt-5* aliases, xai/grok-4.20, xai/grok-4.1-fast,
// mistral/codestral-2508, google/gemini-1.5-* and google/gemini-2.0-flash.
// Those live only in the hand-written table, which is exactly why the catalog
// sits below it in the lookup order rather than replacing it.
func TestDefault_MustExistModels(t *testing.T) {
	cases := []struct {
		provider string
		model    string
	}{
		{"anthropic", "claude-fable-5"},
		{"anthropic", "claude-opus-5"},
		{"anthropic", "claude-opus-4-8"},
		{"anthropic", "claude-opus-4-7"},
		{"anthropic", "claude-opus-4-6"},
		{"anthropic", "claude-sonnet-5"},
		{"anthropic", "claude-sonnet-4-6"},
		{"anthropic", "claude-haiku-4-5"},
		{"openai", "gpt-5.5"},
		{"openai", "gpt-5.4-mini"},
		{"openai", "gpt-5.4-nano"},
		{"openai", "o3-pro"},
		{"openai", "gpt-4o"},
		{"openai", "gpt-4o-mini"},
		{"openai", "o3"},
		{"openai", "o3-mini"},
		{"google", "gemini-2.5-pro"},
		{"google", "gemini-2.5-flash"},
		{"google", "gemini-2.5-flash-lite"},
		{"deepseek", "deepseek-chat"},
		{"deepseek", "deepseek-reasoner"},
		{"amazon-bedrock", "us.anthropic.claude-opus-4-7"},
		{"openrouter", "anthropic/claude-fable-5"},
	}

	c := Default()
	for _, tc := range cases {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			m, ok := c.Lookup(tc.provider, tc.model)
			if !ok {
				t.Fatalf("%s/%s missing from the snapshot", tc.provider, tc.model)
			}
			if m.ID != tc.model {
				t.Errorf("ID = %q, want %q", m.ID, tc.model)
			}
			if m.DisplayName() == "" {
				t.Error("DisplayName() is empty")
			}
			// The uppercase spelling is what internal/api carries.
			if _, ok := c.Lookup(upper(tc.provider), upper(tc.model)); !ok {
				t.Errorf("uppercase spelling %s/%s did not resolve", upper(tc.provider), upper(tc.model))
			}
		})
	}
}

func upper(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'z' {
			out[i] = r - 32
		}
	}
	return string(out)
}

// TestDefault_OpusRateMatchesPriceTable is the unit check that keeps the two
// rate sources in the same currency. paymaster prices in USD per 1,000,000
// tokens and so does models.dev, with no scaling on either side — if that ever
// stopped being true, every catalog-sourced rate would be off by a factor of a
// million and nothing else in the build would notice.
//
// claude-opus-4-7 is the witness because it appears verbatim in both:
// priceTable["anthropic/claude-opus-4-7"] is {5.00, 25.00, 0.50, 6.25}. The
// values are asserted literally rather than imported, because this package
// must not import internal/paymaster (it is a leaf, and paymaster imports it).
func TestDefault_OpusRateMatchesPriceTable(t *testing.T) {
	m, ok := Default().Lookup("anthropic", "claude-opus-4-7")
	if !ok {
		t.Fatal("anthropic/claude-opus-4-7 missing from the snapshot")
	}
	in, out, cacheRead, cacheWrite, ok := m.Rates()
	if !ok {
		t.Fatal("claude-opus-4-7 carries no cost block")
	}
	const (
		wantIn         = 5.00
		wantOut        = 25.00
		wantCacheRead  = 0.50
		wantCacheWrite = 6.25
	)
	if in != wantIn || out != wantOut || cacheRead != wantCacheRead || cacheWrite != wantCacheWrite {
		t.Fatalf("claude-opus-4-7 rates = %v/%v/%v/%v, want %v/%v/%v/%v "+
			"(paymaster priceTable carries the second set; a mismatch means the "+
			"snapshot changed unit or the provider repriced)",
			in, out, cacheRead, cacheWrite, wantIn, wantOut, wantCacheRead, wantCacheWrite)
	}
}

// TestDefault_RatesAreSane sweeps every priced model in the snapshot. A
// negative rate would produce a credit on the ledger; an output rate below the
// input rate is possible but rare enough that it is worth not asserting, so we
// check only the invariants that are true for every real rate card.
func TestDefault_RatesAreSane(t *testing.T) {
	c := Default()
	priced := 0
	for _, prov := range c.Providers() {
		for _, m := range c.Models(prov) {
			in, out, cacheRead, cacheWrite, ok := m.Rates()
			if !ok {
				continue
			}
			priced++
			if in < 0 || out < 0 || cacheRead < 0 || cacheWrite < 0 {
				t.Errorf("%s/%s has a negative rate: %v/%v/%v/%v",
					prov, m.ID, in, out, cacheRead, cacheWrite)
			}
			// The nil-mirror rule must never leave a cache channel unset when
			// input is priced — that is the $0 under-bill this package exists
			// to prevent.
			if in > 0 && cacheRead == 0 && m.Cost.CacheRead == nil {
				t.Errorf("%s/%s: absent cache_read did not mirror input %v", prov, m.ID, in)
			}
			if in > 0 && cacheWrite == 0 && m.Cost.CacheWrite == nil {
				t.Errorf("%s/%s: absent cache_write did not mirror input %v", prov, m.ID, in)
			}
		}
	}
	if priced == 0 {
		t.Fatal("no priced models in the snapshot; the cost block is not decoding")
	}
	t.Logf("%d priced models across %d providers", priced, len(c))
}

// TestDefault_NormalizationHeldOnRealData re-asserts the Parse contract
// against the committed file rather than the fixture. An upstream key with a
// stray capital or a trailing space would otherwise be unreachable through
// Lookup while still counting toward len(Default()).
func TestDefault_NormalizationHeldOnRealData(t *testing.T) {
	c := Default()
	for id, p := range c {
		if id == "" {
			t.Error("snapshot contains an empty provider key")
		}
		if id != canon(id) {
			t.Errorf("provider key %q is not canonical", id)
		}
		if p.ID != id {
			t.Errorf("provider %q has ID %q; Parse must set ID from the key", id, p.ID)
		}
		for mid, m := range p.Models {
			if mid == "" {
				t.Errorf("provider %q contains an empty model key", id)
			}
			if mid != canon(mid) {
				t.Errorf("model key %q/%q is not canonical", id, mid)
			}
			if m.ID != mid {
				t.Errorf("model %q/%q has ID %q; Parse must set ID from the key", id, mid, m.ID)
			}
		}
	}
}
