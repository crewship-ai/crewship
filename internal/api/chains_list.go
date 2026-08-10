package api

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/chain"
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

	// FirstActivity/LastActivity bound the chain: the earliest start and the
	// latest end (falling back to start for a run still going). Rows are
	// ordered by LastActivity descending.
	FirstActivity string `json:"first_activity"`
	LastActivity  string `json:"last_activity"`
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
           MIN(started_at)                                    AS first_activity,
           MAX(COALESCE(ended_at, started_at))                AS last_activity
    FROM pipeline_runs
    WHERE workspace_id = ?
      AND chain_origin IS NOT NULL
      AND chain_origin <> ''
    GROUP BY chain_origin
)
SELECT g.origin, g.runs, g.max_chain_depth, g.failed_runs, g.first_activity, g.last_activity,
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
			&c.Origin, &c.Runs, &c.MaxChainDepth, &c.FailedRuns, &c.FirstActivity, &c.LastActivity,
			&c.TriggeredVia, &triggeredBy, &userID, &c.RoutineID, &c.RoutineSlug,
			&ruleName, &ruleEvent, &issueID, &issueTitle, &scheduleName, &userName,
		); err != nil {
			return nil, err
		}
		c.Failed = c.FailedRuns > 0
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
