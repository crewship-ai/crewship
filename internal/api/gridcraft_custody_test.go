package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
)

// gridcraftCustodyFixture seeds ann's imported private workbook "custody-book".
func gridcraftCustodyFixture(t *testing.T, annInCrew bool) (*instanceFixture, string, string) {
	t.Helper()
	f := gridcraftInstanceFixture(t)
	seedInstanceUser(t, f.db, "ann", "ann@ex.com", "ws-old", "MEMBER")
	seedInstanceUser(t, f.db, "bob", "bob@ex.com", "ws-old", "MEMBER")
	ann := mintTokenFor(t, f.db, "ann", "workbookann000000000000000")
	bob := mintTokenFor(t, f.db, "bob", "workbookbob000000000000000")
	mustExec(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew-a','ws-old','Crew A','crew-a')`)
	if annInCrew {
		mustExec(t, f.db, `INSERT INTO crew_members(id,crew_id,user_id) VALUES('cm-a','crew-a','ann')`)
	}
	wmSeedPage(t, f.db, "ws-old", "custody-book-id", "custody-book", "Custody book", "ann", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='custody-book-id'`)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	wantCode(t, f.do(ann, "POST", "/api/v1/pages/custody-book/workbook/operations?workspace_id=ws-old", gridcraftImportBody("i", 0, 0)), 200, "import")
	return f, ann, bob
}

// A departing or erased workbook owner is handled like the owner of any other
// private output: the workbook is never handed to a crew (crews cannot own a
// workbook), departure refuses until a workspace OWNER picks a human successor
// through the custody flow, and afterwards the departure completes.
// Regression for review round 2, probes A (owner in a crew) and B (no crew).
func TestGridCraftDepartingOwnerWorkbookNeedsHumanSuccessor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inCrew bool
	}{{"owner in a crew", true}, {"owner without a crew", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f, ann, bob := gridcraftCustodyFixture(t, tc.inCrew)
			q := "?workspace_id=ws-old"
			// The conflict describes private outputs only to a workspace OWNER.
			asOwner := context.WithValue(context.Background(), ctxUser, &AuthUser{ID: "boss"})
			_, err := transferDepartingUserPages(asOwner, f.db, nil, "boss", "ws-old", "ann")
			var manual *ErrPagesNeedManualTransfer
			if !errors.As(err, &manual) {
				t.Fatalf("departure err = %v, want manual human transfer", err)
			}
			if len(manual.PageIDs) != 1 || manual.PageIDs[0] != "custody-book-id" || len(manual.OutputPages) != 1 || manual.OutputPages[0].Slug != "custody-book" || manual.OutputPages[0].Type != "workbook" {
				t.Fatalf("departure conflict pages = %+v, want the workbook", manual.OutputPages)
			}
			if n := gridcraftCount(t, f, `SELECT count(*) FROM pages WHERE id='custody-book-id' AND owner_user_id='ann' AND owner_crew_id IS NULL`); n != 1 {
				t.Fatal("refused departure changed workbook ownership")
			}
			listing := f.do(f.boss, "GET", "/api/v1/workspaces/ws-old/page-ownership"+q, "")
			wantCode(t, listing, 200, "custody listing")
			if !strings.Contains(listing.Body.String(), `"slug":"custody-book"`) || !strings.Contains(listing.Body.String(), `"type":"workbook"`) {
				t.Fatalf("custody listing misses the workbook: %s", listing.Body.String())
			}
			if strings.Contains(listing.Body.String(), `"sheets"`) {
				t.Fatal("custody listing exposed workbook contents")
			}
			moved := f.do(f.boss, "POST", "/api/v1/workspaces/ws-old/page-ownership/custody-book-id"+q, `{"expected_owner_user_id":"ann","successor_user_id":"bob"}`)
			wantCode(t, moved, 200, "custody transfer")
			if n := gridcraftCount(t, f, `SELECT count(*) FROM audit_logs WHERE workspace_id='ws-old' AND user_id='boss' AND action='artifact.workbook_custody_transferred' AND entity_id='custody-book-id'`); n != 1 {
				t.Fatalf("workbook custody transfer audit rows = %d, want 1", n)
			}
			wantCode(t, f.do(bob, "GET", "/api/v1/pages/custody-book/workbook"+q, ""), 200, "successor reads")
			wantCode(t, f.do(bob, "POST", "/api/v1/pages/custody-book/workbook/operations"+q, gridcraftImportBody("bob-1", 1, 1)), 200, "successor writes")
			wantCode(t, f.do(ann, "GET", "/api/v1/pages/custody-book/workbook"+q, ""), 404, "former owner")
			if _, err := transferDepartingUserPages(context.Background(), f.db, nil, "boss", "ws-old", "ann"); err != nil {
				t.Fatalf("departure after custody transfer: %v", err)
			}
		})
	}
}

// A workbook whose owner is no longer a member (left before workbook custody
// existed, or removed outside the custody flow) must not stay undeletable.
// Only a workspace OWNER with a current human credential may delete it, the
// rule is repeated in the DELETE, and the deletion is audited.
func TestGridCraftOrphanedWorkbookAuditedOwnerDelete(t *testing.T) {
	for _, tc := range []struct {
		name, orphan string
	}{
		{"legacy crew custody", `UPDATE pages SET owner_user_id=NULL, owner_crew_id='crew-a' WHERE id='custody-book-id'`},
		{"owner no longer a member", `DELETE FROM workspace_members WHERE workspace_id='ws-old' AND user_id='ann'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, bob := gridcraftCustodyFixture(t, true)
			q := "?workspace_id=ws-old"
			page := "/api/v1/pages/custody-book" + q
			// While the owner is a live member, nobody else deletes it.
			wantCode(t, f.do(f.boss, "DELETE", page, ""), 403, "OWNER while owner is a member")
			mustExec(t, f.db, tc.orphan)
			for name, token := range map[string]string{"ADMIN": f.wsAdmin, "member": bob} {
				if code := f.do(token, "DELETE", page, "").Code; code != 403 && code != 404 {
					t.Fatalf("%s DELETE orphaned workbook = %d, want refusal", name, code)
				}
			}
			agent := gridcraftRequest(f, f.boss, "DELETE", page)
			agent.Header.Set(actingAgentSlugHeader, "helper")
			if code := gridcraftServe(f, agent).Code; code != 403 {
				t.Fatalf("OWNER token with acting agent DELETE = %d, want 403", code)
			}
			if n := gridcraftCount(t, f, `SELECT count(*) FROM pages WHERE id='custody-book-id'`); n != 1 {
				t.Fatal("refused delete removed the workbook")
			}
			wantCode(t, f.do(f.boss, "DELETE", page, ""), 204, "OWNER deletes orphaned workbook")
			if n := gridcraftCount(t, f, `SELECT count(*) FROM gridcraft_heads WHERE page_id='custody-book-id'`); n != 0 {
				t.Fatal("orphaned workbook head survived")
			}
			if n := gridcraftCount(t, f, `SELECT count(*) FROM audit_logs WHERE workspace_id='ws-old' AND user_id='boss' AND action='artifact.orphaned_workbook_deleted' AND entity_type='page' AND entity_id='custody-book-id'`); n != 1 {
				t.Fatalf("orphaned workbook deletion audit rows = %d, want 1", n)
			}
		})
	}
}

// Deleting a workbook Page needs the owner's own human credential, exactly as
// changing it does: an agent acting through the owner's token cannot.
func TestGridCraftWorkbookDeleteRequiresHumanCredential(t *testing.T) {
	f, ann, _ := gridcraftCustodyFixture(t, false)
	page := "/api/v1/pages/custody-book?workspace_id=ws-old"
	agent := gridcraftRequest(f, ann, "DELETE", page)
	agent.Header.Set(actingAgentSlugHeader, "helper")
	if code := gridcraftServe(f, agent).Code; code != 403 {
		t.Fatalf("owner token with acting agent DELETE = %d, want 403", code)
	}
	if n := gridcraftCount(t, f, `SELECT count(*) FROM gridcraft_heads WHERE page_id='custody-book-id'`); n != 1 {
		t.Fatal("agent deleted the workbook")
	}
	wantCode(t, f.do(ann, "DELETE", page, ""), 204, "owner delete")
}

// Moving a workbook Page into or out of a folder is the owner's alone, like
// renaming or deleting it: a folder makes the workbook unreachable.
func TestGridCraftWorkbookFolderMoveIsOwnerOnly(t *testing.T) {
	f, ann, bob := gridcraftCustodyFixture(t, false)
	q := "?workspace_id=ws-old"
	wantCode(t, f.do(ann, "POST", "/api/v1/pages/custody-book/workbook/editors"+q, `{"editor":"bob@ex.com","publish":true}`), 200, "publish")
	mustExec(t, f.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('folder-a','ws-old','folder-a','Folder A','crew-a','2026-10-22T00:00:00Z','2026-10-22T00:00:00Z')`)
	for _, user := range []string{"ann", "bob"} {
		mustExec(t, f.db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_write,set_at) VALUES('folder-a','user',?,1,'2026-10-22T00:00:00Z')`, user)
	}
	versions := func() (int, int) {
		return gridcraftCount(t, f, `SELECT pages_version FROM pages WHERE id='custody-book-id'`),
			gridcraftCount(t, f, `SELECT acl_version FROM page_folders WHERE id='folder-a'`)
	}
	move := func(token string) int {
		pv, av := versions()
		return f.do(token, "POST", "/api/v1/page-folders/folder-a/pages"+q, `{"page":"custody-book","pages_version":`+strconv.Itoa(pv)+`,"acl_version":`+strconv.Itoa(av)+`}`).Code
	}
	batch := func(token string) int {
		pv, av := versions()
		return f.do(token, "POST", "/api/v1/page-folders/folder-a/pages:batch"+q, `{"pages":[{"page":"custody-book","pages_version":`+strconv.Itoa(pv)+`}],"acl_version":`+strconv.Itoa(av)+`}`).Code
	}
	remove := func(token string) int {
		pv, _ := versions()
		return f.do(token, "DELETE", "/api/v1/page-folders/folder-a/pages/custody-book"+q, `{"pages_version":`+strconv.Itoa(pv)+`}`).Code
	}
	for name, token := range map[string]string{"ADMIN": f.wsAdmin, "OWNER": f.boss, "editor": bob} {
		if code := move(token); code != 403 && code != 404 {
			t.Fatalf("%s moved the workbook into a folder: %d", name, code)
		}
		if code := batch(token); code != 403 && code != 404 {
			t.Fatalf("%s batch-moved the workbook into a folder: %d", name, code)
		}
	}
	if n := gridcraftCount(t, f, `SELECT count(*) FROM pages WHERE id='custody-book-id' AND folder_id IS NULL`); n != 1 {
		t.Fatal("non-owner filed the workbook")
	}
	if code := move(ann); code != 200 {
		t.Fatalf("owner move = %d, want 200", code)
	}
	for name, token := range map[string]string{"ADMIN": f.wsAdmin, "OWNER": f.boss, "editor": bob} {
		if code := remove(token); code != 403 && code != 404 {
			t.Fatalf("%s took the workbook out of its folder: %d", name, code)
		}
	}
	if code := remove(ann); code != 200 {
		t.Fatalf("owner remove = %d, want 200", code)
	}
	wantCode(t, f.do(bob, "GET", "/api/v1/pages/custody-book/workbook"+q, ""), 200, "editor after the owner's round trip")
}

func gridcraftRequest(f *instanceFixture, token, method, path string) *http.Request {
	f.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func gridcraftServe(f *instanceFixture, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	f.r.ServeHTTP(rr, req)
	return rr
}

// Workbook custody is not a way to take a workbook from a live member. The
// custody listing and transfer offer a workbook only to a workspace OWNER and
// only once its owner is no longer a live member: removed, suspended, or with
// a refused removal or erasure pending. Every workbook custody transfer is
// written to audit_logs in the same transaction.
// Regression for review round 3, probes R3-1 (ADMIN took a live member's
// published workbook, read it and deleted it) and R3-2 (OWNER handed a live
// member's workbook to a third member).
func TestGridCraftWorkbookCustodyOnlyForDepartedOwners(t *testing.T) {
	q := "?workspace_id=ws-old"
	list := "/api/v1/workspaces/ws-old/page-ownership" + q
	transfer := "/api/v1/workspaces/ws-old/page-ownership/custody-book-id" + q
	read := "/api/v1/pages/custody-book/workbook" + q
	owner := func(t *testing.T, f *instanceFixture) string {
		var id string
		if err := f.db.QueryRow(`SELECT COALESCE(owner_user_id,'') FROM pages WHERE id='custody-book-id'`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	transfers := func(t *testing.T, f *instanceFixture) int {
		return gridcraftCount(t, f, `SELECT count(*) FROM audit_logs WHERE workspace_id='ws-old' AND action='artifact.workbook_custody_transferred' AND entity_type='page' AND entity_id='custody-book-id'`)
	}
	refuseLiveOwner := func(t *testing.T, f *instanceFixture, ann, bob string) {
		t.Helper()
		for name, token := range map[string]string{"ADMIN": f.wsAdmin, "OWNER": f.boss} {
			listing := f.do(token, "GET", list, "")
			wantCode(t, listing, 200, name+" custody listing")
			if strings.Contains(listing.Body.String(), "custody-book") {
				t.Fatalf("%s custody listing offers a live member's workbook: %s", name, listing.Body.String())
			}
		}
		for name, body := range map[string]string{
			"ADMIN to self": `{"expected_owner_user_id":"ann","successor_user_id":"wsadmin"}`,
			"ADMIN to bob":  `{"expected_owner_user_id":"ann","successor_user_id":"bob"}`,
		} {
			if code := f.do(f.wsAdmin, "POST", transfer, body).Code; code != 403 && code != 404 {
				t.Fatalf("%s transfer of a live member's workbook = %d, want refusal", name, code)
			}
		}
		if code := f.do(f.boss, "POST", transfer, `{"expected_owner_user_id":"ann","successor_user_id":"bob"}`).Code; code != 403 && code != 404 {
			t.Fatalf("OWNER transfer of a live member's workbook = %d, want refusal", code)
		}
		if got := owner(t, f); got != "ann" {
			t.Fatalf("refused custody transfer moved the workbook to %q", got)
		}
		if n := transfers(t, f); n != 0 {
			t.Fatalf("refused custody transfers wrote %d audit rows", n)
		}
		wantCode(t, f.do(f.wsAdmin, "GET", read, ""), 404, "ADMIN reads a live member's workbook")
		if code := f.do(f.wsAdmin, "DELETE", "/api/v1/pages/custody-book"+q, "").Code; code != 403 && code != 404 {
			t.Fatalf("ADMIN DELETE of a live member's workbook = %d, want refusal", code)
		}
		if n := gridcraftCount(t, f, `SELECT count(*) FROM gridcraft_heads WHERE page_id='custody-book-id'`); n != 1 {
			t.Fatal("a refused custodian removed the workbook")
		}
		wantCode(t, f.do(ann, "GET", read, ""), 200, "owner keeps reading")
		wantCode(t, f.do(bob, "GET", read, ""), 200, "editor keeps reading")
	}
	openCustody := func(t *testing.T, f *instanceFixture, ann, bob string) {
		t.Helper()
		// ADMIN is never a workbook custodian, even when custody is open.
		listing := f.do(f.wsAdmin, "GET", list, "")
		wantCode(t, listing, 200, "ADMIN custody listing")
		if strings.Contains(listing.Body.String(), "custody-book") {
			t.Fatalf("ADMIN custody listing offers a workbook: %s", listing.Body.String())
		}
		if code := f.do(f.wsAdmin, "POST", transfer, `{"expected_owner_user_id":"ann","successor_user_id":"wsadmin"}`).Code; code != 403 && code != 404 {
			t.Fatalf("ADMIN transfer of a departing owner's workbook = %d, want refusal", code)
		}
		listing = f.do(f.boss, "GET", list, "")
		wantCode(t, listing, 200, "OWNER custody listing")
		if !strings.Contains(listing.Body.String(), `"type":"workbook"`) {
			t.Fatalf("OWNER custody listing misses the departing owner's workbook: %s", listing.Body.String())
		}
		wantCode(t, f.do(f.boss, "POST", transfer, `{"expected_owner_user_id":"ann","successor_user_id":"bob"}`), 200, "OWNER custody transfer")
		if got := owner(t, f); got != "bob" {
			t.Fatalf("workbook owner after custody transfer = %q, want bob", got)
		}
		if n := gridcraftCount(t, f, `SELECT count(*) FROM audit_logs WHERE workspace_id='ws-old' AND user_id='boss' AND action='artifact.workbook_custody_transferred' AND entity_type='page' AND entity_id='custody-book-id' AND json_extract(metadata,'$.from_user_id')='ann' AND json_extract(metadata,'$.to_user_id')='bob'`); n != 1 {
			t.Fatalf("workbook custody transfer audit rows = %d, want 1", n)
		}
		wantCode(t, f.do(bob, "GET", read, ""), 200, "successor reads")
	}

	t.Run("live owner keeps the workbook, refused removal opens custody", func(t *testing.T) {
		f, ann, bob := gridcraftCustodyFixture(t, false)
		wantCode(t, f.do(ann, "POST", "/api/v1/pages/custody-book/workbook/editors"+q, `{"editor":"bob@ex.com","publish":true}`), 200, "publish")
		refuseLiveOwner(t, f, ann, bob)
		var member string
		if err := f.db.QueryRow(`SELECT id FROM workspace_members WHERE user_id='ann' AND workspace_id='ws-old'`).Scan(&member); err != nil {
			t.Fatal(err)
		}
		removal := f.do(f.wsAdmin, "DELETE", "/api/v1/workspaces/ws-old/members/"+member+q, "")
		wantCode(t, removal, 409, "removal of a workbook owner")
		openCustody(t, f, ann, bob)
		wantCode(t, f.do(f.wsAdmin, "DELETE", "/api/v1/workspaces/ws-old/members/"+member+q, ""), 200, "removal after custody transfer")
	})
	t.Run("a stale removal request does not open custody", func(t *testing.T) {
		f, ann, bob := gridcraftCustodyFixture(t, false)
		wantCode(t, f.do(ann, "POST", "/api/v1/pages/custody-book/workbook/editors"+q, `{"editor":"bob@ex.com","publish":true}`), 200, "publish")
		mustExec(t, f.db, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES('old-request','ws-old','wsadmin','artifact.workbook_custody_requested','page','custody-book-id','{"owner_user_id":"ann"}','2020-01-01T00:00:00.000000000Z')`)
		refuseLiveOwner(t, f, ann, bob)
	})
	t.Run("suspended owner", func(t *testing.T) {
		f, ann, bob := gridcraftCustodyFixture(t, false)
		// Private: editor grants issued by an owner who is no longer live do not
		// count, so a published workbook's editors would not be successors.
		mustExec(t, f.db, `UPDATE users SET suspended_at='2026-10-22T00:00:00Z' WHERE id='ann'`)
		openCustody(t, f, ann, bob)
	})
	t.Run("owner no longer a member", func(t *testing.T) {
		f, ann, bob := gridcraftCustodyFixture(t, false)
		// Private: editor grants issued by an owner who is no longer live do not
		// count, so a published workbook's editors would not be successors.
		mustExec(t, f.db, `DELETE FROM workspace_members WHERE workspace_id='ws-old' AND user_id='ann'`)
		openCustody(t, f, ann, bob)
	})
}
