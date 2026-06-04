package api

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

// CrewHandler.UpdateMemberRole sets or clears a per-crew role override.
// Only workspace OWNER/ADMIN may reshape membership permissions.

func seedCrewMemberRow(t *testing.T, db *sql.DB, memberID, crewID, userID, role string) {
	t.Helper()
	var roleVal interface{}
	if role != "" {
		roleVal = role
	}
	if _, err := db.Exec(
		`INSERT INTO crew_members (id, crew_id, user_id, role) VALUES (?, ?, ?, ?)`,
		memberID, crewID, userID, roleVal); err != nil {
		t.Fatalf("seed crew_member %s: %v", memberID, err)
	}
}

func updateRoleReq(t *testing.T, userID, wsID, crewID, memberID, wsRole, jsonBody string) *http.Request {
	t.Helper()
	req := httptest.NewRequest("PATCH",
		"/api/v1/crews/"+crewID+"/members/"+memberID, bytes.NewBufferString(jsonBody))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("memberId", memberID)
	return withWorkspaceUser(req, userID, wsID, wsRole)
}

func TestUpdateMemberRole_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-mr", wsID, "Eng", "eng")
	seedCrewMemberRow(t, db, "cm-1", crewID, userID, "MEMBER")

	h := NewCrewHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, updateRoleReq(t, userID, wsID, crewID, "cm-1", "MANAGER", `{"role":"ADMIN"}`))
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403 (MANAGER cannot reshape membership); body=%s", rr.Code, rr.Body.String())
	}
}

func TestUpdateMemberRole_NotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-mr", wsID, "Eng", "eng")

	h := NewCrewHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, updateRoleReq(t, userID, wsID, crewID, "ghost", "OWNER", `{"role":"ADMIN"}`))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestUpdateMemberRole_BadRole(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-mr", wsID, "Eng", "eng")
	seedCrewMemberRow(t, db, "cm-1", crewID, userID, "MEMBER")

	h := NewCrewHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, updateRoleReq(t, userID, wsID, crewID, "cm-1", "OWNER", `{"role":"SUPREME"}`))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 (unknown role); body=%s", rr.Code, rr.Body.String())
	}
}

func TestUpdateMemberRole_SetOverride(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-mr", wsID, "Eng", "eng")
	seedCrewMemberRow(t, db, "cm-1", crewID, userID, "MEMBER")

	h := NewCrewHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, updateRoleReq(t, userID, wsID, crewID, "cm-1", "OWNER", `{"role":"ADMIN"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var role sql.NullString
	if err := db.QueryRow("SELECT role FROM crew_members WHERE id = 'cm-1'").Scan(&role); err != nil {
		t.Fatalf("query role: %v", err)
	}
	if !role.Valid || role.String != "ADMIN" {
		t.Errorf("role=%v want ADMIN", role)
	}
}

func TestUpdateMemberRole_ClearOverride(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-mr", wsID, "Eng", "eng")
	seedCrewMemberRow(t, db, "cm-1", crewID, userID, "ADMIN")

	h := NewCrewHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.UpdateMemberRole(rr, updateRoleReq(t, userID, wsID, crewID, "cm-1", "OWNER", `{"role":""}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var role sql.NullString
	if err := db.QueryRow("SELECT role FROM crew_members WHERE id = 'cm-1'").Scan(&role); err != nil {
		t.Fatalf("query role: %v", err)
	}
	if role.Valid {
		t.Errorf("role=%q want NULL (cleared)", role.String)
	}
}
