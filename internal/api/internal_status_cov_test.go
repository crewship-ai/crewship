package api

// Coverage tests for internal_status.go: CreateCrew / CreateAgent
// validation + DB failure branches, and the RecordMCPToolCall
// bound-token foreign-crew rejection (PR-F24 foreign-ID closure).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newInternalStatusCovHandler(t *testing.T) (*InternalHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInternalHandler(db, "internal-token", newTestLogger())
	return h, wsID, userID
}

func TestInternalCreateCrew_SlugUnderivable_400(t *testing.T) {
	h, wsID, _ := newInternalStatusCovHandler(t)

	req := httptest.NewRequest("POST", "/api/v1/internal/crews?workspace_id="+wsID,
		strings.NewReader(`{"name":"!!!"}`))
	rr := httptest.NewRecorder()
	h.CreateCrew(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "slug is required") {
		t.Errorf("body = %s, want slug-underivable error", rr.Body.String())
	}
}

func TestInternalCreateCrew_SlugCheckDBError_500(t *testing.T) {
	h, wsID, _ := newInternalStatusCovHandler(t)
	h.db.Close()

	req := httptest.NewRequest("POST", "/api/v1/internal/crews?workspace_id="+wsID,
		strings.NewReader(`{"name":"Engineering"}`))
	rr := httptest.NewRecorder()
	h.CreateCrew(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestInternalCreateCrew_InsertFKViolation_500(t *testing.T) {
	h, _, _ := newInternalStatusCovHandler(t)

	// Nonexistent workspace: the slug-uniqueness COUNT succeeds (0
	// rows) but the INSERT trips the crews.workspace_id FK.
	req := httptest.NewRequest("POST", "/api/v1/internal/crews?workspace_id=ghost-ws",
		strings.NewReader(`{"name":"Phantom Crew"}`))
	rr := httptest.NewRecorder()
	h.CreateCrew(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "failed to create crew") {
		t.Errorf("body = %s, want 'failed to create crew'", rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM crews WHERE workspace_id = 'ghost-ws'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("crews rows for ghost-ws = %d, want 0", n)
	}
}

func TestInternalCreateAgent_SlugUnderivable_400(t *testing.T) {
	h, wsID, _ := newInternalStatusCovHandler(t)
	crewID := seedCrewRow(t, h.db, "crew-cov-1", wsID, "Crew Cov", "crew-cov")

	req := httptest.NewRequest("POST", "/api/v1/internal/agents?workspace_id="+wsID,
		strings.NewReader(`{"name":"###","crew_id":"`+crewID+`"}`))
	rr := httptest.NewRecorder()
	h.CreateAgent(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "slug is required") {
		t.Errorf("body = %s, want slug-underivable error", rr.Body.String())
	}
}

func TestInternalCreateAgent_UnknownCrew_InsertFKViolation_500(t *testing.T) {
	h, wsID, _ := newInternalStatusCovHandler(t)

	// Unknown crew: the slug lookup warns (ErrNoRows) and the slug is
	// NOT suffixed; the INSERT then trips the agents.crew_id FK.
	req := httptest.NewRequest("POST", "/api/v1/internal/agents?workspace_id="+wsID,
		strings.NewReader(`{"name":"Ghost Agent","crew_id":"no-such-crew"}`))
	rr := httptest.NewRecorder()
	h.CreateAgent(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "failed to create agent") {
		t.Errorf("body = %s, want 'failed to create agent'", rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE crew_id = 'no-such-crew'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("agents rows for no-such-crew = %d, want 0", n)
	}
}

func TestInternalCreateAgent_SlugCheckDBError_500(t *testing.T) {
	h, wsID, _ := newInternalStatusCovHandler(t)
	h.db.Close()

	req := httptest.NewRequest("POST", "/api/v1/internal/agents?workspace_id="+wsID,
		strings.NewReader(`{"name":"Eva","crew_id":"crew-x"}`))
	rr := httptest.NewRecorder()
	h.CreateAgent(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestInternalRecordMCPToolCall_BoundTokenForeignCrew_403(t *testing.T) {
	h, wsID, _ := newInternalStatusCovHandler(t)

	// Crew living in a different workspace than the token binding.
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other-mcp', 'Other', 'other-mcp')`); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	foreignCrew := seedCrewRow(t, h.db, "crew-foreign-mcp", "ws-other-mcp", "Foreign", "foreign-mcp")

	body := `{"workspace_id":"` + wsID + `","agent_id":"agent-1","crew_id":"` + foreignCrew + `","mcp_server_id":"srv-1","tool_name":"search"}`
	req := httptest.NewRequest("POST", "/api/v1/internal/mcp-tool-calls", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, wsID))
	rr := httptest.NewRecorder()
	h.RecordMCPToolCall(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mcp_tool_calls`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("mcp_tool_calls rows = %d, want 0 (foreign-crew audit write must be rejected)", n)
	}
}
