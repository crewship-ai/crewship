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
// in-flight row that agent owns in the issue's chat — including the ones IT
// dispatched while leading the mission, which DispatchAssignment writes with
// the same assigned_by_id, the same chat_id and a NULL parent. A lead running
// eight tasks on an issue was therefore unmentionable by a human, and the
// refusal was swallowed into a `refused` row nobody reads. The bucket is now
// narrowed by dispatchCaller.selfFiled: a human's mention is counted against
// the rows a human's mentions created (assigned_by = assigned_to = the target),
// not against that agent's outbound delegations. Same setting, same number,
// same server-derived count — a different WHERE, so the two kinds of work stop
// competing for one budget.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
type delegationScope struct {
	ParentID string
	Depth    int
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
	err := db.QueryRowContext(ctx, `
		SELECT id, depth
		  FROM assignments
		 WHERE assigned_to_id = ?
		   AND workspace_id = ?
		   AND status IN ('PENDING','QUEUED','RUNNING')
		 ORDER BY depth DESC, created_at DESC
		 LIMIT 1`, actorAgentID, workspaceID).Scan(&parentID, &parentDepth)
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
	return delegationScope{ParentID: parentID, Depth: parentDepth + 1}, nil
}

// countDelegationSiblings returns how many dispatches the caller's run already
// owns — the number the fan-out cap is compared against.
//
// Two shapes, one limit:
//
//   - a DELEGATED run is one turn of one sub-agent, so every assignment it ever
//     created is counted, terminal or not. That bounds the subtree exactly.
//   - a ROOT run is a lead working a chat that may last hours across many user
//     turns, so only IN-FLIGHT dispatches count. Counting its lifetime output
//     would silently retire a lead after N tasks, which is a different (and
//     wrong) product decision wearing a safety cap's clothes.
//
// A SELF-FILED root (a human's mention: no acting agent, so the row is owned by
// the agent it targets) narrows that second predicate to rows addressed back to
// the same agent. Without the narrowing a person's mention was measured against
// the target's own outbound delegations in that chat — see the file header.
func countDelegationSiblings(ctx context.Context, db *sql.DB, scope delegationScope, caller dispatchCaller, chatID string) (int, error) {
	if db == nil {
		return 0, nil
	}
	var (
		n   int
		err error
	)
	switch {
	case scope.ParentID != "":
		err = db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM assignments WHERE parent_assignment_id = ?`, scope.ParentID).Scan(&n)
	case caller.selfFiled():
		err = db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM assignments
			 WHERE assigned_by_id = ?
			   AND assigned_to_id = ?
			   AND chat_id = ?
			   AND parent_assignment_id IS NULL
			   AND status IN ('PENDING','QUEUED','RUNNING')`,
			caller.FanoutSubjectID, caller.FanoutSubjectID, chatID).Scan(&n)
	default:
		err = db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM assignments
			 WHERE assigned_by_id = ?
			   AND chat_id = ?
			   AND parent_assignment_id IS NULL
			   AND status IN ('PENDING','QUEUED','RUNNING')`, caller.FanoutSubjectID, chatID).Scan(&n)
	}
	if err != nil {
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
// The predicate must stay identical to countDelegationSiblings' three branches;
// they answer the same question at two moments.
func fanoutGuard(scope delegationScope, caller dispatchCaller, chatID string, maxFanout int) (string, []any) {
	switch {
	case scope.ParentID != "":
		return `(SELECT COUNT(*) FROM assignments WHERE parent_assignment_id = ?) < ?`,
			[]any{scope.ParentID, maxFanout}
	case caller.selfFiled():
		return `(SELECT COUNT(*) FROM assignments
		          WHERE assigned_by_id = ? AND assigned_to_id = ? AND chat_id = ?
		            AND parent_assignment_id IS NULL
		            AND status IN ('PENDING','QUEUED','RUNNING')) < ?`,
			[]any{caller.FanoutSubjectID, caller.FanoutSubjectID, chatID, maxFanout}
	default:
		return `(SELECT COUNT(*) FROM assignments
		          WHERE assigned_by_id = ? AND chat_id = ? AND parent_assignment_id IS NULL
		            AND status IN ('PENDING','QUEUED','RUNNING')) < ?`,
			[]any{caller.FanoutSubjectID, chatID, maxFanout}
	}
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
// depth/parent_assignment_id come from the scope the caller was GIVEN by
// enforceDelegationCaps, never from a request — see the file header. A root
// dispatch stores NULL for the parent so the fan-out count for a lead's chat
// keeps working off the in-flight predicate rather than a self-referential
// chain.
//
// A lost race returns a *delegationRefusal, so both callers answer it the same
// way instead of one of them treating "no row written" as success.
func insertCappedAssignment(
	ctx context.Context,
	db *sql.DB,
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
	guardSQL, guardArgs := fanoutGuard(scope, caller, a.ChatID, lim.MaxFanout)
	insertArgs := append([]any{
		assignmentID, a.WorkspaceID, a.ChatID, caller.FanoutSubjectID, a.TargetID,
		a.Task, a.GroupID, scope.Depth, parentVal, a.CreatedAt,
	}, guardArgs...)
	res, err := db.ExecContext(ctx, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, depth, parent_assignment_id, created_at)
		SELECT ?, ?, ?, ?, ?, ?, 'PENDING', ?, ?, ?, ?
		 WHERE `+guardSQL, insertArgs...)
	if err != nil {
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
