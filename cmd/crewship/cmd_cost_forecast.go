package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// costForecastCmd estimates the cost of a future run / routine /
// arbitrary prompt before you spend the tokens.
//
// Two modes — picked by which flag the user supplied:
//
//	--prompt <text|@file|@->    pure prompt-size projection
//	--from-history <agent-slug> average of the last 20 runs of that agent
//
// Both modes print the same row layout (provider, $ input, projected
// total $) so the output is comparable across modes.
var costForecastCmd = &cobra.Command{
	Use:   "forecast",
	Short: "Estimate the cost of a future prompt / mission / agent",
	Long: `Project the cost of a run before sending it.

Two modes:

  # Projection from a prompt (token-count heuristic)
  crewship cost forecast --prompt "rewrite auth"
  cat plan.md | crewship cost forecast --prompt @-

  # Projection from history (averages last 20 runs of the agent)
  crewship cost forecast --from-history viktor

Heuristics: input tokens use the same ~4 chars/token rule as
'crewship ask --estimate'. Output cost is projected as 2× input
unless --output-ratio is set (most agent runs return less than the
input on average).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		promptFlag, _ := cmd.Flags().GetString("prompt")
		historyAgent, _ := cmd.Flags().GetString("from-history")
		ratio, _ := cmd.Flags().GetFloat64("output-ratio")
		if ratio <= 0 {
			ratio = 2.0
		}

		if promptFlag == "" && historyAgent == "" {
			return fmt.Errorf("provide --prompt or --from-history")
		}

		f := newFormatter()

		// Prompt-mode: read the prompt, count tokens, render the rate
		// table for the canonical models. We re-use FormatEstimate for
		// the input-cost block then add output projection.
		if promptFlag != "" {
			prompt, err := cli.BuildPrompt(cmd.Context(), cli.PromptOptions{
				PromptFlag: promptFlag,
				AutoStdin:  true,
			})
			if err != nil {
				return err
			}
			if strings.TrimSpace(prompt) == "" {
				return fmt.Errorf("empty prompt")
			}
			inputTokens := cli.EstimateTokens(prompt)
			outputTokens := int(float64(inputTokens) * ratio)

			rows := buildForecastRows(inputTokens, outputTokens)
			return renderForecast(f, "prompt", inputTokens, outputTokens, rows)
		}

		// History-mode: fetch the agent's recent runs and average the
		// recorded input/output tokens.
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		// resolveAgentID accepts slug or id.
		agentID, err := resolveAgentID(client, historyAgent)
		if err != nil {
			return err
		}
		q := url.Values{}
		q.Set("agent_id", agentID)
		q.Set("limit", "20")
		resp, err := client.Get("/api/v1/runs?" + q.Encode())
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var runs struct {
			Data []struct {
				ID       string         `json:"id"`
				Metadata map[string]any `json:"metadata"`
			} `json:"data"`
		}
		if err := cli.ReadJSON(resp, &runs); err != nil {
			return err
		}
		if len(runs.Data) == 0 {
			return fmt.Errorf("no past runs for agent %q to average", historyAgent)
		}
		// Sum tokens out of run metadata. Many providers stash usage as
		// `usage.input_tokens` / `usage.output_tokens`; not every metadata
		// blob has them, so we skip silently when absent.
		var totalIn, totalOut, count int
		for _, r := range runs.Data {
			in, out, ok := extractUsageTokens(r.Metadata)
			if !ok {
				continue
			}
			totalIn += in
			totalOut += out
			count++
		}
		if count == 0 {
			return fmt.Errorf("none of the last %d runs had recorded token usage", len(runs.Data))
		}
		avgIn := totalIn / count
		avgOut := totalOut / count
		rows := buildForecastRows(avgIn, avgOut)
		return renderForecast(f, "history (avg of "+fmt.Sprintf("%d", count)+" runs)", avgIn, avgOut, rows)
	},
}

// forecastRow is one model + its projected $ for the requested run.
type forecastRow struct {
	Model       string  `json:"model"`
	InputUSD    float64 `json:"input_usd"`
	OutputUSD   float64 `json:"output_usd"`
	TotalUSD    float64 `json:"total_usd"`
	InTokens    int     `json:"input_tokens"`
	OutTokens   int     `json:"output_tokens"`
}

// providerRate is one model + its public per-1M-token list price.
type providerRate struct {
	Name             string
	InputUSDPerMTok  float64
	OutputUSDPerMTok float64
}

// providerRates is the canonical CLI-side rate table for `cost
// forecast` (and `--estimate` via FormatEstimate). These mirror
// Anthropic's published list prices and are deliberately hardcoded —
// pulling them dynamically from the paymaster `model_rates` table
// would couple the CLI to a server round-trip just to render a dry-run
// forecast.
//
// When list prices change, update this slice AND
// internal/cli/tokens.go's FormatEstimate block in the same commit so
// the two forecast surfaces stay in sync. Override at runtime with
// CREWSHIP_FORECAST_RATES (CSV: name,in,out;…) for teams on
// negotiated / volume-discount pricing.
//
// Last reviewed: 2026-05 (Sonnet 4.6, Opus 4.7, Haiku 4.5).
var providerRates = []providerRate{
	{"Sonnet 4.6", 3, 15},
	{"Opus 4.7", 15, 75},
	{"Haiku 4.5", 1, 5},
}

// loadProviderRates returns the active rate table, applying the
// CREWSHIP_FORECAST_RATES env-var override when set. Format is
// `Name,inPerM,outPerM;Name,inPerM,outPerM;…`. Parse failures fall
// back to the hardcoded defaults with a one-time stderr warning so a
// typo never silently produces phantom-zero forecasts.
func loadProviderRates() []providerRate {
	v := os.Getenv("CREWSHIP_FORECAST_RATES")
	if v == "" {
		return providerRates
	}
	out := make([]providerRate, 0, 4)
	for _, entry := range strings.Split(v, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ",")
		if len(parts) != 3 {
			fmt.Fprintf(os.Stderr, "%s[forecast]%s ignoring malformed CREWSHIP_FORECAST_RATES entry %q\n",
				cli.Yellow, cli.Reset, entry)
			return providerRates
		}
		in, err1 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		o, err2 := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		if err1 != nil || err2 != nil {
			fmt.Fprintf(os.Stderr, "%s[forecast]%s ignoring bad rate in %q\n",
				cli.Yellow, cli.Reset, entry)
			return providerRates
		}
		out = append(out, providerRate{Name: strings.TrimSpace(parts[0]), InputUSDPerMTok: in, OutputUSDPerMTok: o})
	}
	if len(out) == 0 {
		return providerRates
	}
	return out
}

// structuredForecast assembles the shared map the json/yaml renderers
// emit. Extracted to one place so the schema stays consistent across
// format flags.
func structuredForecast(source string, inTok, outTok int, rows []forecastRow) map[string]any {
	return map[string]any{
		"source":        source,
		"input_tokens":  inTok,
		"output_tokens": outTok,
		"rows":          rows,
	}
}

func buildForecastRows(inTok, outTok int) []forecastRow {
	rates := loadProviderRates()
	rows := make([]forecastRow, 0, len(rates))
	for _, p := range rates {
		in := float64(inTok) / 1_000_000 * p.InputUSDPerMTok
		out := float64(outTok) / 1_000_000 * p.OutputUSDPerMTok
		rows = append(rows, forecastRow{
			Model:     p.Name,
			InputUSD:  in,
			OutputUSD: out,
			TotalUSD:  in + out,
			InTokens:  inTok,
			OutTokens: outTok,
		})
	}
	return rows
}

func renderForecast(f *Formatter, source string, inTok, outTok int, rows []forecastRow) error {
	// Structured outputs return early; only the table-render path falls
	// through to the Printf block below. The inner switch is exhaustive
	// over its parent's three cases — the previous nested form
	// confused readers and static analysers about whether the table
	// path was reachable for ndjson.
	switch f.Format {
	case "json":
		return f.JSON(structuredForecast(source, inTok, outTok, rows))
	case "yaml":
		return f.YAML(structuredForecast(source, inTok, outTok, rows))
	case "ndjson":
		// Stream one row per model line so jq -c composes well.
		for _, r := range rows {
			if err := f.WriteNDJSONRow(r); err != nil {
				return err
			}
		}
		return nil
	}
	fmt.Printf("%sCost forecast%s  %ssource: %s%s\n", cli.Bold, cli.Reset, cli.Dim, source, cli.Reset)
	fmt.Printf("%sinput ≈ %d tok    output ≈ %d tok%s\n\n", cli.Dim, inTok, outTok, cli.Reset)
	headers := []string{"MODEL", "INPUT $", "OUTPUT $", "TOTAL $"}
	tableRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		tableRows = append(tableRows, []string{
			r.Model,
			fmt.Sprintf("$%.4f", r.InputUSD),
			fmt.Sprintf("$%.4f", r.OutputUSD),
			fmt.Sprintf("$%.4f", r.TotalUSD),
		})
	}
	f.Table(headers, tableRows)
	return nil
}

// extractUsageTokens digs into a run-metadata blob for input/output
// token counts. Returns ok=false when neither side is populated. The
// path traversal is tolerant of either flat (`input_tokens`) or nested
// (`usage.input_tokens`) shapes because run metadata isn't fully
// schema-pinned yet.
func extractUsageTokens(md map[string]any) (in, out int, ok bool) {
	if md == nil {
		return 0, 0, false
	}
	toInt := func(v any) int {
		if f, fok := v.(float64); fok {
			return int(f)
		}
		return 0
	}
	if u, has := md["usage"].(map[string]any); has {
		in = toInt(u["input_tokens"])
		out = toInt(u["output_tokens"])
	}
	if in == 0 {
		in = toInt(md["input_tokens"])
	}
	if out == 0 {
		out = toInt(md["output_tokens"])
	}
	return in, out, in > 0 || out > 0
}

// Formatter is a local alias for cli.Formatter so this file doesn't
// need a separate import cycle dance. cli.Formatter is the source of
// truth; renderForecast just needs the methods.
type Formatter = cli.Formatter

func init() {
	costForecastCmd.Flags().StringP("prompt", "p", "", "Prompt text, @file, or @- for stdin (token-count projection)")
	costForecastCmd.Flags().String("from-history", "", "Agent slug — average over the last 20 runs")
	costForecastCmd.Flags().Float64("output-ratio", 2.0, "Output tokens as multiple of input (prompt mode)")
	costCmd.AddCommand(costForecastCmd)
}
