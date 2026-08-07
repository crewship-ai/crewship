package api

// The volume bound on routine-raised escalations.
//
// WHY THIS IS NOT A MATRIX CELL. policy.ActionEscalationCreate is a flat row:
// every autonomy level may raise an escalation, because the two inbox-shaped
// decisions are self-defeating for this action (an approval to authorise an
// interrupt IS the interrupt; a notice about an escalation duplicates it) and
// because refusing it at strict would forbid asking for help at the level whose
// whole purpose is to ask before acting — while an unpoliced `notify` step
// reaches the same human anyway.
//
// That leaves the risk the matrix cannot express. An escalation costs a HUMAN's
// attention — a PENDING row plus a BLOCKING, high-priority inbox item for every
// MANAGER (escalation_handler.go) — and the autonomy dial decides per call, not
// per hour. A `foreach` over 500 issues raising one escalation each passes every
// cell in the matrix and takes the operator's inbox out. A cell claiming to
// bound that would be the cap believed to cover more than it does, which
// delegation_limits.go already names as worse than a missing one.
//
// WHAT IS BOUNDED. Not a rate but a BACKLOG: how many unresolved escalations one
// crew may have outstanding before a routine may add another. That is the
// resource being protected — unanswered demands on a person — rather than a
// proxy for it, and it is self-healing: as an operator resolves them the budget
// returns, with no timer and nothing to reset. A rate limit would have had to
// pick a window, and every window is wrong for something that is sometimes
// nightly and sometimes hourly.
//
// The count includes escalations an AGENT raised, deliberately. The queue is
// full either way; a routine adding the 21st is adding to the same pile
// regardless of who made the first twenty.
//
// WHICH DOOR THIS COVERS. One: the `crewship` routine step. POST
// /api/v1/internal/escalations is unchanged for agents, and that is a choice
// rather than an oversight — an agent escalating is a live session reporting it
// is stuck, and refusing that strands the work with nobody told. A routine is a
// for-loop; it is the door that just opened, and it is the one that needs a
// number. Stated rather than implied, per delegation_limits.go's rule about
// naming what a cap does not cover.

import (
	"context"
	"database/sql"
	"fmt"
)

// SettingEscalationMaxPendingPerCrew bounds how many unresolved escalations a
// crew may have before a ROUTINE may raise another. 0 switches routine-raised
// escalations off for the instance — the one number an operator sets for "no
// routine pages me" — while leaving agents' escalations untouched.
const SettingEscalationMaxPendingPerCrew = "escalation.max_pending_per_crew"

const (
	// 10 unresolved escalations per crew. The number is about a person, not a
	// machine, so it is not derived from the delegation or mission caps: it is
	// how many open decisions one operator can hold in a triage session before
	// the queue itself is the problem and the next escalation is noise. For
	// scale, the largest seeded crew is 3 agents — ten outstanding requests
	// from three agents' worth of work is already a backlog, not a burst.
	defaultEscalationMaxPendingPerCrew = 10

	// Past this a stored value is a typo rather than a policy, so settingInt
	// answers the compiled default instead of running the gate on a number
	// nobody chose — the same convention the mission and delegation caps use.
	maxEscalationPendingCeiling = 500
)

// escalationCapRefusal is the cap saying no, in words a routine author can act
// on, naming both levers: resolve the queue, or raise the setting.
type escalationCapRefusal struct {
	msg     string
	Setting string
	Limit   int
	Pending int
}

func (e *escalationCapRefusal) Error() string { return e.msg }

// countPendingEscalations returns how many unresolved escalations the crew
// already has — the number the budget is compared against.
//
// The status test is a NOT IN over the TERMINAL statuses rather than an IN over
// PENDING, and the direction matters: RESOLVED is the only terminal value the
// table has today (escalation_handler.go and escalation_autoresolve.go are the
// only writers), so a status added later counts as OUTSTANDING and the cap fails
// closed instead of silently leaving the budget. Same reasoning
// missionLiveStatusFilter records for the mission caps.
func countPendingEscalations(ctx context.Context, db *sql.DB, crewID string) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM escalations
		WHERE crew_id = ? AND status NOT IN ('RESOLVED')`,
		crewID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count pending escalations: %w", err)
	}
	return n, nil
}

// enforceRoutineEscalationCap judges one routine-raised escalation against the
// live budget.
//
// Fails CLOSED on a read error: a cap that cannot read its own state has not
// established that this escalation is inside it, and paging a human anyway is
// the unbounded behaviour it exists to end. A nil DB is the wiring case, not a
// data case, and also refuses — the dispatcher is only ever built with one.
func enforceRoutineEscalationCap(ctx context.Context, db *sql.DB, crewID string) error {
	if db == nil {
		return fmt.Errorf("escalation cap: no database wired — refusing to raise an "+
			"unbounded escalation (setting %s)", SettingEscalationMaxPendingPerCrew)
	}
	limit := settingInt(ctx, db, SettingEscalationMaxPendingPerCrew,
		defaultEscalationMaxPendingPerCrew, 0, maxEscalationPendingCeiling)

	pending, err := countPendingEscalations(ctx, db, crewID)
	if err != nil {
		return err
	}
	if pending < limit {
		return nil
	}
	if limit == 0 {
		return &escalationCapRefusal{
			Setting: SettingEscalationMaxPendingPerCrew,
			Limit:   0,
			Pending: pending,
			msg: fmt.Sprintf(
				"routine-raised escalations are switched off on this instance (%s = 0). "+
					"Report the problem with a notify step instead, or ask an operator to raise %s.",
				SettingEscalationMaxPendingPerCrew, SettingEscalationMaxPendingPerCrew),
		}
	}
	return &escalationCapRefusal{
		Setting: SettingEscalationMaxPendingPerCrew,
		Limit:   limit,
		Pending: pending,
		msg: fmt.Sprintf(
			"escalation refused: this crew already has %d unresolved escalation(s) and the "+
				"limit for routine-raised ones is %d (instance setting %s). Resolve the queue "+
				"— `crewship escalations list` — before a routine adds to it.",
			pending, limit, SettingEscalationMaxPendingPerCrew),
	}
}
