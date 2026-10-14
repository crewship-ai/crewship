package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOutputCustodyInstanceConflictOnlyReturnsOpaqueIDs(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		t.Run(kind, func(t *testing.T) {
			f := custodyRouterFixture(t, kind)
			t.Cleanup(f.r.Shutdown)
			// An instance administrator need not belong to the affected workspace.
			mustExec(t, f.db, `DELETE FROM workspace_members WHERE user_id='boss' AND workspace_id='ws-old'`)
			mustExec(t, f.db, `UPDATE pages SET slug='private-slug-canary',name='Private title canary' WHERE id='private-output'`)
			mustExec(t, f.db, `UPDATE pages SET slug='ordinary-private-slug-canary',name='Private ordinary title canary' WHERE id='regular-page'`)
			before := custodySnapshot(t, f.db, custodyProtectedTables...)
			w := f.do(f.boss, "DELETE", "/api/v1/admin/instance/workspaces/ws-old/members/departing", "")
			wantCode(t, w, 409, "instance departure needs workspace custody")
			for _, private := range []string{"private-slug-canary", "Private title canary", "ordinary-private-slug-canary", "Private ordinary title canary", "departing@ex.com", "owner_name", "owner_user_id", "eligible_successors", "slug", "name", "type"} {
				if strings.Contains(w.Body.String(), private) {
					t.Fatalf("instance conflict discloses private metadata %q: %s", private, w.Body.String())
				}
			}
			var response struct {
				Code  string              `json:"code"`
				Error string              `json:"error"`
				Pages []map[string]string `json:"pages"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Code != "page_ownership_transfer_required" || !strings.Contains(response.Error, "workspace administrator") {
				t.Fatalf("missing actionable custody conflict: %s", w.Body.String())
			}
			ids := map[string]bool{}
			for _, page := range response.Pages {
				if len(page) != 1 || page["id"] == "" {
					t.Fatalf("expected only opaque ID: %+v", page)
				}
				ids[page["id"]] = true
			}
			if len(ids) != 2 || !ids["private-output"] || !ids["regular-page"] {
				t.Fatalf("blocked output and ordinary Page IDs missing: %v", ids)
			}
			if after := custodySnapshot(t, f.db, custodyProtectedTables...); after != before {
				t.Fatal("refusal mutated subject, content, membership or notices")
			}
		})
	}
}

func TestOutputCustodyConflictGuidesWorkspaceAdministrator(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		t.Run(kind, func(t *testing.T) {
			f := custodyRouterFixture(t, kind)
			t.Cleanup(f.r.Shutdown)
			var memberID string
			if err := f.db.QueryRow(`SELECT id FROM workspace_members WHERE workspace_id='ws-old' AND user_id='departing'`).Scan(&memberID); err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/workspaces/ws-old/members/" + memberID
			member := mintTokenFor(t, f.db, "departing", "custodyselfservice00000000000000")
			wantCode(t, f.do(member, "DELETE", path, ""), 403, "member cannot perform self-service departure")
			for _, mode := range []string{"member", "gdpr"} {
				methodPath, body := path, ""
				if mode == "gdpr" {
					methodPath = "/api/v1/admin/users/departing/data?workspace_id=ws-old"
					body = `{"reason":"custody guidance regression"}`
				}
				w := f.do(f.wsAdmin, "DELETE", methodPath, body)
				wantCode(t, w, 409, "administrator receives custody guidance")
				var response struct {
					Error string                 `json:"error"`
					Pages []pageOwnershipSummary `json:"pages"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(response.Error, "workspace administrator") || !strings.Contains(response.Error, "Artifact and Board ownership") {
					t.Fatalf("missing explicit Settings guidance: %s", w.Body.String())
				}
				if strings.Contains(response.Error, "cannot remove this member: cannot erase this user:") {
					t.Fatalf("duplicated operation prefix: %s", response.Error)
				}
				if len(response.Pages) != 1 || response.Pages[0].Slug != "private-output" || response.Pages[0].Type != kind {
					t.Fatalf("workspace administrator lost authorized metadata: %s", w.Body.String())
				}
			}
		})
	}
}
