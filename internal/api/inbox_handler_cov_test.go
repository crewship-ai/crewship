package api

// Coverage tests for inbox_handler.go error branches and the WS-hub
// broadcast path: query/scan failures in List, count failure in
// UnreadCount, and the tx/lookup/update failure paths in PatchState.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

func TestInboxList_DBError_Returns500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)
	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/inbox", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "list failed") {
		t.Errorf("body = %s, want 'list failed'", rr.Body.String())
	}
}

func TestInboxList_MalformedRowSkipped(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Good row.
	if _, err := db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state, priority, blocking, payload_json, created_at, updated_at)
		VALUES ('good-row', ?, 'message', 'src-1', 'fine', 'unread', 'medium', 1, '{}', ?, ?)`, wsID, now, now); err != nil {
		t.Fatalf("seed good row: %v", err)
	}
	// Corrupt row: blocking holds TEXT, so the Scan into int fails and
	// the handler must skip the row rather than abort the whole list.
	if _, err := db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state, priority, blocking, payload_json, created_at, updated_at)
		VALUES ('bad-row', ?, 'message', 'src-2', 'corrupt', 'unread', 'medium', 'not-an-int', '{}', ?, ?)`, wsID, now, now); err != nil {
		t.Fatalf("seed bad row: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/inbox", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp inboxListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 1 {
		t.Fatalf("count = %d, want 1 (corrupt row skipped, good row kept)", resp.Count)
	}
	if resp.Rows[0].ID != "good-row" {
		t.Errorf("row id = %s, want good-row", resp.Rows[0].ID)
	}
}

func TestInboxUnreadCount_DBError_Returns500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)
	db.Close()

	req := httptest.NewRequest("GET", "/api/v1/inbox/count", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.UnreadCount(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "count failed") {
		t.Errorf("body = %s, want 'count failed'", rr.Body.String())
	}
}

func TestInboxPatchState_TxBeginError_Returns500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)
	db.Close()

	req := httptest.NewRequest("PATCH", "/api/v1/inbox/item-1", strings.NewReader(`{"state":"read"}`))
	req.SetPathValue("id", "item-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.PatchState(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "tx failed") {
		t.Errorf("body = %s, want 'tx failed'", rr.Body.String())
	}
}

func TestInboxPatchState_LookupError_Returns500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)

	// Dropping the table makes the lookup fail with a non-ErrNoRows
	// error after BeginTx already succeeded.
	if _, err := db.Exec(`DROP TABLE inbox_items`); err != nil {
		t.Fatalf("drop inbox_items: %v", err)
	}

	req := httptest.NewRequest("PATCH", "/api/v1/inbox/item-1", strings.NewReader(`{"state":"read"}`))
	req.SetPathValue("id", "item-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.PatchState(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "lookup failed") {
		t.Errorf("body = %s, want 'lookup failed'", rr.Body.String())
	}
}

func TestInboxPatchState_UpdateError_Returns500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)

	now := time.Now().UTC()
	seedInboxItem(t, h, wsID, "blocked-item", "message", "unread", "", "", "trigger test", now)

	// A RAISE(ABORT) trigger makes the UPDATE fail after the lookup
	// succeeded — exercising the update-error branch specifically.
	if _, err := db.Exec(`CREATE TRIGGER inbox_block_update BEFORE UPDATE ON inbox_items
		BEGIN SELECT RAISE(ABORT, 'update blocked by test trigger'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	req := httptest.NewRequest("PATCH", "/api/v1/inbox/blocked-item", strings.NewReader(`{"state":"read"}`))
	req.SetPathValue("id", "blocked-item")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.PatchState(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "update failed") {
		t.Errorf("body = %s, want 'update failed'", rr.Body.String())
	}
	// The transaction must have rolled back: state unchanged.
	var state string
	if err := db.QueryRow(`SELECT state FROM inbox_items WHERE id = 'blocked-item'`).Scan(&state); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if state != "unread" {
		t.Errorf("state = %q, want unread (rolled back)", state)
	}
}

func TestInboxPatchState_WithHub_BroadcastsAndFlipsState(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	hub := ws.NewHub(newTestLogger(), nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	h := NewInboxHandler(db, newTestLogger(), hub)

	now := time.Now().UTC()
	seedInboxItem(t, h, wsID, "hub-item", "message", "unread", "", "", "hub broadcast", now)

	req := httptest.NewRequest("PATCH", "/api/v1/inbox/hub-item", strings.NewReader(`{"state":"read"}`))
	req.SetPathValue("id", "hub-item")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.PatchState(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["id"] != "hub-item" || resp["state"] != "read" {
		t.Errorf("resp = %v, want id=hub-item state=read", resp)
	}
	var state, readBy string
	if err := db.QueryRow(`SELECT state, COALESCE(read_by_user_id, '') FROM inbox_items WHERE id = 'hub-item'`).Scan(&state, &readBy); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if state != "read" {
		t.Errorf("state = %q, want read", state)
	}
	if readBy != userID {
		t.Errorf("read_by_user_id = %q, want %q", readBy, userID)
	}
}
