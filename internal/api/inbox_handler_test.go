package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// inbox_handler.go — List, UnreadCount, PatchState
//
// Covers the contract the bell-badge + Linear-style triage UI relies on:
// visibility (workspace vs user vs role), state/kind filters, transitions
// between unread/read/resolved, source-managed-kind PATCH restriction.
// ---------------------------------------------------------------------------

func seedInboxItem(t *testing.T, h *InboxHandler, wsID, id, kind, state, targetUser, targetRole, title string, createdAt time.Time) {
	t.Helper()
	_, err := h.db.Exec(`
		INSERT INTO inbox_items (id, workspace_id, kind, source_id, target_user_id, target_role,
			title, body_md, sender_type, sender_id, sender_name, state, priority, blocking,
			payload_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, '', 'system', '', '', ?, 'medium', 1,
			'{"k":"v"}', ?, ?)`,
		id, wsID, kind, "src-"+id, targetUser, targetRole, title, state,
		createdAt.Format(time.RFC3339Nano), createdAt.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("seed inbox %s: %v", id, err)
	}
}

func TestInboxHandler_List_VisibilityAndFilters(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)

	otherUser := "user-other-" + fmt.Sprint(time.Now().UnixNano())
	_, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'Other')`,
		otherUser, otherUser+"@example.com")
	if err != nil {
		t.Fatalf("seed other user: %v", err)
	}

	now := time.Now().UTC()
	// Visible: workspace-wide (no target), targeted to caller, targeted to caller's role
	seedInboxItem(t, h, wsID, "vis-ws", "message", "unread", "", "", "workspace-wide", now)
	seedInboxItem(t, h, wsID, "vis-user", "message", "unread", userID, "", "for me", now.Add(time.Second))
	seedInboxItem(t, h, wsID, "vis-role", "escalation", "unread", "", "OWNER", "for my role", now.Add(2*time.Second))
	// Hidden: targeted to a different user / different role
	seedInboxItem(t, h, wsID, "hidden-user", "message", "unread", otherUser, "", "not for me", now)
	seedInboxItem(t, h, wsID, "hidden-role", "message", "unread", "", "MEMBER", "wrong role", now)
	// State variety on visible rows
	seedInboxItem(t, h, wsID, "vis-read", "message", "read", "", "", "already read", now.Add(-1*time.Hour))
	seedInboxItem(t, h, wsID, "vis-resolved", "message", "resolved", "", "", "done", now.Add(-2*time.Hour))

	// Default state=all returns everything visible (5 rows), unread_count = 3
	req := httptest.NewRequest("GET", "/api/v1/inbox", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp inboxListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 5 {
		t.Errorf("visible count = %d, want 5 (3 unread + 1 read + 1 resolved, no cross-user/role)", resp.Count)
	}
	if resp.UnreadCount != 3 {
		t.Errorf("unread_count = %d, want 3", resp.UnreadCount)
	}
	// Verify no cross-visibility leaks
	for _, row := range resp.Rows {
		if row.ID == "hidden-user" || row.ID == "hidden-role" {
			t.Errorf("visibility leak: returned %s", row.ID)
		}
	}
	// Sort order: newest first — vis-role was seeded last with +2s
	if len(resp.Rows) > 0 && resp.Rows[0].ID != "vis-role" {
		t.Errorf("first row = %s, want vis-role (most recent)", resp.Rows[0].ID)
	}
	// Payload is parsed, not a raw string
	if len(resp.Rows) > 0 && resp.Rows[0].Payload["k"] != "v" {
		t.Errorf("payload not parsed: %+v", resp.Rows[0].Payload)
	}

	// state=unread filter
	req2 := httptest.NewRequest("GET", "/api/v1/inbox?state=unread", nil)
	req2 = withWorkspaceUser(req2, userID, wsID, "OWNER")
	rr2 := httptest.NewRecorder()
	h.List(rr2, req2)
	var resp2 inboxListResponse
	if err := json.Unmarshal(rr2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode unread response: %v body=%s", err, rr2.Body.String())
	}
	if resp2.Count != 3 {
		t.Errorf("unread-only count = %d, want 3", resp2.Count)
	}

	// kind=escalation filter
	req3 := httptest.NewRequest("GET", "/api/v1/inbox?kind=escalation", nil)
	req3 = withWorkspaceUser(req3, userID, wsID, "OWNER")
	rr3 := httptest.NewRecorder()
	h.List(rr3, req3)
	var resp3 inboxListResponse
	if err := json.Unmarshal(rr3.Body.Bytes(), &resp3); err != nil {
		t.Fatalf("decode escalation response: %v body=%s", err, rr3.Body.String())
	}
	if resp3.Count != 1 {
		t.Errorf("escalation-only count = %d, want 1", resp3.Count)
	}
	if len(resp3.Rows) > 0 && resp3.Rows[0].ID != "vis-role" {
		t.Errorf("kind filter returned wrong row: %s", resp3.Rows[0].ID)
	}

	// Invalid state → 400
	req4 := httptest.NewRequest("GET", "/api/v1/inbox?state=bogus", nil)
	req4 = withWorkspaceUser(req4, userID, wsID, "OWNER")
	rr4 := httptest.NewRecorder()
	h.List(rr4, req4)
	if rr4.Code != http.StatusBadRequest {
		t.Errorf("invalid state status = %d, want 400", rr4.Code)
	}

	// No workspace context → 401
	req5 := httptest.NewRequest("GET", "/api/v1/inbox", nil)
	rr5 := httptest.NewRecorder()
	h.List(rr5, req5)
	if rr5.Code != http.StatusUnauthorized {
		t.Errorf("no auth status = %d, want 401", rr5.Code)
	}

	// limit clamping: limit=2 returns at most 2 rows
	req6 := httptest.NewRequest("GET", "/api/v1/inbox?limit=2", nil)
	req6 = withWorkspaceUser(req6, userID, wsID, "OWNER")
	rr6 := httptest.NewRecorder()
	h.List(rr6, req6)
	var resp6 inboxListResponse
	if err := json.Unmarshal(rr6.Body.Bytes(), &resp6); err != nil {
		t.Fatalf("decode limit response: %v body=%s", err, rr6.Body.String())
	}
	if resp6.Count != 2 {
		t.Errorf("limit=2 returned %d rows", resp6.Count)
	}
	// limit=99999 clamps silently to default (cap is 500, so bogus high values fall back to 100)
	req7 := httptest.NewRequest("GET", "/api/v1/inbox?limit=99999", nil)
	req7 = withWorkspaceUser(req7, userID, wsID, "OWNER")
	rr7 := httptest.NewRecorder()
	h.List(rr7, req7)
	if rr7.Code != http.StatusOK {
		t.Errorf("limit=99999 status = %d, want 200 (silently clamp)", rr7.Code)
	}
}

func TestInboxHandler_UnreadCount(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)

	now := time.Now().UTC()
	seedInboxItem(t, h, wsID, "u1", "message", "unread", "", "", "a", now)
	seedInboxItem(t, h, wsID, "u2", "message", "unread", userID, "", "b", now)
	seedInboxItem(t, h, wsID, "r1", "message", "read", "", "", "c", now)
	// Hidden: targeted to another user — should not appear in count
	otherUser := "user-other2-" + fmt.Sprint(time.Now().UnixNano())
	_, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'Other')`,
		otherUser, otherUser+"@example.com")
	if err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	seedInboxItem(t, h, wsID, "u-other", "message", "unread", otherUser, "", "not mine", now)

	req := httptest.NewRequest("GET", "/api/v1/inbox/count", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.UnreadCount(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("count status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]int
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode count response: %v body=%s", err, rr.Body.String())
	}
	if body["unread_count"] != 2 {
		t.Errorf("unread_count = %d, want 2 (visibility filter applied)", body["unread_count"])
	}

	// No auth → 401
	req2 := httptest.NewRequest("GET", "/api/v1/inbox/count", nil)
	rr2 := httptest.NewRecorder()
	h.UnreadCount(rr2, req2)
	if rr2.Code != http.StatusUnauthorized {
		t.Errorf("no auth status = %d, want 401", rr2.Code)
	}
}

func TestInboxHandler_PatchState_Transitions(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)

	now := time.Now().UTC()
	seedInboxItem(t, h, wsID, "msg-1", "message", "unread", "", "", "test", now)

	// unread → read
	req := httptest.NewRequest("PATCH", "/api/v1/inbox/msg-1", strings.NewReader(`{"state":"read"}`))
	req.SetPathValue("id", "msg-1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.PatchState(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("read status = %d body=%s", rr.Code, rr.Body.String())
	}
	var state, readAt, readBy string
	if err := db.QueryRow(`SELECT state, COALESCE(read_at,''), COALESCE(read_by_user_id,'') FROM inbox_items WHERE id='msg-1'`).Scan(&state, &readAt, &readBy); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if state != "read" || readAt == "" || readBy != userID {
		t.Errorf("after read: state=%s read_at=%q read_by=%s; want read/non-empty/%s", state, readAt, readBy, userID)
	}

	// read → resolved with action
	req2 := httptest.NewRequest("PATCH", "/api/v1/inbox/msg-1", strings.NewReader(`{"state":"resolved","resolved_action":"approved"}`))
	req2.SetPathValue("id", "msg-1")
	req2 = withWorkspaceUser(req2, userID, wsID, "OWNER")
	rr2 := httptest.NewRecorder()
	h.PatchState(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("resolved status = %d body=%s", rr2.Code, rr2.Body.String())
	}
	var resolvedAt, resolvedAction string
	db.QueryRow(`SELECT state, COALESCE(resolved_at,''), COALESCE(resolved_action,'') FROM inbox_items WHERE id='msg-1'`).Scan(&state, &resolvedAt, &resolvedAction)
	if state != "resolved" || resolvedAt == "" || resolvedAction != "approved" {
		t.Errorf("after resolve: state=%s resolved_at=%q action=%s", state, resolvedAt, resolvedAction)
	}

	// resolved → unread clears read/resolved markers
	req3 := httptest.NewRequest("PATCH", "/api/v1/inbox/msg-1", strings.NewReader(`{"state":"unread"}`))
	req3.SetPathValue("id", "msg-1")
	req3 = withWorkspaceUser(req3, userID, wsID, "OWNER")
	rr3 := httptest.NewRecorder()
	h.PatchState(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("unread reset status = %d body=%s", rr3.Code, rr3.Body.String())
	}
	var ra, rba, resa, resba sql.NullString
	db.QueryRow(`SELECT read_at, read_by_user_id, resolved_at, resolved_by_user_id FROM inbox_items WHERE id='msg-1'`).Scan(&ra, &rba, &resa, &resba)
	if ra.Valid || rba.Valid || resa.Valid || resba.Valid {
		t.Errorf("unread reset should null markers: read_at.valid=%v read_by.valid=%v resolved_at.valid=%v resolved_by.valid=%v",
			ra.Valid, rba.Valid, resa.Valid, resba.Valid)
	}
}

func TestInboxHandler_PatchState_Errors(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)

	now := time.Now().UTC()
	seedInboxItem(t, h, wsID, "msg-err", "message", "unread", "", "", "x", now)

	// Invalid state value → 400
	req := httptest.NewRequest("PATCH", "/api/v1/inbox/msg-err", strings.NewReader(`{"state":"deleted"}`))
	req.SetPathValue("id", "msg-err")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.PatchState(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid state code = %d, want 400", rr.Code)
	}

	// Invalid JSON body → 400
	req2 := httptest.NewRequest("PATCH", "/api/v1/inbox/msg-err", strings.NewReader(`not json`))
	req2.SetPathValue("id", "msg-err")
	req2 = withWorkspaceUser(req2, userID, wsID, "OWNER")
	rr2 := httptest.NewRecorder()
	h.PatchState(rr2, req2)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("invalid json code = %d, want 400", rr2.Code)
	}

	// Missing id → 400
	req3 := httptest.NewRequest("PATCH", "/api/v1/inbox/", strings.NewReader(`{"state":"read"}`))
	req3 = withWorkspaceUser(req3, userID, wsID, "OWNER")
	rr3 := httptest.NewRecorder()
	h.PatchState(rr3, req3)
	if rr3.Code != http.StatusBadRequest {
		t.Errorf("missing id code = %d, want 400", rr3.Code)
	}

	// Unknown id → 404 (not 500/200 — privacy-preserving)
	req4 := httptest.NewRequest("PATCH", "/api/v1/inbox/does-not-exist", strings.NewReader(`{"state":"read"}`))
	req4.SetPathValue("id", "does-not-exist")
	req4 = withWorkspaceUser(req4, userID, wsID, "OWNER")
	rr4 := httptest.NewRecorder()
	h.PatchState(rr4, req4)
	if rr4.Code != http.StatusNotFound {
		t.Errorf("unknown id code = %d, want 404", rr4.Code)
	}

	// Cross-user-targeted item: must 404, not silently succeed
	otherUser := "user-other3-" + fmt.Sprint(time.Now().UnixNano())
	_, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'Other')`,
		otherUser, otherUser+"@example.com")
	if err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	seedInboxItem(t, h, wsID, "msg-other", "message", "unread", otherUser, "", "not for me", now)
	req5 := httptest.NewRequest("PATCH", "/api/v1/inbox/msg-other", strings.NewReader(`{"state":"read"}`))
	req5.SetPathValue("id", "msg-other")
	req5 = withWorkspaceUser(req5, userID, wsID, "OWNER")
	rr5 := httptest.NewRecorder()
	h.PatchState(rr5, req5)
	if rr5.Code != http.StatusNotFound {
		t.Errorf("cross-user patch code = %d, want 404 (privacy)", rr5.Code)
	}

	// No auth → 401
	req6 := httptest.NewRequest("PATCH", "/api/v1/inbox/msg-err", strings.NewReader(`{"state":"read"}`))
	req6.SetPathValue("id", "msg-err")
	rr6 := httptest.NewRecorder()
	h.PatchState(rr6, req6)
	if rr6.Code != http.StatusUnauthorized {
		t.Errorf("no auth code = %d, want 401", rr6.Code)
	}
}

func TestInboxHandler_PatchState_SourceManagedKinds(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewInboxHandler(db, newTestLogger(), nil)

	now := time.Now().UTC()
	// Source-managed kinds: only 'read' is allowed; resolved/unread must 409.
	for _, kind := range []string{"waitpoint", "escalation", "failed_run"} {
		id := "src-" + kind
		seedInboxItem(t, h, wsID, id, kind, "unread", "", "", "managed "+kind, now)

		// read is allowed
		req := httptest.NewRequest("PATCH", "/api/v1/inbox/"+id, strings.NewReader(`{"state":"read"}`))
		req.SetPathValue("id", id)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.PatchState(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("%s read: code = %d body=%s, want 200", kind, rr.Code, rr.Body.String())
		}

		// resolved is blocked with 409
		req2 := httptest.NewRequest("PATCH", "/api/v1/inbox/"+id, strings.NewReader(`{"state":"resolved","resolved_action":"approved"}`))
		req2.SetPathValue("id", id)
		req2 = withWorkspaceUser(req2, userID, wsID, "OWNER")
		rr2 := httptest.NewRecorder()
		h.PatchState(rr2, req2)
		if rr2.Code != http.StatusConflict {
			t.Errorf("%s resolved: code = %d, want 409 (source endpoint required)", kind, rr2.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rr2.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s resolved: decode 409 response: %v body=%s", kind, err, rr2.Body.String())
		}
		if body["kind"] != kind {
			t.Errorf("%s resolved: 409 body should echo kind, got %+v", kind, body)
		}

		// unread is also blocked
		req3 := httptest.NewRequest("PATCH", "/api/v1/inbox/"+id, strings.NewReader(`{"state":"unread"}`))
		req3.SetPathValue("id", id)
		req3 = withWorkspaceUser(req3, userID, wsID, "OWNER")
		rr3 := httptest.NewRecorder()
		h.PatchState(rr3, req3)
		if rr3.Code != http.StatusConflict {
			t.Errorf("%s unread: code = %d, want 409", kind, rr3.Code)
		}
	}

	// Generic 'message' kind still supports full flip set
	seedInboxItem(t, h, wsID, "src-msg", "message", "unread", "", "", "generic", now)
	req := httptest.NewRequest("PATCH", "/api/v1/inbox/src-msg", strings.NewReader(`{"state":"resolved","resolved_action":"approved"}`))
	req.SetPathValue("id", "src-msg")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.PatchState(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("message resolved: code = %d, want 200", rr.Code)
	}
}
