package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// End-to-end guard for the chain-safe priority edit (#1369).
//
// The unit tests in internal/journal prove the reconciliation logic. This proves
// the actual HTTP handler participates correctly: an OWNER pinning an entry
// through PATCH .../priority must leave the workspace chain verifying, because it
// writes the ledger row in the same transaction as the column update.
//
// Before the fix this was impossible: priority was inside the hashed projection,
// so the very first pin permanently broke VerifyChain for that row and every
// `crewship journal verify` afterwards reported the workspace as tampered.

// seedChainForPriority emits n chained entries and returns their ids.
func seedChainForPriority(t *testing.T, h *JournalHandler, wsID string, n int) (*journal.Writer, []string) {
	t.Helper()
	w := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{FlushInterval: time.Hour})
	t.Cleanup(func() { _ = w.Close() })
	ctx := context.Background()
	var ids []string
	for i := 0; i < n; i++ {
		id, err := w.Emit(ctx, journal.Entry{
			WorkspaceID: wsID,
			Type:        journal.EntryRunStarted,
			ActorType:   journal.ActorAgent,
			Summary:     "seeded",
		})
		if err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return w, ids
}

func patchPriority(t *testing.T, h *JournalHandler, userID, wsID, entryID, priority string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"priority":"` + priority + `","reason":"operator decision"}`
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(body))
	req.SetPathValue("id", entryID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.SetPriority(rr, req)
	return rr
}

// TestJournalPriority_EditKeepsChainVerifiable is the headline regression.
func TestJournalPriority_EditKeepsChainVerifiable(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewJournalHandler(db, newTestLogger(), noopEmitter{})

	_, ids := seedChainForPriority(t, h, wsID, 4)

	if res, err := journal.VerifyChain(context.Background(), db, wsID); err != nil || !res.OK {
		t.Fatalf("baseline chain should verify; err=%v res=%+v", err, res)
	}

	if rr := patchPriority(t, h, userID, wsID, ids[1], "pin"); rr.Code != http.StatusOK {
		t.Fatalf("pin: status %d body=%s", rr.Code, rr.Body.String())
	}

	res, err := journal.VerifyChain(context.Background(), db, wsID)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !res.OK {
		t.Fatalf("an authorised pin broke chain verification: %+v", res)
	}
}

// TestJournalPriority_EditRecordsLedgerRow asserts the ledger row carries the
// provenance an audit needs: who, from what, to what, and why.
func TestJournalPriority_EditRecordsLedgerRow(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewJournalHandler(db, newTestLogger(), noopEmitter{})

	_, ids := seedChainForPriority(t, h, wsID, 2)
	if rr := patchPriority(t, h, userID, wsID, ids[0], "permanent"); rr.Code != http.StatusOK {
		t.Fatalf("set permanent: status %d body=%s", rr.Code, rr.Body.String())
	}

	var seq int
	var prev, next, reason, setBy string
	if err := db.QueryRow(`
		SELECT seq, previous_priority, priority, COALESCE(reason,''), COALESCE(set_by,'')
		  FROM journal_entry_priorities WHERE entry_id = ?`, ids[0]).
		Scan(&seq, &prev, &next, &reason, &setBy); err != nil {
		t.Fatalf("read ledger row: %v", err)
	}
	if seq != 1 {
		t.Errorf("seq = %d, want 1", seq)
	}
	if prev != "normal" || next != "permanent" {
		t.Errorf("transition = %s → %s, want normal → permanent", prev, next)
	}
	if reason != "operator decision" {
		t.Errorf("reason = %q, want the submitted reason", reason)
	}
	if setBy != userID {
		t.Errorf("set_by = %q, want the acting user %q", setBy, userID)
	}
}

// TestJournalPriority_SuccessiveEditsChain: repeated edits must produce a chained
// ledger and keep verifying, so an entry that gets pinned then un-pinned is not
// suddenly reported as tampered.
func TestJournalPriority_SuccessiveEditsChain(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewJournalHandler(db, newTestLogger(), noopEmitter{})

	_, ids := seedChainForPriority(t, h, wsID, 2)
	for _, p := range []string{"high", "pin", "normal"} {
		if rr := patchPriority(t, h, userID, wsID, ids[0], p); rr.Code != http.StatusOK {
			t.Fatalf("set %s: status %d body=%s", p, rr.Code, rr.Body.String())
		}
	}

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM journal_entry_priorities WHERE entry_id = ?`, ids[0]).Scan(&n); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if n != 3 {
		t.Fatalf("ledger rows = %d, want 3 (one per edit — history, not last-write-wins)", n)
	}

	res, err := journal.VerifyChain(context.Background(), db, wsID)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !res.OK {
		t.Fatalf("a sequence of authorised edits broke verification: %+v", res)
	}
}

// TestJournalPriority_ForeignWorkspaceLeavesNoLedgerRow: the 404 path must not
// write history for a row it did not touch, or a cross-tenant probe would
// pollute another workspace's audit trail.
func TestJournalPriority_ForeignWorkspaceLeavesNoLedgerRow(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewJournalHandler(db, newTestLogger(), noopEmitter{})

	_, ids := seedChainForPriority(t, h, wsID, 1)

	// Same entry id, a workspace that does not own it.
	if rr := patchPriority(t, h, userID, "ws-other", ids[0], "pin"); rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace pin: status %d, want 404 (body=%s)", rr.Code, rr.Body.String())
	}

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM journal_entry_priorities WHERE entry_id = ?`, ids[0]).Scan(&n); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if n != 0 {
		t.Fatalf("a rejected cross-workspace edit wrote %d ledger rows, want 0", n)
	}
	var live string
	if err := db.QueryRow(`SELECT priority FROM journal_entries WHERE id = ?`, ids[0]).Scan(&live); err != nil {
		t.Fatalf("read priority: %v", err)
	}
	if live != "normal" {
		t.Errorf("priority = %q after a rejected edit, want normal", live)
	}
}

// TestJournalPriority_ConcurrentEditsStillChain is the regression for a race in
// recordPriorityChange: previous_priority used to come from the handler's
// `journal.Get`, which ran BEFORE the transaction opened. Two concurrent
// SetPriority calls on one entry both read the same stale value, so the second
// ledger row did not chain from the first row's result — and VerifyChain's
// reconciliation then reported tampering on two legitimate, sequential,
// authorised edits. That is exactly the false positive this feature exists to
// remove, so the ledger has to be self-consistent under concurrency.
//
// previous_priority is now read inside the same transaction as the UPDATE.
func TestJournalPriority_ConcurrentEditsStillChain(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewJournalHandler(db, newTestLogger(), noopEmitter{})

	_, ids := seedChainForPriority(t, h, wsID, 2)
	target := ids[0]

	// Two operators racing on the same entry. Both must land, and the two ledger
	// rows must form a chain (normal -> X -> Y) regardless of who won.
	const editors = 4
	var wg sync.WaitGroup
	codes := make([]int, editors)
	prios := []string{"high", "pin", "permanent", "normal"}
	wg.Add(editors)
	for i := 0; i < editors; i++ {
		go func(i int) {
			defer wg.Done()
			codes[i] = patchPriority(t, h, userID, wsID, target, prios[i]).Code
		}(i)
	}
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("editor %d: status %d, want 200", i, c)
		}
	}

	// The ledger must be a chain: each row's previous_priority equals the prior
	// row's priority, starting from the emit-time value.
	rows, err := db.Query(
		`SELECT seq, previous_priority, priority FROM journal_entry_priorities
		  WHERE entry_id = ? ORDER BY seq`, target)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	defer rows.Close()
	cur := "normal" // seedChainForPriority emits at the default priority
	n := 0
	for rows.Next() {
		var seq int
		var prev, next string
		if err := rows.Scan(&seq, &prev, &next); err != nil {
			t.Fatalf("scan: %v", err)
		}
		n++
		if seq != n {
			t.Errorf("row %d has seq %d, want %d", n, seq, n)
		}
		if prev != cur {
			t.Fatalf("ledger row %d claims previous_priority=%q but the chain is at %q — concurrent edits produced a non-chaining ledger",
				seq, prev, cur)
		}
		cur = next
	}
	if n != editors {
		t.Fatalf("ledger rows = %d, want %d (one per edit)", n, editors)
	}

	// The live value must be the tail of that chain.
	var live string
	if err := db.QueryRow(`SELECT priority FROM journal_entries WHERE id = ?`, target).Scan(&live); err != nil {
		t.Fatalf("read live priority: %v", err)
	}
	if live != cur {
		t.Errorf("live priority %q != ledger tail %q", live, cur)
	}

	// And the whole point: verification must still be clean.
	res, err := journal.VerifyChain(context.Background(), db, wsID)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !res.OK {
		t.Fatalf("two concurrent authorised edits broke verification: %+v", res)
	}
}
