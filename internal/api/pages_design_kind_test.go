package api

import (
	"net/http/httptest"
	"testing"
)

func TestDesignArtifactListKind(t *testing.T) {
	if got := artifactListKind("design"); got != "design" {
		t.Fatalf("Design classified as %q", got)
	}
}

func TestDesignCannotBecomePanelsOrProject(t *testing.T) {
	h, workspace, owner, call := designFixture(t)
	if w := call(owner, "create", `{"slug":"board","name":"Board"}`); w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	request := pagesRequest(t, "PATCH", "/api/v1/pages/board", workspace, owner, "OWNER", `{"panels":[]}`)
	request.SetPathValue("slug", "board")
	out := httptest.NewRecorder()
	h.Update(out, request)
	if out.Code != 409 {
		t.Fatalf("panels mutation %d %s", out.Code, out.Body)
	}
	out = httptest.NewRecorder()
	request = pagesRequest(t, "GET", "/api/v1/pages/board/project", workspace, owner, "OWNER", "")
	request.SetPathValue("slug", "board")
	if _, ok := h.projectPage(out, request); ok || out.Code != 409 {
		t.Fatalf("project mutation %d %s", out.Code, out.Body)
	}
}

func TestDesignMutationScopes(t *testing.T) {
	for _, pattern := range []string{"/api/v1/pages/design", "/api/v1/pages/{slug}/design/operations", "/api/v1/pages/{slug}/design/snapshots", "/api/v1/pages/{slug}/design/restore", "/api/v1/pages/{slug}/design/comments", "/api/v1/pages/{slug}/design/share", "/api/v1/pages/{slug}/design/agent"} {
		if scope := scopeForRoute(pattern); scope != "pages:write" {
			t.Errorf("%s: scope %q", pattern, scope)
		}
	}
}
