package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArtifactProjectCreateRoutesRequireWorkspaceFlagAndWriteScope(t *testing.T) {
	h, workspace, owner, _ := artifactProjectFixture(t)
	writer := mintIngressScopedToken(t, h.db, owner, "artifact-create-route-writer", "pages:write")
	reader := mintIngressScopedToken(t, h.db, owner, "artifact-create-route-reader", "pages:read")
	router, err := NewRouter(h.db, "synthetic-artifact-create-route-secret", newTestLogger(), WithInternalToken("synthetic-artifact-create-route-host"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	request := func(token, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", workspace)
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	availability := func(want bool) {
		t.Helper()
		out := request(reader, "GET", "/api/v1/pages/artifacts/availability", "")
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &body) != nil || body.Enabled == nil || *body.Enabled != want {
			t.Fatalf("availability %d: %s", out.Code, out.Body)
		}
	}
	body := `{"slug":"three-screens","name":"Create route",` + strings.TrimPrefix(artifactProjectBody(0, "Original content"), "{")
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "false")
	availability(false)
	if out := request(writer, "POST", "/api/v1/pages/artifacts", body); out.Code != 404 {
		t.Fatalf("disabled create %d: %s", out.Code, out.Body)
	}
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	availability(true)
	if out := request(reader, "POST", "/api/v1/pages/artifacts", body); out.Code != 403 {
		t.Fatalf("read-scoped create %d: %s", out.Code, out.Body)
	}
	if out := request("", "GET", "/api/v1/pages/artifacts/availability", ""); out.Code != 401 {
		t.Fatalf("anonymous availability %d", out.Code)
	}
	out := request(writer, "POST", "/api/v1/pages/artifacts", body)
	var receipt struct {
		PageID  string `json:"page_id"`
		Slug    string `json:"slug"`
		Version int    `json:"version"`
		URL     string `json:"url"`
	}
	if out.Code != 201 || json.Unmarshal(out.Body.Bytes(), &receipt) != nil || receipt.PageID == "" || receipt.Slug != "three-screens" || receipt.Version != 1 || receipt.URL == "" {
		t.Fatalf("create %d: %s", out.Code, out.Body)
	}
	var count int
	if err := h.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE workspace_id=? AND page_id=?`, workspace, receipt.PageID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("durable creation %d %v", count, err)
	}
}
