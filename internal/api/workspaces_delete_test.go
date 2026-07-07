package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// Tests for DELETE /api/v1/workspaces/{id} (#866.2).
//
// This is a destructive, irreversible-feeling action (soft-delete with
// cascade), so the guards are the contract: OWNER-only, type-the-slug
// confirmation, and a refusal to delete the caller's only workspace.
// RED-first — these pin every guard before the cascade is trusted.

func deleteRig(t *testing.T) (*WorkspaceHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID) // user is OWNER, slug "test"
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewWorkspaceHandler(db, logger)
	return h, userID, wsID
}

func deleteReq(t *testing.T, userID, wsID, role, confirmSlug string) *http.Request {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"confirm_slug": confirmSlug})
	req := httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID, bytes.NewReader(body))
	return withWorkspaceUser(req, userID, wsID, role)
}

// Guard 1: only OWNER may delete. A workspace ADMIN (who clears the
// route-level roleManage gate) must still be refused inside the handler.
func TestWorkspaceDelete_NonOwner_Forbidden(t *testing.T) {
	h, userID, wsID := deleteRig(t)
	rr := httptest.NewRecorder()
	h.Delete(rr, deleteReq(t, userID, wsID, "ADMIN", "test"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	// Workspace must be untouched.
	var deletedAt sql.NullString
	if err := h.db.QueryRow("SELECT deleted_at FROM workspaces WHERE id = ?", wsID).Scan(&deletedAt); err != nil {
		t.Fatalf("query: %v", err)
	}
	if deletedAt.Valid {
		t.Fatalf("workspace was soft-deleted despite 403")
	}
}

// Guard 2: the typed slug must match. A wrong slug is a 400, no delete.
func TestWorkspaceDelete_WrongSlug_BadRequest(t *testing.T) {
	h, userID, wsID := deleteRig(t)
	// Give the OWNER a second workspace so the last-workspace guard
	// doesn't mask the slug check.
	seedOtherWorkspace(t, h, "ws-other", "other", userID)

	rr := httptest.NewRecorder()
	h.Delete(rr, deleteReq(t, userID, wsID, "OWNER", "wrong-slug"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var deletedAt sql.NullString
	_ = h.db.QueryRow("SELECT deleted_at FROM workspaces WHERE id = ?", wsID).Scan(&deletedAt)
	if deletedAt.Valid {
		t.Fatalf("workspace was soft-deleted despite slug mismatch")
	}
}

// Guard 3: refuse deleting the caller's only workspace.
func TestWorkspaceDelete_OnlyWorkspace_Conflict(t *testing.T) {
	h, userID, wsID := deleteRig(t)
	rr := httptest.NewRecorder()
	h.Delete(rr, deleteReq(t, userID, wsID, "OWNER", "test"))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	var deletedAt sql.NullString
	_ = h.db.QueryRow("SELECT deleted_at FROM workspaces WHERE id = ?", wsID).Scan(&deletedAt)
	if deletedAt.Valid {
		t.Fatalf("only workspace was soft-deleted")
	}
}

// Happy path: OWNER + correct slug + a second workspace exists → the
// workspace and its crews/agents are soft-deleted and membership rows
// removed.
func TestWorkspaceDelete_Success_CascadeSoftDelete(t *testing.T) {
	h, userID, wsID := deleteRig(t)
	seedOtherWorkspace(t, h, "ws-other", "other", userID)
	seedCrew(t, h.db, "c1", wsID, "Crew 1", "c1")
	seedAgent(t, h.db, "a1", wsID, "c1", "Agent 1", "a1")

	rr := httptest.NewRecorder()
	h.Delete(rr, deleteReq(t, userID, wsID, "OWNER", "test"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	// Workspace soft-deleted.
	var wsDel sql.NullString
	if err := h.db.QueryRow("SELECT deleted_at FROM workspaces WHERE id = ?", wsID).Scan(&wsDel); err != nil {
		t.Fatalf("query ws: %v", err)
	}
	if !wsDel.Valid {
		t.Fatalf("workspace deleted_at not set")
	}
	// Crew soft-deleted.
	var crewDel sql.NullString
	if err := h.db.QueryRow("SELECT deleted_at FROM crews WHERE id = ?", "c1").Scan(&crewDel); err != nil {
		t.Fatalf("query crew: %v", err)
	}
	if !crewDel.Valid {
		t.Fatalf("crew deleted_at not set")
	}
	// Agent soft-deleted.
	var agentDel sql.NullString
	if err := h.db.QueryRow("SELECT deleted_at FROM agents WHERE id = ?", "a1").Scan(&agentDel); err != nil {
		t.Fatalf("query agent: %v", err)
	}
	if !agentDel.Valid {
		t.Fatalf("agent deleted_at not set")
	}
	// Membership rows removed.
	var memberCount int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ?", wsID).Scan(&memberCount); err != nil {
		t.Fatalf("count members: %v", err)
	}
	if memberCount != 0 {
		t.Fatalf("workspace_members not removed: %d rows", memberCount)
	}

	// Deleted workspace must no longer surface in the owner's List.
	listReq := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	listReq = listReq.WithContext(withUser(listReq.Context(), &AuthUser{ID: userID}))
	lr := httptest.NewRecorder()
	h.List(lr, listReq)
	if lr.Code != http.StatusOK {
		t.Fatalf("list status = %d", lr.Code)
	}
	var rows []workspaceResponse
	if err := json.Unmarshal(lr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	for _, row := range rows {
		if row.ID == wsID {
			t.Fatalf("deleted workspace still in List")
		}
	}
}

// A workspace id with no live row is a 404 (guards run before cascade).
func TestWorkspaceDelete_Missing_NotFound(t *testing.T) {
	h, userID, _ := deleteRig(t)
	rr := httptest.NewRecorder()
	h.Delete(rr, deleteReq(t, userID, "no-such-ws", "OWNER", "whatever"))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}
