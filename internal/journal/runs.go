package journal

// Run aggregation over the journal stream. Two engines write runs and key
// them differently, and this file reads both without unifying the write
// side (#2284 — that unification has its own consequences for correlation
// and cost roll-ups and is a separate decision):
//
//   - An ad-hoc agent run is one trace (trace_id == run.id); its
//     journal entries (run.started + one terminal
//     run.{completed|failed|cancelled|timeout}) reconstruct the equivalent
//     of the legacy agent_runs row via GROUP BY trace_id.
//   - A pipeline/routine run never sets trace_id
//     (internal/pipeline/journal.go); its entries
//     (pipeline.run.started + one terminal
//     pipeline.run.{completed|failed}) are grouped by actor_id instead,
//     which that package stamps to the run's own id on every emit.
//
// runAggregatesCTE's grouping key is COALESCE(trace_id, actor_id) so both
// shapes fall out of one query; RunAggregated.Kind says which engine
// produced a given row.
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

// RunKind discriminates which engine produced a RunAggregated: an ad-hoc
// agent/chat execution (internal/api/assignments_run.go, entry_type
// 'run.*', keyed by trace_id) or a routine/pipeline run
// (internal/pipeline/journal.go, entry_type 'pipeline.run.*', keyed by
// actor_id — see the ListRuns doc comment for why the two engines need
// different keys). Added for #2284 so a caller can tell the two apart
// without inferring it from TriggerType, which pipeline runs don't
// currently populate.
type RunKind string

const (
	RunKindAgent    RunKind = "agent"
	RunKindPipeline RunKind = "pipeline"
)

// runStatusFromTerminal maps a run's terminal entry_type onto the
// legacy RunStatus enum. NULL (no terminal yet) → RUNNING; an empty or
// unknown terminal_type would only happen in a corrupt row (we always
// emit terminal alongside DB UPDATE) so it also maps to RUNNING.
//
// Pipeline/routine terminals (EntryPipelineRunCompleted/Failed, #2284) map
// the same way an ad-hoc run's do, with one wrinkle handled by the caller,
// not here: internal/pipeline/journal.go's emitRunFailed reuses
// EntryPipelineRunFailed for BOTH a real failure and a mid-flight cancel
// (there is no dedicated pipeline.run.cancelled entry type), riding the real
// outcome on payload.status instead. scanRunAggregated reads that override
// after calling this function.
func runStatusFromTerminal(terminalType string) RunStatus {
	switch terminalType {
	case string(EntryRunCompleted), string(EntryPipelineRunCompleted):
		return RunStatusCompleted
	case string(EntryRunFailed), string(EntryPipelineRunFailed):
		return RunStatusFailed
	case string(EntryRunCancelled):
		return RunStatusCancelled
	case string(EntryRunTimeout):
		return RunStatusTimeout
	default:
		return RunStatusRunning
	}
}

// RunAggregated is one agent run reconstructed from its run.* journal
// entries. Field set chosen to be a strict superset of what
// /api/v1/runs returns today — no API contract change.
type RunAggregated struct {
	ID          string // trace_id (ad-hoc runs) or actor_id (pipeline/routine runs) — see RunKind.
	WorkspaceID string
	AgentID     string
	ChatID      string
	TriggeredBy string
	TriggerType string
	// Kind says which engine produced this row. See RunKind.
	Kind         RunKind
	Status       RunStatus
	StartedAt    time.Time
	FinishedAt   *time.Time
	ErrorMessage string
	ExitCode     *int
	Metadata     map[string]any
	// Model is the model the run ACTUALLY resolved to (session-init ground
	// truth), recorded on the terminal run.* entry's metadata by the run
	// driver. Empty for runs predating the field or non-Claude adapters.
	Model string
	// Session provenance the run driver merges into the terminal entry's
	// metadata from the CLI's session-init event: which binary answered
	// (the adapter is pinned while containers install latest), which
	// credential path resolved, whether the permission bypass took, and the
	// CLI's own correlation key for the transcript. All empty for runs
	// predating the fields and for adapters that emit no session-init.
	CLIVersion     string
	APIKeySource   string
	PermissionMode string
	SessionID      string
	// MCPServerErrors are the --mcp-config entries the CLI refused to load at
	// startup. Non-empty means the agent ran with less capability than it was
	// configured for — exit code 0 does not contradict it, which is exactly
	// why the loss needs its own field rather than a line in a log.
	MCPServerErrors []MCPServerError
	// MCPServerErrorCount is how many servers the CLI said it skipped, which is
	// not always len(MCPServerErrors): the producer drops entries whose fields
	// it could not read, so the list is what this build understood and the count
	// is what happened. Zero for runs predating the field — a reader compares
	// the two and only reports a gap when the count is the larger.
	MCPServerErrorCount int
	// MCPServerErrorsTruncated reports that the stored list was capped, so the
	// servers it names are not all of them.
	MCPServerErrorsTruncated bool
	// PermissionDenials names the tools the CLI refused to let the agent use.
	// Tool NAMES only: the producer drops the denied input before storing it,
	// because that input is arbitrary agent-generated text and this record is
	// hash-chained. "Bash was denied" is the diagnosis; the command line is
	// not ours to keep forever.
	//
	// Empty is the normal case and means nothing was denied — which matters,
	// because a run blocked by permissions otherwise reads as a run that CHOSE
	// not to act, sending an operator after a prompt problem.
	//
	// One entry per denied TOOL, not per refusal: the producer collapses an
	// agent's forty retries of the same blocked Bash into one entry carrying
	// the count. A single "unrecognized_shape" entry means the CLI reported a
	// refusal in a shape the producer could not read — the run WAS blocked and
	// the tool is not knowable.
	PermissionDenials []DeniedTool
	// PermissionDenialsTruncated reports that the stored denial list was capped,
	// so the tools it names are not all of them.
	PermissionDenialsTruncated bool
	CreatedAt                  time.Time // == StartedAt for runs (we don't track a separate creation moment)
}

// DeniedTool is one tool the CLI refused to let the agent use, with how many
// times it refused it.
//
// The count is the reason this is a struct and not a name. The producer
// deliberately collapses repeats and attaches the tally, because one refusal is
// an agent that tried something once and forty is an agent hammering a wall it
// cannot see — different diagnoses, different fixes. It was a []string here, so
// the tally died one hop after it was created.
//
// Zero means the record predates the count, NOT "denied zero times": a run
// record only carries a tool it was denied at least once, and inventing a 1
// would be a claim the row does not make.
type DeniedTool struct {
	// ToolName is the tool the CLI named — or, when it named none, the CATEGORY
	// the producer recorded instead (its unrecognized_shape sentinel). The
	// fallback is applied at decode so every renderer shows the alarm; a
	// sentinel nobody can display keeps the alarm and destroys the ability to
	// act on it.
	ToolName string `json:"tool_name"`
	Count    int    `json:"count,omitempty"`
}

// MCPServerError is one MCP server the CLI skipped at startup, as reported on
// its session-init event. Field names match the CLI's own, and this struct is
// what the API serialises, so renaming a field here changes the wire.
type MCPServerError struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Message is normally EMPTY, and that is not a bug. The producer
	// (orchestrator.MergeSessionInitMeta) projects each entry down to name +
	// category before it reaches a run record, because the message is
	// CLI-supplied free text describing a config file and the run's terminal
	// journal entry is hash-chained — what lands there cannot be redacted.
	// The field stays so a run recorded before that projection still renders,
	// and so a consumer that has the full text from elsewhere can fill it.
	// The live chat card is where an operator reads the message.
	Message string `json:"message,omitempty"`
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

// pipelineRunTerminalEntryTypes are the terminal entry types a
// pipeline/routine run writes (internal/pipeline/journal.go). There is no
// pipeline.run.cancelled or pipeline.run.timeout — a run cancelled
// mid-flight is still recorded as EntryPipelineRunFailed, with the real
// outcome riding on payload.status="CANCELLED" instead (see
// runStatusFromTerminal and scanRunAggregated's override).
var pipelineRunTerminalEntryTypes = []string{
	string(EntryPipelineRunCompleted),
	string(EntryPipelineRunFailed),
}

// runTerminalEntryTypes is the union of terminalEntryTypes and
// pipelineRunTerminalEntryTypes — #2284 widened the terminal-* projections
// below to cover both engines' runs with one IN-list. Built once from the
// two slices above (rather than via append(terminalEntryTypes, ...)) so
// growing it can never alias, and silently corrupt, terminalEntryTypes' own
// backing array.
var runTerminalEntryTypes = func() []string {
	out := make([]string, 0, len(terminalEntryTypes)+len(pipelineRunTerminalEntryTypes))
	out = append(out, terminalEntryTypes...)
	out = append(out, pipelineRunTerminalEntryTypes...)
	return out
}()

// runAggregateProjections is the canonical set of MAX(CASE WHEN ...) column
// projections the run_aggregates CTE can expose, in canonical order. Entries
// with terminal=true carry a %s slot for the terminal entry-type IN-list and
// consume one set of terminal placeholders each.
//
// started_at/agent_id/started_payload match BOTH 'run.started' (ad-hoc) and
// 'pipeline.run.started' (routine, #2284) — a run_aggregates group is always
// homogeneous (one trace_id/actor_id belongs to exactly one engine, see
// runAggregatesCTE), so admitting both literals here costs nothing when only
// one of them is actually present in a given group.
//
// triggered_by deliberately stays 'run.started'-only: a pipeline run's
// actor_id is the run's OWN id (internal/pipeline/journal.go), not the
// triggering actor, so projecting it into triggered_by would put a run
// pointing at itself into a field callers read as "who kicked this off".
// originalStartCond is the predicate started_at/agent_id/started_payload
// filter on: a 'started'-family row that is NOT a resume marker. A
// pipeline/routine run whose execution parks on a wait:approval gate, a
// wait:event signal, or a boot-time restart re-enters via
// pipelineEmitContext.emitRunResumed (internal/pipeline/journal.go), which
// emits a SECOND EntryPipelineRunStarted row for the SAME run — same
// actor_id, later ts, and payload.resumed=true (there is no dedicated
// "resumed" entry type; emitRunResumed reuses started with a marker).
// Ad-hoc runs never re-emit run.started (internal/api/assignments_run.go
// has exactly one call site), so this is a no-op for them — payload.resumed
// is simply absent, and COALESCE(...,0)=0 always holds.
//
// Without this filter, MAX(ts) over BOTH the original and every resume
// picks the LATEST re-entry instead of the run's true start — a routine
// that was approved three hours after it parked would report a three-hour-
// old run as having started seconds ago, and MAX() over started_payload (a
// TEXT column) would pick whichever candidate payload sorts
// lexicographically largest, unrelated to which row produced the chosen
// timestamp. Excluding resumed rows leaves exactly one 'started'-family row
// per run — the original emitRunStarted / assignments_run.go emit — so MAX
// over a single candidate is trivially correct again.
const originalStartCond = "entry_type IN ('run.started','pipeline.run.started') " +
	"AND COALESCE(json_extract(payload, '$.resumed'), 0) = 0"

var runAggregateProjections = []struct {
	name     string
	terminal bool
	expr     string
}{
	{"started_at", false, "MAX(CASE WHEN " + originalStartCond + " THEN ts END) AS started_at"},
	{"finished_at", true, "MAX(CASE WHEN entry_type IN %s THEN ts END) AS finished_at"},
	{"terminal_type", true, "MAX(CASE WHEN entry_type IN %s THEN entry_type END) AS terminal_type"},
	{"agent_id", false, "MAX(CASE WHEN " + originalStartCond + " THEN agent_id END) AS agent_id"},
	{"triggered_by", false, "MAX(CASE WHEN entry_type = 'run.started' THEN actor_id END) AS triggered_by"},
	{"started_payload", false, "MAX(CASE WHEN " + originalStartCond + " THEN payload END) AS started_payload"},
	{"terminal_payload", true, "MAX(CASE WHEN entry_type IN %s THEN payload END) AS terminal_payload"},
	// kind discriminates which engine produced the group (RunKind). A group
	// is homogeneous, so MAX over a constant string per matching row is
	// stable — every row in an "agent" group ties into 'agent', every row in
	// a "pipeline" group into 'pipeline'. Deliberately NOT filtered by
	// originalStartCond — a resumed row still carries entry_type
	// 'pipeline.run.started', so it still says which engine produced the
	// group.
	{"kind", false, "MAX(CASE WHEN entry_type LIKE 'pipeline.run.%' THEN 'pipeline' ELSE 'agent' END) AS kind"},
}

// runAggregatesCTE assembles the shared "WITH run_aggregates AS (...)" query
// prefix: one row per run, columns picked via the MAX(CASE WHEN ...) idiom
// which is portable SQL. cols selects projections by name; output always
// follows the canonical order in runAggregateProjections. innerWhere is the
// raw WHERE text applied during grouping (filters on indexed columns, so
// SQLite can prune before the GROUP BY).
//
// The grouping key is COALESCE(trace_id, actor_id): an ad-hoc run's rows
// always carry a non-NULL trace_id (COALESCE picks it, actor_id — the
// triggering actor, not the run — is never consulted); a pipeline/routine
// run's rows never set trace_id (#2284), so COALESCE falls back to
// actor_id, which internal/pipeline/journal.go stamps to the run's own id
// on every emit. The two never collide within one query: innerWhere only
// admits pipeline.run.* rows when they lack a trace_id and run.* rows when
// they have one (see ListRuns/GetRunByID).
//
// The SELECT list aliases this expression AS "trace_id" so every downstream
// column-name reference (SELECT/ORDER BY) stays unchanged — but GROUP BY is
// the one place that alias CANNOT be trusted: SQLite resolves a GROUP BY
// bareword against a same-named FROM-clause column when one exists, not the
// SELECT list's alias, so `GROUP BY trace_id` silently grouped on the raw
// journal_entries.trace_id column — NULL for every pipeline/routine row —
// merging every routine run in a workspace into one bucket. GROUP BY below
// repeats the full COALESCE(...) expression for that reason; see its own
// comment.
//
// Index note: idx_journal_ws_trace (workspace_id, trace_id) WHERE trace_id
// IS NOT NULL covers the ad-hoc branch only — a pipeline/routine run's rows
// fall outside that partial index (trace_id IS NULL there by construction)
// and are found via the workspace_id/entry_type prefix instead. Acceptable
// at current pipeline-run volumes; a follow-up migration could add a
// matching partial index on (workspace_id, actor_id) WHERE entry_type LIKE
// 'pipeline.run.%' if that scan ever shows up in profiling.
//
// The returned args bind the terminal IN-list placeholders — one copy of
// runTerminalEntryTypes per terminal projection selected, in projection
// order. They MUST come before innerWhere's own args in the final arg list,
// because the IN-lists appear in the SELECT clause ahead of the WHERE.
func runAggregatesCTE(cols []string, innerWhere string) (string, []any) {
	want := make(map[string]bool, len(cols))
	for _, c := range cols {
		want[c] = true
	}
	terminalIN := "(" + sqlInPlaceholders(len(runTerminalEntryTypes)) + ")"
	lines := []string{"    SELECT COALESCE(trace_id, actor_id) AS trace_id"}
	var args []any
	for _, p := range runAggregateProjections {
		if !want[p.name] {
			continue
		}
		expr := p.expr
		if p.terminal {
			expr = fmt.Sprintf(expr, terminalIN)
			for _, t := range runTerminalEntryTypes {
				args = append(args, t)
			}
		}
		lines = append(lines, "           "+expr)
	}
	cte := "\nWITH run_aggregates AS (\n" +
		strings.Join(lines, ",\n") + "\n" +
		"    FROM journal_entries\n" +
		"    WHERE " + innerWhere + "\n" +
		// GROUP BY must repeat the COALESCE expression, not the bareword
		// "trace_id" — SQLite resolves a GROUP BY bareword against the
		// FROM-clause column of that name (journal_entries.trace_id),
		// NOT the SELECT list's "AS trace_id" alias, even though the
		// alias shadows the same name. `GROUP BY trace_id` therefore
		// grouped every pipeline/routine row — trace_id is NULL for all
		// of them by construction — into ONE bucket keyed on NULL,
		// silently merging every routine run in a workspace into a
		// single aggregate row instead of the COALESCE(trace_id,
		// actor_id) row-per-run the SELECT computes. Confirmed against
		// modernc.org/sqlite (the driver this package uses): `SELECT
		// COALESCE(a,b) AS x ... GROUP BY x` groups on the source column
		// x if one exists, not the alias; `GROUP BY COALESCE(a,b)` groups
		// on the computed value, which is what every caller here needs.
		"    GROUP BY COALESCE(trace_id, actor_id)\n)"
	return cte, args
}

// ListRuns groups journal_entries by run — trace_id for an ad-hoc run.* run,
// actor_id for a pipeline/routine pipeline.run.* run (#2284, see the
// runAggregatesCTE doc comment) — and returns one RunAggregated per run.
// Total is the unfiltered-by-paging row count so callers can render
// pagination state.
//
// Index used: idx_journal_ws_trace (workspace_id, trace_id) WHERE
// trace_id IS NOT NULL — Phase D migration v60 — covers the ad-hoc branch.
// Without it SQLite would fall back to a full table scan for that branch;
// with it the workspace prefix is a covering range scan. The pipeline/
// routine branch (trace_id IS NULL by construction) isn't covered by that
// partial index; see runAggregatesCTE's index note.
// maxRunsPage caps a single ListRuns page. Doubles as the pre-allocation size
// for the result slice — see the make() call below.
const maxRunsPage = 100

func ListRuns(ctx context.Context, db *sql.DB, q RunsQuery) ([]RunAggregated, int, error) {
	if q.WorkspaceID == "" {
		return nil, 0, fmt.Errorf("journal: ListRuns requires workspace_id")
	}
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > maxRunsPage {
		q.Limit = maxRunsPage
	}

	// Inner WHERE (applied during grouping) — filters that touch indexed
	// columns directly, so SQLite can prune before the GROUP BY.
	//
	// The OR'd condition is #2284: an ad-hoc run's rows carry trace_id
	// (require it non-NULL to stay on idx_journal_ws_trace); a
	// pipeline/routine run's rows never set trace_id at all
	// (internal/pipeline/journal.go), so that branch is keyed on entry_type
	// alone and picked up by runAggregatesCTE's COALESCE(trace_id, actor_id)
	// grouping key instead.
	innerConds := []string{
		"workspace_id = ?",
		"((trace_id IS NOT NULL AND entry_type LIKE 'run.%') OR entry_type LIKE 'pipeline.run.%')",
	}
	innerArgs := []any{q.WorkspaceID}
	if q.AgentID != "" {
		innerConds = append(innerConds, "agent_id = ?")
		innerArgs = append(innerArgs, q.AgentID)
	}

	cte, cteArgs := runAggregatesCTE(
		[]string{"started_at", "finished_at", "terminal_type", "agent_id",
			"triggered_by", "started_payload", "terminal_payload", "kind"},
		strings.Join(innerConds, " AND "))
	// Outer WHERE — filters that operate on derived columns (status,
	// json_extract on payload.trigger_type or .tags).
	outerConds := []string{"started_at IS NOT NULL"}
	var outerArgs []any
	if q.Status != "" {
		switch q.Status {
		case RunStatusRunning:
			outerConds = append(outerConds, "terminal_type IS NULL")
		case RunStatusCompleted:
			outerConds = append(outerConds, "terminal_type IN (?, ?)")
			outerArgs = append(outerArgs, string(EntryRunCompleted), string(EntryPipelineRunCompleted))
		case RunStatusFailed:
			// pipeline.run.failed also covers a mid-flight CANCEL (no
			// dedicated pipeline.run.cancelled entry type — see
			// runStatusFromTerminal) so a FAILED filter must exclude the
			// rows whose payload.status says otherwise.
			outerConds = append(outerConds,
				"(terminal_type = ? OR (terminal_type = ? AND COALESCE(json_extract(terminal_payload, '$.status'), 'FAILED') <> 'CANCELLED'))")
			outerArgs = append(outerArgs, string(EntryRunFailed), string(EntryPipelineRunFailed))
		case RunStatusCancelled:
			outerConds = append(outerConds,
				"(terminal_type = ? OR (terminal_type = ? AND json_extract(terminal_payload, '$.status') = 'CANCELLED'))")
			outerArgs = append(outerArgs, string(EntryRunCancelled), string(EntryPipelineRunFailed))
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
       agent_id, triggered_by, started_payload, terminal_payload, kind
FROM run_aggregates
WHERE ` + strings.Join(outerConds, " AND ") + `
ORDER BY started_at DESC
LIMIT ? OFFSET ?`

	// Compose final args. Placeholders appear in source order across the
	// CTE — the terminal IN-lists in the SELECT come first (bound by
	// cteArgs), then the WHERE clause (workspace_id and optional
	// agent_id), then the outer WHERE filters and finally LIMIT/OFFSET.
	args := make([]any, 0, len(cteArgs)+len(innerArgs)+len(outerArgs)+2)
	args = append(args, cteArgs...)
	args = append(args, innerArgs...)
	args = append(args, outerArgs...)
	args = append(args, q.Limit, q.Offset)

	rows, err := db.QueryContext(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("journal: list runs: %w", err)
	}
	defer rows.Close()

	// Pre-size to the page cap itself rather than to q.Limit. The limit is
	// already clamped to [1, maxRunsPage] in the validation block above, so a
	// row can never exceed this capacity and the only cost is at most
	// maxRunsPage-1 unused slots on a small page.
	//
	// Passing the clamped variable instead kept go/uncontrolled-allocation-size
	// (alert 722) open through two attempted mitigations — an explicit
	// if-clamp, then the min() builtin — because CodeQL credits neither as a
	// barrier and still sees a request-derived value reaching make(). A
	// constant ends that: there is no tainted value left to flag.
	out := make([]RunAggregated, 0, maxRunsPage)
	for rows.Next() {
		r, err := scanRunAggregated(rows, q.WorkspaceID)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// Total row count (unbounded by limit/offset) for pagination UI.
	//
	// Skip the extra COUNT(*) query when the answer is already provable from
	// this page alone:
	//   - a non-empty page shorter than q.Limit means LIMIT wasn't fully
	//     consumed, so this is the last page and total = offset + len(rows)
	//     regardless of what offset was requested.
	//   - an empty first page (offset 0) means there are no matching rows.
	// An empty page at a non-zero offset is ambiguous (offset could be
	// anywhere past a total we haven't measured), and a full page always is
	// — both still run countRuns. See issue #1411.
	var total int
	switch {
	case len(out) > 0 && len(out) < q.Limit:
		total = q.Offset + len(out)
	case len(out) == 0 && q.Offset == 0:
		total = 0
	default:
		total, err = countRuns(ctx, db, innerConds, innerArgs, outerConds, outerArgs)
		if err != nil {
			return nil, 0, err
		}
	}
	return out, total, nil
}

// GetRunByID looks up a single run by id — trace_id for an ad-hoc run,
// actor_id for a pipeline/routine run (#2284; the caller passes one id and
// GetRunByID tries both, since it doesn't know which engine produced it) —
// scoped to workspaceID. Returns (nil, nil) when no such run exists in the
// caller's workspace — callers translate that into a 404 themselves so
// cross-tenant lookups stay masked as "not found" rather than leaking
// existence.
//
// Runs the same run_aggregates CTE as ListRuns with the id folded into the
// inner WHERE, so the ad-hoc branch hits idx_journal_ws_trace (or the
// narrower idx_journal_ws_trace_runs partial index once migration v153
// lands) as an exact probe instead of ListRuns' unbounded-offset page scan
// (internal/api/runs.go RunHandler.Get used to page up to 1000 rows). The
// pipeline/routine branch has no equivalent index yet — see
// runAggregatesCTE's index note.
func GetRunByID(ctx context.Context, db *sql.DB, workspaceID, traceID string) (*RunAggregated, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("journal: GetRunByID requires workspace_id")
	}
	if traceID == "" {
		return nil, fmt.Errorf("journal: GetRunByID requires trace_id")
	}

	// #2284: an ad-hoc run is found by trace_id; a pipeline/routine run
	// never sets trace_id and is found by actor_id instead (see the
	// runAggregatesCTE doc comment). The caller passes one id and doesn't
	// know which engine produced it, so both branches are tried — traceID
	// is bound to both placeholders.
	cte, cteArgs := runAggregatesCTE(
		[]string{"started_at", "finished_at", "terminal_type", "agent_id",
			"triggered_by", "started_payload", "terminal_payload", "kind"},
		"workspace_id = ? AND ((trace_id = ? AND entry_type LIKE 'run.%') OR (actor_id = ? AND entry_type LIKE 'pipeline.run.%'))")
	q := cte + `
SELECT trace_id, started_at, finished_at, terminal_type,
       agent_id, triggered_by, started_payload, terminal_payload, kind
FROM run_aggregates
WHERE started_at IS NOT NULL
LIMIT 1`

	args := make([]any, 0, len(cteArgs)+3)
	args = append(args, cteArgs...)
	args = append(args, workspaceID, traceID, traceID)

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("journal: get run by id: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("journal: get run by id: %w", err)
		}
		return nil, nil
	}
	r, err := scanRunAggregated(rows, workspaceID)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// scanRunAggregated scans one run_aggregates row — column order (trace_id,
// started_at, finished_at, terminal_type, agent_id, triggered_by,
// started_payload, terminal_payload, kind) — into a RunAggregated, decoding
// the run.started / terminal JSON payloads into the derived fields.
func scanRunAggregated(rows *sql.Rows, workspaceID string) (RunAggregated, error) {
	var (
		traceID, startedTS                             string
		finishedTS, terminalType, agentID, triggeredBy sql.NullString
		startedPayload, terminalPayload                sql.NullString
		kind                                           sql.NullString
	)
	if err := rows.Scan(&traceID, &startedTS, &finishedTS, &terminalType,
		&agentID, &triggeredBy, &startedPayload, &terminalPayload, &kind); err != nil {
		return RunAggregated{}, fmt.Errorf("journal: scan run: %w", err)
	}
	r := RunAggregated{
		ID:          traceID,
		WorkspaceID: workspaceID,
		AgentID:     agentID.String,
		TriggeredBy: triggeredBy.String,
		Kind:        RunKindAgent,
	}
	if kind.String == "pipeline" {
		r.Kind = RunKindPipeline
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
	r.Status = runStatusFromTerminal(terminalType.String)
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
				r.applySessionProvenance(v)
			}
			// A pipeline.run.failed terminal covers both a real failure AND
			// a mid-flight cancel — internal/pipeline/journal.go's
			// emitRunFailed has no dedicated pipeline.run.cancelled entry
			// type, so it rides the real outcome on payload.status instead
			// (#2284). runStatusFromTerminal already mapped this row to
			// FAILED; override to CANCELLED when the payload says so.
			if terminalType.String == string(EntryPipelineRunFailed) {
				if v, ok := p["status"].(string); ok && v == "CANCELLED" {
					r.Status = RunStatusCancelled
				}
			}
		}
	}
	return r, nil
}

// applySessionProvenance reads the session-init provenance out of a terminal
// entry's metadata (written by orchestrator.MergeSessionInitMeta).
//
// Terminal-only, unlike model, which also honours run.started as a fallback:
// these fields describe how the CLI was invoked and only the run driver can
// know them, while run.started's metadata is caller-supplied on API-triggered
// runs. Reading them there too would let a caller stamp its own answer to
// "which key served this run".
func (r *RunAggregated) applySessionProvenance(md map[string]any) {
	for _, f := range []struct {
		key string
		dst *string
	}{
		{"cli_version", &r.CLIVersion},
		{"api_key_source", &r.APIKeySource},
		{"permission_mode", &r.PermissionMode},
		{"session_id", &r.SessionID},
	} {
		if v, ok := md[f.key].(string); ok && v != "" {
			*f.dst = v
		}
	}
	if v, ok := md["mcp_server_errors"]; ok {
		r.MCPServerErrors = decodeMCPServerErrors(v)
	}
	if v, ok := md["permission_denials"]; ok {
		r.PermissionDenials = decodeDeniedTools(v)
	}
	// The counts and the truncation markers the producer writes alongside those
	// two lists. Both lists are lossy by design — one drops entries it cannot
	// project, both are capped — and these are the only fields that say so.
	// Reading them nowhere is what made the alarms unreachable.
	r.MCPServerErrorCount = provenanceInt(md["mcp_server_error_count"])
	r.MCPServerErrorsTruncated, _ = md["mcp_server_errors_truncated"].(bool)
	r.PermissionDenialsTruncated, _ = md["permission_denials_truncated"].(bool)
}

// provenanceInt reads a count that may have been stored in-process (int) or read
// back through JSON (float64). Anything else yields 0: a count we cannot read is
// a count we do not report, and a coerced one would be a number an operator
// trusts.
func provenanceInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0
		}
		return int(i)
	}
	return 0
}

// decodeDeniedTools pulls the denied tools out of the projected denial list,
// tolerating both the in-process []map form and the raw JSON a DB read yields.
// A malformed value degrades to no denials rather than a partial list: this
// drives an operator's reading of why a run did nothing, and half an answer is
// worse than none.
//
// The count is carried through. The producer attaches it deliberately so that
// one refusal reads differently from forty; a decoder that kept only the name
// threw that away at the first hop and no surface downstream could get it back.
// Absent on rows written before the producer attached it, and 0 there means
// "not recorded" rather than a tally.
//
// Type is read as a fallback for the name because the producer records a
// CATEGORY there, with no tool_name, when the CLI reported a refusal in a shape
// it could not read (orchestrator's unrecognized_shape sentinel) — it will not
// invent a tool name, and reading tool_name only would drop that entry and put
// the run back to reading as one that CHOSE not to act, which is the reason the
// sentinel is written at all. The same fallback carries any future entry the CLI
// describes by category rather than by name.
func decodeDeniedTools(v any) []DeniedTool {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var entries []struct {
		ToolName string `json:"tool_name"`
		Type     string `json:"type"`
		Count    int    `json:"count"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	out := make([]DeniedTool, 0, len(entries))
	for _, e := range entries {
		name := e.ToolName
		if name == "" {
			name = e.Type
		}
		if name == "" {
			continue
		}
		out = append(out, DeniedTool{ToolName: name, Count: e.Count})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// decodeMCPServerErrors types the array the adapter deliberately passed
// through unparsed. Re-encoding and decoding beats asserting element by
// element: the value arrives as []any after a DB round-trip but as a
// json.RawMessage from an in-process caller, and one path should not read
// differently from the other.
//
// A value that does not fit the shape yields nil. The field's contract is
// "these servers were skipped" — a half-decoded entry would misreport which,
// and that is worse than reporting none.
func decodeMCPServerErrors(v any) []MCPServerError {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out []MCPServerError
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// countRuns mirrors the ListRuns CTE but selects COUNT(*). Kept as a
// private helper so the filter logic stays in one place.
func countRuns(ctx context.Context, db *sql.DB,
	innerConds []string, innerArgs []any,
	outerConds []string, outerArgs []any) (int, error) {
	// terminal_payload is needed here too — not just by ListRuns' own SELECT
	// — because the FAILED/CANCELLED branches of the outerConds this
	// function receives (built in ListRuns) json_extract it to tell a
	// pipeline.run.failed cancel apart from a real failure (#2284).
	cte, cteArgs := runAggregatesCTE(
		[]string{"started_at", "terminal_type", "started_payload", "terminal_payload"},
		strings.Join(innerConds, " AND "))
	q := cte + `
SELECT COUNT(*) FROM run_aggregates
WHERE ` + strings.Join(outerConds, " AND ")

	// Placeholder order: terminal IN-list in the CTE SELECT first, then
	// the inner WHERE args, then the outer WHERE args.
	args := make([]any, 0, len(cteArgs)+len(innerArgs)+len(outerArgs))
	args = append(args, cteArgs...)
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
	Today       int // any run.started with ts >= start-of-today (UTC)
	FailedToday int // run.failed or run.timeout with ts >= start-of-today (UTC)
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
	// "Today" is a half-open range [start-of-today UTC, +inf) on the indexed
	// ts column. The previous form wrapped ts in date() (`date(ts) =
	// date('now')`), which is non-sargable and forced a full workspace scan.
	// ts is stored at millisecond precision (boundLayout) and is
	// lexicographically ordered, so comparing against a lower bound formatted
	// with the same layout is both correct and index-friendly. formatSinceBound
	// is the package's canonical lower-bound formatter.
	now := time.Now().UTC()
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	todayBound := formatSinceBound(startOfToday)
	// Today = run.started rows with ts >= start-of-today UTC
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT trace_id)
FROM journal_entries
WHERE workspace_id = ?
  AND entry_type = 'run.started'
  AND ts >= ?`, workspaceID, todayBound).Scan(&res.Today); err != nil {
		return res, fmt.Errorf("journal: run stats today: %w", err)
	}
	// FailedToday = run.failed/timeout rows with ts >= start-of-today UTC
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT trace_id)
FROM journal_entries
WHERE workspace_id = ?
  AND entry_type IN ('run.failed','run.timeout')
  AND ts >= ?`, workspaceID, todayBound).Scan(&res.FailedToday); err != nil {
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

// duration maps a window to the Go duration subtracted from now to derive
// the cutoff. Unknown values fall back to 24h so a bad query param can't widen
// the scan to the whole table. Stored timestamps are UTC, so a day is exactly
// 24h (no DST/calendar arithmetic needed).
func (w RunInsightsWindow) duration() time.Duration {
	switch w {
	case RunWindow7d:
		return 7 * 24 * time.Hour
	case RunWindow30d:
		return 30 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// normalize returns the canonical window string, defaulting unknown inputs to
// 24h (matching duration).
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

	// One row per trace started within the window. Filtering all of a trace's
	// entries by ts >= cutoff is safe: a run's entries cluster around its start,
	// so a run.started inside the window keeps its terminal entry too.
	//
	// The cutoff is computed in Go and formatted with the SAME layout the writer
	// uses (boundLayout, via formatSinceBound), so `ts >= ?` is a plain
	// lexicographic range on the indexed ts column — sargable. The previous form
	// wrapped ts in datetime() to reconcile the stored format with
	// datetime('now', ?)'s "YYYY-MM-DD HH:MM:SS" output, but that made the
	// predicate non-sargable and forced a full scan. Formatting both sides
	// identically removes the need for the function wrap entirely.
	cutoff := formatSinceBound(time.Now().UTC().Add(-window.duration()))
	cte, cteArgs := runAggregatesCTE(
		[]string{"started_at", "finished_at", "terminal_type", "agent_id",
			"started_payload", "terminal_payload"},
		"workspace_id = ? AND trace_id IS NOT NULL AND entry_type LIKE 'run.%' AND ts >= ?")
	q := cte + `
SELECT started_at, finished_at, terminal_type, agent_id, started_payload, terminal_payload
FROM run_aggregates
WHERE started_at IS NOT NULL
ORDER BY started_at DESC
LIMIT ?`

	// Placeholder order: 3 terminal IN-lists in the CTE SELECT, then
	// workspace_id, the window cutoff, and the LIMIT (+1 to detect overflow).
	args := make([]any, 0, len(cteArgs)+3)
	args = append(args, cteArgs...)
	args = append(args, workspaceID)
	args = append(args, cutoff)
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
		status := runStatusFromTerminal(terminalType.String)
		isFailed := status == RunStatusFailed || status == RunStatusTimeout
		switch status {
		case RunStatusCompleted:
			res.Succeeded++
		case RunStatusFailed, RunStatusTimeout:
			res.Failed++
		case RunStatusCancelled:
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
		return res, fmt.Errorf("journal: run insights iteration: %w", err)
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
