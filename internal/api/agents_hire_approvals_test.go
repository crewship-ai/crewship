package api

// Tests for the issue #1209 fix: ephemeral-hire PENDING_REVIEW
// waitpoints must be visible AND decidable through the standard
// approvals surface, not only via the per-agent `hire approve`
// endpoint. Coverage:
//
//	Hire (guided)                → enqueues a pending kind=ephemeral_hire
//	                               approvals_queue row + echoes approval_id
//	approvals decide (approved)  → agent flips PENDING_REVIEW → IDLE,
//	                               inbox waitpoint resolves
//	approvals decide (denied)    → agent ghosts (expired_at set), inbox
//	                               waitpoint resolves with action=denied
//	approve-hire endpoint        → syncs the pending approvals row to
//	                               approved so the surfaces can't drift

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/harbormaster"
)

// enqueueHireApproval mirrors the approvals_queue row the Hire handler
// writes on the guided path, keyed to an already-seeded PENDING_REVIEW
// agent.
func enqueueHireApproval(t *testing.T, db *sql.DB, wsID, crewID, agentID, requestedBy string) string {
	t.Helper()
	timeout := time.Now().UTC().Add(30 * time.Minute)
	id, err := harbormaster.Enqueue(context.Background(), db, nil, harbormaster.Request{
		WorkspaceID: wsID,
		CrewID:      crewID,
		AgentID:     agentID,
		RequestedBy: requestedBy,
		Kind:        harbormaster.KindEphemeralHire,
		Reason:      "hire ephemeral agent " + agentID + ": test",
		Payload:     map[string]any{"tool": "agent.hire", "agent_id": agentID},
		TimeoutAt:   &timeout,
	})
	if err != nil {
		t.Fatalf("enqueue hire approval: %v", err)
	}
	return id
}

func postApprovalsDecide(t *testing.T, h *ApprovalsHandler, userID, wsID, role, approvalID, status string) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader(`{"status":"` + status + `"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/approvals/"+approvalID+"/decide", body),
		userID, wsID, role,
	)
	req.SetPathValue("id", approvalID)
	rr := httptest.NewRecorder()
	h.Decide(rr, req)
	return rr
}

func newHireApprovalsHandler(t *testing.T, db *sql.DB) *ApprovalsHandler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewApprovalsHandler(db, logger, noopEmitter{})
}

func TestHire_Guided_EnqueuesEphemeralHireApproval(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "guided", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_slug":     "hire-crew",
		"template_slug": "docs-writer",
		"reason":        "needs the approvals surface",
		"ttl_minutes":   45,
	})
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", rr.Code, rr.Body.String())
	}

	var body struct {
		ID         string `json:"id"`
		ApprovalID string `json:"approval_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, rr.Body.String())
	}
	if body.ApprovalID == "" {
		t.Fatalf("approval_id missing from guided hire response; body: %s", rr.Body.String())
	}

	// The row must be a pending kind=ephemeral_hire approval keyed to the
	// hired agent — that's what makes it show up in `approvals list`.
	row, err := harbormaster.Get(context.Background(), db, wsID, body.ApprovalID)
	if err != nil {
		t.Fatalf("load approvals row: %v", err)
	}
	if row == nil {
		t.Fatalf("approvals row %s not found", body.ApprovalID)
	}
	if row.Kind != harbormaster.KindEphemeralHire {
		t.Errorf("kind = %q, want %q", row.Kind, harbormaster.KindEphemeralHire)
	}
	if row.Status != harbormaster.StatusPending {
		t.Errorf("status = %q, want pending", row.Status)
	}
	if row.AgentID != body.ID {
		t.Errorf("agent_id = %q, want %q", row.AgentID, body.ID)
	}
	if row.RequestedBy != userID {
		t.Errorf("requested_by = %q, want %q", row.RequestedBy, userID)
	}
	// Approval timeout aligns with the hire TTL so the pending row can't
	// outlive the agent it gates.
	if row.TimeoutAt == nil {
		t.Fatalf("timeout_at not set")
	}
	wantTimeout := time.Now().UTC().Add(45 * time.Minute)
	if diff := row.TimeoutAt.Sub(wantTimeout); diff > time.Minute || diff < -time.Minute {
		t.Errorf("timeout_at = %v, want ≈ %v (hire TTL)", row.TimeoutAt, wantTimeout)
	}
}

func TestHire_Trusted_DoesNotEnqueueApproval(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, _ := seedHireCrew(t, db, "trusted", 5)
	h := newHireHandler(t, db)

	rr := postHire(t, h, userID, wsID, "MANAGER", map[string]any{
		"crew_slug":     "hire-crew",
		"template_slug": "docs-writer",
		"reason":        "trusted crews skip the queue",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rr.Code, rr.Body.String())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM approvals_queue WHERE workspace_id = ?`, wsID).Scan(&n); err != nil {
		t.Fatalf("count approvals: %v", err)
	}
	if n != 0 {
		t.Errorf("approvals_queue rows = %d, want 0 on trusted autonomy", n)
	}
}

func TestApprovalsDecide_ApproveEphemeralHire_FlipsAgentToIdle(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-via-approvals")
	approvalID := enqueueHireApproval(t, db, wsID, crewID, "a-via-approvals", userID)
	h := newHireApprovalsHandler(t, db)

	rr := postApprovalsDecide(t, h, userID, wsID, "OWNER", approvalID, "approved")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	var status string
	var expiredAt sql.NullString
	if err := db.QueryRow(`SELECT status, expired_at FROM agents WHERE id = ?`, "a-via-approvals").
		Scan(&status, &expiredAt); err != nil {
		t.Fatalf("verify agent: %v", err)
	}
	if status != "IDLE" {
		t.Errorf("agents.status = %q, want IDLE", status)
	}
	if expiredAt.Valid {
		t.Errorf("expired_at = %q, want NULL on approve", expiredAt.String)
	}

	// The blocking inbox waitpoint resolved alongside — same behavior as
	// the /approve-hire endpoint.
	var state, action string
	if err := db.QueryRow(`SELECT state, COALESCE(resolved_action, '') FROM inbox_items WHERE source_id = ?`,
		"a-via-approvals").Scan(&state, &action); err != nil {
		t.Fatalf("verify inbox: %v", err)
	}
	if state != "resolved" || action != "approved" {
		t.Errorf("inbox state/action = %q/%q, want resolved/approved", state, action)
	}
}

func TestApprovalsDecide_DenyEphemeralHire_GhostsAgent(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-denied")
	approvalID := enqueueHireApproval(t, db, wsID, crewID, "a-denied", userID)
	h := newHireApprovalsHandler(t, db)

	rr := postApprovalsDecide(t, h, userID, wsID, "OWNER", approvalID, "denied")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Denied → the staged agent ghosts (expired_at set) and never goes
	// IDLE; audit history is preserved, quota slot freed.
	var status string
	var expiredAt sql.NullString
	if err := db.QueryRow(`SELECT status, expired_at FROM agents WHERE id = ?`, "a-denied").
		Scan(&status, &expiredAt); err != nil {
		t.Fatalf("verify agent: %v", err)
	}
	if status != "PENDING_REVIEW" {
		t.Errorf("agents.status = %q, want PENDING_REVIEW (deny never activates)", status)
	}
	if !expiredAt.Valid || expiredAt.String == "" {
		t.Errorf("expired_at not set on deny — agent would stay a live pending hire forever")
	}

	var state, action string
	if err := db.QueryRow(`SELECT state, COALESCE(resolved_action, '') FROM inbox_items WHERE source_id = ?`,
		"a-denied").Scan(&state, &action); err != nil {
		t.Fatalf("verify inbox: %v", err)
	}
	if state != "resolved" || action != "denied" {
		t.Errorf("inbox state/action = %q/%q, want resolved/denied", state, action)
	}
}

func TestApprovalsDecide_EphemeralHire_AgentAlreadyIdle_SkipsSideEffect(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-raced")
	approvalID := enqueueHireApproval(t, db, wsID, crewID, "a-raced", userID)
	h := newHireApprovalsHandler(t, db)

	// Simulate the legacy `hire approve` path winning the race: agent is
	// already IDLE when the approvals-surface deny lands.
	if _, err := db.Exec(`UPDATE agents SET status = 'IDLE' WHERE id = ?`, "a-raced"); err != nil {
		t.Fatalf("flip agent: %v", err)
	}

	rr := postApprovalsDecide(t, h, userID, wsID, "OWNER", approvalID, "denied")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (decision persists; side effect skips); body: %s",
			rr.Code, rr.Body.String())
	}

	// The live agent must NOT be ghosted by the late deny.
	var expiredAt sql.NullString
	if err := db.QueryRow(`SELECT expired_at FROM agents WHERE id = ?`, "a-raced").Scan(&expiredAt); err != nil {
		t.Fatalf("verify agent: %v", err)
	}
	if expiredAt.Valid {
		t.Errorf("late deny ghosted an already-approved agent (expired_at = %q)", expiredAt.String)
	}
}

func TestApprovalsDecide_NonHireKind_DoesNotTouchAgents(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-untouched")
	h := newHireApprovalsHandler(t, db)

	// A plain tool_call approval that happens to reference the agent must
	// not trigger the hire transition.
	id, err := harbormaster.Enqueue(context.Background(), db, nil, harbormaster.Request{
		WorkspaceID: wsID,
		CrewID:      crewID,
		AgentID:     "a-untouched",
		RequestedBy: userID,
		Kind:        harbormaster.KindToolCall,
		Reason:      "shell.exec rm -rf",
		Payload:     map[string]any{"tool": "shell.exec"},
	})
	if err != nil {
		t.Fatalf("enqueue tool_call approval: %v", err)
	}

	rr := postApprovalsDecide(t, h, userID, wsID, "OWNER", id, "approved")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM agents WHERE id = ?`, "a-untouched").Scan(&status); err != nil {
		t.Fatalf("verify agent: %v", err)
	}
	if status != "PENDING_REVIEW" {
		t.Errorf("agents.status = %q, want PENDING_REVIEW (tool_call approvals must not flip hires)", status)
	}
}

func TestApproveHire_SyncsPendingApprovalsRow(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-sync")
	approvalID := enqueueHireApproval(t, db, wsID, crewID, "a-sync", userID)
	h := newApproveHireHandler(t, db)

	rr := postApproveHire(t, h, userID, wsID, "MANAGER", "a-sync")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// The legacy endpoint must flip the approvals row too, otherwise
	// `approvals list` keeps showing a pending decision for a live agent.
	row, err := harbormaster.Get(context.Background(), db, wsID, approvalID)
	if err != nil {
		t.Fatalf("load approvals row: %v", err)
	}
	if row == nil {
		t.Fatalf("approvals row %s not found", approvalID)
	}
	if row.Status != harbormaster.StatusApproved {
		t.Errorf("approvals row status = %q, want approved", row.Status)
	}
	if row.DecidedBy != userID {
		t.Errorf("decided_by = %q, want %q", row.DecidedBy, userID)
	}
}
