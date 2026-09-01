package harbormaster

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"
)

// seedApprovalRow inserts one approvals_queue row directly, bypassing
// Enqueue/Decide so a test can pin an arbitrary decided_at without waiting
// on real time. ageDays is measured from now; a nil status.decided means
// the row stays 'pending' with no decided_at, mirroring a row still
// awaiting a human.
type seedRow struct {
	id, workspaceID, status string
	decidedAgeDays          int // ignored when status == "pending"
	// kind defaults to KindToolCall when empty, matching every existing
	// caller — only tests that specifically care about kind need to set it.
	kind Kind
}

func seedApprovalRows(t *testing.T, db *sql.DB, rows []seedRow) {
	t.Helper()
	for _, r := range rows {
		var decidedAt any
		if r.status != "pending" {
			decidedAt = time.Now().UTC().
				Add(-time.Duration(r.decidedAgeDays) * 24 * time.Hour).
				Format(timeFmt)
		}
		kind := r.kind
		if kind == "" {
			kind = KindToolCall
		}
		if _, err := db.Exec(
			`INSERT INTO approvals_queue
				(id, workspace_id, requested_by, kind, reason, status, decided_at, created_at)
			 VALUES (?, ?, 'u1', ?, 'because', ?, ?, datetime('now'))`,
			r.id, r.workspaceID, string(kind), r.status, decidedAt,
		); err != nil {
			t.Fatalf("seed approval row %s: %v", r.id, err)
		}
	}
}

func approvalRowIDs(t *testing.T, db *sql.DB, workspaceID string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM approvals_queue WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		t.Fatalf("query approval ids: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan approval id: %v", err)
		}
		out[id] = true
	}
	return out
}

// TestSweepApprovalsRetention_DeletesAgedTerminalRowsSparesFreshAndPending
// pins the core behaviour: an old terminal row goes, a recent terminal row
// and ANY pending row (however old) survive — a pending row is never
// eligible no matter its age, because deleting it out from under an agent
// still waiting on the decision would strand the wait instead of failing it
// deterministically (that is the timeout sweeper's job, not retention's).
func TestSweepApprovalsRetention_DeletesAgedTerminalRowsSparesFreshAndPending(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedApprovalRows(t, db, []seedRow{
		{id: "old-approved", workspaceID: "ws_test", status: "approved", decidedAgeDays: 120},
		{id: "old-denied", workspaceID: "ws_test", status: "denied", decidedAgeDays: 200},
		{id: "old-timeout", workspaceID: "ws_test", status: "timeout", decidedAgeDays: 91},
		{id: "old-cancelled", workspaceID: "ws_test", status: "cancelled", decidedAgeDays: 400},
		{id: "fresh-approved", workspaceID: "ws_test", status: "approved", decidedAgeDays: 5},
		{id: "still-pending", workspaceID: "ws_test", status: "pending"},
	})
	// A pending row's created_at is deliberately old too — the sweep must
	// not use created_at as a fallback cutoff for it.
	if _, err := db.Exec(
		`UPDATE approvals_queue SET created_at = datetime('now', '-365 days') WHERE id = 'still-pending'`,
	); err != nil {
		t.Fatalf("age the pending row: %v", err)
	}

	deleted, capped, err := SweepApprovalsRetention(context.Background(), db, "ws_test", DefaultApprovalsRetentionDays)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if capped {
		t.Fatalf("sweep reported capped on a 4-row backlog")
	}
	if deleted != 4 {
		t.Fatalf("deleted = %d, want 4 (old-approved, old-denied, old-timeout, old-cancelled)", deleted)
	}

	remaining := approvalRowIDs(t, db, "ws_test")
	for _, gone := range []string{"old-approved", "old-denied", "old-timeout", "old-cancelled"} {
		if remaining[gone] {
			t.Errorf("%s should have been swept, still present", gone)
		}
	}
	for _, kept := range []string{"fresh-approved", "still-pending"} {
		if !remaining[kept] {
			t.Errorf("%s should have survived the sweep, was deleted", kept)
		}
	}
}

// TestSweepApprovalsRetention_SparesAutonomyGateRows pins the security fix
// for the regression CodeRabbit flagged on #2254: a terminal
// kind=autonomy_gate row is the ONLY thing stopping POST
// .../missions/{id}/start from dispatching a mission that was denied (or
// timed out) under an autonomy hold — see autonomyGateApproved in
// internal/api/internal_autonomy_gate.go and the hasHold-not-approved 403
// in internal/api/missions_internal.go. Sweeping it on the ordinary 90-day
// terminal-row clock reintroduces "an unattended hold turning into a green
// light", just on a delay. An ordinary terminal row of another kind, aged
// the same amount, must still be swept — the carve-out is scoped to
// autonomy_gate specifically, not a blanket exemption.
func TestSweepApprovalsRetention_SparesAutonomyGateRows(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedApprovalRows(t, db, []seedRow{
		{id: "old-gate-denied", workspaceID: "ws_test", status: "denied", decidedAgeDays: 120, kind: KindAutonomyGate},
		{id: "old-gate-timeout", workspaceID: "ws_test", status: "timeout", decidedAgeDays: 400, kind: KindAutonomyGate},
		{id: "old-toolcall", workspaceID: "ws_test", status: "denied", decidedAgeDays: 120, kind: KindToolCall},
	})

	deleted, capped, err := SweepApprovalsRetention(context.Background(), db, "ws_test", DefaultApprovalsRetentionDays)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if capped {
		t.Fatalf("sweep reported capped on a 3-row backlog")
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (old-toolcall only)", deleted)
	}

	remaining := approvalRowIDs(t, db, "ws_test")
	for _, held := range []string{"old-gate-denied", "old-gate-timeout"} {
		if !remaining[held] {
			t.Errorf("%s is an autonomy_gate row and must survive the sweep regardless of age — it is the sole gate on mission start, not history", held)
		}
	}
	if remaining["old-toolcall"] {
		t.Errorf("old-toolcall is an ordinary terminal row and should have been swept")
	}
}

// TestSweepApprovalsRetention_RetentionDaysLEZeroDeletesNothing pins that
// this function itself treats <= 0 as "delete nothing" — the "resolve to
// the default" behaviour lives one layer up, in
// SweepAllWorkspacesApprovalsRetention, so a caller passing a raw column
// value here (e.g. from a test, or a future direct caller) can't
// accidentally nuke a workspace that meant "no opinion" as "no window".
func TestSweepApprovalsRetention_RetentionDaysLEZeroDeletesNothing(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedApprovalRows(t, db, []seedRow{
		{id: "ancient", workspaceID: "ws_test", status: "approved", decidedAgeDays: 5000},
	})

	for _, days := range []int{0, -1} {
		deleted, _, err := SweepApprovalsRetention(context.Background(), db, "ws_test", days)
		if err != nil {
			t.Fatalf("sweep(days=%d): %v", days, err)
		}
		if deleted != 0 {
			t.Fatalf("sweep(days=%d) deleted %d rows, want 0", days, deleted)
		}
	}
}

// TestSweepAllWorkspacesApprovalsRetention_ResolvesNullVsZeroVsPositive
// pins how the per-workspace override column resolves, matching
// credential_audit_retention_days / audit_log_retention_days rather than
// run_retention_days: NULL (no opinion recorded) falls back to the 90-day
// default, an explicit 0 means keep forever (never swept, regardless of
// age), and a positive override is honoured exactly. A negative value is
// rejected by the API layer (workspaces_mutate.go) so it should never reach
// this column in practice, but is asserted here too: it fails safe as
// "delete nothing" rather than being coerced into some other window.
func TestSweepAllWorkspacesApprovalsRetention_ResolvesNullVsZeroVsPositive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		override  any // nil, 0, negative, or a positive override
		ageDays   int
		wantSwept bool
	}{
		{"NULL override falls back to the 90-day default", nil, 120, true},
		{"zero override means keep forever, not the default", 0, 120, false},
		{"negative override fails safe as delete-nothing", -5, 120, false},
		{"positive override narrower than the row's age sweeps it", 10, 30, true},
		{"positive override wider than the row's age spares it", 365, 120, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := openTestDB(t)
			if _, err := db.Exec(`UPDATE workspaces SET approvals_retention_days = ? WHERE id = 'ws_test'`, tc.override); err != nil {
				t.Fatalf("set override: %v", err)
			}
			seedApprovalRows(t, db, []seedRow{
				{id: "row1", workspaceID: "ws_test", status: "denied", decidedAgeDays: tc.ageDays},
			})

			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			if err := SweepAllWorkspacesApprovalsRetention(context.Background(), db, logger); err != nil {
				t.Fatalf("sweep all: %v", err)
			}

			remaining := approvalRowIDs(t, db, "ws_test")
			if tc.wantSwept && remaining["row1"] {
				t.Errorf("row1 should have been swept, still present")
			}
			if !tc.wantSwept && !remaining["row1"] {
				t.Errorf("row1 should have survived, was deleted")
			}
		})
	}
}

// TestSweepApprovalsRetention_WorkspaceScoped pins tenant isolation: a
// sweep for one workspace must never touch another workspace's rows, aged
// or not.
func TestSweepApprovalsRetention_WorkspaceScoped(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id) VALUES ('ws_other')`); err != nil {
		t.Fatalf("seed ws_other: %v", err)
	}
	seedApprovalRows(t, db, []seedRow{
		{id: "ws-test-old", workspaceID: "ws_test", status: "approved", decidedAgeDays: 500},
		{id: "ws-other-old", workspaceID: "ws_other", status: "approved", decidedAgeDays: 500},
	})

	if _, _, err := SweepApprovalsRetention(context.Background(), db, "ws_test", 90); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if approvalRowIDs(t, db, "ws_test")["ws-test-old"] {
		t.Errorf("ws_test's own aged row should have been swept")
	}
	if !approvalRowIDs(t, db, "ws_other")["ws-other-old"] {
		t.Errorf("ws_other's row was deleted by a sweep scoped to ws_test")
	}
}

// TestSweepApprovalsRetention_RejectsOverflowingRetentionDays pins the
// boundary CodeRabbit flagged on #2254: time.Duration is int64 nanoseconds,
// so retentionDays*24h overflows past MaxApprovalsRetentionDays and wraps
// NEGATIVE. now.Add(-negative) then moves `cutoff` into the FUTURE, and
// every terminal row — however recent — matches `decided_at < cutoff`,
// turning "keep for N days" into "delete everything". The API write path
// (internal/api/workspaces_mutate.go) is supposed to reject this before it
// is ever persisted; this pins the function's own defense in case a bad
// value reaches it some other way (a raw call, a value set before the API
// guard existed, direct DB manipulation).
func TestSweepApprovalsRetention_RejectsOverflowingRetentionDays(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedApprovalRows(t, db, []seedRow{
		{id: "fresh", workspaceID: "ws_test", status: "approved", decidedAgeDays: 1},
	})

	deleted, capped, err := SweepApprovalsRetention(context.Background(), db, "ws_test", MaxApprovalsRetentionDays+1)
	if err == nil {
		t.Fatal("sweep with retentionDays = MaxApprovalsRetentionDays+1 succeeded, want an error refusing the overflow-prone value")
	}
	if deleted != 0 || capped {
		t.Errorf("deleted=%d capped=%v on a refused sweep, want 0/false", deleted, capped)
	}
	if !approvalRowIDs(t, db, "ws_test")["fresh"] {
		t.Error("a one-day-old row was deleted by a retentionDays value that should have been refused before computing any cutoff")
	}
}

// TestSweepApprovalsRetention_AcceptsMaxRetentionDays pins the other edge of
// the same boundary: the maximum itself must still work normally rather
// than being refused by an off-by-one in the guard.
func TestSweepApprovalsRetention_AcceptsMaxRetentionDays(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	seedApprovalRows(t, db, []seedRow{
		{id: "fresh", workspaceID: "ws_test", status: "approved", decidedAgeDays: 1},
	})

	if _, _, err := SweepApprovalsRetention(context.Background(), db, "ws_test", MaxApprovalsRetentionDays); err != nil {
		t.Fatalf("sweep at MaxApprovalsRetentionDays: %v", err)
	}
	if !approvalRowIDs(t, db, "ws_test")["fresh"] {
		t.Error("a one-day-old row was deleted by a ~292-year retention window")
	}
}
