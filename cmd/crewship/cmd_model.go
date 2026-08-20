package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/modelcatalog"
	"github.com/spf13/cobra"
)

// modelCmd groups model-discovery subcommands. CLI parity for
// GET /api/v1/models — agents use this to find out what they can set as
// llm_model before patching an agent.
var modelCmd = &cobra.Command{
	Use:   "model",
	Short: "Discover the models a provider can serve",
}

// modelInfoRow mirrors llm.ModelInfo / the API's modelsListResponse.models,
// plus the facts the embedded catalog knows about the same id.
type modelInfoRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	Provider    string `json:"provider"`
	// Catalog is nil when the embedded models.dev snapshot has no entry for
	// this id — which is the normal case for an Ollama tag or a model newer
	// than the snapshot. It is a pointer and not a flattened set of fields
	// because "the catalog does not know this model" and "the catalog says
	// zero context, free" have to stay distinguishable.
	Catalog *modelCatalogFacts `json:"catalog,omitempty"`
}

// modelCatalogFacts is what the snapshot adds to a model id: how much fits,
// how much comes back, whether it can call tools, and what it costs.
//
// Rates are USD per 1,000,000 tokens, straight from modelcatalog — the same
// unit paymaster prices in, with no scaling on either side. They are pointers
// for the reason Cost.CacheRead is: a model the snapshot carries no cost block
// for must render as "unknown", never as free. They are also the CATALOG's
// rates, not necessarily the billed ones — paymaster's hand-verified table
// sits above the catalog and can correct it.
type modelCatalogFacts struct {
	ContextTokens   int64    `json:"context_tokens,omitempty"`
	MaxOutputTokens int64    `json:"max_output_tokens,omitempty"`
	ToolCall        bool     `json:"tool_call"`
	Reasoning       bool     `json:"reasoning"`
	InputPerMTok    *float64 `json:"input_usd_per_mtok,omitempty"`
	OutputPerMTok   *float64 `json:"output_usd_per_mtok,omitempty"`
}

type modelListResult struct {
	Provider string         `json:"provider"`
	Source   string         `json:"source"`
	Models   []modelInfoRow `json:"models"`
}

// Where `model list` reads from. "live" is the server's own resolution (a live
// provider call when the workspace has a credential, its curated fallback
// otherwise — the reply says which); "catalog" is the embedded models.dev
// snapshot and needs nothing but the binary.
const (
	modelSourceAuto    = "auto"
	modelSourceLive    = "live"
	modelSourceCatalog = "catalog"
)

var modelListCmd = &cobra.Command{
	Use:   "list",
	Short: "List models for a provider (live when the server can, else the embedded catalog)",
	Long: `List the models a provider can serve.

For a provider the server can answer for, the list comes from the server:
live from the provider when the workspace has an active API key for it, and a
curated fallback set otherwise. The "source" field reports which.

Every other provider is answered from the models.dev snapshot embedded in this
binary, offline — no token, no workspace, no server. That covers the providers
this build has no codec for but can still price, so the whole priced vocabulary
is visible from one command. 'crewship provider list --all' names them.

Rows carry the catalog's context window, max output, tool-call support and
input/output rates in USD per million tokens, wherever the snapshot knows the
id. Those are the catalog's rates; a hand-verified correction in paymaster can
sit above them.

Models are listed in source order — most-capable and most-recent first, which
is how both the curated lists and the catalog are ordered — not alphabetically.

Examples:
  crewship model list --provider anthropic
  crewship model list --provider deepseek                  # offline, from the catalog
  crewship model list --provider openrouter --search qwen
  crewship model list --provider anthropic --source catalog --format json
  crewship model list --provider ollama   # live-only; needs a reachable daemon`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, _ := cmd.Flags().GetString("provider")
		source, _ := cmd.Flags().GetString("source")
		search, _ := cmd.Flags().GetString("search")

		provider = canonProviderID(provider)
		if provider == "" {
			return cli.WithExitCode(
				fmt.Errorf("--provider is required (%s)", strings.Join(knownProviderIDs(), ", ")),
				cli.ExitValidation)
		}
		source = strings.ToLower(strings.TrimSpace(source))
		switch source {
		case "", modelSourceAuto, modelSourceLive, modelSourceCatalog:
		default:
			return cli.WithExitCode(
				fmt.Errorf("--source must be one of %s, %s, %s", modelSourceAuto, modelSourceLive, modelSourceCatalog),
				cli.ExitValidation)
		}

		res, err := resolveModelList(provider, source)
		if err != nil {
			return err
		}
		res.Models = filterModelRows(res.Models, search)

		return renderModelList(resolvedFormatter(cmd), res)
	},
}

// resolveModelList picks the source and produces the finished rows.
//
// The live path is unchanged from when it was the only path: it is the server
// that owns "which models does this credential actually see", and Ollama in
// particular has no static answer — its models are whatever the daemon pulled.
// The catalog path exists for the providers the server will not answer for,
// and as an offline escape hatch for the ones it will.
func resolveModelList(provider, source string) (*modelListResult, error) {
	if source == modelSourceLive || (isAutoSource(source) && providerServesLiveModels(provider)) {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return nil, err
		}
		res, err := fetchModels(client, provider)
		if err != nil {
			return nil, err
		}
		res.Models = attachCatalogFacts(modelcatalog.Default(), provider, res.Models)
		return res, nil
	}

	if err := modelcatalog.DefaultErr(); err != nil {
		return nil, cli.WithExitCode(
			fmt.Errorf("embedded model catalog is unreadable: %w", err), cli.ExitGeneric)
	}
	cat := modelcatalog.Default()
	models := cat.Models(catalogIDFor(provider))
	if models == nil {
		// "Not in the catalog" and "not a provider" are different failures and
		// deserve different advice. Ollama is the live case: the catalog has
		// no entries for it because its model set is whatever the daemon
		// pulled, and telling an operator it is an unknown provider — while
		// listing it among the known ones — would be plainly wrong.
		if providerServesLiveModels(provider) {
			return nil, cli.NotFoundf(
				"the embedded model catalog carries no models for %q — list it live instead (drop --source catalog)",
				provider)
		}
		return nil, cli.NotFoundf("unknown provider %q (known: %s)",
			provider, strings.Join(knownProviderIDs(), ", "))
	}
	rows := make([]modelInfoRow, 0, len(models))
	for _, m := range models {
		rows = append(rows, modelInfoRow{
			ID:          m.ID,
			DisplayName: m.DisplayName(),
			Provider:    provider,
			Catalog:     catalogFactsFor(m),
		})
	}
	return &modelListResult{Provider: provider, Source: modelSourceCatalog, Models: rows}, nil
}

func isAutoSource(source string) bool { return source == "" || source == modelSourceAuto }

// providerServesLiveModels reports whether GET /api/v1/models will answer for
// provider.
//
// Derived, not copied. The server's set is documented as exactly "the ones
// that have either a live lister or a curated fallback"
// (internal/api/models.go), so the registry — what this build can construct —
// plus llm.CuratedModels reproduces it without a fifth hardcoded copy of
// anthropic/openai/google/ollama drifting alongside the four that already
// exist. GOOGLE arrives through the curated half; OLLAMA through the registry.
//
// If a registry row ever lands that the server does not serve, this routes it
// to the server and the server says so in a 400. --source catalog is the way
// past that, and --source live is the way to make the server answer for a
// provider this build has no row for.
func providerServesLiveModels(provider string) bool {
	if _, ok := llm.LookupProvider(provider); ok {
		return true
	}
	return llm.CuratedModels(provider) != nil
}

// knownProviderIDs is the --provider vocabulary: registry rows first in
// declaration order (the providers this build can actually call), then every
// remaining catalog provider, sorted.
//
// Enumerated rather than written out, so a new registry row or a provider
// added to the snapshot trim shows up in the error message with no edit here —
// which is the whole point, given how many copies of "anthropic, openai,
// google, ollama" this repo has grown.
//
// One gap, stated so nobody has to find it: a curated-only provider that is in
// neither the registry nor the catalog would be missing from this hint, since
// internal/llm exports no list of curated provider ids to walk. Google, the
// only such provider today, is in the catalog.
func knownProviderIDs() []string {
	out := llm.RegisteredProviders()
	seen := make(map[string]bool, len(out))
	for _, id := range out {
		seen[id] = true
	}
	for _, id := range modelcatalog.Default().Providers() {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// fetchModels calls GET /api/v1/models?provider= and decodes the response.
func fetchModels(c *cli.Client, provider string) (*modelListResult, error) {
	resp, err := c.Get("/api/v1/models" + queryString("provider", provider))
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var res modelListResult
	if err := cli.ReadJSON(resp, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// attachCatalogFacts annotates server-supplied rows with what the snapshot
// knows about the same ids, so the live list and the catalog list carry the
// same columns. Rows the catalog does not recognise are returned untouched.
func attachCatalogFacts(cat modelcatalog.Catalog, provider string, rows []modelInfoRow) []modelInfoRow {
	catalogID := catalogIDFor(provider)
	if catalogID == "" || len(rows) == 0 {
		return rows
	}
	out := make([]modelInfoRow, len(rows))
	copy(out, rows)
	for i := range out {
		if m, ok := cat.Lookup(catalogID, out[i].ID); ok {
			out[i].Catalog = catalogFactsFor(m)
		}
	}
	return out
}

// catalogFactsFor flattens a catalog model into the row's optional half.
func catalogFactsFor(m modelcatalog.Model) *modelCatalogFacts {
	facts := &modelCatalogFacts{
		ContextTokens:   m.Limit.Context,
		MaxOutputTokens: m.Limit.Output,
		ToolCall:        m.ToolCall,
		Reasoning:       m.Reasoning,
	}
	if in, out, _, _, ok := m.Rates(); ok {
		facts.InputPerMTok = &in
		facts.OutputPerMTok = &out
	}
	return facts
}

// filterModelRows keeps rows whose id or display name contains needle,
// case-insensitively. An empty needle keeps everything.
//
// Client-side because there is nothing to ask: the catalog is already in
// memory, and the server's list is a single unpaginated response. It exists
// because openrouter alone is 353 models.
func filterModelRows(rows []modelInfoRow, needle string) []modelInfoRow {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return rows
	}
	out := make([]modelInfoRow, 0, len(rows))
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.ID), needle) ||
			strings.Contains(strings.ToLower(r.DisplayName), needle) {
			out = append(out, r)
		}
	}
	return out
}

// renderModelList writes res in f's format.
//
// The caption carries the two facts the table cannot: which provider was asked
// for, and which source answered. It is printed only in the human view — the
// machine formats carry the same facts as fields, and a caption on stdout
// there would make the output unparseable.
func renderModelList(f *cli.Formatter, res *modelListResult) error {
	if f.Format == "" || f.Format == "table" {
		// "listed" and not "total": --search filters before we get here.
		fmt.Fprintf(f.Writer, "%s%s models%s  (source=%s, %d listed)\n",
			cli.Bold, res.Provider, cli.Reset, res.Source, len(res.Models))
	}
	headers := []string{"MODEL", "NAME", "CONTEXT", "MAX OUT", "TOOLS", "IN $/MTOK", "OUT $/MTOK"}
	return f.Auto(res, headers, modelListRows(res.Models))
}

// modelListRows renders rows for the table view, in source order. Column 0 is
// the model id, which is what --format quiet prints — the value that goes
// straight back into `crewship agent update --llm-model`.
//
// Source order, not sorted: both the curated lists and modelcatalog.Models
// deliberately return most-capable-and-most-recent first so a picker rendering
// top-to-bottom offers the right default, and re-sorting by id here threw that
// away (it put claude-haiku-4-5 above claude-opus-5).
func modelListRows(rows []modelInfoRow) [][]string {
	out := make([][]string, 0, len(rows))
	for _, m := range rows {
		name := m.DisplayName
		if name == m.ID {
			name = ""
		}
		cells := []string{m.ID, name, "", "", "", "", ""}
		if c := m.Catalog; c != nil {
			cells[2] = formatTokenCount(c.ContextTokens)
			cells[3] = formatTokenCount(c.MaxOutputTokens)
			cells[4] = yesNo(c.ToolCall)
			cells[5] = formatMTokRate(c.InputPerMTok)
			cells[6] = formatMTokRate(c.OutputPerMTok)
		}
		// Every cell but the id renders as a dash when it is unknown — an
		// empty column in a bordered table reads as a rendering bug.
		for i := 1; i < len(cells); i++ {
			cells[i] = dashIfEmpty(cells[i])
		}
		out = append(out, cells)
	}
	return out
}

// formatTokenCount abbreviates a context window for a table cell: 200000 reads
// as 200k, 1000000 as 1M. Zero renders empty (the caller dashes it): the
// snapshot published no limit, which is a different thing from a zero-token
// window.
func formatTokenCount(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n >= 1_000_000:
		return strings.TrimSuffix(strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64), ".0") + "M"
	case n >= 1_000:
		return strings.TrimSuffix(strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64), ".0") + "k"
	default:
		return strconv.FormatInt(n, 10)
	}
}

// formatMTokRate renders a USD-per-million-tokens rate exactly, with no
// padding to a fixed precision: catalog rates span 0.0028 to 49.5 and rounding
// either end to a shared number of decimals loses a real digit. (`model price`
// formats to a fixed $0.0000 because it is answering about one model's bill,
// where the column is a currency amount rather than a rate card.)
//
// A nil rate renders empty, which the caller dashes — an unpriced model is
// unknown, never free.
func formatMTokRate(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func init() {
	// The usage string names the discovery command rather than enumerating
	// providers: the vocabulary is now nine ids long and, more to the point,
	// building it here would decode the 650 KB snapshot in package init for
	// every crewship invocation — the exact cost internal/modelcatalog's
	// lazy Default() exists to avoid.
	modelListCmd.Flags().String("provider", "",
		"Provider to list models for (see 'crewship provider list --all')")
	modelListCmd.Flags().String("source", modelSourceAuto,
		"Where to read from: auto, live (ask the server), or catalog (the embedded snapshot, offline)")
	modelListCmd.Flags().String("search", "",
		"Only show models whose id or name contains this substring")
	modelCmd.AddCommand(modelListCmd)
}
