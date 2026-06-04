package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Fork (POST /api/v1/checkpoints/{id}/fork) anchors a new mission +
// checkpoint at a source checkpoint's cursor. Delete (DELETE
// /api/v1/checkpoints/{id}) removes a checkpoint (204), masking
// cross-workspace ids as 404.

func TestCartographerFork_RequiresWorkspace(t *testing.T) {
	h, _, _, _ := cartographerRig(t)
	req := httptest.NewRequest("POST", "/api/v1/checkpoints/cp1/fork", nil)
	req.SetPathValue("id", "cp1")
	rr := httptest.NewRecorder()
	h.Fork(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCartographerFork_NotFound(t *testing.T) {
	h, _, userID, wsID := cartographerRig(t)
	req := httptest.NewRequest("POST", "/api/v1/checkpoints/ghost/fork", nil)
	req.SetPathValue("id", "ghost")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.Fork(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCartographerFork_HappyPath(t *testing.T) {
	h, db, userID, wsID := cartographerRig(t)
	crewID, missionID := seedCartographerMission(t, db, wsID, "mis_fork", "tr_fork")
	cpID := seedCheckpointDirect(t, db, wsID, crewID, missionID, "je_fork_1", "source")

	body := bytes.NewBufferString(`{"label":"forked branch"}`)
	req := httptest.NewRequest("POST", "/api/v1/checkpoints/"+cpID+"/fork", body)
	req.SetPathValue("id", cpID)
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.Fork(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["new_mission_id"] == "" || resp["new_checkpoint_id"] == "" {
		t.Errorf("expected new ids, got %v", resp)
	}
}

func TestCartographerDelete_RequiresWorkspace(t *testing.T) {
	h, _, _, _ := cartographerRig(t)
	req := httptest.NewRequest("DELETE", "/api/v1/checkpoints/cp1", nil)
	req.SetPathValue("id", "cp1")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCartographerDelete_NotFound(t *testing.T) {
	h, _, userID, wsID := cartographerRig(t)
	req := httptest.NewRequest("DELETE", "/api/v1/checkpoints/ghost", nil)
	req.SetPathValue("id", "ghost")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCartographerDelete_HappyPath(t *testing.T) {
	h, db, userID, wsID := cartographerRig(t)
	crewID, missionID := seedCartographerMission(t, db, wsID, "mis_del", "tr_del")
	cpID := seedCheckpointDirect(t, db, wsID, crewID, missionID, "je_del_1", "to delete")

	req := httptest.NewRequest("DELETE", "/api/v1/checkpoints/"+cpID, nil)
	req.SetPathValue("id", cpID)
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204; body=%s", rr.Code, rr.Body.String())
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM checkpoints WHERE id = ?", cpID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("checkpoint still present after delete")
	}
}
