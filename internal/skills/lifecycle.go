// Package skills holds the skill registry primitives. This file
// implements the lifecycle state machine landed by migration v102
// (PRD §6 F4.1).
//
// The lifecycle is orthogonal to the pre-existing `verification`
// column. `verification` records the curator's "do I trust this skill
// to load?" verdict (UNVERIFIED / SANDBOXED / VERIFIED). `lifecycle`
// records the operational "should this skill stay in the catalog?"
// state — a skill can be verified but stale, or unverified and active.
//
// State machine (one direction unless explicitly noted):
//
//	active     → stale       (unused >30d AND not currently assigned)
//	stale      → archived    (>90d unused)
//	archived   → active      (operator-initiated recover via UI/CLI)
//	*          → deprecated  (operator-initiated; tombstoned, immutable)
//
// The "assignment trumps timer" rule lives in the EvaluateTransition
// pure function below: any skill that has at least one non-expired
// assignment stays active regardless of last_used_at. Without this,
// a critical-but-rarely-fired skill would silently rot into stale →
// archived even though an operator deliberately attached it to an
// agent for that exact reason.
//
// EvaluateTransition is a *pure* function over the inputs (current
// state + age clocks + assignment count). The DB write is the
// caller's job — splitting the pure logic from the side effect lets
// the F4.1 evaluator and the CLI `crewship skill lifecycle` admin
// command share the same decision without a DB round trip in tests.
package skills

import (
	"fmt"
	"time"
)

// LifecycleState mirrors the CHECK-constrained values in skills.
// lifecycle_state. Closed set — adding a value here requires
// extending both migration v102's CHECK constraint AND the
// EvaluateTransition switch below.
type LifecycleState string

const (
	// LifecycleActive is the default state assigned to every pre-v100
	// row + every newly imported skill. Indicates the skill is
	// available to agents and counts toward the catalog.
	LifecycleActive LifecycleState = "active"
	// LifecycleStale signals "no usage in StaleAfter days AND no
	// active assignments". Still loadable, still recoverable, but the
	// curator UI should hint "consider archiving".
	LifecycleStale LifecycleState = "stale"
	// LifecycleArchived hides the skill from catalog browse but keeps
	// it loadable for already-assigned agents (back-compat for old
	// crew configs). Recoverable via SetLifecycle(active).
	LifecycleArchived LifecycleState = "archived"
	// LifecycleDeprecated is the terminal state. The skill is
	// tombstoned: no new assignments, no auto-loads, no edits. Existing
	// agents continue to load it until their config is updated. Cannot
	// transition out (operator must hard-delete + re-import).
	LifecycleDeprecated LifecycleState = "deprecated"
)

// StaleAfter is the inactivity threshold for active → stale. PRD
// §6 F4.1 specifies 30 days; revisit only via a coordinated
// PRD/migration update.
const StaleAfter = 30 * 24 * time.Hour

// ArchiveAfter is the inactivity threshold for stale → archived.
// PRD §6 F4.1 specifies 90 days total (so ~60 days *after* hitting
// stale). We measure from last_used_at, not "time since stale" —
// that way a skill flipped active mid-stale-window resets the
// archive clock too.
const ArchiveAfter = 90 * 24 * time.Hour

// validLifecycleStates is the closed set used to validate inputs
// crossing the API/CLI boundary. Mirrors the migration v102 CHECK.
var validLifecycleStates = map[LifecycleState]struct{}{
	LifecycleActive:     {},
	LifecycleStale:      {},
	LifecycleArchived:   {},
	LifecycleDeprecated: {},
}

// ValidateLifecycleState rejects unknown values at the API/CLI
// boundary. The DB CHECK is the defense in depth; this is the
// helpful-error layer that catches typos before they round-trip to
// SQL and bubble up as opaque constraint errors.
func ValidateLifecycleState(s LifecycleState) error {
	if _, ok := validLifecycleStates[s]; !ok {
		return fmt.Errorf("skills: invalid lifecycle_state %q (want active|stale|archived|deprecated)", s)
	}
	return nil
}

// LifecycleSnapshot is the input shape the pure transition function
// reads. All fields come from a single SELECT against skills +
// COUNT(*) against agent_skills — the caller assembles the snapshot
// and feeds it in. The pure function never touches the DB.
type LifecycleSnapshot struct {
	// Current is the persisted lifecycle_state value.
	Current LifecycleState
	// LastUsedAt is the most recent skill_invocations.invoked_at
	// (denormalised into skills.last_used_at by the F4.1 evaluator).
	// Zero time means "never used".
	LastUsedAt time.Time
	// ActiveAssignments is the count of agent_skills rows whose agent
	// is not soft-deleted and not in lifecycle=ghosted (PR-D).
	// Counted at the SQL layer so the pure function doesn't have to
	// reason about expiry.
	ActiveAssignments int
	// Now is the reference clock. Plumbed in so tests can drive the
	// timer-based transitions deterministically.
	Now time.Time
}

// Transition encodes the result of a lifecycle evaluation. Reason
// is a short human-readable rationale rendered in the UI / journal —
// "stale: unused 35d, no assignments" is more useful than "stale".
type Transition struct {
	// Next is the proposed new state. May equal Current (no-op) —
	// the caller should still log the evaluation for audit.
	Next LifecycleState
	// Reason captures the rule that fired. Empty when Next == Current.
	Reason string
}

// EvaluateTransition runs the F4.1 lifecycle state machine. Pure
// function over the snapshot. Caller responsibilities:
//
//   - Persist the result via a separate UPDATE (this function never
//     writes to the DB).
//   - Skip the call entirely when Snapshot.Current == LifecycleDeprecated;
//     deprecated is terminal and EvaluateTransition refuses to
//     transition it (returns Next == LifecycleDeprecated, empty reason).
//   - Treat unknown LifecycleSnapshot.Current values as active —
//     defensive default in case of schema drift (matches the DB
//     CHECK falling back to 'active' for legacy rows).
//
// The "assignment trumps timer" rule is encoded once at the top so
// the per-state branches don't have to repeat it.
func EvaluateTransition(s LifecycleSnapshot) Transition {
	// Deprecated is terminal. Refuse all transitions out.
	if s.Current == LifecycleDeprecated {
		return Transition{Next: LifecycleDeprecated}
	}

	// Assignment trumps timer: any actively-assigned skill should be
	// LifecycleActive regardless of usage. Covers the
	// "rarely-fired but operator-deliberately-attached" case. If the
	// current state is anything else, propose flipping to active.
	if s.ActiveAssignments > 0 {
		if s.Current != LifecycleActive {
			return Transition{
				Next:   LifecycleActive,
				Reason: fmt.Sprintf("active assignment present (%d agent(s)); assignment trumps usage timer", s.ActiveAssignments),
			}
		}
		return Transition{Next: LifecycleActive}
	}

	// No active assignments → consult the timer.
	age := s.Now.Sub(s.LastUsedAt)
	neverUsed := s.LastUsedAt.IsZero()

	switch s.Current {
	case LifecycleActive:
		// Active → stale once inactivity crosses StaleAfter AND there
		// are no assignments. Never-used skills count as "stale" the
		// moment they cross the timer too (a skill imported but never
		// loaded for 30+ days is a curator signal).
		if neverUsed || age >= StaleAfter {
			return Transition{
				Next:   LifecycleStale,
				Reason: stalingReason(neverUsed, age),
			}
		}
		return Transition{Next: LifecycleActive}

	case LifecycleStale:
		// Stale → archived once total inactivity crosses ArchiveAfter.
		// Measured from last_used_at, not "time spent in stale" —
		// a skill briefly revived back to active and then re-stalled
		// keeps its archive clock anchored on the latest use.
		if neverUsed || age >= ArchiveAfter {
			return Transition{
				Next:   LifecycleArchived,
				Reason: archivingReason(neverUsed, age),
			}
		}
		return Transition{Next: LifecycleStale}

	case LifecycleArchived:
		// Archived is recoverable but not auto-recoverable. We never
		// flip archived → active on usage alone (the skill is hidden
		// from catalog browse so a "usage" implies an operator
		// already poked it; the operator should explicitly recover).
		return Transition{Next: LifecycleArchived}
	}

	// Unknown state (schema drift / manual SQL fix-up): default to
	// active and let the next sweep re-evaluate. Matches the
	// "missing crew → safe-default policy" pattern from policy.Resolver.
	return Transition{Next: LifecycleActive}
}

func stalingReason(neverUsed bool, age time.Duration) string {
	if neverUsed {
		return "stale: imported but never used and no active assignments"
	}
	return fmt.Sprintf("stale: unused %dd and no active assignments", int(age.Hours()/24))
}

func archivingReason(neverUsed bool, age time.Duration) string {
	if neverUsed {
		return "archived: never used since import; >90d inactive"
	}
	return fmt.Sprintf("archived: unused %dd (>90d) and no active assignments", int(age.Hours()/24))
}
