package api

// Tests for the PR-D F5 approve-hire endpoint. The endpoint is what
// makes the guided autonomy flow actually block — it flips the
// PENDING_REVIEW agent row to IDLE so the chatbridge guard releases
// and resolves the blocking inbox waitpoint. The matrix:
//
//	PENDING_REVIEW → IDLE        — 200 happy path
//	PENDING_REVIEW (lost race)   — 409 (concurrent approve)
//	IDLE/RUNNING/ERROR           — 409 (idempotent on already-approved)
//	permanent agent              — 409 (approve is ephemeral-only)
//	unknown id                   — 404
//	VIEWER / MEMBER caller       — 403 (RBAC gate)

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// seedPendingReviewAgent inserts an ephemeral agent row in
// PENDING_REVIEW state along with a blocking inbox waitpoint, the
// exact post-Hire(guided) shape. Returns the agent id so the test can
// post to /approve-hire/{id}.
func seedPendingReviewAgent(t *testing.T, db *sql.DB, wsID, crewID, agentID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role, status,
		    cli_adapter, tool_profile, memory_enabled,
		    ephemeral, expires_at, hire_reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'AGENT', 'PENDING_REVIEW',
		        'CLAUDE_CODE', 'CODING', 1,
		        1, '2099-01-01T00:00:00Z', '[hire] test', ?, ?)`,
		agentID, crewID, wsID, agentID, agentID, now, now)
	if err != nil {
		t.Fatalf("seed pending-review agent: %v", err)
	}
	// Blocking inbox waitpoint addressed to the agent, mirrors the
	// row Hire writes on the guided path.
	_, err = db.Exec(`
		INSERT INTO inbox_items (id, workspace_id, kind, source_id, sender_type, sender_id, sender_name,
		    title, body_md, state, priority, blocking, payload_json)
		VALUES (?, ?, 'waitpoint', ?, 'user', 'u', 'tester', 'hire', 'b', 'unread', 'medium', 1, '{}')`,
		"inbox-"+agentID, wsID, agentID)
	if err != nil {
		t.Fatalf("seed inbox: %v", err)
	}
}

func postApproveHire(t *testing.T, h *AgentHandler, userID, wsID, role, agentID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/approve-hire", bytes.NewReader(nil))
	req.SetPathValue("agentId", agentID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, role)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ApproveHire(rr, req)
	return rr
}

func newApproveHireHandler(t *testing.T, db *sql.DB) *AgentHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewAgentHandler(db, logger)
}

func TestApproveHire_FlipsPendingReviewToIdle(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	h := newApproveHireHandler(t, db)

	seedPendingReviewAgent(t, db, wsID, crewID, "a-pending")

	rr := postApproveHire(t, h, userID, wsID, "MANAGER", "a-pending")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Status flipped on the row itself.
	var status string
	if err := db.QueryRow(`SELECT status FROM agents WHERE id = ?`, "a-pending").Scan(&status); err != nil {
		t.Fatalf("verify status: %v", err)
	}
	if status != "IDLE" {
		t.Errorf("agents.status = %q, want IDLE", status)
	}

	// Inbox waitpoint resolved.
	var state string
	if err := db.QueryRow(`SELECT state FROM inbox_items WHERE source_id = ?`, "a-pending").Scan(&state); err != nil {
		t.Fatalf("verify inbox state: %v", err)
	}
	if state != "resolved" {
		t.Errorf("inbox state = %q, want resolved", state)
	}

	// Response body should echo the new state.
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["status"] != "IDLE" {
		t.Errorf("body.status = %v, want IDLE", body["status"])
	}
}

func TestApproveHire_RejectsIfAlreadyApproved(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	h := newApproveHireHandler(t, db)

	seedPendingReviewAgent(t, db, wsID, crewID, "a-once")

	// First approval succeeds.
	rr1 := postApproveHire(t, h, userID, wsID, "MANAGER", "a-once")
	if rr1.Code != http.StatusOK {
		t.Fatalf("first approve: status = %d, want 200", rr1.Code)
	}

	// Second approval must be a 409 — the row is already IDLE.
	rr2 := postApproveHire(t, h, userID, wsID, "MANAGER", "a-once")
	if rr2.Code != http.StatusConflict {
		t.Errorf("second approve: status = %d, want 409 (idempotency)", rr2.Code)
	}
}

func TestApproveHire_RejectsNonEphemeralAgent(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	h := newApproveHireHandler(t, db)

	// Permanent agent — never participates in the hire flow.
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role, status,
		    cli_adapter, tool_profile, memory_enabled, ephemeral,
		    created_at, updated_at)
		VALUES ('a-perm', ?, ?, 'Perm', 'perm', 'AGENT', 'IDLE',
		        'CLAUDE_CODE', 'CODING', 1, 0, ?, ?)`, crewID, wsID, now, now)
	if err != nil {
		t.Fatalf("seed perm: %v", err)
	}

	rr := postApproveHire(t, h, userID, wsID, "MANAGER", "a-perm")
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 on permanent agent", rr.Code)
	}
}

func TestApproveHire_404ForUnknownAgent(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "guided", 5)
	h := newApproveHireHandler(t, db)

	rr := postApproveHire(t, h, userID, wsID, "MANAGER", "nope")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestApproveHire_RequiresManagerOrAbove(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	h := newApproveHireHandler(t, db)

	seedPendingReviewAgent(t, db, wsID, crewID, "a-rbac")

	for _, role := range []string{"VIEWER", "MEMBER"} {
		rr := postApproveHire(t, h, userID, wsID, role, "a-rbac")
		if rr.Code != http.StatusForbidden {
			t.Errorf("role=%s: status = %d, want 403", role, rr.Code)
		}
	}

	// Row must still be PENDING_REVIEW — the 403 must not have
	// silently flipped anything.
	var status string
	_ = db.QueryRow(`SELECT status FROM agents WHERE id = ?`, "a-rbac").Scan(&status)
	if status != "PENDING_REVIEW" {
		t.Errorf("status after 403 = %q, want PENDING_REVIEW (no side-effect)", status)
	}
}
