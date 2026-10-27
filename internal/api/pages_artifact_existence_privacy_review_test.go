package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A trusted workspace membership and live credential must not let an outsider
// distinguish another human's private project from an absent slug by probing
// mutation endpoints. Exercise the production Router, not a forged owner role.
func TestArtifactProjectPrivateExistenceHiddenFromWorkspaceOutsider(t *testing.T) {
	h, workspace, owner, create := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "Private title"), "{")
	if got := create(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Private project",`+body); got.Code != 201 {
		t.Fatalf("owner creation: %d %s", got.Code, got.Body)
	}
	token := seedRoleMemberToken(t, h.db, workspace, "artifact-ungranted-member", "MEMBER", "artifact-ungranted-member")
	router, err := NewRouter(h.db, "synthetic-artifact-existence-secret", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)

	ownerReq := httptest.NewRequest(http.MethodGet, "/api/v1/pages/three-screens/artifact", nil)
	ownerReq.Header.Set("Authorization", "Bearer crewship_cli_artifact-project-owner")
	ownerReq.Header.Set("X-Workspace-ID", workspace)
	ownerRead := httptest.NewRecorder()
	router.ServeHTTP(ownerRead, ownerReq)
	if ownerRead.Code != 200 {
		t.Fatalf("positive owner calibration: %d %s", ownerRead.Code, ownerRead.Body)
	}
	call := func(method, slug, suffix, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/pages/"+slug+"/artifact"+suffix, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", workspace)
		req.Header.Set("Content-Type", "application/json")
		got := httptest.NewRecorder()
		router.ServeHTTP(got, req)
		return got
	}

	for _, audience := range []string{"private_unshared", "shared_without_grant"} {
		if audience == "shared_without_grant" {
			// Selecting a shared pin does not grant this unrelated member Page access.
			if got := create(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); got.Code != 200 {
				t.Fatalf("owner selects share pin: %d %s", got.Code, got.Body)
			}
		}
		t.Run(audience, func(t *testing.T) {
			for _, tc := range []struct{ method, suffix, body string }{
				{http.MethodGet, "", ""},
				{http.MethodGet, "/versions", ""},
				{http.MethodGet, "/comments", ""},
				{http.MethodPost, "/versions", artifactProjectBody(1, "Unauthorized replacement")},
				{http.MethodPost, "/restore", `{"base_version":1,"version":1}`},
				{http.MethodPost, "/share", `{"base_version":1,"version":1}`},
				{http.MethodPost, "/comments", `{"version":1,"path":"index.html","selector":"#hero","quote":"Private title","text":"Unauthorized comment","send_to_agent":false}`},
			} {
				t.Run(tc.method+tc.suffix, func(t *testing.T) {
					absent := call(tc.method, "absent-private-project", tc.suffix, tc.body)
					if absent.Code != 404 {
						t.Fatalf("absent baseline: %d %s", absent.Code, absent.Body)
					}
					private := call(tc.method, "three-screens", tc.suffix, tc.body)
					if private.Code != 404 || private.Body.String() != absent.Body.String() {
						t.Errorf("private project existence distinguishable: existing %d %s; absent %d %s", private.Code, private.Body, absent.Code, absent.Body)
					}
				})
			}
		})
	}

	var latest, pin, comments int
	if err = h.db.QueryRow(`SELECT latest_version,shared_version FROM artifact_version_heads WHERE workspace_id=?`, workspace).Scan(&latest, &pin); err != nil || latest != 1 || pin != 1 {
		t.Fatalf("denied probes changed manifest/share head: latest%d shared%d: %v", latest, pin, err)
	}
	if err = h.db.QueryRow(`SELECT count(*) FROM artifact_project_comments`).Scan(&comments); err != nil || comments != 0 {
		t.Fatalf("denied comment persisted: count%d: %v", comments, err)
	}
}
