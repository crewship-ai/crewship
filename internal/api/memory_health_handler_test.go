package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Get computes the live memory-health snapshot for the caller's
// workspace; an optional ?crew_id scopes it and must belong to the
// workspace (404 otherwise). 401 without a workspace on the context.

func TestMemoryHealthGet_RequiresWorkspace(t *testing.T) {
	h := NewMemoryHealthHandler(setupTestDB(t), newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/memory/health", nil)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestMemoryHealthGet_WorkspaceWide(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewMemoryHealthHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/memory/health", nil)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["workspace_id"] != wsID {
		t.Errorf("workspace_id=%v want %s", resp["workspace_id"], wsID)
	}
	if _, ok := resp["metrics"].(map[string]interface{}); !ok {
		t.Errorf("metrics block missing/wrong type: %v", resp["metrics"])
	}
}

func TestMemoryHealthGet_CrewScoped(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-mh", wsID, "Eng", "eng")

	h := NewMemoryHealthHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/memory/health?crew_id="+crewID, nil)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestMemoryHealthGet_CrewNotInWorkspace(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewMemoryHealthHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/memory/health?crew_id=ghost-crew", nil)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404; body=%s", rr.Code, rr.Body.String())
	}
}
