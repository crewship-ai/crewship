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

	// The three below are #1791's follow-on: the capabilities a `crewship`
	// routine step reaches that had no Action at all, so the step kind shipped
	// with five of its six verbs refused at save rather than dispatched
	// ungoverned (internal/pipeline/crewship_step.go).
	//
	// WHAT THEY ARE NOT. They are not a new restriction on agents. Every one of
	// these capabilities is already reachable, ungated, by an agent inside a
	// container: internal/sidecar/issue_verbs.go forwards update/comment/link,
	// query.go raises escalations, and POST /internal/assignments is the door
	// behind /assign. None of those handlers consults autonomy_level today.
	// Declaring an Action does not close that — it makes it CLOSEABLE, which is
	// the same move #1768 made for the six creation routes, and the enforcement
	// currently hangs off the routine door only.
	//
	// STATE THE GAP RATHER THAN IMPLY IT (delegation_limits.go's rule). At
	// `strict` the routine door refuses what the agent door still allows, so an
	// author on a strict crew can reach the same capability through an
	// `agent_run` step. That asymmetry is the reason to gate the sidecar
	// adapters on these same Actions next; it is not a reason to weaken the
	// rows, because the alternative — strict meaning nothing here — trades a
	// known gap for a silent one.

	// ActionIssueWrite — writing to an issue that ALREADY EXISTS: changing its
	// fields (issue.update), commenting on it (issue.comment), or relating it
	// to another (issue.link).
	//
	// ONE ACTION FOR THREE VERBS, deliberately, and the test is whether any
	// property that decides a cell differs between them. None does:
	//
	//   - none creates a principal, a schedule, or anything that outlives the
	//     call;
	//   - all three are held to the caller's own crew and workspace BEFORE the
	//     gate (assertBoundCrewWorkspaceDB in issues_internal.go and
	//     issues_internal_relations.go), so none can widen what the crew
	//     already reaches;
	//   - all three land in the issue's own audit trail (#1791) and broadcast
	//     issue.updated, i.e. they are already recorded where an operator looks
	//     for them;
	//   - the one non-inert edge — an @mention waking an agent — is shared by
	//     comment AND update, because update carries an inline `comment` field
	//     that runs the same mention trigger.
	//
	// Three constants would therefore be three copies of one row, free to drift
	// apart without anyone choosing to. Splitting later (a separate
	// ActionIssueComment, say) is an append-only change this package allows;
	// starting split would be pretending to a distinction that does not exist.
	//
	// The property that DOES set this row apart from every other row in the
	// matrix is frequency. Every other Action here governs a rare, durable
	// event — a crew, an agent, a cron entry, a plan. A triage routine writes
	// to thirty issues in one run. That is what decides the guided cell; see
	// the DecideAction arm.
	ActionIssueWrite Action = "issue_write"

	// ActionAssignmentCreate — dispatching work to an agent that already
	// exists (POST /api/v1/internal/assignments, the door behind /assign).
	//
	// It is mission_create with the plan taken away: no principal is created,
	// the target agent was already allowed to run, and the crew is fixed by the
	// binding before the gate. What it does cost is real — a container start, a
	// system prompt and a model turn, billed to the workspace.
	//
	// The fan-out is bounded on its own door and NOT by this cell:
	// insertCappedAssignment enforces delegation.max_depth and the per-caller
	// fan-out cap, both read fresh from app_settings and both counted from
	// server-side rows rather than from anything the caller wrote
	// (delegation_limits.go). A routine-fired call has no assignment row of its
	// own, so it is a root at depth 1 and the fan-out number is what carries
	// the weight there.
	ActionAssignmentCreate Action = "assignment_create"

	// ActionEscalationCreate — raising an escalation: a PENDING escalations
	// row, a blocking high-priority inbox item addressed to every MANAGER, and
	// a warn-severity journal entry (escalation_handler.go).
	//
	// The only Action in this matrix whose cost is paid by a HUMAN rather than
	// by the machine, which changes what the matrix can usefully say about it.
	// See the DecideAction arm — the row is flat, and the bound that actually
	// matters (volume) lives on the door, in crewship_escalation_cap.go.
	ActionEscalationCreate Action = "escalation_create"
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
	case ActionIssueWrite:
		// Modify a thing that already exists, create nothing — which is the
		// shape skill_assign already has, and this row matches it.
		//
		// STRICT HOLDS. Strict means "every governable action needs operator
		// Approve", and an issue write is governable. Not Rejected: that value
		// is this matrix's marker for "creates a durable principal or keeps
		// firing" (crew, agent, ephemeral spawn, cron), and a comment is
		// neither. Rejecting it would rank an issue comment above a memory
		// write in severity, which it is not.
		//
		// GUIDED DOES NOT HOLD, and this is the cell that has to be argued,
		// because guided's documented meaning is "writes need OK". Two things
		// override the literal reading:
		//
		//  1. The inbox is the wrong instrument at this frequency. One item per
		//     issue write is how an inbox stops being read — the same argument
		//     routine_schedule_create's arm already makes for a much rarer
		//     action — and here the item would be a DUPLICATE: #1791 put every
		//     issue write in the issue's own activity trail, keyed to the issue,
		//     which is where an operator looks. A second copy in a stream is
		//     strictly worse than the first.
		//
		//  2. A hold is not a hold on the unattended path. A routine has nobody
		//     attached to approve it, so InboxApprove IS a refusal there
		//     (crewship_actions.go says so in as many words). Meanwhile the same
		//     capability is reachable ungated by an agent_run step in the same
		//     routine, because the sidecar's issue verbs consult no policy. So
		//     blocking guided would not stop the write; it would move it to a
		//     door with no policy row at all.
		//
		// What guided keeps is the journal entry and the issue's own trail. What
		// it gives up is a blocking hold that bought neither containment (the
		// crew binding does that) nor visibility (the issue does that).
		switch p.AutonomyLevel {
		case AutonomyStrict:
			return DecisionInboxApprove
		case AutonomyGuided, AutonomyTrusted:
			return DecisionAutoLogJournal
		case AutonomyFull:
			return DecisionAutoJournal
		}
	case ActionAssignmentCreate:
		// The same row as mission_create, because this is mission_create with
		// the plan removed: delegation to an agent that already exists, in a
		// crew the caller has been proven to own, creating no principal.
		//
		// Strict approves rather than rejects, for mission_create's reason:
		// approving one assignment approves one bounded unit of work, not every
		// future one. Nothing durable is granted.
		//
		// Guided gets a NOTICE where issue_write gets journal-only, and the
		// difference is what the action spends. An issue write costs a row; an
		// assignment costs a container start, a system prompt and a model turn,
		// and it is the moment a routine stops editing records and starts making
		// the crew work. That is rare enough to be worth one non-blocking inbox
		// row and consequential enough to want one.
		//
		// Guided does not BLOCK, for the reason it does not block mission_create:
		// nothing on this path widens what the acting crew could already do, the
		// fan-out is bounded on the dispatch door itself (delegation_limits.go's
		// depth + fan-out caps, counted from server state), and a hold on the
		// routine path is a refusal that pushes the author to an agent_run step
		// whose agent calls /assign ungated. Same work, no policy row.
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
	case ActionEscalationCreate:
		// A FLAT ROW, and the flatness is the decision rather than a shortcut.
		//
		// Everything else in this matrix bounds what the crew may do to the
		// SYSTEM, and trust is the right axis for that. An escalation's cost is
		// a human's attention: a blocking, high-priority inbox item for every
		// MANAGER, plus a PENDING row someone has to resolve. The risk it
		// carries does not vary with how much the crew is trusted, because it is
		// not a question of trust — which is why the row does not vary either.
		// persona_direct_write is already flat for the mirror-image reason.
		//
		// BOTH INBOX-SHAPED DECISIONS ARE SELF-DEFEATING HERE, which is what
		// removes them from the choice:
		//
		//   - InboxApprove would interrupt a human to authorise interrupting a
		//     human. The approval request is the same interrupt as the
		//     escalation, arriving first and carrying less.
		//   - AutoLogInbox would write an inbox row ABOUT an inbox row. The
		//     escalation IS the operator-facing artefact; a notice duplicates it.
		//
		// So every arm that proceeds must be journal-only. That leaves one real
		// question — whether strict REFUSES — and the answer is no, twice over:
		//
		//   - A routine can already reach a human at any autonomy level with a
		//     `notify` step, which carries no policy row at all. Refusing the
		//     structured, resolvable, audited escalation while leaving the
		//     unstructured ping open would degrade the record without removing
		//     the interrupt. That is the ungoverned-door failure in its purest
		//     form.
		//   - It is incoherent on strict's own terms. Strict is the level at
		//     which the operator most wants to be in the loop, and this is the
		//     one action in the matrix whose entire purpose is to put them there.
		//     "Ask before you act" cannot sensibly forbid asking.
		//
		// WHAT THE MATRIX CANNOT DO, said plainly: it decides per call and
		// cannot express a rate, and the risk here is volume — a foreach over
		// 500 issues raising one escalation each. A cell pretending to bound
		// that would be the cap believed to cover more than it does. The bound
		// lives on the door instead: escalation.max_pending_per_crew, enforced
		// on the routine path in internal/api/crewship_escalation_cap.go and
		// counted from server-side rows.
		switch p.AutonomyLevel {
		case AutonomyStrict, AutonomyGuided, AutonomyTrusted:
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

// knownActions is the closed set of Actions this package declares. It exists
// so a caller holding an Action as a STRING — internal/pipeline's crewship verb
// registry stores one, because pipeline must not import this package — can ask
// whether it names anything real.
//
// Without it the failure is quiet rather than loud: DecideAction answers its
// defensive DecisionInboxApprove for a typo'd action, so a mis-declared verb
// does not fail open, it simply refuses forever with a message about autonomy
// levels and never says the word "typo". TestPolicy_KnownActionsMatchesSource
// derives the same list out of the source so this map cannot fall behind the
// constants above.
var knownActions = map[Action]struct{}{
	ActionMemoryWrite:           {},
	ActionSkillCreate:           {},
	ActionSkillAssign:           {},
	ActionPersonaSuggest:        {},
	ActionPersonaDirectWrite:    {},
	ActionNegativeLearning:      {},
	ActionEphemeralSpawn:        {},
	ActionCrewCreate:            {},
	ActionAgentCreate:           {},
	ActionMissionCreate:         {},
	ActionRoutineScheduleCreate: {},
	ActionIssueWrite:            {},
	ActionAssignmentCreate:      {},
	ActionEscalationCreate:      {},
}

// IsKnownAction reports whether a is an Action this package declares.
func IsKnownAction(a Action) bool {
	_, ok := knownActions[a]
	return ok
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
