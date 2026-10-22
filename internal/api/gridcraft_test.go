package api

import (
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
	"github.com/crewship-ai/crewship/internal/database"
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
