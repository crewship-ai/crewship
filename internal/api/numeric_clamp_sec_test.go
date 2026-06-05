package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSecClampRunsPageOverflow drives the runs list with an absurdly large
// `page` value. Before the upper clamp, offset = (page-1)*limit overflows a
// signed int to a negative number, confusing pagination (and potentially the
// SQL OFFSET). After the clamp the handler must not panic and must report a
// non-negative, sane page in the response.
func TestSecClampRunsPageOverflow(t *testing.T) {
	f := newRunsTestFixture(t)

	req := httptest.NewRequest("GET", "/api/v1/runs?page=99999999999&limit=50", nil)
	req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	f.h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp runListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// page must stay clamped (not echo the overflowing value) and be positive.
	if resp.Pagination.Page < 1 {
		t.Errorf("page=%d want >= 1", resp.Pagination.Page)
	}
	if resp.Pagination.Page > 1_000_000 {
		t.Errorf("page=%d not clamped to maxPage", resp.Pagination.Page)
	}
}

// TestSecClampAuditPageOverflow is the same overflow probe for the audit log
// list, which shares the (page-1)*limit pagination idiom.
func TestSecClampAuditPageOverflow(t *testing.T) {
	h, db := newAuditHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("GET", "/api/v1/audit?page=99999999999&limit=50", nil)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp auditListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Pagination.Page < 1 {
		t.Errorf("page=%d want >= 1", resp.Pagination.Page)
	}
	if resp.Pagination.Page > 1_000_000 {
		t.Errorf("page=%d not clamped to maxPage", resp.Pagination.Page)
	}
}

// TestSecClampCreateTaskMaxIterations rejects an out-of-range max_iterations on
// task creation with 400. The runtime loop (loop.go ShouldRetry) honours the
// configured value, so an absurd cap must be refused at the input boundary.
func TestSecClampCreateTaskMaxIterations(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)

	body := bytes.NewBufferString(`{"title":"Step 1","max_iterations":10000}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestSecClampCreateTaskMaxIterationsNegative rejects a negative configured cap.
func TestSecClampCreateTaskMaxIterationsNegative(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)

	body := bytes.NewBufferString(`{"title":"Step 1","max_iterations":-1}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestSecClampCreateTaskMaxIterationsValid keeps an in-range value working so
// the validation can't be satisfied by rejecting everything.
func TestSecClampCreateTaskMaxIterationsValid(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)

	body := bytes.NewBufferString(`{"title":"Step 1","max_iterations":5}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201; body=%s", rr.Code, rr.Body.String())
	}
}

// TestSecClampUpdateTaskMaxIterations rejects an out-of-range max_iterations on
// task update with 400.
func TestSecClampUpdateTaskMaxIterations(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)

	// Create a task to update.
	createBody := bytes.NewBufferString(`{"title":"Step 1"}`)
	createReq := httptest.NewRequest("POST", "/", createBody)
	createReq.SetPathValue("crewId", crewID)
	createReq.SetPathValue("missionId", missionID)
	createReq = createReq.WithContext(withWorkspace(withUser(createReq.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	createRR := httptest.NewRecorder()
	h.CreateTask(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRR.Code, createRR.Body.String())
	}
	var created missionTaskResponse
	json.Unmarshal(createRR.Body.Bytes(), &created)

	body := bytes.NewBufferString(`{"max_iterations":10000}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", created.ID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}
