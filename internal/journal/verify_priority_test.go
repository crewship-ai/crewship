package journal

import (
	"context"
	"testing"
	"time"
)

// Chain-safe priority (#1369).
//
// `priority` is inside the hashed projection (ChainFields.Priority) AND is
// UPDATEd in place by the operator-facing pin/permanent control
// (internal/api/journal_handler.go). So an authorised, audited operator action
// permanently broke VerifyChain for that row — a guaranteed false "tampered"
// verdict, which is worse than no verification at all because it trains operators
// to ignore the result.
//
// The fix: the chain commits to priority_at_emit (written once, never updated)
// while `priority` stays where every reader already looks. The mutable column is
// still guarded — each edit appends to journal_entry_priorities and verification
// reconciles the live value against priority_at_emit plus that ledger, so a silent
// DB-level flip (which leaves no ledger row) is still caught.

// TestVerifyChain_OperatorPriorityEditStillVerifies is the red-first regression:
// pinning an entry is a legitimate, authorised, journaled action and must NOT be
// reported as tampering.
func TestVerifyChain_OperatorPriorityEditStillVerifies(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	ids := seedChain(t, w, "ws_test", 4)

	// Baseline: the honest chain verifies.
	if res, err := VerifyChain(ctx, db, "ws_test"); err != nil || !res.OK {
		t.Fatalf("baseline chain should verify; err=%v res=%+v", err, res)
	}

	// An OWNER/ADMIN pins entry 2 — the exact statement journal_handler.go runs,
	// plus the append-only ledger row it now writes in the same transaction.
	if _, err := db.Exec(
		`UPDATE journal_entries SET priority = 'pin' WHERE id = ? AND workspace_id = ?`,
		ids[1], "ws_test"); err != nil {
		t.Fatalf("pin entry: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO journal_entry_priorities
			(id, entry_id, workspace_id, seq, previous_priority, priority, reason, set_by, set_at)
		VALUES ('jep_test_1', ?, 'ws_test', 1, 'normal', 'pin', 'operator pinned it', 'u1', ?)`,
		ids[1], time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("record priority change: %v", err)
	}

	res, err := VerifyChain(ctx, db, "ws_test")
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !res.OK {
		t.Fatalf("an authorised priority edit was reported as tampering: %+v", res)
	}
}

// TestVerifyChain_SilentPriorityFlipDetected is the security half. Moving priority
// out of the hash must not make it a free-for-all: flipping the column with NO
// ledger row is exactly how an attacker would downgrade a `permanent` entry so
// compaction legitimately (and verifiably) removes it later.
func TestVerifyChain_SilentPriorityFlipDetected(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	ids := seedChain(t, w, "ws_test", 4)

	// No ledger row — a raw DB write, not an operator action.
	if _, err := db.Exec(
		`UPDATE journal_entries SET priority = 'normal' WHERE id = ?`, ids[2]); err != nil {
		t.Fatalf("flip priority: %v", err)
	}
	// Guard the fixture: seedChain emits at the default priority, so force a real
	// divergence from priority_at_emit.
	if _, err := db.Exec(
		`UPDATE journal_entries SET priority_at_emit = 'permanent' WHERE id = ?`, ids[2]); err != nil {
		t.Fatalf("set priority_at_emit: %v", err)
	}

	res, err := VerifyChain(ctx, db, "ws_test")
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if res.OK {
		t.Fatal("a silent priority flip with no ledger row was NOT detected")
	}
	if res.BrokenID != ids[2] {
		t.Errorf("BrokenID = %q, want the flipped entry %q", res.BrokenID, ids[2])
	}
}

// TestVerifyChain_ForgedPriorityLedgerDetected: the ledger must not be a bypass.
// An attacker who flips the column AND fabricates a ledger row still has to
// produce a consistent chain of changes starting from priority_at_emit — a
// fabricated row whose previous_priority does not chain back is caught.
func TestVerifyChain_ForgedPriorityLedgerDetected(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	ids := seedChain(t, w, "ws_test", 3)

	if _, err := db.Exec(
		`UPDATE journal_entries SET priority_at_emit = 'permanent', priority = 'normal' WHERE id = ?`,
		ids[1]); err != nil {
		t.Fatalf("flip: %v", err)
	}
	// A ledger row claiming the entry went from 'high' to 'normal' — but the
	// emit-time value was 'permanent', so the chain of changes does not start
	// where it must.
	if _, err := db.Exec(`
		INSERT INTO journal_entry_priorities
			(id, entry_id, workspace_id, seq, previous_priority, priority, set_at)
		VALUES ('jep_forged', ?, 'ws_test', 1, 'high', 'normal', ?)`,
		ids[1], time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("insert forged ledger row: %v", err)
	}

	res, err := VerifyChain(ctx, db, "ws_test")
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if res.OK {
		t.Fatal("a forged priority ledger row was accepted")
	}
}

// TestVerifyChain_PriorityEditSequenceVerifies: several successive operator edits
// must chain — normal → high → pin — and the final live value must match the last
// ledger row.
func TestVerifyChain_PriorityEditSequenceVerifies(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	ids := seedChain(t, w, "ws_test", 3)
	now := time.Now().UTC().Format(time.RFC3339)

	steps := []struct {
		seq        int
		prev, next string
	}{
		{1, "normal", "high"},
		{2, "high", "pin"},
	}
	for _, s := range steps {
		if _, err := db.Exec(
			`UPDATE journal_entries SET priority = ? WHERE id = ?`, s.next, ids[0]); err != nil {
			t.Fatalf("set priority %s: %v", s.next, err)
		}
		if _, err := db.Exec(`
			INSERT INTO journal_entry_priorities
				(id, entry_id, workspace_id, seq, previous_priority, priority, set_at)
			VALUES (?, ?, 'ws_test', ?, ?, ?, ?)`,
			"jep_seq_"+s.next, ids[0], s.seq, s.prev, s.next, now); err != nil {
			t.Fatalf("record change to %s: %v", s.next, err)
		}
	}

	res, err := VerifyChain(ctx, db, "ws_test")
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !res.OK {
		t.Fatalf("a chained sequence of authorised edits was reported as tampering: %+v", res)
	}
}

// TestEmit_WritesPriorityAtEmit pins the invariant the whole scheme rests on: the
// emit path must populate priority_at_emit, or the immutable column drifts to NULL
// and verification loses its anchor.
func TestEmit_WritesPriorityAtEmit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	id, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryRunStarted,
		ActorType:   ActorAgent,
		Summary:     "pinned at birth",
		Priority:    PriorityPin,
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	var live, atEmit string
	if err := db.QueryRow(
		`SELECT priority, COALESCE(priority_at_emit,'') FROM journal_entries WHERE id = ?`, id).
		Scan(&live, &atEmit); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if live != string(PriorityPin) {
		t.Errorf("priority = %q, want pin", live)
	}
	if atEmit != string(PriorityPin) {
		t.Errorf("priority_at_emit = %q, want pin (the chain anchor must be written at emit)", atEmit)
	}

	if res, err := VerifyChain(ctx, db, "ws_test"); err != nil || !res.OK {
		t.Fatalf("an entry emitted with a non-default priority should verify; err=%v res=%+v", err, res)
	}
}

// TestVerifyChain_LegacyPinnedRowVerifies is the reconciliation-side twin of the
// v166 backfill regression. After the migration, a row an operator had pinned
// BEFORE the ledger existed has priority_at_emit == its live (non-default)
// priority — the value its stored hash was already computed over — and NO ledger
// row, because the pre-migration edit history is unrecoverable and the migration
// invents nothing. That state must verify.
//
// Getting this wrong is total, not marginal: it would make the first act of a
// tamper-evidence feature be to declare every existing install compromised.
func TestVerifyChain_LegacyPinnedRowVerifies(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	// A chain with a non-default-priority entry in the MIDDLE, so a false positive
	// would also break the linkage check for everything after it.
	seedChain(t, w, "ws_test", 2)
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryRunStarted,
		ActorType:   ActorAgent,
		Summary:     "pinned before the ledger existed",
		Priority:    PriorityPermanent,
	}); err != nil {
		t.Fatalf("emit pinned entry: %v", err)
	}
	seedChain(t, w, "ws_test", 2)

	// No journal_entry_priorities rows at all — the post-migration legacy state.
	var edits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entry_priorities`).Scan(&edits); err != nil {
		t.Fatalf("count edits: %v", err)
	}
	if edits != 0 {
		t.Fatalf("fixture has %d ledger rows, want 0", edits)
	}

	res, err := VerifyChain(ctx, db, "ws_test")
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !res.OK {
		t.Fatalf("a legacy pinned entry with no edit ledger was reported as tampering: %+v", res)
	}
	if res.Count != 5 {
		t.Errorf("verified %d entries, want 5", res.Count)
	}
}
