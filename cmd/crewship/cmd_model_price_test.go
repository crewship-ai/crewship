package main

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/paymaster"
)

// --- unit: which lookup step answered ---

// The model ids below are pinned against the embedded snapshot on purpose,
// the same way internal/modelcatalog/embed_test.go pins them: a refresh that
// adds a hand-written row for gpt-4o, or drops labs-devstral-small-2512,
// changes which step answers, and that is a fact this command reports. A test
// that went along with it silently would be reporting nothing.
func TestExplainRate_Source(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     rateSource
	}{
		// The documented blind spot: priceTable and the snapshot agree on
		// this model to the cent, so the hand-written row is invisible from
		// out here and the result reads as catalog-sourced. See explainRate.
		{"hand-written row the snapshot agrees with", "anthropic", "claude-opus-4-7", rateFromCatalog},
		{"hand-written row that disagrees with the catalog", "openai", "gpt-5.5", rateFromTable},
		{"alias row the catalog has no equivalent for", "openai", "gpt-5-mini", rateFromTable},
		{"catalog fills a gap the table leaves", "openai", "gpt-4o", rateFromCatalog},
		{"catalog reaches a provider with no codec", "openrouter", "qwen/qwen3-coder-flash", rateFromCatalog},
		{"unknown model of a known provider hits the ceiling", "anthropic", "claude-not-a-model", rateFromFallback},
		{"catalog row priced 0/0 is skipped, not billed at $0", "mistral", "labs-devstral-small-2512", rateFromFallback},
		{"local runtime is free", "ollama", "qwen2.5:3b", rateFromFree},
		{"unknown model of a gateway hits that gateway's ceiling", "openrouter", "not-a-model", rateFromFallback},
		{"vendor nobody has heard of", "acme-llm", "acme-1", rateFromFree},
		// The API enum form and the Provider.Name() form must land on the
		// same row — the case-sensitivity bug the registry exists to fix.
		{"uppercase provider and model", "OpenAI", "GPT-5.5", rateFromTable},
		{"padded provider", "  anthropic  ", "claude-sonnet-5", rateFromTable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := explainRate(tc.provider, tc.model)
			if got.Source != tc.want {
				t.Errorf("explainRate(%q, %q).Source = %q, want %q (rates %+v, unknown-model %+v)",
					tc.provider, tc.model, got.Source, tc.want, got.Rates, got.UnknownModel)
			}
			if got.Detail == "" {
				t.Errorf("Detail is empty for source %q", got.Source)
			}
			// Whatever the source, the reported rates must be the ones
			// paymaster would actually bill.
			if want := cardFor(tc.provider, tc.model); got.Rates != want {
				t.Errorf("Rates = %+v, want %+v", got.Rates, want)
			}
		})
	}
}

func TestExplainRate_NormalizesIdentity(t *testing.T) {
	got := explainRate("  OpenAI ", " GPT-5.5 ")
	if got.Provider != "openai" || got.Model != "gpt-5.5" {
		t.Errorf("identity = %q/%q, want openai/gpt-5.5", got.Provider, got.Model)
	}
}

// A shadowed catalog row is still reported, because "the table says $4 and
// upstream says $5" is exactly the discrepancy an operator is looking for.
func TestExplainRate_ShadowedCatalogIsStillReported(t *testing.T) {
	got := explainRate("openai", "gpt-5.5")
	if got.Source != rateFromTable {
		t.Fatalf("Source = %q, want %q", got.Source, rateFromTable)
	}
	if got.Catalog == nil {
		t.Fatal("Catalog is nil: the snapshot carries openai/gpt-5.5 and the shadowed rate must survive")
	}
	if *got.Catalog == got.Rates {
		t.Errorf("catalog rate %+v equals the billed rate: this fixture only tests anything while they differ", *got.Catalog)
	}
}

func TestExplainRate_NoCatalogRowForALocalTag(t *testing.T) {
	got := explainRate("ollama", "qwen2.5:3b")
	if got.Catalog != nil {
		t.Errorf("Catalog = %+v, want nil (the snapshot carries no ollama entries)", *got.Catalog)
	}
}

func TestCatalogCard(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		wantOK   bool
		wantIn   float64
	}{
		{"priced model", "openai", "gpt-4o", true, 2.5},
		// The CEILING, not the base block: gpt-5.5 publishes a 272k tier at
		// $10, and that is the rate paymaster bills a catalog hit at.
		{"tiered model reports the tier paymaster bills", "openai", "gpt-5.5", true, 10},
		{"0/0 row is skipped like paymaster skips it", "mistral", "labs-devstral-small-2512", false, 0},
		{"unknown model", "openai", "not-a-model", false, 0},
		{"unknown provider", "acme-llm", "acme-1", false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := catalogCard(tc.provider, tc.model)
			if ok != tc.wantOK {
				t.Fatalf("catalogCard(%q, %q) ok = %v, want %v", tc.provider, tc.model, ok, tc.wantOK)
			}
			if ok && got.InputPerM != tc.wantIn {
				t.Errorf("InputPerM = %v, want %v", got.InputPerM, tc.wantIn)
			}
		})
	}
}

// providerHasWildcard is deduced, so its false-positives are what matter: a
// provider whose catalog rows are reachable must never be read as wildcarded,
// including openai, where the o3-pro row happens to carry the provider
// ceiling's exact rates.
func TestProviderHasWildcard(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		{"openai", false},
		{"anthropic", false},
		{"openrouter", false},
		{"ollama", false}, // no catalog rows at all: no evidence either way
		{"acme-llm", false},
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			if got := providerHasWildcard(tc.provider); got != tc.want {
				t.Errorf("providerHasWildcard(%q) = %v, want %v", tc.provider, got, tc.want)
			}
		})
	}
}

// --- unit: the money ---

// The breakdown and the total come from two different places on purpose — the
// total is paymaster.Estimate, the same call that writes cost_ledger — so they
// have to be pinned against each other. A channel this command forgets to show
// would otherwise open a silent gap between what it prints and what is billed.
func TestPriceCall_ChannelsSumToEstimate(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		usage    priceUsage
	}{
		{"table row, all four channels", "anthropic", "claude-sonnet-5", priceUsage{In: 12000, Out: 800, CachedIn: 40000, CacheCreate: 2000}},
		{"catalog row", "openai", "gpt-4o", priceUsage{In: 1000, Out: 100}},
		{"ceiling", "anthropic", "claude-not-a-model", priceUsage{In: 1000, Out: 100, CachedIn: 50}},
		{"free provider", "ollama", "qwen2.5:3b", priceUsage{In: 999999, Out: 999999}},
		{"no tokens at all", "openai", "gpt-5.5", priceUsage{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := priceCall(tc.provider, tc.model, tc.usage)
			var sum float64
			for _, c := range res.Channels {
				sum += c.CostUSD
			}
			if math.Abs(sum-res.TotalUSD) > 1e-12 {
				t.Errorf("channels sum to %.12f, Estimate says %.12f (channels %+v)", sum, res.TotalUSD, res.Channels)
			}
			if len(res.Channels) != 4 {
				t.Errorf("got %d channels, want 4 (input, output, cached input, cache write)", len(res.Channels))
			}
		})
	}
}

func TestPriceCall_Channels(t *testing.T) {
	// openai/gpt-5.5 is hand-written: $4 in, $24 out, $0.40 cached, $4 write.
	res := priceCall("openai", "gpt-5.5", priceUsage{In: 3000, Out: 500, CachedIn: 9000, CacheCreate: 1000})
	want := []priceChannel{
		{Name: "input", Tokens: 3000, PerMTok: 4.00, CostUSD: 0.012},
		{Name: "output", Tokens: 500, PerMTok: 24.00, CostUSD: 0.012},
		{Name: "cached input", Tokens: 9000, PerMTok: 0.40, CostUSD: 0.0036},
		{Name: "cache write", Tokens: 1000, PerMTok: 4.00, CostUSD: 0.004},
	}
	if len(res.Channels) != len(want) {
		t.Fatalf("channels = %+v", res.Channels)
	}
	for i, w := range want {
		got := res.Channels[i]
		if got.Name != w.Name || got.Tokens != w.Tokens || got.PerMTok != w.PerMTok || math.Abs(got.CostUSD-w.CostUSD) > 1e-12 {
			t.Errorf("channel %d = %+v, want %+v", i, got, w)
		}
	}
	if wantTotal := paymaster.Estimate("openai", "gpt-5.5", 3000, 500, 9000, 1000); res.TotalUSD != wantTotal {
		t.Errorf("TotalUSD = %v, want %v", res.TotalUSD, wantTotal)
	}
}

// --- unit: rendering ---

func TestFormatCostAndRate(t *testing.T) {
	tests := []struct {
		name     string
		rate     float64
		wantRate string
		cost     float64
		wantCost string
	}{
		// Six decimals on the cost is the whole point: one haiku call is
		// worth $0.00003, which four decimals renders as a flat "$0.0000".
		{"one small call", 1.00, "$1.0000", 0.00003, "$0.000030"},
		{"free", 0, "$0.0000", 0, "$0.000000"},
		{"cheap cache read", 0.0252, "$0.0252", 0.0000252, "$0.000025"},
		{"expensive", 80, "$80.0000", 1.5, "$1.500000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatRate(tc.rate); got != tc.wantRate {
				t.Errorf("formatRate(%v) = %q, want %q", tc.rate, got, tc.wantRate)
			}
			if got := formatCostUSD(tc.cost); got != tc.wantCost {
				t.Errorf("formatCostUSD(%v) = %q, want %q", tc.cost, got, tc.wantCost)
			}
		})
	}
}

func TestPrintModelPrice_DoesNotPanic(t *testing.T) {
	for _, tc := range []struct {
		provider, model string
	}{
		{"openai", "gpt-5.5"},        // shadowed catalog row present
		{"openai", "gpt-4o"},         // catalog is the source
		{"ollama", "qwen2.5:3b"},     // free, no catalog row
		{"acme-llm", "acme-1"},       // nothing knows anything
		{"anthropic", "claude-nope"}, // ceiling
	} {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			printModelPrice(priceCall(tc.provider, tc.model, priceUsage{In: 10, Out: 10, CachedIn: 10, CacheCreate: 10}))
		})
	}
}

// --- acceptance: the built binary, with no config and no server ---

// The command is local, so the proof it is local is running it with a config
// path that does not exist and no --server: anything that reached for a client
// would fail here.
func TestAcceptance_ModelPrice_Offline(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "model", "price",
		"--provider", "openai", "--model", "gpt-5.5",
		"--in", "3000", "--out", "500", "--cached", "9000",
		"--format", "json")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+filepath.Join(t.TempDir(), "absent.yaml"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}

	var got modelPriceResult
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Provider != "openai" || got.Model != "gpt-5.5" {
		t.Errorf("identity = %q/%q", got.Provider, got.Model)
	}
	if got.Source != rateFromTable {
		t.Errorf("rate_source = %q, want %q", got.Source, rateFromTable)
	}
	if want := paymaster.Estimate("openai", "gpt-5.5", 3000, 500, 9000, 0); got.TotalUSD != want {
		t.Errorf("total_usd = %v, want %v", got.TotalUSD, want)
	}
	if got.Rates.InputPerM != 4.00 {
		t.Errorf("input rate = %v, want 4", got.Rates.InputPerM)
	}
}

func TestAcceptance_ModelPrice_Human(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cmd := exec.Command(bin, "model", "price", "--provider", "anthropic", "--model", "claude-sonnet-5",
		"--in", "12000", "--out", "800", "--no-color")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+filepath.Join(t.TempDir(), "absent.yaml"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\noutput: %s", err, out)
	}
	for _, want := range []string{"anthropic/claude-sonnet-5", "rate source", string(rateFromTable), "$0.048000"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

// Bad input is exit 2, not the bare exit 1 a plain fmt.Errorf would give.
func TestAcceptance_ModelPrice_ValidationExitCodes(t *testing.T) {
	bin := buildCrewshipBinary(t)

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"no provider", []string{"model", "price", "--model", "gpt-5.5"}, cli.ExitValidation},
		{"no model", []string{"model", "price", "--provider", "openai"}, cli.ExitValidation},
		{"negative input", []string{"model", "price", "--provider", "openai", "--model", "gpt-5.5", "--in", "-1"}, cli.ExitValidation},
		{"negative cached", []string{"model", "price", "--provider", "openai", "--model", "gpt-5.5", "--cached", "-5"}, cli.ExitValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin, tc.args...)
			cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+filepath.Join(t.TempDir(), "absent.yaml"))
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected failure, got success:\n%s", out)
			}
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("err = %v (%T), want *exec.ExitError", err, err)
			}
			if ee.ExitCode() != tc.want {
				t.Errorf("exit = %d, want %d\noutput: %s", ee.ExitCode(), tc.want, out)
			}
		})
	}
}
