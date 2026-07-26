package journal

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"
)

// plainChainHash reproduces the UNKEYED sha256 an attacker (or the pre-fix
// code) would compute over an entry's public columns, using the exact same
// length-framing as the real chain hash. It exists only so the keying test can
// simulate a DB-write attacker who recomputes hash columns without the HMAC
// key — the whole point of the fix is that this recomputation no longer
// validates.
func plainChainHash(prevHash string, f ChainFields) string {
	h := sha256.New()
	var seqb [8]byte
	binary.BigEndian.PutUint64(seqb[:], uint64(f.Seq))
	h.Write(seqb[:])
	for _, field := range []string{
		prevHash, f.ID, f.Workspace, f.CrewID, f.AgentID, f.MissionID,
		f.TS, f.EntryType, f.Severity, f.Priority, f.ActorType, f.ActorID,
		f.Summary, f.Payload, f.Refs, f.TraceID, f.SpanID, f.ExpiresAt,
	} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(field)))
		h.Write(n[:])
		h.Write([]byte(field))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestVerifyChain_KeyedRejectsRecomputedHash proves the chain is KEYED: a
// DB-write attacker who mutates a row and recomputes every downstream
// entry_hash/prev_hash with a bare sha256 (which they can compute, unlike the
// HMAC) still fails verification. Before the fix (plain sha256 chain) this
// exact recomputation VALIDATED — an undetectable rewrite. It must now be
// caught because the verifier keys the hash with a secret the attacker lacks.
func TestVerifyChain_KeyedRejectsRecomputedHash(t *testing.T) {
	// A real, non-empty ENCRYPTION_KEY so the derived HMAC key is a genuine
	// secret the simulated attacker does not know.
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key-0123456789abcdef") //gitleaks:allow — fake test fixture key (HMAC chain-key derivation), not a real secret

	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()

	ids := seedChain(t, w, "ws_test", 5)

	// Sanity: the honestly-emitted chain verifies clean under the keyed hash.
	if res, err := VerifyChain(context.Background(), db, "ws_test"); err != nil || !res.OK {
		t.Fatalf("baseline chain should verify; err=%v res=%+v", err, res)
	}

	// Attacker rewrites entry 3 and re-links entries 3..5 with a bare sha256,
	// exactly the recompute the review describes. Walk the tail in seq order,
	// carrying prevHash forward so the *plain* chain is internally consistent
	// (isolating the key as the sole reason verification fails).
	prev := ""
	// prev must start as the (untouched) entry_hash of seq 2.
	if err := db.QueryRow(`SELECT entry_hash FROM journal_entries WHERE id = ?`, ids[1]).Scan(&prev); err != nil {
		t.Fatalf("read seq2 hash: %v", err)
	}
	for i := 2; i < 5; i++ {
		var f ChainFields
		if err := db.QueryRow(`
			SELECT seq, id, workspace_id, COALESCE(crew_id,''), COALESCE(agent_id,''),
			       COALESCE(mission_id,''), ts, entry_type, severity,
			       COALESCE(priority,'normal'), actor_type, COALESCE(actor_id,''),
			       summary, payload, refs, COALESCE(trace_id,''), COALESCE(span_id,''),
			       COALESCE(expires_at,'')
			FROM journal_entries WHERE id = ?`, ids[i]).Scan(
			&f.Seq, &f.ID, &f.Workspace, &f.CrewID, &f.AgentID, &f.MissionID,
			&f.TS, &f.EntryType, &f.Severity, &f.Priority, &f.ActorType, &f.ActorID,
			&f.Summary, &f.Payload, &f.Refs, &f.TraceID, &f.SpanID, &f.ExpiresAt,
		); err != nil {
			t.Fatalf("read row %d: %v", i, err)
		}
		if i == 2 {
			f.Summary = "TAMPERED-BY-ATTACKER" // the actual forgery
		}
		newHash := plainChainHash(prev, f)
		if _, err := db.Exec(
			`UPDATE journal_entries SET summary = ?, prev_hash = ?, entry_hash = ? WHERE id = ?`,
			f.Summary, prev, newHash, f.ID); err != nil {
			t.Fatalf("attacker rewrite row %d: %v", i, err)
		}
		prev = newHash
	}

	res, err := VerifyChain(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK {
		t.Fatalf("KEYING BROKEN: attacker's plain-sha256 rewrite validated — the chain is not actually keyed")
	}
	if res.BrokenID != ids[2] {
		t.Fatalf("want break at the forged row %s, got %s (seq=%d, reason=%q)",
			ids[2], res.BrokenID, res.BrokenSeq, res.Reason)
	}
}

// seedChain emits n well-formed entries into ws via a real Writer and
// flushes, so the hash-chain columns are populated by the production
// emit path.
func seedChain(t *testing.T, w *Writer, ws string, n int) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id, err := w.Emit(ctx, Entry{
			WorkspaceID: ws,
			Type:        EntryRunStarted,
			ActorType:   ActorAgent,
			Summary:     "entry",
			Payload:     map[string]any{"i": i, "note": "hash-chain"},
		})
		if err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return ids
}

// TestVerifyChain_WellFormed: a chain produced by the emit path verifies OK.
func TestVerifyChain_WellFormed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()

	seedChain(t, w, "ws_test", 5)

	res, err := VerifyChain(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK {
		t.Fatalf("want OK chain, got broken at seq=%d reason=%q", res.BrokenSeq, res.Reason)
	}
	if res.Count != 5 {
		t.Fatalf("want 5 entries checked, got %d", res.Count)
	}
}

// TestVerifyChain_Empty: a workspace with no entries is trivially OK.
func TestVerifyChain_Empty(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	res, err := VerifyChain(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK || res.Count != 0 {
		t.Fatalf("want OK/0, got OK=%v count=%d", res.OK, res.Count)
	}
}

// TestVerifyChain_MutatedContent: mutating a row's summary after the fact is
// detected — its stored entry_hash no longer matches recomputed content.
func TestVerifyChain_MutatedContent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()

	ids := seedChain(t, w, "ws_test", 5)

	// Tamper: rewrite the 3rd entry's summary directly, as a compromised
	// operator with DB access would.
	if _, err := db.Exec(`UPDATE journal_entries SET summary = ? WHERE id = ?`,
		"TAMPERED", ids[2]); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	res, err := VerifyChain(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK {
		t.Fatalf("mutation went undetected")
	}
	if res.BrokenID != ids[2] {
		t.Fatalf("want break at %s, got %s (seq=%d, reason=%q)", ids[2], res.BrokenID, res.BrokenSeq, res.Reason)
	}
}

// TestVerifyChain_DeletedMiddle: deleting a middle row leaves a sequence gap
// that verification reports.
func TestVerifyChain_DeletedMiddle(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()

	ids := seedChain(t, w, "ws_test", 5)

	if _, err := db.Exec(`DELETE FROM journal_entries WHERE id = ?`, ids[2]); err != nil {
		t.Fatalf("delete: %v", err)
	}

	res, err := VerifyChain(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK {
		t.Fatalf("mid-chain deletion went undetected")
	}
	// The break surfaces at the first entry after the hole (seq 4, which
	// now follows seq 2).
	if res.BrokenSeq != 4 {
		t.Fatalf("want break reported at seq 4, got seq=%d reason=%q", res.BrokenSeq, res.Reason)
	}
}

// TestVerifyChain_Reordered: swapping two entries' content (keeping their
// seq) breaks the prev_hash linkage.
func TestVerifyChain_Reordered(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()

	ids := seedChain(t, w, "ws_test", 5)

	// Swap the summaries of entries 2 and 3 without touching hashes — an
	// attacker trying to reorder history in place.
	if _, err := db.Exec(`UPDATE journal_entries SET summary = 'swap-a' WHERE id = ?`, ids[1]); err != nil {
		t.Fatalf("swap: %v", err)
	}

	res, err := VerifyChain(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK {
		t.Fatalf("in-place content swap went undetected")
	}
}

// TestVerifyChain_Isolation: chains are independent per workspace; tampering
// one does not flag another.
func TestVerifyChain_Isolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	// The default openTestDB only seeds ws_test; add a second workspace.
	if _, err := db.Exec(`INSERT INTO workspaces (id) VALUES ('ws_other')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()

	seedChain(t, w, "ws_test", 3)
	ids := seedChain(t, w, "ws_other", 3)

	if _, err := db.Exec(`UPDATE journal_entries SET summary = 'x' WHERE id = ?`, ids[1]); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	good, err := VerifyChain(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("verify good: %v", err)
	}
	if !good.OK {
		t.Fatalf("untouched workspace flagged: seq=%d reason=%q", good.BrokenSeq, good.Reason)
	}
	bad, err := VerifyChain(context.Background(), db, "ws_other")
	if err != nil {
		t.Fatalf("verify bad: %v", err)
	}
	if bad.OK {
		t.Fatalf("tampered workspace not flagged")
	}
}

// TestVerifyChain_ReportsEveryContentBreak: one unverifiable row must not
// blind the operator to everything after it.
//
// This is the failure observed on stage 2026-07-25: verify halted at seq 86657
// with a content-hash mismatch, so the ~86k entries written afterwards were
// never checked at all. The broken row was legacy — a pre-v166 entry whose
// priority was pinned before the migration backfilled priority_at_emit, losing
// the emit-time value the stored hash commits to — and it is unrepairable.
// Halting there means the tamper-evidence is dead for everything newer, which
// is the opposite of what it exists to do.
//
// A content-hash mismatch is a fact about ONE row. The rows after it still
// chain onto its STORED hash, so the walk can and must continue.
func TestVerifyChain_ReportsEveryContentBreak(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()

	ids := seedChain(t, w, "ws_test", 6)

	// Two independent tampered rows, with clean rows between and after.
	for _, i := range []int{1, 4} {
		if _, err := db.Exec(`UPDATE journal_entries SET summary = ? WHERE id = ?`,
			"TAMPERED", ids[i]); err != nil {
			t.Fatalf("tamper %d: %v", i, err)
		}
	}

	res, err := VerifyChain(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK {
		t.Fatal("tampering went undetected")
	}
	if len(res.Breaks) != 2 {
		t.Fatalf("Breaks = %d, want 2 — a halt at the first break hides the rest: %+v", len(res.Breaks), res.Breaks)
	}
	if res.Breaks[0].ID != ids[1] || res.Breaks[1].ID != ids[4] {
		t.Errorf("breaks at %s/%s, want %s/%s", res.Breaks[0].ID, res.Breaks[1].ID, ids[1], ids[4])
	}
	// The whole chain must still be walked, so a later tamper is reachable.
	if res.Count != 6 {
		t.Errorf("Count = %d, want 6 — the walk stopped early", res.Count)
	}
	// Back-compat: the legacy single-break fields keep naming the FIRST break.
	if res.BrokenID != ids[1] {
		t.Errorf("BrokenID = %s, want the first break %s", res.BrokenID, ids[1])
	}
	if res.Reason == "" {
		t.Error("Reason must still describe the first break")
	}
}

// A structural break is different in kind: once the sequence itself cannot be
// trusted, continuing produces cascading noise rather than information. Those
// still halt.
func TestVerifyChain_StructuralBreakStillHalts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()

	ids := seedChain(t, w, "ws_test", 5)
	if _, err := db.Exec(`DELETE FROM journal_entries WHERE id = ?`, ids[2]); err != nil {
		t.Fatalf("delete: %v", err)
	}

	res, err := VerifyChain(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK {
		t.Fatal("uncovered deletion went undetected")
	}
	if res.Count >= 5 {
		t.Errorf("Count = %d — a sequence gap must stop the walk, not continue past it", res.Count)
	}
}

// TestVerifyChain_BreaksAreCapped: a wholly-unverifiable chain must not be
// answered with a megabyte of JSON.
//
// The failure mode that motivates this: if the chain KEY differs — a rotated
// or unset ENCRYPTION_KEY — then every single row mismatches. Before breaks
// were collected the walk returned at row 1; collecting them without a bound
// turns that same scenario into one ChainBreak per entry, which on stage's
// 86k-entry journal is tens of megabytes allocated and serialised on an admin
// endpoint. The operator still needs to know the true scale, so the count is
// exact even though the list is not.
func TestVerifyChain_BreaksAreCapped(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()

	const n = maxReportedBreaks + 15
	ids := seedChain(t, w, "ws_test", n)

	// Tamper with every row — the "wrong key" shape, without needing to
	// rotate a key mid-test.
	for _, id := range ids {
		if _, err := db.Exec(`UPDATE journal_entries SET summary = ? WHERE id = ?`, "TAMPERED", id); err != nil {
			t.Fatalf("tamper: %v", err)
		}
	}

	res, err := VerifyChain(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK {
		t.Fatal("wholesale tampering went undetected")
	}
	if len(res.Breaks) > maxReportedBreaks {
		t.Errorf("Breaks = %d, want at most %d — an unbounded list is the bug", len(res.Breaks), maxReportedBreaks)
	}
	if res.BreakCount != n {
		t.Errorf("BreakCount = %d, want %d — the count must stay exact even when the list is trimmed", res.BreakCount, n)
	}
	if !res.BreaksTruncated {
		t.Error("BreaksTruncated must say so when the list is shorter than the count")
	}
	// The first break still names the earliest row, as before.
	if res.BrokenID != ids[0] {
		t.Errorf("BrokenID = %s, want %s", res.BrokenID, ids[0])
	}
}

// TestVerifyChain_RecoversBackfilledPriority: a row broken by the v166 backfill
// is RECOVERABLE, not lost — and proving that is better than waiving it.
//
// The v166 migration set priority_at_emit = COALESCE(priority,'normal') from
// the value at migration time. For a row whose priority had already been
// edited, that wrote the EDITED value into the column the hash commits to, so
// verification failed forever. Stage has one: seq 86657, and it halted the
// walk over ~86k newer entries.
//
// The emit-time value is not gone, though — it is one of exactly four:
// normal | high | pin | permanent. Recomputing the keyed hash against each and
// finding the one that reproduces the STORED hash proves the entry is
// authentic and recovers the value at the same time.
//
// This does not weaken the oracle. The hash is an HMAC under a secret chain
// key; an attacker who could produce a matching hash for any of four candidate
// priorities could already forge one for the real value. What the search
// removes is a false positive, not a real detection.
func TestVerifyChain_RecoversBackfilledPriority(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()

	ids := seedChain(t, w, "ws_test", 4)

	// Reproduce the v166 damage on one row: the operator pinned it, and the
	// migration then copied the pinned value over the emit-time one.
	if _, err := db.Exec(
		`UPDATE journal_entries SET priority = 'pin', priority_at_emit = 'pin' WHERE id = ?`,
		ids[1]); err != nil {
		t.Fatalf("simulate backfill damage: %v", err)
	}

	res, err := VerifyChain(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	if len(res.Breaks) != 0 {
		t.Errorf("reported %d break(s) for a row whose content is provably authentic: %+v",
			len(res.Breaks), res.Breaks)
	}
	if !res.OK {
		t.Errorf("OK=false — a recoverable backfill artefact is not tampering (reason: %q)", res.Reason)
	}
	if len(res.Repairable) != 1 {
		t.Fatalf("Repairable = %d, want 1 — the wrong stored value must still be surfaced", len(res.Repairable))
	}
	if res.Repairable[0].ID != ids[1] {
		t.Errorf("Repairable names %s, want %s", res.Repairable[0].ID, ids[1])
	}
	// The recovered value is what makes a repair possible rather than a guess.
	if res.Repairable[0].EmitPriority == "" {
		t.Error("recovered emit-time priority is empty — nothing to repair with")
	}
	if res.Count != 4 {
		t.Errorf("Count = %d, want 4 — the walk must not stop", res.Count)
	}
}

// Genuine tampering must NOT be laundered by the recovery search.
func TestVerifyChain_RecoveryDoesNotHideRealTampering(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()

	ids := seedChain(t, w, "ws_test", 4)

	// Content edit — no priority value can reproduce this hash.
	if _, err := db.Exec(`UPDATE journal_entries SET summary = 'TAMPERED' WHERE id = ?`, ids[2]); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	res, err := VerifyChain(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.OK {
		t.Fatal("the recovery search laundered a real content edit")
	}
	if len(res.Breaks) != 1 || res.Breaks[0].ID != ids[2] {
		t.Errorf("breaks = %+v, want exactly the tampered row %s", res.Breaks, ids[2])
	}
}

// Same cap as Breaks, for the same reason — and this is the third time in one
// change-set that an unbounded list slipped into the admin response, so it gets
// a test rather than another round of remembering.
func TestVerifyChain_RepairableIsCapped(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()

	const n = maxReportedBreaks + 12
	ids := seedChain(t, w, "ws_test", n)

	// The v166 damage shape on every row: emit and live both pinned, so the
	// fingerprint matches and each row recovers.
	for _, id := range ids {
		if _, err := db.Exec(
			`UPDATE journal_entries SET priority = 'pin', priority_at_emit = 'pin' WHERE id = ?`,
			id); err != nil {
			t.Fatalf("damage: %v", err)
		}
	}

	res, err := VerifyChain(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(res.Repairable) > maxReportedBreaks {
		t.Errorf("Repairable = %d, want at most %d", len(res.Repairable), maxReportedBreaks)
	}
	if res.RepairableCount != n {
		t.Errorf("RepairableCount = %d, want %d — the count must survive the trim", res.RepairableCount, n)
	}
	if !res.RepairableTruncated {
		t.Error("RepairableTruncated must say the list was trimmed")
	}
	if !res.OK {
		t.Error("recoverable rows are not tampering — OK must stay true")
	}
}
