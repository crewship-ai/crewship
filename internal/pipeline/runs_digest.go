package pipeline

// Workspace digest aggregate (#1422 item 4) — the deterministic,
// read-only rollup a workspace-digest routine's `query` step reads. Lives
// on RunStore (not a new package) because it is exactly the same
// pipeline_runs projection every other run-history read already goes
// through, just aggregated instead of listed.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// DigestStats is the run/cost/failure rollup for one workspace over a
// trailing window. Always scoped to exactly one workspace_id — callers
// (the query step) pass RunInput.WorkspaceID, so a routine can never read
// another tenant's run history.
type DigestStats struct {
	WindowHours  int     `json:"window_hours"`
	TotalRuns    int     `json:"total_runs"`
	Completed    int     `json:"completed"`
	Failed       int     `json:"failed"`
	Waiting      int     `json:"waiting"`
	Cancelled    int     `json:"cancelled"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	// TopFailures is capped at maxDigestTopFailures, most-frequent first.
	TopFailures []DigestFailure `json:"top_failures,omitempty"`
	// SummaryMD is a pre-rendered markdown digest — the workspace-digest
	// routine template extracts this one field (via a transform step) and
	// hands it straight to a notify step, no per-field templating needed.
	SummaryMD string `json:"summary_md"`
}

// DigestFailure is one row of the top-failures breakdown.
type DigestFailure struct {
	PipelineSlug string `json:"pipeline_slug"`
	Count        int    `json:"count"`
}

// maxDigestTopFailures caps the top-failures breakdown so a workspace
// with many distinct failing routines still gets a scannable digest.
const maxDigestTopFailures = 5

// DigestStats computes the run/cost/failure rollup for workspaceID over
// the trailing windowHours (default 24 when <= 0). started_at is compared
// as a string against the fixed-width tsformat bound — pipeline_runs has
// always written started_at via tsformat (see runs.go formatRFC3339), so
// this is a same-format comparison, not the legacy mixed-format caveat
// schedules.go documents for next_run_at.
func (s *RunStore) DigestStats(ctx context.Context, workspaceID string, windowHours int) (*DigestStats, error) {
	if windowHours <= 0 {
		windowHours = 24
	}
	since := tsformat.Format(time.Now().Add(-time.Duration(windowHours) * time.Hour))

	stats := &DigestStats{WindowHours: windowHours}
	rows, err := s.db.QueryContext(ctx, `
SELECT status, COUNT(*), COALESCE(SUM(cost_usd), 0)
FROM pipeline_runs
WHERE workspace_id = ? AND started_at >= ?
GROUP BY status`, workspaceID, since)
	if err != nil {
		return nil, fmt.Errorf("digest stats: query status counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		var cost float64
		if err := rows.Scan(&status, &count, &cost); err != nil {
			return nil, fmt.Errorf("digest stats: scan status counts: %w", err)
		}
		stats.TotalRuns += count
		stats.TotalCostUSD += cost
		switch status {
		case "completed":
			stats.Completed = count
		case "failed":
			stats.Failed = count
		case "waiting":
			stats.Waiting = count
		case "cancelled":
			stats.Cancelled = count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("digest stats: iterate status counts: %w", err)
	}

	failRows, err := s.db.QueryContext(ctx, `
SELECT pipeline_slug, COUNT(*) AS n
FROM pipeline_runs
WHERE workspace_id = ? AND started_at >= ? AND status = 'failed'
GROUP BY pipeline_slug
ORDER BY n DESC, pipeline_slug ASC
LIMIT ?`, workspaceID, since, maxDigestTopFailures)
	if err != nil {
		return nil, fmt.Errorf("digest stats: query top failures: %w", err)
	}
	defer failRows.Close()
	for failRows.Next() {
		var f DigestFailure
		if err := failRows.Scan(&f.PipelineSlug, &f.Count); err != nil {
			return nil, fmt.Errorf("digest stats: scan top failures: %w", err)
		}
		stats.TopFailures = append(stats.TopFailures, f)
	}
	if err := failRows.Err(); err != nil {
		return nil, fmt.Errorf("digest stats: iterate top failures: %w", err)
	}

	stats.SummaryMD = renderDigestSummaryMD(stats)
	return stats, nil
}

// renderDigestSummaryMD builds the markdown digest a notify step delivers
// verbatim. Kept deterministic and dependency-free (no template engine) —
// this is server-side formatting, not routine-authored content.
func renderDigestSummaryMD(s *DigestStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Workspace digest — last %dh\n\n", s.WindowHours)
	if s.TotalRuns == 0 {
		b.WriteString("No routine runs in this window.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "- **%d** routine run(s) — %d completed, %d failed, %d waiting, %d cancelled\n",
		s.TotalRuns, s.Completed, s.Failed, s.Waiting, s.Cancelled)
	fmt.Fprintf(&b, "- **$%.4f** total cost\n", s.TotalCostUSD)
	if len(s.TopFailures) > 0 {
		b.WriteString("\n**Top failures:**\n")
		for _, f := range s.TopFailures {
			fmt.Fprintf(&b, "- `%s` — %d\n", f.PipelineSlug, f.Count)
		}
	}
	return b.String()
}
