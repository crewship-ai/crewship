package main

import (
	"fmt"
	"net/url"
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

// providerRates pairs a display name with input/output $/1M.
var providerRates = []struct {
	name             string
	inputUSDPerMTok  float64
	outputUSDPerMTok float64
}{
	{"Sonnet 4.6", 3, 15},
	{"Opus 4.7", 15, 75},
	{"Haiku 4.5", 1, 5},
}

func buildForecastRows(inTok, outTok int) []forecastRow {
	rows := make([]forecastRow, 0, len(providerRates))
	for _, p := range providerRates {
		in := float64(inTok) / 1_000_000 * p.inputUSDPerMTok
		out := float64(outTok) / 1_000_000 * p.outputUSDPerMTok
		rows = append(rows, forecastRow{
			Model:     p.name,
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
	switch f.Format {
	case "json", "yaml", "ndjson":
		v := map[string]any{
			"source":         source,
			"input_tokens":   inTok,
			"output_tokens":  outTok,
			"rows":           rows,
		}
		switch f.Format {
		case "json":
			return f.JSON(v)
		case "yaml":
			return f.YAML(v)
		case "ndjson":
			// Stream one row per model line so jq -c composes well.
			for _, r := range rows {
				if err := f.WriteNDJSONRow(r); err != nil {
					return err
				}
			}
			return nil
		}
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
