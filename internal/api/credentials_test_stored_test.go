package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStored (POST /api/v1/credentials/{credentialId}/test) probes a
// stored credential's validity. The live-probe happy path makes a real
// network call, so here we cover the auth + lookup gates that run
// before the probe: 403 below the "update" tier, 404 for unknown ids.

func TestCredentialTestStored_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewCredentialHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/credentials/c1/test", nil)
	req.SetPathValue("credentialId", "c1")
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.TestStored(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCredentialTestStored_NotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewCredentialHandler(db, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/credentials/ghost/test", nil)
	req.SetPathValue("credentialId", "ghost")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.TestStored(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404; body=%s", rr.Code, rr.Body.String())
	}
}
