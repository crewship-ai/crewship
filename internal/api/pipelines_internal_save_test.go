package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// InternalSave (POST /api/v1/internal/pipelines/save) is the trusted
// sidecar endpoint — auth runs upstream, so the handler itself only
// validates the body and the DSL.

func TestPipelineInternalSave_InvalidJSON(t *testing.T) {
	h := NewPipelineHandler(setupTestDB(t), slog.Default(), nil, nil)
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBufferString(`{bad`))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPipelineInternalSave_MissingFields(t *testing.T) {
	h := NewPipelineHandler(setupTestDB(t), slog.Default(), nil, nil)
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save",
		bytes.NewBufferString(`{"slug":"x"}`)) // no workspace_id / definition
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPipelineInternalSave_BadDefinition(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	// Name with spaces fails DSL validation → 422.
	body := `{"workspace_id":"` + wsID + `","slug":"bad","definition":{"name":"BAD NAME","steps":[]}}`
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("status=%d want 422; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPipelineInternalSave_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-pipe", wsID, "Eng", "eng")
	// InternalSave validates agent_slug references against the author
	// crew's agents, so the referenced slug must resolve to a real row.
	seedAgentRow(t, db, "a-pipe", wsID, crewID, "Lead", "agent_lead", "LEAD")

	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	freshTestRun := time.Now().UTC().Format(time.RFC3339)
	body := `{"workspace_id":"` + wsID + `","slug":"my-pipe","name":"My Pipe","author_crew_id":"` + crewID + `",` +
		`"last_test_run_passed":true,"last_test_run_at":"` + freshTestRun + `",` +
		`"definition":{"name":"my-pipe","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}}`
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["slug"] != "my-pipe" {
		t.Errorf("slug=%v want my-pipe", resp["slug"])
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM pipelines WHERE workspace_id = ? AND slug = 'my-pipe'", wsID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("pipeline rows=%d want 1", count)
	}
}
