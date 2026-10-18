package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// The instance administrator route and GDPR route can admit a person who
// cannot open the Page. They receive opaque IDs, never names or contacts.
func TestPrivateOwnershipDepartureConflictPrivacy(t *testing.T) {
	for _, route := range []string{"instance", "gdpr", "workspace"} {
		t.Run(route, func(t *testing.T) {
			f := newInstanceFixture(t)
			t.Cleanup(f.r.Shutdown)
			seedInstanceUser(t, f.db, "departing", "departing@example.invalid", "ws-old", "MEMBER")
			wmSeedPage(t, f.db, "ws-old", "secret-page-id", "secret-page-slug", "Secret private title", "departing", "")
			mustExec(t, f.db, `CREATE TABLE documents(page_id TEXT PRIMARY KEY REFERENCES pages(id))`)
			mustExec(t, f.db, `INSERT INTO documents VALUES('secret-page-id')`)
			var path, body, token string
			if route == "workspace" {
				var member string
				if err := f.db.QueryRow(`SELECT id FROM workspace_members WHERE user_id='departing'`).Scan(&member); err != nil {
					t.Fatal(err)
				}
				path = "/api/v1/workspaces/ws-old/members/" + member
				token = f.wsAdmin
			} else {
				mustExec(t, f.db, `DELETE FROM workspace_members WHERE user_id='boss' AND workspace_id='ws-old'`)
				path = "/api/v1/admin/instance/workspaces/ws-old/members/departing"
				token = f.boss
				if route == "gdpr" {
					path = "/api/v1/admin/users/departing/data?workspace_id=ws-old"
					body = `{"reason":"custody regression"}`
				}
			}
			w := f.do(token, "DELETE", path, body)
			wantCode(t, w, 409, "private output departure conflict")
			var response map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response["code"] != "page_ownership_transfer_required" || !strings.Contains(w.Body.String(), "secret-page-id") {
				t.Fatalf("missing actionable custody receipt: %s", w.Body.String())
			}
			if route == "workspace" {
				if !strings.Contains(w.Body.String(), "Secret private title") {
					t.Fatal("workspace administrator lost custody details")
				}
			} else {
				for _, private := range []string{"secret-page-slug", "Secret private title", "eligible_successors", "owner_name", "departing@example.invalid"} {
					if strings.Contains(w.Body.String(), private) {
						t.Errorf("instance-only caller received %q: %s", private, w.Body.String())
					}
				}
			}
			if route == "gdpr" {
				var audit string
				if err := f.db.QueryRow(`SELECT error FROM gdpr_actions`).Scan(&audit); err != nil || !strings.Contains(audit, "secret-page-id") || strings.Contains(audit, "Secret private title") {
					t.Fatalf("audit privacy: %s %v", audit, err)
				}
			}
			var owner string
			if err := f.db.QueryRow(`SELECT owner_user_id FROM pages WHERE id='secret-page-id'`).Scan(&owner); err != nil || owner != "departing" {
				t.Fatalf("conflict changed custody: %s %v", owner, err)
			}
		})
	}
}

func TestPrivateOwnershipWorkspaceConflictRechecksAdmission(t *testing.T) {
	r := wmRemoveMemberSetup(t)
	wmSeedPage(t, r.db, r.wsID, "secret-page-id", "secret-page-slug", "Secret private title", r.memberUser, "")
	mustExec(t, r.db, `CREATE TABLE documents(page_id TEXT PRIMARY KEY REFERENCES pages(id))`)
	mustExec(t, r.db, `INSERT INTO documents VALUES('secret-page-id')`)
	request := withWorkspaceUser(httptest.NewRequest("DELETE", "/", nil), r.ownerID, r.wsID, "OWNER")
	request.SetPathValue("memberId", r.memberID)
	mustExec(t, r.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=? AND workspace_id=?`, r.ownerID, r.wsID)
	w := httptest.NewRecorder()
	r.h.RemoveMember(w, request)
	wantCode(t, w, 403, "revoked workspace administrator")
	for _, private := range []string{"secret-page-id", "secret-page-slug", "Secret private title"} {
		if strings.Contains(w.Body.String(), private) {
			t.Errorf("stale admission disclosed %q", private)
		}
	}
}
