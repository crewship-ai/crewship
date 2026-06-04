package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// handleSidecarCostRecord persists a cost_ledger row submitted by the
// sidecar (internal X-Internal-Token surface). Validation rejects empty
// scope/provider/model and unknown billing modes; the happy path returns
// 202 and writes the ledger row.

func costRouter(t *testing.T) (*Router, *emitRecorder) {
	t.Helper()
	em := &emitRecorder{}
	return &Router{db: setupTestDB(t), logger: newTestLogger(), journal: em}, em
}

func postCost(t *testing.T, r *Router, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/internal/cost/record", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	r.handleSidecarCostRecord(rr, req)
	return rr
}

func TestHandleSidecarCostRecord_InvalidJSON(t *testing.T) {
	r, _ := costRouter(t)
	if rr := postCost(t, r, `{bad`); rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleSidecarCostRecord_MissingWorkspace(t *testing.T) {
	r, _ := costRouter(t)
	if rr := postCost(t, r, `{"provider":"anthropic","model":"claude"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleSidecarCostRecord_MissingProviderModel(t *testing.T) {
	r, _ := costRouter(t)
	if rr := postCost(t, r, `{"workspace_id":"ws1","provider":"anthropic"}`); rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleSidecarCostRecord_BadBillingMode(t *testing.T) {
	r, _ := costRouter(t)
	body := `{"workspace_id":"ws1","provider":"anthropic","model":"claude","billing_mode":"bogus"}`
	if rr := postCost(t, r, body); rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleSidecarCostRecord_HappyPath(t *testing.T) {
	em := &emitRecorder{}
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-cost", wsID, "Eng", "eng")
	agentID := seedAgentRow(t, db, "a-cost", wsID, crewID, "Cassie", "cassie", "AGENT")
	r := &Router{db: db, logger: newTestLogger(), journal: em}

	body := `{"workspace_id":"` + wsID + `","crew_id":"` + crewID + `","agent_id":"` + agentID + `",` +
		`"provider":"anthropic","model":"claude-sonnet-4-6","input_tokens":1000,"output_tokens":500,` +
		`"billing_mode":"metered"}`
	rr := postCost(t, r, body)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d want 202; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["id"] == "" {
		t.Errorf("expected ledger id, got %v", resp)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM cost_ledger WHERE workspace_id = ?", wsID).Scan(&count); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if count != 1 {
		t.Errorf("cost_ledger rows=%d want 1", count)
	}
}
