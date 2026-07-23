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

// TestPipelineInternalSave_ForgedTestGate_Rejected pins the #1371 fix: the
// unattended (agent/sidecar) save path must NOT trust the body's forgeable
// "it passed" claim. Without a valid internally-minted save_token — the
// cryptographic proof that a dry-run actually ran for THIS definition — a
// forged last_test_run_passed=true must be rejected (422) and persist nothing,
// exactly as the interactive user path rejects it. Autonomous ≥ interactive.
func TestPipelineInternalSave_ForgedTestGate_Rejected(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-forge", wsID, "Eng", "eng")
	seedAgentRow(t, db, "a-forge", wsID, crewID, "Lead", "agent_lead", "LEAD")

	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	h.SetSaveTokenSecret(internalSaveTestSecret)

	freshTestRun := time.Now().UTC().Format(time.RFC3339)
	body := `{"workspace_id":"` + wsID + `","slug":"forged","name":"Forged","author_crew_id":"` + crewID + `",` +
		`"last_test_run_passed":true,"last_test_run_at":"` + freshTestRun + `",` +
		`"definition":{"name":"forged","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}}`
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422 (forged test-gate must not save); body=%s", rr.Code, rr.Body.String())
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM pipelines WHERE workspace_id = ? AND slug = 'forged'", wsID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("pipeline rows=%d want 0 (forged claim must not persist)", count)
	}
}

// TestPipelineInternalSave_TokenRoundTrip drives the real two-step flow: the
// internal test_run mints a save_token, and InternalSave accepts THAT token
// for the same definition + crew (#1371). It proves the mint side and the
// verify side agree on the definition hash and the crew principal end-to-end.
func TestPipelineInternalSave_TokenRoundTrip(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	crewID := seedCrewRow(t, h.db, "c-rt", wsID, "Eng", "eng-rt")
	seedAgentRow(t, h.db, "a-rt", wsID, crewID, "Eva", "eva", "LEAD")

	def := `{"name":"rt-pipe","steps":[{"id":"a","type":"agent_run","agent_slug":"eva","prompt":"hi"}]}`

	// Step 1 — internal test_run mints the token.
	trBody := `{"workspace_id":"` + wsID + `","author_crew_id":"` + crewID + `","definition":` + def + `,"sample_inputs":{}}`
	trReq := httptest.NewRequest("POST", "/api/v1/internal/pipelines/test_run", bytes.NewBufferString(trBody))
	trReq.ContentLength = int64(len(trBody))
	trRR := httptest.NewRecorder()
	h.InternalTestRun(trRR, trReq)
	if trRR.Code != http.StatusOK {
		t.Fatalf("test_run status=%d want 200; body=%s", trRR.Code, trRR.Body.String())
	}
	var tr struct {
		Status    string `json:"status"`
		SaveToken string `json:"save_token"`
	}
	if err := json.Unmarshal(trRR.Body.Bytes(), &tr); err != nil {
		t.Fatalf("decode test_run: %v", err)
	}
	if tr.SaveToken == "" {
		t.Fatalf("test_run minted no save_token (status=%q)", tr.Status)
	}

	// Step 2 — InternalSave accepts that token for the same definition.
	saveBody := `{"workspace_id":"` + wsID + `","slug":"rt-pipe","name":"RT","author_crew_id":"` + crewID + `",` +
		`"save_token":"` + tr.SaveToken + `","definition":` + def + `}`
	saveReq := httptest.NewRequest("POST", "/api/v1/internal/pipelines/save", bytes.NewBufferString(saveBody))
	saveRR := httptest.NewRecorder()
	h.InternalSave(saveRR, saveReq)
	if saveRR.Code != http.StatusCreated {
		t.Fatalf("save status=%d want 201 (round-trip token must clear the gate); body=%s", saveRR.Code, saveRR.Body.String())
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
	h.SetSaveTokenSecret(internalSaveTestSecret)
	def := `{"name":"my-pipe","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`
	token := internalSaveTokenFor(wsID, crewID, def)
	body := `{"workspace_id":"` + wsID + `","slug":"my-pipe","name":"My Pipe","author_crew_id":"` + crewID + `",` +
		`"save_token":"` + token + `",` +
		`"definition":` + def + `}`
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
