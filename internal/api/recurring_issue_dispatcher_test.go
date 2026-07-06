package api

import (
	"context"
	"database/sql"
	"testing"
)

// seedRecurringDue inserts an enabled recurring issue whose next_run is far in
// the past (so it's due now).
func seedRecurringDue(t *testing.T, h *RecurringIssueHandler, id, wsID, crewID, cron string) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO recurring_issues
		(id, workspace_id, crew_id, title, description, priority, cron_expression, enabled, next_run, run_count, created_at)
		VALUES (?, ?, ?, 'Weekly chores', 'do the thing', 'high', ?, 1, '2020-01-01T00:00:00Z', 0, datetime('now'))`,
		id, wsID, crewID, cron)
}

// The dispatcher must fire a due recurring issue: create a real issue (a
// mission_type='issue' row, authored_via='recurring') and advance the schedule
// (next_run forward, last_run set, run_count incremented). BROKEN on main — no
// consumer exists, so zero issues are created. RED.
func TestRecurringIssueDispatcher_FiresDueRow(t *testing.T) {
	h, _, wsID, crewID := covRIFixture(t)
	seedAgentRow(t, h.db, "lead-1", wsID, crewID, "Lead", "lead-1", "LEAD")
	seedRecurringDue(t, h, "ri-1", wsID, crewID, "*/5 * * * *")

	d := NewRecurringIssueDispatcher(h.db, nil, newTestLogger())
	d.tick(context.Background())

	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM missions WHERE crew_id=? AND mission_type='issue' AND authored_via='recurring'`,
		crewID).Scan(&n); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if n != 1 {
		t.Fatalf("recurring issues created = %d, want 1", n)
	}

	// The created issue carries the recurring row's fields.
	var title, priority string
	var desc sql.NullString
	if err := h.db.QueryRow(
		`SELECT title, priority, description FROM missions WHERE crew_id=? AND authored_via='recurring'`,
		crewID).Scan(&title, &priority, &desc); err != nil {
		t.Fatalf("load issue: %v", err)
	}
	if title != "Weekly chores" || priority != "high" || desc.String != "do the thing" {
		t.Errorf("issue fields = (%q,%q,%q), want (Weekly chores, high, do the thing)", title, priority, desc.String)
	}

	// The schedule advanced.
	var nextRun, lastRun sql.NullString
	var runCount int
	if err := h.db.QueryRow(
		`SELECT next_run, last_run, run_count FROM recurring_issues WHERE id='ri-1'`).
		Scan(&nextRun, &lastRun, &runCount); err != nil {
		t.Fatalf("load recurring row: %v", err)
	}
	if runCount != 1 {
		t.Errorf("run_count = %d, want 1", runCount)
	}
	if !lastRun.Valid || lastRun.String == "" {
		t.Error("last_run should be set after firing")
	}
	if !nextRun.Valid || nextRun.String <= "2020-01-01T00:00:00Z" {
		t.Errorf("next_run = %q, want advanced past the due time", nextRun.String)
	}
}

// A disabled recurring issue must NOT fire.
func TestRecurringIssueDispatcher_SkipsDisabled(t *testing.T) {
	h, _, wsID, crewID := covRIFixture(t)
	seedAgentRow(t, h.db, "lead-2", wsID, crewID, "Lead", "lead-2", "LEAD")
	execOrFatal(t, h.db, `INSERT INTO recurring_issues
		(id, workspace_id, crew_id, title, cron_expression, enabled, next_run, run_count, created_at)
		VALUES ('ri-off', ?, ?, 'Off', '*/5 * * * *', 0, '2020-01-01T00:00:00Z', 0, datetime('now'))`,
		wsID, crewID)

	d := NewRecurringIssueDispatcher(h.db, nil, newTestLogger())
	d.tick(context.Background())

	var n int
	h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE crew_id=? AND authored_via='recurring'`, crewID).Scan(&n)
	if n != 0 {
		t.Fatalf("disabled recurring issue fired %d times, want 0", n)
	}
}

// A recurring issue with an unparseable cron is disabled (not spun on).
func TestRecurringIssueDispatcher_BadCronDisabled(t *testing.T) {
	h, _, wsID, crewID := covRIFixture(t)
	seedAgentRow(t, h.db, "lead-3", wsID, crewID, "Lead", "lead-3", "LEAD")
	execOrFatal(t, h.db, `INSERT INTO recurring_issues
		(id, workspace_id, crew_id, title, cron_expression, enabled, next_run, run_count, created_at)
		VALUES ('ri-bad', ?, ?, 'Bad', 'not a cron', 1, '2020-01-01T00:00:00Z', 0, datetime('now'))`,
		wsID, crewID)

	d := NewRecurringIssueDispatcher(h.db, nil, newTestLogger())
	d.tick(context.Background())

	var enabled int
	h.db.QueryRow(`SELECT enabled FROM recurring_issues WHERE id='ri-bad'`).Scan(&enabled)
	if enabled != 0 {
		t.Errorf("bad-cron recurring issue enabled = %d, want 0 (disabled to stop spinning)", enabled)
	}
}
