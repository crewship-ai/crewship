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
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestMooringAgentRetainedRunMandateAndRevocation(t *testing.T) {
	rig := pagesTransferSetupWithDB(t, gridcraftFullDB(t))
	f, _ := documentCreationAdmissionWithPrincipal(t, newArtifactPrincipalFixtureWithRig(t, rig))
	f.rig.db.SetMaxOpenConns(1)
	wmSeedPage(t, f.rig.db, f.a.WorkspaceID, "mooring-agent-page", "mooring-agent", "Generic", f.a.HumanUserID, "")
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id='mooring-agent-page'`)
	mustExec(t, f.rig.db, `INSERT INTO mooring_documents(workspace_id,page_id,app_id,rev,generation,snapshot) VALUES(?,'mooring-agent-page','hello',1,1,'{"text":"initial"}')`, f.a.WorkspaceID)
	f.pages.artifactMasterToken = "synthetic-mooring-master"
	h := &mooringHTTP{workbook: &gridcraftHTTP{pages: f.pages}, storage: mooring.JSONAdapter{}}
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "mooring-owner-cli")
	humanRequest := func(body string) *http.Request {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+humanToken)
		ctx := context.WithValue(r.Context(), ctxUser, &AuthUser{ID: f.a.HumanUserID})
		ctx = context.WithValue(ctx, ctxWorkspaceID, f.a.WorkspaceID)
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		r = r.WithContext(ctx)
		r.SetPathValue("slug", "@mooring-agent-page")
		return r
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	packet := `{"action":"command","artifact_id":"mooring-agent-page","mandate_id":"set-text","request":{"mutation_id":"agent-one","base_rev":1,"generation":1,"command":"set","arguments":{"text":"agent confirmed"}}}`
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.agent(w, documentDraftRequest(f, token, "POST", body))
		return w
	}
	wantCode(t, call(packet), 404, "no implicit generic agent mandate")
	grant := httptest.NewRecorder()
	h.mandate(grant, humanRequest(fmt.Sprintf(`{"mandate_id":"set-text","run_id":%q,"base_rev":1,"generation":1,"command":"set"}`, f.a.RunID)))
	wantCode(t, grant, 200, "retained manifest command mandate")
	for i := 0; i < 2; i++ {
		w := call(packet)
		wantCode(t, w, 200, "agent commit retry")
		var receipt mooring.Receipt
		if json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Rev != 2 {
			t.Fatal(w.Body.String())
		}
	}
	read := call(`{"action":"read","artifact_id":"mooring-agent-page","mandate_id":"set-text"}`)
	wantCode(t, read, 200, "agent current read")
	if !strings.Contains(read.Body.String(), "agent confirmed") {
		t.Fatal(read.Body.String())
	}
	// Same mandate is never allowed to widen either its declared command or base.
	wantCode(t, call(strings.Replace(packet, `"base_rev":1`, `"base_rev":2`, 1)), 404, "retained precondition")
	mustExec(t, f.rig.db, `UPDATE mooring_intents SET revoked_at='2026-01-01T00:00:00Z' WHERE mandate_id='set-text'`)
	wantCode(t, call(packet), 404, "revocation refuses receipt replay")
	wantCode(t, call(`{"action":"read","artifact_id":"mooring-agent-page","mandate_id":"set-text"}`), 404, "revocation refuses read")
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0 WHERE id='mooring-agent-page'`)
	wantCode(t, call(packet), 404, "shared DIRECT refused")
}

func TestMooringGenericCommandUsesNativeGridCraftAuthority(t *testing.T) {
	rig := pagesTransferSetupWithDB(t, gridcraftFullDB(t))
	f, _ := documentCreationAdmissionWithPrincipal(t, newArtifactPrincipalFixtureWithRig(t, rig))
	f.rig.db.SetMaxOpenConns(1)
	wmSeedPage(t, f.rig.db, f.a.WorkspaceID, "mooring-native-page", "mooring-native", "Workbook", f.a.HumanUserID, "")
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id='mooring-native-page'`)
	f.pages.artifactMasterToken = "synthetic-mooring-native-master"
	store := appengine.New(f.rig.db, appengine.Options{Enabled: true})
	adapter := artifacts.NewGridCraftAdapter(store, &gridcraftBlockingEngine{}, true)
	native := &gridcraftHTTP{pages: f.pages, store: store, engine: &gridcraftBlockingEngine{}, operations: adapter}
	h := &mooringHTTP{workbook: native, storage: mooring.JSONAdapter{}}
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "mooring-native-cli")
	humanRequest := func(body string) *http.Request {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+humanToken)
		ctx := context.WithValue(r.Context(), ctxUser, &AuthUser{ID: f.a.HumanUserID})
		ctx = context.WithValue(ctx, ctxWorkspaceID, f.a.WorkspaceID)
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		r = r.WithContext(ctx)
		r.SetPathValue("slug", "@mooring-native-page")
		return r
	}
	imported := httptest.NewRecorder()
	native.importFile(imported, humanRequest(`{"mutation_id":"import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`))
	wantCode(t, imported, 200, "native import")
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	packet := `{"action":"command","artifact_id":"mooring-native-page","mandate_id":"set-cell","request":{"mutation_id":"native-one","base_rev":1,"generation":1,"command":"cell.set","arguments":{"sheet":"Sheet1","cell":"B2","input":"42"}}}`
	call := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.agent(w, documentDraftRequest(f, token, "POST", body))
		return w
	}
	wantCode(t, call(packet), 404, "generic native route needs explicit mandate")
	grant := httptest.NewRecorder()
	native.mandate(grant, humanRequest(fmt.Sprintf(`{"mandate_id":"set-cell","run_id":%q,"base_rev":1,"generation":1,"targets":["Sheet1!B2"]}`, f.a.RunID)))
	wantCode(t, grant, 200, "native targeted mandate")
	for i := 0; i < 2; i++ {
		w := call(packet)
		wantCode(t, w, 200, "generic native command replay")
		var receipt appengine.Receipt
		if json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Rev != 2 || receipt.Outcome != "applied" {
			t.Fatal(w.Body.String())
		}
	}
	read := call(`{"action":"read","artifact_id":"mooring-native-page","mandate_id":"set-cell"}`)
	wantCode(t, read, 200, "generic native discovery")
	if !strings.Contains(read.Body.String(), `"input_schema"`) || !strings.Contains(read.Body.String(), `"rev":2`) {
		t.Fatal(read.Body.String())
	}
	var rev, count int
	if e := f.rig.db.QueryRow(`SELECT head_rev FROM gridcraft_heads WHERE page_id='mooring-native-page'`).Scan(&rev); e != nil || rev != 2 {
		t.Fatal(rev, e)
	}
	if e := f.rig.db.QueryRow(`SELECT COUNT(*) FROM mooring_documents WHERE page_id='mooring-native-page'`).Scan(&count); e != nil || count != 0 {
		t.Fatal("native data copied to generic store", count, e)
	}

	if !strings.Contains(read.Body.String(), `"structure_rev":1`) {
		t.Fatal("native read lacks structure revision", read.Body.String())
	}
	derived := strings.Replace(packet, `"mutation_id":"native-one"`, `"mutation_id":"stale-derived","read_set":{"cells":[{"sheet":"Sheet1","cell":"B2","rev":1}]}`, 1)
	rejected := call(derived)
	wantCode(t, rejected, 409, "read-set retained across generic native bridge")
	if !strings.Contains(rejected.Body.String(), `"reason":"read_cell_changed"`) {
		t.Fatal("missing read refusal reason", rejected.Body.String())
	}
	malformed := strings.Replace(packet, `"mutation_id":"native-one"`, `"mutation_id":"malformed-read","read_set":{"unexpected":true}`, 1)
	wantCode(t, call(malformed), 400, "unknown read fields rejected")
	nullReads := strings.Replace(packet, `"mutation_id":"native-one"`, `"mutation_id":"null-read","read_set":null`, 1)
	wantCode(t, call(nullReads), 400, "null read-set cannot bypass conservative default")
	mustExec(t, f.rig.db, `UPDATE gridcraft_intents SET revoked_at='2026-01-01T00:00:00Z' WHERE mandate_id='set-cell'`)
	wantCode(t, call(packet), 404, "native revocation on generic path")
}
