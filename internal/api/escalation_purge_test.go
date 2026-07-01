package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func insertEscalation(t *testing.T, h *QueryHandler, id, wsID, crewID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,created_at)
		VALUES (?, ?, ?, 'chat-1', 'agent-1', 'help', 'PENDING', ?)`, id, wsID, crewID, now); err != nil {
		t.Fatalf("insert escalation %s: %v", id, err)
	}
}

func countEscalations(t *testing.T, h *QueryHandler, crewID string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM escalations WHERE crew_id = ?`, crewID).Scan(&n); err != nil {
		t.Fatalf("count escalations: %v", err)
	}
	return n
}

// withCrewPath attaches the {crewId} path value the handler reads.
func withCrewPath(req *http.Request, crewID string) *http.Request {
	req.SetPathValue("crewId", crewID)
	return req
}

func TestPurgeEscalations_CrewScoped(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewQueryHandler(db, nil, nil, "", newTestLogger())

	insertEscalation(t, h, "e1", wsID, "crew-a")
	insertEscalation(t, h, "e2", wsID, "crew-a")
	insertEscalation(t, h, "e3", wsID, "crew-b") // different crew, must survive

	req := withWorkspaceUser(httptest.NewRequest("DELETE", "/api/v1/crews/crew-a/escalations", nil), userID, wsID, "OWNER")
	req = withCrewPath(req, "crew-a")
	rr := httptest.NewRecorder()
	h.PurgeEscalations(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Deleted != 2 {
		t.Errorf("deleted = %d; want 2", out.Deleted)
	}
	if n := countEscalations(t, h, "crew-a"); n != 0 {
		t.Errorf("crew-a remaining = %d; want 0", n)
	}
	if n := countEscalations(t, h, "crew-b"); n != 1 {
		t.Errorf("crew-b remaining = %d; want 1 (other crew must survive)", n)
	}
}

// A caller must not reach another workspace's rows by guessing a crew id: the
// DELETE is scoped by workspace_id too.
func TestPurgeEscalations_WorkspaceScoped(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewQueryHandler(db, nil, nil, "", newTestLogger())

	// Row belongs to the same crew id but a DIFFERENT workspace.
	insertEscalation(t, h, "e1", "other-ws", "crew-a")

	req := withWorkspaceUser(httptest.NewRequest("DELETE", "/api/v1/crews/crew-a/escalations", nil), userID, wsID, "OWNER")
	req = withCrewPath(req, "crew-a")
	rr := httptest.NewRecorder()
	h.PurgeEscalations(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rr.Code)
	}
	var out struct {
		Deleted int64 `json:"deleted"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if out.Deleted != 0 {
		t.Errorf("deleted = %d; want 0 (row is in another workspace)", out.Deleted)
	}
	if n := countEscalations(t, h, "crew-a"); n != 1 {
		t.Errorf("other-workspace row must survive: remaining = %d; want 1", n)
	}
}

func TestPurgeEscalations_RequiresAdmin(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewQueryHandler(db, nil, nil, "", newTestLogger())
	insertEscalation(t, h, "e1", wsID, "crew-a")

	req := withWorkspaceUser(httptest.NewRequest("DELETE", "/api/v1/crews/crew-a/escalations", nil), userID, wsID, "MEMBER")
	req = withCrewPath(req, "crew-a")
	rr := httptest.NewRecorder()
	h.PurgeEscalations(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("MEMBER status = %d; want 403", rr.Code)
	}
	if n := countEscalations(t, h, "crew-a"); n != 1 {
		t.Errorf("forbidden purge must not delete: remaining = %d; want 1", n)
	}
}
