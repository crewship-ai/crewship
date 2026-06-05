package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// These tests cover the defense-in-depth fix for the cross-crew/cross-workspace
// mission read & start vulnerability: the internal mission Start and Get handlers
// must scope their lookups by the caller's workspace (and crew where supplied), not
// by mission id alone. A compromised sidecar — or an agent that enumerated a mission
// id — must not be able to start or read a mission outside its own crew/workspace.
//
// Scope is sourced from the request query params (workspace_id required, crew_id
// optional), mirroring the InternalIssueHandler.Get pattern. The sidecar holds the
// trusted IPC crew/workspace identity and forwards it; the API enforces it.

// seedMissionCrewB inserts a second crew (crew B) in the same workspace plus a
// PLANNING mission owned by crew B, and returns the crew B id and mission id.
func seedMissionCrewB(t *testing.T, h *InternalMissionHandler, wsID, leadA string) (crewB, missionID string) {
	t.Helper()
	crewB = "crew-b-scope-" + generateCUID()
	if _, err := h.db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES (?, ?, 'Bravo', ?, 'BSC')`,
		crewB, wsID, "bravo-"+generateCUID()); err != nil {
		t.Fatalf("insert crew B: %v", err)
	}
	leadB := "agent-lead-bscope-" + generateCUID()
	if _, err := h.db.ExecContext(context.Background(),
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		 VALUES (?, ?, ?, 'LeadBScope', ?, 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		leadB, wsID, crewB, "leadbscope-"+generateCUID()); err != nil {
		t.Fatalf("insert lead B: %v", err)
	}
	missionID = "mission-bscope-" + generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(context.Background(),
		`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'B mission', 'PLANNING', ?, ?)`,
		missionID, wsID, crewB, leadB, "trace-"+missionID, now, now); err != nil {
		t.Fatalf("insert mission B: %v", err)
	}
	return crewB, missionID
}

func missionStatus(t *testing.T, h *InternalMissionHandler, missionID string) string {
	t.Helper()
	var s string
	if err := h.db.QueryRow(`SELECT status FROM missions WHERE id = ?`, missionID).Scan(&s); err != nil {
		t.Fatalf("read mission status: %v", err)
	}
	return s
}

// --- Start: cross-crew must be rejected, status unchanged ---

func TestSecMissScope_Start_CrossCrewRejected(t *testing.T) {
	ih, wsID, crewA, leadA, _ := newInternalIssueHandler(t)
	h := NewInternalMissionHandler(ih.db, nil, nil, testLogger())
	_, missionID := seedMissionCrewB(t, h, wsID, leadA)

	// Caller is crew A; mission belongs to crew B. Same workspace.
	req := httptest.NewRequest("POST", "/?workspace_id="+wsID+"&crew_id="+crewA, nil)
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Start(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-crew start, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := missionStatus(t, h, missionID); got != "PLANNING" {
		t.Errorf("mission status changed to %q, want PLANNING (must not start)", got)
	}
}

// --- Start: cross-workspace must be rejected, status unchanged ---

func TestSecMissScope_Start_CrossWorkspaceRejected(t *testing.T) {
	ih, wsID, _, leadA, _ := newInternalIssueHandler(t)
	h := NewInternalMissionHandler(ih.db, nil, nil, testLogger())
	crewB, missionID := seedMissionCrewB(t, h, wsID, leadA)

	// Correct crew, wrong workspace.
	req := httptest.NewRequest("POST", "/?workspace_id=ws-other&crew_id="+crewB, nil)
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Start(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-workspace start, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := missionStatus(t, h, missionID); got != "PLANNING" {
		t.Errorf("mission status changed to %q, want PLANNING", got)
	}
}

// --- Get: cross-crew must be rejected ---

func TestSecMissScope_Get_CrossCrewRejected(t *testing.T) {
	ih, wsID, crewA, leadA, _ := newInternalIssueHandler(t)
	h := NewInternalMissionHandler(ih.db, nil, nil, testLogger())
	_, missionID := seedMissionCrewB(t, h, wsID, leadA)

	req := httptest.NewRequest("GET", "/?workspace_id="+wsID+"&crew_id="+crewA, nil)
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Get(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-crew get, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// --- Get: cross-workspace must be rejected ---

func TestSecMissScope_Get_CrossWorkspaceRejected(t *testing.T) {
	ih, wsID, _, leadA, _ := newInternalIssueHandler(t)
	h := NewInternalMissionHandler(ih.db, nil, nil, testLogger())
	crewB, missionID := seedMissionCrewB(t, h, wsID, leadA)

	req := httptest.NewRequest("GET", "/?workspace_id=ws-other&crew_id="+crewB, nil)
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Get(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-workspace get, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// --- Positive: same crew + workspace can Start then Get ---

func TestSecMissScope_SameCrewStartAndGetSucceed(t *testing.T) {
	ih, wsID, _, leadA, _ := newInternalIssueHandler(t)
	h := NewInternalMissionHandler(ih.db, nil, nil, testLogger())
	crewB, missionID := seedMissionCrewB(t, h, wsID, leadA)

	// Start with matching scope (mission engine nil → DB transition only).
	startReq := httptest.NewRequest("POST", "/?workspace_id="+wsID+"&crew_id="+crewB, nil)
	startReq.SetPathValue("missionId", missionID)
	startRR := httptest.NewRecorder()
	h.Start(startRR, startReq)
	if startRR.Code != http.StatusOK {
		t.Fatalf("expected 200 for in-scope start, got %d body=%s", startRR.Code, startRR.Body.String())
	}
	if got := missionStatus(t, h, missionID); got != "IN_PROGRESS" {
		t.Errorf("mission status = %q, want IN_PROGRESS", got)
	}

	// Get with matching scope.
	getReq := httptest.NewRequest("GET", "/?workspace_id="+wsID+"&crew_id="+crewB, nil)
	getReq.SetPathValue("missionId", missionID)
	getRR := httptest.NewRecorder()
	h.Get(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("expected 200 for in-scope get, got %d body=%s", getRR.Code, getRR.Body.String())
	}
	var resp struct {
		Mission struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"mission"`
	}
	if err := json.Unmarshal(getRR.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if resp.Mission.ID != missionID {
		t.Errorf("got mission id %q, want %q", resp.Mission.ID, missionID)
	}
}

// --- Missing workspace_id is rejected (scope is mandatory) ---

func TestSecMissScope_Start_MissingWorkspaceRejected(t *testing.T) {
	ih, wsID, _, leadA, _ := newInternalIssueHandler(t)
	h := NewInternalMissionHandler(ih.db, nil, nil, testLogger())
	_, missionID := seedMissionCrewB(t, h, wsID, leadA)

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("missionId", missionID)
	rr := httptest.NewRecorder()
	h.Start(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when workspace_id missing, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := missionStatus(t, h, missionID); got != "PLANNING" {
		t.Errorf("mission status changed to %q, want PLANNING", got)
	}
}
