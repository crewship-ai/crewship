package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// journal_handler_cov2_test.go — second pass: List/Get/Count DB
// failures, the Count bad-filter 400, and the SetPriority write
// failure / lost race / best-effort audit emit. Helpers prefixed
// covJH2.

type covJH2FailEmitter struct{}

func (covJH2FailEmitter) Emit(_ context.Context, _ journal.Entry) (string, error) {
	return "", errors.New("audit sink down")
}
func (covJH2FailEmitter) Flush(_ context.Context) error { return nil }

func covJH2Fixture(t *testing.T) (*JournalHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewJournalHandler(db, newTestLogger(), noopEmitter{})
	return h, userID, wsID
}

func covJH2SeedEntry(t *testing.T, h *JournalHandler, id, wsID string) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO journal_entries
		(id, workspace_id, ts, entry_type, severity, priority, actor_type, summary, payload, refs)
		VALUES (?, ?, ?, 'peer.conversation', 'info', 'normal', 'agent', 'seeded', '{}', '{}')`,
		id, wsID, time.Now().UTC().Format(time.RFC3339Nano))
}

func TestCovJH2_List_DBError_500(t *testing.T) {
	h, userID, wsID := covJH2Fixture(t)
	execOrFatal(t, h.db, `ALTER TABLE journal_entries RENAME TO je_broken`)
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/journal", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovJH2_Get_DBError_500(t *testing.T) {
	h, userID, wsID := covJH2Fixture(t)
	h.db.Close()
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/journal/je-1", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("id", "je-1")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovJH2_Count_BadFilter_400(t *testing.T) {
	h, userID, wsID := covJH2Fixture(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/journal/count?since=not-a-timestamp", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Count(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "bad since") {
		t.Errorf("body = %s, want bad since error", rr.Body.String())
	}
}

func TestCovJH2_Count_DBError_500(t *testing.T) {
	h, userID, wsID := covJH2Fixture(t)
	execOrFatal(t, h.db, `ALTER TABLE journal_entries RENAME TO je_broken`)
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/journal/count", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Count(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func covJH2SetPriority(h *JournalHandler, userID, wsID, entryID string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/journal/"+entryID+"/priority",
			jsonBody(map[string]string{"priority": "high", "reason": "important"})),
		userID, wsID, "OWNER")
	req.SetPathValue("id", entryID)
	rr := httptest.NewRecorder()
	h.SetPriority(rr, req)
	return rr
}

func TestCovJH2_SetPriority_LookupDBError_500(t *testing.T) {
	h, userID, wsID := covJH2Fixture(t)
	h.db.Close()
	rr := covJH2SetPriority(h, userID, wsID, "je-x")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovJH2_SetPriority_UpdateError_500(t *testing.T) {
	h, userID, wsID := covJH2Fixture(t)
	covJH2SeedEntry(t, h, "covjh2-e1", wsID)
	execOrFatal(t, h.db, `CREATE TRIGGER covjh2_block_upd BEFORE UPDATE ON journal_entries
		BEGIN SELECT RAISE(ABORT, 'covjh2 forced'); END`)
	rr := covJH2SetPriority(h, userID, wsID, "covjh2-e1")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovJH2_SetPriority_LostRace_404(t *testing.T) {
	h, userID, wsID := covJH2Fixture(t)
	covJH2SeedEntry(t, h, "covjh2-e2", wsID)
	execOrFatal(t, h.db, `CREATE TRIGGER covjh2_ignore_upd BEFORE UPDATE ON journal_entries
		BEGIN SELECT RAISE(IGNORE); END`)
	rr := covJH2SetPriority(h, userID, wsID, "covjh2-e2")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovJH2_SetPriority_AuditEmitFailure_NonFatal — the UPDATE
// landed; the audit emit failing only logs, and the response carries
// the previous priority.
func TestCovJH2_SetPriority_AuditEmitFailure_NonFatal(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewJournalHandler(db, newTestLogger(), covJH2FailEmitter{})
	covJH2SeedEntry(t, h, "covjh2-e3", wsID)

	rr := covJH2SetPriority(h, userID, wsID, "covjh2-e3")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"previous":"normal"`) {
		t.Errorf("body = %s, want previous priority echoed", rr.Body.String())
	}
	var prio string
	if err := db.QueryRow(`SELECT priority FROM journal_entries WHERE id = 'covjh2-e3'`).Scan(&prio); err != nil || prio != "high" {
		t.Errorf("priority = %q err=%v, want high", prio, err)
	}
}
