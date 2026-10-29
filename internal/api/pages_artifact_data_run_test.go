package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestDashboardDataSignedRunUsesExactPageMandateAndHostProvenance(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	f := artifactProjectOriginalRunFixture(t)
	f.admit(t)
	f.pages.artifactMasterToken = "synthetic-dashboard-data-master"
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "dashboard-consent")
	human := pagesRequest(t, "POST", "/api/v1/pages/artifacts/consent", f.a.WorkspaceID, f.a.HumanUserID, "OWNER", `{"run_id":"principal-run"}`)
	human.Header.Set("Authorization", "Bearer "+humanToken)
	human = human.WithContext(context.WithValue(human.Context(), ctxAuthKind, AuthKindCLIToken))
	w := httptest.NewRecorder()
	f.pages.GrantArtifactProjectRunConsent(w, human)
	if w.Code != 200 {
		t.Fatalf("human consent: %d %s", w.Code, w.Body)
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	tool := func(operation, slug, payload string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/internal/pages/artifact-tool", strings.NewReader(fmt.Sprintf(`{"operation":%q,"slug":%q,"payload":%s}`, operation, slug, payload)))
		r.Header.Set("Authorization", "Bearer "+token)
		ctx := context.WithValue(r.Context(), ctxInternalTokenWS, f.a.WorkspaceID)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.a.CrewID)
		got := httptest.NewRecorder()
		f.pages.ArtifactProjectTool(got, r.WithContext(ctx))
		return got
	}
	create := `{"name":"Dashboard",` + strings.TrimPrefix(dashboardBody(0, "sales"), "{")
	if w := tool("artifact_create", "agent-dashboard", create); w.Code != 201 {
		t.Fatalf("run create: %d %s", w.Code, w.Body)
	}
	mustExec(t, f.rig.db, `INSERT INTO artifact_project_message_intents(message_id,workspace_id,human_user_id,chat_id,submission_id,base_version,run_id,created_at) VALUES(?,?,?,?,?,0,?,datetime('now'))`, f.a.MessageID, f.a.WorkspaceID, f.a.HumanUserID, f.a.ChatID, "dashboard-original", f.a.RunID)
	batch := `{"collection":"sales","base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12,"DisplayName":"` + strings.Repeat("<", 200<<10) + `","optional":null}}]}`
	w = tool("artifact_data_batch", "agent-dashboard", batch)
	if w.Code != 200 || strings.Contains(w.Body.String(), f.a.RunID) {
		t.Fatalf("signed data write: %d %s", w.Code, w.Body)
	}
	var actor string
	if err := f.rig.db.QueryRow(`SELECT updated_by FROM artifact_data_documents WHERE workspace_id=?`, f.a.WorkspaceID).Scan(&actor); err != nil || actor != "agent:"+f.a.AgentID+":"+f.a.RunID {
		t.Fatalf("host provenance: %q %v", actor, err)
	}
	if w := tool("artifact_data_read", "agent-dashboard", `{"collection":"sales","version":1}`); w.Code != 200 {
		t.Fatalf("signed read: %d %s", w.Code, w.Body)
	}
	f.rig.seedPage(t, "other-data-page", "other-data-page", "Other", f.a.HumanUserID, "")
	if w := tool("artifact_data_read", "other-data-page", `{"collection":"sales","version":1}`); w.Code != 404 {
		t.Fatalf("run escaped Page mandate: %d %s", w.Code, w.Body)
	}
	mustExec(t, f.rig.db, `UPDATE users SET access_revoked_at='2026-10-28T00:00:00Z' WHERE id=?`, f.a.HumanUserID)
	if w := tool("artifact_data_batch", "agent-dashboard", strings.Replace(batch, `"ifVersion":0`, `"ifVersion":1`, 1)); w.Code != 403 {
		t.Fatalf("revoked original authority: %d %s", w.Code, w.Body)
	}
}
