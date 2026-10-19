package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// RoutineHealth is what the Routines Health table shows for one routine
// (ODCH-RT-07): its newest finished-or-running runs as outcomes, the success
// rate over the finished ones, and what it ran and spent in a window.
type RoutineHealth struct {
	PipelineID string
	// Outcomes are the newest runs, oldest first: completed, failed, stopped,
	// waiting or running.
	Outcomes []string
	// Runs and SpendUSD cover started_at inside the window.
	Runs     int
	SpendUSD float64
}

// healthOutcome names how a run stands: a failed outcome wins over a
// completed status, because the run did not do its job.
func healthOutcome(status, outcome string) string {
	switch {
	case status == "failed" || status == "interrupted" || outcome == "FAILED":
		return "failed"
	case status == "cancelled":
		return "stopped"
	case status == "waiting":
		return "waiting"
	case status == "running" || status == "queued":
		return "running"
	default:
		return "completed"
	}
}

// HealthByPipeline reads, per routine of the workspace, the newest `last`
// real runs (not test or dry runs) and the runs and spend since `since`.
// Routines without a run are absent from the map.
func (s *RunStore) HealthByPipeline(ctx context.Context, workspaceID string, since time.Time, last int) (map[string]*RoutineHealth, error) {
	if last <= 0 {
		last = 20
	}
	out := map[string]*RoutineHealth{}
	get := func(id string) *RoutineHealth {
		h := out[id]
		if h == nil {
			h = &RoutineHealth{PipelineID: id}
			out[id] = h
		}
		return h
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT pipeline_id, COUNT(*), COALESCE(SUM(cost_usd), 0)
FROM pipeline_runs
WHERE workspace_id = ? AND mode = 'run' AND started_at >= ?
GROUP BY pipeline_id`, workspaceID, tsformat.Format(since))
	if err != nil {
		return nil, fmt.Errorf("routine health: window: %w", err)
	}
	for rows.Next() {
		var id string
		var n int
		var spend float64
		if err := rows.Scan(&id, &n, &spend); err != nil {
			rows.Close()
			return nil, fmt.Errorf("routine health: scan window: %w", err)
		}
		h := get(id)
		h.Runs, h.SpendUSD = n, spend
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("routine health: window: %w", err)
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `
SELECT pipeline_id, status, COALESCE(outcome, '') FROM (
  SELECT pipeline_id, status, outcome, started_at,
         ROW_NUMBER() OVER (PARTITION BY pipeline_id ORDER BY started_at DESC, id DESC) AS n
  FROM pipeline_runs WHERE workspace_id = ? AND mode = 'run'
) WHERE n <= ? ORDER BY pipeline_id, started_at ASC, n DESC`, workspaceID, last)
	if err != nil {
		return nil, fmt.Errorf("routine health: outcomes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, status, outcome string
		if err := rows.Scan(&id, &status, &outcome); err != nil {
			return nil, fmt.Errorf("routine health: scan outcome: %w", err)
		}
		h := get(id)
		h.Outcomes = append(h.Outcomes, healthOutcome(status, outcome))
	}
	return out, rows.Err()
}
