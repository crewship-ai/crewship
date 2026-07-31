package journal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Explicit attempt-to-bypass tests for the journal's tamper-evidence invariant
// (#1486).
//
// The distinction this file draws, and the reason it exists as its own file:
// every test here starts from "I am an attacker with DB write access but not
// the HMAC chain key — here is my plan" and asserts the plan FAILS. That is a
// different exercise from the coverage in verify_test.go / verify_priority_test.go,
// which mostly asserts that a given damaged state is reported correctly. The
// modelled attacker is the one verify.go:26-30 names, and the specific thing
// they want is for `crewship journal verify` to say OK while an audit record
// has been weakened or removed.
//
// The three checkpoint tests below all attack the SAME surface — the signed
// compaction checkpoint, which is the one mechanism in the whole design that
// turns a red (uncovered sequence gap) into a green (bridged gap). Per #1486's
// addition: a repair path is a bypass with better branding, so it gets more
// than one test.

// bypassTestKey is a non-empty ENCRYPTION_KEY so the derived chain key is a
// genuine secret the simulated attacker does not hold. Without it the key is
// derived from "" and every "the attacker cannot compute this" assertion would
// be vacuous.
const bypassTestKey = "bypass-suite-encryption-key-0123456789" //gitleaks:allow — fake test fixture key (HMAC chain-key derivation), not a real secret

// TestVerifyChain_ForgedCheckpointCannotBridgeMaliciousDelete: an attacker
// deletes a mid-chain audit row and fabricates the checkpoint that would
// legitimise the resulting sequence gap. They can copy the (seq, entry_hash)
// pair verbatim from the row they are about to delete — those are plain
// columns — so the ONLY thing standing between them and a clean verify is the
// MAC. This asserts the MAC is actually checked and not merely stored.
func TestVerifyChain_ForgedCheckpointCannotBridgeMaliciousDelete(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", bypassTestKey)

	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	ids := seedChain(t, w, "ws_test", 5)
	if res, err := VerifyChain(ctx, db, "ws_test"); err != nil || !res.OK {
		t.Fatalf("baseline chain should verify; err=%v res=%+v", err, res)
	}

	// The attacker reads the facts they need off the victim row.
	var seq int64
	var entryHash string
	if err := db.QueryRow(
		`SELECT seq, entry_hash FROM journal_entries WHERE id = ?`, ids[2]).Scan(&seq, &entryHash); err != nil {
		t.Fatalf("read victim row: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM journal_entries WHERE id = ?`, ids[2]); err != nil {
		t.Fatalf("delete victim row: %v", err)
	}

	// A perfectly-shaped checkpoint body — same schema, same seq, same hash a
	// real compaction would have written. Only the MAC is guessed.
	body, err := json.Marshal([]RemovedEntry{{Seq: seq, Hash: entryHash}})
	if err != nil {
		t.Fatalf("marshal checkpoint body: %v", err)
	}
	plain := sha256.Sum256(body)

	for _, forged := range []struct {
		name string
		mac  string
	}{
		// The obvious forgery: hash the body with something they CAN compute.
		{"plain sha256 of the body", hex.EncodeToString(plain[:])},
		// Replay a value the attacker already holds. entry_hash is a valid
		// HMAC under the real key, so if the checkpoint MAC were not
		// domain-separated and workspace-bound this is what would be tried.
		{"replayed entry_hash", entryHash},
		// Structurally valid hex of the right length, contents guessed.
		{"guessed hex", strings.Repeat("ab", 32)},
		// Empty — the "maybe it's only checked when present" probe.
		{"empty mac", ""},
	} {
		t.Run(forged.name, func(t *testing.T) {
			if _, err := db.Exec(`DELETE FROM journal_chain_checkpoints`); err != nil {
				t.Fatalf("clear checkpoints: %v", err)
			}
			if _, err := db.Exec(checkpointInsertSQL,
				"ckpt_forged", "ws_test", string(body), forged.mac); err != nil {
				t.Fatalf("insert forged checkpoint: %v", err)
			}

			res, err := VerifyChain(ctx, db, "ws_test")
			if err != nil {
				t.Fatalf("VerifyChain: %v", err)
			}
			if res.OK {
				t.Fatalf("a forged checkpoint (%s) laundered a malicious mid-chain delete — "+
					"the compaction bridge is not actually authenticated", forged.name)
			}
			if res.Checkpoints != 0 {
				t.Errorf("Checkpoints = %d, want 0 — a bad-MAC checkpoint must contribute nothing", res.Checkpoints)
			}
			if !strings.Contains(res.Reason, "sequence gap") {
				t.Errorf("Reason = %q, want the uncovered-gap verdict", res.Reason)
			}
		})
	}
}

// TestVerifyChain_ValidCheckpointCannotBeReplayedIntoAnotherWorkspace: the
// attacker does not need to forge a MAC if they can steal one. Compaction runs
// per workspace, so a genuine, valid checkpoint exists in workspace A. This
// copies that row verbatim into workspace B — same body, same MAC — to cover a
// delete there.
//
// It must fail because CheckpointMAC frames the workspace id into the MAC
// (verify.go:182-183). If that framing were ever dropped, a single legitimate
// compaction anywhere in the install would become a universal delete permit.
func TestVerifyChain_ValidCheckpointCannotBeReplayedIntoAnotherWorkspace(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", bypassTestKey)

	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	seedChain(t, w, "ws_a", 4)
	bIDs := seedChain(t, w, "ws_b", 4)

	// Workspace A gets a REAL compaction: delete seq 2 and sign the checkpoint
	// with the production helper, exactly as internal/consolidate does.
	var aSeq int64
	var aHash string
	if err := db.QueryRow(
		`SELECT seq, entry_hash FROM journal_entries WHERE workspace_id = 'ws_a' AND seq = 2`).
		Scan(&aSeq, &aHash); err != nil {
		t.Fatalf("read ws_a row: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(`DELETE FROM journal_entries WHERE workspace_id = 'ws_a' AND seq = 2`); err != nil {
		t.Fatalf("compact ws_a: %v", err)
	}
	if err := WriteChainCheckpoint(ctx, tx, ChainKeyFromEnv(), "ws_a",
		[]RemovedEntry{{Seq: aSeq, Hash: aHash}}); err != nil {
		t.Fatalf("write real checkpoint: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if res, err := VerifyChain(ctx, db, "ws_a"); err != nil || !res.OK {
		t.Fatalf("a legitimately compacted workspace must still verify; err=%v res=%+v", err, res)
	}

	// Now the attack on workspace B. Delete the row at the same seq, then steal
	// A's checkpoint row wholesale — body and MAC — and file it under ws_b.
	if _, err := db.Exec(`DELETE FROM journal_entries WHERE id = ?`, bIDs[1]); err != nil {
		t.Fatalf("delete ws_b row: %v", err)
	}
	var stolenBody, stolenMAC string
	if err := db.QueryRow(
		`SELECT removed_json, mac FROM journal_chain_checkpoints WHERE workspace_id = 'ws_a'`).
		Scan(&stolenBody, &stolenMAC); err != nil {
		t.Fatalf("steal checkpoint: %v", err)
	}
	if _, err := db.Exec(checkpointInsertSQL, "ckpt_replayed", "ws_b", stolenBody, stolenMAC); err != nil {
		t.Fatalf("replay checkpoint into ws_b: %v", err)
	}

	res, err := VerifyChain(ctx, db, "ws_b")
	if err != nil {
		t.Fatalf("VerifyChain(ws_b): %v", err)
	}
	if res.OK {
		t.Fatal("a checkpoint minted for another workspace covered a delete here — " +
			"the checkpoint MAC is not bound to its workspace")
	}
	if res.Checkpoints != 0 {
		t.Errorf("Checkpoints = %d, want 0 — the replayed checkpoint must contribute nothing", res.Checkpoints)
	}
	// And the theft must not have disturbed the honest workspace.
	if res, err := VerifyChain(ctx, db, "ws_a"); err != nil || !res.OK {
		t.Errorf("ws_a stopped verifying after the replay attempt; err=%v res=%+v", err, res)
	}
}

// TestVerifyChain_CheckpointCannotBeWidenedToCoverExtraRows: the attacker has a
// genuine checkpoint (say compaction removed seq 2) and wants it to also cover
// seq 3, which they are about to delete. They append an entry to removed_json.
// The MAC covers the whole set, so widening it invalidates the signature and
// the checkpoint stops covering ANYTHING — including the row it legitimately
// covered. Greedy widening must be strictly worse for the attacker than doing
// nothing.
func TestVerifyChain_CheckpointCannotBeWidenedToCoverExtraRows(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", bypassTestKey)

	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	seedChain(t, w, "ws_test", 5)

	type fact struct {
		seq  int64
		hash string
	}
	read := func(seq int64) fact {
		t.Helper()
		var f fact
		if err := db.QueryRow(
			`SELECT seq, entry_hash FROM journal_entries WHERE workspace_id='ws_test' AND seq = ?`, seq).
			Scan(&f.seq, &f.hash); err != nil {
			t.Fatalf("read seq %d: %v", seq, err)
		}
		return f
	}
	legit := read(2)
	extra := read(3)

	// A real, correctly-signed checkpoint for seq 2 only.
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(`DELETE FROM journal_entries WHERE workspace_id='ws_test' AND seq = 2`); err != nil {
		t.Fatalf("compact seq 2: %v", err)
	}
	if err := WriteChainCheckpoint(ctx, tx, ChainKeyFromEnv(), "ws_test",
		[]RemovedEntry{{Seq: legit.seq, Hash: legit.hash}}); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if res, err := VerifyChain(ctx, db, "ws_test"); err != nil || !res.OK {
		t.Fatalf("precondition: the honestly compacted chain must verify; err=%v res=%+v", err, res)
	}

	// The attack: delete seq 3 too, and widen the existing checkpoint's body to
	// claim it. The MAC column is left as-is — it is the only part they cannot
	// recompute.
	if _, err := db.Exec(`DELETE FROM journal_entries WHERE workspace_id='ws_test' AND seq = 3`); err != nil {
		t.Fatalf("delete seq 3: %v", err)
	}
	widened, err := json.Marshal([]RemovedEntry{
		{Seq: legit.seq, Hash: legit.hash},
		{Seq: extra.seq, Hash: extra.hash},
	})
	if err != nil {
		t.Fatalf("marshal widened body: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE journal_chain_checkpoints SET removed_json = ? WHERE workspace_id = 'ws_test'`,
		string(widened)); err != nil {
		t.Fatalf("widen checkpoint: %v", err)
	}

	res, err := VerifyChain(ctx, db, "ws_test")
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if res.OK {
		t.Fatal("a widened checkpoint covered a row it was never signed for — " +
			"the MAC does not commit to the whole removed set")
	}
	if res.Checkpoints != 0 {
		t.Errorf("Checkpoints = %d, want 0 — widening must invalidate the signature outright", res.Checkpoints)
	}
}

// TestVerifyChain_ContentEditCannotBeHiddenBehindACheckpoint: a checkpoint
// bridges a DELETED seq. The attacker's hope is that it also excuses a row that
// is still present — i.e. that the bridge is applied by seq without checking
// the row is actually gone. It is not: the walk consumes checkpoints only while
// filling a gap, so a checkpoint naming a live row's seq is inert and the
// tampered row is still reported.
func TestVerifyChain_ContentEditCannotBeHiddenBehindACheckpoint(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", bypassTestKey)

	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	ids := seedChain(t, w, "ws_test", 4)

	var seq int64
	var entryHash string
	if err := db.QueryRow(
		`SELECT seq, entry_hash FROM journal_entries WHERE id = ?`, ids[1]).Scan(&seq, &entryHash); err != nil {
		t.Fatalf("read row: %v", err)
	}
	// Tamper with the row's content but leave it in place.
	if _, err := db.Exec(
		`UPDATE journal_entries SET summary = 'nothing to see here' WHERE id = ?`, ids[1]); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	// And file a fully VALID checkpoint claiming that seq was compacted away.
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := WriteChainCheckpoint(ctx, tx, ChainKeyFromEnv(), "ws_test",
		[]RemovedEntry{{Seq: seq, Hash: entryHash}}); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	res, err := VerifyChain(ctx, db, "ws_test")
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if res.OK {
		t.Fatal("a checkpoint naming a row that is still present excused an edit to it — " +
			"the compaction bridge is being applied outside a sequence gap")
	}
	if res.BrokenID != ids[1] {
		t.Errorf("BrokenID = %q, want the edited row %q", res.BrokenID, ids[1])
	}
}

// TestVerifyChain_SelfConsistentPriorityDowngradeIsNotLaundered is the
// invariant TestVerifyChain_SilentPriorityFlipDetected states in prose
// (verify_priority_test.go:65-68) but only half-covers:
//
//	"flipping the column with NO ledger row is exactly how an attacker would
//	 downgrade a `permanent` entry so compaction legitimately (and verifiably)
//	 removes it later."
//
// That test flips `priority` and leaves `priority_at_emit` divergent, which
// trips the backfill fingerprint at verify.go and is caught. An attacker who
// sets BOTH columns used to produce state that is byte-identical to legitimate
// v166 backfill damage: recoverEmitPriority proved the row authentic, it was
// filed as Repairable rather than a Break, skipReconcile suppressed the
// priority check, and VerifyChain returned OK=true.
//
// #1572 removed the suppression: recovery still proves the CONTENT authentic
// (and still reports the recovered value, which is the repair material), but it
// no longer excuses the live priority. Reconciliation runs against the
// recovered emit-time value, and a live value that no ledger row explains is a
// break — whichever way the two columns were written.
func TestVerifyChain_SelfConsistentPriorityDowngradeIsNotLaundered(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", bypassTestKey)

	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	seedChain(t, w, "ws_test", 2)
	victim, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryRunStarted,
		ActorType:   ActorAgent,
		Summary:     "the record the attacker wants compaction to eat",
		Priority:    PriorityPermanent,
	})
	if err != nil {
		t.Fatalf("emit victim: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	seedChain(t, w, "ws_test", 2)

	if res, err := VerifyChain(ctx, db, "ws_test"); err != nil || !res.OK {
		t.Fatalf("baseline chain should verify; err=%v res=%+v", err, res)
	}

	// One statement, no ledger row, no key needed: drop the entry out of the
	// `priority != 'permanent'` compaction exemption (compact.go:223) and make
	// the two columns agree so the v166 fingerprint matches.
	if _, err := db.Exec(
		`UPDATE journal_entries SET priority = 'normal', priority_at_emit = 'normal' WHERE id = ?`,
		victim); err != nil {
		t.Fatalf("downgrade: %v", err)
	}

	res, err := VerifyChain(ctx, db, "ws_test")
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if res.OK {
		t.Fatal("a permanent→normal downgrade with no ledger row was reported as an intact chain — " +
			"the entry is now compaction-eligible and its removal will be signed and verifiable")
	}
	if res.BrokenID != victim {
		t.Errorf("BrokenID = %q, want the downgraded entry %q", res.BrokenID, victim)
	}
}

// TestVerifyChain_ConsistentForgedPriorityLedgerIsADocumentedResidual pins the
// SECOND route to the same downgrade, and pins it as a KNOWN, DOCUMENTED bound
// rather than a surprise.
//
// verify.go:494-501 is explicit that the ledger row is not MAC-authenticated
// and that an attacker with DB write can append a self-consistent chain of fake
// edits. The stated compensating control — every real edit also writes a
// `memory.priority_changed` entry INTO the keyed chain, so a forged ledger with
// no matching chained entry is detectable by comparing the two — is NOT
// implemented inside VerifyChain today. This test exists so that fact lives in
// the test suite and not only in a comment: it asserts the current behaviour,
// so implementing the cross-check will turn it red and the fixer will find this
// note. See #1572, which tracks both vectors.
//
// #1572 UPDATE: the verifier's verdict here is deliberately unchanged — a
// self-consistent forged ledger still verifies clean, and the cross-check
// against `memory.priority_changed` is still not implemented. What changed is
// the PAYLOAD: journal.UnresolvedIntegrity refuses to let compaction delete an
// entry that was `permanent` at emit, whatever the ledger claims, so this
// forgery no longer gets the record destroyed. See
// TestCompactor_NeverDeletesAnEntryPinnedAtEmit in internal/consolidate.
func TestVerifyChain_ConsistentForgedPriorityLedgerIsADocumentedResidual(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", bypassTestKey)

	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	victim, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryRunStarted,
		ActorType:   ActorAgent,
		Summary:     "permanent record",
		Priority:    PriorityPermanent,
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	seedChain(t, w, "ws_test", 2)

	// priority_at_emit is left at 'permanent' (untouched, still hashed), and the
	// live column is dropped to 'normal' with a ledger row that chains cleanly
	// from the emit-time value. No corresponding memory.priority_changed entry
	// is written — the tell that no operator action occurred.
	if _, err := db.Exec(
		`UPDATE journal_entries SET priority = 'normal' WHERE id = ?`, victim); err != nil {
		t.Fatalf("flip live priority: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO journal_entry_priorities
			(id, entry_id, workspace_id, seq, previous_priority, priority, reason, set_by, set_at)
		VALUES ('jep_forged_consistent', ?, 'ws_test', 1, 'permanent', 'normal', 'routine cleanup', 'u_attacker', ?)`,
		victim, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("append forged ledger row: %v", err)
	}

	res, err := VerifyChain(ctx, db, "ws_test")
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !res.OK {
		t.Fatalf("BEHAVIOUR CHANGED (good, probably): VerifyChain now rejects a self-consistent forged "+
			"priority ledger. That closes the second vector in #1572 — delete this residual test and "+
			"replace it with the positive assertion. res=%+v", res)
	}
	// Documenting the exact shape of the residual: the entry is now outside the
	// compaction exemption with the chain still reporting green.
	var live string
	if err := db.QueryRow(`SELECT priority FROM journal_entries WHERE id = ?`, victim).Scan(&live); err != nil {
		t.Fatalf("read live priority: %v", err)
	}
	if live != "normal" {
		t.Fatalf("fixture broken: live priority = %q, want normal", live)
	}
}

// TestVerifyChain_TailTruncationIsADocumentedResidual pins the residual
// verify.go:45-49 names in prose: deleting the NEWEST n entries leaves a
// shorter but internally consistent chain, so the walk finds nothing wrong.
//
// This is the highest-value thing an attacker can do to the journal without the
// key, and the reason it is worth a test rather than only a comment is that a
// reader of the suite would otherwise reasonably conclude "mid-chain delete is
// caught, therefore deletes are caught". Tail truncation is tracked separately;
// when a tail anchor (a signed high-water mark) lands, this test goes red and
// should be inverted.
func TestVerifyChain_TailTruncationIsADocumentedResidual(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", bypassTestKey)

	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	seedChain(t, w, "ws_test", 6)

	// Lop off the two newest entries — no checkpoint, no key.
	if _, err := db.Exec(
		`DELETE FROM journal_entries WHERE workspace_id = 'ws_test' AND seq > 4`); err != nil {
		t.Fatalf("truncate tail: %v", err)
	}

	res, err := VerifyChain(ctx, db, "ws_test")
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !res.OK {
		t.Fatalf("BEHAVIOUR CHANGED (good): tail truncation is now detected. Invert this test into a "+
			"positive assertion and drop the residual note in verify.go:45-49. res=%+v", res)
	}
	if res.Count != 4 {
		t.Errorf("Count = %d, want 4 — the fixture did not truncate what it meant to", res.Count)
	}
}
