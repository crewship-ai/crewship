package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestArtifactProjectToolUsesCheckedWorkspaceFeatureOverride(t *testing.T) {
	f := artifactProjectOriginalRunFixture(t)
	f.admit(t)
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "false")
	mustExec(t, f.rig.db, `UPDATE feature_flags SET enabled=0 WHERE key='artifact_projects'`)
	mustExec(t, f.rig.db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled,created_at) SELECT 'artifact-tool-workspace-override',id,?,1,datetime('now') FROM feature_flags WHERE key='artifact_projects'`, f.a.WorkspaceID)
	workspaceCtx := withWorkspace(context.Background(), f.a.WorkspaceID, "OWNER")
	if !f.pages.artifactProjectEnabled(workspaceCtx) || f.pages.artifactProjectEnabled(context.Background()) {
		t.Fatal("fixture must enable only the checked workspace, not the global feature")
	}
	f.pages.artifactMasterToken = "synthetic-artifact-workspace-feature-master"
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "artifact-workspace-feature-consent")
	grant := pagesRequest(t, "POST", "/api/v1/pages/artifacts/consent", f.a.WorkspaceID, f.a.HumanUserID, "OWNER", fmt.Sprintf(`{"run_id":%q}`, f.a.RunID))
	grant.Header.Set("Authorization", "Bearer "+humanToken)
	grant = grant.WithContext(context.WithValue(grant.Context(), ctxAuthKind, AuthKindCLIToken))
	consent := httptest.NewRecorder()
	f.pages.GrantArtifactProjectRunConsent(consent, grant)
	if consent.Code != 200 {
		t.Fatalf("workspace-only human consent: %d %s", consent.Code, consent.Body)
	}
	key := internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID)
	bearer := internaltoken.DeriveAgentRunToken(key, f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	call := func(operation, payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/internal/pages/artifact-tool", strings.NewReader(fmt.Sprintf(`{"operation":%q,"slug":"workspace-project","payload":%s}`, operation, payload)))
		req.Header.Set("Authorization", "Bearer "+bearer)
		ctx := context.WithValue(req.Context(), ctxInternalTokenWS, f.a.WorkspaceID)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.a.CrewID)
		if WorkspaceIDFromContext(ctx) != "" {
			t.Fatal("raw internal context unexpectedly carries the checked human workspace")
		}
		got := httptest.NewRecorder()
		f.pages.ArtifactProjectTool(got, req.WithContext(ctx))
		return got
	}
	body := `{"name":"Workspace project",` + strings.TrimPrefix(artifactProjectBody(0, "Workspace flag"), "{")
	if got := call("artifact_create", body); got.Code != 201 {
		t.Fatalf("workspace-only signed creation: %d %s", got.Code, got.Body)
	}
	if got := call("artifact_versions", `{}`); got.Code != 200 || !strings.Contains(got.Body.String(), "Workspace flag") {
		t.Fatalf("workspace-only signed read: %d %s", got.Code, got.Body)
	}
}
