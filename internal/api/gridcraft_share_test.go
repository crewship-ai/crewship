package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
)

func gridcraftImportBody(mutation string, base, generation int) string {
	return fmt.Sprintf(`{"mutation_id":%q,"mandate_id":"human-import","base_rev":%d,"generation":%d,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`, mutation, base, generation)
}

func gridcraftCount(t *testing.T, f *instanceFixture, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// An owner explicitly names a second human editor. The transition from private
// output is explicit and audited, grants from anyone but the owner stay inert,
// history stays private, OWNER reads remain audited and revocation is immediate.
func TestGridCraftSecondEditorPublishGrantAndRevoke(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	seedInstanceUser(t, f.db, "ann", "ann@ex.com", "ws-old", "MEMBER")
	seedInstanceUser(t, f.db, "bob", "bob@ex.com", "ws-old", "MEMBER")
	seedInstanceUser(t, f.db, "dave", "dave@ex.com", "ws-old", "MEMBER")
	ann := mintTokenFor(t, f.db, "ann", "workbookann000000000000000")
	bob := mintTokenFor(t, f.db, "bob", "workbookbob000000000000000")
	dave := mintTokenFor(t, f.db, "dave", "workbookdave00000000000000")
	wmSeedPage(t, f.db, "ws-old", "shared-book-id", "shared-book", "Shared book", "ann", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='shared-book-id'`)
	engine := &gridcraftBlockingEngine{}
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, engine)
	q := "?workspace_id=ws-old"
	base := "/api/v1/pages/shared-book/workbook"
	share := func(token, body string) *httptest.ResponseRecorder {
		return f.do(token, "POST", base+"/editors"+q, body)
	}

	wantCode(t, f.do(ann, "POST", base+"/operations"+q, gridcraftImportBody("ann-import", 0, 0)), 200, "owner import")
	wantCode(t, f.do(bob, "GET", base+q, ""), 404, "no grant read")
	wantCode(t, f.do(bob, "POST", base+"/operations"+q, gridcraftImportBody("bob-early", 1, 1)), 404, "no grant write")
	if strings.Contains(f.do(bob, "GET", "/api/v1/pages"+q, "").Body.String(), "shared-book") {
		t.Fatal("private workbook listed before publication")
	}
	// Ordinary grants still cannot widen the private audience.
	wantCode(t, f.do(ann, "PUT", "/api/v1/pages/shared-book/grants"+q, `{"subject_type":"user","subject":"bob@ex.com","level":"write"}`), 409, "ordinary grant on private")
	wantCode(t, share(ann, `{"editor":"bob@ex.com"}`), 409, "publication must be explicit")
	wantCode(t, share(bob, `{"editor":"bob@ex.com","publish":true}`), 404, "non-owner cannot share")
	wantCode(t, share(f.boss, `{"editor":"bob@ex.com","publish":true}`), 404, "audited OWNER cannot share")
	wantCode(t, share(ann, `{"editor":"ann@ex.com","publish":true}`), 400, "owner is not an editor")
	if n := gridcraftCount(t, f, `SELECT count(*) FROM pages WHERE id='shared-book-id' AND private_audience=1`); n != 1 {
		t.Fatal("refused share changed the audience")
	}

	shared := share(ann, `{"editor":"bob@ex.com","publish":true}`)
	wantCode(t, shared, 200, "explicit publication")
	if !strings.Contains(shared.Body.String(), `"published":true`) || !strings.Contains(shared.Body.String(), `"published_rev":1`) {
		t.Fatalf("share response %s", shared.Body.String())
	}
	if n := gridcraftCount(t, f, `SELECT count(*) FROM audit_logs WHERE action='artifact.private_output_published' AND entity_id='shared-book-id' AND user_id='ann' AND json_extract(metadata,'$.head_rev')=1`); n != 1 {
		t.Fatalf("publication audit rows = %d", n)
	}
	if n := gridcraftCount(t, f, `SELECT count(*) FROM journal_entries WHERE entry_type='page.grant_added' AND json_extract(payload,'$.page_id')='shared-book-id'`); n != 1 {
		t.Fatalf("grant journal rows = %d", n)
	}
	wantCode(t, share(ann, `{"editor":"bob@ex.com"}`), 200, "idempotent re-share of a published workbook")

	// The editor reads the current head and writes; history stays private.
	wantCode(t, f.do(bob, "GET", base+q, ""), 200, "editor read")
	wantCode(t, f.do(bob, "POST", base+"/operations"+q, gridcraftImportBody("bob-import", 1, 1)), 200, "editor write")
	wantCode(t, f.do(bob, "GET", base+"/operations/bob-import"+q, ""), 200, "editor own status")
	wantCode(t, f.do(bob, "GET", base+"/operations/ann-import"+q, ""), 404, "owner history is not shared")
	wantCode(t, f.do(ann, "POST", base+"/operations"+q, gridcraftImportBody("ann-second", 2, 1)), 200, "owner still writes")
	if !strings.Contains(f.do(bob, "GET", "/api/v1/pages"+q, "").Body.String(), "shared-book") {
		t.Fatal("explicitly shared workbook missing from editor listing")
	}

	// Others are unaffected: no grant means 404 and no listing entry.
	wantCode(t, f.do(dave, "GET", base+q, ""), 404, "other member read")
	wantCode(t, f.do(dave, "POST", base+"/operations"+q, gridcraftImportBody("dave", 3, 1)), 404, "other member write")
	if strings.Contains(f.do(dave, "GET", "/api/v1/pages"+q, "").Body.String(), "shared-book") {
		t.Fatal("published workbook listed for a member without a grant")
	}
	// An ADMIN may administer ordinary grants, but a grant the owner did not
	// issue never reaches workbook content.
	wantCode(t, f.do(f.wsAdmin, "GET", base+q, ""), 404, "admin role is not a workbook reader")
	wantCode(t, f.do(f.wsAdmin, "PUT", "/api/v1/pages/shared-book/grants"+q, `{"subject_type":"user","subject":"wsadmin@ex.com","level":"write"}`), 409, "admin self grant refused")
	wantCode(t, f.do(f.wsAdmin, "GET", base+q, ""), 404, "admin-issued grant is inert")
	wantCode(t, f.do(f.wsAdmin, "POST", base+"/operations"+q, gridcraftImportBody("admin", 3, 1)), 404, "admin-issued grant cannot write")

	// The workspace OWNER read remains audited and read-only.
	audits := gridcraftCount(t, f, `SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND entity_id='shared-book-id'`)
	wantCode(t, f.do(f.boss, "GET", base+q, ""), 200, "owner audit read")
	if n := gridcraftCount(t, f, `SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND entity_id='shared-book-id'`); n != audits+1 {
		t.Fatalf("OWNER read audit rows = %d, want %d", n, audits+1)
	}
	wantCode(t, f.do(f.boss, "POST", base+"/operations"+q, gridcraftImportBody("boss", 3, 1)), 404, "OWNER audit is not write")

	// A grant withdrawn while the editor's candidate runs commits nothing.
	engine.before = func() {
		mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='shared-book-id' AND subject_id='bob'`)
	}
	wantCode(t, f.do(bob, "POST", base+"/operations"+q, gridcraftImportBody("bob-raced", 3, 1)), 404, "revoked during candidate")
	engine.before = nil
	if n := gridcraftCount(t, f, `SELECT head_rev FROM gridcraft_heads WHERE page_id='shared-book-id'`); n != 3 {
		t.Fatalf("revoked candidate committed: head=%d", n)
	}
	wantCode(t, share(ann, `{"editor":"bob@ex.com"}`), 200, "re-grant")
	wantCode(t, f.do(bob, "GET", base+q, ""), 200, "re-granted read")
	// Revocation through the ordinary page grant surface stops the next request.
	wantCode(t, f.do(ann, "DELETE", "/api/v1/pages/shared-book/grants"+q+"&subject_type=user&subject=bob@ex.com", ""), 200, "owner revokes editor")
	wantCode(t, f.do(bob, "GET", base+q, ""), 404, "revoked read")
	wantCode(t, f.do(bob, "POST", base+"/operations"+q, gridcraftImportBody("bob-late", 3, 1)), 404, "revoked write")
	wantCode(t, f.do(bob, "GET", base+"/operations/bob-import"+q, ""), 404, "revoked status")
	if n := gridcraftCount(t, f, `SELECT count(*) FROM pages WHERE id='shared-book-id' AND private_audience=0`); n != 1 {
		t.Fatal("publication must stay explicit and one-way")
	}

	// An editor loses access when the owner no longer holds live authority.
	wantCode(t, share(ann, `{"editor":"bob@ex.com"}`), 200, "grant again")
	mustExec(t, f.db, `UPDATE users SET suspended_at='2026-01-01T00:00:00Z' WHERE id='ann'`)
	wantCode(t, f.do(bob, "GET", base+q, ""), 404, "owner suspended")
	mustExec(t, f.db, `UPDATE users SET suspended_at=NULL WHERE id='ann'`)
	wantCode(t, f.do(bob, "GET", base+q, ""), 200, "owner restored")
	mustExec(t, f.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id='bob'`)
	wantCode(t, f.do(bob, "GET", base+q, ""), 404, "restricted editor")
}

func TestGridCraftShareRequiresImportedWorkbookAndLiveEditor(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	seedInstanceUser(t, f.db, "ann", "ann@ex.com", "ws-old", "MEMBER")
	seedInstanceUser(t, f.db, "guest", "guest@ex.com", "ws-old", "MEMBER")
	mustExec(t, f.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id='guest'`)
	ann := mintTokenFor(t, f.db, "ann", "workbookann000000000000000")
	wmSeedPage(t, f.db, "ws-old", "empty-book-id", "empty-book", "Empty", "ann", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='empty-book-id'`)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	path := "/api/v1/pages/empty-book/workbook/editors?workspace_id=ws-old"
	wantCode(t, f.do(ann, "POST", path, `{"editor":"boss@ex.com","publish":true}`), 409, "no head")
	wantCode(t, f.do(ann, "POST", "/api/v1/pages/empty-book/workbook/operations?workspace_id=ws-old", gridcraftImportBody("i", 0, 0)), 200, "import")
	wantCode(t, f.do(ann, "POST", path, `{"editor":"guest@ex.com","publish":true}`), 400, "restricted editor")
	wantCode(t, f.do(ann, "POST", path, `{"editor":"carol@ex.com","publish":true}`), 400, "non-member editor")
	wantCode(t, f.do(ann, "POST", path, `{"editor":"boss@ex.com","publish":true,"extra":1}`), 400, "strict body")
	if n := gridcraftCount(t, f, `SELECT count(*) FROM pages WHERE id='empty-book-id' AND private_audience=1`); n != 1 {
		t.Fatal("refused share changed the audience")
	}
}

// A workbook has no backup or archive, so deleting, renaming or rolling back
// its Page stays with the owner before and after publication. Version history
// from the private period is not exposed to anyone else.
func TestGridCraftWorkbookPageLifecycleIsOwnerOnly(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	seedInstanceUser(t, f.db, "ann", "ann@ex.com", "ws-old", "MEMBER")
	seedInstanceUser(t, f.db, "bob", "bob@ex.com", "ws-old", "MEMBER")
	ann := mintTokenFor(t, f.db, "ann", "workbookann000000000000000")
	bob := mintTokenFor(t, f.db, "bob", "workbookbob000000000000000")
	wmSeedPage(t, f.db, "ws-old", "life-book-id", "life-book", "Life book", "ann", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='life-book-id'`)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	q := "?workspace_id=ws-old"
	page := "/api/v1/pages/life-book"
	wantCode(t, f.do(ann, "POST", page+"/workbook/operations"+q, gridcraftImportBody("i", 0, 0)), 200, "import")
	wantCode(t, f.do(ann, "PATCH", page+q, `{"name":"Renamed privately"}`), 200, "owner rename")
	refused := func(token, what string) {
		t.Helper()
		for _, c := range []struct{ method, path, body string }{
			{"DELETE", page, ""},
			{"PATCH", page, `{"name":"Taken over"}`},
			{"GET", page + "/versions", ""},
			{"POST", page + "/rollback", `{"to":1}`},
		} {
			if code := f.do(token, c.method, c.path+q, c.body).Code; code != 403 && code != 404 {
				t.Fatalf("%s %s %s = %d, want refusal", what, c.method, c.path, code)
			}
		}
	}
	refused(f.boss, "private OWNER")
	refused(f.wsAdmin, "private ADMIN")
	wantCode(t, f.do(ann, "POST", page+"/workbook/editors"+q, `{"editor":"bob@ex.com","publish":true}`), 200, "publish")
	refused(f.boss, "published OWNER")
	refused(f.wsAdmin, "published ADMIN")
	refused(bob, "editor")
	if n := gridcraftCount(t, f, `SELECT count(*) FROM pages WHERE id='life-book-id' AND name='Renamed privately'`); n != 1 {
		t.Fatal("non-owner changed the workbook Page")
	}
	wantCode(t, f.do(ann, "GET", page+"/versions"+q, ""), 200, "owner versions")
	wantCode(t, f.do(ann, "DELETE", page+q, ""), 204, "owner delete")
}

// Workbook editors are named only through the share flow. The generic grant
// endpoint cannot create or re-anchor a write grant, and `page grants` reports
// a write grant the current owner did not issue as inert.
func TestGridCraftGenericWriteGrantCannotChangeWorkbookEditors(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	seedInstanceUser(t, f.db, "ann", "ann@ex.com", "ws-old", "MEMBER")
	seedInstanceUser(t, f.db, "bob", "bob@ex.com", "ws-old", "MEMBER")
	seedInstanceUser(t, f.db, "dave", "dave@ex.com", "ws-old", "MEMBER")
	ann := mintTokenFor(t, f.db, "ann", "workbookann000000000000000")
	bob := mintTokenFor(t, f.db, "bob", "workbookbob000000000000000")
	dave := mintTokenFor(t, f.db, "dave", "workbookdave00000000000000")
	wmSeedPage(t, f.db, "ws-old", "grant-book-id", "grant-book", "Grant book", "ann", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='grant-book-id'`)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	q := "?workspace_id=ws-old"
	grants := "/api/v1/pages/grant-book/grants" + q
	wantCode(t, f.do(ann, "POST", "/api/v1/pages/grant-book/workbook/operations"+q, gridcraftImportBody("i", 0, 0)), 200, "import")
	wantCode(t, f.do(ann, "POST", "/api/v1/pages/grant-book/workbook/editors"+q, `{"editor":"bob@ex.com","publish":true}`), 200, "publish")

	for name, token := range map[string]string{"owner": ann, "admin": f.wsAdmin} {
		for _, subject := range []string{"bob@ex.com", "dave@ex.com"} {
			w := f.do(token, "PUT", grants, `{"subject_type":"user","subject":"`+subject+`","level":"write"}`)
			wantCode(t, w, 409, name+" generic write grant for "+subject)
			if !strings.Contains(w.Body.String(), "workbook share") {
				t.Fatalf("refusal does not name the share flow: %s", w.Body.String())
			}
		}
		wantCode(t, f.do(token, "PUT", grants, `{"subject_type":"workspace","level":"write"}`), 409, name+" workspace write grant")
	}
	if n := gridcraftCount(t, f, `SELECT count(*) FROM page_grants WHERE page_id='grant-book-id' AND subject_id='bob' AND granted_by_user_id='ann'`); n != 1 {
		t.Fatal("editor grant was re-anchored")
	}
	wantCode(t, f.do(bob, "GET", "/api/v1/pages/grant-book/workbook"+q, ""), 200, "editor unaffected")
	wantCode(t, f.do(dave, "GET", "/api/v1/pages/grant-book/workbook"+q, ""), 404, "no second path to editor")

	// A write grant the current owner did not issue (for example, from before
	// an ownership change) is listed as inert and reaches nothing.
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES('grant-book-id','user','dave','write','wsadmin','2026-01-01T00:00:00Z')`)
	listing := f.do(ann, "GET", grants, "")
	wantCode(t, listing, 200, "grant listing")
	var out pageGrantsWire
	if err := json.Unmarshal(listing.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, g := range out.Grants {
		switch g.SubjectID {
		case "bob":
			seen++
			if !g.Live {
				t.Fatalf("owner-issued editor listed inert: %+v", g)
			}
		case "dave":
			seen++
			if g.Live || !strings.Contains(g.InertReason, "workbook") {
				t.Fatalf("non-owner write grant listed live: %+v", g)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("grants listing %s", listing.Body.String())
	}
	wantCode(t, f.do(dave, "GET", "/api/v1/pages/grant-book/workbook"+q, ""), 404, "inert grant content")
	if strings.Contains(f.do(dave, "GET", "/api/v1/pages"+q, "").Body.String(), "grant-book") {
		t.Fatal("inert workbook write grant reached the Page listing")
	}
}
