package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// evalCompareCmd runs ONE eval scenario back-to-back on two tiers
// and reports a head-to-head verdict. Built specifically for the
// "weak vs strong agent same outcome?" question — the matrix from
// `eval scenarios` is great for sweeps, but when investigating ONE
// scenario you want the side-by-side text and a clean delta.
//
// The two runs share the same inputs, the same routine version, and
// only differ on the resolved tier. Output captures:
//
//   - Status A vs Status B
//   - Output text (first 1 KiB of each, indented for diff)
//   - Cost / latency delta
//   - Failed-step + error message when one side trips its gate
//
// What this is NOT: a generic "diff two run records" tool. We
// deliberately re-run the routine here rather than fetching two
// pre-existing runs by id — the comparison is most informative
// when both runs are fresh against the same routine version + same
// inputs. A "diff two persisted runs by id" command is a follow-up.
var evalCompareCmd = &cobra.Command{
	Use:   "compare <scenario-slug>",
	Short: "Run one eval scenario on two tiers back-to-back and report the head-to-head delta",
	Long: `Compare worker tiers on a single eval scenario.

Examples:
  # Default: fast vs smart on the workspace's authored inputs
  crewship eval compare eval-extract-emails

  # Custom inputs + alternative tier pair
  crewship eval compare eval-syllogism-reasoning \
      --tier-a moderate --tier-b smart \
      --inputs '{"premises":"X is taller than Y. Y is taller than Z."}'

  # Markdown output for PR descriptions
  crewship eval compare eval-classify-sentiment -f markdown`,
	Args: cobra.ExactArgs(1),
	RunE: runEvalCompare,
}

// compareSide captures one half of the head-to-head. We mirror
// the run response fields plus a captured `Output` so the diff
// renderer doesn't need a second round-trip to the journal.
type compareSide struct {
	Tier         string  `json:"tier"`
	RunID        string  `json:"run_id"`
	Status       string  `json:"status"`
	Output       string  `json:"output"`
	DurationMs   int64   `json:"duration_ms"`
	CostUSD      float64 `json:"cost_usd"`
	FailedAtStep string  `json:"failed_at_step,omitempty"`
	ErrorMessage string  `json:"error_message,omitempty"`
}

func runEvalCompare(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	slug := args[0]
	tierA, _ := cmd.Flags().GetString("tier-a")
	tierB, _ := cmd.Flags().GetString("tier-b")
	inputsRaw, _ := cmd.Flags().GetString("inputs")

	if tierA == tierB {
		return fmt.Errorf("--tier-a and --tier-b must differ (got both = %q)", tierA)
	}

	var inputs map[string]any
	if inputsRaw != "" {
		if err := json.Unmarshal([]byte(inputsRaw), &inputs); err != nil {
			return fmt.Errorf("parse --inputs JSON: %w", err)
		}
	}

	client := newAPIClient()
	ws := client.GetWorkspaceID()

	a, err := runOneSide(client, ws, slug, tierA, inputs)
	if err != nil {
		return fmt.Errorf("run side A (tier=%s): %w", tierA, err)
	}
	b, err := runOneSide(client, ws, slug, tierB, inputs)
	if err != nil {
		return fmt.Errorf("run side B (tier=%s): %w", tierB, err)
	}

	f := newFormatter()
	if f.Format == "json" {
		return f.JSON(map[string]any{
			"scenario":  slug,
			"side_a":    a,
			"side_b":    b,
			"agreement": semanticAgreementVerdict(a, b),
		})
	}
	if f.Format == "yaml" {
		return f.YAML(map[string]any{
			"scenario":  slug,
			"side_a":    a,
			"side_b":    b,
			"agreement": semanticAgreementVerdict(a, b),
		})
	}
	if f.Format == "markdown" {
		return printCompareMarkdown(cmd, slug, a, b)
	}
	return printCompareTable(cmd, slug, a, b)
}

// runOneSide fires one /run with the supplied tier and shapes the
// response into compareSide. Treats non-2xx HTTP as a "side
// transport error" — surfaces in the verdict output rather than
// crashing the comparison.
func runOneSide(client interface {
	Post(string, any) (*http.Response, error)
}, ws, slug, tier string, inputs map[string]any) (compareSide, error) {
	body := map[string]any{}
	if inputs != nil {
		body["inputs"] = inputs
	} else {
		body["inputs"] = map[string]any{}
	}
	if tier != "" {
		body["tier_override"] = tier
	}
	resp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/run", ws, slug), body)
	if err != nil {
		return compareSide{Tier: tier}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return compareSide{Tier: tier, Status: fmt.Sprintf("HTTP_%d", resp.StatusCode)}, nil
	}
	var result struct {
		RunID        string  `json:"run_id"`
		Status       string  `json:"status"`
		Output       string  `json:"output"`
		DurationMs   int64   `json:"duration_ms"`
		CostUSD      float64 `json:"cost_usd"`
		FailedAtStep string  `json:"failed_at_step"`
		ErrorMessage string  `json:"error_message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return compareSide{Tier: tier, Status: "DECODE_ERROR"}, err
	}
	return compareSide{
		Tier:         tier,
		RunID:        result.RunID,
		Status:       result.Status,
		Output:       result.Output,
		DurationMs:   result.DurationMs,
		CostUSD:      result.CostUSD,
		FailedAtStep: result.FailedAtStep,
		ErrorMessage: result.ErrorMessage,
	}, nil
}

// semanticAgreementVerdict produces a short verdict label across
// the two sides. Goal: glanceable summary the operator can act on.
//
//   - "AGREE-PASS"        → both sides pass the gate
//   - "AGREE-FAIL"        → both sides fail the gate (regression on
//     the routine, not a tier-strength signal)
//   - "DIVERGE-A-PASS"    → side A passes, side B fails (weak-only path)
//   - "DIVERGE-B-PASS"    → side B passes, side A fails (smart-only path)
//   - "AMBIGUOUS"         → at least one side errored at transport
//     level — verdict can't be cleanly assigned
//
// Word-level identical match is NOT required for AGREE — the gate
// already decided pass/fail. This verdict is about cross-tier
// gate-pass agreement, not text identity. (Text identity for two
// LLM runs is essentially never true; insisting on it would be a
// useless test.)
func semanticAgreementVerdict(a, b compareSide) string {
	pa := isPassStatus(a.Status)
	pb := isPassStatus(b.Status)
	switch {
	case a.Status == "" || b.Status == "":
		return "AMBIGUOUS"
	case strings.HasPrefix(a.Status, "HTTP_") || strings.HasPrefix(b.Status, "HTTP_"):
		return "AMBIGUOUS"
	case pa && pb:
		return "AGREE-PASS"
	case !pa && !pb:
		return "AGREE-FAIL"
	case pa && !pb:
		return "DIVERGE-A-PASS"
	default:
		return "DIVERGE-B-PASS"
	}
}

func isPassStatus(s string) bool {
	return s == "COMPLETED" || s == "DEDUPED"
}

// printCompareTable renders the human-friendly head-to-head: each
// side's status / latency / cost / output preview, then the
// verdict. Borders use the project's existing formatter helpers
// for consistency with `routine list` etc. — no bespoke ANSI.
func printCompareTable(cmd *cobra.Command, slug string, a, b compareSide) error {
	out := cmd.OutOrStdout()
	verdict := semanticAgreementVerdict(a, b)

	fmt.Fprintf(out, "\nScenario: %s\nVerdict:  %s\n\n", slug, verdict)
	fmt.Fprintf(out, "  %-12s %-12s %s\n", "tier", "status", "cost / duration")
	fmt.Fprintf(out, "  %-12s %-12s %s\n", "----", "------", "---------------")
	fmt.Fprintf(out, "  A: %-9s %-12s $%.4f / %dms\n", labelTier(a.Tier), a.Status, a.CostUSD, a.DurationMs)
	fmt.Fprintf(out, "  B: %-9s %-12s $%.4f / %dms\n", labelTier(b.Tier), b.Status, b.CostUSD, b.DurationMs)

	if a.ErrorMessage != "" {
		fmt.Fprintf(out, "\n  A error @ %s: %s\n", a.FailedAtStep, truncEvalLine(a.ErrorMessage, 200))
	}
	if b.ErrorMessage != "" {
		fmt.Fprintf(out, "\n  B error @ %s: %s\n", b.FailedAtStep, truncEvalLine(b.ErrorMessage, 200))
	}

	if a.Output != "" {
		fmt.Fprintf(out, "\n--- Side A output (1KiB cap) ---\n%s\n", capOutput(a.Output, 1024))
	}
	if b.Output != "" {
		fmt.Fprintf(out, "\n--- Side B output (1KiB cap) ---\n%s\n", capOutput(b.Output, 1024))
	}
	return nil
}

// printCompareMarkdown emits a GitHub-flavoured markdown block
// suitable for pasting into a PR description or eval-suite review
// doc. Keeps the verdict at the top so a reviewer skimming the
// PR doesn't have to scroll.
func printCompareMarkdown(cmd *cobra.Command, slug string, a, b compareSide) error {
	out := cmd.OutOrStdout()
	verdict := semanticAgreementVerdict(a, b)

	fmt.Fprintf(out, "## Eval compare — `%s` (%s)\n\n", slug, verdict)
	fmt.Fprintln(out, "| Side | Tier | Status | Cost (USD) | Duration (ms) |")
	fmt.Fprintln(out, "| --- | --- | --- | --- | --- |")
	fmt.Fprintf(out, "| A | %s | %s | $%.4f | %d |\n", labelTier(a.Tier), a.Status, a.CostUSD, a.DurationMs)
	fmt.Fprintf(out, "| B | %s | %s | $%.4f | %d |\n", labelTier(b.Tier), b.Status, b.CostUSD, b.DurationMs)

	if a.Output != "" {
		fmt.Fprintf(out, "\n### Side A output\n```\n%s\n```\n", capOutput(a.Output, 1024))
	}
	if b.Output != "" {
		fmt.Fprintf(out, "\n### Side B output\n```\n%s\n```\n", capOutput(b.Output, 1024))
	}
	if a.ErrorMessage != "" || b.ErrorMessage != "" {
		fmt.Fprintln(out, "\n### Errors")
		if a.ErrorMessage != "" {
			fmt.Fprintf(out, "- **A** at `%s`: %s\n", a.FailedAtStep, a.ErrorMessage)
		}
		if b.ErrorMessage != "" {
			fmt.Fprintf(out, "- **B** at `%s`: %s\n", b.FailedAtStep, b.ErrorMessage)
		}
	}
	return nil
}

func labelTier(t string) string {
	if t == "" {
		return "(authored)"
	}
	return t
}

func capOutput(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... [truncated, " + fmt.Sprintf("%d bytes total", len(s)) + "]"
}

func init() {
	evalCompareCmd.Flags().String("tier-a", "fast", "tier override for side A")
	evalCompareCmd.Flags().String("tier-b", "smart", "tier override for side B")
	evalCompareCmd.Flags().String("inputs", "", "JSON inputs forwarded to both sides (default: routine's authored defaults)")
	evalCmd.AddCommand(evalCompareCmd)
}
