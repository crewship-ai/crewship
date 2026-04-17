package quartermaster

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Compare diffs two EvalMetrics snapshots and returns a structured
// regression report. A candidate is "regressed" when any of:
//   - ToolSuccessRate drops by > 5% (absolute, not relative)
//   - StepsToGoal rises by > 20% (relative)
//   - TotalCostUSD rises by > 15% (relative)
//   - Hallucinations increases at all
//
// Thresholds are conservative — it's cheaper to investigate a false
// positive than to ship a regression.
func Compare(baseline, candidate EvalMetrics) RegressionReport {
	report := RegressionReport{
		Baseline:  baseline,
		Candidate: candidate,
	}

	// tool_success_rate: absolute drop > 0.05
	delta := candidate.ToolSuccessRate - baseline.ToolSuccessRate
	d := MetricDelta{
		Name:      "tool_success_rate",
		Baseline:  baseline.ToolSuccessRate,
		Candidate: candidate.ToolSuccessRate,
		Delta:     delta,
	}
	if delta < -0.05 {
		d.Regressed = true
		d.Reason = fmt.Sprintf("tool success dropped %.1f%% (> 5%% threshold)", -delta*100)
		report.Regressed = true
	}
	report.Deltas = append(report.Deltas, d)

	// steps_to_goal: relative rise > 20%
	d = MetricDelta{
		Name:      "steps_to_goal",
		Baseline:  float64(baseline.StepsToGoal),
		Candidate: float64(candidate.StepsToGoal),
		Delta:     float64(candidate.StepsToGoal - baseline.StepsToGoal),
	}
	if baseline.StepsToGoal > 0 {
		rel := float64(candidate.StepsToGoal-baseline.StepsToGoal) / float64(baseline.StepsToGoal)
		if rel > 0.20 {
			d.Regressed = true
			d.Reason = fmt.Sprintf("steps to goal rose %.1f%% (> 20%% threshold)", rel*100)
			report.Regressed = true
		}
	}
	report.Deltas = append(report.Deltas, d)

	// total_cost_usd: relative rise > 15%
	d = MetricDelta{
		Name:      "total_cost_usd",
		Baseline:  baseline.TotalCostUSD,
		Candidate: candidate.TotalCostUSD,
		Delta:     candidate.TotalCostUSD - baseline.TotalCostUSD,
	}
	if baseline.TotalCostUSD > 0 {
		rel := (candidate.TotalCostUSD - baseline.TotalCostUSD) / baseline.TotalCostUSD
		if rel > 0.15 {
			d.Regressed = true
			d.Reason = fmt.Sprintf("cost rose %.1f%% (> 15%% threshold)", rel*100)
			report.Regressed = true
		}
	}
	report.Deltas = append(report.Deltas, d)

	// hallucinations: any increase
	d = MetricDelta{
		Name:      "hallucinations",
		Baseline:  float64(baseline.Hallucinations),
		Candidate: float64(candidate.Hallucinations),
		Delta:     float64(candidate.Hallucinations - baseline.Hallucinations),
	}
	if candidate.Hallucinations > baseline.Hallucinations {
		d.Regressed = true
		d.Reason = fmt.Sprintf("hallucinations rose from %d to %d", baseline.Hallucinations, candidate.Hallucinations)
		report.Regressed = true
	}
	report.Deltas = append(report.Deltas, d)

	report.DeltaSummary = summarize(report)
	return report
}

// DetectRegression loads two missions' trajectories, compares them, and
// emits an eval.regression_detected entry if the candidate regressed.
// Returns the full report regardless so the caller can log or render it.
func DetectRegression(ctx context.Context, db *sql.DB, j journal.Emitter, workspaceID, baselineMissionID, candidateMissionID string) (RegressionReport, error) {
	if j == nil {
		return RegressionReport{}, fmt.Errorf("quartermaster: emitter required")
	}

	baselineSteps, err := Extract(ctx, db, workspaceID, baselineMissionID)
	if err != nil {
		return RegressionReport{}, fmt.Errorf("quartermaster: baseline extract: %w", err)
	}
	candSteps, err := Extract(ctx, db, workspaceID, candidateMissionID)
	if err != nil {
		return RegressionReport{}, fmt.Errorf("quartermaster: candidate extract: %w", err)
	}

	report := Compare(Compute(baselineSteps), Compute(candSteps))

	if report.Regressed {
		if _, err := j.Emit(ctx, journal.Entry{
			WorkspaceID: workspaceID,
			MissionID:   candidateMissionID,
			Type:        journal.EntryEvalRegression,
			Severity:    journal.SeverityWarn,
			ActorType:   journal.ActorSystem,
			ActorID:     "quartermaster",
			Summary:     "regression detected: " + report.DeltaSummary,
			Payload: map[string]any{
				"baseline_mission_id":  baselineMissionID,
				"candidate_mission_id": candidateMissionID,
				"delta_summary":        report.DeltaSummary,
				"regressed_metrics":    regressedNames(report),
			},
			Refs: map[string]any{
				"baseline_mission_id":  baselineMissionID,
				"candidate_mission_id": candidateMissionID,
			},
		}); err != nil {
			return report, fmt.Errorf("quartermaster: emit regression: %w", err)
		}
	}
	return report, nil
}

// summarize renders the regressed metrics as a short human-readable
// sentence for UI + log output.
func summarize(r RegressionReport) string {
	if !r.Regressed {
		return "no regression"
	}
	parts := make([]string, 0, len(r.Deltas))
	for _, d := range r.Deltas {
		if d.Regressed {
			parts = append(parts, d.Reason)
		}
	}
	if len(parts) == 0 {
		return "regression detected"
	}
	return strings.Join(parts, "; ")
}

// regressedNames returns the names of metrics that regressed. Used as a
// structured payload on the eval.regression_detected journal entry.
func regressedNames(r RegressionReport) []string {
	var out []string
	for _, d := range r.Deltas {
		if d.Regressed {
			out = append(out, d.Name)
		}
	}
	return out
}
