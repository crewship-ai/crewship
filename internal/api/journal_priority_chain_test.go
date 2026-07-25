package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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
