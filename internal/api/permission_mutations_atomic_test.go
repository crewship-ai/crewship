package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPermissionMutationRechecksAdmittedActor(t *testing.T) {
	for _, door := range []string{"workspace_delete", "member_remove"} {
		for _, change := range []string{"demoted", "suspended", "restricted"} {
			t.Run(door+"/"+change, func(t *testing.T) {
				var h *WorkspaceHandler
				var user, ws string
				var invoke func() *httptest.ResponseRecorder
				if door == "workspace_delete" {
					h, user, ws = deleteRig(t)
					seedOtherWorkspace(t, h, "ws-other", "other", user)
					invoke = func() *httptest.ResponseRecorder {
						rr := httptest.NewRecorder()
						h.Delete(rr, deleteReq(t, user, ws, "OWNER", "test"))
						return rr
					}
				} else {
					f := wmRemoveMemberSetup(t)
					h, user, ws = f.h, f.ownerID, f.wsID
					invoke = func() *httptest.ResponseRecorder { return f.removeReq(t) }
				}
				switch change {
				case "demoted":
					execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, user)
				case "suspended":
					execOrFatal(t, h.db, `UPDATE users SET suspended_at='2026-10-20T00:00:00Z' WHERE id=?`, user)
				case "restricted":
					execOrFatal(t, h.db, `UPDATE workspace_members SET role='VIEWER' WHERE user_id=?`, user)
					execOrFatal(t, h.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id=?`, user)
				}
				rr := invoke()
				if rr.Code != http.StatusForbidden {
					t.Errorf("stale OWNER admission yielded %d want403: %s", rr.Code, rr.Body.String())
				}
				var n int
				if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE id=? AND deleted_at IS NULL`, ws).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 1 {
					t.Error("refused mutation deleted workspace")
				}
				if door == "member_remove" {
					if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id=? AND user_id='wm-departing'`, ws).Scan(&n); err != nil {
						t.Fatal(err)
					}
					if n != 1 {
						t.Error("refused mutation removed member")
					}
				}
			})
		}
	}
}

func TestWorkspaceDeleteAtomicAudit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "audit_failure"}[fail], func(t *testing.T) {
			h, user, ws := deleteRig(t)
			seedOtherWorkspace(t, h, "ws-other", "other", user)
			seedCrew(t, h.db, "audit-crew", ws, "Audit crew", "audit-crew")
			if fail {
				execOrFatal(t, h.db, `CREATE TRIGGER fail_workspace_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='workspace.deleted' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			}
			rr := httptest.NewRecorder()
			h.Delete(rr, deleteReq(t, user, ws, "OWNER", "test"))
			want := http.StatusOK
			if fail {
				want = http.StatusInternalServerError
			}
			if rr.Code != want {
				t.Errorf("HTTP=%d want=%d body=%s", rr.Code, want, rr.Body.String())
			}
			var deleted sql.NullString
			if err := h.db.QueryRow(`SELECT deleted_at FROM workspaces WHERE id=?`, ws).Scan(&deleted); err != nil {
				t.Fatal(err)
			}
			if deleted.Valid == fail {
				t.Error("workspace deletion did not commit/roll back with audit")
			}
			var n int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id=?`, ws).Scan(&n); err != nil {
				t.Fatal(err)
			}
			want = 0
			if fail {
				want = 1
			}
			if n != want {
				t.Errorf("membership count=%d want=%d", n, want)
			}
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND entry_type='workspace.deleted' AND seq IS NOT NULL`, ws).Scan(&n); err != nil {
				t.Fatal(err)
			}
			want = 1
			if fail {
				want = 0
			}
			if n != want {
				t.Errorf("chained deletion audit=%d want=%d", n, want)
			}
		})
	}
}

func TestFolderACLAtomicAudit(t *testing.T) {
	for _, op := range []string{"set", "removed"} {
		for _, fail := range []bool{false, true} {
			t.Run(op+map[bool]string{false: "/success", true: "/audit_failure"}[fail], func(t *testing.T) {
				f := newFoldersFixture(t)
				f.createFolder(t, "atomic-acl", "Atomic ACL", "crew/engine")
				if op == "removed" {
					f.share(t, "atomic-acl", "user", "frank", true)
				}
				var version int
				if err := f.h.db.QueryRow(`SELECT acl_version FROM page_folders WHERE workspace_id=? AND slug='atomic-acl'`, f.wsID).Scan(&version); err != nil {
					t.Fatal(err)
				}
				if fail {
					execOrFatal(t, f.h.db, `CREATE TRIGGER fail_folder_acl_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='page.folder_acl_changed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
				}
				method, path, body := "PUT", "/api/v1/page-folders/atomic-acl/acl", `{"subject_type":"user","subject_id":"frank","can_write":true}`
				want := http.StatusOK
				if op == "removed" {
					method, path, body, want = "DELETE", path+"/user/frank", "", http.StatusNoContent
				}
				if fail {
					want = http.StatusInternalServerError
				}
				rr := f.call(t, method, path, "erin", "ADMIN", body)
				if rr.Code != want {
					t.Errorf("HTTP=%d want=%d body=%s", rr.Code, want, rr.Body.String())
				}
				var after, n int
				if err := f.h.db.QueryRow(`SELECT acl_version FROM page_folders WHERE workspace_id=? AND slug='atomic-acl'`, f.wsID).Scan(&after); err != nil {
					t.Fatal(err)
				}
				if !fail {
					version++
				}
				if after != version {
					t.Errorf("ACL version=%d want=%d", after, version)
				}
				if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM page_folder_acl WHERE subject_id='frank'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				want = 1
				if op == "removed" && !fail || op == "set" && fail {
					want = 0
				}
				if n != want {
					t.Errorf("ACL entries=%d want=%d", n, want)
				}
				if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND entry_type='page.folder_acl_changed' AND json_extract(payload,'$.op')=? AND seq IS NOT NULL`, f.wsID, op).Scan(&n); err != nil {
					t.Fatal(err)
				}
				want = 1
				if fail {
					want = 0
				}
				if n != want {
					t.Errorf("chained ACL audit=%d want=%d", n, want)
				}
			})
		}
	}
}

func TestFolderACLRejectsIgnoredAndUndoneWrites(t *testing.T) {
	for _, fault := range []string{"ignore_acl", "ignore_version", "undo_after_audit"} {
		t.Run(fault, func(t *testing.T) {
			f := newFoldersFixture(t)
			f.createFolder(t, "atomic-acl", "Atomic ACL", "crew/engine")
			switch fault {
			case "ignore_acl":
				execOrFatal(t, f.h.db, `CREATE TRIGGER ignore_acl BEFORE INSERT ON page_folder_acl BEGIN SELECT RAISE(IGNORE); END`)
			case "ignore_version":
				execOrFatal(t, f.h.db, `CREATE TRIGGER ignore_version BEFORE UPDATE OF acl_version ON page_folders BEGIN SELECT RAISE(IGNORE); END`)
			case "undo_after_audit":
				execOrFatal(t, f.h.db, `CREATE TRIGGER undo_acl AFTER INSERT ON journal_entries WHEN NEW.entry_type='page.folder_acl_changed' BEGIN DELETE FROM page_folder_acl; UPDATE page_folders SET acl_version=0 WHERE slug='atomic-acl'; END`)
			}
			rr := f.call(t, "PUT", "/api/v1/page-folders/atomic-acl/acl", "erin", "ADMIN", `{"subject_type":"user","subject_id":"frank","can_write":true}`)
			if rr.Code != http.StatusInternalServerError {
				t.Errorf("ignored/undone mutation HTTP=%d want500", rr.Code)
			}
			var n int
			if err := f.h.db.QueryRow(`SELECT acl_version FROM page_folders WHERE slug='atomic-acl'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("rolled-back ACL version=%d", n)
			}
			if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM page_folder_acl WHERE subject_id='frank'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("rolled-back ACL count=%d", n)
			}
			if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='page.folder_acl_changed'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("rolled-back journal count=%d", n)
			}
		})
	}
}

func TestWorkspaceDeleteRejectsIgnoredCascade(t *testing.T) {
	h, user, ws := deleteRig(t)
	seedOtherWorkspace(t, h, "ws-other", "other", user)
	execOrFatal(t, h.db, `CREATE TRIGGER ignore_workspace_delete BEFORE UPDATE OF deleted_at ON workspaces BEGIN SELECT RAISE(IGNORE); END`)
	rr := httptest.NewRecorder()
	h.Delete(rr, deleteReq(t, user, ws, "OWNER", "test"))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("ignored cascade HTTP=%d want500", rr.Code)
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id=?`, ws).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("ignored cascade lost membership")
	}
}

func TestFolderACLRechecksAfterBody(t *testing.T) {
	for _, change := range []string{"demoted", "suspended", "restricted", "crew_removed"} {
		t.Run(change, func(t *testing.T) {
			f := newFoldersFixture(t)
			f.createFolder(t, "atomic-acl", "Atomic ACL", "crew/engine")
			actor, role := "erin", "ADMIN"
			if change == "crew_removed" {
				actor, role = "mia", "MANAGER"
			}
			req := pagesRequest(t, "PUT", "/api/v1/page-folders/atomic-acl/acl", f.wsID, actor, role, "")
			req.SetPathValue("slug", "atomic-acl")
			req.Body = &ownershipChangingBody{Reader: strings.NewReader(`{"subject_type":"user","subject_id":"frank","can_write":true}`), change: func() {
				switch change {
				case "demoted":
					execOrFatal(t, f.h.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id='erin'`)
				case "suspended":
					execOrFatal(t, f.h.db, `UPDATE users SET suspended_at='2026-10-20T00:00:00Z' WHERE id='erin'`)
				case "restricted":
					execOrFatal(t, f.h.db, `UPDATE workspace_members SET role='VIEWER',access_mode='restricted' WHERE user_id='erin'`)
				case "crew_removed":
					execOrFatal(t, f.h.db, `DELETE FROM crew_members WHERE user_id='mia'`)
				}
			}}
			rr := httptest.NewRecorder()
			f.h.PutFolderACL(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Errorf("lost authority HTTP=%d want403", rr.Code)
			}
			var n int
			if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM page_folder_acl WHERE subject_id='frank'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Error("lost authority still changed ACL")
			}
		})
	}
}

func TestFolderDeleteAtomicAudit(t *testing.T) {
	for _, fault := range []string{"none", "audit_abort", "ignore_delete"} {
		t.Run(fault, func(t *testing.T) {
			f := newFoldersFixture(t)
			f.createFolder(t, "atomic-acl", "Atomic ACL", "crew/engine")
			f.share(t, "atomic-acl", "user", "frank", true)
			if fault == "audit_abort" {
				execOrFatal(t, f.h.db, `CREATE TRIGGER refuse_folder_delete_audit BEFORE INSERT ON journal_entries WHEN NEW.entry_type='page.folder_changed' AND json_extract(NEW.payload,'$.op')='deleted' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			}
			if fault == "ignore_delete" {
				execOrFatal(t, f.h.db, `CREATE TRIGGER ignore_folder_delete BEFORE DELETE ON page_folders BEGIN SELECT RAISE(IGNORE); END`)
			}
			rr := f.call(t, "DELETE", "/api/v1/page-folders/atomic-acl", "erin", "ADMIN", "")
			want := http.StatusNoContent
			if fault != "none" {
				want = http.StatusInternalServerError
			}
			if rr.Code != want {
				t.Errorf("HTTP=%d want=%d", rr.Code, want)
			}
			var n int
			if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM page_folders WHERE slug='atomic-acl'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			want = 0
			if fault != "none" {
				want = 1
			}
			if n != want {
				t.Errorf("folder count=%d want=%d", n, want)
			}
			if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM page_folder_acl WHERE subject_id='frank'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != want {
				t.Errorf("ACL count=%d want=%d", n, want)
			}
			if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE entry_type='page.folder_changed' AND json_extract(payload,'$.op')='deleted' AND seq IS NOT NULL`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			want = 1
			if fault != "none" {
				want = 0
			}
			if n != want {
				t.Errorf("chained folder deletion count=%d want=%d", n, want)
			}
		})
	}
}
