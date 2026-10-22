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
