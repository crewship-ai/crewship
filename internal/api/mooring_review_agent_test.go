package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestReviewMooringAgentRevocationAtAudit(t *testing.T) {
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
	mustExec(t, f.rig.db, `CREATE TRIGGER review_agent_revoke AFTER INSERT ON journal_entries WHEN NEW.entry_type='artifact.mooring_committed' BEGIN UPDATE mooring_intents SET revoked_at='2026-01-01T00:00:00Z' WHERE mandate_id='set-text'; END`)
	wantCode(t, call(packet), 404, "revocation during final audit")
	var rev, n int
	if e := f.rig.db.QueryRow(`SELECT rev FROM mooring_documents WHERE page_id='mooring-agent-page'`).Scan(&rev); e != nil {
		t.Fatal(e)
	}
	if e := f.rig.db.QueryRow(`SELECT count(*) FROM mooring_receipts WHERE page_id='mooring-agent-page'`).Scan(&n); e != nil {
		t.Fatal(e)
	}
	if rev != 1 || n != 0 {
		t.Fatalf("partial commit rev=%d receipts=%d", rev, n)
	}
}

func TestReviewMooringMandateRevocationAtAudit(t *testing.T) {
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
	mustExec(t, f.rig.db, `CREATE TRIGGER review_mandate_revoke AFTER INSERT ON journal_entries WHEN NEW.entry_type='artifact.mooring_mandate_admitted' BEGIN UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z'; END`)
	grant := httptest.NewRecorder()
	h.mandate(grant, humanRequest(fmt.Sprintf(`{"mandate_id":"set-text","run_id":%q,"base_rev":1,"generation":1,"command":"set"}`, f.a.RunID)))
	if grant.Code == 200 {
		t.Fatal("admission after revocation", grant.Body.String())
	}
	var n int
	if e := f.rig.db.QueryRow(`SELECT count(*) FROM mooring_intents WHERE page_id='mooring-agent-page'`).Scan(&n); e != nil {
		t.Fatal(e)
	}
	if n != 0 {
		t.Fatalf("partial admission=%d", n)
	}
}
