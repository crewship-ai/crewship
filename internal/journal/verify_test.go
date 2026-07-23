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
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key-0123456789abcdef")

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
