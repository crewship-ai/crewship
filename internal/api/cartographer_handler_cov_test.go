package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cartographer_handler_cov_test.go — remaining branches: explicit
// ?limit=, invalid JSON bodies, capture/create/list failures via
// renamed tables and triggers, the reload-after-create degradation,
// the happy Restore (empty divergence normalization), and the generic
// 500s on Restore/Fork/Delete. Helpers prefixed covCart.

func covCartFixture(t *testing.T) (*CartographerHandler, string, string, string, string) {
	t.Helper()
	h, db, userID, wsID := cartographerRig(t)
	crewID, missionID := seedCartographerMission(t, db, wsID, "covcart-m1", "covcart-tr1")
	seedCartographerJournalEntry(t, db, "covcart-je1", wsID, missionID, time.Now().UTC())
	_ = crewID
	return h, userID, wsID, crewID, missionID
}

func TestCovCart_List_ExplicitLimitAndQueryError(t *testing.T) {
	h, userID, wsID, _, missionID := covCartFixture(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/missions/"+missionID+"/checkpoints?limit=5", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}

	// Break only the checkpoints table; resolveMission still works.
	execOrFatal(t, h.db, `ALTER TABLE checkpoints RENAME TO checkpoints_broken`)
	rr = httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCart_Create_InvalidJSONBody_400(t *testing.T) {
	h, userID, wsID, _, missionID := covCartFixture(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/missions/"+missionID+"/checkpoints",
			strings.NewReader("{nope")),
		userID, wsID, "OWNER")
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCart_Create_CaptureFailure_500(t *testing.T) {
	h, userID, wsID, _, missionID := covCartFixture(t)
	execOrFatal(t, h.db, `ALTER TABLE journal_entries RENAME TO je_broken`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/missions/"+missionID+"/checkpoints", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCart_Create_InsertFailure_500(t *testing.T) {
	h, userID, wsID, _, missionID := covCartFixture(t)
	execOrFatal(t, h.db, `CREATE TRIGGER covcart_block_ins BEFORE INSERT ON checkpoints
		BEGIN SELECT RAISE(ABORT, 'covcart forced'); END`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/missions/"+missionID+"/checkpoints",
			jsonBody(map[string]string{"label": "pre-deploy"})),
		userID, wsID, "OWNER")
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovCart_Create_ReloadFails_DegradesToMinimalBody — the insert
// commits but the read-back races a delete: 201 with {"id": ...} only.
func TestCovCart_Create_ReloadFails_DegradesToMinimalBody(t *testing.T) {
	h, userID, wsID, _, missionID := covCartFixture(t)
	execOrFatal(t, h.db, `CREATE TRIGGER covcart_vanish AFTER INSERT ON checkpoints
		BEGIN DELETE FROM checkpoints WHERE id = NEW.id; END`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/missions/"+missionID+"/checkpoints",
			jsonBody(map[string]string{"label": "ghost"})),
		userID, wsID, "OWNER")
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if id, _ := resp["id"].(string); id == "" {
		t.Errorf("resp = %v, want minimal {id} body", resp)
	}
	if _, hasLabel := resp["label"]; hasLabel {
		t.Errorf("resp = %v, want minimal body without full checkpoint fields", resp)
	}
}

// TestCovCart_Restore_HappyPath_EmptyDivergenceNormalized — restoring
// the newest checkpoint diverges from nothing; warn_divergence must be
// [] not null.
func TestCovCart_Restore_HappyPath_EmptyDivergenceNormalized(t *testing.T) {
	h, userID, wsID, crewID, missionID := covCartFixture(t)
	cpID := seedCheckpointDirect(t, h.db, wsID, crewID, missionID, "covcart-je1", "anchor")

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/checkpoints/"+cpID+"/restore", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("id", cpID)
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"warn_divergence":[]`) {
		t.Errorf("body = %s, want warn_divergence normalized to []", rr.Body.String())
	}
}

func TestCovCart_Restore_DBError_500(t *testing.T) {
	h, userID, wsID, _, _ := covCartFixture(t)
	h.db.Close()
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/checkpoints/cp-x/restore", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("id", "cp-x")
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCart_Fork_InvalidJSONBody_400(t *testing.T) {
	h, userID, wsID, _, _ := covCartFixture(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/checkpoints/cp-x/fork", strings.NewReader("{nope")),
		userID, wsID, "OWNER")
	req.SetPathValue("id", "cp-x")
	rr := httptest.NewRecorder()
	h.Fork(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCart_Fork_DBError_500(t *testing.T) {
	h, userID, wsID, _, _ := covCartFixture(t)
	h.db.Close()
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/checkpoints/cp-x/fork", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("id", "cp-x")
	rr := httptest.NewRecorder()
	h.Fork(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovCart_Delete_DBError_500(t *testing.T) {
	h, userID, wsID, _, _ := covCartFixture(t)
	h.db.Close()
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/checkpoints/cp-x", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("id", "cp-x")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
