package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/slides"
	"github.com/crewship-ai/crewship/internal/testutil"
)

type gridcraftFixtureEngine struct{}

func (gridcraftFixtureEngine) Identity() string { return "fixture-engine@1" }
func (gridcraftFixtureEngine) Prepare(_ context.Context, b appengine.Binding, s appengine.State, c appengine.Command) (appengine.Candidate, error) {
	return appengine.NewCandidate(b, c.Snapshot, []string{"*"}), nil
}
func TestGridCraftHumanDefaultOffImportReadRetryAndPrivacy(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	wmSeedPage(t, f.db, "ws-old", "workbook-original", "workbook-test", "Workbook", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='workbook-original'`)
	base := "/api/v1/pages/workbook-test/workbook"
	wantCode(t, f.do(f.boss, "GET", base+"?workspace_id=ws-old", ""), 404, "default off")
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	req := appengine.Request{MutationID: "import-1", MandateID: "human-import", Command: appengine.Command{Kind: "import", Snapshot: json.RawMessage(`{"sheets":[{}]}`)}}
	raw, _ := json.Marshal(req)
	call := func(token, method, path, body string) {
		t.Helper()
		wantCode(t, f.do(token, method, path+"?workspace_id=ws-old", body), 200, "authorized workbook")
	}
	call(f.boss, "POST", base+"/operations", string(raw))
	call(f.boss, "POST", base+"/operations", string(raw))
	call(f.boss, "GET", base, "")
	wantCode(t, f.do(f.wsAdmin, "GET", base+"?workspace_id=ws-old", ""), 404, "private admin")
	wantCode(t, f.do(f.wsAdmin, "POST", base+"/operations?workspace_id=ws-old", string(raw)), 404, "private write")
	var n int
	if err := f.db.QueryRow(`SELECT count(*) FROM gridcraft_revisions`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("revisions=%d err=%v", n, err)
	}
	mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE token_hash=?`, sha256Hex(f.boss))
	wantCode(t, f.do(f.boss, "POST", base+"/operations?workspace_id=ws-old", string(raw)), 401, "revoked retry")
}

func TestGridCraftAgentRequiresRetainedMandateAndRevokesDuringCandidate(t *testing.T) {
	rig := pagesTransferSetupWithDB(t, gridcraftFullDB(t))
	f, _ := documentCreationAdmissionWithPrincipal(t, newArtifactPrincipalFixtureWithRig(t, rig))
	f.rig.db.SetMaxOpenConns(1)
	wmSeedPage(t, f.rig.db, f.a.WorkspaceID, "grid-agent-page", "grid-agent", "Workbook", f.a.HumanUserID, "")
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id='grid-agent-page'`)
	f.pages.artifactMasterToken = "synthetic-workbook-master"
	store := appengine.New(f.rig.db, appengine.Options{Enabled: true})
	engine := &gridcraftBlockingEngine{}
	adapter := artifacts.NewGridCraftAdapter(store, engine, true)
	h := &gridcraftHTTP{pages: f.pages, store: store, engine: engine, operations: adapter}
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "workbook-owner-cli")
	humanRequest := func(method, path, body string) *http.Request {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+humanToken)
		ctx := context.WithValue(r.Context(), ctxUser, &AuthUser{ID: f.a.HumanUserID})
		ctx = context.WithValue(ctx, ctxWorkspaceID, f.a.WorkspaceID)
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		r = r.WithContext(ctx)
		r.SetPathValue("slug", "@grid-agent-page")
		return r
	}
	initial := httptest.NewRecorder()
	h.importFile(initial, humanRequest("POST", "/", `{"mutation_id":"import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`))
	wantCode(t, initial, 200, "import")
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	packet := `{"action":"apply","artifact_id":"grid-agent-page","mandate_id":"cell-edit","request":{"mutation_id":"agent-edit","mandate_id":"cell-edit","base_rev":1,"generation":1,"command":{"kind":"cell.set","sheet":"Sheet1","cell":"B2","input":"42"}}}`
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.agent(w, documentDraftRequest(f, token, "POST", body))
		return w
	}
	wantCode(t, call(packet), 404, "no implicit run-wide workbook grant")
	grant := httptest.NewRecorder()
	grantBody := fmt.Sprintf(`{"mandate_id":"cell-edit","run_id":%q,"base_rev":1,"generation":1,"targets":["Sheet1!B2"]}`, f.a.RunID)
	h.mandate(grant, humanRequest("POST", "/", grantBody))
	wantCode(t, grant, 200, "retained human intent")
	wantCode(t, call(strings.Replace(packet, `"B2"`, `"C9"`, 1)), 403, "target bound")
	applied := call(packet)
	wantCode(t, applied, 200, "signed current run")
	wantCode(t, call(packet), 200, "authorized retry")
	wantCode(t, call(strings.Replace(packet, `"mutation_id":"agent-edit",`, `"mutation_id":"agent-session","session_id":"s1",`, 1)), 400, "agent cannot claim a client session")
	// A different Page does not inherit this grant, even for the same human/run.
	wantCode(t, call(strings.Replace(packet, "grid-agent-page", "other-page", 1)), 404, "Page bound")
	secondGrant := httptest.NewRecorder()
	h.mandate(secondGrant, humanRequest("POST", "/", strings.Replace(strings.Replace(grantBody, "cell-edit", "cell-edit-2", 1), `"base_rev":1`, `"base_rev":2`, 1)))
	wantCode(t, secondGrant, 200, "second retained intent")
	engine.before = func() {
		mustExec(t, f.rig.db, `UPDATE gridcraft_intents SET revoked_at='2026-01-01T00:00:00Z' WHERE mandate_id='cell-edit-2'`)
	}
	secondPacket := strings.ReplaceAll(strings.Replace(packet, `"base_rev":1`, `"base_rev":2`, 1), "cell-edit", "cell-edit-2")
	secondPacket = strings.Replace(secondPacket, "agent-edit", "agent-edit-2", 1)
	wantCode(t, call(secondPacket), 404, "revoked during candidate")
	var rev int
	if err := f.rig.db.QueryRow(`SELECT head_rev FROM gridcraft_heads WHERE page_id='grid-agent-page'`).Scan(&rev); err != nil || rev != 2 {
		t.Fatalf("revoked candidate committed: %d %v", rev, err)
	}
	engine.before = nil
	// An exact retry is refused after revocation without running the engine.
	mustExec(t, f.rig.db, `UPDATE gridcraft_intents SET revoked_at='2026-01-01T00:00:00Z' WHERE mandate_id='cell-edit'`)
	wantCode(t, call(packet), 404, "revoked replay")
	read := `{"action":"read","artifact_id":"grid-agent-page","mandate_id":"cell-edit"}`
	wantCode(t, call(read), 404, "revoked read")
	thirdGrant := httptest.NewRecorder()
	thirdBody := strings.Replace(strings.Replace(grantBody, "cell-edit", "policy-bound", 1), `"base_rev":1`, `"base_rev":2`, 1)
	h.mandate(thirdGrant, humanRequest("POST", "/", thirdBody))
	wantCode(t, thirdGrant, 200, "policy-bound mandate")
	mustExec(t, f.rig.db, `UPDATE workspace_artifact_creation_policy SET version=version+1 WHERE workspace_id=?`, f.a.WorkspaceID)
	engine.before = func() { t.Fatal("old policy mandate restarted a candidate") }
	thirdPacket := strings.ReplaceAll(strings.Replace(packet, `"base_rev":1`, `"base_rev":2`, 1), "cell-edit", "policy-bound")
	wantCode(t, call(thirdPacket), 404, "policy regrant cannot revive old intent")

}

type gridcraftBlockingEngine struct{ before func() }

func (*gridcraftBlockingEngine) Identity() string { return "authority-fixture@1" }
func (e *gridcraftBlockingEngine) Prepare(_ context.Context, b appengine.Binding, _ appengine.State, c appengine.Command) (appengine.Candidate, error) {
	if e.before != nil {
		e.before()
	}
	snapshot := c.Snapshot
	effects := []string{"*"}
	if c.Kind == "cell.set" {
		snapshot = json.RawMessage(`{"sheets":[{"B2":"42"}]}`)
		effects = []string{"Sheet1!B2"}
	}
	return appengine.NewCandidate(b, snapshot, effects), nil
}

func registerGridCraftTestRoutes(r *Router) {
	r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
}

func TestGridCraftOwnerReadAuditsOtherPrivateWorkbook(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	wmSeedPage(t, f.db, "ws-old", "workbook-admin-private", "admin-workbook", "Private workbook", "wsadmin", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='workbook-admin-private'`)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	body := `{"mutation_id":"private-import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`
	base := "/api/v1/pages/admin-workbook/workbook"
	wantCode(t, f.do(f.wsAdmin, "POST", base+"/operations?workspace_id=ws-old", body), 200, "human custodian import")
	wantCode(t, f.do(f.boss, "GET", base+"?workspace_id=ws-old", ""), 200, "workspace owner audited read")
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND entity_id='workbook-admin-private'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit=%d err=%v", count, err)
	}
	mustExec(t, f.db, `CREATE TRIGGER deny_workbook_audit BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	response := f.do(f.boss, "GET", base+"?workspace_id=ws-old", "")
	if response.Code == 200 || strings.Contains(response.Body.String(), `"sheets"`) {
		t.Fatalf("private bytes released without audit: %d %s", response.Code, response.Body.String())
	}
}

func gridcraftInstanceFixture(t *testing.T) *instanceFixture {
	t.Helper()
	f := newInstanceFixtureWithDB(t, gridcraftFullDB(t).DB)
	f.db.SetMaxOpenConns(1)
	return f
}

func TestGridCraftFixtureRetainsDurabilityAfterConnectionReplacement(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	// A connection-local PRAGMA must not be mistaken for the pool's DSN policy.
	f.db.SetMaxIdleConns(0)
	wmSeedPage(t, f.db, "ws-old", "reopened-workbook", "reopened-workbook", "Workbook", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='reopened-workbook'`)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	body := `{"mutation_id":"reopened-import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`
	wantCode(t, f.do(f.boss, "POST", "/api/v1/pages/reopened-workbook/workbook/operations?workspace_id=ws-old", body), 200, "FULL after replacement")
}

// The standard lightweight fixture intentionally uses NORMAL in its DSN.
// Workbook receipts require production FULL semantics on every new connection.
func gridcraftFullDB(t *testing.T) *database.DB {
	t.Helper()
	resetCapabilityCache()
	data, err := os.ReadFile(testutil.MigratedTemplatePath(t))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "workbook.db")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	drainBackgroundWork(t)
	return db
}

func TestGridCraftNormalPoolCannotClaimNodeDurability(t *testing.T) {
	f := newInstanceFixture(t)
	f.db.SetMaxIdleConns(0) // every checkout uses the fixture's NORMAL DSN
	wmSeedPage(t, f.db, "ws-old", "nondurable-workbook", "nondurable-workbook", "Workbook", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='nondurable-workbook'`)
	engine := &gridcraftBlockingEngine{before: func() { t.Fatal("non-durable admission started engine work") }}
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, engine)
	body := `{"mutation_id":"nondurable-import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`
	wantCode(t, f.do(f.boss, "POST", "/api/v1/pages/nondurable-workbook/workbook/operations?workspace_id=ws-old", body), 400, "NORMAL storage refused")
	for _, table := range []string{"gridcraft_heads", "gridcraft_operations", "gridcraft_audit", "gridcraft_outbox"} {
		var count int
		if err := f.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("non-durable %s changed: %d %v", table, count, err)
		}
	}
}

func TestGridCraftAdmitsOrdinaryHumanRunWithoutDocsIntent(t *testing.T) {
	for _, scenario := range []string{"ordinary", "no-human-receipt", "terminal", "message-author", "stale-chat"} {
		t.Run(scenario, func(t *testing.T) {
			rig := pagesTransferSetupWithDB(t, gridcraftFullDB(t))
			f := newArtifactPrincipalFixtureWithRig(t, rig)
			mustExec(t, rig.db, `UPDATE chats SET visibility='private' WHERE id=?`, f.a.ChatID)
			receipt, err := chataudience.CaptureTrusted(t.Context(), rig.db, f.a.ChatID, f.a.HumanUserID, nil)
			if err != nil || receipt == nil {
				t.Fatalf("receipt: %v", err)
			}
			mustExec(t, rig.db, `INSERT INTO workspace_artifact_creation_policy(workspace_id,enabled,version,updated_at) VALUES(?,1,1,'2026-10-17T00:00:00Z')`, f.a.WorkspaceID)
			mustExec(t, rig.db, `INSERT INTO workspace_artifact_creation_crews(workspace_id,crew_id) VALUES(?,?)`, f.a.WorkspaceID, f.a.CrewID)
			writer := journal.NewWriter(rig.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
			t.Cleanup(func() { _ = writer.Close() })
			internal := NewInternalHandler(rig.db, "synthetic-host", newTestLogger())
			internal.SetJournal(writer)
			body := map[string]any{"id": f.a.RunID, "agent_id": f.a.AgentID, "workspace_id": f.a.WorkspaceID, "chat_id": f.a.ChatID, "trigger_type": "USER", "human_authority": receipt, "trigger_message_id": f.a.MessageID}
			if scenario == "no-human-receipt" {
				delete(body, "human_authority")
				delete(body, "trigger_message_id")
				body["metadata"] = map[string]any{"human_authority": receipt, "trigger_message_id": f.a.MessageID}
			}
			if scenario == "message-author" {
				body["trigger_message_id"] = "principal-other-human-message"
			}
			raw, _ := json.Marshal(body)
			started := httptest.NewRecorder()
			internal.CreateRun(started, httptest.NewRequest("POST", "/api/v1/internal/runs", bytes.NewReader(raw)))
			wantCode(t, started, 201, "ordinary host run start")
			var principals int
			if err := rig.db.QueryRow(`SELECT count(*) FROM artifact_run_principals`).Scan(&principals); err != nil || principals != 0 {
				t.Fatalf("ordinary run unexpectedly requires Docs binding: %d %v", principals, err)
			}
			wmSeedPage(t, rig.db, f.a.WorkspaceID, "ordinary-workbook", "ordinary-workbook", "Workbook", f.a.HumanUserID, "")
			mustExec(t, rig.db, `UPDATE pages SET private_audience=1 WHERE id='ordinary-workbook'`)
			f.pages.artifactMasterToken = "synthetic-ordinary-workbook-master"
			store := appengine.New(rig.db, appengine.Options{Enabled: true})
			engine := &gridcraftBlockingEngine{}
			h := &gridcraftHTTP{pages: f.pages, store: store, engine: engine, operations: artifacts.NewGridCraftAdapter(store, engine, true)}
			credential := mintTokenFor(t, rig.db, f.a.HumanUserID, "ordinary-workbook-cli")
			human := func(body string) *http.Request {
				request := httptest.NewRequest("POST", "/", strings.NewReader(body))
				request.Header.Set("Authorization", "Bearer "+credential)
				ctx := context.WithValue(request.Context(), ctxUser, &AuthUser{ID: f.a.HumanUserID})
				ctx = context.WithValue(ctx, ctxWorkspaceID, f.a.WorkspaceID)
				ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
				request = request.WithContext(ctx)
				request.SetPathValue("slug", "@ordinary-workbook")
				return request
			}
			imported := httptest.NewRecorder()
			h.importFile(imported, human(`{"mutation_id":"ordinary-import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`))
			wantCode(t, imported, 200, "ordinary workbook import")
			switch scenario {
			case "terminal":
				_, err := writer.EmitSync(t.Context(), journal.Entry{ID: "ordinary-run-completed", WorkspaceID: f.a.WorkspaceID, AgentID: f.a.AgentID, CrewID: f.a.CrewID, Type: journal.EntryRunCompleted, TraceID: f.a.RunID, ActorType: journal.ActorSystem, Summary: "done"})
				if err != nil {
					t.Fatal(err)
				}

			case "stale-chat":
				mustExec(t, rig.db, `UPDATE chats SET authority_revision=authority_revision+1 WHERE id=?`, f.a.ChatID)
			}
			grant := httptest.NewRecorder()
			h.mandate(grant, human(fmt.Sprintf(`{"mandate_id":"ordinary-cell","run_id":%q,"base_rev":1,"generation":1,"targets":["Sheet1!B2"]}`, f.a.RunID)))
			if scenario != "ordinary" {
				wantCode(t, grant, 404, "invalid ordinary authority refused")
				for _, table := range []string{"artifact_run_principals", "gridcraft_intents"} {
					var n int
					if err := rig.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
						t.Fatalf("rejected admission persisted %s: %d %v", table, n, err)
					}
				}
				return
			}
			wantCode(t, grant, 200, "explicit ordinary human mandate")
			token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
			applied := httptest.NewRecorder()
			h.agent(applied, documentDraftRequest(f, token, "POST", `{"action":"apply","artifact_id":"ordinary-workbook","mandate_id":"ordinary-cell","request":{"mutation_id":"ordinary-agent-edit","mandate_id":"ordinary-cell","base_rev":1,"generation":1,"command":{"kind":"cell.set","sheet":"Sheet1","cell":"B2","input":"42"}}}`))
			wantCode(t, applied, 200, "ordinary signed agent operation")
			var docsIntents int
			if err := rig.db.QueryRow(`SELECT count(*) FROM document_creation_consents`).Scan(&docsIntents); err != nil || docsIntents != 0 {
				t.Fatalf("workbook created Docs consent: %d %v", docsIntents, err)
			}
		})
	}
}

// Initializing another format must not hide committed workbook data, even when
// the workbook routes are subsequently disabled.
func TestGridCraftWorkbookPreventsDocumentAndSlidesInitialization(t *testing.T) {
	for _, kind := range []string{"document", "slides"} {
		t.Run(kind, func(t *testing.T) {
			f := gridcraftInstanceFixture(t)
			wmSeedPage(t, f.db, "ws-old", "workbook-exclusive", "workbook-exclusive", "Workbook", "boss", "")
			mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='workbook-exclusive'`)
			f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
			base := "/api/v1/pages/workbook-exclusive/workbook"
			body := `{"mutation_id":"import-exclusive","mandate_id":"human-import","command":{"kind":"import","snapshot":{"sheets":[{}]}}}`
			wantCode(t, f.do(f.boss, "POST", base+"/operations?workspace_id=ws-old", body), 200, "import")
			if kind == "slides" {
				f.r.RegisterSlidesHuman(true)
				init := humanSlidesJSON(t, slides.InitializeRequest{PageID: "workbook-exclusive", SchemaVersion: 1, MutationID: "other-format", Deck: humanSlidesDeck(t, "workbook-exclusive")})
				wantCode(t, f.do(f.boss, "POST", "/api/v1/pages/workbook-exclusive/slides/initialize?workspace_id=ws-old", init), 404, "workbook excludes slides")
			} else {
				authority, err := f.r.DocumentAuthority(t.Context(), strings.Repeat("s", 64))
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest("POST", "/internal/docs/v1/workbook-exclusive/initialize?workspace_id=ws-old", strings.NewReader(`{"id":"document-init","fingerprint":"document-init","schema":3,"snapshot":"c25hcHNob3Q=","blocks":[]}`))
				req.Header.Set("Authorization", "Bearer "+f.boss)
				req.Header.Set("X-Crewship-Docs-Service", strings.Repeat("s", 64))
				w := httptest.NewRecorder()
				authority.ServeHTTP(w, req)
				wantCode(t, w, 403, "workbook excludes document")
			}
			wantCode(t, f.do(f.boss, "GET", base+"?workspace_id=ws-old", ""), 200, "workbook remains readable")
		})
	}
}

func TestGridCraftHumanCreatesEmptyPrivateWorkbookPage(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	create := func(token, body string) *httptest.ResponseRecorder {
		t.Helper()
		return f.do(token, "POST", "/api/v1/workbooks?workspace_id=ws-old", body)
	}
	body := `{"slug":"pilot-book","name":"Pilot book"}`
	wantCode(t, create(f.boss, body), 404, "default off")
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	first := create(f.boss, body)
	wantCode(t, first, 201, "create private workbook page")
	var created struct {
		PageID string `json:"page_id"`
		Slug   string `json:"slug"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil || created.PageID == "" || created.Slug != "pilot-book" {
		t.Fatalf("created=%+v err=%v body=%s", created, err, first.Body.String())
	}
	var owner string
	var private int
	if err := f.db.QueryRow(`SELECT owner_user_id,private_audience FROM pages WHERE id=? AND folder_id IS NULL AND owner_crew_id IS NULL`, created.PageID).Scan(&owner, &private); err != nil || owner != "boss" || private != 1 {
		t.Fatalf("owner=%q private=%d err=%v", owner, private, err)
	}
	retry := create(f.boss, body)
	wantCode(t, retry, 200, "identical retry returns the same empty page")
	if !strings.Contains(retry.Body.String(), created.PageID) {
		t.Fatalf("retry body=%s", retry.Body.String())
	}
	wantCode(t, create(f.wsAdmin, body), 409, "another user cannot claim the slug")
	wantCode(t, create(f.boss, `{"slug":"pilot-book","name":"Renamed"}`), 409, "different name is a different request")
	wantCode(t, create(f.boss, `{"slug":"Bad Slug","name":"x"}`), 400, "invalid slug")
	wantCode(t, create(f.boss, `{"slug":"ok-slug","name":"x","extra":1}`), 400, "unknown field")
	wmSeedPage(t, f.db, "ws-old", "shared-page", "shared-page", "Shared", "boss", "")
	wantCode(t, create(f.boss, `{"slug":"shared-page","name":"Shared"}`), 409, "existing non-private page is never converted")
	importBody := `{"mutation_id":"import-1","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`
	wantCode(t, f.do(f.boss, "POST", "/api/v1/pages/pilot-book/workbook/operations?workspace_id=ws-old", importBody), 200, "created page accepts import")
	wantCode(t, create(f.boss, body), 409, "a page with a workbook is no longer an empty create result")
	detail := f.do(f.boss, "GET", "/api/v1/pages/pilot-book?workspace_id=ws-old", "")
	wantCode(t, detail, 200, "author reads the workbook page")
	if !strings.Contains(detail.Body.String(), `"has_workbook":true`) {
		t.Fatalf("page detail does not classify the workbook: %s", detail.Body.String())
	}
	if list := f.do(f.boss, "GET", "/api/v1/pages?workspace_id=ws-old", ""); !strings.Contains(list.Body.String(), `"has_workbook":true`) {
		t.Fatalf("page list does not classify the workbook: %s", list.Body.String())
	}
	mustExec(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew-lookout','ws-old','Lookout','lookout')`)
	panels := `{"panels":[{"id":"p","schema":"status.v1","title":"t","owner":"crew/lookout","producer":"script/watch.sh","sla_seconds":30}]}`
	wantCode(t, f.do(f.boss, "PATCH", "/api/v1/pages/pilot-book?workspace_id=ws-old", panels), 409, "panels cannot hide a workbook")
	wantCode(t, f.do(f.boss, "GET", "/api/v1/pages/pilot-book/workbook?workspace_id=ws-old", ""), 200, "workbook still readable")
	wantCode(t, f.do(f.wsAdmin, "GET", "/api/v1/pages/pilot-book/workbook?workspace_id=ws-old", ""), 404, "private to its author")
	listed := f.do(f.wsAdmin, "GET", "/api/v1/pages?workspace_id=ws-old", "")
	if strings.Contains(listed.Body.String(), "pilot-book") || strings.Contains(listed.Body.String(), created.PageID) {
		t.Fatalf("admin page list exposes the private workbook page: %s", listed.Body.String())
	}
	wantCode(t, create(f.boss, `{"slug":"long-name","name":"`+strings.Repeat("n", 201)+`"}`), 400, "name length bound")
	for i := 0; i < maxEmptyWorkbookPages; i++ {
		wantCode(t, create(f.boss, fmt.Sprintf(`{"slug":"empty-%d","name":"Empty"}`, i)), 201, "staged empty page")
	}
	wantCode(t, create(f.boss, `{"slug":"one-too-many","name":"Empty"}`), 409, "per-author empty page bound")
	wantCode(t, create(f.boss, `{"slug":"empty-0","name":"Empty"}`), 200, "retry within the bound stays idempotent")
	mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE token_hash=?`, sha256Hex(f.boss))
	wantCode(t, create(f.boss, `{"slug":"after-revoke","name":"x"}`), 401, "revoked credential")
}
