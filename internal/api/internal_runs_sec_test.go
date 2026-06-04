package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/ws"
)

// seedSecTestWorkspace inserts an additional workspace + owner membership
// with caller-supplied ids, unlike seedTestWorkspace which hardcodes a
// single workspace. The cross-tenant CreateRun checks need two distinct
// workspaces, so this lets each test stand up WS-A and WS-B side by side.
func seedSecTestWorkspace(t *testing.T, db *sql.DB, userID, wsID, slug string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, wsID, slug, slug); err != nil {
		t.Fatalf("insert workspace %s: %v", wsID, err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'OWNER')`, "m-"+wsID, wsID, userID); err != nil {
		t.Fatalf("insert member %s: %v", wsID, err)
	}
}

func agentStatus(t *testing.T, db *sql.DB, agentID string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM agents WHERE id = ?`, agentID).Scan(&status); err != nil {
		t.Fatalf("read agent %s status: %v", agentID, err)
	}
	return status
}

// TestSecRuns_CreateRun_RejectsCrossWorkspaceAgent is the core regression
// for the HIGH-severity cross-tenant mutation: a caller holding the
// internal token posts workspace_id of WS-A but agent_id of an agent that
// lives in WS-B. Before the fix, CreateRun would happily flip the WS-B
// agent to RUNNING (and emit a forged journal entry attributing it to
// WS-A). After the fix the request is rejected and the WS-B agent is
// untouched.
func TestSecRuns_CreateRun_RejectsCrossWorkspaceAgent(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsA := seedTestWorkspace(t, db, userID) // attacker's own workspace
	wsB := "ws-victim"
	seedSecTestWorkspace(t, db, userID, wsB, "victim")

	// Victim agent lives in WS-B, currently IDLE.
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('victim-agent', ?, 'Victim', 'victim', 'IDLE')`, wsB); err != nil {
		t.Fatalf("insert victim agent: %v", err)
	}

	h := NewInternalHandler(db, "test-token", logger)
	h.SetHub(ws.NewHub(logger, nil, ws.NopValidatorForTests, ws.NopSessionsForTests))
	_ = wireTestJournalForHandler(t, db, h)

	// Attacker posts their own workspace_id but the victim's agent_id.
	body := strings.NewReader(`{"id":"run-evil","agent_id":"victim-agent","workspace_id":"` + wsA + `","trigger_type":"USER"}`)
	req := httptest.NewRequest("POST", "/api/v1/internal/runs", body)
	rr := httptest.NewRecorder()
	h.CreateRun(rr, req)

	if rr.Code != http.StatusNotFound && rr.Code != http.StatusForbidden {
		t.Fatalf("cross-workspace CreateRun must be rejected (404/403); got %d body=%s", rr.Code, rr.Body.String())
	}

	// The victim agent must remain IDLE — never flipped to RUNNING.
	if got := agentStatus(t, db, "victim-agent"); got != "IDLE" {
		t.Errorf("victim agent status = %q, want IDLE (cross-tenant mutation leaked through)", got)
	}
}

// TestSecRuns_CreateRun_AllowsMatchingWorkspaceAgent is the positive
// counterpart: when workspace_id and agent_id genuinely belong together,
// CreateRun still records the run and flips the agent to RUNNING.
func TestSecRuns_CreateRun_AllowsMatchingWorkspaceAgent(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug, status) VALUES ('a-ok', ?, 'Bot', 'bot', 'IDLE')`, wsID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	h := NewInternalHandler(db, "test-token", logger)
	h.SetHub(ws.NewHub(logger, nil, ws.NopValidatorForTests, ws.NopSessionsForTests))
	_ = wireTestJournalForHandler(t, db, h)

	body := strings.NewReader(`{"id":"run-ok","agent_id":"a-ok","workspace_id":"` + wsID + `","trigger_type":"USER"}`)
	req := httptest.NewRequest("POST", "/api/v1/internal/runs", body)
	rr := httptest.NewRecorder()
	h.CreateRun(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("matching-workspace CreateRun must succeed (201); got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := agentStatus(t, db, "a-ok"); got != "RUNNING" {
		t.Errorf("agent status = %q, want RUNNING after legitimate run create", got)
	}
}
