package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

type gridcraftFixtureEngine struct{}

func (gridcraftFixtureEngine) Identity() string { return "fixture-engine@1" }
func (gridcraftFixtureEngine) Prepare(_ context.Context, b mooring.Binding, s mooring.State, c mooring.Command) (mooring.Candidate, error) {
	return mooring.NewCandidate(b, c.Snapshot, []string{"*"}), nil
}
func TestGridCraftHumanDefaultOffImportReadRetryAndPrivacy(t *testing.T) {
	f := newInstanceFixture(t)
	f.db.SetMaxOpenConns(1)
	mustExec(t, f.db, `PRAGMA synchronous=FULL`)
	wmSeedPage(t, f.db, "ws-old", "workbook-original", "workbook-test", "Workbook", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='workbook-original'`)
	base := "/api/v1/pages/workbook-test/workbook"
	wantCode(t, f.do(f.boss, "GET", base+"?workspace_id=ws-old", ""), 404, "default off")
	f.r.RegisterGridCraft(mooring.Options{Enabled: true}, gridcraftFixtureEngine{})
	req := mooring.Request{MutationID: "import-1", MandateID: "human-import", Command: mooring.Command{Kind: "import", Snapshot: json.RawMessage(`{"sheets":[{}]}`)}}
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
	f, _ := documentCreationAdmissionFixture(t)
	f.rig.db.SetMaxOpenConns(1)
	mustExec(t, f.rig.db, `PRAGMA synchronous=FULL`)
	wmSeedPage(t, f.rig.db, f.a.WorkspaceID, "grid-agent-page", "grid-agent", "Workbook", f.a.HumanUserID, "")
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id='grid-agent-page'`)
	f.pages.artifactMasterToken = "synthetic-workbook-master"
	store := mooring.New(f.rig.db, mooring.Options{Enabled: true})
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
}

type gridcraftBlockingEngine struct{ before func() }

func (*gridcraftBlockingEngine) Identity() string { return "authority-fixture@1" }
func (e *gridcraftBlockingEngine) Prepare(_ context.Context, b mooring.Binding, _ mooring.State, c mooring.Command) (mooring.Candidate, error) {
	if e.before != nil {
		e.before()
	}
	snapshot := c.Snapshot
	effects := []string{"*"}
	if c.Kind == "cell.set" {
		snapshot = json.RawMessage(`{"sheets":[{"B2":"42"}]}`)
		effects = []string{"Sheet1!B2"}
	}
	return mooring.NewCandidate(b, snapshot, effects), nil
}

func registerGridCraftTestRoutes(r *Router) {
	r.RegisterGridCraft(mooring.Options{Enabled: true}, gridcraftFixtureEngine{})
}
