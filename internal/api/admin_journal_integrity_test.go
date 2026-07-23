package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// TestJournalIntegrity_VerifyDetectsTamper drives the admin verify handler:
// a clean chain reports ok=true; mutating a row makes the same endpoint
// report ok=false with the offending seq — the API↔CLI-parity contract the
// `crewship journal verify` command consumes.
func TestJournalIntegrity_VerifyDetectsTamper(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	w := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()
	var lastID string
	for i := 0; i < 4; i++ {
		id, err := w.Emit(ctx, journal.Entry{
			WorkspaceID: wsID,
			Type:        journal.EntryRunStarted,
			ActorType:   journal.ActorAgent,
			Summary:     "seeded",
		})
		if err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
		lastID = id
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	h := NewJournalIntegrityHandler(db, newTestLogger())

	// Clean chain → ok=true.
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/admin/journal/verify", nil), userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Verify(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("clean: status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var clean journal.VerifyResult
	if err := json.Unmarshal(rr.Body.Bytes(), &clean); err != nil {
		t.Fatalf("decode clean: %v", err)
	}
	if !clean.OK || clean.Count != 4 {
		t.Fatalf("clean: ok=%v count=%d reason=%q", clean.OK, clean.Count, clean.Reason)
	}

	// Tamper the last entry → ok=false.
	if _, err := db.Exec(`UPDATE journal_entries SET summary = 'HACKED' WHERE id = ?`, lastID); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	req = withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/admin/journal/verify", nil), userID, wsID, "ADMIN")
	rr = httptest.NewRecorder()
	h.Verify(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("tampered: status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var broken journal.VerifyResult
	if err := json.Unmarshal(rr.Body.Bytes(), &broken); err != nil {
		t.Fatalf("decode broken: %v", err)
	}
	if broken.OK {
		t.Fatalf("tampered chain reported OK")
	}
	if broken.BrokenID != lastID {
		t.Fatalf("break at %s; want %s (reason=%q)", broken.BrokenID, lastID, broken.Reason)
	}
}

// TestJournalIntegrity_RequiresAdmin: a MEMBER cannot run the check.
func TestJournalIntegrity_RequiresAdmin(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewJournalIntegrityHandler(db, newTestLogger())
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/admin/journal/verify", nil), userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Verify(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 403", rr.Code)
	}
}
