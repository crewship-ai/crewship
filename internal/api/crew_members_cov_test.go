package api

// Coverage tests for crew_members.go — role-override mapping, validation
// 400s, and DB error branches (driven by dropping the table the handler
// touches mid-flow, which leaves earlier guard queries working).

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type covCMRig struct {
	h      *CrewHandler
	db     *sql.DB
	userID string
	wsID   string
	crewID string
}

func newCovCMRig(t *testing.T) *covCMRig {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := "crew-cm"
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'CM', 'cm')`, crewID, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	return &covCMRig{h: NewCrewHandler(db, newTestLogger()), db: db, userID: userID, wsID: wsID, crewID: crewID}
}

func (r *covCMRig) req(method, body, crewID, memberID, role string) *http.Request {
	req := httptest.NewRequest(method, "/x", strings.NewReader(body))
	if crewID != "" {
		req.SetPathValue("crewId", crewID)
	}
	if memberID != "" {
		req.SetPathValue("memberId", memberID)
	}
	return withWorkspaceUser(req, r.userID, r.wsID, role)
}

// --- ListMembers ---------------------------------------------------------

func TestCovCMListMembers_RoleOverrideMapping(t *testing.T) {
	r := newCovCMRig(t)
	// Member WITH per-crew role override and one WITHOUT.
	if _, err := r.db.Exec(`INSERT INTO crew_members (id, crew_id, user_id, role, created_at)
		VALUES ('cm-1', ?, ?, 'MANAGER', '2026-01-01T00:00:00Z')`, r.crewID, r.userID); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if _, err := r.db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('u-plain', 'p@x', 'P')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := r.db.Exec(`INSERT INTO crew_members (id, crew_id, user_id, created_at)
		VALUES ('cm-2', ?, 'u-plain', '2026-01-02T00:00:00Z')`, r.crewID); err != nil {
		t.Fatalf("seed member 2: %v", err)
	}

	rec := httptest.NewRecorder()
	r.h.ListMembers(rec, r.req("GET", "", r.crewID, "", "MEMBER"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var out []crewMemberResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Role == nil || *out[0].Role != "MANAGER" {
		t.Errorf("first member role = %v, want MANAGER override", out[0].Role)
	}
	if out[1].Role != nil {
		t.Errorf("second member role = %v, want nil (inherit)", out[1].Role)
	}
	if out[0].User == nil || out[0].User.Email != "test@example.com" {
		t.Errorf("user join missing: %+v", out[0].User)
	}
}

func TestCovCMListMembers_QueryError500(t *testing.T) {
	r := newCovCMRig(t)
	// Crew check passes (crews intact); member listing fails.
	if _, err := r.db.Exec(`DROP TABLE crew_members`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	r.h.ListMembers(rec, r.req("GET", "", r.crewID, "", "MEMBER"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- AddMember -------------------------------------------------------------

func TestCovCMAddMember_RoleOverride(t *testing.T) {
	r := newCovCMRig(t)

	t.Run("invalid role 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.AddMember(rec, r.req("POST", `{"user_id":"`+r.userID+`","role":"SUPREME_LEADER"}`, r.crewID, "", "ADMIN"))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("valid role persisted", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.AddMember(rec, r.req("POST", `{"user_id":"`+r.userID+`","role":"MANAGER"}`, r.crewID, "", "ADMIN"))
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
		}
		var role sql.NullString
		if err := r.db.QueryRow(`SELECT role FROM crew_members WHERE crew_id = ? AND user_id = ?`, r.crewID, r.userID).Scan(&role); err != nil {
			t.Fatalf("query: %v", err)
		}
		if !role.Valid || role.String != "MANAGER" {
			t.Errorf("role = %+v, want MANAGER", role)
		}
	})

	t.Run("duplicate 409", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.AddMember(rec, r.req("POST", `{"user_id":"`+r.userID+`"}`, r.crewID, "", "ADMIN"))
		if rec.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409", rec.Code)
		}
	})
}

func TestCovCMAddMember_DBErrors(t *testing.T) {
	t.Run("ws membership check error", func(t *testing.T) {
		r := newCovCMRig(t)
		if _, err := r.db.Exec(`DROP TABLE workspace_members`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		rec := httptest.NewRecorder()
		r.h.AddMember(rec, r.req("POST", `{"user_id":"u-x"}`, r.crewID, "", "ADMIN"))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})

	t.Run("crew membership check error", func(t *testing.T) {
		r := newCovCMRig(t)
		if _, err := r.db.Exec(`DROP TABLE crew_members`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		rec := httptest.NewRecorder()
		r.h.AddMember(rec, r.req("POST", `{"user_id":"`+r.userID+`"}`, r.crewID, "", "ADMIN"))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})
}

// --- RemoveMember -----------------------------------------------------------

func TestCovCMRemoveMember_Guards(t *testing.T) {
	r := newCovCMRig(t)

	t.Run("missing crewId 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.RemoveMember(rec, r.req("DELETE", "", "", "m-1", "ADMIN"))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("member lookup error 500", func(t *testing.T) {
		if _, err := r.db.Exec(`DROP TABLE crew_members`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		rec := httptest.NewRecorder()
		r.h.RemoveMember(rec, r.req("DELETE", "", r.crewID, "m-1", "ADMIN"))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})
}

// --- UpdateMemberRole ----------------------------------------------------------

func TestCovCMUpdateMemberRole_Guards(t *testing.T) {
	r := newCovCMRig(t)
	if _, err := r.db.Exec(`INSERT INTO crew_members (id, crew_id, user_id, created_at)
		VALUES ('cm-up', ?, ?, datetime('now'))`, r.crewID, r.userID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Run("missing ids 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.UpdateMemberRole(rec, r.req("PATCH", `{"role":"MANAGER"}`, "", "", "ADMIN"))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("invalid JSON 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.UpdateMemberRole(rec, r.req("PATCH", `{nope`, r.crewID, "cm-up", "ADMIN"))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})
}
