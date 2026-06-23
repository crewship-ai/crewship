package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// saved_view_handler_cov_test.go — remaining branches: the
// missing-user 401 on Update, ownership-lookup DB error, write
// failures via triggers, the read-back race, and the delete race
// (RAISE(IGNORE) makes the DELETE affect 0 rows after the owner
// check passed). Helpers prefixed covSV.

func covSVFixture(t *testing.T) (*SavedViewHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewSavedViewHandler(db, newTestLogger()), userID, wsID
}

func covSVSeedView(t *testing.T, h *SavedViewHandler, id, wsID, userID string) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO saved_views
		(id, workspace_id, user_id, name, filters_json, view_type, created_at)
		VALUES (?, ?, ?, 'V', '{}', 'list', datetime('now'))`, id, wsID, userID)
}

// TestCovSV_Update_NoUserInContext_401 — role check passes but the
// user object is missing (e.g. token middleware mismatch).
func TestCovSV_Update_NoUserInContext_401(t *testing.T) {
	h, _, wsID := covSVFixture(t)
	req := httptest.NewRequest("PATCH", "/api/v1/saved-views/v1", jsonBody(map[string]string{"name": "N"}))
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	req.SetPathValue("viewId", "v1")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovSV_Update_OwnershipLookupDBError_500(t *testing.T) {
	h, userID, wsID := covSVFixture(t)
	h.db.Close()
	req := withWorkspaceUser(
		httptest.NewRequest("PATCH", "/api/v1/saved-views/v1", jsonBody(map[string]string{"name": "N"})),
		userID, wsID, "OWNER")
	req.SetPathValue("viewId", "v1")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovSV_Update_ExecError_500(t *testing.T) {
	h, userID, wsID := covSVFixture(t)
	covSVSeedView(t, h, "covsv-v1", wsID, userID)
	execOrFatal(t, h.db, `CREATE TRIGGER covsv_block_upd BEFORE UPDATE ON saved_views
		BEGIN SELECT RAISE(ABORT, 'covsv forced'); END`)
	req := withWorkspaceUser(
		httptest.NewRequest("PATCH", "/api/v1/saved-views/covsv-v1", jsonBody(map[string]string{"name": "N"})),
		userID, wsID, "OWNER")
	req.SetPathValue("viewId", "covsv-v1")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovSV_Update_ReadBackRaces_500(t *testing.T) {
	h, userID, wsID := covSVFixture(t)
	covSVSeedView(t, h, "covsv-v2", wsID, userID)
	execOrFatal(t, h.db, `CREATE TRIGGER covsv_vanish AFTER UPDATE ON saved_views
		BEGIN DELETE FROM saved_views WHERE id = NEW.id; END`)
	req := withWorkspaceUser(
		httptest.NewRequest("PATCH", "/api/v1/saved-views/covsv-v2", jsonBody(map[string]string{"name": "N"})),
		userID, wsID, "OWNER")
	req.SetPathValue("viewId", "covsv-v2")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovSV_Delete_ExecError_500(t *testing.T) {
	h, userID, wsID := covSVFixture(t)
	covSVSeedView(t, h, "covsv-v3", wsID, userID)
	execOrFatal(t, h.db, `CREATE TRIGGER covsv_block_del BEFORE DELETE ON saved_views
		BEGIN SELECT RAISE(ABORT, 'covsv forced'); END`)
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/saved-views/covsv-v3", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("viewId", "covsv-v3")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovSV_Delete_ZeroRowsAfterOwnerCheck_404 — RAISE(IGNORE)
// silently skips the row delete, simulating a concurrent removal
// between the owner check and the DELETE.
func TestCovSV_Delete_ZeroRowsAfterOwnerCheck_404(t *testing.T) {
	h, userID, wsID := covSVFixture(t)
	covSVSeedView(t, h, "covsv-v4", wsID, userID)
	execOrFatal(t, h.db, `CREATE TRIGGER covsv_ignore_del BEFORE DELETE ON saved_views
		BEGIN SELECT RAISE(IGNORE); END`)
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/saved-views/covsv-v4", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("viewId", "covsv-v4")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (zero rows affected); body=%s", rr.Code, rr.Body.String())
	}
}
