package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/chain"
	"github.com/crewship-ai/crewship/internal/journal"
)

// ChainsListHandler serves GET /api/v1/chains — the index of chain RUNS, one
// row per chain, newest first.
//
// It is the missing half of GET /api/v1/chains/{anchor}. The walk answers
// "what happened around this thing" but needs an anchor you already know;
// nothing answered "what workflows ran in this workspace", which is why the
// Activity sidebar could only offer flat lists of issues and routines. This
// route is that question, and the walk stays the way to drill into any row.
//
// A chain is identified by pipeline_runs.chain_origin — the run that started
// it. The executor stamps every persisted run with one (its own id when it is
// the root, the inherited one otherwise, see chainOriginForRun), so the index
// is a GROUP BY over that single column rather than a traversal. That is why
// this handler owns its SQL while chain_handler.go owns none: there is no walk
// here to share, only one aggregate query. The node/edge vocabulary is still
// borrowed from internal/chain (started_by_kind reuses its kind names) so a
// client can hand any row's origin straight back to the walk.
type ChainsListHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewChainsListHandler(db *sql.DB, logger *slog.Logger) *ChainsListHandler {
	return &ChainsListHandler{db: db, logger: logger}
}

// Page bounds. The query is a grouped scan of the workspace's runs, so the cap
// is what keeps one authenticated request off the busiest table in the schema
// for an unbounded amount of time. 50 is a screenful of Activity; the ceiling
// is stated in the docs rather than left for a caller to discover.
const (
	DefaultChainsListLimit = 50
	MaxChainsListLimit     = 200
)

// MaxChainSummaryRefs bounds how many issues and how many agents a single row
// carries.
//
// This is a LIST. Everything a row says about what a chain touched costs a join
// over tables that grow with every dispatch, and doing it per row without a
// bound is the slow query behind an authenticated route — the same argument
// that caps the page itself. 5 is what a rail line renders before it starts
// eliding anyway, and the exact totals ride alongside (ChainSummary.IssueCount
// / AgentCount) so a truncated list is visible as truncation rather than
// mistaken for the whole story.
//
// The bound is per row, not per page: the queries that fill these are batched
// across the whole page and keep the top MaxChainSummaryRefs PER origin, so the
// worst case for one request is limit × 5 issues and limit × 5 agents.
const MaxChainSummaryRefs = 5

// Trigger kinds that internal/chain has no node kind for, because they are not
// rows the walk can stand on: a workspace member, a cron schedule, an inbound
// webhook, and "we cannot tell". Everything else reuses chain.Kind* so the two
// endpoints name the same thing the same way.
const (
	chainStartKindUser    = "user"
	chainStartKindSchdule = "schedule"
	chainStartKindWebhook = "webhook"
	chainStartKindUnknown = "unknown"
)

// ChainSummary is one chain run: the origin, what set it off, how big it got,
// and the window it covers.
//
// Deliberately flat. The row is a list line — a client renders it without
// dereferencing anything, and asks GET /api/v1/chains/{origin} when the reader
// wants the graph.
type ChainSummary struct {
	// Origin is pipeline_runs.chain_origin: the id of the run that started the
	// chain. It is also a valid anchor for the walk.
	Origin string `json:"origin"`

	// StartedByKind names WHAT set the chain off, resolved from the root run's
	// triggered_via: "automation", "issue", "routine", "run", "user",
	// "schedule", "webhook", or "unknown". StartedBy is the human label for
	// it (a rule name, an issue title, a person's name), StartedByKey the
	// handle a human recognises (the event that armed a rule, the issue
	// identifier), StartedByID the row it points at.
	//
	// An unresolvable pointer leaves the label falling back to the raw trigger
	// rather than inventing one: a rule that has been deleted, or one that
	// belongs to another workspace, must not lend its name to this row.
	StartedByKind string `json:"started_by_kind"`
	StartedByID   string `json:"started_by_id,omitempty"`
	StartedByKey  string `json:"started_by_key,omitempty"`
	StartedBy     string `json:"started_by"`

	// TriggeredVia is the raw pipeline_runs.triggered_via of the root run,
	// carried unresolved so a client can tell 'manual' from 'wake_check' even
	// where both render as the same label.
	TriggeredVia string `json:"triggered_via,omitempty"`

	// Routine is the root run's routine. Empty when the root run itself is no
	// longer in the table (swept by retention, or the chain was rooted at a
	// journal entry rather than a run).
	RoutineID   string `json:"routine_id,omitempty"`
	RoutineSlug string `json:"routine_slug,omitempty"`

	// Runs counts the runs still recorded for this chain, MaxChainDepth is the
	// deepest composed hop any of them reached. Depth 0 with one run is a run
	// somebody started by hand; depth 3 is a chain that built itself.
	Runs          int `json:"runs"`
	MaxChainDepth int `json:"max_chain_depth"`

	// FailedRuns counts runs with status 'failed'. Failed is the flag a list
	// renders. 'interrupted' is NOT counted: a process that died mid-run is an
	// operational event, and folding it into "this chain failed" would make the
	// flag mean two different things at once.
	FailedRuns int  `json:"failed_runs"`
	Failed     bool `json:"failed"`

	// RunningRuns and WaitingRuns are the chain's NON-TERMINAL runs, split by
	// whether anything can move without a person.
	//
	// They exist because the timestamps cannot answer this. LastActivity falls
	// back to started_at while a run is in flight, so a chain parked on an
	// approval since Tuesday and one that finished on Tuesday carry the same
	// instant — and "what needs me right now" is the question this page is
	// opened twice a day to answer.
	//
	// Split rather than one "active" count, because the two are different asks:
	// a running chain resolves itself, a waiting one never will. Folding them
	// together files "awaiting your approval" under the same word as "busy",
	// which is how a queue goes unread.
	//
	// Derived from status, NOT from a NULL ended_at. The cheap derivation would
	// report every interrupted run — a process that died mid-run and will never
	// write an end — as running forever.
	RunningRuns int `json:"running_runs"`
	WaitingRuns int `json:"waiting_runs"`

	// FirstActivity/LastActivity bound the chain: the earliest start and the
	// latest end (falling back to start for a run still going). Rows are
	// ordered by LastActivity descending.
	FirstActivity string `json:"first_activity"`
	LastActivity  string `json:"last_activity"`

	// DurationMS is the WALL CLOCK from FirstActivity to LastActivity, and is
	// null when there is no span to measure between. See chainElapsedMS.
	DurationMS *int64 `json:"duration_ms"`

	// Issues and Agents are the concrete nouns — what this run actually
	// touched, which is the only thing that tells two runs of one routine
	// apart. Capped at MaxChainSummaryRefs each; IssueCount and AgentCount are
	// the full totals, so a cut list reads as cut.
	//
	// Empty is a fact, not a gap: a routine that touched no issue and
	// dispatched nobody has nothing to show, and the row says so by omitting
	// the arrays rather than by carrying two empty ones.
	Issues     []ChainIssueRef `json:"issues,omitempty"`
	IssueCount int             `json:"issue_count"`
	Agents     []ChainAgentRef `json:"agents,omitempty"`
	AgentCount int             `json:"agent_count"`
}

// ChainIssueRef is one issue this chain created or changed.
//
// Identifier ("ENG-7") rather than the id is what a human recognises, and it is
// also a valid anchor for GET /api/v1/chains/{anchor}, so a row's nouns are
// clickable without a second lookup.
type ChainIssueRef struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier,omitempty"`
	Title      string `json:"title,omitempty"`

	// Created separates the two ways a chain can touch an issue, because they
	// are not the same claim: an issue that exists BECAUSE of this run is the
	// strongest thing a row can say about it, while one the run merely moved
	// was somebody else's before and after.
	//
	// It is read from missions.author_run_id — the column insertIssueTx writes
	// from the crewship verb's author_run_id — and NOT from a journal entry,
	// because nothing emits mission.created: journalTypeForIssueAction maps it,
	// and issueAction "created" has no producer. Deriving the flag from that
	// type would be a field that is false forever.
	Created bool `json:"created,omitempty"`
}

// ChainAgentRef is one agent this chain put to work, and how many pieces of
// work it took. The count is what distinguishes "asked Ada once" from "handed
// Ada the whole thing in six parts" on rows that otherwise look identical.
type ChainAgentRef struct {
	ID          string `json:"id"`
	Slug        string `json:"slug,omitempty"`
	Name        string `json:"name,omitempty"`
	Assignments int    `json:"assignments"`
}

// ChainsListResponse is the page.
type ChainsListResponse struct {
	Chains  []ChainSummary `json:"chains"`
	Count   int            `json:"count"`
	Limit   int            `json:"limit"`
	Offset  int            `json:"offset"`
	HasMore bool           `json:"has_more"`

	// HasUnrecordedRuns reports that this workspace holds runs from before
	// chain_origin existed (migration 20260807160100). Those runs are NOT in
	// the index: the link was never written and cannot be backfilled, so
	// listing them would mean asserting each was its own chain root — an
	// assertion the data does not support, since a composed chain from that
	// era is indistinguishable from three unrelated runs.
	//
	// The flag is here so the absence is stated rather than implied. A client
	// showing an empty or short index can say "older runs predate chain
	// recording" instead of "nothing ever ran".
	HasUnrecordedRuns bool `json:"has_unrecorded_runs"`
}

// List serves GET /api/v1/chains?limit=<n>&offset=<n>
func (h *ChainsListHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	limit, offset := parsePagination(r, DefaultChainsListLimit, MaxChainsListLimit)

	// One row over the page size, so has_more is a fact about the data rather
	// than a guess from a full page.
	rows, err := h.query(r, workspaceID, limit+1, offset)
	if err != nil {
		h.logger.Error("chains index", "error", err, "workspace_id", workspaceID)
		replyError(w, http.StatusInternalServerError, "load chains")
		return
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	// After the trim, never before: the extra row exists only to answer
	// has_more and is not on the page, so fanning out over it would buy two
	// joins nobody reads.
	if err := h.attachTouched(r.Context(), workspaceID, rows); err != nil {
		h.logger.Error("chains index: touched work", "error", err, "workspace_id", workspaceID)
		replyError(w, http.StatusInternalServerError, "load chains")
		return
	}

	unrecorded, err := h.hasUnrecordedRuns(r, workspaceID)
	if err != nil {
		h.logger.Error("chains index: unrecorded runs", "error", err, "workspace_id", workspaceID)
		replyError(w, http.StatusInternalServerError, "load chains")
		return
	}

	writeJSON(w, http.StatusOK, ChainsListResponse{
		Chains:            rows,
		Count:             len(rows),
		Limit:             limit,
		Offset:            offset,
		HasMore:           hasMore,
		HasUnrecordedRuns: unrecorded,
	})
}

// chainsIndexQuery groups the workspace's runs by chain and resolves what
// started each one.
//
// Every predicate carries the workspace, including the five label lookups.
// That is not defence in depth: chain_origin and triggered_by_id are untyped
// string columns, and issue identifiers are only unique PER workspace, so an
// unfenced label join hands another tenant's issue title or rule name to a run
// of ours. The join to `root` is fenced for the same reason — chain_origin is
// a bare id with no foreign key behind it.
//
// The labels are correlated scalar subqueries rather than LEFT JOINs on
// purpose: a subquery cannot multiply the row count, so a duplicate identifier
// (or a schema that stops enforcing per-workspace uniqueness) can never split
// one chain into two index rows.
//
// Rows before the chain_origin column are excluded by the IS NOT NULL
// predicate. See ChainsListResponse.HasUnrecordedRuns for why they are not
// synthesised into single-run chains instead.
const chainsIndexQuery = `
WITH grouped AS (
    SELECT chain_origin                                       AS origin,
           COUNT(*)                                           AS runs,
           MAX(COALESCE(chain_depth, 0))                      AS max_chain_depth,
           SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) AS failed_runs,
           -- 'queued' rides with 'running': from the reader's side a run that
           -- has been accepted but not yet picked up is in flight, and a rail
           -- that showed it as neither running nor finished would be reporting
           -- a gap in its own bookkeeping rather than a state of the world.
           SUM(CASE WHEN status IN ('running','queued') THEN 1 ELSE 0 END) AS running_runs,
           -- 'paused' is not written anywhere in the server today; it is spelled
           -- because the frontend's isAwaitingApproval has always accepted both,
           -- and one predicate meaning two things in two places is how the two
           -- drift apart the day it starts being written.
           SUM(CASE WHEN status IN ('waiting','paused') THEN 1 ELSE 0 END) AS waiting_runs,
           MIN(started_at)                                    AS first_activity,
           MAX(COALESCE(ended_at, started_at))                AS last_activity
    FROM pipeline_runs
    WHERE workspace_id = ?
      AND chain_origin IS NOT NULL
      AND chain_origin <> ''
    GROUP BY chain_origin
)
SELECT g.origin, g.runs, g.max_chain_depth, g.failed_runs, g.running_runs, g.waiting_runs,
       g.first_activity, g.last_activity,
       COALESCE(root.triggered_via, ''),
       COALESCE(root.triggered_by_id, ''),
       COALESCE(root.invoking_user_id, ''),
       COALESCE(root.pipeline_id, ''),
       COALESCE(root.pipeline_slug, ''),
       COALESCE((SELECT a.name       FROM automations a
                  WHERE root.triggered_via = 'automation'
                    AND a.id = root.triggered_by_id AND a.workspace_id = ?), ''),
       COALESCE((SELECT a.event_type FROM automations a
                  WHERE root.triggered_via = 'automation'
                    AND a.id = root.triggered_by_id AND a.workspace_id = ?), ''),
       COALESCE((SELECT m.id         FROM missions m
                  WHERE root.triggered_via = 'issue'
                    AND m.identifier = root.triggered_by_id AND m.workspace_id = ?), ''),
       COALESCE((SELECT m.title      FROM missions m
                  WHERE root.triggered_via = 'issue'
                    AND m.identifier = root.triggered_by_id AND m.workspace_id = ?), ''),
       COALESCE((SELECT s.name       FROM pipeline_schedules s
                  WHERE root.triggered_via = 'schedule'
                    AND s.id = root.triggered_by_id AND s.workspace_id = ?), ''),
       COALESCE((SELECT COALESCE(NULLIF(u.full_name, ''), u.email) FROM users u
                  WHERE u.id = root.invoking_user_id), '')
FROM grouped g
LEFT JOIN pipeline_runs root ON root.id = g.origin AND root.workspace_id = ?
ORDER BY g.last_activity DESC, g.origin DESC
LIMIT ? OFFSET ?`

func (h *ChainsListHandler) query(r *http.Request, workspaceID string, limit, offset int) ([]ChainSummary, error) {
	rows, err := h.db.QueryContext(r.Context(), chainsIndexQuery,
		workspaceID, // grouped
		workspaceID, // automations.name
		workspaceID, // automations.event_type
		workspaceID, // missions.id
		workspaceID, // missions.title
		workspaceID, // pipeline_schedules.name
		workspaceID, // root run
		limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ChainSummary, 0, capacityHint(limit))
	for rows.Next() {
		var (
			c            ChainSummary
			triggeredBy  string
			userID       string
			ruleName     string
			ruleEvent    string
			issueID      string
			issueTitle   string
			scheduleName string
			userName     string
		)
		if err := rows.Scan(
			&c.Origin, &c.Runs, &c.MaxChainDepth, &c.FailedRuns, &c.RunningRuns, &c.WaitingRuns,
			&c.FirstActivity, &c.LastActivity,
			&c.TriggeredVia, &triggeredBy, &userID, &c.RoutineID, &c.RoutineSlug,
			&ruleName, &ruleEvent, &issueID, &issueTitle, &scheduleName, &userName,
		); err != nil {
			return nil, err
		}
		c.Failed = c.FailedRuns > 0
		c.DurationMS = chainElapsedMS(c.FirstActivity, c.LastActivity)
		c.StartedByKind, c.StartedByID, c.StartedByKey, c.StartedBy = resolveChainStart(chainStart{
			via:          c.TriggeredVia,
			triggeredBy:  triggeredBy,
			userID:       userID,
			ruleName:     ruleName,
			ruleEvent:    ruleEvent,
			issueID:      issueID,
			issueTitle:   issueTitle,
			scheduleName: scheduleName,
			userName:     userName,
		})
		out = append(out, c)
	}
	return out, rows.Err()
}

// hasUnrecordedRuns is an EXISTS rather than a COUNT: the client needs to know
// whether the era before chain_origin is represented in this workspace, not how
// many rows it holds, and EXISTS stops at the first match instead of scanning
// the table to produce a number nothing renders.
func (h *ChainsListHandler) hasUnrecordedRuns(r *http.Request, workspaceID string) (bool, error) {
	var found int
	err := h.db.QueryRowContext(r.Context(), `
		SELECT EXISTS(
		    SELECT 1 FROM pipeline_runs
		    WHERE workspace_id = ? AND (chain_origin IS NULL OR chain_origin = '')
		)`, workspaceID).Scan(&found)
	if err != nil {
		return false, err
	}
	return found == 1, nil
}

// chainStart is the root run's raw provenance plus whatever each fenced lookup
// resolved. Every field is either a column of a run in THIS workspace or the
// result of a workspace-fenced lookup, so nothing here can carry a foreign row.
type chainStart struct {
	via          string
	triggeredBy  string
	userID       string
	ruleName     string
	ruleEvent    string
	issueID      string
	issueTitle   string
	scheduleName string
	userName     string
}

// resolveChainStart turns the root run's (triggered_via, triggered_by_id) pair
// into something a human reads.
//
// triggered_by_id is polymorphic — a rule id, an issue IDENTIFIER, a schedule
// id, a webhook id, a parent run id — so it is only ever interpreted against
// the table triggered_via names. That is the same discipline internal/chain
// applies when it walks the pair, and for the same reason: reading it blind is
// how a row grows a fabricated cause.
//
// When a lookup resolves nothing the label falls back to the raw trigger and
// the id stays on the row. A deleted rule therefore still reads "automation"
// with its id, which is the truth — pipeline_runs records that a rule started
// this, and the rule is simply no longer readable. Inventing a name, or
// blanking the row into "unknown", would each lose one half of that.
func resolveChainStart(s chainStart) (kind, id, key, label string) {
	switch s.via {
	case "automation":
		return string(chain.KindAutomation), s.triggeredBy, s.ruleEvent, orRaw(s.ruleName, s.via)
	case "issue":
		// The run's column holds the issue IDENTIFIER, which is the handle a
		// human uses, so it is the key even when the mission row does not
		// resolve.
		return string(chain.KindIssue), s.issueID, s.triggeredBy, orRaw(s.issueTitle, s.via)
	case "schedule":
		return chainStartKindSchdule, s.triggeredBy, "", orRaw(s.scheduleName, s.via)
	case "webhook":
		return chainStartKindWebhook, s.triggeredBy, "", s.via
	case "call_pipeline":
		// A chain rooted at a call_pipeline run means the parent's row is gone
		// (nested runs share the parent's row while it lives), so the pointer
		// is all there is.
		return string(chain.KindRun), s.triggeredBy, "", s.via
	case "manual":
		if s.userName != "" {
			return chainStartKindUser, s.userID, "", s.userName
		}
		return chainStartKindUnknown, "", "", s.via
	case "":
		// No root row: retention swept it, or the chain was rooted at a
		// journal entry rather than a run. The chain is still real — its
		// member runs are right there — so it is listed with an honest
		// "we cannot tell" rather than dropped.
		return chainStartKindUnknown, "", "", ""
	default:
		// A trigger this index has no resolver for (wake_check, and whatever
		// lands next). Naming the raw value beats an empty cell: it is the
		// truth, and it is greppable.
		return chainStartKindUnknown, "", "", s.via
	}
}

func orRaw(resolved, raw string) string {
	if resolved != "" {
		return resolved
	}
	return raw
}

// ---------------------------------------------------------------------------
// How long the chain took.
// ---------------------------------------------------------------------------

// chainElapsedMS is the wall clock between the chain's first and last activity.
//
// Deliberately NOT the sum of the runs' own pipeline_runs.duration_ms, which is
// the obvious implementation and the wrong one. lib/activity-stream's
// chainElapsedMs settled this client-side for two reasons that hold identically
// here:
//
//   - the sum reads 0 for work no agent billed time for — a routine of
//     agentless steps records duration_ms 0 on every run, so a chain that took
//     three minutes would report "instant";
//   - it double-counts a nested run's time inside the run that contains it.
//
// It also cannot see the gaps BETWEEN runs, and on a composed chain those gaps
// are most of the elapsed time: the wait while a rule debounces is part of how
// long the workflow took, from the only perspective that asked.
//
// Null, not zero, when the two timestamps are equal or out of order. One
// datable moment has no span, and 0ms would assert "it was instant" where the
// truth is "it has not finished" — the single-run-still-going case, where
// last_activity falls back to started_at. Unparseable timestamps yield null for
// the same reason rather than a number derived from a zero time.
func chainElapsedMS(first, last string) *int64 {
	f, ferr := time.Parse(time.RFC3339Nano, first)
	l, lerr := time.Parse(time.RFC3339Nano, last)
	if ferr != nil || lerr != nil {
		return nil
	}
	ms := l.Sub(f).Milliseconds()
	if ms <= 0 {
		return nil
	}
	return &ms
}

// ---------------------------------------------------------------------------
// What the chain touched.
// ---------------------------------------------------------------------------

// chainIssueEntryTypes are the journal entry types that record a run CHANGING
// an issue. They are the types issueEvents.log writes with a TraceID, which is
// the pointer back to the causing run.
//
// journal.EntryMissionCreated is deliberately absent. journalTypeForIssueAction
// maps it, but the issueAction that would produce it has no call site anywhere
// in the server, so matching on it would be a predicate that can never fire —
// and would make ChainIssueRef.Created a field that is false forever. Creation
// is read from missions.author_run_id instead, which is a column production
// actually writes.
var chainIssueEntryTypes = []string{
	string(journal.EntryMissionStatus),
	string(journal.EntryMissionAssigned),
	string(journal.EntryMissionComment),
}

// chainAgentsQuery lists the agents each chain put to work, capped per chain.
//
// One query for the WHOLE page rather than one per row: assignments.chain_origin
// carries the same value on every hop of a chain (idx_assignment_chain_origin),
// so the page's rows are one indexed range scan together, and the cap is applied
// with a window function instead of by issuing `limit` separate LIMIT queries.
//
// The fence is a.workspace_id plus ag.workspace_id = a.workspace_id on the join.
// chain_origin has no foreign key behind it and assigned_to_id is only unique
// globally by luck, so a workspace on the outer predicate alone would let a row
// another tenant stamped with OUR origin lend us its agent's name.
//
// COUNT(*) OVER is exact rather than capped: the reader needs to know the list
// was cut, and "5+" is not that. It is bounded by the delegation fan-out cap,
// which is what stops a chain from having unboundedly many assignments at all.
const chainAgentsQuery = `
WITH work AS (
    SELECT a.chain_origin        AS origin,
           ag.id                 AS agent_id,
           COALESCE(ag.name, '') AS agent_name,
           COALESCE(ag.slug, '') AS agent_slug,
           COUNT(*)              AS assignments
    FROM assignments a
    JOIN agents ag
      ON ag.id = a.assigned_to_id
     AND ag.workspace_id = a.workspace_id
    WHERE a.workspace_id = ?
      AND a.chain_origin IN (%s)
    GROUP BY a.chain_origin, ag.id
),
ranked AS (
    SELECT origin, agent_id, agent_name, agent_slug, assignments,
           ROW_NUMBER() OVER (PARTITION BY origin ORDER BY assignments DESC, agent_id ASC) AS rn,
           COUNT(*)     OVER (PARTITION BY origin)                                         AS distinct_agents
    FROM work
)
SELECT origin, agent_id, agent_name, agent_slug, assignments, distinct_agents
FROM ranked
WHERE rn <= ?
ORDER BY origin ASC, rn ASC`

// chainIssuesQuery lists the issues each chain created or changed, capped per
// chain.
//
// Two arms, because there are two records and they say different things:
//
//   - missions.author_run_id (idx_mission_run) — this run AUTHORED the issue.
//     The crewship issue.create verb passes author_run_id and insertIssueTx
//     stores it, so the link is exact.
//   - journal_entries.trace_id (idx_journal_ws_trace_run) — this run CHANGED
//     the issue. issueEvents.log stamps the causing run as the entry's trace,
//     which is the same pointer internal/chain's expandRun follows to find who
//     executed a run.
//
// It matches trace_id ONLY, where the walk matches (trace_id OR run_id). That
// is not a divergence in scoping but the absence of a second arm to scope: run_id
// is a generated column over payload.$.run_id, and issueEventPayload writes
// action/details/from/to and nothing else, so the run_id arm cannot match a
// mission entry. An OR that can never be true is a guard that never runs.
//
// Every join carries the workspace. mission_id, trace_id and author_run_id are
// untyped string columns whose foreign keys (where they exist at all) constrain
// the row but not the tenant, and a chain's origin is a bare id — so an unfenced
// arm reads another tenant's issue titles into our rows, the same hole the
// started_by lookups were fenced for. Three of the four are individually
// load-bearing and TestChainsList_ForeignWorkspaceWorkNeverAttachesToARow kills
// each one on its own.
//
// The fourth — m.workspace_id on the FINAL label join — is a backstop and is
// stated as one rather than passed off as a fence: once the three upstream arms
// hold, `agg` can only contain in-workspace issue ids, so nothing reaches it to
// exclude. It stays because that join is where the text on the wire comes from,
// and a title lookup with no workspace on it is the shape of the bug even when
// this particular query cannot reach it.
//
// KNOWN GAP, stated rather than papered over: issue writes an AGENT makes from
// inside its assignment do not appear. Those journal entries carry the
// assignment's own journal run id as their trace, and that id is not recorded on
// the assignments row, so there is no column joining them back to the chain. The
// issues a chain's ROUTINE runs touched are complete; the ones its agents
// touched are not reachable from here.
const chainIssuesQuery = `
WITH chain_runs AS (
    SELECT id, chain_origin
    FROM pipeline_runs
    WHERE workspace_id = ?
      AND chain_origin IN (%s)
),
touched AS (
    SELECT cr.chain_origin AS origin, m.id AS issue_id, 1 AS created
    FROM chain_runs cr
    JOIN missions m
      ON m.author_run_id = cr.id
     AND m.workspace_id = ?
    UNION ALL
    SELECT cr.chain_origin, m.id, 0
    FROM chain_runs cr
    JOIN journal_entries j
      ON j.trace_id = cr.id
     AND j.workspace_id = ?
     AND j.entry_type IN (%s)
    JOIN missions m
      ON m.id = j.mission_id
     AND m.workspace_id = j.workspace_id
),
agg AS (
    SELECT origin, issue_id, MAX(created) AS created, COUNT(*) AS touches
    FROM touched
    GROUP BY origin, issue_id
),
ranked AS (
    SELECT origin, issue_id, created, touches,
           ROW_NUMBER() OVER (PARTITION BY origin ORDER BY created DESC, touches DESC, issue_id ASC) AS rn,
           COUNT(*)     OVER (PARTITION BY origin)                                                   AS distinct_issues
    FROM agg
)
SELECT rk.origin, rk.issue_id, COALESCE(m.identifier, ''), COALESCE(m.title, ''),
       rk.created, rk.distinct_issues
FROM ranked rk
JOIN missions m
  ON m.id = rk.issue_id
 AND m.workspace_id = ?
WHERE rk.rn <= ?
ORDER BY rk.origin ASC, rk.rn ASC`

// attachTouched fills Issues/Agents and their totals for one page of rows.
//
// Two queries for the page, not two per row. A row with nothing to show keeps
// its nil slices and zero counts, which is the honest answer for a routine that
// touched no issue and dispatched nobody.
func (h *ChainsListHandler) attachTouched(ctx context.Context, workspaceID string, rows []ChainSummary) error {
	if len(rows) == 0 {
		return nil
	}
	origins := make([]any, 0, len(rows))
	at := make(map[string]*ChainSummary, len(rows))
	for i := range rows {
		origins = append(origins, rows[i].Origin)
		at[rows[i].Origin] = &rows[i]
	}
	if err := h.attachAgents(ctx, workspaceID, origins, at); err != nil {
		return err
	}
	return h.attachIssues(ctx, workspaceID, origins, at)
}

func (h *ChainsListHandler) attachAgents(ctx context.Context, workspaceID string, origins []any, at map[string]*ChainSummary) error {
	args := append([]any{workspaceID}, origins...)
	args = append(args, MaxChainSummaryRefs)
	rows, err := h.db.QueryContext(ctx, fmt.Sprintf(chainAgentsQuery, sqlPlaceholders(len(origins))), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			origin string
			ref    ChainAgentRef
			total  int
		)
		if err := rows.Scan(&origin, &ref.ID, &ref.Name, &ref.Slug, &ref.Assignments, &total); err != nil {
			return err
		}
		// A row the page does not hold cannot arrive — the IN list is built
		// from the page — but the map lookup is what makes that structural
		// rather than assumed.
		if c, ok := at[origin]; ok {
			c.Agents = append(c.Agents, ref)
			c.AgentCount = total
		}
	}
	return rows.Err()
}

func (h *ChainsListHandler) attachIssues(ctx context.Context, workspaceID string, origins []any, at map[string]*ChainSummary) error {
	args := append([]any{workspaceID}, origins...)
	args = append(args, workspaceID, workspaceID)
	for _, t := range chainIssueEntryTypes {
		args = append(args, t)
	}
	args = append(args, workspaceID, MaxChainSummaryRefs)
	query := fmt.Sprintf(chainIssuesQuery, sqlPlaceholders(len(origins)), sqlPlaceholders(len(chainIssueEntryTypes)))
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			origin  string
			ref     ChainIssueRef
			created int
			total   int
		)
		if err := rows.Scan(&origin, &ref.ID, &ref.Identifier, &ref.Title, &created, &total); err != nil {
			return err
		}
		ref.Created = created == 1
		if c, ok := at[origin]; ok {
			c.Issues = append(c.Issues, ref)
			c.IssueCount = total
		}
	}
	return rows.Err()
}
