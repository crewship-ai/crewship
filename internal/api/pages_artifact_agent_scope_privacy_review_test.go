package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// An original human's identity is not a mandate for every private Page they
// own. A valid run consented to Page A must not discover Page B by its denial.
func TestArtifactProjectScopedAgentCannotProbeOtherOwnedProject(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	f := artifactProjectOriginalRunFixture(t)
	f.admit(t)
	const master = "synthetic-artifact-agent-page-scope-master"
	router, err := NewRouter(f.rig.db, "synthetic-artifact-agent-page-scope-secret", newTestLogger(), WithInternalToken(master))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "artifact-agent-page-scope-human")
	human := func(method, path, body string) *httptest.ResponseRecorder {
		req := pagesRequest(t, method, path, f.a.WorkspaceID, f.a.HumanUserID, "OWNER", body)
		req.Header.Set("Authorization", "Bearer "+humanToken)
		req.Header.Set("X-Workspace-ID", f.a.WorkspaceID)
		req.Header.Set("Content-Type", "application/json")
		got := httptest.NewRecorder()
		// Seed through production human handlers before public route registration;
		// every signed-agent probe below still crosses the complete Router.
		req = req.WithContext(context.WithValue(req.Context(), ctxAuthKind, AuthKindCLIToken))
		switch path {
		case "/api/v1/pages/artifacts":
			router.pages.CreateArtifactProject(got, req)
		case "/api/v1/pages/artifacts/consent":
			router.pages.GrantArtifactProjectRunConsent(got, req)
		default:
			req.SetPathValue("slug", "scope-project-b")
			router.pages.GetArtifactProject(got, req)
		}
		return got
	}
	for _, slug := range []string{"scope-project-a", "scope-project-b"} {
		body := `{"slug":"` + slug + `","name":"` + slug + `",` + strings.TrimPrefix(artifactProjectBody(0, slug), "{")
		if got := human(http.MethodPost, "/api/v1/pages/artifacts", body); got.Code != 201 {
			t.Fatalf("create %s: %d %s", slug, got.Code, got.Body)
		}
	}
	var pageA string
	if err = f.rig.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug='scope-project-a'`, f.a.WorkspaceID).Scan(&pageA); err != nil {
		t.Fatal(err)
	}
	grant, _ := json.Marshal(map[string]any{"run_id": f.a.RunID, "page_id": pageA})
	if got := human(http.MethodPost, "/api/v1/pages/artifacts/consent", string(grant)); got.Code != 200 {
		t.Fatalf("exact PageA consent: %d %s", got.Code, got.Body)
	}
	if got := human(http.MethodGet, "/api/v1/pages/scope-project-b/artifact", ""); got.Code != 200 || !strings.Contains(got.Body.String(), "scope-project-b") {
		t.Fatalf("owner PageB calibration: %d %s", got.Code, got.Body)
	}
	key := internaltoken.DeriveAgentRunKey(master, f.a.WorkspaceID, f.a.CrewID)
	runToken := internaltoken.DeriveAgentRunToken(key, f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
	crewToken := internaltoken.DeriveCrewToken(master, f.a.WorkspaceID, f.a.CrewID)
	tool := func(token, operation, slug string, payload json.RawMessage) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"operation": operation, "slug": slug, "payload": payload})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/pages/artifact-tool", bytes.NewReader(raw))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Internal-Token", crewToken)
		req.Header.Set("Content-Type", "application/json")
		got := httptest.NewRecorder()
		router.ServeHTTP(got, req)
		return got
	}
	for _, operation := range []string{"artifact_versions", "artifact_comments"} {
		if got := tool(runToken, operation, "scope-project-a", json.RawMessage(`{}`)); got.Code != 200 || !strings.Contains(got.Body.String(), "scope-project-a") {
			t.Fatalf("valid scoped run PageA %s calibration: %d %s", operation, got.Code, got.Body)
		}
	}
	for _, tc := range []struct{ operation, payload string }{
		{"artifact_versions", `{}`},
		{"artifact_comments", `{}`},
		{"artifact_publish", artifactProjectBody(1, "Unauthorized candidate")},
		{"artifact_restore", `{"base_version":1,"version":1}`},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			absent := tool(runToken, tc.operation, "absent-scope-project", json.RawMessage(tc.payload))
			if absent.Code != 404 {
				t.Fatalf("absent baseline: %d %s", absent.Code, absent.Body)
			}
			other := tool(runToken, tc.operation, "scope-project-b", json.RawMessage(tc.payload))
			if other.Code != 404 || other.Body.String() != absent.Body.String() {
				t.Errorf("out-of-mandate PageB existence distinguishable: existing %d %s; absent %d %s", other.Code, other.Body, absent.Code, absent.Body)
			}
		})
	}
	// Invalid credentials fail before a Page lookup and keep their uniform403.
	var invalidBody string
	for _, slug := range []string{"scope-project-a", "scope-project-b", "absent-scope-project"} {
		got := tool("invalid-synthetic-run-bearer", "artifact_versions", slug, json.RawMessage(`{}`))
		if got.Code != 403 {
			t.Errorf("invalid run %s: %d %s", slug, got.Code, got.Body)
		}
		if invalidBody != "" && got.Body.String() != invalidBody {
			t.Errorf("invalid-run denial differs by Page: %s", got.Body)
		}
		invalidBody = got.Body.String()
	}
	var heads, latest, pins, versions, comments int
	if err = f.rig.db.QueryRow(`SELECT count(*),sum(latest_version),sum(shared_version) FROM artifact_version_heads WHERE workspace_id=?`, f.a.WorkspaceID).Scan(&heads, &latest, &pins); err != nil || heads != 2 || latest != 2 || pins != 0 {
		t.Fatalf("denied agent probes changed heads: count%d latest%d shared%d: %v", heads, latest, pins, err)
	}
	if err = f.rig.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE workspace_id=?`, f.a.WorkspaceID).Scan(&versions); err != nil || versions != 2 {
		t.Fatalf("denied agent probes retained new version: %d: %v", versions, err)
	}
	if err = f.rig.db.QueryRow(`SELECT count(*) FROM artifact_project_comments`).Scan(&comments); err != nil || comments != 0 {
		t.Fatalf("denied agent probes inserted comment: %d: %v", comments, err)
	}
}
