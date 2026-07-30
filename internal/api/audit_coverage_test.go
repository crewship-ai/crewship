package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The audit log answers "who changed this" only for the things something
// bothered to record. WriteAuditLog had 19 call sites — agents, backups,
// connectors, re-encrypt, consolidate — and nothing else. So a workspace could
// have its crews rebuilt, its credentials replaced, its members re-roled and
// its isolation boundary switched off without the log holding a single row,
// while the settings UI offered filters for Credentials, Crews, Users and
// System that could never match anything.
//
// These tests pin each of those events to a row. They assert the row exists
// and points at the right entity; the wording of the action is the handler's
// business, so they match a prefix rather than an exact verb.

func auditRowsFor(t *testing.T, db *sql.DB, wsID, entityType string) []struct {
	Action   string
	EntityID string
} {
	t.Helper()
	rows, err := db.Query(
		`SELECT action, COALESCE(entity_id,'') FROM audit_logs
		 WHERE workspace_id = ? AND UPPER(entity_type) = UPPER(?) ORDER BY created_at`,
		wsID, entityType)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	var out []struct {
		Action   string
		EntityID string
	}
	for rows.Next() {
		var r struct {
			Action   string
			EntityID string
		}
		if err := rows.Scan(&r.Action, &r.EntityID); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func assertAudited(t *testing.T, db *sql.DB, wsID, entityType, wantAction, wantEntity string) {
	t.Helper()
	for _, r := range auditRowsFor(t, db, wsID, entityType) {
		if strings.Contains(r.Action, wantAction) && (wantEntity == "" || r.EntityID == wantEntity) {
			return
		}
	}
	t.Errorf("no audit row for %s %q on %s — rows: %+v",
		entityType, wantAction, wantEntity, auditRowsFor(t, db, wsID, entityType))
}

func TestAuditCoverage_CrewLifecycle(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCrewHandler(db, newTestLogger())

	body := `{"name":"Platform","slug":"platform"}`
	rr := httptest.NewRecorder()
	h.Create(rr, withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/crews", strings.NewReader(body)), userID, wsID, "OWNER"))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rr.Code, rr.Body.String())
	}
	assertAudited(t, db, wsID, "CREW", "create", "")

	var crewID string
	if err := db.QueryRow(`SELECT id FROM crews WHERE workspace_id = ? AND slug = 'platform'`, wsID).Scan(&crewID); err != nil {
		t.Fatalf("find crew: %v", err)
	}

	upd := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID, strings.NewReader(`{"name":"Platform Team"}`))
	upd.SetPathValue("crewId", crewID)
	rrU := httptest.NewRecorder()
	h.Update(rrU, withWorkspaceUser(upd, userID, wsID, "OWNER"))
	if rrU.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", rrU.Code, rrU.Body.String())
	}
	assertAudited(t, db, wsID, "CREW", "update", crewID)

	del := httptest.NewRequest("DELETE", "/api/v1/crews/"+crewID, nil)
	del.SetPathValue("crewId", crewID)
	rrD := httptest.NewRecorder()
	h.Delete(rrD, withWorkspaceUser(del, userID, wsID, "OWNER"))
	if rrD.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", rrD.Code, rrD.Body.String())
	}
	assertAudited(t, db, wsID, "CREW", "delete", crewID)
}

func TestAuditCoverage_CredentialLifecycle(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("0123456789abcdef", 4))
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, newTestLogger())

	body := `{"name":"GH_TOKEN","value":"ghp_secret","type":"API_KEY","scope":"WORKSPACE"}`
	rr := httptest.NewRecorder()
	h.Create(rr, withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/credentials", strings.NewReader(body)), userID, wsID, "OWNER"))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rr.Code, rr.Body.String())
	}
	// The secret must never reach the audit row — the log is read by more
	// people than the credential is.
	assertAudited(t, db, wsID, "CREDENTIAL", "create", "")
	var meta sql.NullString
	if err := db.QueryRow(
		`SELECT metadata FROM audit_logs WHERE workspace_id = ? AND UPPER(entity_type)='CREDENTIAL'`, wsID,
	).Scan(&meta); err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if strings.Contains(meta.String, "ghp_secret") {
		t.Fatalf("the credential value leaked into the audit metadata: %s", meta.String)
	}

	var credID string
	if err := db.QueryRow(`SELECT id FROM credentials WHERE workspace_id = ?`, wsID).Scan(&credID); err != nil {
		t.Fatalf("find credential: %v", err)
	}
	del := httptest.NewRequest("DELETE", "/api/v1/credentials/"+credID, nil)
	del.SetPathValue("credentialId", credID)
	rrD := httptest.NewRecorder()
	h.Delete(rrD, withWorkspaceUser(del, userID, wsID, "OWNER"))
	if rrD.Code != http.StatusOK && rrD.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body=%s", rrD.Code, rrD.Body.String())
	}
	assertAudited(t, db, wsID, "CREDENTIAL", "delete", credID)
}

func TestAuditCoverage_MemberRoleChange(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	memberID := "audit-member-user"
	if _, err := db.Exec(
		`INSERT INTO users (id, email, full_name) VALUES (?, 'member@example.com', 'A Member')`, memberID); err != nil {
		t.Fatalf("seed member user: %v", err)
	}
	membershipID := "audit-membership"
	if _, err := db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'MEMBER')`,
		membershipID, wsID, memberID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	h := NewWorkspaceHandler(db, newTestLogger())
	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+wsID+"/members/"+memberID,
		strings.NewReader(`{"role":"MANAGER"}`))
	req.SetPathValue("workspaceId", wsID)
	req.SetPathValue("memberId", membershipID)
	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, withWorkspaceUser(req, ownerID, wsID, "OWNER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}

	// Recorded against the USER, not the membership row: the name resolver
	// turns that into the person's email, which is what the reader is after.
	assertAudited(t, db, wsID, "WorkspaceMember", "role", memberID)
}

// The privileged-credentials switch removes the fail-closed boundary between
// privileged crews and stored secrets. If any workspace setting deserves a
// row, it is that one.
func TestAuditCoverage_WorkspaceSettingsChange(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewWorkspaceHandler(db, newTestLogger())

	req := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+wsID,
		strings.NewReader(`{"allow_privileged_credentials":true}`))
	req.SetPathValue("workspaceId", wsID)
	rr := httptest.NewRecorder()
	h.Update(rr, withWorkspaceUser(req, userID, wsID, "OWNER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}

	assertAudited(t, db, wsID, "WORKSPACE", "update", wsID)
}

func TestAuditCoverage_CrewLinkLifecycle(t *testing.T) {
	h, db, userID, wsID, crewA, crewB := crewConnectionsRig(t)

	rr := httptest.NewRecorder()
	h.Create(rr, withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/crew-connections",
			strings.NewReader(`{"from_crew_id":"`+crewA+`","to_crew_id":"`+crewB+`"}`)),
		userID, wsID, "OWNER"))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rr.Code, rr.Body.String())
	}
	assertAudited(t, db, wsID, "CREW_LINK", "create", "")

	var connID string
	if err := db.QueryRow(`SELECT id FROM crew_connections WHERE workspace_id = ?`, wsID).Scan(&connID); err != nil {
		t.Fatalf("find connection: %v", err)
	}
	del := httptest.NewRequest("DELETE", "/api/v1/crew-connections/"+connID, nil)
	del.SetPathValue("connectionId", connID)
	rrD := httptest.NewRecorder()
	h.Delete(rrD, withWorkspaceUser(del, userID, wsID, "OWNER"))
	if rrD.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body=%s", rrD.Code, rrD.Body.String())
	}
	assertAudited(t, db, wsID, "CREW_LINK", "delete", connID)
}
