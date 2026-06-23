package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// journal_lookup_cov2_test.go — the per-section query failures of the
// lookup snapshot: crews succeed but agents fail, then crews+agents
// succeed but missions fail (one renamed table per test isolates the
// section). Helpers prefixed covJL2.

func covJL2Get(t *testing.T, breakTable string) *httptest.ResponseRecorder {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if breakTable != "" {
		execOrFatal(t, db, `ALTER TABLE `+breakTable+` RENAME TO `+breakTable+`_broken`)
	}
	h := NewJournalLookupHandler(db, newTestLogger())
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/journal/lookup", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	return rr
}

func TestCovJL2_Get_AgentsQueryError_500(t *testing.T) {
	rr := covJL2Get(t, "agents")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovJL2_Get_MissionsQueryError_500(t *testing.T) {
	rr := covJL2Get(t, "missions")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
