package pipeline

// Per-routine monthly budget meter (#1422 item 3). The engine already has
// a per-run hard gate (DSL.MaxCostUSD, see types.go + executor_retry.go)
// and cost-aware retry; what's been missing is a budget-vs-actual VIEW —
// this file is the read side: aggregate spend per routine for the
// current calendar month, to compare against Pipeline.MonthlyBudgetUSD
// (set via Store.SetMonthlyBudget).

import (
	"context"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// CurrentMonthStart returns the start of the current calendar month in
// UTC, truncated to midnight. Shared by MonthlySpendByPipeline and its
// callers so "this month" means the same instant everywhere.
func CurrentMonthStart(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// MonthlySpendByPipeline sums cost_usd per pipeline_id for workspaceID,
// over runs started since monthStart (see CurrentMonthStart). Pipelines
// with zero spend this month are simply absent from the map — callers
// treat a missing key as 0, not an error.
func (s *RunStore) MonthlySpendByPipeline(ctx context.Context, workspaceID string, monthStart time.Time) (map[string]float64, error) {
	since := tsformat.Format(monthStart)
	rows, err := s.db.QueryContext(ctx, `
SELECT pipeline_id, COALESCE(SUM(cost_usd), 0)
FROM pipeline_runs
WHERE workspace_id = ? AND started_at >= ?
GROUP BY pipeline_id`, workspaceID, since)
	if err != nil {
		return nil, fmt.Errorf("monthly spend: query: %w", err)
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var pipelineID string
		var spent float64
		if err := rows.Scan(&pipelineID, &spent); err != nil {
			return nil, fmt.Errorf("monthly spend: scan: %w", err)
		}
		out[pipelineID] = spent
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("monthly spend: iterate: %w", err)
	}
	return out, nil
}

// MonthlySpendForPipeline is the single-routine convenience wrapper
// around MonthlySpendByPipeline, for the per-routine budget endpoint
// (which doesn't need every other routine's spend).
func (s *RunStore) MonthlySpendForPipeline(ctx context.Context, workspaceID, pipelineID string, monthStart time.Time) (float64, error) {
	since := tsformat.Format(monthStart)
	var spent float64
	err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(cost_usd), 0)
FROM pipeline_runs
WHERE workspace_id = ? AND pipeline_id = ? AND started_at >= ?`,
		workspaceID, pipelineID, since).Scan(&spent)
	if err != nil {
		return 0, fmt.Errorf("monthly spend: %w", err)
	}
	return spent, nil
}
