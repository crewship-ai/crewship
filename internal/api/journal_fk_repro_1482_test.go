package api

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
	_ "modernc.org/sqlite"
)

// TestJournalChain_MissionDeleteKeepsAuditRows_1482 (formerly
// TestJournalChain_MissionDeleteBreaksChain_1482) pins the root cause of #1482
// and now guards the fix.
//
// It began life as a characterization test that ASSERTED the bug: the schema
// declared
//
//	journal_entries.mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL
//
// and mission_id is one of the fields the tamper-evident chain hash commits to.
// Deleting a mission therefore made SQLite UPDATE every journal row that
// referenced it, setting mission_id to NULL, and those rows stopped verifying —
// reported as "entry was modified after write", by a constraint rather than an
// attacker. That is why grepping for "a code path that UPDATEs a journal row"
// found nothing: there was no such code path. On stage the break count GREW
// with every `seed --nuke`, because each nuke deleted that generation's
// missions and nulled another block of references.
//
// Migration v167 removed the FK action (and restored the values it had nulled,
// proving each one against the stored hash before writing it back), so the
// assertion is flipped: the delete must now leave the audit rows exactly as
// they were written. The test is KEPT in this shape on purpose — if a future
// schema change reintroduces a destructive FK action on the audit table, this
// fails on the same line that once documented the bug.
func TestJournalChain_MissionDeleteKeepsAuditRows_1482(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key-0123456789abcdef") //gitleaks:allow — fake test fixture key

	// SQLite only enforces foreign keys — and therefore only performs their
	// actions — when the pragma is on, so the very behaviour under test cannot
	// happen without it. The fixture opens through database.Open, which sets
	// foreign_keys(ON) in the DSN: that applies to every pooled connection,
	// unlike the `PRAGMA foreign_keys = ON` this test used to run, which only
	// bound the one connection it happened to land on.
	db := testutil.MigratedSQLDB(t)
	ctx := context.Background()

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
	if nulled != 0 {
		t.Errorf("deleting a mission nulled mission_id on %d audit rows — a destructive FK action is back on journal_entries (#1482)", nulled)
	}

	res, err = journal.VerifyChain(ctx, db, wsID)
	if err != nil {
		t.Fatalf("verify (after): %v", err)
	}
	if !res.OK {
		t.Fatalf("#1482 regressed: deleting a mission broke the audit chain at seq %d (%s). "+
			"The audit log must outlive the rows it refers to.", res.BrokenSeq, res.Reason)
	}
}

// The other half of #1482, and the worse one: crew_id and agent_id carried
// ON DELETE CASCADE, so deleting a crew did not merely rewrite audit rows — it
// DELETED them. That destroys the record of everything the crew ever did and
// leaves a seq gap which, to the verifier, is indistinguishable from a
// malicious mid-chain deletion.
//
// Kept separate from the mission case above because it fails differently: a
// CASCADE never breaks a hash, so a test that only checked VerifyChain on the
// remaining rows could pass while the history was being erased. This asserts
// the rows are still THERE first.
func TestJournalChain_CrewDeleteKeepsAuditRows_1482(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key-0123456789abcdef") //gitleaks:allow — fake test fixture key

	// foreign_keys(ON) comes from the fixture's DSN — see the sibling test.
	db := testutil.MigratedSQLDB(t)
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	const wsID, crewID, agentID = "ws_cascade", "crew_cascade", "agent_cascade"
	mustExec1482(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?,?,?)`, wsID, "cascade", "cascade")
	mustExec1482(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?,?,?,?)`, crewID, wsID, "c", "c")
	mustExec1482(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES (?,?,?,?)`, agentID, wsID, "a", "a")

	w := journal.NewWriter(db, quiet, journal.WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	for i := 0; i < 3; i++ {
		if _, err := w.Emit(ctx, journal.Entry{
			WorkspaceID: wsID,
			CrewID:      crewID,
			AgentID:     agentID,
			Type:        journal.EntryKeeperDecision,
			Severity:    journal.SeverityNotice,
			ActorType:   journal.ActorKeeper,
			Summary:     "credential access allowed",
			Payload:     map[string]any{"decision": "ALLOW"},
		}); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	count := func() int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM journal_entries WHERE workspace_id = ?`, wsID).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	before := count()
	if before != 3 {
		t.Fatalf("precondition: %d audit rows, want 3", before)
	}

	mustExec1482(t, db, `DELETE FROM crews WHERE id = ?`, crewID)
	if got := count(); got != before {
		t.Errorf("deleting a crew destroyed %d of %d audit rows — ON DELETE CASCADE is back on journal_entries.crew_id (#1482)",
			before-got, before)
	}
	mustExec1482(t, db, `DELETE FROM agents WHERE id = ?`, agentID)
	if got := count(); got != before {
		t.Errorf("deleting an agent destroyed %d of %d audit rows — ON DELETE CASCADE is back on journal_entries.agent_id (#1482)",
			before-got, before)
	}

	res, err := journal.VerifyChain(ctx, db, wsID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK {
		t.Errorf("the chain does not verify after deleting the crew and agent (seq %d: %s)", res.BrokenSeq, res.Reason)
	}
	if res.Count != before {
		t.Errorf("verify walked %d entries, want %d", res.Count, before)
	}
}

func mustExec1482(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %.60s: %v", q, err)
	}
}
