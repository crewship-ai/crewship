package main

// `crewship model price` — the human-readable window into the pricing chain.
//
// paymaster resolves a rate in five steps (hand-written table → provider
// wildcard → embedded models.dev catalog → provider ceiling), and until now the
// only way to find out which one answered was to read pricing.go and guess.
// That matters because the five steps carry very different confidence: a
// hand-verified row is a number someone checked against an invoice, the catalog
// is a snapshot that can be a release behind, and the ceiling is deliberately an
// over-estimate. An operator staring at a cost line needs to know which of those
// they are looking at before they trust it.
//
// Local by construction: the rate card and the catalog are both compiled into
// this binary, so the command needs no server, no token and no workspace. It is
// the same code path internal/paymaster takes at ledger-write time, asked the
// same question ahead of time.

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/modelcatalog"
	"github.com/crewship-ai/crewship/internal/paymaster"
)

// rateCard mirrors paymaster's unexported modelPrice. It has to be re-declared
// here rather than reused because the type is unexported — paymaster.RateCard
// hands back a value whose exported fields are readable but whose type cannot
// be named outside the package, so it cannot be a struct field or a JSON
// document on its own. Copying the four floats across is the whole conversion.
type rateCard struct {
	InputPerM       float64 `json:"input_per_mtok" yaml:"input_per_mtok"`
	OutputPerM      float64 `json:"output_per_mtok" yaml:"output_per_mtok"`
	CachedInputPerM float64 `json:"cached_input_per_mtok" yaml:"cached_input_per_mtok"`
	CacheWritePerM  float64 `json:"cache_write_per_mtok" yaml:"cache_write_per_mtok"`
}

// cardFor asks paymaster what it would bill for this pair — the real lookup,
// not a re-implementation of it.
func cardFor(provider, model string) rateCard {
	p := paymaster.RateCard(provider, model)
	return rateCard{
		InputPerM:       p.InputPerM,
		OutputPerM:      p.OutputPerM,
		CachedInputPerM: p.CachedInputPerM,
		CacheWritePerM:  p.CacheWritePerM,
	}
}

// rateSource names the step of paymaster's lookup that produced the rate.
type rateSource string

const (
	rateFromTable    rateSource = "table"
	rateFromWildcard rateSource = "wildcard"
	rateFromCatalog  rateSource = "catalog"
	rateFromFallback rateSource = "fallback"
	// rateFromNone is what paymaster reports when nothing prices the pair at
	// all — an unknown vendor. It replaced a "free" value that the old inference
	// layer produced for BOTH an ollama-style wildcard and an unknown vendor;
	// ExplainRate tells those apart, and conflating them told an operator that a
	// vendor typo was a free local model.
	rateFromNone rateSource = "none"
)

// describe is the one-line explanation printed next to the label. It says what
// the number IS, because "catalog" on its own does not tell an operator whether
// to trust it.
func (s rateSource) describe() string {
	switch s {
	case rateFromTable:
		return "hand-written rate card, verified against the provider's published prices (internal/paymaster/pricing.go)"
	case rateFromWildcard:
		return "provider wildcard row \"<provider>/*\" — every model of this provider bills at this rate"
	case rateFromCatalog:
		return "embedded models.dev snapshot (internal/modelcatalog) — accurate as of the snapshot's fetch date, not hand-verified"
	case rateFromFallback:
		return "provider ceiling — this model has no rate anywhere, so the provider's most expensive known tier is used and the cost is an OVER-estimate"
	case rateFromNone:
		return "no rate anywhere for this provider — check the spelling; an unknown vendor bills $0 and the operator is meant to notice"
	}
	return string(s)
}

// priceProbeModel is a model id that cannot match any row in any of the four
// sources: priceTable and the catalog are both keyed by normalized names, and a
// NUL byte survives normalization while appearing in no catalogue on earth.
// Pricing it therefore yields exactly what the chain returns when the exact and
// catalog steps both miss — the wildcard, or the ceiling below it.
const priceProbeModel = "\x00crewship-price-probe"

// PriceExplain is the resolved rate plus enough of its neighbourhood to explain
// the resolution.
//
// The source is paymaster's own answer, not an inference: ExplainRate walks the
// same tables lookupPrice bills from. This file used to deduce it from outside
// the package by comparing values, which mislabelled two whole classes of rate —
// see the ExplainRate comment for what that cost.
type PriceExplain struct {
	Provider string     `json:"provider" yaml:"provider"`
	Model    string     `json:"model" yaml:"model"`
	Rates    rateCard   `json:"rates" yaml:"rates"`
	Source   rateSource `json:"rate_source" yaml:"rate_source"`
	Detail   string     `json:"rate_source_detail" yaml:"rate_source_detail"`
	// Catalog is the snapshot's rate for this pair when it has one, whether or
	// not it won. A shadowed catalog row is worth printing: it is how an
	// operator sees that the hand-written table disagrees with upstream.
	Catalog *rateCard `json:"catalog_rates,omitempty" yaml:"catalog_rates,omitempty"`
	// UnknownModel is what this provider bills for a model nothing prices —
	// the wildcard or the ceiling. Printing it gives the resolved number a
	// scale to be read against.
	UnknownModel rateCard `json:"unknown_model_rates" yaml:"unknown_model_rates"`
}

// explainRate resolves (provider, model) and reports which step of paymaster's
// lookup produced the rate, plus enough of the neighbourhood to read it against:
// the catalogue's own figure when it has one (shadowed or not), and what an
// unknown model of the same provider would bill.
func explainRate(provider, model string) PriceExplain {
	// Normalized the same way paymaster.lookupPrice normalizes, so the pair
	// this reports on is the pair that would be billed.
	prov := canonProviderID(provider)
	mod := canonProviderID(model)

	resolved, src := explainedCardFor(prov, mod)
	unknown := cardFor(prov, priceProbeModel)
	cat, hasCat := catalogCard(prov, mod)

	ex := PriceExplain{
		Provider:     prov,
		Model:        mod,
		Rates:        resolved,
		UnknownModel: unknown,
	}
	if hasCat {
		c := cat
		ex.Catalog = &c
	}

	// The source comes from paymaster itself. It used to be DEDUCED here by
	// re-querying RateCard with a probe model and comparing values, which is
	// not decidable from outside the package: two steps that happen to agree
	// are indistinguishable, and that produced two wrong labels — every tiered
	// model reported as hand-verified, and a hand-written row equal to the
	// provider ceiling reported as `fallback`.
	ex.Source = rateSource(src)
	ex.Detail = ex.Source.describe()
	return ex
}

// catalogCard is the snapshot's rate for a pair, reproducing the one filter
// paymaster applies on top of Model.Rates: a model whose input AND output are
// both zero is SKIPPED rather than billed at $0 (see catalog_pricing.go). Without
// that filter this would report a catalog hit on a row the chain never consults.
func catalogCard(provider, model string) (rateCard, bool) {
	m, ok := modelcatalog.Default().Lookup(provider, model)
	if !ok {
		return rateCard{}, false
	}
	return cardOfCatalogModel(m)
}

// cardOfCatalogModel uses the SAME accessor paymaster bills with — CeilingRates,
// not Rates. They differ on the 76 tiered models in the snapshot, so reading base
// rates here would print a "catalog says" figure that is not what the catalogue
// would actually charge.
func cardOfCatalogModel(m modelcatalog.Model) (rateCard, bool) {
	in, out, cacheRead, cacheWrite, ok := m.CeilingRates()
	if !ok || (in == 0 && out == 0) {
		return rateCard{}, false
	}
	return rateCard{
		InputPerM:       in,
		OutputPerM:      out,
		CachedInputPerM: cacheRead,
		CacheWritePerM:  cacheWrite,
	}, true
}

// priceChannel is one billed channel of a call: what it costs per million
// tokens, how many of them there were, and the product.
type priceChannel struct {
	Name    string  `json:"channel" yaml:"channel"`
	Tokens  int64   `json:"tokens" yaml:"tokens"`
	PerMTok float64 `json:"per_mtok" yaml:"per_mtok"`
	CostUSD float64 `json:"cost_usd" yaml:"cost_usd"`
}

// modelPriceResult is the JSON contract an agent parses.
type modelPriceResult struct {
	PriceExplain `json:",inline" yaml:",inline"`
	Channels     []priceChannel `json:"channels" yaml:"channels"`
	TotalUSD     float64        `json:"total_usd" yaml:"total_usd"`
}

// priceUsage is the token count for each of the four channels.
type priceUsage struct {
	In          int64
	Out         int64
	CachedIn    int64
	CacheCreate int64
}

// priceCall builds the full result. The TOTAL comes from paymaster.Estimate —
// the same function that writes cost_ledger — while the per-channel lines are
// computed here from the resolved card. They must agree, and
// TestPriceCall_ChannelsSumToEstimate is what keeps them agreeing: a channel
// this command forgets to show would otherwise silently widen the gap between
// the breakdown and the number that gets billed.
func priceCall(provider, model string, u priceUsage) modelPriceResult {
	ex := explainRate(provider, model)
	const perM = 1_000_000.0
	channel := func(name string, toks int64, rate float64) priceChannel {
		return priceChannel{Name: name, Tokens: toks, PerMTok: rate, CostUSD: float64(toks) * rate / perM}
	}
	return modelPriceResult{
		PriceExplain: ex,
		Channels: []priceChannel{
			channel("input", u.In, ex.Rates.InputPerM),
			channel("output", u.Out, ex.Rates.OutputPerM),
			channel("cached input", u.CachedIn, ex.Rates.CachedInputPerM),
			channel("cache write", u.CacheCreate, ex.Rates.CacheWritePerM),
		},
		TotalUSD: paymaster.Estimate(ex.Provider, ex.Model, u.In, u.Out, u.CachedIn, u.CacheCreate),
	}
}

var modelPriceCmd = &cobra.Command{
	Use:   "price",
	Short: "Resolve the rate card for a model and estimate what a call costs",
	Long: `Print the per-million-token rates paymaster will bill a (provider, model)
pair at, which of its four lookup steps produced them, and the cost of a call
with the given token counts.

The lookup order is: the hand-written rate card, then a "<provider>/*" wildcard,
then the embedded models.dev snapshot, then the provider's ceiling. Knowing
which one answered is the point of the command — a ceiling rate is a deliberate
over-estimate for a model nothing prices, and reading it as a real price is how
a budget line stops being believed.

--in is FRESH input: tokens that were not served from the prompt cache. Cached
reads go in --cached and cache writes in --cache-write, because all four
channels are priced separately.

Runs entirely locally: the rate card and the catalog are compiled into this
binary, so no server, token or workspace is needed.

Examples:
  crewship model price --provider anthropic --model claude-sonnet-5 --in 12000 --out 800
  crewship model price --provider openai --model gpt-5.5 --in 3000 --out 500 --cached 9000
  crewship model price --provider openrouter --model qwen/qwen3-coder-flash --format json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, _ := cmd.Flags().GetString("provider")
		model, _ := cmd.Flags().GetString("model")
		if strings.TrimSpace(provider) == "" {
			// knownProviderIDs (cmd_model.go) is registry ∪ catalog, which is
			// exactly the set that can be priced: a provider with a codec but
			// no catalog row still has a hand-written table or a ceiling, and
			// a catalog-only provider is priceable without being callable.
			return cli.WithExitCode(fmt.Errorf("--provider is required (%s)", strings.Join(knownProviderIDs(), ", ")), cli.ExitValidation)
		}
		if strings.TrimSpace(model) == "" {
			return cli.WithExitCode(fmt.Errorf("--model is required"), cli.ExitValidation)
		}

		var u priceUsage
		for _, f := range []struct {
			flag string
			dst  *int64
		}{
			{"in", &u.In},
			{"out", &u.Out},
			{"cached", &u.CachedIn},
			{"cache-write", &u.CacheCreate},
		} {
			v, _ := cmd.Flags().GetInt64(f.flag)
			// paymaster clamps negatives to zero rather than crediting the
			// ledger. Refusing here instead of silently clamping means a
			// scripted caller that computed a negative count finds out.
			if v < 0 {
				return cli.WithExitCode(fmt.Errorf("--%s must not be negative", f.flag), cli.ExitValidation)
			}
			*f.dst = v
		}

		res := priceCall(provider, model, u)
		return resolvedFormatter(cmd).AutoHuman(res, func() { printModelPrice(res) })
	},
}

func printModelPrice(res modelPriceResult) {
	fmt.Printf("%s%s/%s%s\n", cli.Bold, res.Provider, res.Model, cli.Reset)
	fmt.Printf("  rate source  %s%s%s\n", cli.Bold, res.Source, cli.Reset)
	fmt.Printf("               %s%s%s\n\n", cli.Dim, res.Detail, cli.Reset)

	fmt.Printf("  %-14s %12s %12s %14s\n", "channel", "tokens", "$/Mtok", "cost")
	for _, c := range res.Channels {
		fmt.Printf("  %-14s %12d %12s %14s\n", c.Name, c.Tokens, formatRate(c.PerMTok), formatCostUSD(c.CostUSD))
	}
	fmt.Printf("  %-14s %12s %12s %s%14s%s\n", "total", "", "", cli.Bold, formatCostUSD(res.TotalUSD), cli.Reset)

	if res.Catalog != nil && res.Source != rateFromCatalog {
		fmt.Printf("\n  %scatalog says%s  %s %s(shadowed by the %s rate above)%s\n",
			cli.Dim, cli.Reset, formatCardLine(*res.Catalog), cli.Dim, res.Source, cli.Reset)
	}
	// Only worth printing when it is not the number already above: for a
	// wildcard, a ceiling or a free provider the resolved rate IS the
	// unknown-model rate, and repeating it reads as two different facts.
	if res.Rates != res.UnknownModel {
		fmt.Printf("\n  %sunknown %s model bills at%s  %s\n",
			cli.Dim, res.Provider, cli.Reset, formatCardLine(res.UnknownModel))
	}
}

// formatCardLine renders the four channels of a rate card on one line.
func formatCardLine(c rateCard) string {
	return fmt.Sprintf("in %s  out %s  cached-in %s  cache-write %s",
		formatRate(c.InputPerM), formatRate(c.OutputPerM),
		formatRate(c.CachedInputPerM), formatRate(c.CacheWritePerM))
}

// formatRate prints a per-million rate. Four decimals because the cheap tiers
// are already at three ($0.0252 for a deepseek cache read) and a rate that
// rounds to $0.00 is indistinguishable from free.
func formatRate(v float64) string { return fmt.Sprintf("$%.4f", v) }

// formatCostUSD prints one call's cost at six decimals, not the four the
// aggregate cost views use. A single completion is routinely worth $0.00003 —
// four decimals renders that as "$0.0000", which is the one value this command
// exists to distinguish from a real zero.
func formatCostUSD(v float64) string { return fmt.Sprintf("$%.6f", v) }

func init() {
	modelPriceCmd.Flags().String("provider", "", "Provider id the model is billed under (the paymaster pricing key)")
	modelPriceCmd.Flags().String("model", "", "Model id to price")
	modelPriceCmd.Flags().Int64("in", 0, "Fresh input tokens (not served from cache)")
	modelPriceCmd.Flags().Int64("out", 0, "Output tokens")
	modelPriceCmd.Flags().Int64("cached", 0, "Cached input tokens (cache reads)")
	modelPriceCmd.Flags().Int64("cache-write", 0, "Cache creation tokens (cache writes)")
	modelCmd.AddCommand(modelPriceCmd)
}

// explainedCardFor asks paymaster both questions at once: what it would bill,
// and which step of its own lookup produced it.
func explainedCardFor(provider, model string) (rateCard, paymaster.RateSource) {
	p, src := paymaster.ExplainRate(provider, model)
	return rateCard{
		InputPerM:       p.InputPerM,
		OutputPerM:      p.OutputPerM,
		CachedInputPerM: p.CachedInputPerM,
		CacheWritePerM:  p.CacheWritePerM,
	}, src
}
