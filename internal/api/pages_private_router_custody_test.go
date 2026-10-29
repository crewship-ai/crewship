package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/documents"
)

func TestPrivateCustodyActualHTTPBoundaries(t *testing.T) {
	rig := pagesTransferSetup(t)
	for _, p := range []struct {
		id, owner string
		private   bool
	}{{"http-private", rig.adminID, true}, {"http-ordinary", rig.adminID, false}, {"http-foreign", rig.userID, true}} {
		rig.seedPage(t, p.id, p.id, p.id, p.owner, "")
		if p.private {
			execOrFatal(t, rig.db, `UPDATE pages SET private_audience=1 WHERE id=?`, p.id)
		}
	}
	router, err := NewRouter(rig.db, "private-custody-router-synthetic-secret", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	session, err := router.sessionsStore.Create(t.Context(), rig.adminID, "native HTTP fixture", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := router.authMw.validator.IssueAccessToken(rig.adminID, session.ID, "Fixture", "ptadmin@x")
	if err != nil {
		t.Fatal(err)
	}
	store := documents.New(rig.db, documents.Options{})
	allow := func(context.Context, *sql.Tx) (string, error) { return "user:" + rig.adminID, nil }
	for _, page := range []string{"http-private", "http-ordinary", "http-foreign"} {
		if _, err := store.Init(t.Context(), documents.Key{WorkspaceID: rig.wsID, PageID: page}, 2, []byte("opaque-native-doc"), []byte(`[]`), allow); err != nil {
			t.Fatal(err)
		}
	}
	const secret = "private-custody-native-service-secret"
	authority := &documentAuthority{pages: router.pages, store: store, middleware: router.authMw, secret: secret}
	call := func(handler http.Handler, method, path string, want int) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("X-Workspace-ID", rig.wsID)
		req.Header.Set("X-Crewship-Docs-Service", secret)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != want {
			t.Errorf("%s %s: HTTP%d want%d: %s", method, path, out.Code, want, out.Body.String())
		}
	}
	// Healthy Router and private Authority middleware, no injected auth context.
	for _, page := range []string{"http-private", "http-ordinary", "http-foreign"} {
		call(router, "GET", "/api/v1/pages/"+page+"/versions", 200)
		call(authority, "POST", "/internal/docs/v1/"+page+"/state", 200)
	}
	if t.Failed() {
		t.FailNow()
	}
	execOrFatal(t, rig.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, rig.wsID, rig.adminID)
	execOrFatal(t, rig.db, `INSERT INTO workspaces(id,name,slug) VALUES('http-other','Other','http-other')`)
	execOrFatal(t, rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('http-other-member','http-other',?,'MEMBER','restricted')`, rig.adminID)
	call(router, "GET", "/api/v1/pages/http-private/versions", 200)
	call(authority, "POST", "/internal/docs/v1/http-private/state", 200)
	call(authority, "POST", "/internal/docs/v1/http-private/checkpoints", 200)
	for _, page := range []string{"http-ordinary", "http-foreign"} {
		call(router, "GET", "/api/v1/pages/"+page+"/versions", 404)
		call(authority, "POST", "/internal/docs/v1/"+page+"/state", 404)
	}
	call(router, "POST", "/api/v1/pages/http-private/document/initialize", 404)
	call(authority, "POST", "/internal/docs/v1/http-private/initialize", 404)
	req := httptest.NewRequest("POST", "/internal/docs/v1/http-private/state", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Workspace-ID", rig.wsID)
	req.Header.Set("X-Crewship-Docs-Service", "forged")
	out := httptest.NewRecorder()
	authority.ServeHTTP(out, req)
	if out.Code != 403 {
		t.Errorf("unauthenticated service admission HTTP%d", out.Code)
	}
	// A client header cannot mark an otherwise unclassified request.
	out = httptest.NewRecorder()
	router.authMw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })).ServeHTTP(out, req)
	if out.Code != 404 {
		t.Errorf("client header bypassed empty-pattern ceiling: HTTP%d", out.Code)
	}

}

// The route table's checked entries must never become a generic Page pass.
func TestRestrictedPrivateCustodyAdmission(t *testing.T) {
	for _, scenario := range []string{"custodian", "ordinary", "foreign", "suspended", "missing-membership", "deleted-workspace"} {
		t.Run(scenario, func(t *testing.T) {
			rig := pagesTransferSetup(t)
			rig.seedPage(t, "admission-private", "admission-private", "Private", rig.adminID, "")
			execOrFatal(t, rig.db, `UPDATE pages SET private_audience=1 WHERE id='admission-private'`)
			execOrFatal(t, rig.db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE workspace_id=? AND user_id=?`, rig.wsID, rig.adminID)
			switch scenario {
			case "ordinary":
				execOrFatal(t, rig.db, `UPDATE pages SET private_audience=0 WHERE id='admission-private'`)
			case "foreign":
				execOrFatal(t, rig.db, `UPDATE pages SET owner_user_id=? WHERE id='admission-private'`, rig.userID)
			case "suspended":
				execOrFatal(t, rig.db, `UPDATE users SET suspended_at=CURRENT_TIMESTAMP WHERE id=?`, rig.adminID)
			case "missing-membership":
				execOrFatal(t, rig.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, rig.wsID, rig.adminID)
			case "deleted-workspace":
				execOrFatal(t, rig.db, `UPDATE workspaces SET deleted_at=CURRENT_TIMESTAMP WHERE id=?`, rig.wsID)
			}
			middleware := &AuthMiddleware{db: rig.db, logger: newTestLogger()}
			for _, pattern := range restrictedPrivateCustodyPatterns {
				req := httptest.NewRequest("GET", "/admission", nil)
				req.Pattern = pattern
				req.SetPathValue("slug", "admission-private")
				req.Header.Set("X-Workspace-ID", rig.wsID)
				// Invoke the exact checked rule even when deleting the last restricted
				// membership: there must still be no positive custody admission.
				err := restrictedRoutes[pattern].check(middleware, access.Store{DB: rig.db}, req, rig.adminID)
				if (err == nil) != (scenario == "custodian") {
					t.Errorf("%s admission error=%v", pattern, err)
				}
			}
		})
	}
}
