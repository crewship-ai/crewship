package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type custodyAdmissionChangeReader struct {
	io.Reader
	onRead func()
	once   sync.Once
}

func (r *custodyAdmissionChangeReader) Read(p []byte) (int, error) {
	r.once.Do(r.onRead)
	return r.Reader.Read(p)
}

// This handler-context regression models middleware admitting an administrator
// before membership changes while departure waits for the database writer. It
// does not claim to synchronize that wait through an actual HTTP Router.
func TestOutputCustodyWorkspaceConflictRechecksStaleAdmission(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		for _, role := range []string{"removed", "MEMBER", "ADMIN", "OWNER"} {
			t.Run(kind+"/"+role, func(t *testing.T) {
				f := custodyRouterFixture(t, kind)
				t.Cleanup(f.r.Shutdown)
				mustExec(t, f.db, `UPDATE pages SET slug='private-slug-canary',name='Private title canary' WHERE id='private-output'`)
				mustExec(t, f.db, `UPDATE pages SET slug='ordinary-private-slug-canary',name='Private ordinary title canary' WHERE id='regular-page'`)
				var memberID string
				if err := f.db.QueryRow(`SELECT id FROM workspace_members WHERE workspace_id='ws-old' AND user_id='departing'`).Scan(&memberID); err != nil {
					t.Fatal(err)
				}
				admittedRole := "ADMIN"
				if role == "OWNER" {
					admittedRole = role
				}
				request := pagesRequest(t, "DELETE", "/api/v1/workspaces/ws-old/members/"+memberID, "ws-old", "wsadmin", admittedRole, "")
				request.SetPathValue("memberId", memberID)
				if role == "removed" {
					mustExec(t, f.db, `DELETE FROM workspace_members WHERE user_id='wsadmin' AND workspace_id='ws-old'`)
				} else {
					mustExec(t, f.db, `UPDATE workspace_members SET role=? WHERE user_id='wsadmin' AND workspace_id='ws-old'`, role)
				}
				before := custodySnapshot(t, f.db, custodyProtectedTables...)
				w := httptest.NewRecorder()
				NewWorkspaceHandler(f.db, newTestLogger()).RemoveMember(w, request)
				var response map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if role == "ADMIN" || role == "OWNER" {
					wantCode(t, w, 409, "current workspace administrator receives custody details")
					pages, ok := response["pages"].([]any)
					if !ok || len(pages) != 1 || pages[0].(map[string]any)["slug"] != "private-slug-canary" || pages[0].(map[string]any)["type"] != kind || !strings.HasPrefix(response["error"].(string), "cannot remove this member: ") {
						t.Errorf("authorized workspace administrator lost custody metadata: %s", w.Body.String())
					}
				} else {
					wantCode(t, w, 403, "revoked workspace administration cannot inspect custody")
					if len(response) != 1 || response["error"] == nil {
						t.Errorf("revoked administrator received custody IDs/details: %s", w.Body.String())
					}
					for _, private := range []string{"private-output", "regular-page", "private-slug-canary", "Private title canary", "ordinary-private-slug-canary", "Private ordinary title canary"} {
						if strings.Contains(w.Body.String(), private) {
							t.Errorf("revoked administrator received private metadata %q", private)
						}
					}
				}
				if after := custodySnapshot(t, f.db, custodyProtectedTables...); after != before {
					t.Error("refused departure mutated protected content or membership")
				}
			})
		}
	}
}

func TestOutputCustodyGDPRRevokedWorkspaceAdminGetsNoPageIDs(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		t.Run(kind, func(t *testing.T) {
			f := custodyRouterFixture(t, kind)
			t.Cleanup(f.r.Shutdown)
			before := custodySnapshot(t, f.db, custodyProtectedTables...)
			request := httptest.NewRequest("DELETE", "/api/v1/admin/users/departing/data?workspace_id=ws-old", &custodyAdmissionChangeReader{Reader: strings.NewReader(`{"reason":"revoked workspace administration"}`), onRead: func() {
				mustExec(t, f.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id='wsadmin' AND workspace_id='ws-old'`)
				before = custodySnapshot(t, f.db, custodyProtectedTables...)
			}})
			request.Header.Set("Authorization", "Bearer "+f.wsAdmin)
			request.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			f.r.ServeHTTP(w, request)
			wantCode(t, w, 403, "ordinary revoked workspace administrator has no instance exception")
			var response map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			action, _ := response["action_id"].(string)
			if len(response) != 4 || action == "" || response["data_subject"] != "departing" || response["workspace_id"] != "ws-old" || response["error"] == nil {
				t.Errorf("403 must retain GDPR receipt without custody metadata: %s", w.Body.String())
			}
			for _, private := range []string{"private-output", "regular-page", "Private output", "Regular Page"} {
				if strings.Contains(w.Body.String(), private) {
					t.Errorf("revoked GDPR administrator received private Page identity %q", private)
				}
			}
			if after := custodySnapshot(t, f.db, custodyProtectedTables...); after != before {
				t.Error("refused erasure mutated protected content or membership")
			}
			var status, actor, auditError string
			if err := f.db.QueryRow(`SELECT status,actor_user_id,error FROM gdpr_actions WHERE id=?`, action).Scan(&status, &actor, &auditError); err != nil {
				t.Fatal(err)
			}
			if status != "failed" || actor != "wsadmin" || !strings.HasPrefix(auditError, "page_ownership_transfer_required:") || strings.Contains(auditError, "Private output") {
				t.Errorf("revoked GDPR request lost opaque failed audit: %s %s %s", status, actor, auditError)
			}
		})
	}
}

// Direct handler context models an instance gate that admitted the actor before
// their instance role was revoked; it does not claim an actual Router wait race.
func TestOutputCustodyInstanceConflictRechecksStaleAdmission(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		t.Run(kind, func(t *testing.T) {
			f := custodyRouterFixture(t, kind)
			t.Cleanup(f.r.Shutdown)
			request := pagesRequest(t, "DELETE", "/api/v1/admin/instance/workspaces/ws-old/members/departing", "ws-old", "boss", "OWNER", "")
			request.SetPathValue("workspaceId", "ws-old")
			request.SetPathValue("userId", "departing")
			request = request.WithContext(context.WithValue(request.Context(), ctxInstanceAdmin, true))
			mustExec(t, f.db, `UPDATE users SET instance_role=NULL WHERE id='boss'`)
			before := custodySnapshot(t, f.db, custodyProtectedTables...)
			w := httptest.NewRecorder()
			NewInstanceAdminHandler(f.db, newTestLogger(), nil, nil, nil).RemoveMembership(w, request)
			wantCode(t, w, 403, "revoked instance authority cannot inspect unresolved Page IDs")
			var response map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response) != 1 || response["error"] == nil {
				t.Errorf("revoked instance administrator received custody metadata: %s", w.Body.String())
			}
			if after := custodySnapshot(t, f.db, custodyProtectedTables...); after != before {
				t.Error("refused instance departure mutated protected content or membership")
			}
		})
	}
}

func TestOutputCustodyGDPRConflictUsesLiveWorkspaceVisibility(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		for _, role := range []string{"nonmember", "MEMBER", "ADMIN", "OWNER", "demoted after admission"} {
			t.Run(kind+"/"+role, func(t *testing.T) {
				f := custodyRouterFixture(t, kind)
				t.Cleanup(f.r.Shutdown)
				if role == "nonmember" {
					mustExec(t, f.db, `DELETE FROM workspace_members WHERE user_id='boss' AND workspace_id='ws-old'`)
				} else if role != "demoted after admission" {
					mustExec(t, f.db, `UPDATE workspace_members SET role=? WHERE user_id='boss' AND workspace_id='ws-old'`, role)
				}
				mustExec(t, f.db, `UPDATE pages SET slug='private-slug-canary',name='Private title canary' WHERE id='private-output'`)
				mustExec(t, f.db, `UPDATE pages SET slug='ordinary-private-slug-canary',name='Private ordinary title canary' WHERE id='regular-page'`)
				before := custodySnapshot(t, f.db, custodyProtectedTables...)
				path, body := "/api/v1/admin/users/departing/data?workspace_id=ws-old", `{"reason":"custody privacy regression"}`
				request := httptest.NewRequest("DELETE", path, strings.NewReader(body))
				if role == "demoted after admission" {
					// Middleware and adminContext admitted the OWNER. The body
					// read changes current membership before the custody conflict.
					request = httptest.NewRequest("DELETE", path, &custodyAdmissionChangeReader{Reader: strings.NewReader(body), onRead: func() {
						mustExec(t, f.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id='boss' AND workspace_id='ws-old'`)
						before = custodySnapshot(t, f.db, custodyProtectedTables...)
					}})
				}
				request.Header.Set("Authorization", "Bearer "+f.boss)
				request.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				f.r.ServeHTTP(w, request)
				wantCode(t, w, 409, "instance administrator reaches GDPR custody conflict")
				var response struct {
					ActionID  string           `json:"action_id"`
					SubjectID string           `json:"data_subject"`
					Workspace string           `json:"workspace_id"`
					Code      string           `json:"code"`
					Error     string           `json:"error"`
					Pages     []map[string]any `json:"pages"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.ActionID == "" || response.SubjectID != "departing" || response.Workspace != "ws-old" || response.Code != "page_ownership_transfer_required" || !strings.HasPrefix(response.Error, "cannot erase this user: ") || !strings.Contains(response.Error, "workspace administrator") {
					t.Errorf("lost GDPR receipt or actionable conflict: %s", w.Body.String())
				}
				private := []string{"private-slug-canary", "Private title canary", "ordinary-private-slug-canary", "Private ordinary title canary", "departing@ex.com"}
				if role == "ADMIN" || role == "OWNER" {
					if len(response.Pages) != 1 || response.Pages[0]["id"] != "private-output" || response.Pages[0]["slug"] != "private-slug-canary" || response.Pages[0]["type"] != kind || !strings.Contains(response.Error, "ordinary-private-slug-canary") {
						t.Errorf("authorized workspace administrator lost metadata: %s", w.Body.String())
					}
				} else {
					for _, canary := range append(private, "owner_name", "owner_user_id", "eligible_successors", "slug", "name", "type") {
						if strings.Contains(w.Body.String(), canary) {
							t.Errorf("instance-only GDPR conflict exposes %q: %s", canary, w.Body.String())
						}
					}
					ids := map[string]bool{}
					for _, page := range response.Pages {
						id, ok := page["id"].(string)
						if len(page) != 1 || !ok || id == "" {
							t.Errorf("expected opaque unresolved Page ID only: %+v", page)
						}
						ids[id] = true
					}
					if len(ids) != 2 || !ids["private-output"] || !ids["regular-page"] {
						t.Errorf("missing unresolved Page IDs: %v", ids)
					}
				}
				if after := custodySnapshot(t, f.db, custodyProtectedTables...); after != before {
					t.Error("refused GDPR operation mutated protected content or membership")
				}
				var status, actor, subject, workspace, scope, auditError, completed string
				if err := f.db.QueryRow(`SELECT status,actor_user_id,data_subject_id,workspace_id,scope_json,COALESCE(error,''),COALESCE(completed_at,'') FROM gdpr_actions WHERE id=?`, response.ActionID).Scan(&status, &actor, &subject, &workspace, &scope, &auditError, &completed); err != nil {
					t.Fatal(err)
				}
				if status != "failed" || actor != "boss" || subject != "departing" || workspace != "ws-old" || completed == "" || !strings.Contains(auditError, "private-output") || !strings.Contains(auditError, "regular-page") {
					t.Errorf("lost failed GDPR audit attribution/opaque Page IDs: %s %s %s %s %s", status, actor, subject, workspace, auditError)
				}
				for _, canary := range private {
					if strings.Contains(auditError+scope, canary) {
						t.Errorf("persisted GDPR audit exposes private metadata %q", canary)
					}
				}
				var actions int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM gdpr_actions`).Scan(&actions); err != nil || actions != 1 {
					t.Errorf("failed attempt must retain exactly one audit row: %d %v", actions, err)
				}
			})
		}
	}
}

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
