package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// R71 §0.14/1 and §0.3/4 for the artifact-project family (Library projects,
// Slides source and Dashboard data): a run's human lends it their write only
// while they own the artifact. A workspace ADMIN, or a human with an Edit
// grant, who does not own it cannot have an agent write it. Source save and
// data writes share one predicate, so both are asserted, and nothing changes.
func TestArtifactProjectRunWritesOnlyForTheOwner(t *testing.T) {
	for _, tc := range []struct {
		name      string
		role      string
		humanEdit bool
	}{
		{"workspace admin who no longer owns it", "ADMIN", false},
		{"human Edit grantee", "MEMBER", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
			t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
			f := artifactProjectOriginalRunFixture(t)
			mustExec(t, f.rig.db, `UPDATE workspace_members SET role=? WHERE workspace_id=? AND user_id=?`, tc.role, f.a.WorkspaceID, f.a.HumanUserID)
			f.receipt = artifactRunFreshReceipt(t, f)
			f.admit(t)
			f.pages.artifactMasterToken = "synthetic-project-run-delegation-master"
			humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "project-run-delegation")
			consent := pagesRequest(t, "POST", "/api/v1/pages/artifacts/consent", f.a.WorkspaceID, f.a.HumanUserID, tc.role, `{"run_id":"`+f.a.RunID+`"}`)
			consent.Header.Set("Authorization", "Bearer "+humanToken)
			consent = consent.WithContext(context.WithValue(consent.Context(), ctxAuthKind, AuthKindCLIToken))
			granted := httptest.NewRecorder()
			f.pages.GrantArtifactProjectRunConsent(granted, consent)
			if granted.Code != 200 {
				t.Fatalf("human consent: %d %s", granted.Code, granted.Body)
			}
			token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
			tool := func(operation, payload string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "/api/v1/internal/pages/artifact-tool", strings.NewReader(fmt.Sprintf(`{"operation":%q,"slug":"delegation-dashboard","payload":%s}`, operation, payload)))
				r.Header.Set("Authorization", "Bearer "+token)
				ctx := context.WithValue(r.Context(), ctxInternalTokenWS, f.a.WorkspaceID)
				ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.a.CrewID)
				w := httptest.NewRecorder()
				f.pages.ArtifactProjectTool(w, r.WithContext(ctx))
				return w
			}
			if w := tool("artifact_create", `{"name":"Dashboard",`+strings.TrimPrefix(dashboardBody(0, "sales"), "{")); w.Code != 201 {
				t.Fatalf("run create: %d %s", w.Code, w.Body)
			}
			var page string
			if err := f.rig.db.QueryRow(`SELECT id FROM pages WHERE slug='delegation-dashboard'`).Scan(&page); err != nil {
				t.Fatal(err)
			}
			batch := func(id string) string {
				return `{"collection":"sales","base_version":1,"documents":[{"id":"` + id + `","ifVersion":0,"value":{"amount":1}}]}`
			}
			if w := tool("artifact_data_batch", batch("owner-run")); w.Code != 200 {
				t.Fatalf("calibration: owner run write: %d %s", w.Code, w.Body)
			}
			mustExec(t, f.rig.db, `INSERT INTO users(id,email) VALUES('delegation-owner','delegation-owner@example.test')`)
			mustExec(t, f.rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,'delegation-owner','MEMBER')`, generateCUID(), f.a.WorkspaceID)
			mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id='delegation-owner',private_audience=0 WHERE id=?`, page)
			if tc.humanEdit {
				mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'write','delegation-owner',datetime('now'))`, page, f.a.HumanUserID)
			}
			var docs int
			if err := f.rig.db.QueryRow(`SELECT count(*) FROM artifact_data_documents WHERE page_id=?`, page).Scan(&docs); err != nil {
				t.Fatal(err)
			}
			if w := tool("artifact_publish", dashboardBody(1, "sales")); w.Code != http.StatusForbidden {
				t.Errorf("source save through the run: %d, want 403; %s", w.Code, w.Body)
			}
			if w := tool("artifact_data_batch", batch("undelegated-run")); w.Code != http.StatusForbidden {
				t.Errorf("data write through the run: %d, want 403; %s", w.Code, w.Body)
			}
			var latest int64
			var after int
			if err := f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id=?`, page).Scan(&latest); err != nil || latest != 1 {
				t.Errorf("refused run changed source: %d %v", latest, err)
			}
			if err := f.rig.db.QueryRow(`SELECT count(*) FROM artifact_data_documents WHERE page_id=?`, page).Scan(&after); err != nil || after != docs {
				t.Errorf("refused run changed data: %d -> %d %v", docs, after, err)
			}
		})
	}
}
