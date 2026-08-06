// Package policy is the per-crew autonomy + behavior_mode decision
// surface introduced by PRD §6 F2 (PR-B). Every HITL-relevant
// subsystem (memory writes, skill creation, persona suggestions,
// behavior monitor escalations, ephemeral spawn) consults this
// package to decide whether an action should go through inbox
// approval, auto-execute with logging, journal-only, or be rejected.
//
// Resolution flow:
//
//	policy.Resolver.Resolve(ctx, crewID)  →  policy.Policy
//	policy.Policy.DecideAction(action)    →  policy.Decision
//
// The Policy struct is a snapshot: callers read once at the start of
// the operation and act on the decision rather than re-resolving per
// step (a flip mid-operation would create surprising half-applied
// outcomes). Resolver.Invalidate(crewID) is called after a PATCH on
// the policy so the next Resolve fetches fresh state.
//
// All public types here are append-only. New autonomy levels,
// actions, or decisions must extend the closed sets in lockstep with
// the consumers that switch on the value — adding a value without
// updating consumers is a silent regression that this package will
// not catch.
package policy

import (
	"fmt"
	"time"
)

// AutonomyLevel is the crew-wide trust dial. Values are persisted in
// crews.autonomy_level (CHECK-constrained at DB level by v98).
type AutonomyLevel string

const (
	// AutonomyStrict: every governable action needs operator Approve.
	// Used for production / compliance-sensitive crews.
	AutonomyStrict AutonomyLevel = "strict"
	// AutonomyGuided: read-only actions auto-execute; writes need OK.
	// Default for new crews.
	AutonomyGuided AutonomyLevel = "guided"
	// AutonomyTrusted: most actions auto-execute; writes log to inbox
	// so the operator can review after the fact.
	AutonomyTrusted AutonomyLevel = "trusted"
	// AutonomyFull: autonomous; journal-only logging. Opt-in for
	// power-team workflows where HITL friction outweighs the safety
	// benefit.
	AutonomyFull AutonomyLevel = "full"
)

// BehaviorMode is the orthogonal "how F4.2 behavior monitor responds
// to anti-patterns" dial. Persisted in crews.behavior_mode.
type BehaviorMode string

const (
	// BehaviorWarn: DENY decisions land as non-blocking inbox
	// notifications; the agent's action proceeds. Default — let
	// the model see warnings and self-correct rather than block on
	// heuristic false-positives.
	BehaviorWarn BehaviorMode = "warn"
	// BehaviorBlock: DENY throws BlockedError in the hook handler;
	// next tool call interrupted. Opt-in for crews that have built
	// behavior-monitor confidence over time.
	BehaviorBlock BehaviorMode = "block"
)

// Action enumerates the HITL-relevant operations the policy gates.
// Adding a new Action requires extending the decision matrix in
// Policy.DecideAction *and* the test matrix in types_test.go.
type Action string

const (
	ActionMemoryWrite        Action = "memory_write"
	ActionSkillCreate        Action = "skill_create"         // agent-authored skill (F4.1 future)
	ActionSkillAssign        Action = "skill_assign"         // existing skill → existing agent
	ActionPersonaSuggest     Action = "persona_suggest"      // inbox proposal flow (Phase 1)
	ActionPersonaDirectWrite Action = "persona_direct_write" // forbidden across Phase 1
	ActionNegativeLearning   Action = "negative_learning"
	ActionEphemeralSpawn     Action = "ephemeral_spawn"

	// The four below are #1768. Each names a capability an agent already
	// had — through the sidecar, ungated — because the backend adapter
	// serving it justified skipping its gate with a claim about its caller
	// rather than a check (internal_routines.go:41, capabilities_check.go:316
	// both assert the autonomy gate runs "upstream"; nothing runs it). An
	// Action is what makes that claim checkable, so the enum comes first and
	// the enforcement hangs off it.

	// ActionCrewCreate — an agent creating a new CREW. The escape this
	// closes: a new crew used to be born with the DB-default autonomy_level
	// (guided, migration v101), so an agent constrained by a strict crew
	// could create a permissive one, create an agent inside it, and act
	// there unbounded.
	//
	// Two things close that, and NEITHER of them is the blocking at guided:
	// strict refuses crew_create outright, and an allowed crew now INHERITS
	// the creating crew's autonomy level instead of taking the column
	// default (internal/api/internal_status.go CreateCrew, pinned by
	// TestAutonomyInvariant_ChildCrewNeverOutranksCreator). With inheritance
	// in place no created crew can ever be more permissive than its creator.
	// What the guided row buys is operator oversight of a durable principal,
	// which is a product decision — see the DecideAction arm.
	ActionCrewCreate Action = "crew_create"
	// ActionAgentCreate — an agent creating a PERSISTENT agent. Distinct
	// from ActionEphemeralSpawn on every axis that made spawn tolerable at
	// trusted: no TTL, no template, no max_ephemeral_agents quota, and a
	// caller-supplied system_prompt. That last one is persona authorship
	// reached through an INSERT instead of an UPDATE — the capability
	// ActionPersonaDirectWrite refuses at every level.
	ActionAgentCreate Action = "agent_create"
	// ActionMissionCreate — an agent creating a mission (plan + tasks
	// assigned to crew members). Creates no principal: it plans work for
	// agents that already exist, in a crew the caller has been proven to own
	// (assertBoundCrewWorkspaceDB runs before the gate). It causes runs once
	// started, which is why it is gated at all rather than not at all.
	ActionMissionCreate Action = "mission_create"
	// ActionRoutineScheduleCreate — an agent creating a CRON schedule:
	// recurring execution with no human in the loop, outliving the session
	// that asked for it. No principal either, but the only one of the four
	// that keeps firing after everyone has stopped looking.
	ActionRoutineScheduleCreate Action = "routine_schedule_create"
)

// Decision is the resolved instruction for the caller. Closed set;
// callers switch on the value to wire the right HITL / logging path.
type Decision string

const (
	// DecisionInboxApprove: write a blocking inbox item; the action
	// does not happen until the operator approves.
	DecisionInboxApprove Decision = "inbox_approve"
	// DecisionAutoLogInbox: the action proceeds immediately; a
	// non-blocking inbox item is created for visibility.
	DecisionAutoLogInbox Decision = "auto_log_inbox"
	// DecisionAutoLogJournal: the action proceeds; logged only to
	// the journal (not the inbox). Used for low-noise side effects.
	DecisionAutoLogJournal Decision = "auto_log_journal"
	// DecisionAutoJournal: the action proceeds with journal-only
	// logging. Same wire path as AutoLogJournal but semantically
	// distinguished for "agent decided autonomously" vs "system
	// auto-executed a routine action".
	DecisionAutoJournal Decision = "auto_journal"
	// DecisionBlockInbox: the hook handler must abort the action
	// (throws BlockedError) AND write a blocking inbox item.
	// Used in behavior_mode=block at strict/guided autonomy.
	DecisionBlockInbox Decision = "block_inbox"
	// DecisionBlockJournal: the hook handler aborts the action,
	// journal-only logging (no inbox noise). Used in behavior_mode=
	// block at trusted autonomy.
	DecisionBlockJournal Decision = "block_journal"
	// DecisionRejected: the action is refused outright at the
	// policy layer without an inbox round-trip. Caller returns an
	// error to its own caller. Used for combinations PRD says are
	// never allowed (persona_direct_write everywhere; ephemeral_
	// spawn at strict).
	DecisionRejected Decision = "rejected"
)

// Policy is the resolved per-crew state. Snapshotted by the resolver
// and consumed by DecideAction / DecideBehaviorDeny. Fields are
// public so the API layer can serialize it for crewship policy get.
type Policy struct {
	CrewID        string
	AutonomyLevel AutonomyLevel
	BehaviorMode  BehaviorMode
	SetByUserID   string
	SetAt         time.Time
	Reason        string
}

// DecideAction maps (autonomy_level × action) to a Decision. Encodes
// the full matrix from PRD §6 F2. The matrix is intentionally
// flat (no fallthrough / inheritance) because each cell was decided
// case-by-case and any "this is just like X but..." shortcut tends
// to drift from the documented intent.
func (p Policy) DecideAction(a Action) Decision {
	switch a {
	case ActionMemoryWrite:
		switch p.AutonomyLevel {
		case AutonomyStrict, AutonomyGuided:
			return DecisionInboxApprove
		case AutonomyTrusted:
			return DecisionAutoLogInbox
		case AutonomyFull:
			return DecisionAutoJournal
		}
	case ActionSkillCreate:
		switch p.AutonomyLevel {
		case AutonomyStrict, AutonomyGuided, AutonomyTrusted:
			return DecisionInboxApprove
		case AutonomyFull:
			return DecisionAutoLogInbox
		}
	case ActionSkillAssign:
		switch p.AutonomyLevel {
		case AutonomyStrict:
			return DecisionInboxApprove
		case AutonomyGuided, AutonomyTrusted, AutonomyFull:
			return DecisionAutoLogJournal
		}
	case ActionPersonaSuggest:
		switch p.AutonomyLevel {
		case AutonomyStrict, AutonomyGuided, AutonomyTrusted:
			return DecisionInboxApprove
		case AutonomyFull:
			return DecisionAutoJournal
		}
	case ActionPersonaDirectWrite:
		// Phase 1: rejected everywhere. PR-E might relax for full
		// once we have peer-card-driven persona drift handled.
		return DecisionRejected
	case ActionNegativeLearning:
		switch p.AutonomyLevel {
		case AutonomyStrict:
			return DecisionInboxApprove
		case AutonomyGuided:
			return DecisionAutoLogJournal
		case AutonomyTrusted, AutonomyFull:
			return DecisionAutoJournal
		}
	case ActionEphemeralSpawn:
		switch p.AutonomyLevel {
		case AutonomyStrict:
			return DecisionRejected
		case AutonomyGuided:
			return DecisionInboxApprove
		case AutonomyTrusted:
			return DecisionAutoLogJournal
		case AutonomyFull:
			return DecisionAutoJournal
		}
	case ActionCrewCreate, ActionAgentCreate:
		// One row for two actions — the deliberate exception to this matrix's
		// "no shortcuts" rule, because both create a durable PRINCIPAL: a
		// thing that will act again tomorrow, on its own, after the session
		// that made it is gone.
		//
		// That is a narrower claim than the row used to make. It once also
		// carried routine_schedule_create under the heading "standing grant";
		// a cron entry is standing but it is not a principal, and the
		// difference decides the guided cell — see its own arm below.
		//
		// The property is what sets strict to Rejected rather than
		// InboxApprove. Strict means "every governable action needs operator
		// Approve"; approving a permanent agent or a new crew is approving
		// every future action it will take, which is the one thing an
		// operator on strict has said they do not want to do. Same reasoning
		// that already puts ephemeral_spawn at Rejected under strict, applied
		// to principals that — unlike a TTL'd contractor — never expire.
		//
		// Guided and trusted stay BLOCKING, and the reason is not security.
		// The autonomy escape (#1768) is closed by two other things, both
		// already in place: strict refuses crew_create outright, and an
		// allowed crew inherits its creator's autonomy level rather than the
		// v101 column default, so no child is ever more permissive than its
		// parent. With inheritance in place, blocking at guided buys no
		// additional security — it buys operator OVERSIGHT, which is a
		// separate product question, and the answer here is yes for
		// principals. agent_create additionally writes a caller-supplied
		// system_prompt: persona authorship reached through an INSERT, while
		// ActionPersonaDirectWrite is DecisionRejected at every level. An
		// agent quietly acquiring a permanent colleague whose prompt it wrote
		// itself is exactly what an operator wants to see BEFORE it happens,
		// and "writes need OK" — the documented meaning of guided, see
		// AutonomyGuided above — applies literally.
		//
		// Trusted stays at InboxApprove where ephemeral_spawn relaxes to
		// journal. A trusted crew taking on a 60-minute template-derived
		// contractor under a quota is a bounded bet; the same crew taking on
		// a permanent teammate whose system prompt the agent wrote is not,
		// and no quota bounds it (POST /api/v1/internal/crews and
		// /api/v1/internal/agents have none). Same call ActionSkillCreate
		// made, for the same reason: durable artefacts stay gated a level
		// longer than transient ones.
		//
		// Full lands on AutoLogInbox rather than AutoJournal. Full is
		// journal-only for routine side effects, but a new permanent agent or
		// crew is precisely the change an operator wants to SEE having
		// happened, and the journal is where things go to not be read.
		// Non-blocking, so full autonomy is still autonomous.
		switch p.AutonomyLevel {
		case AutonomyStrict:
			return DecisionRejected
		case AutonomyGuided, AutonomyTrusted:
			return DecisionInboxApprove
		case AutonomyFull:
			return DecisionAutoLogInbox
		}
	case ActionRoutineScheduleCreate:
		// The genuinely borderline one, which is why it no longer shares the
		// principal row. A cron entry creates nobody — it cannot author a
		// persona, cannot be delegated to, cannot hire — but it does outlive
		// the session that asked for it, and it is the only action in this
		// matrix that keeps firing after everyone has stopped looking.
		//
		// Strict still refuses: approving a schedule once is approving every
		// future firing, and that is the thing strict exists to not do.
		//
		// Below strict the answer is a NON-blocking inbox item rather than a
		// blocking one. A blocking hold and a notice give the operator the
		// same visibility — the same row, in the same place, naming the same
		// schedule; the only difference is whether ordinary work stops
		// meanwhile. And the schedule stays an operator-editable row either
		// way: PATCH /workspaces/{id}/pipeline-schedules/{id} disables it in
		// one call, which is the same lever approving/denying would have
		// pulled. Trusted and full drop to journal-only for the reason every
		// other row does at that level: one item per created thing is how an
		// inbox stops being read.
		switch p.AutonomyLevel {
		case AutonomyStrict:
			return DecisionRejected
		case AutonomyGuided:
			return DecisionAutoLogInbox
		case AutonomyTrusted, AutonomyFull:
			return DecisionAutoLogJournal
		}
	case ActionMissionCreate:
		// Not on the principal row either, and for a stronger reason: a
		// mission creates nothing that outlives it. It plans work for agents
		// that already exist, in a crew the caller has been proven to own
		// (assertBoundCrewWorkspaceDB runs before the gate in
		// missions_internal.go), and every task it dispatches is executed by
		// an agent that was already allowed to run. It is delegation with a
		// plan attached.
		//
		// Strict gets Approve rather than Reject: a strict crew that cannot
		// plan work at all is a usability cliff strict does not have anywhere
		// else (memory_write at strict is InboxApprove too), and a mission
		// that cannot start dispatches nothing, so "created, inert" is a real
		// state here.
		//
		// Guided relaxes to a non-blocking notice. Planning is ordinary work;
		// stopping it needs a reason beyond "an agent did it", and the escape
		// #1768 closed is not on this path — no principal is created and the
		// crew is fixed by the token binding, so nothing here can widen what
		// the acting agent was already allowed to do.
		//
		// WHAT BOUNDS THE FAN-OUT — and, still, what does not.
		//
		// The #1757 depth/fan-out cap does NOT reach a mission, and never
		// will on this path: the engine dispatches its task list through
		// DispatchAssignment, which does not pass through
		// insertCappedAssignment, and delegation_limits.go states that
		// outright. What closes it is a separate control on the door
		// assignments.go named — api/mission_limits.go bounds the task list
		// of one agent-created mission (mission.max_tasks) and how many
		// agent-created missions a crew may have live at once
		// (mission.max_active_per_crew). Both are read live from app_settings
		// and both are counted from server-side state, never from the
		// request. The second number carries most of the weight, because
		// mission creation RECURSES: a task-less mission makes the engine run
		// its lead as a LEAD-planning dispatch — the one shape that keeps the
		// sidecar — and the planning brief itself offers "create a new
		// sub-mission" as Option B. A per-mission cap alone would have been
		// the cap that covers less than it appears to.
		//
		// THE CAVEAT SHRINKS TO THIS, and does not disappear. The caps sit on
		// InternalMissionHandler.Create only. The human JWT door (POST
		// /api/v1/crews/{crewId}/missions) is uncapped by design — an
		// operator planning work is making a decision, and that handler
		// cannot carry a task list at all — and missions a human created are
		// deliberately not counted against the agents' budget either.
		// Recursion that crosses into a linked crew spends THAT crew's
		// budget, so the bound is per crew rather than global. Re-blocking
		// here would still be the wrong lever: a gate on creation says
		// nothing about how wide the thing fans out once approved, which is
		// why the fix was a number on the dispatch-causing door and not this
		// cell.
		switch p.AutonomyLevel {
		case AutonomyStrict:
			return DecisionInboxApprove
		case AutonomyGuided:
			return DecisionAutoLogInbox
		case AutonomyTrusted:
			return DecisionAutoLogJournal
		case AutonomyFull:
			return DecisionAutoJournal
		}
	}
	// Defensive default: any (action, level) pair we haven't mapped
	// gets the safest treatment — inbox approval. Adding a new
	// Action without extending the switch becomes a "weird, why is
	// this always going through inbox?" signal in operator UX.
	return DecisionInboxApprove
}

// DecideBehaviorDeny resolves what to do when the F4.2 behavior
// evaluator returns DENY. The mapping depends on both autonomy
// level AND behavior_mode (warn vs block). Validate() guarantees
// the forbidden combination (full × block) never reaches here.
func (p Policy) DecideBehaviorDeny() Decision {
	if p.BehaviorMode == BehaviorWarn {
		// warn mode: DENY downgrades to a non-blocking notification
		if p.AutonomyLevel == AutonomyFull {
			return DecisionAutoJournal
		}
		return DecisionAutoLogInbox
	}
	// block mode: actually stop the agent
	switch p.AutonomyLevel {
	case AutonomyStrict, AutonomyGuided:
		return DecisionBlockInbox
	case AutonomyTrusted:
		return DecisionBlockJournal
	}
	// AutonomyFull × BehaviorBlock is forbidden — Validate catches this
	// at the API boundary. Defensive default if validation was bypassed
	// (manual SQL fix-up, schema drift): fail closed with the strictest
	// block decision. The "surprise block on a fully-trusted agent" risk
	// is acceptable; the "silently let an agent through that an operator
	// thought was blocked" risk is not.
	return DecisionBlockInbox
}

var validAutonomyLevels = map[AutonomyLevel]struct{}{
	AutonomyStrict: {}, AutonomyGuided: {}, AutonomyTrusted: {}, AutonomyFull: {},
}

var validBehaviorModes = map[BehaviorMode]struct{}{
	BehaviorWarn: {}, BehaviorBlock: {},
}

// Validate enforces the enum closed sets + the forbidden
// (full × block) combination. Called at the API PATCH boundary so a
// bad request never lands in the DB.
func (p Policy) Validate() error {
	if _, ok := validAutonomyLevels[p.AutonomyLevel]; !ok {
		return fmt.Errorf("policy: invalid autonomy_level %q (want strict|guided|trusted|full)", p.AutonomyLevel)
	}
	if _, ok := validBehaviorModes[p.BehaviorMode]; !ok {
		return fmt.Errorf("policy: invalid behavior_mode %q (want warn|block)", p.BehaviorMode)
	}
	if p.AutonomyLevel == AutonomyFull && p.BehaviorMode == BehaviorBlock {
		return fmt.Errorf("policy: autonomy_level=full is incompatible with behavior_mode=block (opt-in trust × opt-in restriction)")
	}
	return nil
}
