package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
)

// returnPrivateFixture is a shared workbook (PX-11): Ann imported it privately
// and published it to Bob as editor. Dave holds an ordinary read grant, a
// public link is live and Bob's agent holds a
// live workbook mandate. Every one of those accesses is what returning the
// workbook to Ann's personal space must remove. (A workbook is never filed in
// a folder; folder inheritance is covered by
// TestReturnPrivateEndsFolderInheritance.)
type returnPrivateFixture struct {
	*instanceFixture
	ann, bob, dave, erin string
}

const returnPrivatePath = "/api/v1/pages/shared-book/return-private?workspace_id=ws-old"

func newReturnPrivateFixture(t *testing.T) *returnPrivateFixture {
	t.Helper()
	f := gridcraftInstanceFixture(t)
	for _, u := range []string{"ann", "bob", "dave", "erin"} {
		seedInstanceUser(t, f.db, u, u+"@ex.com", "ws-old", "MEMBER")
	}
	rf := &returnPrivateFixture{instanceFixture: f,
		ann:  mintTokenFor(t, f.db, "ann", "returnann00000000000000000"),
		bob:  mintTokenFor(t, f.db, "bob", "returnbob00000000000000000"),
		dave: mintTokenFor(t, f.db, "dave", "returndave0000000000000000"),
		erin: mintTokenFor(t, f.db, "erin", "returnerin0000000000000000"),
	}
	wmSeedPage(t, f.db, "ws-old", "shared-book-id", "shared-book", "Shared book", "ann", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='shared-book-id'`)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	q := "?workspace_id=ws-old"
	wantCode(t, f.do(rf.ann, "POST", "/api/v1/pages/shared-book/workbook/operations"+q, gridcraftImportBody("ann-import", 0, 0)), 200, "owner import")
	wantCode(t, f.do(rf.ann, "POST", "/api/v1/pages/shared-book/workbook/editors"+q, `{"editor":"bob@ex.com","publish":true}`), 200, "publish to Bob")
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES('shared-book-id','user','dave','read','ann','2026-11-01T00:00:00Z')`)
	mustExec(t, f.db, `INSERT INTO page_public_tokens(id,page_id,token_hash,expires_at,created_by_user_id) VALUES('shared-book-link','shared-book-id',?,'2999-01-01T00:00:00Z','ann')`, hashPagePublicToken("shared-book-public-token"))
	mustExec(t, f.db, `INSERT INTO gridcraft_intents(workspace_id,page_id,mandate_id,human_user_id,run_id,base_rev,generation,page_version,policy_version,targets_json,payload_hash) VALUES('ws-old','shared-book-id','bob-mandate','bob','bob-run',1,1,0,0,'[]','hash')`)
	return rf
}

func (f *returnPrivateFixture) pagesVersion(t *testing.T) int64 {
	t.Helper()
	return int64(gridcraftCount(t, f.instanceFixture, `SELECT pages_version FROM pages WHERE id='shared-book-id'`))
}

func (f *returnPrivateFixture) preview(t *testing.T, token string) (*httptest.ResponseRecorder, pageReturnPrivateWire) {
	t.Helper()
	w := f.do(token, "POST", returnPrivatePath, fmt.Sprintf(`{"pages_version":%d,"dry_run":true}`, f.pagesVersion(t)))
	var out pageReturnPrivateWire
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode preview: %v %s", err, w.Body)
		}
	}
	return w, out
}

func (f *returnPrivateFixture) confirm(token string, version int64, review string) *httptest.ResponseRecorder {
	return f.do(token, "POST", returnPrivatePath, fmt.Sprintf(`{"pages_version":%d,"confirm":true,"review_token":%q}`, version, review))
}

// The complete PX-11 path: the owner previews exactly what is removed,
// confirms, and the same Page ID is private again for every reader path.
func TestReturnPrivateOwnerReturnsSharedWorkbook(t *testing.T) {
	f := newReturnPrivateFixture(t)
	q := "?workspace_id=ws-old"
	book := "/api/v1/pages/shared-book/workbook" + q
	wantCode(t, f.do(f.bob, "GET", book, ""), 200, "editor reads before return")
	wantCode(t, f.do(f.dave, "GET", "/api/v1/pages/shared-book"+q, ""), 200, "grant holder reads before return")
	epochBefore := gridcraftCount(t, f.instanceFixture, `SELECT authority_revision FROM workspaces WHERE id='ws-old'`)
	version := f.pagesVersion(t)

	w, preview := f.preview(t, f.ann)
	wantCode(t, w, 200, "owner preview")
	if !preview.DryRun || preview.ReviewToken == "" || preview.PageID != "shared-book-id" || preview.Kind != "workbook" || preview.FromFolder != nil {
		t.Fatalf("preview %s", w.Body)
	}
	lost := map[string]bool{}
	for _, c := range preview.Changes {
		if c.Before.Read && !c.After.Read {
			lost[c.SubjectID] = true
		}
	}
	for _, who := range []string{"bob", "dave"} {
		if !lost[who] {
			t.Fatalf("preview does not list %s losing read: %s", who, w.Body)
		}
	}
	if lost["ann"] || lost["boss"] || lost["wsadmin"] {
		t.Fatalf("preview removes the owner or the audited administrator read: %s", w.Body)
	}
	if len(preview.RemovedGrants) != 2 || len(preview.RevokedPublicLinks) != 1 || preview.RevokedPublicLinks[0].ID != "shared-book-link" || preview.RevokedMandates != 1 || preview.Notice == "" {
		t.Fatalf("preview removals %s", w.Body)
	}
	// The dry run changes nothing.
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM pages WHERE id='shared-book-id' AND private_audience=0 AND pages_version=?`, version); n != 1 {
		t.Fatal("dry run changed the page")
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT (SELECT count(*) FROM page_grants WHERE page_id='shared-book-id')+(SELECT count(*) FROM page_public_tokens WHERE page_id='shared-book-id' AND revoked_at IS NULL)+(SELECT count(*) FROM gridcraft_intents WHERE page_id='shared-book-id' AND revoked_at IS NULL)`); n != 4 {
		t.Fatalf("dry run removed accesses: %d live left", n)
	}

	wantCode(t, f.confirm(f.ann, version, strings.Repeat("0", 64)), 409, "forged review token")
	confirmed := f.confirm(f.ann, version, preview.ReviewToken)
	wantCode(t, confirmed, 200, "owner confirms")
	var done pageReturnPrivateWire
	if err := json.Unmarshal(confirmed.Body.Bytes(), &done); err != nil || !done.Changed || done.DryRun || done.PagesVersion != version+1 || done.PageID != "shared-book-id" {
		t.Fatalf("confirmation %s %v", confirmed.Body, err)
	}

	// Same ID, private, personal space, same owner; nothing left to re-share.
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM pages WHERE id='shared-book-id' AND slug='shared-book' AND private_audience=1 AND folder_id IS NULL AND owner_user_id='ann' AND owner_crew_id IS NULL`); n != 1 {
		t.Fatal("page was not returned to the owner's personal space with the same ID")
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM page_grants WHERE page_id='shared-book-id'`); n != 0 {
		t.Fatalf("%d grants survived", n)
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM page_public_tokens WHERE page_id='shared-book-id' AND revoked_at IS NULL`); n != 0 {
		t.Fatal("public link survived")
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM gridcraft_intents WHERE page_id='shared-book-id' AND revoked_at IS NULL`); n != 0 {
		t.Fatal("agent mandate survived")
	}
	if epochAfter := gridcraftCount(t, f.instanceFixture, `SELECT authority_revision FROM workspaces WHERE id='ws-old'`); epochAfter <= epochBefore {
		t.Fatalf("authority epoch did not advance: %d -> %d", epochBefore, epochAfter)
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM audit_logs WHERE action='artifact.returned_to_private' AND entity_id='shared-book-id' AND user_id='ann' AND json_extract(metadata,'$.audience_after')='private' AND json_extract(metadata,'$.removed_grants')=2`); n != 1 {
		t.Fatalf("return audit rows = %d", n)
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM audit_logs WHERE action='artifact.returned_to_private' AND metadata LIKE '%Shared book%'`); n != 0 {
		t.Fatal("audit carries page content or name")
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM journal_entries WHERE entry_type='artifact.returned_to_private' AND json_extract(payload,'$.page_id')='shared-book-id'`); n != 1 {
		t.Fatalf("return journal rows = %d", n)
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM journal_entries WHERE entry_type='page.grant_removed' AND json_extract(payload,'$.page_id')='shared-book-id'`); n != 2 {
		t.Fatalf("grant removal journal rows = %d", n)
	}

	// The server refuses every former reader at once.
	wantCode(t, f.do(f.bob, "GET", book, ""), 404, "editor read after return")
	wantCode(t, f.do(f.bob, "POST", "/api/v1/pages/shared-book/workbook/operations"+q, gridcraftImportBody("bob-late", 1, 1)), 404, "editor write after return")
	wantCode(t, f.do(f.dave, "GET", "/api/v1/pages/shared-book"+q, ""), 404, "grant holder read after return")
	wantCode(t, f.do("", "GET", "/api/v1/public/pages/shared-book-public-token", ""), 404, "public link after return")
	wantCode(t, f.do(f.ann, "GET", book, ""), 200, "owner still reads")
	// Workspace administrators keep the audited read (decision of 30 October, #4825).
	audits := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND entity_id='shared-book-id' AND user_id='wsadmin'`)
	wantCode(t, f.do(f.wsAdmin, "GET", book, ""), 200, "admin audited read")
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND entity_id='shared-book-id' AND user_id='wsadmin'`); n != audits+1 {
		t.Fatalf("admin read audit rows = %d, want %d", n, audits+1)
	}

	// A second return is refused: the page is already private.
	wantCode(t, f.do(f.ann, "POST", returnPrivatePath, fmt.Sprintf(`{"pages_version":%d,"dry_run":true}`, f.pagesVersion(t))), 409, "already private")

	// Re-sharing is a new publication: the old grants do not come back.
	wantCode(t, f.do(f.ann, "POST", "/api/v1/pages/shared-book/workbook/editors"+q, `{"editor":"bob@ex.com","publish":true}`), 200, "re-share")
	wantCode(t, f.do(f.bob, "GET", book, ""), 200, "re-named editor reads")
	wantCode(t, f.do(f.dave, "GET", "/api/v1/pages/shared-book"+q, ""), 404, "old grant is not restored")
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM page_grants WHERE page_id='shared-book-id'`); n != 1 {
		t.Fatalf("re-share grants = %d, want only the new editor", n)
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM page_public_tokens WHERE page_id='shared-book-id' AND revoked_at IS NULL`); n != 0 {
		t.Fatal("re-share revived the public link")
	}
}

// Only the page's human owner returns it. Workspace OWNER and ADMIN read
// private output, audited, but never administer its audience.
func TestReturnPrivateRefusals(t *testing.T) {
	f := newReturnPrivateFixture(t)
	version := f.pagesVersion(t)
	dry := fmt.Sprintf(`{"pages_version":%d,"dry_run":true}`, version)
	for _, c := range []struct {
		name, token, body string
		code              int
	}{
		{"editor", f.bob, dry, 403},
		{"grant holder", f.dave, dry, 403},
		{"workspace OWNER", f.boss, dry, 403},
		{"workspace ADMIN", f.wsAdmin, dry, 403},
		{"missing pages_version", f.ann, `{"dry_run":true}`, 400},
		{"stale pages_version", f.ann, fmt.Sprintf(`{"pages_version":%d,"dry_run":true}`, version+1), 409},
		{"dry run and confirm", f.ann, fmt.Sprintf(`{"pages_version":%d,"dry_run":true,"confirm":true}`, version), 400},
		{"confirm without token", f.ann, fmt.Sprintf(`{"pages_version":%d,"confirm":true}`, version), 400},
		{"unknown field", f.ann, fmt.Sprintf(`{"pages_version":%d,"dry_run":true,"audience":"x"}`, version), 400},
	} {
		t.Run(c.name, func(t *testing.T) {
			wantCode(t, f.do(c.token, "POST", returnPrivatePath, c.body), c.code, c.name)
		})
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM pages WHERE id='shared-book-id' AND private_audience=0 AND pages_version=?`, version); n != 1 {
		t.Fatal("a refused return changed the page")
	}

	// A review is bound to the accesses it showed: a grant issued after the
	// preview makes the confirmation stale, and nothing changes.
	_, preview := f.preview(t, f.ann)
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES('shared-book-id','user','erin','comment','ann','2026-11-02T00:00:00Z')`)
	wantCode(t, f.confirm(f.ann, version, preview.ReviewToken), 409, "stale review")
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM pages WHERE id='shared-book-id' AND private_audience=0 AND pages_version=?`, version); n != 1 {
		t.Fatal("stale review changed the page")
	}
	// Another user's review token is never accepted.
	mustExec(t, f.db, `DELETE FROM page_grants WHERE subject_id='erin'`)
	_, preview = f.preview(t, f.ann)
	wantCode(t, f.confirm(f.bob, version, preview.ReviewToken), 403, "token replayed by a non-owner")
}

// Supported types are the ones with a private audience today: Docs
// documents, workbooks, native Word and Sheet, and HTML artifact projects.
// Crew-owned Pages name no person and are refused, as are types without
// private support.
func TestReturnPrivateSupportedKinds(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	seedInstanceUser(t, f.db, "ann", "ann@ex.com", "ws-old", "MEMBER")
	ann := mintTokenFor(t, f.db, "ann", "returnann00000000000000000")
	mustExec(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew-kinds','ws-old','Kinds','kinds')`)
	mustExec(t, f.db, `INSERT INTO crew_members(id,crew_id,user_id,role) VALUES('cm-kinds','crew-kinds','ann','MANAGER')`)
	const hex40, hex64 = "0123456789abcdef0123456789abcdef01234567", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, c := range []struct {
		name, owner, crew string
		seed              []string
		kind              string
		code              int
	}{
		{"document", "ann", "", []string{`INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('ws-old','kind-document',3,x'',x'')`}, "document", 200},
		{"native word", "ann", "", []string{`INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('kind-native-word','ws-old','wordcraft','` + hex40 + `','` + hex64 + `','` + hex64 + `')`}, "word", 200},
		{"native sheet", "ann", "", []string{`INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('kind-native-sheet','ws-old','gridcraft','` + hex40 + `','` + hex64 + `','` + hex64 + `')`}, "sheet", 200},
		{"artifact project", "ann", "", []string{`INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version,shared_version) VALUES('ws-old','kind-artifact-project','artifact_project',1,1)`}, "artifact_project", 200},
		{"design", "ann", "", []string{`INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version,shared_version) VALUES('ws-old','kind-design','design',1,1)`}, "", 409},
		{"dashboard", "ann", "", nil, "", 409},
		{"crew-owned artifact project", "", "crew-kinds", []string{`INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version,shared_version) VALUES('ws-old','kind-crew-owned-artifact-project','artifact_project',1,1)`}, "", 409},
	} {
		t.Run(c.name, func(t *testing.T) {
			id := "kind-" + strings.ReplaceAll(c.name, " ", "-")
			wmSeedPage(t, f.db, "ws-old", id, id, c.name, c.owner, c.crew)
			for _, s := range c.seed {
				mustExec(t, f.db, s)
			}
			w := f.do(ann, "POST", "/api/v1/pages/"+id+"/return-private?workspace_id=ws-old", `{"pages_version":0,"dry_run":true}`)
			wantCode(t, w, c.code, c.name)
			if c.code == 200 && !strings.Contains(w.Body.String(), `"kind":"`+c.kind+`"`) {
				t.Fatalf("%s kind: %s", c.name, w.Body)
			}
		})
	}
}

// Folder inheritance ends with the return: the Page leaves its folder, a
// folder reader loses it at once, and the preview names the folder.
func TestReturnPrivateEndsFolderInheritance(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	seedInstanceUser(t, f.db, "ann", "ann@ex.com", "ws-old", "MEMBER")
	seedInstanceUser(t, f.db, "erin", "erin@ex.com", "ws-old", "MEMBER")
	ann := mintTokenFor(t, f.db, "ann", "returnann00000000000000000")
	erin := mintTokenFor(t, f.db, "erin", "returnerin0000000000000000")
	wmSeedPage(t, f.db, "ws-old", "filed-app-id", "filed-app", "Filed app", "ann", "")
	mustExec(t, f.db, `INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version,shared_version) VALUES('ws-old','filed-app-id','artifact_project',1,1)`)
	mustExec(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew-share','ws-old','Share','share')`)
	mustExec(t, f.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('share-folder','ws-old','share-folder','Share folder','crew-share','2026-11-01T00:00:00Z','2026-11-01T00:00:00Z')`)
	mustExec(t, f.db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_at) VALUES('share-folder','user','erin',1,0,'2026-11-01T00:00:00Z')`)
	mustExec(t, f.db, `UPDATE pages SET folder_id='share-folder' WHERE id='filed-app-id'`)
	q := "?workspace_id=ws-old"
	path := "/api/v1/pages/filed-app/return-private" + q
	wantCode(t, f.do(erin, "GET", "/api/v1/pages/filed-app"+q, ""), 200, "folder reader before return")
	w := f.do(ann, "POST", path, `{"pages_version":0,"dry_run":true}`)
	wantCode(t, w, 200, "preview")
	var preview pageReturnPrivateWire
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || preview.FromFolder == nil || preview.FromFolder.Slug != "share-folder" || preview.Kind != "artifact_project" {
		t.Fatalf("preview %s %v", w.Body, err)
	}
	lost := false
	for _, c := range preview.Changes {
		lost = lost || (c.SubjectID == "erin" && c.Before.Read && !c.After.Read)
	}
	if !lost {
		t.Fatalf("preview does not list the folder reader: %s", w.Body)
	}
	wantCode(t, f.do(ann, "POST", path, fmt.Sprintf(`{"pages_version":0,"confirm":true,"review_token":%q}`, preview.ReviewToken)), 200, "confirm")
	if n := gridcraftCount(t, f, `SELECT count(*) FROM pages WHERE id='filed-app-id' AND private_audience=1 AND folder_id IS NULL`); n != 1 {
		t.Fatal("page still filed")
	}
	wantCode(t, f.do(erin, "GET", "/api/v1/pages/filed-app"+q, ""), 404, "folder reader after return")
	wantCode(t, f.do(ann, "GET", "/api/v1/pages/filed-app"+q, ""), 200, "owner after return")
}
