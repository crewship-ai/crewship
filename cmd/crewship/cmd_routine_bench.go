package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

// routineBenchCmd characterises a single routine's stability under
// repeated invocation. Trigger.dev-style "is this task production-
// ready?" smoke: run N times with the same inputs, report pass-rate,
// cost variance, and latency p50/p95/max so an operator can decide
// whether to pin the routine to its current tier or escalate.
//
// Distinct from `eval scenarios` (matrix across many routines × tiers)
// and `eval compare` (head-to-head two tiers). bench is "drill into
// THIS one routine" — the single-pipeline observability surface.
//
// What gets measured:
//
//   - Pass rate              — Status == COMPLETED || DEDUPED
//   - Cost: total / mean / p95 / max
//   - Duration: p50 / p95 / max (ms)
//   - Failure breakdown      — top fail reasons (gate / cost cap / error)
//
// What this tells you:
//
//   - 10/10 pass + tight latency variance  → ship at this tier
//   - 8/10 pass with the 2 fails on cost   → bump max_cost_usd
//   - 5/10 pass with rubric fails          → tier too weak; escalate
//   - High p95 vs p50                      → scheduler / container churn
var routineBenchCmd = &cobra.Command{
	Use:   "bench <slug>",
	Short: "Characterise a routine's stability under repeated invocation (pass-rate + cost/latency variance)",
	Long: `Run a routine N times with the same inputs and report pass-rate,
cost stats, and latency distribution. Use to decide whether a routine
is production-ready at its current tier.

Examples:
  # 10 runs at the routine's authored tier
  crewship routine bench eval-extract-emails --runs 10

  # 5 runs with explicit tier override and JSON output for CI
  crewship routine bench eval-extract-emails --runs 5 --tier-override fast -f json

  # Bench with custom inputs
  crewship routine bench summarize-text --runs 5 \
      --inputs '{"text":"specific text to bench"}'`,
	Args: cobra.ExactArgs(1),
	RunE: runRoutineBench,
}

// benchAttempt captures one run for stat aggregation. Tracks the
// gate/error reason on failure so the bench report can group fail
// modes — "5 of 10 hit the cost cap" is more actionable than "5/10
// failed."
type benchAttempt struct {
	Attempt    int     `json:"attempt"`
	RunID      string  `json:"run_id"`
	Status     string  `json:"status"`
	DurationMs int64   `json:"duration_ms"`
	CostUSD    float64 `json:"cost_usd"`
	FailReason string  `json:"fail_reason,omitempty"`
}

// benchSummary is the high-level aggregate emitted as the table /
// JSON top-level. Stats are computed once at the end so the
// per-attempt output stays clean during the run.
type benchSummary struct {
	Slug         string         `json:"slug"`
	TierOverride string         `json:"tier_override,omitempty"`
	Runs         int            `json:"runs"`
	Pass         int            `json:"pass"`
	PassRate     float64        `json:"pass_rate"`
	CostTotal    float64        `json:"cost_total_usd"`
	CostMean     float64        `json:"cost_mean_usd"`
	CostP95      float64        `json:"cost_p95_usd"`
	CostMax      float64        `json:"cost_max_usd"`
	DurP50Ms     int64          `json:"duration_p50_ms"`
	DurP95Ms     int64          `json:"duration_p95_ms"`
	DurMaxMs     int64          `json:"duration_max_ms"`
	FailReasons  map[string]int `json:"fail_reasons,omitempty"`
	Attempts     []benchAttempt `json:"attempts"`
	GeneratedAt  string         `json:"generated_at"`
}

func runRoutineBench(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	slug := args[0]
	runs, _ := cmd.Flags().GetInt("runs")
	tierOverride, _ := cmd.Flags().GetString("tier-override")
	inputsRaw, _ := cmd.Flags().GetString("inputs")
	failFast, _ := cmd.Flags().GetBool("fail-fast")
	cooldownMs, _ := cmd.Flags().GetInt("cooldown-ms")

	if runs < 1 {
		return fmt.Errorf("--runs must be >= 1")
	}

	var inputs map[string]any
	if inputsRaw != "" {
		if err := json.Unmarshal([]byte(inputsRaw), &inputs); err != nil {
			return fmt.Errorf("parse --inputs JSON: %w", err)
		}
	}

	client := newAPIClient()
	ws := client.GetWorkspaceID()
	out := cmd.OutOrStderr()

	fmt.Fprintf(out, "Benching %s × %d runs", slug, runs)
	if tierOverride != "" {
		fmt.Fprintf(out, " (tier=%s)", tierOverride)
	}
	fmt.Fprintln(out)

	attempts := make([]benchAttempt, 0, runs)
	for i := 1; i <= runs; i++ {
		a := executeBenchAttempt(client, ws, slug, tierOverride, i, inputs)
		attempts = append(attempts, a)
		printBenchAttempt(out, a)
		if failFast && !isPassStatus(a.Status) {
			fmt.Fprintln(out, "fail-fast: aborting after first failure")
			break
		}
		if cooldownMs > 0 && i < runs {
			time.Sleep(time.Duration(cooldownMs) * time.Millisecond)
		}
	}

	summary := summariseBench(slug, tierOverride, attempts)
	return renderBenchReport(cmd, summary)
}

// executeBenchAttempt mirrors executeOneScenario but typed for
// bench's per-attempt record. Kept separate so the two commands
// can evolve independently — bench will likely grow inputs-per-run
// jitter and cooldown semantics that scenarios doesn't need.
func executeBenchAttempt(client interface {
	Post(string, any) (*http.Response, error)
}, ws, slug, tier string, attempt int, inputs map[string]any) benchAttempt {
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
		// Defensive: some HTTP clients return (resp, err) with a
		// non-nil resp even on transport error (e.g. body-read
		// failures after headers landed). Drain + close to avoid
		// leaking the connection across N bench iterations.
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return benchAttempt{Attempt: attempt, Status: "ERROR", FailReason: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return benchAttempt{Attempt: attempt, Status: fmt.Sprintf("HTTP_%d", resp.StatusCode)}
	}
	var result struct {
		RunID        string  `json:"run_id"`
		Status       string  `json:"status"`
		DurationMs   int64   `json:"duration_ms"`
		CostUSD      float64 `json:"cost_usd"`
		FailedAtStep string  `json:"failed_at_step"`
		ErrorMessage string  `json:"error_message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return benchAttempt{Attempt: attempt, Status: "DECODE_ERROR", FailReason: err.Error()}
	}
	failReason := ""
	if !isPassStatus(result.Status) && result.ErrorMessage != "" {
		failReason = classifyFailReason(result.ErrorMessage)
	}
	return benchAttempt{
		Attempt:    attempt,
		RunID:      result.RunID,
		Status:     result.Status,
		DurationMs: result.DurationMs,
		CostUSD:    result.CostUSD,
		FailReason: failReason,
	}
}

// classifyFailReason buckets the error_message text into a coarse
// category so the summary can show "5 cost-cap, 2 rubric, 3 schema"
// instead of 10 unique error strings. Heuristic — extends as we
// see new fail modes.
//
// Order of cases matters: "outcomes failed" must come before the
// generic "output ..." gate-fail bucket because outcomes errors
// can include the underlying output diagnostic in their text.
func classifyFailReason(msg string) string {
	switch {
	case containsAny(msg, "cost cap exceeded"):
		return "cost-cap"
	case containsAny(msg, "outcomes failed"):
		return "rubric-fail"
	case containsAny(msg,
		"output length",
		"output contains banned",
		"output missing required",
		"schema validation:",
		"schema invalid:",
		"output not valid JSON",
	):
		return "gate-fail"
	case containsAny(msg, "invalid Anthropic API key", "no active Anthropic credential"):
		return "auth-fail"
	case containsAny(msg, "context deadline exceeded", "timeout"):
		return "timeout"
	default:
		return "other"
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub == "" {
			continue
		}
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}

func printBenchAttempt(w interface{ Write([]byte) (int, error) }, a benchAttempt) {
	tail := ""
	if a.FailReason != "" {
		tail = "  (" + a.FailReason + ")"
	}
	fmt.Fprintf(asWriter(w), "  #%-3d  %-9s  %5dms  $%.4f%s\n",
		a.Attempt, a.Status, a.DurationMs, a.CostUSD, tail)
}

// summariseBench collapses the per-attempt slice into headline
// stats. Percentile math uses linear interpolation; for small N
// (typical bench is 5-20) the difference between methods is
// negligible.
func summariseBench(slug, tier string, attempts []benchAttempt) benchSummary {
	s := benchSummary{
		Slug:         slug,
		TierOverride: tier,
		Runs:         len(attempts),
		Attempts:     attempts,
		FailReasons:  map[string]int{},
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	if len(attempts) == 0 {
		return s
	}

	costs := make([]float64, 0, len(attempts))
	durs := make([]int64, 0, len(attempts))
	for _, a := range attempts {
		if isPassStatus(a.Status) {
			s.Pass++
		}
		s.CostTotal += a.CostUSD
		costs = append(costs, a.CostUSD)
		durs = append(durs, a.DurationMs)
		if a.FailReason != "" {
			s.FailReasons[a.FailReason]++
		}
		if a.CostUSD > s.CostMax {
			s.CostMax = a.CostUSD
		}
		if a.DurationMs > s.DurMaxMs {
			s.DurMaxMs = a.DurationMs
		}
	}
	s.PassRate = float64(s.Pass) / float64(s.Runs)
	s.CostMean = s.CostTotal / float64(s.Runs)

	sort.Float64s(costs)
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })

	s.CostP95 = percentileF64(costs, 95)
	s.DurP50Ms = percentileInt64(durs, 50)
	s.DurP95Ms = percentileInt64(durs, 95)

	if len(s.FailReasons) == 0 {
		s.FailReasons = nil // omitempty in JSON
	}
	return s
}

func percentileF64(sorted []float64, p int) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := float64(p) / 100 * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}
	w := rank - float64(lower)
	return sorted[lower]*(1-w) + sorted[upper]*w
}

func percentileInt64(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := float64(p) / 100 * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}
	w := rank - float64(lower)
	return int64(float64(sorted[lower])*(1-w) + float64(sorted[upper])*w)
}

func renderBenchReport(cmd *cobra.Command, s benchSummary) error {
	f := newFormatter()
	if f.Format == "json" {
		return f.JSON(s)
	}
	if f.Format == "yaml" {
		return f.YAML(s)
	}

	out := cmd.OutOrStdout()
	tier := s.TierOverride
	if tier == "" {
		tier = "(authored)"
	}
	fmt.Fprintf(out, "\n────── %s × %d runs (tier=%s) ──────\n\n", s.Slug, s.Runs, tier)
	fmt.Fprintf(out, "  Pass rate:  %d/%d  (%.0f%%)\n", s.Pass, s.Runs, s.PassRate*100)
	fmt.Fprintf(out, "  Cost:       total $%.4f  /  mean $%.4f  /  p95 $%.4f  /  max $%.4f\n",
		s.CostTotal, s.CostMean, s.CostP95, s.CostMax)
	fmt.Fprintf(out, "  Duration:   p50 %dms  /  p95 %dms  /  max %dms\n",
		s.DurP50Ms, s.DurP95Ms, s.DurMaxMs)

	if len(s.FailReasons) > 0 {
		fmt.Fprintln(out, "\n  Fail breakdown:")
		// Stable order: highest count first, ties broken alphabetically.
		type kv struct {
			k string
			v int
		}
		pairs := make([]kv, 0, len(s.FailReasons))
		for k, v := range s.FailReasons {
			pairs = append(pairs, kv{k, v})
		}
		sort.Slice(pairs, func(i, j int) bool {
			if pairs[i].v != pairs[j].v {
				return pairs[i].v > pairs[j].v
			}
			return pairs[i].k < pairs[j].k
		})
		for _, p := range pairs {
			fmt.Fprintf(out, "    %-12s  %d\n", p.k, p.v)
		}
	}

	// Brief production-readiness verdict — a one-line "is this
	// usable now?" so the operator doesn't need to interpret stats.
	verdict := readinessVerdict(s)
	fmt.Fprintf(out, "\n  Verdict:    %s\n", verdict)
	return nil
}

// readinessVerdict maps the bench summary to a short production-
// readiness label. Thresholds chosen to match the operator workflow
// in PRD §18 (promote-to-fast / pin-to-smart / rewrite). Aggressive
// on the upside (90%+ = ship), conservative on the downside (<70% =
// rewrite recommended).
func readinessVerdict(s benchSummary) string {
	switch {
	case s.Runs == 0:
		return "INSUFFICIENT_DATA"
	case s.PassRate >= 0.9:
		return fmt.Sprintf("PRODUCTION_READY (≥90%% pass rate over %d runs)", s.Runs)
	case s.PassRate >= 0.7:
		return fmt.Sprintf("FLAKY (%d%% pass — investigate top fail reason before shipping)", int(s.PassRate*100))
	case s.PassRate > 0:
		return fmt.Sprintf("UNRELIABLE (%d%% pass — escalate tier or rewrite gates)", int(s.PassRate*100))
	default:
		return "BROKEN (0% pass — gate is unsatisfiable or auth/cost guardrail trips every time)"
	}
}

func init() {
	routineBenchCmd.Flags().Int("runs", 10, "number of runs to execute")
	routineBenchCmd.Flags().String("tier-override", "", "force every agent_run step onto a tier (trivial|fast|moderate|smart). Empty = use authored complexity.")
	routineBenchCmd.Flags().String("inputs", "", "JSON inputs forwarded to every run (default: routine's authored defaults)")
	routineBenchCmd.Flags().Bool("fail-fast", false, "abort the bench on the first non-pass instead of completing every run")
	routineBenchCmd.Flags().Int("cooldown-ms", 0, "delay between runs in milliseconds; useful when stress-testing rate-limited credentials")

	pipelineCmd.AddCommand(routineBenchCmd)
}
