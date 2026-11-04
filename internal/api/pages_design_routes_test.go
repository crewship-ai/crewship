package api

import (
	"encoding/json"
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
	if w := request(writer, "POST", "/api/v1/pages/board/design/share", `{"version":2}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := request(reader, "GET", "/api/v1/pages/board/design", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"can_edit":false`) {
		t.Fatalf("read get %d %s", w.Code, w.Body)
	}
	if w := request(reader, "POST", "/api/v1/pages/board/design/snapshots", `{}`); w.Code != 403 {
		t.Fatalf("read snapshot %d %s", w.Code, w.Body)
	}
}

// Characterize the credential boundary: Page ownership/Edit grants do not make
// a MEMBER's read-scoped token a live-read credential. Use the real router.
func TestDesignReadScopedMemberEditorsRemainSharedOnly(t *testing.T) {
	for _, audience := range []string{"owner", "edit-grantee"} {
		t.Run(audience, func(t *testing.T) {
			f := newDesignAuthorityFixture(t)
			user := f.owner
			if audience == "owner" {
				mustExec(t, f.h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, f.workspace, user)
			} else {
				user = "read-scoped-member-editor"
				f.addUser(t, user)
				if w := f.grant(t, user, "write"); w.Code != 200 {
					t.Fatal(w.Code, w.Body)
				}
			}
			reader := mintIngressScopedToken(t, f.h.db, user, "member-read-"+audience, "pages:read")
			router, err := NewRouter(f.h.db, "synthetic-design-member-reader-secret-32-chars", newTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			requestWithToken := func(token, method, suffix, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, "/api/v1/pages/board/design"+suffix, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("X-Workspace-ID", f.workspace)
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				return w
			}
			request := func(method, suffix, body string) *httptest.ResponseRecorder {
				return requestWithToken(reader, method, suffix, body)
			}
			writeOnly := mintIngressScopedToken(t, f.h.db, user, "member-write-only-"+audience, "pages:write")
			for _, suffix := range []string{"", "/versions", "/comments"} {
				if w := requestWithToken(writeOnly, "GET", suffix, ""); w.Code != 403 {
					t.Fatalf("write scope implied read: %s %d %s", suffix, w.Code, w.Body)
				}
			}
			for _, suffix := range []string{"", "/versions"} {
				if w := request("GET", suffix, ""); w.Code != 404 {
					t.Fatalf("read-scoped %s exposed unshared %s: %d %s", audience, suffix, w.Code, w.Body)
				}
			}
			var beforePin struct {
				Comments []json.RawMessage `json:"comments"`
			}
			w := request("GET", "/comments", "")
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &beforePin) != nil || len(beforePin.Comments) != 0 {
				t.Fatalf("pre-pin comments were not empty: %d %s", w.Code, w.Body)
			}
			for _, step := range []struct{ op, body string }{
				{"apply", `{"operations":[{"op":"upsert","node":{"id":"title","type":"text","x":0,"y":0,"width":200,"height":30,"text":"PUBLIC_PIN_CANARY"}}]}`},
				{"share", `{"version":2}`},
				{"apply", `{"operations":[{"op":"upsert","node":{"id":"title","text":"PRIVATE_SAVED_CANARY"}}]}`},
			} {
				if w := f.call(t, f.owner, step.op, step.body); w.Code != 200 {
					t.Fatal(step.op, w.Code, w.Body)
				}
				if step.op == "share" {
					if w := f.call(t, f.owner, "comment", `{"version":2,"path":"design.json","selector":"title","quote":"PUBLIC_PIN_CANARY","text":"PUBLIC_COMMENT_CANARY"}`); w.Code != 201 {
						t.Fatal("shared comment setup", w.Code, w.Body)
					}
				}
			}
			for _, comment := range []string{
				`{"version":3,"path":"design.json","selector":"title","quote":"PRIVATE_SAVED_CANARY","text":"PRIVATE_COMMENT_CANARY"}`,
			} {
				if w := f.call(t, f.owner, "comment", comment); w.Code != 201 {
					t.Fatal("comment setup", w.Code, w.Body)
				}
			}
			f.liveOnly(t, `[{"op":"upsert","node":{"id":"secret-live","type":"text","x":0,"y":40,"width":200,"height":30,"text":"PRIVATE_LIVE_CANARY"}}]`)
			w = request("GET", "", "")
			if w.Code != 200 || !strings.Contains(w.Body.String(), "PUBLIC_PIN_CANARY") || strings.Contains(w.Body.String(), "PRIVATE_") || !strings.Contains(w.Body.String(), `"can_edit":false`) || !strings.Contains(w.Body.String(), `"version":2`) {
				t.Fatalf("read-scoped %s escaped pin: %d %s", audience, w.Code, w.Body)
			}
			if w := request("GET", "/comments", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "PUBLIC_COMMENT_CANARY") || strings.Contains(w.Body.String(), "PRIVATE_") {
				t.Fatalf("read-scoped comments escaped shared pin: %d %s", w.Code, w.Body)
			}
			fullRole := mintIngressScopedToken(t, f.h.db, user, "member-unscoped-"+audience, "pages:read")
			mustExec(t, f.h.db, `UPDATE cli_tokens SET scopes='[]' WHERE id=?`, "clt-ingress-scoped-member-unscoped-"+audience)
			legacyNull := mintIngressScopedToken(t, f.h.db, user, "member-legacy-null-"+audience, "pages:read")
			mustExec(t, f.h.db, `UPDATE cli_tokens SET scopes=NULL WHERE id=?`, "clt-ingress-scoped-member-legacy-null-"+audience)
			for _, profile := range []struct{ name, token string }{
				{"pages:*", mintIngressScopedToken(t, f.h.db, user, "member-pages-all-"+audience, "pages:*")},
				{"*", mintIngressScopedToken(t, f.h.db, user, "member-all-"+audience, "*")},
				{"unscoped-empty", fullRole},
				{"unscoped-null", legacyNull},
			} {
				w := requestWithToken(profile.token, "GET", "", "")
				if w.Code != 200 || !strings.Contains(w.Body.String(), "PRIVATE_LIVE_CANARY") || !strings.Contains(w.Body.String(), "PRIVATE_SAVED_CANARY") || !strings.Contains(w.Body.String(), `"can_edit":true`) {
					t.Fatalf("same MEMBER %s %s lost authorized live view: %d %s", audience, profile.name, w.Code, w.Body)
				}
				w = requestWithToken(profile.token, "GET", "/comments", "")
				if w.Code != 200 || !strings.Contains(w.Body.String(), "PUBLIC_COMMENT_CANARY") || !strings.Contains(w.Body.String(), "PRIVATE_COMMENT_CANARY") {
					t.Fatalf("same MEMBER %s %s lost authorized comments: %d %s", audience, profile.name, w.Code, w.Body)
				}
			}
			if w := request("GET", "?version=3", ""); w.Code != 404 {
				t.Fatalf("private version exposed: %d %s", w.Code, w.Body)
			}
			w = request("GET", "/versions", "")
			var history struct {
				Versions []struct {
					Version int64 `json:"version"`
				} `json:"versions"`
				Latest int64 `json:"latest_version"`
			}
			if json.Unmarshal(w.Body.Bytes(), &history) != nil || w.Code != 200 || history.Latest != 2 || len(history.Versions) != 1 || history.Versions[0].Version != 2 {
				t.Fatalf("private history exposed: %d %s", w.Code, w.Body)
			}
			revision, versions := f.revision(t), f.versions(t)
			if w := request("POST", "/operations", `{"operations":[{"op":"delete","id":"title"}]}`); w.Code != 403 {
				t.Fatalf("read token wrote: %d %s", w.Code, w.Body)
			}
			if f.revision(t) != revision || f.versions(t) != versions {
				t.Fatal("denied write changed retained state")
			}
		})
	}
}
