package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// #5317 step 5.3: GridCraft (Sheet), Mooring and Document (Word) access by a
// chat run stays on the kind's own exact-run consent (a mandate, a drafting or
// iteration consent) with the flag on, because that consent binds the targets,
// base revision and lease of every write. With the flag on each access is
// still audited as artifact.agent_run_accessed with its human, in the access's
// own transaction and failing closed; with the flag off nothing changes.

func agentAccessModesOn(t *testing.T, f *artifactPrincipalFixture) {
	t.Helper()
	mustExec(t, f.rig.db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('kinds-flag-on','ff_artifact_agent_access_modes',?,1)`, f.a.WorkspaceID)
}

func kindAuditRows(t *testing.T, f *artifactPrincipalFixture) []runAuditRow {
	t.Helper()
	return runAuditRows(t, &runScopeRig{f: f})
}

func wantKindAudit(t *testing.T, f *artifactPrincipalFixture, want map[string]string) {
	t.Helper()
	rows := kindAuditRows(t, f)
	got := map[string]string{}
	for _, r := range rows {
		if r.User != f.a.HumanUserID || r.Agent != f.a.AgentID || r.Run != f.a.RunID {
			t.Fatalf("audit row names the wrong principal: %+v", r)
		}
		got[r.Page+"/"+r.Operation] = r.Reach
	}
	if len(got) != len(want) || len(rows) != len(want) {
		t.Fatalf("audit rows %+v, want %v", rows, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("audit %s = %q, want %q; rows %+v", k, got[k], v, rows)
		}
	}
}

type sheetRunRig struct {
	f     *artifactPrincipalFixture
	h     *gridcraftHTTP
	human func(method, path, body string) *http.Request
	token string
}

func newSheetRunRig(t *testing.T, flagOn bool) *sheetRunRig {
	t.Helper()
	rig := pagesTransferSetupWithDB(t, gridcraftFullDB(t))
	f, _ := documentCreationAdmissionWithPrincipal(t, newArtifactPrincipalFixtureWithRig(t, rig))
	f.rig.db.SetMaxOpenConns(1)
	wmSeedPage(t, f.rig.db, f.a.WorkspaceID, "grid-run-page", "grid-run", "Workbook", f.a.HumanUserID, "")
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id='grid-run-page'`)
	f.pages.artifactMasterToken = "synthetic-sheet-run-master"
	store := appengine.New(f.rig.db, appengine.Options{Enabled: true})
	engine := &gridcraftBlockingEngine{}
	h := &gridcraftHTTP{pages: f.pages, store: store, engine: engine, operations: artifacts.NewGridCraftAdapter(store, engine, true)}
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "sheet-run-owner-cli")
	human := func(method, path, body string) *http.Request {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+humanToken)
		ctx := context.WithValue(r.Context(), ctxUser, &AuthUser{ID: f.a.HumanUserID})
		ctx = context.WithValue(ctx, ctxWorkspaceID, f.a.WorkspaceID)
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		r = r.WithContext(ctx)
		r.SetPathValue("slug", "@grid-run-page")
		return r
	}
	imported := httptest.NewRecorder()
	h.importFile(imported, human("POST", "/", `{"mutation_id":"import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`))
	wantCode(t, imported, 200, "import")
	grant := httptest.NewRecorder()
	h.mandate(grant, human("POST", "/", fmt.Sprintf(`{"mandate_id":"cell-edit","run_id":%q,"base_rev":1,"generation":1,"targets":["Sheet1!B2"]}`, f.a.RunID)))
	wantCode(t, grant, 200, "mandate")
	if flagOn {
		agentAccessModesOn(t, f)
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	return &sheetRunRig{f: f, h: h, human: human, token: token}
}

func (g *sheetRunRig) call(body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	g.h.agent(w, documentDraftRequest(g.f, g.token, "POST", body))
	return w
}

const (
	sheetRunRead  = `{"action":"read","artifact_id":"grid-run-page","mandate_id":"cell-edit"}`
	sheetRunApply = `{"action":"apply","artifact_id":"grid-run-page","mandate_id":"cell-edit","request":{"mutation_id":"agent-edit","mandate_id":"cell-edit","base_rev":1,"generation":1,"command":{"kind":"cell.set","sheet":"Sheet1","cell":"B2","input":"42"}}}`
)

func sheetHead(t *testing.T, f *artifactPrincipalFixture) int {
	t.Helper()
	var rev int
	if err := f.rig.db.QueryRow(`SELECT head_rev FROM gridcraft_heads WHERE page_id='grid-run-page'`).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

// A Sheet's mandate access is audited with its human; an unmandated Sheet is
// refused like a missing one; a standing grant writes nothing.
func TestRunScopeSheet_MandateAccessIsAudited(t *testing.T) {
	g := newSheetRunRig(t, true)
	wantCode(t, g.call(sheetRunRead), 200, "mandated read")
	wantCode(t, g.call(sheetRunApply), 200, "mandated write")
	wantCode(t, g.call(sheetRunApply), 200, "exact retry")
	wmSeedPage(t, g.f.rig.db, g.f.a.WorkspaceID, "grid-run-other", "grid-run-other", "Other", g.f.a.HumanUserID, "")
	mustExec(t, g.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('grid-run-other','agent',?,'write',?)`, g.f.a.AgentID, g.f.a.HumanUserID)
	other := g.call(strings.ReplaceAll(sheetRunApply, "grid-run-page", "grid-run-other"))
	missing := g.call(strings.ReplaceAll(sheetRunApply, "grid-run-page", "grid-run-nowhere"))
	if other.Code == 200 || other.Code != missing.Code || other.Body.String() != missing.Body.String() {
		t.Fatalf("an unmandated Sheet: %d %q, missing: %d %q", other.Code, other.Body, missing.Code, missing.Body)
	}
	wantKindAudit(t, g.f, map[string]string{"grid-run-page/read": "consented", "grid-run-page/write": "consented"})
}

// A Sheet whose human loses it mid-run is no longer reachable.
func TestRunScopeSheet_HumanLosingAccessStopsTheRun(t *testing.T) {
	g := newSheetRunRig(t, true)
	wantCode(t, g.call(sheetRunRead), 200, "baseline")
	mustExec(t, g.f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='grid-run-page'`, g.f.rig.adminID)
	if w := g.call(sheetRunRead); w.Code == 200 {
		t.Fatal("the run kept reading after its human lost the Sheet")
	}
}

func TestRunScopeSheet_AuditFailsClosed(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER run_audit_abort BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`,
		`CREATE TRIGGER run_audit_ignore BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(IGNORE); END`,
	} {
		t.Run(strings.Fields(trigger)[2], func(t *testing.T) {
			g := newSheetRunRig(t, true)
			mustExec(t, g.f.rig.db, trigger)
			if w := g.call(sheetRunRead); w.Code == 200 {
				t.Fatal("read succeeded without its audit")
			}
			if w := g.call(sheetRunApply); w.Code == 200 {
				t.Fatal("write succeeded without its audit")
			}
			if rev := sheetHead(t, g.f); rev != 1 {
				t.Fatalf("refused write moved the head to %d", rev)
			}
		})
	}
}

func TestRunScopeSheet_FlagOffUnchanged(t *testing.T) {
	g := newSheetRunRig(t, false)
	wantCode(t, g.call(sheetRunRead), 200, "mandated read")
	wantCode(t, g.call(sheetRunApply), 200, "mandated write")
	if rows := kindAuditRows(t, g.f); len(rows) != 0 {
		t.Fatalf("flag off wrote agent-run audit records: %+v", rows)
	}
}

func newMooringRunRig(t *testing.T, flagOn bool) (*artifactPrincipalFixture, func(string) *httptest.ResponseRecorder) {
	t.Helper()
	rig := pagesTransferSetupWithDB(t, gridcraftFullDB(t))
	f, _ := documentCreationAdmissionWithPrincipal(t, newArtifactPrincipalFixtureWithRig(t, rig))
	f.rig.db.SetMaxOpenConns(1)
	wmSeedPage(t, f.rig.db, f.a.WorkspaceID, "mooring-run-page", "mooring-run", "Generic", f.a.HumanUserID, "")
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id='mooring-run-page'`)
	mustExec(t, f.rig.db, `INSERT INTO mooring_documents(workspace_id,page_id,app_id,rev,generation,snapshot) VALUES(?,'mooring-run-page','hello',1,1,'{"text":"initial"}')`, f.a.WorkspaceID)
	f.pages.artifactMasterToken = "synthetic-mooring-run-master"
	h := &mooringHTTP{workbook: &gridcraftHTTP{pages: f.pages}, storage: mooring.JSONAdapter{}}
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "mooring-run-cli")
	r := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"mandate_id":"set-text","run_id":%q,"base_rev":1,"generation":1,"command":"set"}`, f.a.RunID)))
	r.Header.Set("Authorization", "Bearer "+humanToken)
	ctx := context.WithValue(r.Context(), ctxUser, &AuthUser{ID: f.a.HumanUserID})
	ctx = context.WithValue(ctx, ctxWorkspaceID, f.a.WorkspaceID)
	ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
	r = r.WithContext(ctx)
	r.SetPathValue("slug", "@mooring-run-page")
	grant := httptest.NewRecorder()
	h.mandate(grant, r)
	wantCode(t, grant, 200, "mooring mandate")
	if flagOn {
		agentAccessModesOn(t, f)
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	return f, func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.agent(w, documentDraftRequest(f, token, "POST", body))
		return w
	}
}

const (
	mooringRunRead    = `{"action":"read","artifact_id":"mooring-run-page","mandate_id":"set-text"}`
	mooringRunCommand = `{"action":"command","artifact_id":"mooring-run-page","mandate_id":"set-text","request":{"mutation_id":"agent-one","base_rev":1,"generation":1,"command":"set","arguments":{"text":"agent confirmed"}}}`
)

func TestRunScopeMooring_MandateAccessIsAudited(t *testing.T) {
	f, call := newMooringRunRig(t, true)
	wantCode(t, call(mooringRunRead), 200, "mandated read")
	wantCode(t, call(mooringRunCommand), 200, "mandated command")
	wantCode(t, call(mooringRunRead), 200, "second read")
	wantKindAudit(t, f, map[string]string{"mooring-run-page/read": "consented", "mooring-run-page/write": "consented"})
}

func TestRunScopeMooring_AuditFailsClosed(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER run_audit_abort BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.agent_run_accessed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`,
		`CREATE TRIGGER run_audit_ignore BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.agent_run_accessed' BEGIN SELECT RAISE(IGNORE); END`,
	} {
		t.Run(strings.Fields(trigger)[2], func(t *testing.T) {
			f, call := newMooringRunRig(t, true)
			mustExec(t, f.rig.db, trigger)
			if w := call(mooringRunRead); w.Code == 200 {
				t.Fatal("read succeeded without its audit")
			}
			if w := call(mooringRunCommand); w.Code == 200 {
				t.Fatal("command succeeded without its audit")
			}
			var rev int
			if err := f.rig.db.QueryRow(`SELECT rev FROM mooring_documents WHERE page_id='mooring-run-page'`).Scan(&rev); err != nil || rev != 1 {
				t.Fatalf("refused command moved the document to %d %v", rev, err)
			}
		})
	}
}

func TestRunScopeMooring_FlagOffUnchanged(t *testing.T) {
	f, call := newMooringRunRig(t, false)
	wantCode(t, call(mooringRunRead), 200, "mandated read")
	wantCode(t, call(mooringRunCommand), 200, "mandated command")
	if rows := kindAuditRows(t, f); len(rows) != 0 {
		t.Fatalf("flag off wrote agent-run audit records: %+v", rows)
	}
}

// A Document (Word) created and drafted under its creation consent: the
// create, the reads and the writes are audited; the lease is not content.
func TestRunScopeDocument_ConsentAccessIsAudited(t *testing.T) {
	f, h, token, created := newDocumentDraftFixture(t, true)
	agentAccessModesOn(t, f)
	// The create happened before the flag; it is not recorded.
	if rows := kindAuditRows(t, f); len(rows) != 0 {
		t.Fatalf("flag-off create recorded: %+v", rows)
	}
	f.rig.db.SetMaxOpenConns(1)
	initializeDocumentDraftProjection(t, f, created.PageID)
	wantCode(t, callDocumentDraftAuthority(t, f, h, token, created.PageID, "state", map[string]any{}), 200, "draft read")
	lease := documentDraftLease(t, f, h, token, created.PageID)
	wantCode(t, callDocumentDraftAuthority(t, f, h, token, created.PageID, "commit", documentDraftPayload(t, "draft-one", "initial", "draft one", 0, lease)), 200, "draft commit")
	wantKindAudit(t, f, map[string]string{created.PageID + "/read": "consented", created.PageID + "/write": "consented"})
}

func TestRunScopeDocument_CreateIsAuditedAndFailsClosed(t *testing.T) {
	f, intent := documentCreationAdmissionFixture(t, true)
	if err := bindCreation(f, intent); err != nil {
		t.Fatal(err)
	}
	agentAccessModesOn(t, f)
	f.pages.artifactMasterToken = "synthetic-draft"
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	f.pages.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
		return []byte(`{"schema":5,"snapshot":"c3ludGhldGlj","blocks":[{"id":"paragraph","hash":"synthetic"}]}`), 200, nil
	}
	mustExec(t, f.rig.db, `CREATE TRIGGER run_audit_abort BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.agent_run_accessed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	w := httptest.NewRecorder()
	f.pages.CreateDocumentAgent(w, documentDraftRequest(f, token, http.MethodPost, documentCreationBody))
	if w.Code == 201 {
		t.Fatal("create succeeded without its audit")
	}
	var pages int
	if err := f.rig.db.QueryRow(`SELECT count(*) FROM document_creations WHERE run_id=?`, f.a.RunID).Scan(&pages); err != nil || pages != 0 {
		t.Fatalf("refused create left a Document: %d %v", pages, err)
	}
	mustExec(t, f.rig.db, `DROP TRIGGER run_audit_abort`)
	w = httptest.NewRecorder()
	f.pages.CreateDocumentAgent(w, documentDraftRequest(f, token, http.MethodPost, documentCreationBody))
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created documentCreationResult
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	wantKindAudit(t, f, map[string]string{created.PageID + "/create": "created"})
}

// Required test 18: a write to a Word document under another person's lease
// ends as a proposal. With the flag on, the run's human edits a shared
// Document another person owns, and that person holds the lease: the agent
// cannot take the lease, its direct commit is refused, its proposal is stored,
// the Document is unchanged and the person keeps the lease.
func TestRunScopeDocument_WriteUnderAnotherPersonsLeaseEndsAsAProposal(t *testing.T) {
	var owner string
	f, _, token, intent := iterationFixtureFor(t, func(f *artifactPrincipalFixture, intent *documents.IterationIntent) {
		owner = f.rig.adminID
		mustExec(t, f.rig.db, `UPDATE documents SET schema_version=? WHERE page_id=?`, documents.FormattedSchema, intent.PageID)
		intent.Schema = documents.FormattedSchema
		mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0,owner_user_id=? WHERE id=?`, owner, intent.PageID)
		mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write',?)`, intent.PageID, f.a.HumanUserID, owner)
		mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent',?,'write',?)`, intent.PageID, f.a.AgentID, owner)
	})
	agentAccessModesOn(t, f)
	f.rig.db.SetMaxOpenConns(1)
	h := &documentAuthority{pages: f.pages, store: documents.New(f.rig.db, documents.Options{})}
	key := documents.Key{WorkspaceID: f.a.WorkspaceID, PageID: intent.PageID}
	initializeDocumentDraftProjection(t, f, intent.PageID)
	// The person editing holds the lease, acquired through the store as a human.
	human, err := h.store.Acquire(t.Context(), key, "person-editor", func(context.Context, *sql.Tx) (string, error) { return "user:" + owner, nil })
	if err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := f.rig.db.QueryRow(`SELECT head_rev FROM documents WHERE page_id=?`, intent.PageID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if w := callDocumentDraftAuthority(t, f, h, token, intent.PageID, "lease", map[string]any{"owner": "synthetic-engine"}); w.Code == 200 {
		t.Fatalf("the agent took the person's lease: %s", w.Body)
	}
	commit := documentDraftPayloadAt(t, documents.FormattedSchema, "agent-direct", "initial", "agent edit", before, human)
	if w := callDocumentDraftAuthority(t, f, h, token, intent.PageID, "commit", commit); w.Code == 200 {
		t.Fatalf("the agent wrote directly under the person's lease: %s", w.Body)
	}
	propose := documentDraftPayloadAt(t, documents.FormattedSchema, "agent-proposal", "initial", "agent edit", before, documents.Lease{})
	for _, k := range []string{"kind", "epoch", "leaseToken", "leaseOwner", "snapshot", "update"} {
		delete(propose, k)
	}
	propose["target"] = "paragraph"
	if w := callDocumentDraftAuthority(t, f, h, token, intent.PageID, "propose", propose); w.Code != 200 {
		t.Fatalf("the agent's proposal: %d %s", w.Code, w.Body)
	}
	var after int64
	var proposals int
	if err := f.rig.db.QueryRow(`SELECT head_rev,(SELECT count(*) FROM document_proposals WHERE page_id=documents.page_id) FROM documents WHERE page_id=?`, intent.PageID).Scan(&after, &proposals); err != nil {
		t.Fatal(err)
	}
	if after != before || proposals != 1 {
		t.Fatalf("head %d -> %d, proposals %d: want unchanged head and one proposal", before, after, proposals)
	}
	if _, err := h.store.Renew(t.Context(), key, human, func(context.Context, *sql.Tx) (string, error) { return "user:" + owner, nil }); err != nil {
		t.Fatalf("the person lost the lease: %v", err)
	}
	got := map[string]string{}
	for _, r := range kindAuditRows(t, f) {
		got[r.Page+"/"+r.Operation] = r.Reach
	}
	if got[intent.PageID+"/propose"] != "consented" {
		t.Fatalf("the proposal was not audited as a propose: %v", got)
	}
}
