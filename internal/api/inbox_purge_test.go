package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func countInbox(t *testing.T, h *InboxHandler, wsID string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = ?`, wsID).Scan(&n); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	return n
}

func insertInbox(t *testing.T, h *InboxHandler, id, wsID, kind string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state, priority, blocking, payload_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'x', 'unread', 'medium', 0, '{}', ?, ?)`, id, wsID, kind, "src-"+id, now, now); err != nil {
		t.Fatalf("insert inbox row %s: %v", id, err)
	}
}

func TestInboxPurge_WorkspaceScoped(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)

	// Two rows in the active workspace, one in another workspace.
	const otherWS = "other-ws-inbox"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'O', 'o-inbox')`, otherWS); err != nil {
		t.Fatalf("insert other ws: %v", err)
	}
	insertInbox(t, h, "a", wsID, "failed_run")
	insertInbox(t, h, "b", wsID, "message")
	insertInbox(t, h, "c", otherWS, "failed_run")

	req := withWorkspaceUser(httptest.NewRequest("DELETE", "/api/v1/inbox", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Purge(rr, req)

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
	if n := countInbox(t, h, wsID); n != 0 {
		t.Errorf("active workspace rows remaining = %d; want 0", n)
	}
	if n := countInbox(t, h, otherWS); n != 1 {
		t.Errorf("other workspace rows = %d; want 1 (must survive)", n)
	}
}

func TestInboxPurge_KindFilter(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)

	insertInbox(t, h, "a", wsID, "failed_run")
	insertInbox(t, h, "b", wsID, "failed_run")
	insertInbox(t, h, "c", wsID, "message")

	req := withWorkspaceUser(httptest.NewRequest("DELETE", "/api/v1/inbox?kind=failed_run", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Purge(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Deleted int64 `json:"deleted"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if out.Deleted != 2 {
		t.Errorf("deleted = %d; want 2 (only failed_run)", out.Deleted)
	}
	if n := countInbox(t, h, wsID); n != 1 {
		t.Errorf("remaining = %d; want 1 (message survives)", n)
	}
}

func TestInboxPurge_InvalidKindIs400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)

	req := withWorkspaceUser(httptest.NewRequest("DELETE", "/api/v1/inbox?kind=bogus", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Purge(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", rr.Code)
	}
}

func TestInboxPurge_RequiresAdmin(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)
	insertInbox(t, h, "a", wsID, "failed_run")

	req := withWorkspaceUser(httptest.NewRequest("DELETE", "/api/v1/inbox", nil), userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Purge(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("MEMBER status = %d; want 403", rr.Code)
	}
	if n := countInbox(t, h, wsID); n != 1 {
		t.Errorf("forbidden purge must not delete: remaining = %d; want 1", n)
	}
}
