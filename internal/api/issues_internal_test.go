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

func newInternalIssueHandler(t *testing.T) (*InternalIssueHandler, string, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	_ = userID
	_ = leadID
	return NewInternalIssueHandler(db, nil, logger), wsID, crewID, leadID, userID
}

func TestInternalIssue_Create_Success(t *testing.T) {
	h, wsID, crewID, _, _ := newInternalIssueHandler(t)

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","crew_id":"` + crewID + `","title":"Internal","priority":"high"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["identifier"] != "ENG-1" {
		t.Errorf("identifier = %q want ENG-1", resp["identifier"])
	}
}

func TestInternalIssue_Create_MissingFields(t *testing.T) {
	h, _, _, _, _ := newInternalIssueHandler(t)
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{}`))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestInternalIssue_Create_InvalidJSON(t *testing.T) {
	h, _, _, _, _ := newInternalIssueHandler(t)
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{`))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestInternalIssue_Create_CrewNotFound(t *testing.T) {
	h, wsID, _, _, _ := newInternalIssueHandler(t)
	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","crew_id":"missing","title":"x"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestInternalIssue_List_FullFilters(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "TODO")

	req := httptest.NewRequest("GET", "/?workspace_id="+wsID+"&crew_id="+crewID+"&status=BACKLOG,TODO&assignee_id=&mission_type=issue&limit=10", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got []issueResponse
	json.Unmarshal(rr.Body.Bytes(), &got)
	if len(got) != 2 {
		t.Errorf("got %d, want 2", len(got))
	}
}

func TestInternalIssue_List_NoWorkspace(t *testing.T) {
	h, _, _, _, _ := newInternalIssueHandler(t)
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestInternalIssue_Get(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-9", "BACKLOG")

	req := httptest.NewRequest("GET", "/?workspace_id="+wsID, nil)
	req.SetPathValue("identifier", "ENG-9")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestInternalIssue_Get_NotFound(t *testing.T) {
	h, wsID, _, _, _ := newInternalIssueHandler(t)
	req := httptest.NewRequest("GET", "/?workspace_id="+wsID, nil)
	req.SetPathValue("identifier", "MISSING")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestInternalIssue_Get_NoWorkspace(t *testing.T) {
	h, _, _, _, _ := newInternalIssueHandler(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestInternalIssue_UpdateStatus(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"TODO","priority":"high","comment":"hello","agent_id":"agent-worker"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestInternalIssue_UpdateStatus_InvalidTransition(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"DONE"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestInternalIssue_UpdateStatus_NotFound(t *testing.T) {
	h, wsID, _, _, _ := newInternalIssueHandler(t)
	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"TODO"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "MISS")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestInternalIssue_UpdateStatus_MissingWorkspace(t *testing.T) {
	h, _, _, _, _ := newInternalIssueHandler(t)
	body := bytes.NewBufferString(`{"status":"TODO"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestInternalIssue_CreateComment(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","agent_id":"agent-worker","body":"hello"}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.CreateComment(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestInternalIssue_CreateComment_MissingFields(t *testing.T) {
	h, _, _, _, _ := newInternalIssueHandler(t)
	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("identifier", "x")
	rr := httptest.NewRecorder()
	h.CreateComment(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestInternalIssue_CreateComment_NotFound(t *testing.T) {
	h, wsID, _, _, _ := newInternalIssueHandler(t)
	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","body":"x"}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("identifier", "MISS")
	rr := httptest.NewRecorder()
	h.CreateComment(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}
