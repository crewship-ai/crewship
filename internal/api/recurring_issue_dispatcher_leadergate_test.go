package api

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// Leader-gate consumption at RecurringIssueDispatcher.tick (#1376).
//
// SetLeaderGate/leaderGate is the multi-replica safety mechanism: when two
// crewshipd replicas both run the recurring-issue dispatcher against the same
// DB, only the lease holder should stamp a due template into a new issue.
// The gate mechanism itself (internal/leader) is well tested elsewhere; what
// was UNTESTED anywhere in the repo is that RecurringIssueDispatcher.tick
// actually *consults* the gate it's given. Before this file, deleting
//
//	if d.leaderGate != nil && !d.leaderGate.IsLeader() { return }
//
// from tick (internal/api/recurring_issue_dispatcher.go) did not fail a
// single test — a due recurring issue would fire on every replica
// regardless of leadership, stamping a duplicate issue per tick interval on
// a multi-replica deploy. These two tests pin that the gate is both
// consulted (false → no issue created) and not over-applied (true → fires
// normally, mirroring TestRecurringIssueDispatcher_FiresDueRow).
// ---------------------------------------------------------------------------

// stubLeaderGate is a minimal leader.Gate stub — IsLeader always returns the
// fixed value it was constructed with.
type stubLeaderGate struct{ leader bool }

func (g stubLeaderGate) IsLeader() bool { return g.leader }

func TestRecurringIssueDispatcher_Tick_NoopWhenNotLeader(t *testing.T) {
	h, _, wsID, crewID := covRIFixture(t)
	seedAgentRow(t, h.db, "lead-lg-1", wsID, crewID, "Lead", "lead-lg-1", "LEAD")
	seedRecurringDue(t, h, "ri-lg-1", wsID, crewID, "*/5 * * * *")

	d := NewRecurringIssueDispatcher(h.db, nil, newTestLogger())
	d.SetLeaderGate(stubLeaderGate{leader: false})
	d.tick(context.Background())

	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM missions WHERE crew_id=? AND mission_type='issue' AND authored_via='recurring'`,
		crewID).Scan(&n); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if n != 0 {
		t.Fatalf("issues created while not leader = %d, want 0 — the leader gate was not consulted", n)
	}

	var runCount int
	if err := h.db.QueryRow(`SELECT run_count FROM recurring_issues WHERE id='ri-lg-1'`).Scan(&runCount); err != nil {
		t.Fatalf("load recurring row: %v", err)
	}
	if runCount != 0 {
		t.Fatalf("run_count = %d while not leader, want 0 (schedule must not advance)", runCount)
	}
}

func TestRecurringIssueDispatcher_Tick_FiresWhenLeader(t *testing.T) {
	h, _, wsID, crewID := covRIFixture(t)
	seedAgentRow(t, h.db, "lead-lg-2", wsID, crewID, "Lead", "lead-lg-2", "LEAD")
	seedRecurringDue(t, h, "ri-lg-2", wsID, crewID, "*/5 * * * *")

	d := NewRecurringIssueDispatcher(h.db, nil, newTestLogger())
	d.SetLeaderGate(stubLeaderGate{leader: true})
	d.tick(context.Background())

	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM missions WHERE crew_id=? AND mission_type='issue' AND authored_via='recurring'`,
		crewID).Scan(&n); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if n != 1 {
		t.Fatalf("issues created while leader = %d, want exactly 1", n)
	}

	var runCount int
	if err := h.db.QueryRow(`SELECT run_count FROM recurring_issues WHERE id='ri-lg-2'`).Scan(&runCount); err != nil {
		t.Fatalf("load recurring row: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("run_count = %d while leader, want exactly 1", runCount)
	}
}
