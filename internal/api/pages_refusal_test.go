package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPageManagementConcealsUnreachablePages(t *testing.T) {
	h, _, ws, owner, pageID := pagesGrantFixture(t, "")
	pagesSeedUser(t, h, ws, "outsider", "outsider@example.com", "MEMBER")
	routes := []struct {
		name, method string
		handler      http.HandlerFunc
	}{
		{"push", "PUT", h.PushData},
		{"unknown panel", "PUT", func(w http.ResponseWriter, r *http.Request) { r.SetPathValue("panelId", "missing"); h.PushData(w, r) }}, {"update", "PATCH", h.Update}, {"delete", "DELETE", h.Delete},
		{"grants", "GET", h.ListGrants}, {"grant", "PUT", h.PutGrant}, {"revoke grant", "DELETE", h.DeleteGrant},
		{"access", "GET", h.PageAccess}, {"versions", "GET", h.ListVersions}, {"rollback", "POST", h.Rollback},
		{"export", "GET", h.Export}, {"project", "GET", h.GetProject},
		{"publish link", "POST", h.Publish}, {"links", "GET", h.ListPublicLinks}, {"revoke link", "DELETE", h.RevokePublicLink},
		{"webhook", "POST", h.CreateWebhook}, {"webhooks", "GET", h.ListWebhooks}, {"revoke webhook", "DELETE", h.RevokeWebhook},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			call := func(slug string) *httptest.ResponseRecorder {
				r := pagesGrantRequest(t, route.method, "/", ws, "outsider", "MEMBER", `{}`)
				r.SetPathValue("slug", slug)
				r.SetPathValue("panelId", "sluzby")
				w := httptest.NewRecorder()
				route.handler(w, r)
				return w
			}
			for _, slug := range []string{"fleet-201", "missing"} {
				w := call(slug)
				if w.Code != 404 {
					t.Fatalf("%s: %d %s", slug, w.Code, w.Body.String())
				}
				expected := fmt.Sprintf("page %q not found", slug)
				if route.name == "project" {
					expected = "page not found"
				}
				reference := httptest.NewRecorder()
				replyError(reference, 404, expected)
				if w.Body.String() != reference.Body.String() {
					t.Fatalf("unexpected body: %s", w.Body.String())
				}
			}
		})
	}
	// A read grant gives reach, never management. Denials must remain 403.
	if _, err := h.db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES (?,'user','outsider','read',?)`, pageID, owner); err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		t.Run("reachable/"+route.name, func(t *testing.T) {
			r := pagesGrantRequest(t, route.method, "/", ws, "outsider", "MEMBER", `{}`)
			r.SetPathValue("slug", "fleet-201")
			r.SetPathValue("panelId", "sluzby")
			w := httptest.NewRecorder()
			route.handler(w, r)
			want := 403
			if route.name == "unknown panel" {
				want = 404
			}
			if w.Code != want || strings.Contains(w.Body.String(), "not found") {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}

// Folder ACLs are additive even for workspace Viewers. Revocation ends that
// authority; panel membership continues to grant read, but cannot grant write.
func TestPageFolderViewerExplicitWriteAndRevocation(t *testing.T) {
	f := newFoldersFixture(t)
	if _, err := f.h.db.Exec(`UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id='frank'`, f.wsID); err != nil {
		t.Fatal(err)
	}
	pagesJoinCrew(t, f.h, "frank", "crew-lookout")
	f.createFolder(t, "viewer-test", "Viewer test", "crew/engine")
	f.file(t, "viewer-test", "fleet-201")
	edit := func() *httptest.ResponseRecorder {
		return f.pageCall(t, "PATCH", "/api/v1/pages/fleet-201", "frank", "VIEWER", `{"name":"Viewer edit"}`, f.h.Update)
	}
	expectStatus(t, edit(), http.StatusForbidden)
	f.share(t, "viewer-test", "user", "frank", true)
	expectStatus(t, edit(), http.StatusOK)
	f.unshare(t, "viewer-test", "user", "frank")
	expectStatus(t, edit(), http.StatusForbidden)
}
