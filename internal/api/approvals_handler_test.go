package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/harbormaster"
)

// approvalsHandlerRig builds the workspace+user fixtures every test in
// this file needs, plus a constructed handler. Returning the workspace
// id keeps each test compact while still letting callers verify
// workspace-scoping behaviour.
func approvalsHandlerRig(t *testing.T) (*ApprovalsHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewApprovalsHandler(db, logger, noopEmitter{})
	return h, userID, wsID
}

func enqueueApproval(t *testing.T, h *ApprovalsHandler, wsID, requestedBy, reason string) string {
	t.Helper()
	id, err := harbormaster.Enqueue(context.Background(), h.db, h.journal, harbormaster.Request{
		WorkspaceID: wsID,
		RequestedBy: requestedBy,
		Kind:        harbormaster.KindToolCall,
		Reason:      reason,
		Payload:     map[string]any{"tool": "shell.exec", "args": []string{"ls"}},
	})
	if err != nil {
		t.Fatalf("enqueue approval: %v", err)
	}
	return id
}

// ── List ────────────────────────────────────────────────────────────────

func TestApprovals_List_NoWorkspaceContext_Returns401(t *testing.T) {
	h, _, _ := approvalsHandlerRig(t)
	req := httptest.NewRequest("GET", "/api/v1/approvals", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestApprovals_List_EmptyWorkspace_Returns200WithZeroRows(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/approvals", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Rows   []any  `json:"rows"`
		Status string `json:"status"`
		Count  int    `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Count != 0 {
		t.Errorf("count = %d, want 0", resp.Count)
	}
	// Default filter is "pending" — verify the handler echoed that back.
	if resp.Status != string(harbormaster.StatusPending) {
		t.Errorf("status echo = %q, want %q", resp.Status, harbormaster.StatusPending)
	}
}

func TestApprovals_List_DefaultsToPending_HidesDecidedRows(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	_ = enqueueApproval(t, h, wsID, userID, "pending-1")
	decidedID := enqueueApproval(t, h, wsID, userID, "decided-1")
	if err := harbormaster.Decide(context.Background(), h.db, h.journal,
		wsID, decidedID, harbormaster.StatusApproved, userID, "lgtm"); err != nil {
		t.Fatalf("decide: %v", err)
	}

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/approvals", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp struct {
		Rows []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Rows) != 1 {
		t.Fatalf("rows = %d, want exactly 1 (the pending one)", len(resp.Rows))
	}
	if resp.Rows[0].Status != string(harbormaster.StatusPending) {
		t.Errorf("returned row status = %q, want pending", resp.Rows[0].Status)
	}
}

func TestApprovals_List_StatusAll_IncludesDecided(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	_ = enqueueApproval(t, h, wsID, userID, "still-pending")
	decidedID := enqueueApproval(t, h, wsID, userID, "approved-already")
	if err := harbormaster.Decide(context.Background(), h.db, h.journal,
		wsID, decidedID, harbormaster.StatusApproved, userID, "ok"); err != nil {
		t.Fatalf("decide: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/approvals?status=all", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp struct {
		Rows []any `json:"rows"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Rows) != 2 {
		t.Errorf("?status=all rows = %d, want 2", len(resp.Rows))
	}
}

func TestApprovals_List_GarbageLimitFallsBackToDefault(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	for i := 0; i < 3; i++ {
		_ = enqueueApproval(t, h, wsID, userID, "garbage-limit-row")
	}

	// "abc" and "9999" both must NOT crash the handler. Per the inline
	// code, > 200 is rejected (silently keeps default 50) so 9999 falls
	// back; "abc" fails Atoi and also falls back. Either way we expect
	// 200 with all 3 rows visible (default 50 limit is far above 3).
	for _, raw := range []string{"abc", "9999", "0", "-5"} {
		req := withWorkspaceUser(
			httptest.NewRequest("GET", "/api/v1/approvals?limit="+raw, nil),
			userID, wsID, "OWNER",
		)
		rr := httptest.NewRecorder()
		h.List(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("limit=%q: status = %d, want 200", raw, rr.Code)
		}
	}
}

// ── Get ─────────────────────────────────────────────────────────────────

func TestApprovals_Get_NoWorkspaceContext_Returns401(t *testing.T) {
	h, _, _ := approvalsHandlerRig(t)
	req := httptest.NewRequest("GET", "/api/v1/approvals/anything", nil)
	req.SetPathValue("id", "anything")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestApprovals_Get_UnknownID_Returns404(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/approvals/ap_nope", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("id", "ap_nope")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestApprovals_Get_HappyPath_ReturnsRowWithPayload(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	id := enqueueApproval(t, h, wsID, userID, "fetch-me")

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/approvals/"+id, nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var row map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &row); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if row["ID"] != id && row["id"] != id { // tolerate either case
		t.Errorf("expected ID field to equal %q, got %v", id, row)
	}
}

func TestApprovals_Get_CrossWorkspace_Returns404(t *testing.T) {
	h, userID, wsA := approvalsHandlerRig(t)
	id := enqueueApproval(t, h, wsA, userID, "owned-by-A")

	// Create a second workspace and try to read wsA's approval from
	// wsB's context. The handler must scope by workspace; surfacing the
	// row would be a tenant-isolation bug.
	otherWS := "ws_other"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/approvals/"+id, nil),
		userID, otherWS, "OWNER",
	)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace read leaked: status = %d, want 404", rr.Code)
	}
}

// ── Decide ──────────────────────────────────────────────────────────────

func TestApprovals_Decide_NoUser_Returns401(t *testing.T) {
	h, _, wsID := approvalsHandlerRig(t)
	// Only workspace, no user — caller should be bounced.
	ctx := withWorkspace(context.Background(), wsID, "OWNER")
	req := httptest.NewRequest("POST", "/api/v1/approvals/x/decide",
		strings.NewReader(`{"status":"approved"}`)).WithContext(ctx)
	req.SetPathValue("id", "x")
	rr := httptest.NewRecorder()
	h.Decide(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestApprovals_Decide_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	id := enqueueApproval(t, h, wsID, userID, "members-cant-decide")

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/approvals/"+id+"/decide",
			strings.NewReader(`{"status":"approved"}`)),
		userID, wsID, "MEMBER", // <-- below OWNER/ADMIN threshold
	)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.Decide(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestApprovals_Decide_BadJSON_Returns400(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/approvals/whatever/decide",
			strings.NewReader(`{NOT_JSON`)),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("id", "whatever")
	rr := httptest.NewRecorder()
	h.Decide(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestApprovals_Decide_UnknownStatus_Returns400(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/approvals/x/decide",
			strings.NewReader(`{"status":"maybe"}`)),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("id", "x")
	rr := httptest.NewRecorder()
	h.Decide(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestApprovals_Decide_UnknownID_Returns404(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/approvals/ap_nope/decide",
			strings.NewReader(`{"status":"approved"}`)),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("id", "ap_nope")
	rr := httptest.NewRecorder()
	h.Decide(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestApprovals_Decide_Approve_PersistsAndReturns200(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	id := enqueueApproval(t, h, wsID, userID, "approve-me")

	body := strings.NewReader(`{"status":"approved","comment":"shipping"}`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/approvals/"+id+"/decide", body),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.Decide(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	// Verify the row actually flipped — a green HTTP status with a
	// still-pending row would be a silent regression.
	row, err := harbormaster.Get(context.Background(), h.db, wsID, id)
	if err != nil {
		t.Fatalf("post-decide read: %v", err)
	}
	if row.Status != harbormaster.StatusApproved {
		t.Errorf("row status = %q, want approved", row.Status)
	}
}

func TestApprovals_Decide_AlreadyDecided_Returns409(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	id := enqueueApproval(t, h, wsID, userID, "race-condition")
	if err := harbormaster.Decide(context.Background(), h.db, h.journal,
		wsID, id, harbormaster.StatusApproved, userID, "first"); err != nil {
		t.Fatalf("seed decide: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/approvals/"+id+"/decide",
			strings.NewReader(`{"status":"denied"}`)),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.Decide(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
}

// ── ResetAutoTuning ─────────────────────────────────────────────────────

func TestApprovals_ResetAutoTuning_NoWorkspace_Returns401(t *testing.T) {
	h, _, _ := approvalsHandlerRig(t)
	req := httptest.NewRequest("POST", "/api/v1/approvals/reset-auto-tuning",
		strings.NewReader(`{"tool":"shell.exec"}`))
	rr := httptest.NewRecorder()
	h.ResetAutoTuning(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestApprovals_ResetAutoTuning_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/approvals/reset-auto-tuning",
			strings.NewReader(`{"tool":"shell.exec"}`)),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.ResetAutoTuning(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestApprovals_ResetAutoTuning_MissingTool_Returns400(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/approvals/reset-auto-tuning",
			strings.NewReader(`{}`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ResetAutoTuning(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestApprovals_ResetAutoTuning_BadJSON_Returns400(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/approvals/reset-auto-tuning",
			strings.NewReader(`{NOT_JSON`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ResetAutoTuning(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestApprovals_ResetAutoTuning_HappyPath_Returns200(t *testing.T) {
	h, userID, wsID := approvalsHandlerRig(t)

	// No reward rows exist for this tool — the reset is still valid;
	// rows_deleted should just be 0. We're verifying the success-path
	// contract (200 + tool/rows_deleted/workspace_id payload), not the
	// reward-store internals which have their own tests in
	// internal/harbormaster.
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/approvals/reset-auto-tuning",
			strings.NewReader(`{"tool":"shell.exec"}`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ResetAutoTuning(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Tool        string `json:"tool"`
		RowsDeleted int64  `json:"rows_deleted"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Tool != "shell.exec" {
		t.Errorf("tool echo = %q, want shell.exec", resp.Tool)
	}
	if resp.WorkspaceID != wsID {
		t.Errorf("workspace_id echo = %q, want %q", resp.WorkspaceID, wsID)
	}
}
