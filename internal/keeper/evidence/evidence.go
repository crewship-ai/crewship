// Package evidence computes verified, database-sourced facts about one
// credential request, and renders them as a prompt block for the Keeper
// judge.
//
// Why it exists: the judge's prompt is prose. It carries the credential
// tier, the agent's name and the recent conversation, and then asks the
// model to decide whether the request is "corroborated" — a judgement it
// has no material for, because corroboration lives in tables the prompt
// never reads. On a 9B model that gap is the whole failure: measured
// 2026-08-01, the same request that a prose-only prompt ALLOWed 3/3 was
// DENYed 3/3 once six computed facts were prepended, and the risk score
// rose from 4 (below the notify threshold, so no human ever sees it) to
// 8–9. The facts are not new information; they were already in the
// database, just not on the decision path.
//
// The package is deliberately self-contained: it depends on nothing in
// internal/keeper/gatekeeper, so gathering can be built, tested and
// changed without touching the prompt builder that eventually calls it.
// Its only inputs are two ids and a clock.
//
// # Omission over guessing
//
// Every fact here is optional, and Gather never returns an error. A fact
// whose query failed is left nil and never rendered. That asymmetry is
// the point: a missing line costs the judge some context, but a wrong
// line is repeated back as justification and acted on. "0 denials in the
// last 7 days" produced by a failed query is not a neutral degradation —
// it is an argument for granting access, manufactured by an outage.
// Facts.Omitted carries what was dropped and why, for the caller's log.
//
// # Placement in the prompt
//
// Render's output belongs ABOVE the untrusted conversation fence, next to
// the watch policy and the tier block, for the same reason those sit
// there: the block's claim to authority is only credible if agent-authored
// text cannot precede, restate or contradict it. The one untrusted string
// that reaches the block — a task or issue title — is quoted and length
// capped on the way in.
package evidence

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Querier is the read-only slice of *sql.DB this package needs. Narrowing it
// keeps Gather callable inside an existing transaction and, more usefully,
// makes the "every query fails" path something a test can construct in three
// lines — that path is the one carrying the safety property.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Fact keys. They are the labels the judge reads, so they double as the
// identifiers in Facts.Omitted: an operator reading a "fact omitted" log line
// and an operator reading the prompt see the same name.
const (
	FactBinding          = "credential_bound_to_agent"
	FactPairHistory      = "prior_requests_same_pair"
	FactRecentDenies     = "agent_denies_last_7d"
	FactOpenAssignedWork = "open_assigned_work"
)

// FactKeys returns every fact key this collector can produce, in prompt order.
//
// It exists so the vocabulary has exactly one owner. The judge profile validates
// an operator's --evidence-facts selection against these names, and a second
// hand-maintained copy of the list drifts: the first version of this pair
// disagreed on prior_requests_same_pair and offered two names nothing computed.
// Both failure modes are silent, and both end with a fact missing from a
// security decision — which is a judge reasoning about a credential without
// knowing whether the agent is even bound to it.
//
// Adding a fact means adding it here, and the profile picks it up for free.
func FactKeys() []string {
	return []string{
		FactBinding,
		FactPairHistory,
		FactRecentDenies,
		FactOpenAssignedWork,
	}
}

// denyWindowDays is the lookback for the standing-denials count. Seven days is
// short enough that a fixed pattern of refusals is still current behaviour
// rather than history, and long enough to survive a weekend.
const denyWindowDays = 7

// recentRequestWindow bounds "requested recently". Past it, a prior request is
// context rather than a repeat attempt, and the judge is told so explicitly
// instead of being left to infer it from a date.
const recentRequestWindow = 48 * time.Hour

// maxRenderedWorkItems caps how many open work items reach the prompt. The
// count is always exact; only the enumeration is clipped, because an agent
// with a thirty-item backlog would otherwise crowd the other five facts out
// of a 4096-token context and defeat the purpose of the block.
const maxRenderedWorkItems = 3

// maxTitleRunes truncates agent-authored work titles. Long enough to judge
// relevance ("profile slow order queries" against a PROD_DB_ADMIN request),
// short enough that one title cannot own the budget.
const maxTitleRunes = 60

// Query identifies the request to gather facts for.
type Query struct {
	// AgentID is keeper_requests.requesting_agent_id / agent_credentials.agent_id.
	AgentID string
	// CredentialID is the credential being requested.
	CredentialID string
	// Now anchors every relative window (the 7-day denial lookback, "4h ago").
	// It is a field rather than a time.Now() call so the windows are testable
	// and so a replay can reproduce the exact block a decision was made on.
	// Zero means time.Now().UTC().
	Now time.Time
}

// Binding is the agent↔credential grant from agent_credentials. Bound=false is
// a verified answer, not an absence: the row genuinely does not exist.
type Binding struct {
	Bound      bool
	EnvVarName string
	Priority   int
	// BoundAt is the grant's created_at, normalised to RFC3339. agent_credentials
	// records no grantor, so "by whom" is not available from this table.
	BoundAt string
}

// PairHistory is what this agent has previously asked for this exact
// credential: how often, how it went, and how long ago. It answers three of
// the PRD's facts at once because they read the same index and the same rows —
// splitting them would triple the work and let the three disagree.
type PairHistory struct {
	Total   int
	Allowed int
	Denied  int
	// FirstEncounter is true when there is no settled prior request at all —
	// the credential_first_seen_for_agent fact in its most decision-relevant
	// form. When true, every other field is zero.
	FirstEncounter bool
	FirstAt        string // RFC3339
	LastAt         string // RFC3339
	LastDecision   string // ALLOW | DENY | ESCALATE, upper-cased
	HoursSinceLast int
}

// RecentDenies counts refusals this agent collected across all credentials in
// the lookback window. It is the "is this agent probing" signal, and it is
// deliberately not scoped to the requested credential.
type RecentDenies struct {
	Count int
	Days  int
}

// WorkItem is one open piece of work assigned to the agent. Title is
// agent-authored and untrusted; Render quotes and truncates it.
type WorkItem struct {
	Title  string
	Status string
}

// OpenWork is the agent's currently-assigned, non-terminal work — the
// strongest available answer to "does this agent have a reason to be here".
// An empty Items with Total 0 is a verified "none".
type OpenWork struct {
	Total int
	Items []WorkItem
}

// Omission records a fact that could not be computed, so the caller can log
// the degradation. It never reaches the prompt.
type Omission struct {
	Fact string
	Err  error
}

// Facts is the gathered evidence. Every field is a pointer because nil is
// load-bearing: it means "not established", and Render skips it. There is no
// zero value that means "no" — a false or a 0 always came from a query that
// answered.
type Facts struct {
	Binding      *Binding
	PairHistory  *PairHistory
	RecentDenies *RecentDenies
	OpenWork     *OpenWork

	// Omitted lists the facts that failed, in a fixed order. Not rendered.
	Omitted []Omission
}

// sqlInstant normalises keeper_requests.created_at (and friends) to RFC3339
// UTC inside SQLite, yielding NULL for anything unparseable.
//
// This is not decoration. The column holds two formats: Go writers store
// RFC3339 ("2026-08-01T10:00:00Z"), while the column DEFAULT stores SQLite's
// legacy "2026-08-01 10:00:00" — and migrate_backfill_timestamps.go documents
// that its cleanup is one-shot and does not stop new legacy rows arriving. A
// plain text compare orders every legacy row before every RFC3339 row (' '
// 0x20 sorts under 'T' 0x54), so `created_at >= cutoff` would drop legacy rows
// out of the window. For a denial count that error runs one way only: it makes
// a repeat offender look clean.
//
// strftime parses both layouts (and an offset suffix) and re-emits a single
// comparable form, so ordering, windowing and the returned strings all agree.
// It costs the created_at index on these queries; the agent/credential
// predicate is the selective one and keeps its own index either way.
const sqlInstant = `strftime('%Y-%m-%dT%H:%M:%SZ', created_at)`

// settledDecisions is the closed set of decisions that represent a finished
// judgement. PENDING rows and NULL decisions are in-flight — counting them as
// history would let the request currently being judged corroborate itself.
const settledDecisions = `UPPER(decision) IN ('ALLOW','DENY','ESCALATE')`

// Gather computes every fact it can and returns them. It never returns an
// error: a failed query yields a nil fact plus an entry in Facts.Omitted, so
// there is no way for a caller to accidentally turn a database hiccup into
// either a fabricated fact or a dropped block.
//
// Six indexed queries (the pair history and the open-work fact cost two
// each). Pass a ctx with a deadline — this runs on the synchronous path to the
// judge, and a slow gather spends the judge-timeout budget the model call
// needs.
func Gather(ctx context.Context, db Querier, q Query) Facts {
	var f Facts

	if q.AgentID == "" || q.CredentialID == "" {
		// Not a database failure, but the same rule applies: with no ids there
		// is nothing to verify, and every fact would be a guess.
		err := fmt.Errorf("evidence: agent id and credential id are both required (got agent=%q credential=%q)", q.AgentID, q.CredentialID)
		for _, name := range []string{FactBinding, FactPairHistory, FactRecentDenies, FactOpenAssignedWork} {
			f.Omitted = append(f.Omitted, Omission{Fact: name, Err: err})
		}
		return f
	}

	now := q.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	if b, err := queryBinding(ctx, db, q); err != nil {
		f.Omitted = append(f.Omitted, Omission{Fact: FactBinding, Err: err})
	} else {
		f.Binding = b
	}

	if h, err := queryPairHistory(ctx, db, q, now); err != nil {
		f.Omitted = append(f.Omitted, Omission{Fact: FactPairHistory, Err: err})
	} else {
		f.PairHistory = h
	}

	if d, err := queryRecentDenies(ctx, db, q, now); err != nil {
		f.Omitted = append(f.Omitted, Omission{Fact: FactRecentDenies, Err: err})
	} else {
		f.RecentDenies = d
	}

	if w, err := queryOpenWork(ctx, db, q); err != nil {
		f.Omitted = append(f.Omitted, Omission{Fact: FactOpenAssignedWork, Err: err})
	} else {
		f.OpenWork = w
	}

	return f
}

// queryBinding reads the agent_credentials grant. The UNIQUE(agent_id,
// credential_id) constraint makes this a single index probe, and makes "no
// row" unambiguous.
func queryBinding(ctx context.Context, db Querier, q Query) (*Binding, error) {
	b := &Binding{}
	var env string
	var prio int
	var at sql.NullString

	found, err := queryOneRow(ctx, db, `
		SELECT env_var_name, priority, `+sqlInstant+`
		FROM agent_credentials
		WHERE agent_id = ? AND credential_id = ?`,
		[]any{q.AgentID, q.CredentialID}, &env, &prio, &at)
	if err != nil {
		return nil, fmt.Errorf("evidence: credential binding: %w", err)
	}
	if found {
		b.Bound = true
		b.EnvVarName = env
		b.Priority = prio
		b.BoundAt = at.String
	}
	return b, nil
}

// queryOneRow runs a statement expected to yield at most one row, scans it and
// releases the connection before returning.
//
// It exists because Querier deliberately exposes only QueryContext (so a test
// can fail every query with a three-line stub), which means there is no
// QueryRow to lean on — and because a *sql.Rows left open across a second
// query holds its pool connection for the whole round trip. That is wasteful
// against a real database and outright wrong against SQLite's `:memory:`,
// where the second connection is a second, empty database.
func queryOneRow(ctx context.Context, db Querier, query string, args []any, dest ...any) (bool, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	if !rows.Next() {
		return false, rows.Err()
	}
	if err := rows.Scan(dest...); err != nil {
		return false, err
	}
	return true, rows.Err()
}

// queryPairHistory reads this agent's settled history for this credential.
//
// Two statements rather than one: the aggregate cannot also name the decision
// on the newest row (SQLite's bare-column-with-MAX shortcut is undefined once
// a query carries both MIN and MAX). They share a failure unit deliberately —
// counts without the recency, or recency without the counts, is a half-fact,
// and a half-fact is the thing this package exists to avoid.
func queryPairHistory(ctx context.Context, db Querier, q Query, now time.Time) (*PairHistory, error) {
	h := &PairHistory{}
	var unparsed int
	var firstAt sql.NullString

	if _, err := queryOneRow(ctx, db, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN UPPER(decision) = 'ALLOW' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN UPPER(decision) = 'DENY'  THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN `+sqlInstant+` IS NULL THEN 1 ELSE 0 END), 0),
		       MIN(`+sqlInstant+`)
		FROM keeper_requests
		WHERE requesting_agent_id = ? AND credential_id = ? AND `+settledDecisions,
		[]any{q.AgentID, q.CredentialID},
		&h.Total, &h.Allowed, &h.Denied, &unparsed, &firstAt); err != nil {
		return nil, fmt.Errorf("evidence: pair history: %w", err)
	}
	if unparsed > 0 {
		// A row we cannot place in time silently vanishes from MIN/MAX and from
		// the ordering below, which would understate both the history and how
		// recent it is — the reassuring direction. Report nothing instead.
		return nil, fmt.Errorf("evidence: pair history: %d row(s) have an unparseable created_at", unparsed)
	}
	if h.Total == 0 {
		h.FirstEncounter = true
		return h, nil
	}
	h.FirstAt = firstAt.String

	var lastAt sql.NullString
	found, err := queryOneRow(ctx, db, `
		SELECT UPPER(decision), `+sqlInstant+`
		FROM keeper_requests
		WHERE requesting_agent_id = ? AND credential_id = ? AND `+settledDecisions+`
		ORDER BY `+sqlInstant+` DESC
		LIMIT 1`,
		[]any{q.AgentID, q.CredentialID}, &h.LastDecision, &lastAt)
	if err != nil {
		return nil, fmt.Errorf("evidence: pair history (latest): %w", err)
	}
	if !found {
		// The aggregate counted rows a moment ago, so an empty second read means
		// the two statements saw different snapshots. Rendering the counts with
		// no recency would understate a repeat attempt; drop the fact.
		return nil, fmt.Errorf("evidence: pair history: %d prior request(s) counted but none readable", h.Total)
	}
	h.LastAt = lastAt.String
	ts, perr := time.Parse(time.RFC3339, h.LastAt)
	if perr != nil {
		return nil, fmt.Errorf("evidence: pair history: latest request timestamp %q: %w", h.LastAt, perr)
	}
	h.HoursSinceLast = int(now.Sub(ts).Hours())
	return h, nil
}

// queryRecentDenies counts this agent's refusals across all credentials inside
// the lookback window, using the idx_keeper_req_agent index.
func queryRecentDenies(ctx context.Context, db Querier, q Query, now time.Time) (*RecentDenies, error) {
	cutoff := now.AddDate(0, 0, -denyWindowDays).Format(time.RFC3339)

	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN `+sqlInstant+` >= ? THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN `+sqlInstant+` IS NULL THEN 1 ELSE 0 END), 0)
		FROM keeper_requests
		WHERE requesting_agent_id = ? AND UPPER(decision) = 'DENY'`,
		cutoff, q.AgentID)
	if err != nil {
		return nil, fmt.Errorf("evidence: recent denies: %w", err)
	}
	defer rows.Close()

	d := &RecentDenies{Days: denyWindowDays}
	var unparsed int
	if rows.Next() {
		if err := rows.Scan(&d.Count, &unparsed); err != nil {
			return nil, fmt.Errorf("evidence: recent denies scan: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("evidence: recent denies: %w", err)
	}
	if unparsed > 0 {
		// An undatable denial cannot be placed inside or outside the window, so
		// the count can only be a floor. A floor rendered as a count reads as
		// "this agent has been refused less than it has".
		return nil, fmt.Errorf("evidence: recent denies: %d row(s) have an unparseable created_at", unparsed)
	}
	return d, nil
}

// openTaskStatuses are the non-terminal mission_tasks states, taken from the
// cancel sweep in internal/api/issue_handler_workflow.go which cancels exactly
// these — so "open" here means the same thing it means to the issue tracker.
//
// AWAITING_APPROVAL is added on top of that sweep's list, and deliberately.
// internal/statuses/transitions.go excludes it from the transition map because
// it moves via /approve rather than a normal transition — an exclusion about
// STATE MACHINE plumbing, not about whether the work is live. Reading the sweep
// alone gave an agent whose only task awaited approval a rendered
// "open_assigned_work: none", under a header telling the judge those facts
// outrank the conversation. That is a fabricated negative in a security
// decision: work that exists, reported as absent, arguing for refusal.
var openTaskStatuses = []string{"PENDING", "IN_PROGRESS", "BLOCKED", "AWAITING_APPROVAL"}

// terminalIssueStatuses are the missions states that close an issue, matching
// the completion branch in issue_handler_update.go. Issues are enumerated by
// exclusion because the tracker's open set (BACKLOG, TODO, IN_PROGRESS,
// IN_REVIEW, …) grows with workflow features while the closed set does not.
var terminalIssueStatuses = []string{"DONE", "CANCELLED", "DUPLICATE"}

// queryOpenWork assembles the agent's open assigned work from the two places
// Crewship records an assignment: mission_tasks.assigned_agent_id, and the
// issue-tracker columns on missions.
//
// There is no `issues` table — issues are rows on `missions` with
// mission_type='issue' (migrate_consts_v33_v41.go:90). Both halves must
// succeed: "no open assigned work" derived from one source while the other
// errored is an argument for refusal that the evidence does not support, which
// is exactly the class of confident-and-wrong this package must not emit.
func queryOpenWork(ctx context.Context, db Querier, q Query) (*OpenWork, error) {
	w := &OpenWork{}

	// No LIMIT: this is one agent's non-terminal work, bounded by how much can
	// be assigned to a single agent at once, and Total must be exact.
	taskArgs := append([]any{q.AgentID}, toAny(openTaskStatuses)...)
	rows, err := db.QueryContext(ctx, `
		SELECT title, status
		FROM mission_tasks
		WHERE assigned_agent_id = ? AND status IN (`+placeholders(len(openTaskStatuses))+`)`,
		taskArgs...)
	if err != nil {
		return nil, fmt.Errorf("evidence: open assigned tasks: %w", err)
	}
	if err := scanWorkItems(rows, w); err != nil {
		return nil, fmt.Errorf("evidence: open assigned tasks: %w", err)
	}

	// assignee_id is polymorphic — it holds a user id when assignee_type is
	// 'user' — so the type filter is what keeps a human's issue from being read
	// as the agent's own justification.
	issueArgs := append([]any{q.AgentID}, toAny(terminalIssueStatuses)...)
	irows, err := db.QueryContext(ctx, `
		SELECT title, status
		FROM missions
		WHERE assignee_id = ? AND assignee_type = 'agent' AND mission_type = 'issue'
		  AND UPPER(status) NOT IN (`+placeholders(len(terminalIssueStatuses))+`)`,
		issueArgs...)
	if err != nil {
		return nil, fmt.Errorf("evidence: open assigned issues: %w", err)
	}
	if err := scanWorkItems(irows, w); err != nil {
		return nil, fmt.Errorf("evidence: open assigned issues: %w", err)
	}

	w.Total = len(w.Items)
	return w, nil
}

func scanWorkItems(rows *sql.Rows, w *OpenWork) error {
	defer rows.Close()
	for rows.Next() {
		var it WorkItem
		if err := rows.Scan(&it.Title, &it.Status); err != nil {
			return err
		}
		w.Items = append(w.Items, it)
	}
	return rows.Err()
}

func placeholders(n int) string {
	if n == 0 {
		return "NULL"
	}
	s := "?"
	for range n - 1 {
		s += ",?"
	}
	return s
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
