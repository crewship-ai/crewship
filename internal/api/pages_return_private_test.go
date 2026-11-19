package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
)

// returnPrivateFixture is a shared workbook (PX-11): Ann imported it privately
// and published it to Bob as editor. Dave holds an ordinary read grant, a
// public link is live and Bob's agent holds a live workbook mandate. Every one
// of those accesses is what returning the workbook to Ann's personal space
// must remove. (A workbook is never filed in a folder; folder inheritance is
// covered with an HTML artifact project.)
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

// returnPrivateJournalLeaks lists the journal entries written by a return
// that a member's /api/v1/journal listing shows with a name, slug or email.
func returnPrivateJournalLeaks(t *testing.T, body []byte, secrets ...string) []string {
	t.Helper()
	var listing struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		t.Fatalf("journal listing: %v %s", err, body)
	}
	var leaks []string
	seen := 0
	for _, raw := range listing.Entries {
		var e struct {
			Type string `json:"entry_type"`
		}
		_ = json.Unmarshal(raw, &e)
		switch e.Type {
		case "page.grant_removed", "page.link_revoked", "artifact.returned_to_private", "page.folder_membership_changed":
		default:
			continue
		}
		if !strings.Contains(string(raw), returnPrivateReason) && e.Type != "artifact.returned_to_private" && e.Type != "page.folder_membership_changed" {
			continue // an ordinary revoke from before the return
		}
		seen++
		for _, s := range secrets {
			if strings.Contains(string(raw), s) {
				leaks = append(leaks, fmt.Sprintf("%s carries %q: %s", e.Type, s, raw))
			}
		}
	}
	if seen == 0 {
		t.Fatalf("journal listing shows no entry of the return: %s", body)
	}
	return leaks
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
	if !preview.DryRun || preview.ReviewToken == "" || preview.PageID != "shared-book-id" || preview.Kind != "workbook" || preview.FromFolder != nil || !strings.Contains(preview.Reshare, "workbook/editors") {
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
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM audit_logs WHERE action='artifact.returned_to_private' AND (metadata LIKE '%Shared book%' OR metadata LIKE '%shared-book"%')`); n != 0 {
		t.Fatal("audit carries page content or name")
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM journal_entries WHERE entry_type='artifact.returned_to_private' AND json_extract(payload,'$.page_id')='shared-book-id'`); n != 1 {
		t.Fatalf("return journal rows = %d", n)
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM journal_entries WHERE entry_type='page.grant_removed' AND json_extract(payload,'$.page_id')='shared-book-id' AND json_extract(payload,'$.reason')='returned_to_private'`); n != 2 {
		t.Fatalf("grant removal journal rows = %d", n)
	}
	// A member's journal never names the now-private Page or its people.
	listing := f.do(f.dave, "GET", "/api/v1/journal?workspace_id=ws-old&limit=500", "")
	wantCode(t, listing, 200, "member journal")
	if leaks := returnPrivateJournalLeaks(t, listing.Body.Bytes(), `"shared-book"`, "Shared book", "ann@ex.com", "dave@ex.com", "bob@ex.com"); len(leaks) > 0 {
		t.Fatalf("journal leaks the private page: %v", leaks)
	}

	// The server refuses every former reader at once.
	wantCode(t, f.do(f.bob, "GET", book, ""), 404, "editor read after return")
	wantCode(t, f.do(f.bob, "POST", "/api/v1/pages/shared-book/workbook/operations"+q, gridcraftImportBody("bob-late", 1, 1)), 404, "editor write after return")
	wantCode(t, f.do(f.dave, "GET", "/api/v1/pages/shared-book"+q, ""), 404, "grant holder read after return")
	wantCode(t, f.do(f.dave, "GET", "/api/v1/pages/shared-book/export"+q, ""), 404, "grant holder export after return")
	wantCode(t, f.do("", "GET", "/api/v1/public/pages/shared-book-public-token", ""), 404, "public link after return")
	wantCode(t, f.do(f.ann, "GET", book, ""), 200, "owner still reads")
	// Workspace administrators keep the audited read (decision of 30 October, #4825).
	audits := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND entity_id='shared-book-id' AND user_id='wsadmin'`)
	wantCode(t, f.do(f.wsAdmin, "GET", book, ""), 200, "admin audited read")
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND entity_id='shared-book-id' AND user_id='wsadmin'`); n != audits+1 {
		t.Fatalf("admin read audit rows = %d, want %d", n, audits+1)
	}

	// A second return is refused: the page is already private.
	again := f.do(f.ann, "POST", returnPrivatePath, fmt.Sprintf(`{"pages_version":%d,"dry_run":true}`, f.pagesVersion(t)))
	wantCode(t, again, 409, "already private")
	if !strings.Contains(again.Body.String(), `"reason":"already_private"`) {
		t.Fatalf("refusal reason %s", again.Body)
	}

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

// Every kind of grant subject, a retired grant, all four mandate tables and
// other actors' edit leases end with the return; each deleted grant is
// remembered as a retired issuance so a restored backup cannot revive it.
func TestReturnPrivateEndsEveryGrantMandateAndLease(t *testing.T) {
	f := newReturnPrivateFixture(t)
	mustExec(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew-share','ws-old','Share','share')`)
	mustExec(t, f.db, `INSERT INTO agents(id,crew_id,workspace_id,name,slug) VALUES('agent-share','crew-share','ws-old','Helper','helper')`)
	for _, g := range []string{
		`('shared-book-id','crew','crew-share','read','ann','2026-11-01T00:00:01Z',NULL)`,
		`('shared-book-id','workspace','','read','ann','2026-11-01T00:00:02Z',NULL)`,
		`('shared-book-id','agent','agent-share','read','ann','2026-11-01T00:00:03Z',NULL)`,
		`('shared-book-id','user','erin','read','ann','2026-11-01T00:00:04Z','2026-11-02T00:00:00Z')`,
	} {
		mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at,retired_at) VALUES`+g)
	}
	// Mandates from every table; their parents (messages, chats, Mooring
	// documents) are outside this operation, so seed without FK checks.
	mustExec(t, f.db, `PRAGMA foreign_keys=OFF`)
	mustExec(t, f.db, `INSERT INTO mooring_intents(page_id,mandate_id,run_id,human_user_id,page_version,policy_version,generation,base_rev,command,fingerprint) VALUES('shared-book-id','m-mandate','m-run','bob',0,0,1,1,'{}','fp')`)
	mustExec(t, f.db, `INSERT INTO artifact_project_message_intents(message_id,workspace_id,human_user_id,chat_id,submission_id,page_id,base_version,created_at) VALUES('apm-message','ws-old','bob','chat-1','sub-1','shared-book-id',1,'2026-11-01T00:00:00Z')`)
	mustExec(t, f.db, `INSERT INTO document_iteration_intents(message_id,workspace_id,human_user_id,chat_id,submission_id,page_id,schema_version,document_generation,original_rev,draft_rev,pages_version,policy_version,member_id,member_revision,chat_generation,chat_revision) VALUES('doc-message','ws-old','ann','chat-1','sub-2','shared-book-id',3,0,0,0,0,0,'m',1,'g',1)`)
	mustExec(t, f.db, `INSERT INTO mooring_edit_leases(page_id,workspace_id,actor_id,session_id,generation,boot_id,expires_ns) VALUES('shared-book-id','ws-old','user:bob','bob-session',7,'boot',9999999999999)`)
	mustExec(t, f.db, `INSERT INTO mooring_edit_lease_requests(page_id,actor_id,session_id,generation) VALUES('shared-book-id','user:dave','dave-session',7),('shared-book-id','user:ann','ann-session',7)`)
	mustExec(t, f.db, `PRAGMA foreign_keys=ON`)

	w, preview := f.preview(t, f.ann)
	wantCode(t, w, 200, "preview")
	retired := 0
	for _, g := range preview.RemovedGrants {
		if g.Retired {
			retired++
		}
	}
	if len(preview.RemovedGrants) != 6 || retired != 1 || preview.RevokedMandates != 4 || preview.ReleasedEditLeases != 2 {
		t.Fatalf("preview %s", w.Body)
	}
	wantCode(t, f.confirm(f.ann, f.pagesVersion(t), preview.ReviewToken), 200, "confirm")
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM page_grants WHERE page_id='shared-book-id'`); n != 0 {
		t.Fatalf("%d grants survived", n)
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM page_grant_retired_issuances WHERE page_id='shared-book-id'`); n != 6 {
		t.Fatalf("retired issuances = %d, want every deleted grant", n)
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM page_grant_retired_issuances WHERE page_id='shared-book-id' AND reason='returned_to_private'`); n != 5 {
		t.Fatalf("returned_to_private tombstones = %d, want the five live grants", n)
	}
	for _, table := range returnPrivateMandateTables {
		if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM `+table+` WHERE page_id='shared-book-id' AND revoked_at IS NULL`); n != 0 {
			t.Fatalf("%s: %d live mandates", table, n)
		}
		if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM `+table+` WHERE page_id='shared-book-id' AND revoked_at IS NOT NULL`); n != 1 {
			t.Fatalf("%s: %d revoked mandates, want 1", table, n)
		}
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM mooring_edit_leases WHERE page_id='shared-book-id' AND actor_id='' AND session_id='' AND expires_ns=0 AND generation=7`); n != 1 {
		t.Fatal("another actor's edit lease was not released with its fence kept")
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM mooring_edit_lease_requests WHERE page_id='shared-book-id'`); n != 1 {
		t.Fatalf("lease requests left = %d, want only the owner's", n)
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM mooring_edit_lease_requests WHERE page_id='shared-book-id' AND actor_id='user:ann'`); n != 1 {
		t.Fatal("the owner's own lease request was removed")
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

	// A review is bound to the accesses it showed: a grant the owner issues
	// through the ordinary API after the preview makes the confirmation stale.
	_, preview := f.preview(t, f.ann)
	wantCode(t, f.do(f.ann, "PUT", "/api/v1/pages/shared-book/grants?workspace_id=ws-old", `{"subject_type":"user","subject":"erin@ex.com","level":"read"}`), 200, "grant between preview and confirm")
	stale := f.confirm(f.ann, version, preview.ReviewToken)
	wantCode(t, stale, 409, "stale review")
	if !strings.Contains(stale.Body.String(), `"reason":"stale_review"`) {
		t.Fatalf("stale reason %s", stale.Body)
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM pages WHERE id='shared-book-id' AND private_audience=0 AND pages_version=?`, version); n != 1 {
		t.Fatal("stale review changed the page")
	}
	// Another user's replay of the owner's token is never accepted.
	_, preview = f.preview(t, f.ann)
	wantCode(t, f.confirm(f.bob, version, preview.ReviewToken), 403, "token replayed by a non-owner")

	// An owner without live standing cannot return the page either.
	mustExec(t, f.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id='ann'`)
	if w := f.confirm(f.ann, version, preview.ReviewToken); w.Code == 200 {
		t.Fatal("restricted owner returned the page")
	}
	mustExec(t, f.db, `UPDATE workspace_members SET access_mode='trusted' WHERE user_id='ann'`)
	mustExec(t, f.db, `UPDATE users SET suspended_at='2026-01-01T00:00:00Z' WHERE id='ann'`)
	if w := f.confirm(f.ann, version, preview.ReviewToken); w.Code == 200 {
		t.Fatal("suspended owner returned the page")
	}
	if n := gridcraftCount(t, f.instanceFixture, `SELECT count(*) FROM pages WHERE id='shared-book-id' AND private_audience=0 AND pages_version=?`, version); n != 1 {
		t.Fatal("a refused owner changed the page")
	}
}

// Returning is offered only where sharing again is possible: workbooks and
// HTML artifact projects. Docs and native Word and Sheet have a private
// audience but no share path yet (PX-10, #5617), so returning them would be
// one-way and is refused. Crew-owned Pages name no person and are refused, as
// are types without a private audience.
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
		kind, reason      string
		code              int
	}{
		{"artifact project", "ann", "", []string{`INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version,shared_version) VALUES('ws-old','kind-artifact-project','artifact_project',1,1)`}, "artifact_project", "", 200},
		{"document", "ann", "", []string{`INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('ws-old','kind-document',3,x'',x'')`}, "", "no_reshare_path", 409},
		{"native word", "ann", "", []string{`INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('kind-native-word','ws-old','wordcraft','` + hex40 + `','` + hex64 + `','` + hex64 + `')`}, "", "no_reshare_path", 409},
		{"native sheet", "ann", "", []string{`INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('kind-native-sheet','ws-old','gridcraft','` + hex40 + `','` + hex64 + `','` + hex64 + `')`}, "", "no_reshare_path", 409},
		{"design", "ann", "", []string{`INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version,shared_version) VALUES('ws-old','kind-design','design',1,1)`}, "", "unsupported_type", 409},
		{"dashboard", "ann", "", nil, "", "unsupported_type", 409},
		{"crew-owned artifact project", "", "crew-kinds", []string{`INSERT INTO artifact_version_heads(workspace_id,page_id,content_type,latest_version,shared_version) VALUES('ws-old','kind-crew-owned-artifact-project','artifact_project',1,1)`}, "", "crew_owned", 409},
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
			if c.reason != "" && !strings.Contains(w.Body.String(), `"reason":"`+c.reason+`"`) {
				t.Fatalf("%s reason: %s", c.name, w.Body)
			}
			if c.reason == "no_reshare_path" && !strings.Contains(w.Body.String(), "#5617") {
				t.Fatalf("%s refusal does not name the missing share path: %s", c.name, w.Body)
			}
		})
	}
}

// An HTML artifact project through its real create and share handlers:
// filed in a folder, shared at version 1, read by a grant holder and a
// folder reader. The return removes both, the old shared version is
// unreachable until the owner shares again, the folder never comes back, and
// the member journal shows no name.
func TestReturnPrivateArtifactProjectEndToEnd(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	if created := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Three screens",`+strings.TrimPrefix(artifactProjectBody(0, "Version one"), "{")); created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body)
	}
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='three-screens'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, owner)
	pagesSeedUser(t, h, ws, "project-reader", "project-reader@example.test", "MEMBER")
	pagesSeedUser(t, h, ws, "project-folder", "project-folder@example.test", "MEMBER")
	if got := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); got.Code != 200 {
		t.Fatalf("share %d %s", got.Code, got.Body)
	}
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user','project-reader','read',?,datetime('now'))`, page, owner)
	execOrFatal(t, h.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('share-folder',?,'share-folder','Share folder','crew-lookout',datetime('now'),datetime('now'))`, ws)
	execOrFatal(t, h.db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_at) VALUES('share-folder','user','project-folder',1,0,datetime('now'))`)
	execOrFatal(t, h.db, `UPDATE pages SET folder_id='share-folder' WHERE id=?`, page)
	// Readers act as the MEMBERs they are (the fixture's call is OWNER-scoped).
	tokens := map[string]string{owner: "crewship_cli_artifact-project-owner"}
	asMember := func(user, method, path, body string, serve func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		if tokens[user] == "" {
			tokens[user] = mintTokenFor(t, h.db, user, "return-project-"+user)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "MEMBER")
		req = req.WithContext(context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken))
		req.SetPathValue("slug", "three-screens")
		w := httptest.NewRecorder()
		serve(w, req)
		return w
	}
	read := func(user, query string) int {
		return asMember(user, "GET", "/api/v1/pages/three-screens/artifact"+query, "", h.GetArtifactProject).Code
	}
	for _, who := range []string{"project-reader", "project-folder"} {
		if code := read(who, ""); code != 200 {
			t.Fatalf("%s reads the shared version before return: %d", who, code)
		}
	}
	returnCall := func(user, body string) *httptest.ResponseRecorder {
		return asMember(user, "POST", "/api/v1/pages/three-screens/return-private", body, h.ReturnPageToPrivate)
	}
	var version int64
	if err := h.db.QueryRow(`SELECT pages_version FROM pages WHERE id=?`, page).Scan(&version); err != nil {
		t.Fatal(err)
	}
	w := returnCall(owner, fmt.Sprintf(`{"pages_version":%d,"dry_run":true}`, version))
	if w.Code != 200 {
		t.Fatalf("preview %d %s", w.Code, w.Body)
	}
	var preview pageReturnPrivateWire
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || preview.Kind != "artifact_project" || preview.FromFolder == nil || preview.FromFolder.Slug != "share-folder" || !strings.Contains(preview.Reshare, "artifact/share") {
		t.Fatalf("preview %s %v", w.Body, err)
	}
	lost := map[string]bool{}
	for _, c := range preview.Changes {
		lost[c.SubjectID] = c.Before.Read && !c.After.Read
	}
	if !lost["project-reader"] || !lost["project-folder"] {
		t.Fatalf("preview does not list the grant holder and the folder reader: %s", w.Body)
	}
	confirmed := returnCall(owner, fmt.Sprintf(`{"pages_version":%d,"confirm":true,"review_token":%q}`, version, preview.ReviewToken))
	if confirmed.Code != 200 {
		t.Fatalf("confirm %d %s", confirmed.Code, confirmed.Body)
	}
	var private, shared int64
	var folder *string
	if err := h.db.QueryRow(`SELECT p.private_audience,p.folder_id,v.shared_version FROM pages p JOIN artifact_version_heads v ON v.page_id=p.id WHERE p.id=?`, page).Scan(&private, &folder, &shared); err != nil || private != 1 || folder != nil {
		t.Fatalf("after return: private=%d folder=%v err=%v", private, folder, err)
	}
	// The old shared version is unreachable to everyone the audience excludes.
	for _, who := range []string{"project-reader", "project-folder"} {
		for _, query := range []string{"", fmt.Sprintf("?version=%d", shared)} {
			if code := read(who, query); code != 404 {
				t.Fatalf("%s reads shared version %q after return: %d", who, query, code)
			}
		}
	}
	if code := read(owner, ""); code != 200 {
		t.Fatalf("owner after return: %d", code)
	}
	// Sharing again is the supported path back; it restores neither the grant
	// nor the folder.
	if got := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); got.Code != 200 {
		t.Fatalf("re-share %d %s", got.Code, got.Body)
	}
	for _, who := range []string{"project-reader", "project-folder"} {
		if code := read(who, ""); code != 404 {
			t.Fatalf("%s regained access through a re-share: %d", who, code)
		}
	}
	// The member journal never names the returned project.
	rr := httptest.NewRecorder()
	NewJournalHandler(h.db, newTestLogger(), nil).List(rr, withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/journal?workspace_id="+ws+"&limit=500", nil), "project-reader", ws, "MEMBER"))
	if rr.Code != 200 {
		t.Fatalf("journal %d %s", rr.Code, rr.Body)
	}
	if leaks := returnPrivateJournalLeaks(t, rr.Body.Bytes(), `"three-screens"`, "Three screens", "project-reader@example.test", "project-folder@example.test"); len(leaks) > 0 {
		t.Fatalf("journal leaks the private project: %v", leaks)
	}
}
