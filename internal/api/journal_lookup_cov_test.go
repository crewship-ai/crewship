package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// journal_lookup_cov_test.go covers the error branches of
// JournalLookupHandler.Get. The handler runs three sequential queries
// (crews, agents, missions); each test breaks exactly one of them:
//   - closed DB        -> crews query error
//   - dropped table    -> agents / missions query error
//   - lax table + NULL -> scan error inside the row loop
//
// Helpers prefixed covJL.

func covJLGet(t *testing.T, h *JournalLookupHandler, wsID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/journal/lookup", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	return rr
}

func TestCovJL_CrewQueryError(t *testing.T) {
	db := setupTestDB(t)
	h := NewJournalLookupHandler(db, newTestLogger())
	db.Close()
	rr := covJLGet(t, h, "ws-x")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestCovJL_CrewScanError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Replace crews with a lax clone holding a NULL name: scanning into
	// the non-nullable Name string fails -> 500.
	execOrFatal(t, db, `DROP TABLE crews`)
	execOrFatal(t, db, `CREATE TABLE crews (id TEXT, workspace_id TEXT, name TEXT, slug TEXT, icon TEXT, color TEXT, deleted_at TEXT)`)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('c1', ?, NULL, 's1')`, wsID)

	h := NewJournalLookupHandler(db, newTestLogger())
	rr := covJLGet(t, h, wsID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on crew scan error", rr.Code)
	}
}

func TestCovJL_AgentQueryError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `DROP TABLE agents`)

	h := NewJournalLookupHandler(db, newTestLogger())
	rr := covJLGet(t, h, wsID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on agents query error", rr.Code)
	}
}

func TestCovJL_AgentScanError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `DROP TABLE agents`)
	execOrFatal(t, db, `CREATE TABLE agents (id TEXT, workspace_id TEXT, name TEXT, slug TEXT, crew_id TEXT, avatar_seed TEXT, avatar_style TEXT, deleted_at TEXT)`)
	execOrFatal(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES ('a1', ?, NULL, 'a1')`, wsID)

	h := NewJournalLookupHandler(db, newTestLogger())
	rr := covJLGet(t, h, wsID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on agent scan error", rr.Code)
	}
}

func TestCovJL_MissionQueryError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `DROP TABLE missions`)

	h := NewJournalLookupHandler(db, newTestLogger())
	rr := covJLGet(t, h, wsID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on missions query error", rr.Code)
	}
}

func TestCovJL_MissionScanError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `DROP TABLE missions`)
	execOrFatal(t, db, `CREATE TABLE missions (id TEXT, workspace_id TEXT, title TEXT, status TEXT, created_at TEXT)`)
	execOrFatal(t, db, `INSERT INTO missions (id, workspace_id, title, status, created_at) VALUES ('m1', ?, NULL, 'ACTIVE', datetime('now'))`, wsID)

	h := NewJournalLookupHandler(db, newTestLogger())
	rr := covJLGet(t, h, wsID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on mission scan error", rr.Code)
	}
}
