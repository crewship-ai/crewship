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
	// notifications; the agent's action proceeds. Default, Hermes-
	// aligned — let the model see warnings and self-correct rather
	// than block on heuristic false-positives.
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
	// AutonomyFull × BehaviorBlock is forbidden — Validate catches
	// this at the API boundary. Defensive default if it somehow
	// reaches here: behave like warn-mode full (auto journal) so we
	// don't surprise-block an agent the operator marked as fully
	// trusted.
	return DecisionAutoJournal
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
