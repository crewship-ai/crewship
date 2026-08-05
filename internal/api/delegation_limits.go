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
// WHICH DOOR THIS COVERS. One: AssignmentHandler.Create, the endpoint behind
// the sidecar's /assign. The other ways an agent can cause work to run —
// /mission/create (the mission engine dispatches its task list through
// DispatchAssignment, which does not pass through here) and /spawn (an
// ephemeral hire, gated instead by the crew's autonomy_level) — are NOT capped
// by this. Stated rather than implied, because a cap that is believed to cover
// more than it does is worse than a missing one.

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
func countDelegationSiblings(ctx context.Context, db *sql.DB, scope delegationScope, actorAgentID, chatID string) (int, error) {
	if db == nil {
		return 0, nil
	}
	var (
		n   int
		err error
	)
	if scope.ParentID != "" {
		err = db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM assignments WHERE parent_assignment_id = ?`, scope.ParentID).Scan(&n)
	} else {
		err = db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM assignments
			 WHERE assigned_by_id = ?
			   AND chat_id = ?
			   AND parent_assignment_id IS NULL
			   AND status IN ('PENDING','QUEUED','RUNNING')`, actorAgentID, chatID).Scan(&n)
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
// The predicate must stay identical to countDelegationSiblings' two branches;
// they answer the same question at two moments.
func fanoutGuard(scope delegationScope, actorAgentID, chatID string, maxFanout int) (string, []any) {
	if scope.ParentID != "" {
		return `(SELECT COUNT(*) FROM assignments WHERE parent_assignment_id = ?) < ?`,
			[]any{scope.ParentID, maxFanout}
	}
	return `(SELECT COUNT(*) FROM assignments
	          WHERE assigned_by_id = ? AND chat_id = ? AND parent_assignment_id IS NULL
	            AND status IN ('PENDING','QUEUED','RUNNING')) < ?`,
		[]any{actorAgentID, chatID, maxFanout}
}

// delegationRefusal is a cap saying no, in words the agent can act on.
//
// The agent is the one that reads this string, and its only useful next moves
// are "do it yourself" or "report back" — so the message says which, names the
// number it hit, and names the setting an operator would change. A bare
// "forbidden" would be reported to a human as a bug in Crewship.
type delegationRefusal struct{ msg string }

func (e *delegationRefusal) Error() string { return e.msg }

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
	actorAgentID, workspaceID, chatID string,
) (delegationScope, delegationLimits, error) {
	lim := DelegationLimits(ctx, db)

	scope, err := resolveDelegationScope(ctx, db, actorAgentID, workspaceID)
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

	used, err := countDelegationSiblings(ctx, db, scope, actorAgentID, chatID)
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
