package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
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
	wantCode(t, f.do(f.wsAdmin, "PUT", "/api/v1/pages/shared-book/grants"+q, `{"subject_type":"user","subject":"wsadmin@ex.com","level":"write"}`), 200, "admin self grant")
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

// Publication revokes direct agent mandates; a shared workbook refuses direct
// agent access and new mandates with proposal_required.
func TestGridCraftSharedWorkbookRequiresAgentProposal(t *testing.T) {
	rig := pagesTransferSetupWithDB(t, gridcraftFullDB(t))
	f, _ := documentCreationAdmissionWithPrincipal(t, newArtifactPrincipalFixtureWithRig(t, rig))
	f.rig.db.SetMaxOpenConns(1)
	wmSeedPage(t, f.rig.db, f.a.WorkspaceID, "grid-shared-page", "grid-shared", "Workbook", f.a.HumanUserID, "")
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id='grid-shared-page'`)
	mustExec(t, f.rig.db, `INSERT INTO users(id,email,full_name) VALUES('grid-peer','grid-peer@example.test','Peer')`)
	mustExec(t, f.rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('grid-peer-member',?,'grid-peer','MEMBER')`, f.a.WorkspaceID)
	f.pages.artifactMasterToken = "synthetic-workbook-master"
	store := appengine.New(f.rig.db, appengine.Options{Enabled: true})
	engine := &gridcraftBlockingEngine{}
	h := &gridcraftHTTP{pages: f.pages, store: store, engine: engine, operations: artifacts.NewGridCraftAdapter(store, engine, true)}
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "workbook-share-owner")
	human := func(method, body string) *http.Request {
		r := httptest.NewRequest(method, "/", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+humanToken)
		ctx := context.WithValue(r.Context(), ctxUser, &AuthUser{ID: f.a.HumanUserID, Email: "owner@example.test"})
		ctx = context.WithValue(ctx, ctxWorkspaceID, f.a.WorkspaceID)
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		r = r.WithContext(ctx)
		r.SetPathValue("slug", "@grid-shared-page")
		return r
	}
	serve := func(handler http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	wantCode(t, serve(h.importFile, human("POST", gridcraftImportBody("import", 0, 0))), 200, "import")
	mandate := fmt.Sprintf(`{"mandate_id":"cell-edit","run_id":%q,"base_rev":1,"generation":1,"targets":["Sheet1!B2"]}`, f.a.RunID)
	wantCode(t, serve(h.mandate, human("POST", mandate)), 200, "private mandate")
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	agent := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.agent(w, documentDraftRequest(f, token, "POST", body))
		return w
	}
	read := `{"action":"read","artifact_id":"grid-shared-page","mandate_id":"cell-edit"}`
	wantCode(t, agent(read), 200, "private direct read")

	shared := serve(h.share, human("POST", `{"editor":"grid-peer@example.test","publish":true}`))
	wantCode(t, shared, 200, "publish")
	if !strings.Contains(shared.Body.String(), `"revoked_mandates":1`) {
		t.Fatalf("share response %s", shared.Body.String())
	}
	var revoked int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM gridcraft_intents WHERE page_id='grid-shared-page' AND revoked_at IS NOT NULL`).Scan(&revoked); err != nil || revoked != 1 {
		t.Fatalf("mandates revoked=%d err=%v", revoked, err)
	}
	apply := `{"action":"apply","artifact_id":"grid-shared-page","mandate_id":"cell-edit","request":{"mutation_id":"agent-edit","mandate_id":"cell-edit","base_rev":1,"generation":1,"command":{"kind":"cell.set","sheet":"Sheet1","cell":"B2","input":"42"}}}`
	for name, body := range map[string]string{"read": read, "apply": apply} {
		w := agent(body)
		wantCode(t, w, 409, "shared "+name)
		if !strings.Contains(w.Body.String(), "proposal_required") {
			t.Fatalf("shared %s: %s", name, w.Body.String())
		}
	}
	again := serve(h.mandate, human("POST", strings.Replace(mandate, "cell-edit", "cell-edit-2", 1)))
	wantCode(t, again, 409, "no direct mandate on shared workbook")
	if !strings.Contains(again.Body.String(), "proposal_required") {
		t.Fatalf("mandate refusal %s", again.Body.String())
	}
	var head int
	if err := f.rig.db.QueryRow(`SELECT head_rev FROM gridcraft_heads WHERE page_id='grid-shared-page'`).Scan(&head); err != nil || head != 1 {
		t.Fatalf("agent changed shared workbook: %d %v", head, err)
	}
}
