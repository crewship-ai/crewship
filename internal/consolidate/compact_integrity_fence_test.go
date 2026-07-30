package consolidate

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Compaction is the payload of #1572, not the vulnerability.
//
// The downgrade attack is only worth mounting because of what happens next: the
// compactor's `priority != 'permanent'` predicate reads the live column, deletes
// the row, and signs a checkpoint over the deletion — so the audit record is
// gone AND the chain verifies clean forever afterwards. Detecting the downgrade
// in VerifyChain is necessary but not sufficient, because compaction runs on a
// timer and nobody reads the verifier first.
//
// These tests pin the fence: the compactor may only destroy rows whose
// integrity it can prove, and it may never destroy a row that was PINNED AT
// EMIT, whichever value the mutable column currently holds.

// emitOldChunk emits a chained, compactable entry that is ALREADY older than
// the retention cutoff. The timestamp goes through the emit path rather than a
// later UPDATE because `ts` is inside the hashed projection — ageing a row with
// an UPDATE is a content edit, which the fence under test correctly refuses,
// and a fixture that did that would only ever prove the fence works on its own
// tampering.
func emitOldChunk(t *testing.T, w *journal.Writer, summary string, prio journal.Priority) string {
	t.Helper()
	id, err := w.Emit(context.Background(), journal.Entry{
		WorkspaceID: "ws_test",
		CrewID:      "crew_test",
		Type:        journal.EntryExecOutputChunk,
		ActorType:   journal.ActorAgent,
		Summary:     summary,
		Payload:     map[string]any{"s": summary},
		Priority:    prio,
		TS:          time.Now().UTC().Add(-45 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("emit %s: %v", summary, err)
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return id
}

// createPriorityLedger adds the v166 append-only edit ledger to the package's
// minimal test schema. It lives here rather than in testSchema because only the
// #1572 fence tests need it, and loadPriorityLedger treats the table as
// optional (a pre-v166 DB has none).
func createPriorityLedger(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		CREATE TABLE journal_entry_priorities (
			id TEXT PRIMARY KEY,
			entry_id TEXT NOT NULL,
			workspace_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			previous_priority TEXT NOT NULL,
			priority TEXT NOT NULL,
			reason TEXT,
			set_by TEXT,
			set_at TEXT NOT NULL,
			UNIQUE(entry_id, seq)
		)`); err != nil {
		t.Fatalf("create priority ledger: %v", err)
	}
}

func rowExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", id, err)
	}
	return n == 1
}

// TestCompactor_RefusesRowsWithUnresolvedPriorityIntegrity is the end-to-end
// #1572 attack: downgrade a permanent entry with one UPDATE, then let the
// compactor do the deleting and the signing.
//
// The victim must survive. Everything else in its bucket must still compact —
// a fence that stops compaction working would be replaced within a release.
func TestCompactor_RefusesRowsWithUnresolvedPriorityIntegrity(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "compaction-fence-key-0123456789abcdef")

	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	victim := emitOldChunk(t, w, "the record the attacker wants compaction to eat", journal.PriorityPermanent)
	var ordinary []string
	for i := 0; i < 12; i++ {
		ordinary = append(ordinary, emitOldChunk(t, w, "chunk", journal.PriorityNormal))
	}

	// One statement, no key, no ledger row: drop the victim out of the
	// `priority != 'permanent'` exemption and make the two columns agree so the
	// state is byte-identical to v166 backfill damage.
	if _, err := db.Exec(
		`UPDATE journal_entries SET priority = 'normal', priority_at_emit = 'normal' WHERE id = ?`,
		victim); err != nil {
		t.Fatalf("downgrade: %v", err)
	}

	c := &Compactor{DB: db, Journal: w, Logger: quietLogger()}
	if _, err := c.Run(ctx, "ws_test", 30*24*time.Hour); err != nil {
		t.Fatalf("compact: %v", err)
	}

	if !rowExists(t, db, victim) {
		t.Fatal("compaction deleted an entry whose priority integrity is unresolved — " +
			"the deletion is now covered by a valid signed checkpoint and the audit record is gone")
	}
	deleted := 0
	for _, id := range ordinary {
		if !rowExists(t, db, id) {
			deleted++
		}
	}
	if deleted != len(ordinary) {
		t.Errorf("only %d/%d intact rows were compacted — the fence must not stop honest compaction",
			deleted, len(ordinary))
	}
}

// TestDeleteBucket_RefusesABucketWithAnUnresolvedRow tests the chokepoint on
// its own, not through Run.
//
// Run fences its candidates before they reach a bucket, so this path should
// never fire in practice — which is exactly why it needs its own test. The
// DELETE and the checkpoint that signs it are issued HERE, together, so this is
// the function whose refusal has to hold when a future caller finds another way
// in or a row changes mid-pass. A guard that is only ever exercised through the
// caller that already made it unnecessary is a guard nobody notices deleting.
func TestDeleteBucket_RefusesABucketWithAnUnresolvedRow(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "compaction-fence-key-0123456789abcdef")

	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushInterval: time.Hour})
	defer w.Close()

	victim := emitOldChunk(t, w, "downgraded", journal.PriorityPermanent)
	bystander := emitOldChunk(t, w, "chunk", journal.PriorityNormal)
	if _, err := db.Exec(
		`UPDATE journal_entries SET priority = 'normal', priority_at_emit = 'normal' WHERE id = ?`,
		victim); err != nil {
		t.Fatalf("downgrade: %v", err)
	}

	c := &Compactor{DB: db, Journal: w, Logger: quietLogger()}
	deleted, _, err := c.deleteBucket(context.Background(), "ws_test", []string{bystander, victim})
	if err == nil {
		t.Fatal("deleteBucket signed off on a bucket containing a row it cannot vouch for")
	}
	if deleted != 0 {
		t.Errorf("deleted %d rows while refusing — the refusal must be all-or-nothing", deleted)
	}
	for _, id := range []string{victim, bystander} {
		if !rowExists(t, db, id) {
			t.Errorf("row %s was deleted by a call that returned an error", id)
		}
	}
	// And nothing was signed: a checkpoint here would be a standing excuse for
	// the gap a later delete leaves.
	var checkpoints int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_chain_checkpoints`).Scan(&checkpoints); err != nil {
		t.Fatalf("count checkpoints: %v", err)
	}
	if checkpoints != 0 {
		t.Errorf("a refused delete wrote %d checkpoint(s)", checkpoints)
	}
}

// TestCompactor_NeverDeletesAnEntryPinnedAtEmit closes the second route to the
// same outcome, the one verify.go documents as a residual: the ledger row is
// NOT MAC-authenticated, so an attacker with DB write can append a
// self-consistent un-pin and the chain still verifies.
//
// The fence does not depend on the chain's verdict here. `permanent` at EMIT is
// a keyed, unforgeable fact; un-pinning is an operator convenience recorded in a
// table anyone with DB write can extend. The convenience does not get to
// authorise destruction of the record. An operator who genuinely wants the row
// gone deletes it explicitly — compaction is a space-reclaiming timer, and a
// timer must never be the thing that acts on a forgeable claim.
func TestCompactor_NeverDeletesAnEntryPinnedAtEmit(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "compaction-fence-key-0123456789abcdef")

	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	createPriorityLedger(t, db)
	pinned := emitOldChunk(t, w, "pinned at emit, unpinned by a forged ledger", journal.PriorityPermanent)
	for i := 0; i < 12; i++ {
		emitOldChunk(t, w, "chunk", journal.PriorityNormal)
	}

	// priority_at_emit is left alone, so the keyed hash still verifies; only
	// the mutable column moves, with a ledger row that chains cleanly from the
	// emit-time value. VerifyChain reports this chain as intact.
	if _, err := db.Exec(`UPDATE journal_entries SET priority = 'normal' WHERE id = ?`, pinned); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO journal_entry_priorities
			(id, entry_id, workspace_id, seq, previous_priority, priority, reason, set_by, set_at)
		VALUES ('jep_forged_unpin', ?, 'ws_test', 1, 'permanent', 'normal', 'routine cleanup', 'u_attacker', ?)`,
		pinned, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("ledger row: %v", err)
	}
	if vr, err := journal.VerifyChain(ctx, db, "ws_test"); err != nil || !vr.OK {
		t.Fatalf("fixture guard: this chain is supposed to verify clean; err=%v res=%+v", err, vr)
	}

	c := &Compactor{DB: db, Journal: w, Logger: quietLogger()}
	if _, err := c.Run(ctx, "ws_test", 30*24*time.Hour); err != nil {
		t.Fatalf("compact: %v", err)
	}

	if !rowExists(t, db, pinned) {
		t.Fatal("an entry that was permanent AT EMIT was compacted away on the strength of an " +
			"unauthenticated ledger row — the pin is a keyed fact, the un-pin is not")
	}
}
