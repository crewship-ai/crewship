package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Fix #1 (creator attribution): a fired issue must be stamped with the
// recurring template's created_by user, so the API surfaces the creator
// instead of omitting it (both-NULL → omitted per v129 semantics). RED
// before the fix: the dispatcher never threads created_by, so
// created_by_user_id is NULL.
func TestRecurringIssueDispatcher_StampsCreator(t *testing.T) {
	h, userID, wsID, crewID := covRIFixture(t)
	seedAgentRow(t, h.db, "lead-cr", wsID, crewID, "Lead", "lead-cr", "LEAD")
	execOrFatal(t, h.db, `INSERT INTO recurring_issues
		(id, workspace_id, crew_id, title, cron_expression, enabled, next_run, run_count, created_at, created_by)
		VALUES ('ri-cr', ?, ?, 'Attributed', '*/5 * * * *', 1, '2020-01-01T00:00:00Z', 0, datetime('now'), ?)`,
		wsID, crewID, userID)

	d := NewRecurringIssueDispatcher(h.db, nil, newTestLogger())
	d.tick(context.Background())

	var createdBy sql.NullString
	if err := h.db.QueryRow(
		`SELECT created_by_user_id FROM missions WHERE crew_id=? AND authored_via='recurring'`,
		crewID).Scan(&createdBy); err != nil {
		t.Fatalf("load fired issue: %v", err)
	}
	if !createdBy.Valid || createdBy.String != userID {
		t.Errorf("created_by_user_id = %q (valid=%v), want %q", createdBy.String, createdBy.Valid, userID)
	}
}

// Fix #2 (fire idempotency): two fires of the SAME occurrence (same
// next_run bucket) must create exactly one issue; a distinct occurrence
// creates another. Guards against duplicate issues when two replicas run
// the ticker concurrently. RED before the fix: no durable key, so both
// fires insert.
func TestRecurringIssueDispatcher_FireIdempotent(t *testing.T) {
	ctx := context.Background()
	h, _, wsID, crewID := covRIFixture(t)
	seedAgentRow(t, h.db, "lead-id", wsID, crewID, "Lead", "lead-id", "LEAD")

	row := recurringDueRow{
		id:            "ri-idem",
		workspaceID:   wsID,
		crewID:        crewID,
		title:         "Idem",
		cronExpr:      "*/5 * * * *",
		nextRunBucket: "2020-01-01T00:00:00Z",
	}
	// The row must exist so the same-tx schedule advance can UPDATE it.
	execOrFatal(t, h.db, `INSERT INTO recurring_issues
		(id, workspace_id, crew_id, title, cron_expression, enabled, next_run, run_count, created_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, 0, datetime('now'))`,
		row.id, wsID, crewID, row.title, row.cronExpr, row.nextRunBucket)

	d := NewRecurringIssueDispatcher(h.db, nil, newTestLogger())

	d.fireOne(ctx, row) // reserves the occurrence key + inserts
	d.fireOne(ctx, row) // same bucket → deduped, no insert

	count := func() int {
		var n int
		if err := h.db.QueryRow(
			`SELECT COUNT(*) FROM missions WHERE crew_id=? AND authored_via='recurring'`,
			crewID).Scan(&n); err != nil {
			t.Fatalf("count issues: %v", err)
		}
		return n
	}
	if got := count(); got != 1 {
		t.Fatalf("same-occurrence double fire created %d issues, want 1", got)
	}

	// A distinct occurrence (different bucket) is a new fire.
	row2 := row
	row2.nextRunBucket = "2020-01-02T00:00:00Z"
	d.fireOne(ctx, row2)
	if got := count(); got != 2 {
		t.Fatalf("distinct occurrence created total %d issues, want 2", got)
	}
}

// Fix #3 (timezone consistency): Create must compute the first next_run in
// UTC, matching the dispatcher's UTC advance. On a non-UTC server the
// wall-clock (time.Now()) computation diverges from the UTC one. RED
// before the fix: schedule.Next(time.Now()) yields the local-interpreted
// occurrence.
func TestRecurringIssueCreate_NextRunIsUTC(t *testing.T) {
	// Force a non-UTC local zone so a wall-clock computation would land on
	// a different absolute instant than the UTC one.
	origLocal := time.Local
	time.Local = time.FixedZone("TEST+05", 5*3600)
	t.Cleanup(func() { time.Local = origLocal })

	h, userID, wsID, crewID := covRIFixture(t)

	const cronExpr = "0 9 * * *" // daily 09:00 — TZ-sensitive
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/recurring-issues", jsonBody(map[string]any{
			"crew_id": crewID, "title": "T", "cron_expression": cronExpr,
		})),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	got, _ := resp["next_run"].(string)

	sched, err := cronParser.Parse(cronExpr)
	if err != nil {
		t.Fatalf("parse cron: %v", err)
	}
	wantUTC := sched.Next(time.Now().UTC()).UTC().Format(time.RFC3339)
	if got != wantUTC {
		t.Errorf("next_run = %q, want UTC-computed %q (Create used wall-clock instead of UTC)", got, wantUTC)
	}
}
