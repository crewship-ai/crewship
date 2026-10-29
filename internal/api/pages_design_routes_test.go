package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDesignRouterUsesPageWriteScopeAndRejectsReadWrites(t *testing.T) {
	h, workspace, owner, _ := designFixture(t)
	writer := mintIngressScopedToken(t, h.db, owner, "design-route-writer", "pages:write")
	reader := mintIngressScopedToken(t, h.db, owner, "design-route-reader", "pages:read")
	router, err := NewRouter(h.db, "synthetic-design-route-secret-at-least-32-chars", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	request := func(token, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Workspace-ID", workspace)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	create := `{"slug":"board","name":"Scoped Design"}`
	if w := request(reader, "POST", "/api/v1/pages/design", create); w.Code != 403 {
		t.Fatalf("read create %d %s", w.Code, w.Body)
	}
	if w := request(writer, "POST", "/api/v1/pages/design", create); w.Code != 201 {
		t.Fatalf("write create %d %s", w.Code, w.Body)
	}
	body := `{"operations":[{"op":"upsert","node":{"id":"frame","type":"frame","x":0,"y":0,"width":300,"height":600}}]}`
	if w := request(reader, "POST", "/api/v1/pages/board/design/operations", body); w.Code != 403 {
		t.Fatalf("read update %d %s", w.Code, w.Body)
	}
	if w := request(writer, "POST", "/api/v1/pages/board/design/operations", body); w.Code != 200 {
		t.Fatalf("write update %d %s", w.Code, w.Body)
	}
	if w := request(reader, "GET", "/api/v1/pages/board/design", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"can_edit":false`) {
		t.Fatalf("read get %d %s", w.Code, w.Body)
	}
	if w := request(reader, "POST", "/api/v1/pages/board/design/snapshots", `{}`); w.Code != 403 {
		t.Fatalf("read snapshot %d %s", w.Code, w.Body)
	}
}
