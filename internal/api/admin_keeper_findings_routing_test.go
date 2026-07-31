package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The findings test exists to answer "can Keeper reach a human". So the tests
// are about the answer being TRUE — the item really lands in inbox_items on the
// normal path — and about the answer being HONEST when routing is broken: a
// contact who left the workspace, or a workspace with nobody who can see a
// MANAGER-targeted item, must be reported rather than papered over.

func seedFindingsWorkspace(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO workspaces (id, name, slug) VALUES ('ws-fnd', 'Findings', 'findings')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	users := []struct{ id, email, name, role string }{
		{"u-owner", "owner@example.com", "Olga Owner", "OWNER"},
		{"u-manager", "manager@example.com", "Mara Manager", "MANAGER"},
		{"u-member", "member@example.com", "Mel Member", "MEMBER"},
		{"u-viewer", "viewer@example.com", "Val Viewer", "VIEWER"},
	}
	for _, u := range users {
		if _, err := db.Exec(
			`INSERT OR IGNORE INTO users (id, email, full_name) VALUES (?, ?, ?)`, u.id, u.email, u.name); err != nil {
			t.Fatalf("seed user %s: %v", u.id, err)
		}
		if _, err := db.Exec(
			`INSERT OR IGNORE INTO workspace_members (id, workspace_id, user_id, role)
			 VALUES (?, 'ws-fnd', ?, ?)`, "wm-"+u.id, u.id, u.role); err != nil {
			t.Fatalf("seed member %s: %v", u.id, err)
		}
	}
}

func findingsRequest(t *testing.T, role, userID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/admin/keeper/findings/test", nil)
	ctx := context.WithValue(req.Context(), ctxRole, role)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: userID, Email: "owner@example.com"})
	ctx = context.WithValue(ctx, ctxWorkspaceID, "ws-fnd")
	return req.WithContext(ctx)
}

func decodeFindings(t *testing.T, rr *httptest.ResponseRecorder) keeperFindingsTestResponse {
	t.Helper()
	var resp keeperFindingsTestResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, rr.Body.String())
	}
	return resp
}

// countInboxItems returns how many escalation rows exist for the workspace.
func countInboxItems(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = 'ws-fnd' AND kind = 'escalation'`).Scan(&n); err != nil {
		t.Fatalf("count inbox: %v", err)
	}
	return n
}

func TestKeeperFindingsTest_WritesToTheRealInbox(t *testing.T) {
	db := setupTestDB(t)
	seedFindingsWorkspace(t, db)
	h := NewAdminKeeperFindingsHandler(db, nil, nil, newTestLogger())

	rr := httptest.NewRecorder()
	h.SendTest(rr, findingsRequest(t, "OWNER", "u-owner"))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	resp := decodeFindings(t, rr)

	if countInboxItems(t, db) != 1 {
		t.Fatalf("expected exactly one inbox item, got %d", countInboxItems(t, db))
	}
	var id, title, targetRole, priority string
	var blocking int
	var payload string
	if err := db.QueryRow(`
		SELECT id, title, COALESCE(target_role, ''), priority, blocking, payload_json
		  FROM inbox_items WHERE workspace_id = 'ws-fnd'`).
		Scan(&id, &title, &targetRole, &priority, &blocking, &payload); err != nil {
		t.Fatalf("read inbox item: %v", err)
	}
	if id != resp.InboxItemID {
		t.Errorf("response id %q does not match the stored row %q", resp.InboxItemID, id)
	}
	// It must be recognisable as a drill from the title alone — an operator
	// seeing it in their inbox should not have to open it to know.
	if !strings.Contains(strings.ToLower(title), "test") {
		t.Errorf("title does not identify itself as a test: %q", title)
	}
	if targetRole != "MANAGER" {
		t.Errorf("target_role = %q, want MANAGER (the real fanout)", targetRole)
	}
	// A drill that parks a blocking item in somebody's queue is a drill that
	// gets the feature switched off.
	if blocking != 0 {
		t.Error("the test finding is blocking")
	}
	if !strings.Contains(payload, `"test":true`) {
		t.Errorf("payload does not carry the test flag: %s", payload)
	}
}

// The recipient preview is the actual product here: a list that disagrees with
// the inbox visibility filter would be worse than no list.
func TestKeeperFindingsTest_ReportsRoleFanout(t *testing.T) {
	db := setupTestDB(t)
	seedFindingsWorkspace(t, db)
	h := NewAdminKeeperFindingsHandler(db, nil, nil, newTestLogger())

	rr := httptest.NewRecorder()
	h.SendTest(rr, findingsRequest(t, "OWNER", "u-owner"))
	resp := decodeFindings(t, rr)

	got := map[string]string{}
	for _, rec := range resp.Recipients {
		got[rec.UserID] = rec.Reason
	}
	// MANAGER-and-above see a MANAGER-targeted item; MEMBER and VIEWER do not.
	for _, want := range []string{"u-owner", "u-manager"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s missing from recipients: %+v", want, resp.Recipients)
		}
	}
	for _, unwanted := range []string{"u-member", "u-viewer"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("%s listed as a recipient but cannot see MANAGER items", unwanted)
		}
	}
	if resp.Warning != "" {
		t.Errorf("unexpected warning: %q", resp.Warning)
	}
}

func TestKeeperFindingsTest_NamesTheSecurityContact(t *testing.T) {
	db := setupTestDB(t)
	seedFindingsWorkspace(t, db)
	if _, err := db.Exec(`
		INSERT INTO keeper_governance_settings (workspace_id, enabled, security_contact_user_id)
		VALUES ('ws-fnd', 1, 'u-manager')`); err != nil {
		t.Fatalf("seed governance: %v", err)
	}
	h := NewAdminKeeperFindingsHandler(db, nil, nil, newTestLogger())

	rr := httptest.NewRecorder()
	h.SendTest(rr, findingsRequest(t, "OWNER", "u-owner"))
	resp := decodeFindings(t, rr)

	if resp.SecurityContactUserID != "u-manager" {
		t.Errorf("security contact = %q, want u-manager", resp.SecurityContactUserID)
	}
	var contactReason string
	for _, rec := range resp.Recipients {
		if rec.UserID == "u-manager" {
			contactReason = rec.Reason
		}
	}
	if !strings.Contains(contactReason, "security contact") {
		t.Errorf("the named contact is not labelled as such: %q", contactReason)
	}
}

// The stale-configuration case this endpoint exists for: the contact was named,
// then left the workspace. They cannot see the item, and saying so is the whole
// point — listing them as a recipient would be a lie with a security cost.
func TestKeeperFindingsTest_FlagsAContactWhoLeft(t *testing.T) {
	db := setupTestDB(t)
	seedFindingsWorkspace(t, db)
	// A real user who is NOT a member here — the stale-contact case is somebody
	// who left the workspace, not somebody who never existed (the governance
	// column is a foreign key into users).
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO users (id, email, full_name) VALUES ('u-ghost', 'ghost@example.com', 'Gone Ghost')`); err != nil {
		t.Fatalf("seed departed user: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO keeper_governance_settings (workspace_id, enabled, security_contact_user_id)
		VALUES ('ws-fnd', 1, 'u-ghost')`); err != nil {
		t.Fatalf("seed governance: %v", err)
	}
	h := NewAdminKeeperFindingsHandler(db, nil, nil, newTestLogger())

	rr := httptest.NewRecorder()
	h.SendTest(rr, findingsRequest(t, "OWNER", "u-owner"))
	resp := decodeFindings(t, rr)

	var ghost *keeperFindingsRecipient
	for i := range resp.Recipients {
		if resp.Recipients[i].UserID == "u-ghost" {
			ghost = &resp.Recipients[i]
		}
	}
	if ghost == nil {
		t.Fatalf("the departed contact is absent from the report entirely: %+v", resp.Recipients)
	}
	if !strings.Contains(strings.ToLower(ghost.Reason), "not a member") {
		t.Errorf("reason does not say they cannot see it: %q", ghost.Reason)
	}
}

// Two sends must produce two items. inbox.Insert derives its row id from
// (kind, source_id) and INSERT OR IGNOREs, so a fixed source id would make every
// send after the first a silent no-op — the worst failure mode a test button has.
func TestKeeperFindingsTest_IsRepeatable(t *testing.T) {
	db := setupTestDB(t)
	seedFindingsWorkspace(t, db)
	h := NewAdminKeeperFindingsHandler(db, nil, nil, newTestLogger())

	for i := range 2 {
		rr := httptest.NewRecorder()
		h.SendTest(rr, findingsRequest(t, "OWNER", "u-owner"))
		if rr.Code != http.StatusOK {
			t.Fatalf("send %d: got %d: %s", i+1, rr.Code, rr.Body.String())
		}
	}
	if n := countInboxItems(t, db); n != 2 {
		t.Errorf("two sends produced %d inbox items, want 2", n)
	}
}

func TestKeeperFindingsTest_RequiresManageRole(t *testing.T) {
	db := setupTestDB(t)
	seedFindingsWorkspace(t, db)
	h := NewAdminKeeperFindingsHandler(db, nil, nil, newTestLogger())

	rr := httptest.NewRecorder()
	h.SendTest(rr, findingsRequest(t, "MEMBER", "u-member"))
	if rr.Code != http.StatusForbidden {
		t.Errorf("member got %d, want 403", rr.Code)
	}
	if n := countInboxItems(t, db); n != 0 {
		t.Errorf("a forbidden request still wrote %d items", n)
	}
}
