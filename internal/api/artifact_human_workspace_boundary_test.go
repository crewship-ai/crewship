package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// Exercise the human HTTP boundary, rather than injecting authenticated
// context into a handler. Each reader belongs only to its selected workspace.
// Slides here is the typed ArtifactProject projection, not the native Deck.
func TestTypedArtifactHumanRouterWorkspaceBoundary(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACTS_DESIGN_ENABLED", "true")
	f := gridcraftInstanceFixture(t)
	t.Cleanup(f.r.Shutdown)
	type human struct{ workspace, writer, reader string }
	people := make([]human, 2)
	for i, workspace := range []string{"ws-old", "ws-new"} {
		user := fmt.Sprintf("typed-boundary-human-%d", i)
		seedInstanceUser(t, f.db, user, user+"@example.test", workspace, "MANAGER")
		people[i] = human{workspace, mintTokenFor(t, f.db, user, "typed-boundary-writer-"+user), mintIngressScopedToken(t, f.db, user, "typed-boundary-reader-"+user, "pages:read")}
		var memberships int
		if err := f.db.QueryRow(`SELECT count(*) FROM workspace_members WHERE user_id=?`, user).Scan(&memberships); err != nil || memberships != 1 {
			t.Fatalf("human must belong to exactly one workspace: count=%d err=%v", memberships, err)
		}
	}
	request := func(person human, write bool, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		token := person.reader
		if write {
			token = person.writer
		}
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		return f.do(token, method, path+separator+"workspace_id="+person.workspace, body)
	}
	for _, kind := range []string{"project", "dashboard", "design", "slides"} {
		t.Run(kind, func(t *testing.T) {
			create := func(person human, slug, marker string) string {
				t.Helper()
				body, route := artifactProjectBody(0, marker), "/api/v1/pages/artifacts"
				switch kind {
				case "dashboard":
					body = dashboardBody(0, "sales")
				case "slides":
					body = slidesArtifactBody(0, marker)
				case "design":
					body, route = `{}`, "/api/v1/pages/design"
				}
				var payload map[string]any
				if err := json.Unmarshal([]byte(body), &payload); err != nil {
					t.Fatal(err)
				}
				payload["slug"], payload["name"] = slug, marker
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				got := request(person, true, "POST", route, string(raw))
				var receipt struct {
					PageID string `json:"page_id"`
				}
				if got.Code != 201 || json.Unmarshal(got.Body.Bytes(), &receipt) != nil || receipt.PageID == "" {
					t.Fatalf("create %s: %d %s", kind, got.Code, got.Body)
				}
				var mutationPath, mutationBody string
				switch kind {
				case "dashboard":
					mutationPath = "/artifact/data/sales"
					mutationBody = fmt.Sprintf(`{"base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"marker":%q}}]}`, marker)
				case "design":
					mutationPath = "/design/operations"
					mutationBody = fmt.Sprintf(`{"operations":[{"op":"upsert","node":{"id":%q,"type":"frame","x":0,"y":0,"width":300,"height":600}}]}`, marker)
				}
				if mutationPath != "" {
					got = request(person, true, "POST", "/api/v1/pages/@"+receipt.PageID+mutationPath, mutationBody)
					if got.Code != 200 {
						t.Fatalf("seed %s content: %d %s", kind, got.Code, got.Body)
					}
				}
				// A read-scoped non-admin Design owner reads the shared snapshot.
				// Publish the current version through the normal route so its
				// positive control exercises that profile rather than admin live read.
				if kind == "design" {
					got = request(person, true, "POST", "/api/v1/pages/@"+receipt.PageID+"/design/share", `{"version":2}`)
					if got.Code != 200 {
						t.Fatalf("share Design snapshot: %d %s", got.Code, got.Body)
					}
				}
				return receipt.PageID
			}
			suffix := map[string]string{"project": "/artifact", "dashboard": "/artifact/data/sales?version=1", "design": "/design", "slides": "/artifact/slides?version=1"}[kind]
			read := func(person human, ref string) *httptest.ResponseRecorder {
				return request(person, false, "GET", "/api/v1/pages/"+ref+suffix, "")
			}
			assertOwn := func(person human, ref, marker string) {
				t.Helper()
				got := read(person, ref)
				if got.Code != 200 || !strings.Contains(got.Body.String(), marker) {
					t.Fatalf("positive %s %s: %d %s", kind, ref, got.Code, got.Body)
				}
			}
			slug := "typed-boundary-" + kind
			ownMarker, foreignMarker, collisionMarker := kind+"-own-canary", kind+"-foreign-canary", kind+"-collision-canary"
			ownID := create(people[0], slug, ownMarker)
			foreignID := create(people[1], slug, foreignMarker)
			// A local slug equal to the foreign immutable ID makes an incorrect
			// ID-to-slug fallback observable even when it releases no foreign bytes.
			create(people[0], foreignID, collisionMarker)
			assertOwn(people[0], "@"+ownID, ownMarker)
			assertOwn(people[0], slug, ownMarker)
			assertOwn(people[0], foreignID, collisionMarker)
			assertOwn(people[1], "@"+foreignID, foreignMarker)
			missing := read(people[0], "@missing-typed-boundary-"+kind)
			foreign := read(people[0], "@"+foreignID)
			if missing.Code != 404 || foreign.Code != 404 || missing.Body.String() != foreign.Body.String() {
				t.Fatalf("foreign and absent must be opaque 404: absent=%d %s foreign=%d %s", missing.Code, missing.Body, foreign.Code, foreign.Body)
			}
			for _, marker := range []string{foreignMarker, collisionMarker, foreignID} {
				if strings.Contains(foreign.Body.String(), marker) {
					t.Fatalf("foreign immutable lookup released %q: %s", marker, foreign.Body)
				}
			}
		})
	}
}
