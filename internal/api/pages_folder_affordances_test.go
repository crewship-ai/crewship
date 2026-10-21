package api

import (
	"net/http"
	"testing"
)

func TestPageFolderAffordancesFollowOwnershipAndACL(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "ops", "Ops", "crew/engine")
	for _, user := range []string{"alice", "frank", "dave", "mel"} {
		if _, err := f.h.db.Exec(`UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, f.wsID, user); err != nil {
			t.Fatal(err)
		}
	}
	f.share(t, "ops", "user", "frank", true)
	f.share(t, "ops", "user", "dave", false)
	f.share(t, "ops", "user", "max", false)
	if rr := f.call(t, "POST", "/api/v1/page-folders/ops/pages", "erin", "ADMIN", f.moveBody(t, "ops", "fleet-201")); rr.Code != http.StatusOK {
		t.Fatal(rr.Code, rr.Body.String())
	}
	crewPage := pagesCreateWith(t, f.h, f.wsID, f.owner, pagesReachBody("engine-status", "engine"))
	pagesSetOwner(t, f.h, crewPage, "", "crew-engine")
	if rr := f.call(t, "POST", "/api/v1/page-folders/ops/pages", "erin", "ADMIN", f.moveBody(t, "ops", "engine-status")); rr.Code != http.StatusOK {
		t.Fatal(rr.Code, rr.Body.String())
	}
	for _, tc := range []struct {
		user, role            string
		rename, share, delete bool
		move, remove          bool
	}{
		{"erin", "ADMIN", true, true, true, true, true},
		{"mia", "MANAGER", true, true, true, false, false},
		{"max", "MANAGER", false, false, false, false, false},
		{"mel", "VIEWER", false, false, false, false, false},
		{"alice", "VIEWER", false, false, false, true, true},
		{"frank", "VIEWER", true, false, false, false, true},
		{"dave", "VIEWER", false, false, false, false, false},
	} {
		t.Run(tc.user, func(t *testing.T) {
			rr := f.call(t, "GET", "/api/v1/page-folders/ops", tc.user, tc.role, "")
			if rr.Code != http.StatusOK {
				t.Fatal(rr.Code, rr.Body.String())
			}
			doc := decodeFoldersJSON(t, rr)
			for key, want := range map[string]bool{"can_rename": tc.rename, "can_share": tc.share, "can_delete": tc.delete, "can_accept_pages": tc.rename} {
				if doc[key] != want {
					t.Errorf("%s=%v want %v", key, doc[key], want)
				}
			}
			page := pagesListRows(t, f.h, f.wsID, tc.user, tc.role)["fleet-201"]
			if page != nil && (page["can_move"] != tc.move || page["can_remove_from_folder"] != tc.remove) {
				t.Errorf("page authority: %+v; move=%v remove=%v", page, tc.move, tc.remove)
			}
		})
	}
	// VIEWER membership in the owning crew is still page ownership. A
	// blanket role floor would remove these legitimate operations.
	page := pagesListRows(t, f.h, f.wsID, "mel", "VIEWER")["engine-status"]
	if page == nil || page["can_move"] != true || page["can_remove_from_folder"] != true {
		t.Fatalf("crew owner lost page arrangement: %+v", page)
	}
	if rr := f.call(t, "PATCH", "/api/v1/page-folders/ops", "frank", "VIEWER", `{"name":"Arranged by writer"}`); rr.Code != http.StatusOK {
		t.Fatalf("ACL writer cannot rename: %d %s", rr.Code, rr.Body.String())
	}
	if rr := f.call(t, "PUT", "/api/v1/page-folders/ops/acl", "frank", "VIEWER", `{"subject_type":"user","subject_id":"dave","can_write":true}`); rr.Code != http.StatusForbidden {
		t.Fatalf("ACL writer may share: %d %s", rr.Code, rr.Body.String())
	}
	if rr := f.call(t, "DELETE", "/api/v1/page-folders/ops", "frank", "VIEWER", ""); rr.Code != http.StatusForbidden {
		t.Fatalf("ACL writer may delete: %d %s", rr.Code, rr.Body.String())
	}
	f.share(t, "ops", "user", "frank", false)
	doc := decodeFoldersJSON(t, f.call(t, "GET", "/api/v1/page-folders/ops", "frank", "VIEWER", ""))
	page = pagesListRows(t, f.h, f.wsID, "frank", "VIEWER")["fleet-201"]
	if doc["can_rename"] != false || doc["can_accept_pages"] != false || page["can_remove_from_folder"] != false {
		t.Fatalf("revoked write still advertised: folder=%+v page=%+v", doc, page)
	}
	if rr := f.call(t, "PATCH", "/api/v1/page-folders/ops", "frank", "VIEWER", `{"name":"Stale authority"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("stale write accepted: %d %s", rr.Code, rr.Body.String())
	}
}
