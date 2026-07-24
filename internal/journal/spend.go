package journal

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// SpendByAgentBucket is one day×crew×agent cost rollup row, sourced
// from cost.incurred journal entries — the paymaster ledger's source
// of truth for $ amounts (see internal/paymaster/ledger.go's
// emitCostIncurred).
type SpendByAgentBucket struct {
	Date      string  `json:"date"`
	CrewID    string  `json:"crew_id"`
	AgentID   string  `json:"agent_id"`
	CostUSD   float64 `json:"cost_usd"`
	CallCount int     `json:"call_count"`
}

// SpendByRoutineBucket is one day×routine cost rollup row, sourced
// from pipeline_runs.cost_usd (already denormalized from the same
// cost ledger — see migration v83's doc comment).
type SpendByRoutineBucket struct {
	Date         string  `json:"date"`
	PipelineID   string  `json:"pipeline_id"`
	PipelineSlug string  `json:"pipeline_slug"`
	CostUSD      float64 `json:"cost_usd"`
	RunCount     int     `json:"run_count"`
}

// TopSpender is one row of a top-N ranking — either a routine (total
// spend across its runs in the window) or a single run.
type TopSpender struct {
	Kind    string  `json:"kind"` // "routine" | "run"
	ID      string  `json:"id"`
	Label   string  `json:"label"`
	CostUSD float64 `json:"cost_usd"`
}

// SpendResult is the #1404 cost rollup: no single journal entry type
// carries all of (day, crew, agent, routine) at once, so this returns
// multiple breakdown sections rather than one artificially-joined
// table — the same shape RunInsights already uses for
// by_trigger/by_model/by_agent. TotalCostUSD is computed from
// cost.incurred ONLY (the ledger's source of truth) to avoid
// double-counting against the routine breakdown, which reads a
// denormalized copy of the same underlying spend. It is summed by a
// dedicated unbounded query (spendTotal), so it stays exact even when the
// ByAgent breakdown is clipped at maxSpendRows and Truncated is set.
type SpendResult struct {
	Window       string                 `json:"window"`
	TotalCostUSD float64                `json:"total_cost_usd"`
	ByAgent      []SpendByAgentBucket   `json:"by_agent"`
	ByRoutine    []SpendByRoutineBucket `json:"by_routine"`
	TopRoutines  []TopSpender           `json:"top_routines"`
	TopRuns      []TopSpender           `json:"top_runs"`
	Truncated    bool                   `json:"truncated"`
}

// maxSpendRows bounds the by-agent aggregation scan, mirroring
// RunInsights' maxInsightRows — the most-recent rows are aggregated;
// older ones beyond the cap are dropped and Truncated is set. A var (not
// const) only so tests can shrink it to exercise the truncation path
// without inserting 20k distinct buckets; production never reassigns it.
var maxSpendRows = 20000

const defaultTopN = 5

// Spend computes the #1404 cost rollup for a workspace over window.
// topN bounds TopRoutines/TopRuns (defaults to 5 when <= 0).
func Spend(ctx context.Context, db *sql.DB, workspaceID string, window RunInsightsWindow, topN int) (SpendResult, error) {
	if workspaceID == "" {
		return SpendResult{}, fmt.Errorf("journal: Spend requires workspace_id")
	}
	if topN <= 0 {
		topN = defaultTopN
	}
	res := SpendResult{
		Window:      window.normalize(),
		ByAgent:     []SpendByAgentBucket{},
		ByRoutine:   []SpendByRoutineBucket{},
		TopRoutines: []TopSpender{},
		TopRuns:     []TopSpender{},
	}

	since := time.Now().UTC().Add(-window.duration())
	// journal_entries.ts and pipeline_runs.started_at are written by
	// different formatters (formatSinceBound: millisecond precision vs
	// tsformat.Format: 9-digit nanosecond precision, see tsformat's doc
	// comment on why the width must match for a lexicographic cutoff
	// comparison to sort correctly) — each query below is cut against
	// the bound in ITS OWN column's format, not a shared one.
	journalCutoff := formatSinceBound(since)
	runsCutoff := tsformat.Format(since)

	// TotalCostUSD is computed from a dedicated unbounded SUM over the whole
	// window, NOT by accumulating the ByAgent buckets — the bucket scan is
	// capped at maxSpendRows and can truncate, which would make a
	// bucket-summed total silently under-report. The total must stay exact
	// even when the per-bucket breakdown is clipped.
	if err := spendTotal(ctx, db, workspaceID, journalCutoff, &res); err != nil {
		return res, err
	}
	if err := spendByAgent(ctx, db, workspaceID, journalCutoff, &res); err != nil {
		return res, err
	}
	if err := spendByRoutine(ctx, db, workspaceID, runsCutoff, &res); err != nil {
		return res, err
	}
	if err := topRoutines(ctx, db, workspaceID, runsCutoff, topN, &res); err != nil {
		return res, err
	}
	if err := topRuns(ctx, db, workspaceID, runsCutoff, topN, &res); err != nil {
		return res, err
	}
	return res, nil
}

// dayBucketUTC note: journal_entries.ts and pipeline_runs.started_at are
// always written UTC-with-offset (tsformat.Format / formatSinceBound both
// call t.UTC()), so SQLite's bare date(<col>) yields the UTC calendar day
// — date(col,'localtime') would shift late-UTC rows into the wrong day and
// make buckets depend on the server's TZ. Keep these bucketing expressions
// modifier-free; TestSpend_DayBuckets_AreUTC guards against a 'localtime'
// regression.
func spendByAgent(ctx context.Context, db *sql.DB, workspaceID, cutoff string, res *SpendResult) error {
	rows, err := db.QueryContext(ctx, `
SELECT date(ts) AS day, COALESCE(crew_id, ''), COALESCE(agent_id, ''),
       SUM(CAST(json_extract(payload, '$.cost_usd') AS REAL)) AS cost, COUNT(*) AS calls
FROM journal_entries
WHERE workspace_id = ? AND entry_type = 'cost.incurred' AND ts >= ?
GROUP BY day, crew_id, agent_id
ORDER BY day DESC
LIMIT ?`, workspaceID, cutoff, maxSpendRows+1)
	if err != nil {
		return fmt.Errorf("journal: spend by agent: %w", err)
	}
	defer rows.Close()

	scanned := 0
	for rows.Next() {
		scanned++
		if scanned > maxSpendRows {
			res.Truncated = true
			break
		}
		var b SpendByAgentBucket
		var cost sql.NullFloat64
		if err := rows.Scan(&b.Date, &b.CrewID, &b.AgentID, &cost, &b.CallCount); err != nil {
			return fmt.Errorf("journal: scan spend by agent: %w", err)
		}
		b.CostUSD = cost.Float64
		res.ByAgent = append(res.ByAgent, b)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("journal: spend by agent iteration: %w", err)
	}
	return nil
}

// spendTotal computes the exact total cost.incurred spend over the window,
// independent of the maxSpendRows cap that bounds spendByAgent's per-bucket
// breakdown — so TotalCostUSD never under-reports when ByAgent truncates.
func spendTotal(ctx context.Context, db *sql.DB, workspaceID, cutoff string, res *SpendResult) error {
	var total sql.NullFloat64
	err := db.QueryRowContext(ctx, `
SELECT SUM(CAST(json_extract(payload, '$.cost_usd') AS REAL))
FROM journal_entries
WHERE workspace_id = ? AND entry_type = 'cost.incurred' AND ts >= ?`,
		workspaceID, cutoff).Scan(&total)
	if err != nil {
		return fmt.Errorf("journal: spend total: %w", err)
	}
	res.TotalCostUSD = total.Float64
	return nil
}

func spendByRoutine(ctx context.Context, db *sql.DB, workspaceID, cutoff string, res *SpendResult) error {
	rows, err := db.QueryContext(ctx, `
SELECT date(started_at) AS day, pipeline_id, pipeline_slug,
       SUM(cost_usd) AS cost, COUNT(*) AS runs
FROM pipeline_runs
WHERE workspace_id = ? AND started_at >= ?
GROUP BY day, pipeline_id, pipeline_slug
ORDER BY day DESC
LIMIT ?`, workspaceID, cutoff, maxSpendRows+1)
	if err != nil {
		return fmt.Errorf("journal: spend by routine: %w", err)
	}
	defer rows.Close()

	scanned := 0
	for rows.Next() {
		scanned++
		if scanned > maxSpendRows {
			res.Truncated = true
			break
		}
		var b SpendByRoutineBucket
		if err := rows.Scan(&b.Date, &b.PipelineID, &b.PipelineSlug, &b.CostUSD, &b.RunCount); err != nil {
			return fmt.Errorf("journal: scan spend by routine: %w", err)
		}
		res.ByRoutine = append(res.ByRoutine, b)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("journal: spend by routine iteration: %w", err)
	}
	return nil
}

func topRoutines(ctx context.Context, db *sql.DB, workspaceID, cutoff string, topN int, res *SpendResult) error {
	rows, err := db.QueryContext(ctx, `
SELECT pipeline_id, pipeline_slug, SUM(cost_usd) AS total
FROM pipeline_runs
WHERE workspace_id = ? AND started_at >= ?
GROUP BY pipeline_id, pipeline_slug
ORDER BY total DESC
LIMIT ?`, workspaceID, cutoff, topN)
	if err != nil {
		return fmt.Errorf("journal: top routines: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var t TopSpender
		var id, slug string
		if err := rows.Scan(&id, &slug, &t.CostUSD); err != nil {
			return fmt.Errorf("journal: scan top routines: %w", err)
		}
		t.Kind = "routine"
		t.ID = id
		t.Label = slug
		res.TopRoutines = append(res.TopRoutines, t)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("journal: top routines iteration: %w", err)
	}
	return nil
}

func topRuns(ctx context.Context, db *sql.DB, workspaceID, cutoff string, topN int, res *SpendResult) error {
	rows, err := db.QueryContext(ctx, `
SELECT id, pipeline_slug, cost_usd
FROM pipeline_runs
WHERE workspace_id = ? AND started_at >= ?
ORDER BY cost_usd DESC
LIMIT ?`, workspaceID, cutoff, topN)
	if err != nil {
		return fmt.Errorf("journal: top runs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var t TopSpender
		var id, slug string
		if err := rows.Scan(&id, &slug, &t.CostUSD); err != nil {
			return fmt.Errorf("journal: scan top runs: %w", err)
		}
		t.Kind = "run"
		t.ID = id
		t.Label = slug
		res.TopRuns = append(res.TopRuns, t)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("journal: top runs iteration: %w", err)
	}
	return nil
}
