package api

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
	_ "modernc.org/sqlite"
)

// TestJournalChain_MissionDeleteBreaksChain_1482 pins the root cause of #1482.
//
// IT ASSERTS THE BUG, NOT THE FIX. The chain is EXPECTED to break here today.
// When #1482 is fixed this test starts failing — that is the signal to flip the
// assertion, not to delete the test.
//
// Why it is not fixed in the same commit: removing the offending FK action
// needs journal_entries rebuilt, and migrate.go's v89 comment documents that a
// table rebuild does not work inside Migrate()'s wrapper transaction on a
// populated database (DROP fires dependents' FKs and queues deferred violations
// COMMIT then refuses; foreign_keys can only be toggled in autocommit mode, and
// the framework already holds the tx). Restoring mission_id from refs after the
// fact is also blocked: the FK is still declared, so writing back an id whose
// missions row is gone violates it. That makes this a deliberate schema
// decision on the audit table, not a drive-by fix.
//
// The proof of the root cause of #1482:
// a FOREIGN KEY action silently rewrites rows of the tamper-evident audit log.
//
//	journal_entries.mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL
//
// Deleting a mission therefore UPDATEs every journal row that referenced it,
// setting mission_id to NULL. mission_id is one of the fields the chain hash
// commits to, so those rows stop verifying — reported as
// "entry was modified after write", which is exactly what happened, by a
// constraint rather than an attacker.
//
// This is why stage cannot be repaired by reseeding: `seed --nuke` deletes the
// missions, and every nuke nulls another generation of references. Observed on
// stage 2026-07-27: 396 broken rows (up from 306 when #1482 was filed, growing
// with each reseed), every one a mission-referencing entry type, every one with
// mission_id NULL at the column while its `refs` JSON still carries the id —
// precisely the fingerprint of SET NULL.
func TestJournalChain_MissionDeleteBreaksChain_1482(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key-0123456789abcdef") //gitleaks:allow — fake test fixture key

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	if err := database.Migrate(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// SQLite only enforces foreign keys — and therefore only performs their
	// actions — when this is on. The server turns it on; so must the test, or
	// the very behaviour under test cannot happen.
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("pragma: %v", err)
	}

	const wsID, crewID, missionID = "ws_1482", "crew_1482", "mission_1482"
	mustExec1482(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?,?,?)`, wsID, "repro", "repro-1482")
	mustExec1482(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?,?,?,?)`, crewID, wsID, "c", "c")
	mustExec1482(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES (?,?,?,?)`,
		"agent_1482", wsID, "lead", "lead")
	mustExec1482(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status)
		VALUES (?,?,?,?,?,?,?)`,
		missionID, wsID, crewID, "agent_1482", "trace_1482", "repro mission", "BACKLOG")

	w := journal.NewWriter(db, slog.New(slog.NewTextHandler(io.Discard, nil)), journal.WriterOptions{FlushInterval: time.Hour})
	defer w.Close()

	// The exact shape issue_handler.go emits on a status transition.
	for i := 0; i < 3; i++ {
		if _, err := w.Emit(ctx, journal.Entry{
			WorkspaceID: wsID,
			CrewID:      crewID,
			MissionID:   missionID,
			Type:        journal.EntryMissionStatus,
			Severity:    journal.SeverityInfo,
			ActorType:   journal.ActorUser,
			ActorID:     "user_1",
			Summary:     "status_changed: BACKLOG → TODO",
			Payload:     map[string]any{"action": "status_changed", "details": "BACKLOG → TODO"},
			Refs:        map[string]any{"mission_id": missionID},
		}); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	res, err := journal.VerifyChain(ctx, db, wsID)
	if err != nil {
		t.Fatalf("verify (before): %v", err)
	}
	if !res.OK {
		t.Fatalf("precondition failed: chain must verify before the mission is deleted (reason=%s)", res.Reason)
	}

	// The trigger. `seed --nuke` does exactly this, as does deleting an issue.
	mustExec1482(t, db, `DELETE FROM missions WHERE id = ?`, missionID)

	var nulled int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries WHERE workspace_id = ? AND mission_id IS NULL`, wsID).Scan(&nulled); err != nil {
		t.Fatalf("count: %v", err)
	}

	res, err = journal.VerifyChain(ctx, db, wsID)
	if err != nil {
		t.Fatalf("verify (after): %v", err)
	}
	if nulled == 0 {
		t.Fatal("expected the FK action to null mission_id on the audit rows; it did not — " +
			"if the FK was removed, #1482 is fixed and this test should now assert res.OK == true")
	}
	if res.OK {
		t.Fatalf("#1482 appears FIXED: deleting a mission nulled mission_id on %d audit rows and the chain still "+
			"verifies. Flip this test to assert a clean chain and close the issue.", nulled)
	}
	t.Logf("#1482 reproduced as expected: %d audit rows had mission_id nulled by the FK; "+
		"break_count=%d seq=%d reason=%s", nulled, res.BreakCount, res.BrokenSeq, res.Reason)
}

func mustExec1482(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %.60s: %v", q, err)
	}
}
