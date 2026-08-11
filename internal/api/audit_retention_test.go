package api

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// credential_audit and audit_logs were the only tables in the schema with no
// pruning at all. These tests pin the sweep, and — more importantly — pin the
// two defaults apart, because the failure mode of getting them wrong is silent
// in opposite directions: too eager on audit_logs deletes compliance records
// nobody meant to lose, too lax on credential_audit is the unbounded growth
// this work exists to stop.

func auditRetentionDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return db, wsID
}

// seedAuditRows writes n rows into each audit table at the given age in days.
func seedAuditRows(t *testing.T, db *sql.DB, wsID string, ageDays, n int, tag string) {
	t.Helper()
	ts := tsformat.Format(time.Now().Add(-time.Duration(ageDays) * 24 * time.Hour).UTC())
	credID := "cred-ret-" + tag
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO credentials (id, workspace_id, name, encrypted_value, created_by)
		 VALUES (?, ?, ?, 'enc', (SELECT id FROM users LIMIT 1))`,
		credID, wsID, "KEY_"+tag); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := db.Exec(
			`INSERT INTO credential_audit (id, credential_id, workspace_id, event_type, occurred_at)
			 VALUES (?, ?, ?, 'USE', ?)`,
			fmt.Sprintf("ca-%s-%d", tag, i), credID, wsID, ts); err != nil {
			t.Fatalf("seed credential_audit: %v", err)
		}
		if _, err := db.Exec(
			`INSERT INTO audit_logs (id, workspace_id, action, entity_type, created_at)
			 VALUES (?, ?, 'update', 'agent', ?)`,
			fmt.Sprintf("al-%s-%d", tag, i), wsID, ts); err != nil {
			t.Fatalf("seed audit_logs: %v", err)
		}
	}
}

func auditRetentionCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	//nolint:gosec // table is a test-local constant
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestAuditRetentionDefaultsDifferByTable is the heart of it: with no
// per-workspace override, old credential_audit rows go and old audit_logs
// rows stay. A single shared default — in either direction — would break one
// of these two assertions, which is exactly why they are asserted together.
func TestAuditRetentionDefaultsDifferByTable(t *testing.T) {
	t.Parallel()
	db, wsID := auditRetentionDB(t)

	seedAuditRows(t, db, wsID, 200, 3, "old")   // well past 90 days
	seedAuditRows(t, db, wsID, 10, 2, "recent") // well inside it

	if err := SweepAllWorkspacesAuditRetention(context.Background(), db, auditLogger()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if got := auditRetentionCount(t, db, "credential_audit"); got != 2 {
		t.Errorf("credential_audit = %d rows, want 2 (the recent pair; 200-day-old rows are past the %d-day default)",
			got, DefaultCredentialAuditRetentionDays)
	}
	if got := auditRetentionCount(t, db, "audit_logs"); got != 5 {
		t.Errorf("audit_logs = %d rows, want all 5 — it is the compliance trail and defaults to keeping everything; "+
			"deleting it without the operator setting a window is the footgun this default exists to avoid", got)
	}
}

// TestAuditRetentionHonoursPerWorkspaceOverrides covers the opt-in: an
// operator who sets a window gets one, including on audit_logs.
func TestAuditRetentionHonoursPerWorkspaceOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		credDays      any
		logDays       any
		wantCredRows  int
		wantAuditRows int
	}{
		{"no overrides", nil, nil, 2, 5},
		{"audit_logs opted in", nil, 30, 2, 2},
		{"credential window widened past the rows", 365, nil, 5, 5},
		{"both tightened", 30, 30, 2, 2},
		{"zero means keep forever", 0, 0, 5, 5},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, wsID := auditRetentionDB(t)
			seedAuditRows(t, db, wsID, 200, 3, "old")
			seedAuditRows(t, db, wsID, 10, 2, "recent")

			if _, err := db.Exec(
				`UPDATE workspaces SET credential_audit_retention_days = ?, audit_log_retention_days = ? WHERE id = ?`,
				tc.credDays, tc.logDays, wsID); err != nil {
				t.Fatalf("set overrides: %v", err)
			}

			if err := SweepAllWorkspacesAuditRetention(context.Background(), db, auditLogger()); err != nil {
				t.Fatalf("sweep: %v", err)
			}
			if got := auditRetentionCount(t, db, "credential_audit"); got != tc.wantCredRows {
				t.Errorf("credential_audit = %d, want %d", got, tc.wantCredRows)
			}
			if got := auditRetentionCount(t, db, "audit_logs"); got != tc.wantAuditRows {
				t.Errorf("audit_logs = %d, want %d", got, tc.wantAuditRows)
			}
		})
	}
}

// TestAuditRetentionIsIdempotent — the sweeper runs daily forever, and a
// second pass over rows it already handled must be a no-op rather than a
// second round of deletion (or an error).
func TestAuditRetentionIsIdempotent(t *testing.T) {
	t.Parallel()
	db, wsID := auditRetentionDB(t)
	seedAuditRows(t, db, wsID, 200, 3, "old")
	seedAuditRows(t, db, wsID, 10, 2, "recent")

	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if err := SweepAllWorkspacesAuditRetention(ctx, db, auditLogger()); err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
		if got := auditRetentionCount(t, db, "credential_audit"); got != 2 {
			t.Fatalf("after sweep %d: credential_audit = %d, want 2", i, got)
		}
	}
}

// TestAuditRetentionSweepsUnattributedRows covers the rows a per-workspace
// pass can never reach. credential_audit.workspace_id is nullable (added by
// 20260810153104 and backfilled), so a row whose parent credential was gone
// at backfill time has no tenant — and without this pass it would live
// forever, which is precisely the growth being fixed.
func TestAuditRetentionSweepsUnattributedRows(t *testing.T) {
	t.Parallel()
	db, wsID := auditRetentionDB(t)
	seedAuditRows(t, db, wsID, 200, 2, "orphan")

	if _, err := db.Exec(`UPDATE credential_audit SET workspace_id = NULL`); err != nil {
		t.Fatalf("orphan the rows: %v", err)
	}

	if err := SweepAllWorkspacesAuditRetention(context.Background(), db, auditLogger()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := auditRetentionCount(t, db, "credential_audit"); got != 0 {
		t.Errorf("credential_audit = %d rows, want 0 — unattributed rows belong to no workspace, so nothing else would ever remove them", got)
	}
}

// TestAuditRetentionBatchingDrainsABacklog exercises the bounded-DELETE loop
// past a single batch. A backlog larger than one batch must still drain
// within a tick, or the first sweep on a long-lived instance would never
// catch up.
func TestAuditRetentionBatchingDrainsABacklog(t *testing.T) {
	t.Parallel()
	db, wsID := auditRetentionDB(t)

	// One more than a batch, so the loop is forced round twice.
	const n = auditRetentionBatchRows + 25
	ts := tsformat.Format(time.Now().Add(-200 * 24 * time.Hour).UTC())
	if _, err := db.Exec(
		`INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by)
		 VALUES ('cred-bulk', ?, 'BULK', 'enc', (SELECT id FROM users LIMIT 1))`, wsID); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO credential_audit (id, credential_id, workspace_id, event_type, occurred_at) VALUES (?, 'cred-bulk', ?, 'USE', ?)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := stmt.Exec(fmt.Sprintf("ca-bulk-%d", i), wsID, ts); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := SweepAllWorkspacesAuditRetention(context.Background(), db, auditLogger()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := auditRetentionCount(t, db, "credential_audit"); got != 0 {
		t.Errorf("credential_audit = %d rows, want 0 — a backlog spanning more than one batch must still drain in a tick", got)
	}
}

// TestAuditRetentionStopsOnContextCancel — the sweeper runs under the
// daemon's lifetime context, and a shutdown must not have to wait out a
// hundred batches.
func TestAuditRetentionStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	db, wsID := auditRetentionDB(t)
	seedAuditRows(t, db, wsID, 200, 2, "cancel")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := SweepAllWorkspacesAuditRetention(ctx, db, auditLogger())
	if err == nil {
		t.Fatal("sweep returned nil on a cancelled context")
	}
	if !isContextCancelled(err) {
		t.Errorf("error = %v, want a context cancellation", err)
	}
}

func isContextCancelled(err error) bool {
	for err != nil {
		if err == context.Canceled {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
