package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These tests document finding A1 (HIGH) from the 2026-06 security audit
// (.claude/context/SECURITY-AUDIT-2026-06.md): CrewHandler.AddMember
// (internal/api/crew_members.go:104-193) gates only on canRole(role,"create")
// — which lets MANAGER through — and validates the requested per-crew role
// ONLY against the roleRank enum (crew_members.go:178-186). It NEVER checks
// that the granted per-crew role stays at or below the caller's effective
// role. So a workspace MANAGER can POST /crews/{id}/members {role:"OWNER"}
// and ladder a member straight past the workspace gate.
//
// The sibling UpdateMemberRole (crew_members.go:291-313) DOES block this:
// it hard-gates promotion/demotion to workspace OWNER/ADMIN only. AddMember
// is the unguarded twin.
//
// Fixed: AddMember now enforces a role-grant ceiling (crew_members.go) —
// a create-capable caller can never grant a per-crew role that outranks
// their own effective (workspace) role. The escalation rows below assert
// that secure behavior (403 + nothing persisted); the non-escalation and
// non-create-capable rows remain regression guards.

// addMemberRBACRig is a fresh-DB harness mirroring newCovCMRig but with a
// dedicated target user that is a workspace member yet not yet a crew member,
// so AddMember reaches the role-grant path instead of short-circuiting on the
// membership/duplicate guards.
type addMemberRBACRig struct {
	h        *CrewHandler
	db       *sql.DB
	callerID string
	wsID     string
	crewID   string
	targetID string
}

func newAddMemberRBACRig(t *testing.T) *addMemberRBACRig {
	t.Helper()
	db := setupTestDB(t)
	callerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, callerID)
	crewID := "crew-rbac-ceil"
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Ceil', 'ceil')`,
		crewID, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	// Target user: a real workspace member (so the membership guard passes)
	// but NOT yet a crew member (so the duplicate guard passes). Its own
	// workspace role is irrelevant to the bug — AddMember only checks that a
	// membership row exists.
	targetID := "target-user-id"
	if _, err := db.Exec(
		`INSERT INTO users (id, email, full_name) VALUES (?, 'target@example.com', 'Target')`,
		targetID); err != nil {
		t.Fatalf("seed target user: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-target', ?, ?, 'MEMBER')`,
		wsID, targetID); err != nil {
		t.Fatalf("seed target ws membership: %v", err)
	}
	return &addMemberRBACRig{
		h:        NewCrewHandler(db, newTestLogger()),
		db:       db,
		callerID: callerID,
		wsID:     wsID,
		crewID:   crewID,
		targetID: targetID,
	}
}

func (r *addMemberRBACRig) addMember(t *testing.T, callerRole, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST",
		"/api/v1/crews/"+r.crewID+"/members", strings.NewReader(body))
	req.SetPathValue("crewId", r.crewID)
	req = withWorkspaceUser(req, r.callerID, r.wsID, callerRole)
	rec := httptest.NewRecorder()
	r.h.AddMember(rec, req)
	return rec
}

// TestAddMember_RoleGrantCeiling_VULN is the table-driven heart of A1. For
// every (caller role × requested req.Role) it computes the SECURE expectation
// — granted role must be <= caller's effective (workspace) role — and compares
// it against what AddMember actually does.
//
//   - VIEWER/MEMBER callers are stopped by canRole(...,"create") (403). Secure
//     today; asserted as a regression guard.
//   - Create-capable callers (MANAGER/ADMIN/OWNER) granting a role <= their own
//     are accepted (201) and the role persists. Regression guard.
//   - Create-capable callers granting a role ABOVE their own are rejected with
//     403 and nothing is persisted — the A1 ladder is closed.
func TestAddMember_RoleGrantCeiling_VULN(t *testing.T) {
	cases := []struct {
		name       string
		callerRole string
		reqRole    string // requested per-crew override ("" = inherit)
	}{
		// Non-create-capable callers — blocked at the create gate.
		{"viewer grants member", "VIEWER", "MEMBER"},
		{"member grants member", "MEMBER", "MEMBER"},

		// Create-capable, no escalation — legitimate grants.
		{"manager inherit", "MANAGER", ""},
		{"manager grants member", "MANAGER", "MEMBER"},
		{"manager grants manager", "MANAGER", "MANAGER"},
		{"admin grants admin", "ADMIN", "ADMIN"},
		{"owner grants admin", "OWNER", "ADMIN"},

		// Create-capable, ESCALATION — the A1 ladder.
		{"manager grants admin", "MANAGER", "ADMIN"},
		{"manager grants owner", "MANAGER", "OWNER"},
		{"admin grants owner", "ADMIN", "OWNER"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newAddMemberRBACRig(t)

			body := `{"user_id":"` + r.targetID + `"}`
			if tc.reqRole != "" {
				body = `{"user_id":"` + r.targetID + `","role":"` + tc.reqRole + `"}`
			}
			rec := r.addMember(t, tc.callerRole, body)

			createCapable := canRole(tc.callerRole, "create")
			escalates := roleRank[tc.reqRole] > roleRank[tc.callerRole]

			switch {
			case !createCapable:
				// Secure today: create gate rejects VIEWER/MEMBER.
				if rec.Code != http.StatusForbidden {
					t.Errorf("caller=%s req=%q: status=%d want 403 (create gate); body=%s",
						tc.callerRole, tc.reqRole, rec.Code, rec.Body.String())
				}

			case escalates:
				// A1 fixed: granting a role above the caller's effective role
				// must be rejected with 403 and nothing may be persisted.
				if rec.Code != http.StatusForbidden {
					t.Fatalf("caller=%s req=%q: status=%d want 403 (role ceiling); body=%s",
						tc.callerRole, tc.reqRole, rec.Code, rec.Body.String())
				}
				var n int
				if err := r.db.QueryRow(
					`SELECT COUNT(*) FROM crew_members WHERE crew_id = ? AND user_id = ?`,
					r.crewID, r.targetID).Scan(&n); err != nil {
					t.Fatalf("count persisted rows: %v", err)
				}
				if n != 0 {
					t.Fatalf("caller=%s req=%q: %d membership rows persisted, want 0 (rejected grant must not write)",
						tc.callerRole, tc.reqRole, n)
				}

			default:
				// Create-capable, no escalation — a legitimate grant must succeed.
				if rec.Code != http.StatusCreated {
					t.Errorf("caller=%s req=%q: status=%d want 201 (legitimate grant <= caller role); body=%s",
						tc.callerRole, tc.reqRole, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

// TestAddMember_RoleGrantCeiling_SecureTarget is the active regression test
// for the A1 fix: AddMember enforces the role ceiling (mirroring
// UpdateMemberRole's promotion gate).
//
// A create-capable caller granting a per-crew role ABOVE their own effective
// role must be rejected with 403 and nothing must be persisted.
func TestAddMember_RoleGrantCeiling_SecureTarget(t *testing.T) {
	escalations := []struct {
		callerRole string
		reqRole    string
	}{
		{"MANAGER", "ADMIN"},
		{"MANAGER", "OWNER"},
		{"ADMIN", "OWNER"},
	}
	for _, e := range escalations {
		r := newAddMemberRBACRig(t)
		rec := r.addMember(t, e.callerRole, `{"user_id":"`+r.targetID+`","role":"`+e.reqRole+`"}`)
		if rec.Code != http.StatusForbidden {
			t.Errorf("caller=%s req=%s: status=%d want 403 (role ceiling)", e.callerRole, e.reqRole, rec.Code)
		}
		var n int
		_ = r.db.QueryRow(`SELECT COUNT(*) FROM crew_members WHERE crew_id = ? AND user_id = ?`,
			r.crewID, r.targetID).Scan(&n)
		if n != 0 {
			t.Errorf("caller=%s req=%s: %d membership rows persisted, want 0", e.callerRole, e.reqRole, n)
		}
	}
}
