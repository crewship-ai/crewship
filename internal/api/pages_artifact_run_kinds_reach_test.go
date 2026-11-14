package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/sidecar"
)

// #5317 step 5.3, owner decision: a Sheet (GridCraft), a Mooring document and
// a Word document (Docs) are reachable by a flag-on chat run through its scope
// like every other artifact (attached, created, standing, folder), without a
// mandate or consent. Every access is intersected with the human as an
// ordinary member and refused exactly like a missing artifact; standing and
// folder reads count against run_read_limit. The scope only ever reads: an
// agent change without a mandate or consent is refused (a Sheet or Mooring
// change answers proposal_required, as no proposal delivery exists for them)
// and never touches the head or a lease.

type kindReachRig struct {
	*runScopeRig
	sheet   *gridcraftHTTP
	mooring *mooringHTTP
	docs    *documentAuthority
}

func newKindReachRig(t *testing.T, setup ...func(*artifactPrincipalFixture)) *kindReachRig {
	t.Helper()
	g := newRunScopeRig(t, setup...)
	store := appengine.New(g.f.rig.db, appengine.Options{Enabled: true})
	engine := &gridcraftBlockingEngine{}
	sheet := &gridcraftHTTP{pages: g.router.pages, store: store, engine: engine, operations: artifacts.NewGridCraftAdapter(store, engine, true)}
	return &kindReachRig{
		runScopeRig: g,
		sheet:       sheet,
		mooring:     &mooringHTTP{workbook: sheet, storage: mooring.JSONAdapter{}},
		docs:        &documentAuthority{pages: g.router.pages, store: documents.New(g.f.rig.db, documents.Options{})},
	}
}

// humanAdmin makes the run's human a workspace ADMIN before its chat receipt
// is captured (a role change during a run ends the run on its own).
func humanAdmin(f *artifactPrincipalFixture) {
	if _, err := f.rig.db.Exec(`UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, f.a.WorkspaceID, f.a.HumanUserID); err != nil {
		panic(err)
	}
}

func (g *kindReachRig) humanToken(t *testing.T, user string) string {
	t.Helper()
	token, ok := g.tokens[user]
	if !ok {
		token = mintTokenFor(t, g.f.rig.db, user, "kind-reach-"+user)
		g.tokens[user] = token
	}
	return token
}

// agentRequest is the run's own call, with its original bearer.
func (g *kindReachRig) agentRequest(method, target, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+g.runToken)
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, g.f.a.WorkspaceID)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, g.f.a.CrewID)
	return r.WithContext(ctx)
}

// workbook seeds a private workbook owned by owner, imported as that owner.
func (g *kindReachRig) workbook(t *testing.T, id, owner string) {
	t.Helper()
	wmSeedPage(t, g.f.rig.db, g.f.a.WorkspaceID, id, id, "Workbook", owner, "")
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=1 WHERE id=?`, id)
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"mutation_id":"import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`))
	r.Header.Set("Authorization", "Bearer "+g.humanToken(t, owner))
	ctx := context.WithValue(r.Context(), ctxUser, &AuthUser{ID: owner})
	ctx = context.WithValue(ctx, ctxWorkspaceID, g.f.a.WorkspaceID)
	ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
	r = r.WithContext(ctx)
	r.SetPathValue("slug", "@"+id)
	w := httptest.NewRecorder()
	g.sheet.importFile(w, r)
	wantCode(t, w, 200, "import "+id)
}

func (g *kindReachRig) sheetCall(body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	g.sheet.agent(w, g.agentRequest("POST", "/api/v1/internal/gridcraft", body))
	return w
}

func sheetScopeRead(page string) string {
	return fmt.Sprintf(`{"action":"read","artifact_id":%q}`, page)
}

func sheetScopeApply(page string) string {
	return fmt.Sprintf(`{"action":"apply","artifact_id":%q,"request":{"mutation_id":"agent-edit","base_rev":1,"generation":1,"command":{"kind":"cell.set","sheet":"Sheet1","cell":"B2","input":"42"}}}`, page)
}

func (g *kindReachRig) sheetHead(t *testing.T, page string) int {
	t.Helper()
	var rev int
	if err := g.f.rig.db.QueryRow(`SELECT head_rev FROM gridcraft_heads WHERE page_id=?`, page).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

// sameRefusal: the refusal of something the run does not reach is the one a
// missing artifact gets.
func sameRefusal(t *testing.T, what string, got, missing *httptest.ResponseRecorder) {
	t.Helper()
	if got.Code < 400 || got.Code != missing.Code || got.Body.String() != missing.Body.String() {
		t.Fatalf("%s: %d %q, missing: %d %q", what, got.Code, got.Body, missing.Code, missing.Body)
	}
}

func TestRunScopeSheetReach_AttachedReadableUnattachedLikeMissing(t *testing.T) {
	g := newKindReachRig(t)
	g.workbook(t, "sheet-attached", g.f.a.HumanUserID)
	g.workbook(t, "sheet-sibling", g.f.a.HumanUserID)
	g.start(t, "sheet-attached")
	read := g.sheetCall(sheetScopeRead("sheet-attached"))
	wantCode(t, read, 200, "attached Sheet read")
	var state appengine.State
	if err := json.Unmarshal(read.Body.Bytes(), &state); err != nil || state.Rev != 1 {
		t.Fatalf("attached Sheet read the wrong version: %+v %v", state, err)
	}
	sameRefusal(t, "an unattached Sheet", g.sheetCall(sheetScopeRead("sheet-sibling")), g.sheetCall(sheetScopeRead("sheet-nowhere")))
	wantKindAudit(t, g.f, map[string]string{"sheet-attached/read": runReachReferenced})
}

// An agent change to a reached Sheet without a mandate is a proposal, and no
// proposal delivery exists: proposal_required, the head unchanged, nothing
// recorded. An unreached Sheet answers like a missing one.
func TestRunScopeSheetReach_WriteEndsAsProposalRequired(t *testing.T) {
	g := newKindReachRig(t)
	g.workbook(t, "sheet-attached", g.f.a.HumanUserID)
	g.workbook(t, "sheet-sibling", g.f.a.HumanUserID)
	g.start(t, "sheet-attached")
	w := g.sheetCall(sheetScopeApply("sheet-attached"))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "proposal_required") {
		t.Fatalf("an unmandated change to an attached Sheet: %d %s", w.Code, w.Body)
	}
	if rev := g.sheetHead(t, "sheet-attached"); rev != 1 {
		t.Fatalf("a refused change moved the head to %d", rev)
	}
	sameRefusal(t, "a change to an unattached Sheet", g.sheetCall(sheetScopeApply("sheet-sibling")), g.sheetCall(sheetScopeApply("sheet-nowhere")))
	// Status and commits need a mandate: through the scope they get exactly a
	// missing workbook's answer.
	for _, action := range []string{`{"action":"commits","artifact_id":%q,"since":0}`, `{"action":"status","artifact_id":%q,"mutation_id":"agent-edit"}`} {
		sameRefusal(t, "the scope's "+action, g.sheetCall(fmt.Sprintf(action, "sheet-attached")), g.sheetCall(fmt.Sprintf(action, "sheet-nowhere")))
	}
	if rows := kindAuditRows(t, g.f); len(rows) != 0 {
		t.Fatalf("a refused change was recorded: %+v", rows)
	}
}

// A workbook's own rule: agent, crew and folder access never reach workbook
// content (workbookHumanAccess), so neither does a run's standing or folder
// reach. Only attached, created or consented reach applies.
func TestRunScopeSheetReach_StandingAndFolderDoNotReachAWorkbook(t *testing.T) {
	g := newKindReachRig(t)
	g.workbook(t, "sheet-standing", g.f.a.HumanUserID)
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id='sheet-standing'`)
	mustExec(t, g.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('sheet-standing','agent',?,'read',?)`, g.f.a.AgentID, g.f.a.HumanUserID)
	g.start(t)
	sameRefusal(t, "a Sheet reached only through the agent's grant", g.sheetCall(sheetScopeRead("sheet-standing")), g.sheetCall(sheetScopeRead("sheet-nowhere")))
	sameRefusal(t, "a change through the agent's grant", g.sheetCall(sheetScopeApply("sheet-standing")), g.sheetCall(sheetScopeApply("sheet-nowhere")))
	if rev := g.sheetHead(t, "sheet-standing"); rev != 1 {
		t.Fatalf("standing reach moved the head to %d", rev)
	}
	if rows := kindAuditRows(t, g.f); len(rows) != 0 {
		t.Fatalf("a refused access was recorded: %+v", rows)
	}
}

// A published workbook: the human's owner-named editor right is an ordinary
// member's read (the head; workbooks have no shared version). Once the editor
// grant is retired and the human reads only as the audited ADMIN, the run is
// refused like a missing Sheet.
func TestRunScopeSheetReach_HumanLosingAccessAndRoleOnlyReach(t *testing.T) {
	g := newKindReachRig(t, humanAdmin)
	g.workbook(t, "sheet-shared", g.f.rig.adminID)
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id='sheet-shared'`)
	mustExec(t, g.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('sheet-shared','user',?,'write',?)`, g.f.a.HumanUserID, g.f.rig.adminID)
	// The human is an ADMIN from the start (a role change ends the run
	// anyway); an ADMIN who is also an owner-named editor reads as that editor.
	g.start(t, "sheet-shared")
	wantCode(t, g.sheetCall(sheetScopeRead("sheet-shared")), 200, "an ADMIN editor's run reads")
	mustExec(t, g.f.rig.db, `UPDATE page_grants SET retired_at='2026-11-14T00:00:00Z' WHERE page_id='sheet-shared' AND subject_type='user'`)
	sameRefusal(t, "a Sheet the human reads only through the ADMIN role", g.sheetCall(sheetScopeRead("sheet-shared")), g.sheetCall(sheetScopeRead("sheet-nowhere")))
}

// A workbook's own rule is narrower than a member's page read: a `read` grant
// does not reach workbook content. An ADMIN holding one reads the workbook
// only as the audited administrator, and that never passes to the agent.
func TestRunScopeSheetReach_ReadGrantIsNotAWorkbookRead(t *testing.T) {
	g := newKindReachRig(t, humanAdmin)
	g.workbook(t, "sheet-shared", g.f.rig.adminID)
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id='sheet-shared'`)
	mustExec(t, g.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('sheet-shared','user',?,'read',?)`, g.f.a.HumanUserID, g.f.rig.adminID)
	g.start(t, "sheet-shared")
	sameRefusal(t, "an ADMIN's read grant on a Sheet", g.sheetCall(sheetScopeRead("sheet-shared")), g.sheetCall(sheetScopeRead("sheet-nowhere")))
	if rows := kindAuditRows(t, g.f); len(rows) != 0 {
		t.Fatalf("a refused read was recorded: %+v", rows)
	}
}

func TestRunScopeSheetReach_OwnerChangeStopsTheRun(t *testing.T) {
	g := newKindReachRig(t)
	g.workbook(t, "sheet-attached", g.f.a.HumanUserID)
	g.start(t, "sheet-attached")
	wantCode(t, g.sheetCall(sheetScopeRead("sheet-attached")), 200, "baseline")
	mustExec(t, g.f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='sheet-attached'`, g.f.rig.adminID)
	sameRefusal(t, "a Sheet its human lost", g.sheetCall(sheetScopeRead("sheet-attached")), g.sheetCall(sheetScopeRead("sheet-nowhere")))
}

func TestRunScopeSheetReach_FlagOffUnchanged(t *testing.T) {
	g := newKindReachRig(t)
	g.workbook(t, "sheet-attached", g.f.a.HumanUserID)
	g.start(t, "sheet-attached")
	mustExec(t, g.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='ref-flag-on'`)
	sameRefusal(t, "flag off, an attached Sheet without a mandate", g.sheetCall(sheetScopeRead("sheet-attached")), g.sheetCall(sheetScopeRead("sheet-nowhere")))
	sameRefusal(t, "flag off, a change without a mandate", g.sheetCall(sheetScopeApply("sheet-attached")), g.sheetCall(sheetScopeApply("sheet-nowhere")))
	if rows := kindAuditRows(t, g.f); len(rows) != 0 {
		t.Fatalf("flag off wrote agent-run audit records: %+v", rows)
	}
}

// mooringDoc seeds a private generic Mooring document owned by owner.
func (g *kindReachRig) mooringDoc(t *testing.T, id, owner string) {
	t.Helper()
	wmSeedPage(t, g.f.rig.db, g.f.a.WorkspaceID, id, id, "Generic", owner, "")
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=1 WHERE id=?`, id)
	mustExec(t, g.f.rig.db, `INSERT INTO mooring_documents(workspace_id,page_id,app_id,rev,generation,snapshot) VALUES(?,?,'hello',1,1,'{"text":"initial"}')`, g.f.a.WorkspaceID, id)
}

func (g *kindReachRig) mooringCall(body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	g.mooring.agent(w, g.agentRequest("POST", "/api/v1/internal/mooring", body))
	return w
}

func mooringScopeRead(page string) string {
	return fmt.Sprintf(`{"action":"read","artifact_id":%q}`, page)
}

func mooringScopeCommand(page string) string {
	return fmt.Sprintf(`{"action":"command","artifact_id":%q,"request":{"mutation_id":"agent-one","base_rev":1,"generation":1,"command":"set","arguments":{"text":"agent"}}}`, page)
}

func (g *kindReachRig) mooringRev(t *testing.T, page string) int {
	t.Helper()
	var rev int
	if err := g.f.rig.db.QueryRow(`SELECT rev FROM mooring_documents WHERE page_id=?`, page).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

func TestRunScopeMooringReach_AttachedReadableUnattachedLikeMissing(t *testing.T) {
	g := newKindReachRig(t)
	g.mooringDoc(t, "mooring-attached", g.f.a.HumanUserID)
	g.mooringDoc(t, "mooring-sibling", g.f.a.HumanUserID)
	g.start(t, "mooring-attached")
	read := g.mooringCall(mooringScopeRead("mooring-attached"))
	wantCode(t, read, 200, "attached Mooring read")
	if !strings.Contains(read.Body.String(), `"initial"`) {
		t.Fatalf("attached Mooring read: %s", read.Body)
	}
	sameRefusal(t, "an unattached Mooring document", g.mooringCall(mooringScopeRead("mooring-sibling")), g.mooringCall(mooringScopeRead("mooring-nowhere")))
	wantKindAudit(t, g.f, map[string]string{"mooring-attached/read": runReachReferenced})
}

// Mooring has no proposal path: an agent command without a mandate is
// refused, the document is unchanged and nothing is recorded.
func TestRunScopeMooringReach_CommandIsRefused(t *testing.T) {
	g := newKindReachRig(t)
	g.mooringDoc(t, "mooring-attached", g.f.a.HumanUserID)
	g.mooringDoc(t, "mooring-sibling", g.f.a.HumanUserID)
	g.start(t, "mooring-attached")
	w := g.mooringCall(mooringScopeCommand("mooring-attached"))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "proposal_required") {
		t.Fatalf("an unmandated Mooring command: %d %s", w.Code, w.Body)
	}
	if rev := g.mooringRev(t, "mooring-attached"); rev != 1 {
		t.Fatalf("a refused command moved the document to %d", rev)
	}
	sameRefusal(t, "a command to an unattached Mooring document", g.mooringCall(mooringScopeCommand("mooring-sibling")), g.mooringCall(mooringScopeCommand("mooring-nowhere")))
	if rows := kindAuditRows(t, g.f); len(rows) != 0 {
		t.Fatalf("a refused command was recorded: %+v", rows)
	}
}

// A Mooring document is owner-only private output under the same rule as a
// workbook: an agent's own grant does not reach it.
func TestRunScopeMooringReach_StandingDoesNotReach(t *testing.T) {
	g := newKindReachRig(t)
	g.mooringDoc(t, "mooring-standing", g.f.a.HumanUserID)
	mustExec(t, g.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('mooring-standing','agent',?,'read',?)`, g.f.a.AgentID, g.f.a.HumanUserID)
	g.start(t)
	sameRefusal(t, "a Mooring document reached only through the agent's grant", g.mooringCall(mooringScopeRead("mooring-standing")), g.mooringCall(mooringScopeRead("mooring-nowhere")))
	if rows := kindAuditRows(t, g.f); len(rows) != 0 {
		t.Fatalf("a refused access was recorded: %+v", rows)
	}
}

func TestRunScopeMooringReach_HumanLosingAccessStopsTheRun(t *testing.T) {
	g := newKindReachRig(t)
	g.mooringDoc(t, "mooring-attached", g.f.a.HumanUserID)
	g.start(t, "mooring-attached")
	wantCode(t, g.mooringCall(mooringScopeRead("mooring-attached")), 200, "baseline")
	// The document becomes another person's.
	mustExec(t, g.f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='mooring-attached'`, g.f.rig.adminID)
	sameRefusal(t, "a Mooring document the human lost", g.mooringCall(mooringScopeRead("mooring-attached")), g.mooringCall(mooringScopeRead("mooring-nowhere")))
}

func TestRunScopeMooringReach_FlagOffUnchanged(t *testing.T) {
	g := newKindReachRig(t)
	g.mooringDoc(t, "mooring-attached", g.f.a.HumanUserID)
	g.start(t, "mooring-attached")
	mustExec(t, g.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='ref-flag-on'`)
	if w := g.mooringCall(mooringScopeRead("mooring-attached")); w.Code != 400 {
		t.Fatalf("flag off, a read without a mandate: %d %s", w.Code, w.Body)
	}
	if rows := kindAuditRows(t, g.f); len(rows) != 0 {
		t.Fatalf("flag off wrote agent-run audit records: %+v", rows)
	}
}

// wordDoc seeds a Word document owned by owner (private unless shared).
func (g *kindReachRig) wordDoc(t *testing.T, id, owner string, private bool) {
	t.Helper()
	wmSeedPage(t, g.f.rig.db, g.f.a.WorkspaceID, id, id, "Word", owner, "")
	if private {
		mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=1 WHERE id=?`, id)
	}
	mustExec(t, g.f.rig.db, `INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection,head_rev,snapshot_rev,generation) VALUES(?,?,5,X'01',CAST('{}' AS BLOB),3,3,1)`, g.f.a.WorkspaceID, id)
}

// docAuthority is the private Docs authority callback the Node service makes
// with the run's bearer.
// A state callback here is the Node read behind the draft view.
func (g *kindReachRig) docAuthority(t *testing.T, page, op string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	purpose := ""
	if op == "state" {
		purpose = "read"
	}
	return g.docAuthorityFor(t, page, op, purpose, body)
}

// docAuthorityFor is docAuthority with the Node operation that loads the
// state named (Node names it on every state callback; "" names none).
func (g *kindReachRig) docAuthorityFor(t *testing.T, page, op, purpose string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := g.agentRequest("POST", "/", string(raw))
	if purpose != "" {
		r.Header.Set(documentStatePurposeHeader, purpose)
	}
	r.SetPathValue("page", page)
	r.SetPathValue("operation", op)
	w := httptest.NewRecorder()
	checked, ok := g.router.pages.artifactAgentRequest(w, r)
	if !ok {
		t.Fatalf("run control: %d %s", w.Code, w.Body)
	}
	g.docs.operation(w, checked)
	return w
}

// docView is the run's draft view of one Document, served through Node.
func (g *kindReachRig) docView(page string) *httptest.ResponseRecorder {
	target := "/api/v1/internal/documents/draft?workspace_id=" + g.f.a.WorkspaceID
	if page != "" {
		target += "&page_id=" + page
	}
	w := httptest.NewRecorder()
	g.router.pages.GetDocumentDraftAgent(w, g.agentRequest("GET", target, ""))
	return w
}

func (g *kindReachRig) fakeDocs(t *testing.T) {
	t.Helper()
	hash := strings.Repeat("a", 64)
	g.router.pages.documentCall = func(_ context.Context, _, _, page, op string, _ any) ([]byte, int, error) {
		if op != "read" {
			return nil, 500, fmt.Errorf("unexpected Docs call %s", op)
		}
		return []byte(fmt.Sprintf(`{"schema":5,"rev":3,"generation":1,"epoch":"e1","outline":[{"id":"intro","type":"paragraph","hash":%q,"text":"Hello"}]}`, hash)), 200, nil
	}
}

func (g *kindReachRig) docLease(t *testing.T, page string) string {
	t.Helper()
	var writer string
	if err := g.f.rig.db.QueryRow(`SELECT writer FROM documents WHERE page_id=?`, page).Scan(&writer); err != nil {
		t.Fatal(err)
	}
	return writer
}

func TestRunScopeDocumentReach_AttachedReadableUnattachedLikeMissing(t *testing.T) {
	g := newKindReachRig(t)
	g.fakeDocs(t)
	g.wordDoc(t, "word-attached", g.f.a.HumanUserID, true)
	g.wordDoc(t, "word-sibling", g.f.a.HumanUserID, true)
	g.start(t, "word-attached")
	state := g.docAuthority(t, "word-attached", "state", map[string]any{})
	wantCode(t, state, 200, "attached Document state")
	var got struct {
		Rev int64 `json:"rev"`
	}
	if err := json.Unmarshal(state.Body.Bytes(), &got); err != nil || got.Rev != 3 {
		t.Fatalf("attached Document read the wrong revision: %s %v", state.Body, err)
	}
	sameRefusal(t, "an unattached Document", g.docAuthority(t, "word-sibling", "state", map[string]any{}), g.docAuthority(t, "word-nowhere", "state", map[string]any{}))
	view := g.docView("word-attached")
	wantCode(t, view, 200, "attached Document view")
	var v DocumentDraftView
	if err := json.Unmarshal(view.Body.Bytes(), &v); err != nil || v.PageID != "word-attached" || v.Mode != "read" || v.Rev != 3 || len(v.Targets) != 1 {
		t.Fatalf("attached Document view: %s %v", view.Body, err)
	}
	sameRefusal(t, "the view of an unattached Document", g.docView("word-sibling"), g.docView("word-nowhere"))
	wantKindAudit(t, g.f, map[string]string{"word-attached/read": runReachReferenced})
}

// Without a drafting or iteration consent the scope never takes a lease,
// commits, checkpoints or proposes: the Document and its lease are untouched.
func TestRunScopeDocumentReach_WritesAreRefusedAndTheLeaseUntouched(t *testing.T) {
	g := newKindReachRig(t)
	g.wordDoc(t, "word-attached", g.f.a.HumanUserID, true)
	g.start(t, "word-attached")
	for _, op := range []string{"lease", "commit", "checkpoint", "propose", "renew", "release"} {
		got := g.docAuthority(t, "word-attached", op, map[string]any{"owner": "synthetic-engine"})
		missing := g.docAuthority(t, "word-nowhere", op, map[string]any{"owner": "synthetic-engine"})
		sameRefusal(t, op+" on an attached Document", got, missing)
	}
	if writer := g.docLease(t, "word-attached"); writer != "" {
		t.Fatalf("the scope took the lease: %q", writer)
	}
	var head int
	if err := g.f.rig.db.QueryRow(`SELECT head_rev FROM documents WHERE page_id='word-attached'`).Scan(&head); err != nil || head != 3 {
		t.Fatalf("head %d %v, want 3", head, err)
	}
	if rows := kindAuditRows(t, g.f); len(rows) != 0 {
		t.Fatalf("a refused write was recorded: %+v", rows)
	}
}

// Standing and folder reach read a shared Document the human may read, count
// against run_read_limit and never write.
func TestRunScopeDocumentReach_StandingAndFolderReadsAreCappedAndNeverWrite(t *testing.T) {
	g := newKindReachRig(t)
	g.fakeDocs(t)
	g.wordDoc(t, "word-standing", g.f.rig.adminID, false)
	g.wordDoc(t, "word-filed", g.f.rig.adminID, false)
	g.wordDoc(t, "word-third", g.f.rig.adminID, false)
	for _, p := range []string{"word-standing", "word-filed", "word-third"} {
		mustExec(t, g.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'read',?)`, p, g.f.a.HumanUserID, g.f.rig.adminID)
	}
	mustExec(t, g.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('word-standing','agent',?,'write',?),('word-third','agent',?,'read',?)`, g.f.a.AgentID, g.f.rig.adminID, g.f.a.AgentID, g.f.rig.adminID)
	g.folder(t, "word-folder", g.f.a.CrewID, "read", "word-filed")
	mustExec(t, g.f.rig.db, `UPDATE workspace_artifact_creation_policy SET run_read_limit=2 WHERE workspace_id=?`, g.f.a.WorkspaceID)
	g.start(t)
	wantCode(t, g.docAuthority(t, "word-standing", "state", map[string]any{}), 200, "standing read")
	wantCode(t, g.docAuthority(t, "word-filed", "state", map[string]any{}), 200, "folder read")
	if w := g.docAuthority(t, "word-third", "state", map[string]any{}); w.Code != 429 {
		t.Fatalf("a read past run_read_limit: %d %s", w.Code, w.Body)
	}
	for _, op := range []string{"lease", "commit", "propose"} {
		if w := g.docAuthority(t, "word-standing", op, map[string]any{"owner": "synthetic-engine"}); w.Code == 200 {
			t.Fatalf("standing reach %s: %s", op, w.Body)
		}
	}
	if writer := g.docLease(t, "word-standing"); writer != "" {
		t.Fatalf("standing reach took the lease: %q", writer)
	}
	wantKindAudit(t, g.f, map[string]string{"word-standing/read": runReachStanding, "word-filed/read": runReachFolder})
}

// The human reads a shared Document through a user grant; once it is retired
// the human reads it only through the ADMIN role, which never passes on.
func TestRunScopeDocumentReach_HumanLosingAccessAndRoleOnlyReach(t *testing.T) {
	g := newKindReachRig(t, humanAdmin)
	g.wordDoc(t, "word-shared", g.f.rig.adminID, false)
	mustExec(t, g.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('word-shared','user',?,'read',?)`, g.f.a.HumanUserID, g.f.rig.adminID)
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id='word-shared'`)
	g.start(t, "word-shared")
	wantCode(t, g.docAuthority(t, "word-shared", "state", map[string]any{}), 200, "baseline")
	// Without the grant the human still sees the shared Document, but only
	// through the ADMIN role.
	mustExec(t, g.f.rig.db, `UPDATE page_grants SET retired_at='2026-11-14T00:00:00Z' WHERE page_id='word-shared'`)
	sameRefusal(t, "a Document the human reads only through the ADMIN role", g.docAuthority(t, "word-shared", "state", map[string]any{}), g.docAuthority(t, "word-nowhere", "state", map[string]any{}))
}

func TestRunScopeDocumentReach_FlagOffUnchanged(t *testing.T) {
	g := newKindReachRig(t)
	g.fakeDocs(t)
	g.wordDoc(t, "word-attached", g.f.a.HumanUserID, true)
	g.start(t, "word-attached")
	mustExec(t, g.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='ref-flag-on'`)
	sameRefusal(t, "flag off, an attached Document without a consent", g.docAuthority(t, "word-attached", "state", map[string]any{}), g.docAuthority(t, "word-nowhere", "state", map[string]any{}))
	if w := g.docView("word-attached"); w.Code != 400 {
		t.Fatalf("flag off, a view naming a page: %d %s", w.Code, w.Body)
	}
	if rows := kindAuditRows(t, g.f); len(rows) != 0 {
		t.Fatalf("flag off wrote agent-run audit records: %+v", rows)
	}
}

// The agent's own path, end to end: the real CLI binary (`crewship doc draft
// view --page`) through the real run sidecar to the server. An attached
// Document is read; a Document the run does not reach gets exactly the
// answer a missing one gets, through the sidecar; with the flag off a named
// page is refused.
func TestRunScopeDocumentReach_DraftViewBinaryThroughSidecar(t *testing.T) {
	g := newKindReachRig(t)
	g.fakeDocs(t)
	g.wordDoc(t, "word-attached", g.f.a.HumanUserID, true)
	g.wordDoc(t, "word-sibling", g.f.a.HumanUserID, true)
	g.start(t, "word-attached")
	host := httptest.NewServer(g.router)
	t.Cleanup(host.Close)

	const master = "synthetic-artifact-run-scope-master"
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	side := sidecar.NewServer(sidecar.ServerConfig{Addr: addr, Logger: newTestLogger(), IPC: &sidecar.IPCConfig{
		BaseURL: host.URL, Token: g.crewTok, WorkspaceID: g.f.a.WorkspaceID, CrewID: g.f.a.CrewID,
		AgentID: g.f.a.AgentID, AgentSlug: "principal-agent", AgentToken: internaltoken.DeriveAgentToken(master, g.f.a.WorkspaceID, g.f.a.AgentID),
		AgentRunKey: internaltoken.DeriveAgentRunKey(master, g.f.a.WorkspaceID, g.f.a.CrewID), RunID: g.f.a.RunID, RunChatID: g.f.a.ChatID,
	}})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() { _ = side.Start(ctx) }()
	<-side.Ready()

	// The sidecar's own answer for a page the run does not reach is the
	// answer for a missing page, status and body.
	direct := func(page string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), "GET", "http://"+addr+"/api/v1/documents/draft?page_id="+page, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+g.runToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	if code, body := direct("word-attached"); code != 200 || !strings.Contains(body, `"mode":"read"`) {
		t.Fatalf("attached through the sidecar: %d %s", code, body)
	}
	siblingCode, siblingBody := direct("word-sibling")
	missingCode, missingBody := direct("word-nowhere")
	if siblingCode < 400 || siblingCode != missingCode || siblingBody != missingBody {
		t.Fatalf("an unreached Document through the sidecar: %d %q, missing: %d %q", siblingCode, siblingBody, missingCode, missingBody)
	}

	dir := t.TempDir()
	binary := filepath.Join(dir, "crewship")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/crewship")
	build.Dir = "../.."
	build.Env = nestedGoBuildEnv(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual binary: %v %s", err, out)
	}
	cli := func(args ...string) (string, error) {
		t.Helper()
		command := exec.CommandContext(t.Context(), binary, args...)
		command.Dir = dir
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CREWSHIP_") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_AGENT_TOKEN="+g.runToken, "CREWSHIP_SIDECAR_URL=http://"+addr, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		out, err := command.CombinedOutput()
		return string(out), err
	}
	out, err := cli("doc", "draft", "view", "--page", "word-attached")
	var v DocumentDraftView
	if err != nil || json.Unmarshal([]byte(out), &v) != nil || v.PageID != "word-attached" || v.Mode != "read" || len(v.Targets) != 1 {
		t.Fatalf("attached view through the binary: %v %s", err, out)
	}
	sibling, siblingErr := cli("doc", "draft", "view", "--page", "word-sibling")
	missing, missingErr := cli("doc", "draft", "view", "--page", "word-nowhere")
	if siblingErr == nil || missingErr == nil || sibling != missing {
		t.Fatalf("an unreached Document through the binary: %v %q, missing: %v %q", siblingErr, sibling, missingErr, missing)
	}
	mustExec(t, g.f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='ref-flag-on'`)
	if out, err := cli("doc", "draft", "view", "--page", "word-attached"); err == nil || !strings.Contains(out, "HTTP400") {
		t.Fatalf("flag off, a named page through the binary: %v %s", err, out)
	}
	if code, _ := direct("word-attached"); code != 400 {
		t.Fatalf("flag off, a named page through the sidecar: %d", code)
	}
	wantKindAudit(t, g.f, map[string]string{})
}

// Reviewer finding 1: scope reach serves only the Node read behind the draft
// view. A state load for an export, comments, checkpoints or a pinned
// checkpoint, or one that names no operation, is refused exactly like a
// missing Document, and nothing is recorded for it.
func TestRunScopeDocumentReach_OnlyTheDraftViewReadLoadsState(t *testing.T) {
	g := newKindReachRig(t)
	g.wordDoc(t, "word-attached", g.f.a.HumanUserID, true)
	g.start(t, "word-attached")
	for _, purpose := range []string{"", "export", "comments", "checkpoints", "checkpoint-read", "patch", "propose", "Read", "read,export"} {
		sameRefusal(t, "a state load for "+purpose, g.docAuthorityFor(t, "word-attached", "state", purpose, map[string]any{}), g.docAuthorityFor(t, "word-nowhere", "state", purpose, map[string]any{}))
	}
	for _, op := range []string{"comments", "checkpoints", "checkpoint-read"} {
		sameRefusal(t, op+" through the scope", g.docAuthorityFor(t, "word-attached", op, op, map[string]any{}), g.docAuthorityFor(t, "word-nowhere", op, op, map[string]any{}))
	}
	if rows := kindAuditRows(t, g.f); len(rows) != 0 {
		t.Fatalf("a refused state load was recorded: %+v", rows)
	}
	wantCode(t, g.docAuthorityFor(t, "word-attached", "state", "read", map[string]any{}), 200, "the draft view's read")
	wantKindAudit(t, g.f, map[string]string{"word-attached/read": runReachReferenced})
}

// Reviewer finding 2: when the run holds a consent for the page it names, the
// consent serves the view, with its block limit, and the audit names the
// consent; another attached page without a consent is served by the scope
// and audited as referenced.
func TestRunScopeDocumentReach_ConsentKeepsItsBlocksInTheNamedView(t *testing.T) {
	g := newKindReachRig(t)
	g.wordDoc(t, "word-mandated", g.f.a.HumanUserID, true)
	g.wordDoc(t, "word-other", g.f.a.HumanUserID, true)
	mustExec(t, g.f.rig.db, `UPDATE documents SET schema_version=?,head_rev=0,snapshot_rev=0,generation=0 WHERE page_id='word-mandated'`, documents.FormattedSchema)
	g.start(t, "word-mandated", "word-other")
	intent := documents.IterationIntent{SubmissionID: "followup", PageID: "word-mandated", Schema: documents.FormattedSchema, Blocks: "intro"}
	if err := g.f.pages.persistDocumentIterationIntent(httptest.NewRequest("POST", "/", nil), g.f.receipt, g.f.a.MessageID, intent); err != nil {
		t.Fatalf("persist iteration: %v", err)
	}
	if err := bindIterationFixture(t, g.f, intent); err != nil {
		t.Fatalf("bind iteration: %v", err)
	}
	hash := strings.Repeat("a", 64)
	g.router.pages.documentCall = func(_ context.Context, _, _, page, op string, _ any) ([]byte, int, error) {
		if op != "read" {
			return nil, 500, fmt.Errorf("unexpected Docs call %s", op)
		}
		schema, rev, generation := documents.FormattedSchema, 0, 0
		if page == "word-other" {
			schema, rev, generation = 5, 3, 1
		}
		return []byte(fmt.Sprintf(`{"schema":%d,"rev":%d,"generation":%d,"epoch":"e1","outline":[{"id":"intro","type":"paragraph","hash":%q,"text":"Intro"},{"id":"body","type":"paragraph","hash":%q,"text":"Private body"}]}`, schema, rev, generation, hash, hash)), 200, nil
	}
	view := func(page string) DocumentDraftView {
		t.Helper()
		w := g.docView(page)
		wantCode(t, w, 200, "view of "+page)
		var v DocumentDraftView
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := view("word-mandated"); v.Mode != "propose" || len(v.Targets) != 1 || v.Targets[0].ID != "intro" {
		t.Fatalf("the named mandated page lost its block limit: %+v", v)
	}
	if v := view("word-other"); v.Mode != "read" || len(v.Targets) != 2 {
		t.Fatalf("the attached page without a consent: %+v", v)
	}
	wantCode(t, g.docAuthority(t, "word-mandated", "state", map[string]any{}), 200, "the mandated page's read")
	wantCode(t, g.docAuthority(t, "word-other", "state", map[string]any{}), 200, "the attached page's read")
	wantKindAudit(t, g.f, map[string]string{"word-mandated/read": runReachConsented, "word-other/read": runReachReferenced})
	// Once the mandate lapses its page is still never read through the scope,
	// so the block limit cannot be bypassed: it is refused like a missing one.
	mustExec(t, g.f.rig.db, `UPDATE workspace_artifact_creation_policy SET version=version+1 WHERE workspace_id=?`, g.f.a.WorkspaceID)
	if v := view("word-other"); v.Mode != "read" {
		t.Fatalf("the scope stopped reading: %+v", v)
	}
	sameRefusal(t, "the page of a lapsed mandate", g.docView("word-mandated"), g.docView("word-nowhere"))
	sameRefusal(t, "the state of a lapsed mandate's page", g.docAuthority(t, "word-mandated", "state", map[string]any{}), g.docAuthority(t, "word-nowhere", "state", map[string]any{}))
}

// Reviewer finding 3: Mooring output is owner-only. An ADMIN cannot attach
// another member's Mooring document, private or shared: the reference is
// refused exactly like a missing page's, so the run never reaches it (the
// run read's own owner check stays behind this as defence in depth).
func TestRunScopeMooringReach_AnotherOwnersDocumentIsRefusedLikeMissing(t *testing.T) {
	g := newKindReachRig(t, humanAdmin)
	g.mooringDoc(t, "mooring-private", g.f.rig.adminID)
	g.mooringDoc(t, "mooring-shared", g.f.rig.adminID)
	mustExec(t, g.f.rig.db, `UPDATE pages SET private_audience=0 WHERE id='mooring-shared'`)
	missing := g.f.pages.RecordArtifactRefs(t.Context(), g.f.receipt, g.f.a.MessageID, []string{"mooring-nowhere"})
	if missing == nil {
		t.Fatal("a missing page was attached")
	}
	for _, page := range []string{"mooring-private", "mooring-shared"} {
		err := g.f.pages.RecordArtifactRefs(t.Context(), g.f.receipt, g.f.a.MessageID, []string{page})
		if err == nil || err.Error() != missing.Error() {
			t.Fatalf("an ADMIN attached another owner's Mooring document %s: %v, missing: %v", page, err, missing)
		}
	}
	g.start(t)
	sameRefusal(t, "another owner's Mooring document", g.mooringCall(mooringScopeRead("mooring-shared")), g.mooringCall(mooringScopeRead("mooring-nowhere")))
	if rows := kindAuditRows(t, g.f); len(rows) != 0 {
		t.Fatalf("a refused Mooring read was recorded: %+v", rows)
	}
}
