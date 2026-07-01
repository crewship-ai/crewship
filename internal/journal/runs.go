package journal

// Run aggregation over the journal stream. Each agent run is one trace
// (trace_id == run.id); the per-run journal entries (run.started + one
// terminal run.{completed|failed|cancelled|timeout}) reconstruct the
// equivalent of the legacy agent_runs row via GROUP BY trace_id.
//
// This is the read-side that backs /api/v1/runs once Phase E lands.
// The write side (run.* emits) is Phase C.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RunStatus mirrors the legacy agent_runs.status enum so the API
// response shape stays identical post-migration. UI knows these values.
type RunStatus string

const (
	RunStatusRunning   RunStatus = "RUNNING"
	RunStatusCompleted RunStatus = "COMPLETED"
	RunStatusFailed    RunStatus = "FAILED"
	RunStatusCancelled RunStatus = "CANCELLED"
	RunStatusTimeout   RunStatus = "TIMEOUT"
)

// RunAggregated is one agent run reconstructed from its run.* journal
// entries. Field set chosen to be a strict superset of what
// /api/v1/runs returns today — no API contract change.
type RunAggregated struct {
	ID           string // trace_id
	WorkspaceID  string
	AgentID      string
	ChatID       string
	TriggeredBy  string
	TriggerType  string
	Status       RunStatus
	StartedAt    time.Time
	FinishedAt   *time.Time
	ErrorMessage string
	ExitCode     *int
	Metadata     map[string]any
	// Model is the model the run ACTUALLY resolved to (session-init ground
	// truth), recorded on the terminal run.* entry's metadata by the run
	// driver. Empty for runs predating the field or non-Claude adapters.
	Model     string
	CreatedAt time.Time // == StartedAt for runs (we don't track a separate creation moment)
}

// RunsQuery filters ListRuns. WorkspaceID is required; rest are
// optional. Pagination is offset-based because we aggregate (keyset over
// a derived column would need a synthetic key) — the index keeps the
// scan cheap.
type RunsQuery struct {
	WorkspaceID string
	AgentID     string
	Status      RunStatus // RUNNING / COMPLETED / FAILED / CANCELLED / TIMEOUT
	TriggerType string
	Tag         string // matches a value inside metadata.tags array
	Limit       int    // default 50, max 100
	Offset      int
}

// terminalEntryTypes is a small constant set we reference twice in the
// SQL — having it here keeps the two case lists in sync.
var terminalEntryTypes = []string{
	string(EntryRunCompleted),
	string(EntryRunFailed),
	string(EntryRunCancelled),
	string(EntryRunTimeout),
}

// ListRuns groups journal_entries by trace_id over run.* event types and
// returns one RunAggregated per trace. Total is the unfiltered-by-paging
// row count so callers can render pagination state.
//
// Index used: idx_journal_ws_trace (workspace_id, trace_id) WHERE
// trace_id IS NOT NULL — Phase D migration v60. Without it SQLite would
// fall back to a full table scan; with it the workspace prefix is a
// covering range scan.
func ListRuns(ctx context.Context, db *sql.DB, q RunsQuery) ([]RunAggregated, int, error) {
	if q.WorkspaceID == "" {
		return nil, 0, fmt.Errorf("journal: ListRuns requires workspace_id")
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 100 {
		q.Limit = 100
	}

	// Inner WHERE (applied during grouping) — filters that touch indexed
	// columns directly, so SQLite can prune before the GROUP BY.
	innerConds := []string{
		"workspace_id = ?",
		"trace_id IS NOT NULL",
		"entry_type LIKE 'run.%'",
	}
	innerArgs := []any{q.WorkspaceID}
	if q.AgentID != "" {
		innerConds = append(innerConds, "agent_id = ?")
		innerArgs = append(innerArgs, q.AgentID)
	}

	// terminalIN expands to an IN-list of the terminal entry_types so
	// the CTE picks them up uniformly.
	terminalIN := "(" + sqlInPlaceholders(len(terminalEntryTypes)) + ")"
	terminalArgs := make([]any, 0, len(terminalEntryTypes))
	for _, t := range terminalEntryTypes {
		terminalArgs = append(terminalArgs, t)
	}

	// CTE expansion: one row per trace_id, columns picked via
	// MAX(CASE WHEN ...) idiom which is portable SQL and uses the
	// already-built index.
	cte := `
WITH run_aggregates AS (
    SELECT trace_id,
           MAX(CASE WHEN entry_type = 'run.started' THEN ts END)        AS started_at,
           MAX(CASE WHEN entry_type IN ` + terminalIN + ` THEN ts END)   AS finished_at,
           MAX(CASE WHEN entry_type IN ` + terminalIN + ` THEN entry_type END) AS terminal_type,
           MAX(CASE WHEN entry_type = 'run.started' THEN agent_id END)  AS agent_id,
           MAX(CASE WHEN entry_type = 'run.started' THEN actor_id END)  AS triggered_by,
           MAX(CASE WHEN entry_type = 'run.started' THEN payload END)   AS started_payload,
           MAX(CASE WHEN entry_type IN ` + terminalIN + ` THEN payload END) AS terminal_payload
    FROM journal_entries
    WHERE ` + strings.Join(innerConds, " AND ") + `
    GROUP BY trace_id
)`
	// Outer WHERE — filters that operate on derived columns (status,
	// json_extract on payload.trigger_type or .tags).
	outerConds := []string{"started_at IS NOT NULL"}
	var outerArgs []any
	if q.Status != "" {
		switch q.Status {
		case RunStatusRunning:
			outerConds = append(outerConds, "terminal_type IS NULL")
		case RunStatusCompleted:
			outerConds = append(outerConds, "terminal_type = ?")
			outerArgs = append(outerArgs, string(EntryRunCompleted))
		case RunStatusFailed:
			outerConds = append(outerConds, "terminal_type = ?")
			outerArgs = append(outerArgs, string(EntryRunFailed))
		case RunStatusCancelled:
			outerConds = append(outerConds, "terminal_type = ?")
			outerArgs = append(outerArgs, string(EntryRunCancelled))
		case RunStatusTimeout:
			outerConds = append(outerConds, "terminal_type = ?")
			outerArgs = append(outerArgs, string(EntryRunTimeout))
		}
	}
	if q.TriggerType != "" {
		outerConds = append(outerConds, "json_extract(started_payload, '$.trigger_type') = ?")
		outerArgs = append(outerArgs, q.TriggerType)
	}
	if q.Tag != "" {
		// EXISTS over json_each so we match a single tag inside
		// metadata.tags array regardless of position.
		outerConds = append(outerConds, "EXISTS (SELECT 1 FROM json_each(json_extract(started_payload, '$.metadata.tags')) j WHERE j.value = ?)")
		outerArgs = append(outerArgs, q.Tag)
	}

	listSQL := cte + `
SELECT trace_id, started_at, finished_at, terminal_type,
       agent_id, triggered_by, started_payload, terminal_payload
FROM run_aggregates
WHERE ` + strings.Join(outerConds, " AND ") + `
ORDER BY started_at DESC
LIMIT ? OFFSET ?`

	// Compose final args. Placeholders appear in source order across the
	// CTE — three terminal IN-lists in the SELECT come first (each has
	// len(terminalEntryTypes) placeholders), then the WHERE clause
	// (workspace_id and optional agent_id), then the outer WHERE
	// filters and finally LIMIT/OFFSET.
	args := make([]any, 0, 3*len(terminalArgs)+len(innerArgs)+len(outerArgs)+2)
	args = append(args, terminalArgs...)
	args = append(args, terminalArgs...)
	args = append(args, terminalArgs...)
	args = append(args, innerArgs...)
	args = append(args, outerArgs...)
	args = append(args, q.Limit, q.Offset)

	rows, err := db.QueryContext(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("journal: list runs: %w", err)
	}
	defer rows.Close()

	// q.Limit is clamped to [1, 100] in the validation block above;
	// re-apply at the make() call so the cap is locally visible to
	// CodeQL's go/uncontrolled-allocation-size rule (min builtin is on
	// CodeQL's recognised-sanitiser list; the local allocCap = q.Limit
	// + if-clamp pattern was not).
	allocCap := min(q.Limit, 100)
	if allocCap < 0 {
		allocCap = 0
	}
	out := make([]RunAggregated, 0, allocCap)
	for rows.Next() {
		var (
			traceID, startedTS                             string
			finishedTS, terminalType, agentID, triggeredBy sql.NullString
			startedPayload, terminalPayload                sql.NullString
		)
		if err := rows.Scan(&traceID, &startedTS, &finishedTS, &terminalType,
			&agentID, &triggeredBy, &startedPayload, &terminalPayload); err != nil {
			return nil, 0, fmt.Errorf("journal: scan run: %w", err)
		}
		r := RunAggregated{
			ID:          traceID,
			WorkspaceID: q.WorkspaceID,
			AgentID:     agentID.String,
			TriggeredBy: triggeredBy.String,
		}
		if t, perr := parseJournalTS(startedTS); perr == nil {
			r.StartedAt = t
			r.CreatedAt = t
		}
		if finishedTS.Valid {
			if t, perr := parseJournalTS(finishedTS.String); perr == nil {
				r.FinishedAt = &t
			}
		}
		// Status mapping: terminal entry_type → legacy enum, NULL →
		// RUNNING. Empty terminal_type would only happen in a corrupt
		// row (we always emit terminal alongside DB UPDATE) so we map
		// to RUNNING by default.
		switch terminalType.String {
		case string(EntryRunCompleted):
			r.Status = RunStatusCompleted
		case string(EntryRunFailed):
			r.Status = RunStatusFailed
		case string(EntryRunCancelled):
			r.Status = RunStatusCancelled
		case string(EntryRunTimeout):
			r.Status = RunStatusTimeout
		default:
			r.Status = RunStatusRunning
		}
		// Pull trigger_type, chat_id, metadata out of the run.started
		// payload — that's the authoritative source.
		if startedPayload.Valid && startedPayload.String != "" && startedPayload.String != "{}" {
			var p map[string]any
			if err := json.Unmarshal([]byte(startedPayload.String), &p); err == nil {
				if v, ok := p["trigger_type"].(string); ok {
					r.TriggerType = v
				}
				if v, ok := p["chat_id"].(string); ok {
					r.ChatID = v
				}
				if v, ok := p["metadata"].(map[string]any); ok {
					r.Metadata = v
					// run.started rarely carries the resolved model (it's
					// known only after session-init), but honour it as a
					// fallback so a future producer that stamps it here works.
					if m, ok := v["model"].(string); ok && m != "" {
						r.Model = m
					}
				}
			}
		}
		// exit_code, error_message and the resolved model live on the
		// terminal entry — the run driver knows the served model only after
		// the stream completes, so the terminal metadata is authoritative.
		if terminalPayload.Valid && terminalPayload.String != "" && terminalPayload.String != "{}" {
			var p map[string]any
			if err := json.Unmarshal([]byte(terminalPayload.String), &p); err == nil {
				if v, ok := p["error_message"].(string); ok {
					r.ErrorMessage = v
				}
				// JSON numbers come back as float64 from encoding/json.
				if v, ok := p["exit_code"].(float64); ok {
					ec := int(v)
					r.ExitCode = &ec
				}
				if v, ok := p["metadata"].(map[string]any); ok {
					if m, ok := v["model"].(string); ok && m != "" {
						r.Model = m
					}
				}
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// Total row count (unbounded by limit/offset) for pagination UI.
	total, err := countRuns(ctx, db, q, innerConds, innerArgs, outerConds, outerArgs, terminalArgs)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// countRuns mirrors the ListRuns CTE but selects COUNT(*). Kept as a
// private helper so the filter logic stays in one place.
func countRuns(ctx context.Context, db *sql.DB, _ RunsQuery,
	innerConds []string, innerArgs []any,
	outerConds []string, outerArgs []any,
	terminalArgs []any) (int, error) {
	terminalIN := "(" + sqlInPlaceholders(len(terminalEntryTypes)) + ")"
	q := `
WITH run_aggregates AS (
    SELECT trace_id,
           MAX(CASE WHEN entry_type = 'run.started' THEN ts END) AS started_at,
           MAX(CASE WHEN entry_type IN ` + terminalIN + ` THEN entry_type END) AS terminal_type,
           MAX(CASE WHEN entry_type = 'run.started' THEN payload END) AS started_payload
    FROM journal_entries
    WHERE ` + strings.Join(innerConds, " AND ") + `
    GROUP BY trace_id
)
SELECT COUNT(*) FROM run_aggregates
WHERE ` + strings.Join(outerConds, " AND ")

	// Placeholder order: terminal IN-list in the CTE SELECT first, then
	// the inner WHERE args, then the outer WHERE args.
	args := make([]any, 0, len(terminalArgs)+len(innerArgs)+len(outerArgs))
	args = append(args, terminalArgs...)
	args = append(args, innerArgs...)
	args = append(args, outerArgs...)
	var total int
	if err := db.QueryRowContext(ctx, q, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("journal: count runs: %w", err)
	}
	return total, nil
}

// RunStatsResult is the small KPI bundle the /runs page renders at the
// top: how many runs are live now, how many started today, how many
// failed today.
type RunStatsResult struct {
	Running     int // run.started without a terminal entry yet
	Today       int // any run.started with date(ts) = today (UTC)
	FailedToday int // run.failed or run.timeout with date(ts) = today
}

// RunStats computes the three KPI counters in one query for a workspace.
// Used by the Runs API and the dashboard widget.
func RunStats(ctx context.Context, db *sql.DB, workspaceID string) (RunStatsResult, error) {
	if workspaceID == "" {
		return RunStatsResult{}, fmt.Errorf("journal: RunStats requires workspace_id")
	}
	var res RunStatsResult
	// Running = traces with run.started and no terminal in the same
	// trace AND workspace. The je2 subquery must repeat workspace_id —
	// without it a terminal entry that happens to share trace_id with
	// another workspace (test fixtures, restored backups, future cross-
	// tenant constructs) would suppress this workspace's running count.
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT trace_id)
FROM journal_entries je1
WHERE je1.workspace_id = ?
  AND je1.entry_type = 'run.started'
  AND NOT EXISTS (
      SELECT 1 FROM journal_entries je2
      WHERE je2.workspace_id = je1.workspace_id
        AND je2.trace_id = je1.trace_id
        AND je2.entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')
  )`, workspaceID).Scan(&res.Running); err != nil {
		return res, fmt.Errorf("journal: run stats running: %w", err)
	}
	// Today = run.started rows with ts >= start-of-today UTC
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT trace_id)
FROM journal_entries
WHERE workspace_id = ?
  AND entry_type = 'run.started'
  AND date(ts) = date('now')`, workspaceID).Scan(&res.Today); err != nil {
		return res, fmt.Errorf("journal: run stats today: %w", err)
	}
	// FailedToday = run.failed/timeout rows with date(ts)=today
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT trace_id)
FROM journal_entries
WHERE workspace_id = ?
  AND entry_type IN ('run.failed','run.timeout')
  AND date(ts) = date('now')`, workspaceID).Scan(&res.FailedToday); err != nil {
		return res, fmt.Errorf("journal: run stats failed today: %w", err)
	}
	return res, nil
}

// RunInsightsWindow bounds the aggregation window for RunInsights. Runs are
// bucketed by their run.started timestamp; a run started inside the window
// counts even if it finished (or is still running) later.
type RunInsightsWindow string

const (
	RunWindow24h RunInsightsWindow = "24h"
	RunWindow7d  RunInsightsWindow = "7d"
	RunWindow30d RunInsightsWindow = "30d"
)

// sqlModifier maps a window to the SQLite datetime() modifier used to derive
// the cutoff. Unknown values fall back to 24h so a bad query param can't widen
// the scan to the whole table.
func (w RunInsightsWindow) sqlModifier() string {
	switch w {
	case RunWindow7d:
		return "-7 days"
	case RunWindow30d:
		return "-30 days"
	default:
		return "-1 day"
	}
}

// normalize returns the canonical window string, defaulting unknown inputs to
// 24h (matching sqlModifier).
func (w RunInsightsWindow) normalize() string {
	switch w {
	case RunWindow7d:
		return "7d"
	case RunWindow30d:
		return "30d"
	default:
		return "24h"
	}
}

// CategoryCount is one breakdown bucket: total runs plus the failed subset for
// a single key (a trigger type, model id, …). Failed counts run.failed and
// run.timeout; cancelled and running runs are neither succeeded nor failed but
// still contribute to Total.
type CategoryCount struct {
	Key    string `json:"key"`
	Total  int    `json:"total"`
	Failed int    `json:"failed"`
}

// AgentCount is a per-agent breakdown row. The API layer resolves AgentID to a
// display name + crew before returning it to the UI — the journal layer only
// knows the id.
type AgentCount struct {
	AgentID string `json:"agent_id"`
	Total   int    `json:"total"`
	Failed  int    `json:"failed"`
}

// RunInsightsResult is the fleet-wide operational aggregate over a window: the
// numbers the Journal "Runs" ops overview renders (outcome split, duration
// percentiles, and breakdowns by trigger / model / agent). It spans ALL runs
// in the workspace, not just routine-triggered ones.
type RunInsightsResult struct {
	Window        string          `json:"window"`
	Total         int             `json:"total"`
	Succeeded     int             `json:"succeeded"`
	Failed        int             `json:"failed"`
	Running       int             `json:"running"`
	DurationP50Ms int64           `json:"duration_p50_ms"`
	DurationP95Ms int64           `json:"duration_p95_ms"`
	ByTrigger     []CategoryCount `json:"by_trigger"`
	ByModel       []CategoryCount `json:"by_model"`
	ByAgent       []AgentCount    `json:"by_agent"`
	// Truncated is set when the window held more runs than maxInsightRows, so
	// the aggregate is computed over the most-recent maxInsightRows only. The
	// caller surfaces this rather than presenting a partial total as complete.
	Truncated bool `json:"truncated"`
}

// maxInsightRows bounds the in-memory aggregation scan so a very large window
// can't balloon memory. The most-recent runs are aggregated; older ones beyond
// the cap are dropped and Truncated is set.
const maxInsightRows = 20000

const insightUnknownKey = "unknown"

// RunInsights computes the fleet operations aggregate for a workspace over the
// given window. It reuses the same trace_id grouping as ListRuns, then folds
// the rows in Go — the window bounds the row count and the fold is trivially
// testable. Crew rollups and agent display names are added by the API layer;
// here ByAgent is keyed on the raw agent_id.
func RunInsights(ctx context.Context, db *sql.DB, workspaceID string, window RunInsightsWindow) (RunInsightsResult, error) {
	if workspaceID == "" {
		return RunInsightsResult{}, fmt.Errorf("journal: RunInsights requires workspace_id")
	}
	res := RunInsightsResult{
		Window:    window.normalize(),
		ByTrigger: []CategoryCount{},
		ByModel:   []CategoryCount{},
		ByAgent:   []AgentCount{},
	}

	terminalIN := "(" + sqlInPlaceholders(len(terminalEntryTypes)) + ")"
	terminalArgs := make([]any, 0, len(terminalEntryTypes))
	for _, t := range terminalEntryTypes {
		terminalArgs = append(terminalArgs, t)
	}

	// One row per trace started within the window. Filtering all of a trace's
	// entries by ts >= cutoff is safe: a run's entries cluster around its start,
	// so a run.started inside the window keeps its terminal entry too.
	q := `
WITH run_aggregates AS (
    SELECT trace_id,
           MAX(CASE WHEN entry_type = 'run.started' THEN ts END)              AS started_at,
           MAX(CASE WHEN entry_type IN ` + terminalIN + ` THEN ts END)         AS finished_at,
           MAX(CASE WHEN entry_type IN ` + terminalIN + ` THEN entry_type END) AS terminal_type,
           MAX(CASE WHEN entry_type = 'run.started' THEN agent_id END)        AS agent_id,
           MAX(CASE WHEN entry_type = 'run.started' THEN payload END)         AS started_payload,
           MAX(CASE WHEN entry_type IN ` + terminalIN + ` THEN payload END)    AS terminal_payload
    FROM journal_entries
    WHERE workspace_id = ?
      AND trace_id IS NOT NULL
      AND entry_type LIKE 'run.%'
      AND ts >= datetime('now', ?)
    GROUP BY trace_id
)
SELECT started_at, finished_at, terminal_type, agent_id, started_payload, terminal_payload
FROM run_aggregates
WHERE started_at IS NOT NULL
ORDER BY started_at DESC
LIMIT ?`

	// Placeholder order: 3 terminal IN-lists in the CTE SELECT, then
	// workspace_id, the window modifier, and the LIMIT (+1 to detect overflow).
	args := make([]any, 0, 3*len(terminalArgs)+3)
	args = append(args, terminalArgs...)
	args = append(args, terminalArgs...)
	args = append(args, terminalArgs...)
	args = append(args, workspaceID)
	args = append(args, window.sqlModifier())
	args = append(args, maxInsightRows+1)

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return res, fmt.Errorf("journal: run insights: %w", err)
	}
	defer rows.Close()

	byTrigger := map[string]*CategoryCount{}
	byModel := map[string]*CategoryCount{}
	byAgent := map[string]*AgentCount{}
	durations := make([]int64, 0, 256)

	scanned := 0
	for rows.Next() {
		scanned++
		if scanned > maxInsightRows {
			res.Truncated = true
			break
		}
		var (
			startedTS                       string
			finishedTS, terminalType        sql.NullString
			agentID                         sql.NullString
			startedPayload, terminalPayload sql.NullString
		)
		if err := rows.Scan(&startedTS, &finishedTS, &terminalType, &agentID, &startedPayload, &terminalPayload); err != nil {
			return res, fmt.Errorf("journal: scan run insight: %w", err)
		}

		res.Total++

		// Outcome buckets. Terminal type decides; a NULL terminal is RUNNING.
		isFailed := terminalType.String == string(EntryRunFailed) || terminalType.String == string(EntryRunTimeout)
		switch terminalType.String {
		case string(EntryRunCompleted):
			res.Succeeded++
		case string(EntryRunFailed), string(EntryRunTimeout):
			res.Failed++
		case string(EntryRunCancelled):
			// counted in Total, excluded from success/fail rate
		default:
			res.Running++
		}

		// Duration over finished runs (any terminal type with both timestamps).
		if finishedTS.Valid {
			if st, e1 := parseJournalTS(startedTS); e1 == nil {
				if ft, e2 := parseJournalTS(finishedTS.String); e2 == nil {
					if ms := ft.Sub(st).Milliseconds(); ms >= 0 {
						durations = append(durations, ms)
					}
				}
			}
		}

		trigger := insightUnknownKey
		if v := jsonStringField(startedPayload, "trigger_type"); v != "" {
			trigger = v
		}
		model := insightModel(startedPayload, terminalPayload)

		bumpCategory(byTrigger, trigger, isFailed)
		bumpCategory(byModel, model, isFailed)
		if agentID.String != "" {
			a := byAgent[agentID.String]
			if a == nil {
				a = &AgentCount{AgentID: agentID.String}
				byAgent[agentID.String] = a
			}
			a.Total++
			if isFailed {
				a.Failed++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return res, err
	}

	res.DurationP50Ms = percentile(durations, 0.50)
	res.DurationP95Ms = percentile(durations, 0.95)
	res.ByTrigger = sortedCategories(byTrigger)
	res.ByModel = sortedCategories(byModel)
	res.ByAgent = sortedAgents(byAgent)
	return res, nil
}

// bumpCategory increments a breakdown bucket, allocating it on first use.
func bumpCategory(m map[string]*CategoryCount, key string, failed bool) {
	c := m[key]
	if c == nil {
		c = &CategoryCount{Key: key}
		m[key] = c
	}
	c.Total++
	if failed {
		c.Failed++
	}
}

// jsonStringField pulls a top-level string field from a JSON payload column,
// returning "" when absent/unparseable.
func jsonStringField(payload sql.NullString, field string) string {
	if !payload.Valid || payload.String == "" || payload.String == "{}" {
		return ""
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(payload.String), &p); err != nil {
		return ""
	}
	if v, ok := p[field].(string); ok {
		return v
	}
	return ""
}

// insightModel resolves the run's model the same way ListRuns does: prefer the
// terminal entry's metadata.model (authoritative — known after session-init),
// falling back to run.started metadata for still-running rows. Returns the
// unknown sentinel when neither carries a model.
func insightModel(startedPayload, terminalPayload sql.NullString) string {
	if m := jsonMetadataModel(terminalPayload); m != "" {
		return m
	}
	if m := jsonMetadataModel(startedPayload); m != "" {
		return m
	}
	return insightUnknownKey
}

// jsonMetadataModel extracts payload.metadata.model, "" when absent.
func jsonMetadataModel(payload sql.NullString) string {
	if !payload.Valid || payload.String == "" || payload.String == "{}" {
		return ""
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(payload.String), &p); err != nil {
		return ""
	}
	meta, ok := p["metadata"].(map[string]any)
	if !ok {
		return ""
	}
	if v, ok := meta["model"].(string); ok {
		return v
	}
	return ""
}

// percentile returns the nearest-rank percentile (p in [0,1]) of the values,
// 0 for an empty slice. Sorts a copy so the caller's slice order is untouched.
func percentile(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]int64, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	// nearest-rank: idx = ceil(p*n) - 1, clamped to [0, n-1]
	idx := int(float64(len(sorted))*p+0.9999999) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// sortedCategories flattens the map into a slice ordered by total desc, then
// key asc, so the output is deterministic (stable across identical inputs).
func sortedCategories(m map[string]*CategoryCount) []CategoryCount {
	out := make([]CategoryCount, 0, len(m))
	for _, c := range m {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// sortedAgents flattens the per-agent map ordered by total desc, then id asc.
func sortedAgents(m map[string]*AgentCount) []AgentCount {
	out := make([]AgentCount, 0, len(m))
	for _, a := range m {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out
}
