package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func newProjectHandler(t *testing.T) (*ProjectHandler, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewProjectHandler(db, nil, logger), userID, wsID, ""
}

func TestProject_Create(t *testing.T) {
	h, userID, wsID, _ := newProjectHandler(t)

	body := bytes.NewBufferString(`{"name":"Apollo","description":"moon"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp projectResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Slug != "apollo" {
		t.Errorf("slug = %q want apollo", resp.Slug)
	}
}

func TestProject_Create_NoName(t *testing.T) {
	h, userID, wsID, _ := newProjectHandler(t)
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{}`))
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestProject_Create_Forbidden(t *testing.T) {
	h, userID, wsID, _ := newProjectHandler(t)
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"name":"x"}`))
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestProject_Create_BadJSON(t *testing.T) {
	h, userID, wsID, _ := newProjectHandler(t)
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{`))
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestProject_List_AndGet(t *testing.T) {
	h, userID, wsID, _ := newProjectHandler(t)
	pid := seedProject(t, h.db, wsID, "alpha")
	seedProject(t, h.db, wsID, "beta")

	// List
	req := httptest.NewRequest("GET", "/?status=planned&sort=created_at", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d", rr.Code)
	}
	var ps []projectResponse
	json.Unmarshal(rr.Body.Bytes(), &ps)
	if len(ps) != 2 {
		t.Errorf("got %d projects, want 2", len(ps))
	}

	// Get
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.SetPathValue("projectId", pid)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.Get(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("get: %d", rr2.Code)
	}
}

func TestProject_Get_NotFound(t *testing.T) {
	h, userID, wsID, _ := newProjectHandler(t)

	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("projectId", "ghost")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestProject_Update(t *testing.T) {
	h, userID, wsID, _ := newProjectHandler(t)
	pid := seedProject(t, h.db, wsID, "alpha")

	body := bytes.NewBufferString(`{"name":"Alpha Renamed","status":"in_progress","priority":"high","health":"at_risk","color":"red","description":"x","start_date":"2030-01-01","target_date":"2030-12-01"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("projectId", pid)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp projectResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Name != "Alpha Renamed" {
		t.Errorf("name = %q", resp.Name)
	}
}

func TestProject_Update_NoFields(t *testing.T) {
	h, userID, wsID, _ := newProjectHandler(t)
	pid := seedProject(t, h.db, wsID, "alpha")

	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{}`))
	req.SetPathValue("projectId", pid)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestProject_Update_NotFound(t *testing.T) {
	h, userID, wsID, _ := newProjectHandler(t)

	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"name":"x"}`))
	req.SetPathValue("projectId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestProject_Delete(t *testing.T) {
	h, userID, wsID, _ := newProjectHandler(t)
	pid := seedProject(t, h.db, wsID, "alpha")

	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("projectId", pid)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rr.Code)
	}
}

func TestProject_Delete_NotFound(t *testing.T) {
	h, userID, wsID, _ := newProjectHandler(t)

	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("projectId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestProject_Delete_Forbidden(t *testing.T) {
	h, userID, wsID, _ := newProjectHandler(t)

	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("projectId", "anything")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "MANAGER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestProject_Stats(t *testing.T) {
	h, userID, wsID, _ := newProjectHandler(t)
	pid := seedProject(t, h.db, wsID, "alpha")

	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("projectId", pid)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Stats(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestProject_Stats_NotFound(t *testing.T) {
	h, userID, wsID, _ := newProjectHandler(t)

	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("projectId", "ghost")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Stats(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// ── Milestone handler ────────────────────────────────────────────────

func newMilestoneHandler(t *testing.T) (*MilestoneHandler, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	pid := seedProject(t, db, wsID, "milestone-test")
	return NewMilestoneHandler(db, nil, logger), userID, wsID, pid
}

func TestMilestone_CRUD(t *testing.T) {
	h, userID, wsID, pid := newMilestoneHandler(t)

	// Create
	body := bytes.NewBufferString(`{"name":"v1","description":"first","target_date":"2030-01-01","status":"active"}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("projectId", pid)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", rr.Code, rr.Body.String())
	}
	var ms milestoneResponse
	json.Unmarshal(rr.Body.Bytes(), &ms)

	// List
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.SetPathValue("projectId", pid)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.List(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("list: %d", rr2.Code)
	}
	var list []milestoneResponse
	json.Unmarshal(rr2.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Errorf("list len = %d want 1", len(list))
	}

	// Update
	body3 := bytes.NewBufferString(`{"name":"v1.0","description":"updated","target_date":"2030-02-01","status":"completed","position":2}`)
	req3 := httptest.NewRequest("PATCH", "/", body3)
	req3.SetPathValue("milestoneId", ms.ID)
	req3 = req3.WithContext(withWorkspace(withUser(req3.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr3 := httptest.NewRecorder()
	h.Update(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("update: %d body=%s", rr3.Code, rr3.Body.String())
	}

	// Delete
	req4 := httptest.NewRequest("DELETE", "/", nil)
	req4.SetPathValue("milestoneId", ms.ID)
	req4 = req4.WithContext(withWorkspace(withUser(req4.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr4 := httptest.NewRecorder()
	h.Delete(rr4, req4)
	if rr4.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rr4.Code)
	}
}

func TestMilestone_Create_NoName(t *testing.T) {
	h, userID, wsID, pid := newMilestoneHandler(t)
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{}`))
	req.SetPathValue("projectId", pid)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestMilestone_Create_ProjectNotFound(t *testing.T) {
	h, userID, wsID, _ := newMilestoneHandler(t)
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"name":"x"}`))
	req.SetPathValue("projectId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestMilestone_Create_Forbidden(t *testing.T) {
	h, userID, wsID, pid := newMilestoneHandler(t)
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"name":"x"}`))
	req.SetPathValue("projectId", pid)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestMilestone_List_ProjectNotFound(t *testing.T) {
	h, userID, wsID, _ := newMilestoneHandler(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("projectId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestMilestone_Update_NotFound(t *testing.T) {
	h, userID, wsID, _ := newMilestoneHandler(t)
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"name":"x"}`))
	req.SetPathValue("milestoneId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestMilestone_Delete_NotFound(t *testing.T) {
	h, userID, wsID, _ := newMilestoneHandler(t)
	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("milestoneId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}
