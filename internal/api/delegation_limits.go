package api

// Delegation caps — how deep a delegation chain may go, and how wide one run
// may fan out (#1754).
//
// The thing being bounded is a tree, and a tree with no bound on either axis
// is a fork bomb billed by the token: every hop spends a container start, a
// full system prompt and a model turn, and the worst case is the product of
// the two limits, not their sum.
//
// The load-bearing property is WHERE the numbers come from. Both the position
// of a call in the tree (depth, parent) and the limits it is measured against
// are read from the server's own tables. Nothing on this path is taken from
// the agent:
//
//   - depth comes from `assignments.depth` of the row the CALLER is executing,
//     found by the acting agent id the sidecar resolves from a per-agent
//     bearer token (internal/sidecar/identity.go). A `depth` field in the
//     request body is not read by any code — createAssignmentBody has no such
//     field, so it decodes to nothing.
//   - the limits come from app_settings, read fresh per decision like the
//     admission limits next door (runtime_capacity_policy.go), so
//     `crewship instance settings set delegation.max_depth 3` takes effect on
//     the next dispatch rather than the next restart.
//
// The counter-example lives one directory over: /query caps peer-question
// depth at 2, but reads that depth from the request body (or an env var inside
// the agent's own container), and never propagates it to the peer it runs. An
// agent bypasses it by sending {"depth":0} — which is to say it is not a
// control, and this file exists to not be that.
//
// WHICH DOORS THIS COVERS. Two, and they share one insert
// (insertCappedAssignment, below) so they cannot drift:
//
//   - AssignmentHandler.Create, the endpoint behind the sidecar's /assign;
//   - AssignmentHandler.DispatchMention, the @mention trigger on an issue
//     comment (#1768 item 3). A mention is a dispatch — the mentioned agent
//     runs — so it is bounded by the same two numbers rather than by a
//     mention-specific cap. An agent that can be mentioned can mention back,
//     and a chain of comments is a delegation tree wearing different clothes.
//
// The other ways an agent can cause work to run — /mission/create (the mission
// engine dispatches its task list through DispatchAssignment, which does not
// pass through here) and /spawn (an ephemeral hire, gated instead by the
// crew's autonomy_level) — are NOT capped by this. Stated rather than implied,
// because a cap that is believed to cover more than it does is worse than a
// missing one.
//
// /mission/create has since grown its own control on its own door, which is
// what that paragraph was asking for: mission_limits.go bounds one plan's task
// list and a crew's live agent-created missions. It is deliberately NOT this
// cap — a mission's tasks are one authored plan, not delegation hops, and
// routing DispatchAssignment through insertCappedAssignment would have counted
// them as hops. The two files bound the same resource through different doors;
// neither covers the other's.
//
// THE HUMAN CALLER. A mention written by a PERSON has no assignment row of its
// own, so it is a root: depth 1, no parent. That is not a hole — a human
// comment is not a delegation hop, and reading a depth off whatever the
// mentioned agent happened to be running would refuse mentions of busy agents
// with a message about delegation. The fan-out cap still applies, counted
// against the agent the row is filed under (see dispatchCaller.FanoutSubjectID
// below), so "mention the same agent on the same issue forever" is bounded by
// the same number an agent's /assign fan-out is.
//
// What that cost, and what it costs now. A human's mention is filed under the
// TARGET (a person has no agents.id and assigned_by_id is NOT NULL with a
// foreign key), so the naive root count charged the mention against every
// in-flight row that agent owns in the issue's chat — including the ones the
// MISSION ENGINE writes on its behalf, which carry the same assigned_by_id,
// the same chat_id and a NULL parent. A lead running eight tasks on an issue
// was therefore unmentionable by a human, and the refusal was swallowed into a
// `refused` row nobody reads.
//
// The first attempt at fixing that narrowed the bucket by
// dispatchCaller.selfFiled — count only rows addressed BACK to the target
// (assigned_by = assigned_to). It did not work, because that is exactly the
// shape the mission engine writes for a lead's own planning turn and for every
// task a lead assigns to itself. The two kinds of work were still in one
// bucket; the WHERE had just moved.
//
// THE DISCRIMINATOR IS `depth`. Every row insertCappedAssignment writes
// carries the depth enforceDelegationCaps derived, which is 1 at the shallowest
// (see resolveDelegationScope). The mission engine writes 0 — explicitly, and
// the migration that added the column says why: "0 is deliberately NOT a valid
// depth for a new row … so a legacy row can never be mistaken for one this
// code wrote." So `depth > 0` means "a row one of THESE doors admitted", which
// is precisely the population this cap is entitled to count. It keeps the
// property that matters: the number is still derived from server state, from a
// column no request can write, on the same doors as before.
//
// Both ROOT buckets carry it, not just the self-filed one. A mission task is
// one authored plan, not a delegation hop — this file has always said so, and
// mission_limits.go bounds that plan on its own door — so a lead's mission
// rows must not consume its /assign budget either. The children bucket needs
// no such filter: a row with a parent came from a capped door by construction.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

const (
	// SettingDelegationMaxDepth bounds delegation hops from the originating
	// run. A lead's own dispatch is depth 1; what that sub-agent dispatches is
	// depth 2. 0 disables delegation entirely — an operator needs one number
	// that means "no agent dispatches anything".
	SettingDelegationMaxDepth = "delegation.max_depth"

	// SettingDelegationMaxFanout bounds how many sub-agents ONE run may
	// dispatch. For a delegated run that is the children of its parent
	// assignment; for a lead working a chat it is its in-flight dispatches in
	// that chat (see resolveDelegationScope). 0 disables delegation entirely.
	SettingDelegationMaxFanout = "delegation.max_fanout"
)

const (
	// Depth 2 permits exactly one hop beyond what shipped before this change
	// (lead → worker), which is the capability being unlocked — a worker may
	// delegate, and what IT dispatches may not delegate again. Deeper trees
	// are a policy an operator can opt into; they are not a default, because
	// each extra level multiplies the worst case by the fan-out.
	defaultDelegationMaxDepth = 2

	// 8 mirrors the orchestrator's default MaxConcurrentRuns and sits above
	// typical crew size, so the existing behaviour — a lead handing work to
	// every member of its crew in one turn — is not regressed by a cap that
	// was introduced for a different reason. With the depth default that
	// bounds one originating run at 8 + 64 dispatches; actual concurrency is
	// far lower, since computeCrewBudget queues everything past the crew's
	// slot budget (typically 1-2).
	defaultDelegationMaxFanout = 8

	// Sanity ceilings. Past these the value is a typo rather than a policy and
	// is answered with the default instead of being clamped — same convention
	// as agentMinMemoryMB and AdmissionLimits: a clamped nonsense value is a
	// gate running on a number nobody chose.
	maxDelegationDepthCeiling  = 16
	maxDelegationFanoutCeiling = 256
)

// delegationLimits is the live cap policy.
type delegationLimits struct {
	MaxDepth  int
	MaxFanout int
}

// DelegationLimits resolves the live delegation policy. A nil or unreadable DB
// yields the compiled defaults — the same fail-to-default contract the
// admission limits use, and the reason settingInt answers `def` for anything
// missing, unparseable or out of range.
func DelegationLimits(ctx context.Context, db *sql.DB) delegationLimits {
	return delegationLimits{
		MaxDepth:  settingInt(ctx, db, SettingDelegationMaxDepth, defaultDelegationMaxDepth, 0, maxDelegationDepthCeiling),
		MaxFanout: settingInt(ctx, db, SettingDelegationMaxFanout, defaultDelegationMaxFanout, 0, maxDelegationFanoutCeiling),
	}
}

// dispatchCaller is who is dispatching, in the two senses the caps need.
//
// They are the same agent for /assign and differ only for a human-authored
// mention, which is why they are named apart rather than passed as one id that
// silently means two things:
//
//   - ActorAgentID is whose position in the tree this dispatch inherits. It is
//     the agent the SERVER resolved (a per-agent bearer token for /assign, the
//     comment's author_id for a mention), never a field in a request body.
//     Empty means "not an agent" — a human — which resolveDelegationScope
//     already answers as a root.
//   - FanoutSubjectID is the agents.id the row is stored under
//     (assignments.assigned_by_id) and therefore the id a ROOT dispatch's
//     fan-out is counted against. It must never be empty: the column is NOT
//     NULL with a foreign key to agents, and an empty subject would make
//     countDelegationSiblings count zero forever, i.e. no fan-out cap at all.
type dispatchCaller struct {
	ActorAgentID    string
	FanoutSubjectID string
}

// agentCaller is the /assign shape, where one agent is both the position in
// the tree and the row's owner.
func agentCaller(agentID string) dispatchCaller {
	return dispatchCaller{ActorAgentID: agentID, FanoutSubjectID: agentID}
}

// selfFiled reports the human shape: no acting agent, so the row is filed under
// the agent it is addressed TO. It is the only construction with an empty
// ActorAgentID, and it is what separates the two kinds of root row that share
// one chat — "work a person asked this agent for" (assigned_by = assigned_to)
// from "work this agent handed to somebody else" (assigned_by = it,
// assigned_to = another). Counting them in one bucket made a busy lead
// unmentionable; see the file header.
func (c dispatchCaller) selfFiled() bool { return c.ActorAgentID == "" }

// delegationScope is one /assign call's server-derived position in the tree.
//
// ParentID is the assignment the caller was executing, empty when the caller is
// not itself running a delegated task (a lead in a chat, a mission lead's
// planning turn before its own row exists). Depth is what the NEW assignment
// would carry: parent depth + 1, or 1 at the root.
//
// ChainOrigin is which CHAIN the new row belongs to — the trace id
// pipeline_runs already carries under that exact name. It sits here, with the
// other two, because it is the same kind of value: derived by the server from
// rows the caller cannot write. A chain_origin an agent could supply would be
// laundering of a different sort than depth's — not "I am shallower than I am"
// but "my work belongs to somebody else's story".
type delegationScope struct {
	ParentID string
	Depth    int
	// ChainOrigin is WHICH trace this work belongs to — the same value on
	// every hop of a workflow, which is what collapses one into a single row.
	ChainOrigin string
	// ParentRunID is WHAT DISPATCHED it, when a routine did. Distinct from the
	// origin on purpose: the origin is shared by the whole trace, so a tree
	// built from it alone is a star. Exactly one parent is ever set — a
	// delegation carries ParentID, a routine dispatch carries this — because a
	// row with two parents can be reached by two paths and drawn twice.
	ParentRunID string
}

// rootedAt names a chain for a scope that could not derive one from the tree.
//
// It is deliberately a FILL, not a set: a scope with a parent already answered
// the question, and letting a second source overwrite that answer is how one
// chain becomes two — the immediate-parent renumbering bug internal/pipeline's
// chainOrigin exists to avoid, arriving through a different door. The only
// caller is the routine hop, where "the run that dispatched this" is the only
// origin there is.
func (s delegationScope) rootedAt(origin string) delegationScope {
	if s.ChainOrigin == "" {
		s.ChainOrigin = origin
	}
	return s
}

// dispatchedBy records the run that made a routine's assignment.create call.
//
// A fill like rootedAt, and for a sharper reason: a scope with a ParentID came
// from the delegation tree, where the edge is parent_assignment_id. Setting a
// run parent there too would give the row two parents, and the walk would
// reach the same work down both and draw it twice.
func (s delegationScope) dispatchedBy(runID string) delegationScope {
	if s.ParentID == "" {
		s.ParentRunID = runID
	}
	return s
}

// resolveDelegationScope finds where a dispatch by actorAgentID sits.
//
// "The caller's own run" is the newest non-terminal assignment addressed TO the
// acting agent. When an agent somehow holds several at once the deepest wins:
// the cap is a safety bound, and guessing shallow is the failure that lets the
// tree grow.
//
// A caller with no in-flight assignment is a root dispatch (depth 1). That is
// the ordinary lead-in-a-chat case, and it is also the fallback when the row
// cannot be found — which is safe in the direction that matters, because a root
// dispatch is still subject to the fan-out cap and its own children are counted
// against it by parent id.
func resolveDelegationScope(ctx context.Context, db *sql.DB, actorAgentID, workspaceID string) (delegationScope, error) {
	if db == nil || actorAgentID == "" {
		return delegationScope{Depth: 1}, nil
	}
	var parentID string
	var parentDepth int
	var parentOrigin sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT id, depth, chain_origin
		  FROM assignments
		 WHERE assigned_to_id = ?
		   AND workspace_id = ?
		   AND status IN ('PENDING','QUEUED','RUNNING')
		 ORDER BY depth DESC, created_at DESC
		 LIMIT 1`, actorAgentID, workspaceID).Scan(&parentID, &parentDepth, &parentOrigin)
	if errors.Is(err, sql.ErrNoRows) {
		return delegationScope{Depth: 1}, nil
	}
	if err != nil {
		return delegationScope{}, fmt.Errorf("resolve delegation parent: %w", err)
	}
	// A legacy row predating the depth column reads 0; treat it as the root it
	// was, so the hop off it is depth 1 rather than a free extra level.
	if parentDepth < 0 {
		parentDepth = 0
	}
	// A chain has ONE root. Inherit the parent's origin when it has one;
	// otherwise the parent IS the root, so name it. Naming the immediate parent
	// in BOTH cases would renumber the chain at every hop, which reads as N
	// unrelated one-hop chains — the bug 20260807220000 fixed for pipeline_runs,
	// stated here in the same words so the two sides cannot drift apart.
	origin := parentOrigin.String
	if origin == "" {
		origin = parentID
	}
	return delegationScope{ParentID: parentID, Depth: parentDepth + 1, ChainOrigin: origin}, nil
}

// chainOriginForCausingRun resolves which chain a run belongs to, for the hop
// where a ROUTINE dispatches an agent (the assignment.create crewship verb).
// The dispatcher injects author_run_id — a field crewshipBody strips from the
// author's args first, so it names the real run — and this turns that run id
// into the chain the new assignment joins.
//
// It reuses pipeline.RunChainReader rather than issuing a second query for
// pipeline_runs.chain_origin: that package owns the column, and a second reader
// is a second answer to "which chain is this", which is the drift the single
// reader exists to prevent.
//
// Three answers, matching automation.Registry.Flush's resolution exactly:
//
//   - the run has an origin → that origin (the run is mid-chain);
//   - the run exists with none → the run itself IS the root, so name it;
//   - the run does not resolve in this workspace → "", i.e. say nothing.
//
// The last is the one worth stating. An unresolvable run is a swept row or
// another tenant's id, and copying it in would put an id nobody can walk into a
// column readers treat as evidence. Empty is honest: this row starts its own
// trace.
//
// A READ ERROR degrades to "" rather than failing the dispatch. This is the one
// place these rules differ from the composed-depth cap next door, and
// deliberately: depth is a safety property, so an unreadable depth must fail
// closed, whereas an origin is provenance, and refusing an agent's work because
// its provenance could not be read trades a real capability for a field in a
// trace. The caller logs it.
func chainOriginForCausingRun(ctx context.Context, db *sql.DB, workspaceID, runID string) (string, error) {
	if db == nil || workspaceID == "" || runID == "" {
		return "", nil
	}
	pos, ok, err := pipeline.NewRunChainReader(db).ChainOf(ctx, workspaceID, runID)
	if err != nil {
		return "", fmt.Errorf("resolve causing run chain: %w", err)
	}
	if !ok {
		return "", nil
	}
	if pos.Origin != "" {
		return pos.Origin, nil
	}
	return runID, nil
}

// The three fan-out buckets, as one WHERE clause each.
//
// The pre-check (countDelegationSiblings) and the insert-time re-prove
// (fanoutGuard) answer the same question at two moments, and the file header
// has always insisted they must not drift. They used to be two hand-copied
// pairs of SQL strings, which is a promise rather than a mechanism; now both
// build on these, so a change lands in one place or not at all.
// TestFanoutPreCheckAndInsertGuardSelectTheSameRows checks the pair over a
// seeded table rather than by inspection.
//
//   - CHILDREN bounds a delegated run's subtree exactly, so every row it ever
//     created counts, terminal or not. No depth filter: a row with a parent
//     came from a capped door by construction.
//   - SELF-FILED is a human's mention of an agent (no acting agent, so the row
//     is owned by the agent it targets).
//   - ROOT is a lead working a chat.
//
// Both root buckets count only IN-FLIGHT rows — a lead's chat can last hours
// across many turns, and counting its lifetime output would silently retire it
// after N tasks — and only rows a capped door wrote (`depth > 0`, see the file
// header).
const (
	fanoutBucketChildren = `parent_assignment_id = ?`

	fanoutBucketSelfFiled = `assigned_by_id = ?
			   AND assigned_to_id = ?
			   AND chat_id = ?
			   AND parent_assignment_id IS NULL
			   AND depth > 0
			   AND status IN ('PENDING','QUEUED','RUNNING')`

	fanoutBucketRoot = `assigned_by_id = ?
			   AND chat_id = ?
			   AND parent_assignment_id IS NULL
			   AND depth > 0
			   AND status IN ('PENDING','QUEUED','RUNNING')`
)

// fanoutBucket picks the predicate and its arguments for one dispatch.
func fanoutBucket(scope delegationScope, caller dispatchCaller, chatID string) (string, []any) {
	switch {
	case scope.ParentID != "":
		return fanoutBucketChildren, []any{scope.ParentID}
	case caller.selfFiled():
		return fanoutBucketSelfFiled, []any{caller.FanoutSubjectID, caller.FanoutSubjectID, chatID}
	default:
		return fanoutBucketRoot, []any{caller.FanoutSubjectID, chatID}
	}
}

// countDelegationSiblings returns how many dispatches the caller's run already
// owns — the number the fan-out cap is compared against. See fanoutBucket for
// which rows that is and why.
func countDelegationSiblings(ctx context.Context, db *sql.DB, scope delegationScope, caller dispatchCaller, chatID string) (int, error) {
	if db == nil {
		return 0, nil
	}
	where, args := fanoutBucket(scope, caller, chatID)
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assignments WHERE `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count delegation siblings: %w", err)
	}
	return n, nil
}

// fanoutGuard returns the SQL predicate (and its arguments) that re-proves the
// fan-out headroom AT INSERT TIME, so the caps survive concurrency.
//
// The pre-check in enforceDelegationCaps exists to produce a good error; on its
// own it is a TOCTOU window, and "many dispatches at once" is not an exotic
// race here — it is the exact shape of the failure the cap is for. SQLite
// evaluates this subquery and the INSERT under one write transaction, so two
// racing dispatches serialise and the second sees the first's row, the same
// argument claimCrewSlot rests on (assignments_queue.go).
//
// It counts the SAME rows as countDelegationSiblings because it is built from
// the same fanoutBucket — they cannot be edited apart.
func fanoutGuard(scope delegationScope, caller dispatchCaller, chatID string, maxFanout int) (string, []any) {
	where, args := fanoutBucket(scope, caller, chatID)
	return `(SELECT COUNT(*) FROM assignments WHERE ` + where + `) < ?`, append(args, maxFanout)
}

// delegationRefusal is a cap saying no, in words the agent can act on.
//
// The agent is the one that reads this string, and its only useful next moves
// are "do it yourself" or "report back" — so the message says which, names the
// number it hit, and names the setting an operator would change. A bare
// "forbidden" would be reported to a human as a bug in Crewship.
type delegationRefusal struct{ msg string }

func (e *delegationRefusal) Error() string { return e.msg }

// dispatchRefused marks a cap's "no" as a DECISION rather than a failure —
// the same marker *agentHeldError carries, so a caller can record either
// without enumerating gate types. See assignments.go's dispatchRefusal.
func (e *delegationRefusal) dispatchRefused() {}

// enforceDelegationCaps resolves the caller's position in the tree and refuses
// the dispatch when either cap is already met. It returns the scope the new
// assignment must be stored with — so the next hop is measured from a number
// this function derived rather than one the caller supplied — and the limits it
// judged against, so the caller can re-prove the fan-out inside its INSERT
// (fanoutGuard) instead of trusting this read to still hold.
//
// Fails CLOSED on a DB error: a cap that cannot read its own state has not
// established that the dispatch is safe, and the alternative — dispatching
// anyway — is the unbounded behaviour this exists to end.
func enforceDelegationCaps(
	ctx context.Context,
	db *sql.DB,
	caller dispatchCaller,
	workspaceID, chatID string,
) (delegationScope, delegationLimits, error) {
	lim := DelegationLimits(ctx, db)

	scope, err := resolveDelegationScope(ctx, db, caller.ActorAgentID, workspaceID)
	if err != nil {
		return delegationScope{}, lim, err
	}

	if scope.Depth > lim.MaxDepth {
		if lim.MaxDepth == 0 {
			return scope, lim, &delegationRefusal{msg: fmt.Sprintf(
				"delegation is switched off on this instance (%s = 0). "+
					"Do this work yourself, or report that it needs delegating — an operator raises %s to allow it.",
				SettingDelegationMaxDepth, SettingDelegationMaxDepth)}
		}
		return scope, lim, &delegationRefusal{msg: fmt.Sprintf(
			"delegation refused: this run is %d delegation hop(s) deep and the limit is %d "+
				"(instance setting %s). Do this task yourself and report the result to whoever assigned it — "+
				"delegating further needs an operator to raise the limit.",
			scope.Depth-1, lim.MaxDepth, SettingDelegationMaxDepth)}
	}

	used, err := countDelegationSiblings(ctx, db, scope, caller, chatID)
	if err != nil {
		return delegationScope{}, lim, err
	}
	if used >= lim.MaxFanout {
		scopeWord := "this run has already dispatched"
		if scope.ParentID == "" {
			scopeWord = "you already have"
		}
		return scope, lim, &delegationRefusal{msg: fmt.Sprintf(
			"delegation refused: %s %d of a maximum %d sub-agent task(s) (fan-out limit, instance setting %s). "+
				"Wait for the running ones with /results/<assignment_id> and use what they return, "+
				"or do the remaining work yourself — raising the limit is an operator action.",
			scopeWord, used, lim.MaxFanout, SettingDelegationMaxFanout)}
	}

	return scope, lim, nil
}

// cappedAssignment is one row insertCappedAssignment writes. Everything the
// caps care about — depth, parent, the fan-out subject — is deliberately NOT
// in here: it arrives as the scope/limits enforceDelegationCaps derived and the
// caller it judged, so a door cannot hand this function a position it chose for
// itself. assigned_by_id is likewise taken from that caller rather than from
// this struct, so the row's owner and the id the fan-out was counted against
// are one value and cannot drift apart.
type cappedAssignment struct {
	WorkspaceID string
	ChatID      string
	TargetID    string
	Task        string
	GroupID     string
	CreatedAt   string
	// MissionID, AuthorAgentID, CreatedByUserID persist what a lock-loss
	// requeue needs to rebuild an identical dispatch door later (#2269
	// follow-up): dispatchByID (assignments_dispatch_pump.go) used to
	// derive MissionID from GroupID, which is wrong for a door whose
	// GroupID is a chat id rather than a mission id (Create's /assign —
	// see its call site). Persisted here instead so dispatchByID reads
	// the row's own word for it rather than reverse-engineering GroupID's
	// meaning per caller. Empty stores NULL (see parentVal's reasoning
	// above); a row with no mission/attribution is legitimately NULL, not
	// "".
	MissionID       string
	AuthorAgentID   string
	CreatedByUserID string
	// SessionID links the run to the issue_agent_sessions row it belongs to
	// (§9.4, PRD-ISSUES-AND-ROUTINES-2026, B1 — #2332). Empty stores NULL,
	// same as the rest of this struct's optional attribution fields: only
	// DispatchMention resolves a session today (resolveOrCreateIssueAgentSession,
	// issue_sessions.go), so a root /assign with no mention involved
	// legitimately names none. Since B3, a non-empty SessionID is
	// load-bearing: idx_assignments_one_active_per_session
	// (20260904172200_assignments_one_active_per_session.sql) refuses a
	// second PENDING/QUEUED/RUNNING row for the same session, and
	// insertCappedAssignment below turns that refusal into a
	// *sessionBusyError naming the run that already holds it, rather than
	// letting the raw constraint violation surface as an opaque 500.
	SessionID string
}

// insertCappedAssignment writes the PENDING assignment row with the fan-out
// headroom re-proved AT INSERT TIME, and is the only place either dispatch
// door inserts one.
//
// The pre-check in enforceDelegationCaps produced the readable refusal; this
// is the one that holds when a run fires ten dispatches at once, which is the
// whole scenario the cap exists for. Same shape as claimCrewSlot: predicate +
// write in one statement, so SQLite serialises the racers instead of admitting
// them all.
//
// depth/parent_assignment_id/chain_origin come from the scope the caller was
// GIVEN by enforceDelegationCaps, never from a request — see the file header. A
// root dispatch stores NULL for the parent so the fan-out count for a lead's
// chat keeps working off the in-flight predicate rather than a self-referential
// chain, and NULL for the origin so it reads as the chain root it is.
//
// A lost race against the FAN-OUT guard returns a *delegationRefusal, so
// both callers answer it the same way instead of one of them treating "no
// row written" as success. A lost race against the SESSION EXCLUSIVITY
// index (idx_assignments_one_active_per_session, §9.4/B3 — only possible
// when a.SessionID is set) returns a *sessionBusyError instead, naming the
// run that already holds the session, resolved with a SELECT run inside
// the SAME conn as the failed INSERT: when db is a *sql.Tx (as
// DispatchMention passes), that read sees a state no concurrent writer can
// have changed out from under it between the failed insert and this read,
// which is the whole reason the session resolve, this insert and that
// lookup all have to share one transaction — see dbConn's doc comment
// (issue_sessions.go).
//
// db accepts *sql.DB or *sql.Tx (dbConn, issue_sessions.go) so a caller that
// needs the session-resolve-then-insert step to be one atomic unit (B3) can
// pass a transaction; Create (assignments_run.go) keeps passing h.db
// directly since it never sets SessionID and has nothing to make atomic
// with this insert.
func insertCappedAssignment(
	ctx context.Context,
	db dbConn,
	scope delegationScope,
	lim delegationLimits,
	caller dispatchCaller,
	a cappedAssignment,
) (string, error) {
	assignmentID := generateCUID()
	var parentVal any
	if scope.ParentID != "" {
		parentVal = scope.ParentID
	}
	// NULL rather than '' when the scope derived no chain: an empty string is a
	// value that says "belongs to the chain named by nothing", which every
	// reader would then have to special-case. NULL is the column's own word for
	// "did not say", and it is what the migration's untouched rows hold.
	var originVal any
	if scope.ChainOrigin != "" {
		originVal = scope.ChainOrigin
	}
	// Same NULL-not-empty-string reasoning as originVal above.
	var parentRunVal any
	if scope.ParentRunID != "" {
		parentRunVal = scope.ParentRunID
	}
	var missionVal, authorAgentVal, createdByUserVal, sessionVal any
	if a.MissionID != "" {
		missionVal = a.MissionID
	}
	if a.AuthorAgentID != "" {
		authorAgentVal = a.AuthorAgentID
	}
	if a.CreatedByUserID != "" {
		createdByUserVal = a.CreatedByUserID
	}
	if a.SessionID != "" {
		sessionVal = a.SessionID
	}
	guardSQL, guardArgs := fanoutGuard(scope, caller, a.ChatID, lim.MaxFanout)
	insertArgs := append([]any{
		assignmentID, a.WorkspaceID, a.ChatID, caller.FanoutSubjectID, a.TargetID,
		a.Task, a.GroupID, scope.Depth, parentVal, originVal, parentRunVal, a.CreatedAt,
		missionVal, authorAgentVal, createdByUserVal, sessionVal,
	}, guardArgs...)
	res, err := db.ExecContext(ctx, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, depth, parent_assignment_id, chain_origin, parent_run_id, created_at, mission_id, author_agent_id, created_by_user_id, session_id)
		SELECT ?, ?, ?, ?, ?, ?, 'PENDING', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		 WHERE `+guardSQL, insertArgs...)
	if err != nil {
		// The fan-out guard's WHERE clause fails CLOSED as zero rows
		// affected (handled below, no error) — the session exclusivity
		// index fails as a hard constraint violation from the INSERT
		// itself, because unlike the fan-out subquery it is not part of
		// this statement's WHERE clause; it is a second, independent
		// constraint SQLite checks against the row this statement tried to
		// write. Only ever reachable when a.SessionID is set: with it NULL
		// the row's session_id is NULL, and idx_assignments_one_active_per_session's
		// own WHERE excludes NULL session_id rows entirely (no two NULLs
		// collide in a unique index).
		if a.SessionID != "" && isSessionExclusivityErr(err) {
			if busyErr := sessionBusyErrorFor(ctx, db, a.SessionID, a.Task); busyErr != nil {
				return "", busyErr
			}
			// The lookup found no in-flight run for this session after
			// all — the row that won the race must have finished (or been
			// cancelled) in the instant between our failed INSERT and this
			// SELECT. Rare, and not actually a conflict any more: fall
			// through to the raw error rather than manufacture a run id
			// that does not exist. The caller logs it like any other
			// insert failure; a retry of the same dispatch would succeed.
		}
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("create assignment rows affected: %w", err)
	}
	if n == 0 {
		return "", &delegationRefusal{msg: fmt.Sprintf(
			"delegation refused: this run is at its limit of %d concurrent sub-agent task(s) "+
				"(fan-out limit, instance setting %s). Wait for one to finish and read it with "+
				"/results/<assignment_id>, or do the remaining work yourself.",
			lim.MaxFanout, SettingDelegationMaxFanout)}
	}
	return assignmentID, nil
}

// sessionBusyError marks the outcome of losing the race against
// idx_assignments_one_active_per_session (§9.4/B3): the target session
// already has a live PENDING/QUEUED/RUNNING run, named by ActiveAssignmentID.
//
// Deliberately NOT a *delegationRefusal — a fan-out/depth refusal means "the
// agent should not do this work at all"; a session-busy result means "this
// exact work is already being done, by the run this error names". The two
// need different handling at the call site: a refusal is reported to the
// agent that asked; a session-busy result is absorbed into the run already
// in flight (steered into its chat, its delivery attached to
// ActiveAssignmentID) and never surfaces as a dispatch failure at all — see
// DispatchMention (issue_mentions.go).
type sessionBusyError struct {
	SessionID          string
	ActiveAssignmentID string
}

func (e *sessionBusyError) Error() string {
	return fmt.Sprintf("session %s already has an active run (%s)", e.SessionID, e.ActiveAssignmentID)
}

// isSessionExclusivityErr reports whether err is a UNIQUE constraint failure
// on idx_assignments_one_active_per_session specifically.
//
// modernc.org/sqlite (verified directly, not assumed from
// agents_create.go's superficially similar one_lead_per_crew check) reports
// a partial-unique-index violation as "UNIQUE constraint failed:
// <table>.<column>" — the INDEXED COLUMN, never the index's own name. A
// detector written to look for "one_active_per_session" would never match
// and every session-busy insert would fall straight through to the raw
// constraint error this function exists to catch — caught by
// TestSessionExclusivity_SecondInsertAttachesToActiveRun failing red against
// exactly that string before this comment was corrected. session_id is
// unique to this one index on this table (idx_assignments_session, B1, is a
// plain non-unique index and cannot produce this error text), so matching
// the column name is unambiguous.
func isSessionExclusivityErr(err error) bool {
	return err != nil &&
		strings.Contains(err.Error(), "UNIQUE constraint failed") &&
		strings.Contains(err.Error(), "assignments.session_id")
}

// sessionBusyErrorFor resolves which run currently holds sessionID's
// exclusivity slot and — this is B3's answer to "the existing steering
// queue" the PRD's own wording assumes — appends followUpTask onto that
// run's OWN task column, rather than routing it through
// internal/chatbridge/steer.go.
//
// That deviation from the PRD's literal wording is deliberate and is
// written up in the PR description: chatbridge.Steer queues a message into
// the chat's CONVERSATION HISTORY for the next turn to read, but every
// assignment/mention run sets req.SkipConvHistory = true unconditionally
// (buildAssignmentRunRequest, assignments_run.go — F13), so a run dispatched
// through DispatchMention never reads that history back on any turn, next or
// otherwise. Steering into it would durably persist the follow-up while
// silently guaranteeing it is never seen — worse than not queuing it at all.
// The task column IS read fresh on every actual dispatch (dispatchByID
// "loads every input runAssignment needs straight from the row",
// assignments_dispatch_pump.go), so appending there is the mechanism that
// can actually deliver text on this run shape.
//
// The honest limit, stated here and in docs/guides/issue-mentions.mdx: this
// reaches a run that has not yet reached its own exec — still PENDING, or
// QUEUED behind chatbridge.AgentRunLock/the crew budget and picked back up
// by dispatchByID — because DispatchMention hands runAssignment's goroutine
// body.Task as a Go value, not a row id it re-reads, so a run whose exec has
// ALREADY started captured its own copy before this UPDATE runs and will
// never see it. There is no live-injection channel anywhere in this
// codebase (non-goal N1); B3 does not invent one. What it guarantees is the
// bookkeeping half regardless: the delivery this follow-up belongs to is
// still attached to ActiveAssignmentID (by the caller) and consumed when
// that run finishes, so "one run, two consumed deliveries" (the accept
// line) holds even on the turns this append cannot reach.
//
// Uses the SAME conn the failed INSERT ran on — when that is a *sql.Tx
// (DispatchMention's case), this read-then-write is inside the one write
// transaction SQLite serialises every writer through, so it sees the exact
// state the failed INSERT collided with, not a state some other writer has
// since moved on from.
//
// Returns nil (not an error) when no in-flight row is found: the row that
// won the race must have reached a terminal status in the instant between
// the failed INSERT and this SELECT. That is not this function's problem to
// solve — the caller (insertCappedAssignment) falls back to the raw
// constraint error, which is a true statement (the INSERT really did lose)
// even though the slot has since freed up again.
func sessionBusyErrorFor(ctx context.Context, db dbConn, sessionID, followUpTask string) *sessionBusyError {
	var activeID string
	err := db.QueryRowContext(ctx, `
		SELECT id FROM assignments
		 WHERE session_id = ? AND status IN ('PENDING','QUEUED','RUNNING')
		 ORDER BY created_at DESC LIMIT 1`, sessionID).Scan(&activeID)
	if err != nil || activeID == "" {
		return nil
	}
	if followUpTask != "" {
		// Best-effort: even a failed UPDATE here does not orphan anything —
		// the delivery still gets attached to ActiveAssignmentID by the
		// caller (issue_mentions.go) and consumed when that run finishes,
		// which is the part the accept line actually requires.
		_, _ = db.ExecContext(ctx,
			`UPDATE assignments SET task = task || ? WHERE id = ?`,
			"\n\n---\nFollow-up received while this run was already in progress:\n"+followUpTask,
			activeID)
	}
	return &sessionBusyError{SessionID: sessionID, ActiveAssignmentID: activeID}
}

// resolveSessionAndInsertAssignment is the B3 rewrite of the mention
// dispatch door's session-then-insert sequence (§9.4). It resolves-or-
// creates the (mission, agent) session and inserts the new PENDING
// assignment for it inside ONE explicit transaction, so nothing can observe
// "the session exists, no active run yet" and decide it is safe to insert a
// second row before this call's own INSERT does the same check — splitting
// the two steps across separate autocommit statements is exactly the TOCTOU
// §9.4 warns comes back the moment resolve-or-create and insert are not
// atomic together.
//
// Four outcomes:
//
//   - a fresh PENDING assignment is inserted: (id, false, sessionID, nil).
//   - the session already had a live run
//     (idx_assignments_one_active_per_session, 20260904172200_assignments_one_active_per_session.sql):
//     no new row is written; (existingRunID, true, sessionID, nil) is
//     returned instead, so the caller (DispatchMention) treats the
//     follow-up as belonging to the run already in flight — its brief
//     appended onto that run's task (sessionBusyErrorFor), its delivery
//     attached to that run — rather than as a dispatch failure (§10.5).
//     sessionID here is the REAL id resolveOrCreateIssueAgentSessionTx
//     resolved, not the empty string a caller that only kept the bool
//     would be left rebuilding a *sessionBusyError with — a bug caught in
//     review on #2342 (the "attached" branch was constructing one with
//     SessionID left blank).
//   - a flag-read/begin-tx/session-resolve fault falls back to the
//     session-less insert (below): (id, false, "", err).
//   - anything else (a DB fault, a fan-out/depth refusal) returns the error
//     verbatim, exactly as insertCappedAssignment always has: ("", false,
//     "", err).
//
// The issue_agent_sessions feature flag is read OUTSIDE the transaction —
// featureflags.IsEnabled has no *sql.Tx overload, and a flag read has no
// business holding a write lock open. Best-effort like
// resolveOrCreateIssueAgentSession's existing contract: a flag-read,
// begin-tx or session-resolve fault must not refuse a dispatch the caps
// already approved, so all three fall back to the session-less insert on db
// directly (a.SessionID stays "", which the exclusivity index's own WHERE
// excludes) rather than failing the whole call — fallback centralises the
// three identical copies an earlier revision had, caught in the same
// review.
//
// One asymmetry worth naming: a session row created by
// resolveOrCreateIssueAgentSessionTx is rolled back with the rest of the
// transaction if the INSERT that follows it fails for a reason OTHER than
// session-busy (a lost fan-out race, say) — unlike the flag-off/begin-tx/
// session-resolve fallbacks above, which never open a transaction to roll
// back in the first place. This is harmless, not a bug: the UPSERT behind
// resolveOrCreateIssueAgentSessionTx is idempotent, so the next mention
// simply recreates the identical row; nothing is lost beyond one wasted
// write.
func resolveSessionAndInsertAssignment(
	ctx context.Context, db *sql.DB, logger *slog.Logger,
	workspaceID, missionID, agentID string,
	scope delegationScope, lim delegationLimits, caller dispatchCaller, a cappedAssignment,
) (assignmentID string, attachedToActive bool, sessionID string, err error) {
	// fallback is the session-less insert every early-exit path below
	// shares — one copy instead of three, so a future change (a metric, an
	// error wrap) lands once. cause is the fault that forced the fallback,
	// logged only when non-nil (a cleanly disabled flag is not a fault).
	fallback := func(step string, cause error) (string, bool, string, error) {
		if cause != nil && logger != nil {
			logger.Warn("resolve session and insert assignment: "+step, "error", cause,
				"mission_id", missionID, "agent_id", agentID)
		}
		id, err := insertCappedAssignment(ctx, db, scope, lim, caller, a)
		return id, false, "", err
	}

	enabled, ffErr := featureflags.IsEnabled(ctx, db, workspaceID, issueAgentSessionsFlagKey)
	if ffErr != nil || !enabled {
		return fallback("check issue_agent_sessions flag", ffErr)
	}

	tx, txErr := db.BeginTx(ctx, nil)
	if txErr != nil {
		return fallback("begin tx", txErr)
	}
	defer func() { _ = tx.Rollback() }() // no-op once Commit has run; see the asymmetry note above

	sid, sessErr := resolveOrCreateIssueAgentSessionTx(ctx, tx, workspaceID, missionID, agentID)
	if sessErr != nil {
		// Roll back BEFORE calling fallback, not after: fallback's insert
		// runs on the pooled *sql.DB, and this tx (BEGIN IMMEDIATE via
		// database.Open's _txlock=immediate) is still holding the write
		// lock at this point. Returning straight to `return fallback(...)`
		// leaves the deferred Rollback above queued to run only once this
		// call returns — so fallback's own write blocks on the lock IT is
		// waiting on, for the full busy_timeout, and then fails with
		// "database is locked" instead of succeeding immediately. A
		// cancelled context mid-transaction (client disconnect, shutdown)
		// is a real way to reach this path, not just a theoretical one.
		// Explicit here; the deferred call becomes a safe no-op after it.
		_ = tx.Rollback()
		return fallback("resolve session", sessErr)
	}
	a.SessionID = sid

	id, insErr := insertCappedAssignment(ctx, tx, scope, lim, caller, a)
	if insErr != nil {
		var busy *sessionBusyError
		if errors.As(insErr, &busy) {
			if cErr := tx.Commit(); cErr != nil {
				return "", false, "", fmt.Errorf("resolve session and insert assignment: commit after session busy: %w", cErr)
			}
			return busy.ActiveAssignmentID, true, sid, nil
		}
		return "", false, "", insErr
	}

	if cErr := tx.Commit(); cErr != nil {
		return "", false, "", fmt.Errorf("resolve session and insert assignment: commit: %w", cErr)
	}
	return id, false, sid, nil
}
