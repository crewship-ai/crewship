package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type staticArtifactHTTPFixture struct {
	server                   *httptest.Server
	router                   *Router
	ws, owner, token, pageID string
}

func newStaticArtifactHTTPFixture(t *testing.T) staticArtifactHTTPFixture {
	t.Helper()
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	token := mintTokenFor(t, db, owner, "static-artifact-owner-fixture")
	router, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithStoragePath(storageDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	f := staticArtifactHTTPFixture{server: server, router: router, ws: ws, owner: owner, token: token}
	status, raw := f.call(t, "POST", "/api/v1/pages", token, `{"slug":"report","name":"Private Report","panels":[],"allow_empty":true}`)
	if status != 201 {
		t.Fatalf("create staging Page: %d %s", status, raw)
	}
	var page struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	f.pageID = page.ID
	return f
}
func (f staticArtifactHTTPFixture) call(t *testing.T, method, path, token, body string) (int, []byte) {
	t.Helper()
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	req, err := http.NewRequestWithContext(t.Context(), method, f.server.URL+path+separator+"workspace_id="+f.ws, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}
func (f staticArtifactHTTPFixture) mutation(html, id string, base, revision int64) string {
	raw, _ := json.Marshal(map[string]any{"page_id": f.pageID, "html": html, "mutation_id": id, "base_rev": base, "revision": revision})
	return string(raw)
}
func staticArtifactStatus(t *testing.T, status int, raw []byte, want int) {
	t.Helper()
	if status != want {
		t.Fatalf("status%d want%d: %s", status, want, raw)
	}
}

func TestStaticArtifactHTTPRevisionsIdentityAndReadCeiling(t *testing.T) {
	f := newStaticArtifactHTTPFixture(t)
	path := "/api/v1/pages/report/artifact"
	status, raw := f.call(t, "GET", path, f.token, "")
	staticArtifactStatus(t, status, raw, 400)
	status, raw = f.call(t, "GET", path+"?page_id="+f.pageID, f.token, "")
	staticArtifactStatus(t, status, raw, 200)
	first := f.mutation("<h1>Žluťoučký kůň</h1>", "first", 0, 0)
	status, raw = f.call(t, "POST", path+"/revisions", f.token, first)
	staticArtifactStatus(t, status, raw, 201)
	status, raw = f.call(t, "POST", path+"/revisions", f.token, first)
	staticArtifactStatus(t, status, raw, 200)
	status, raw = f.call(t, "POST", path+"/revisions", f.token, f.mutation("different", "first", 0, 0))
	staticArtifactStatus(t, status, raw, 409)
	status, raw = f.call(t, "POST", path+"/revisions", f.token, f.mutation("stale", "second", 0, 0))
	staticArtifactStatus(t, status, raw, 409)
	status, raw = f.call(t, "POST", path+"/revisions", f.token, f.mutation("<p>Second</p>", "second", 1, 0))
	staticArtifactStatus(t, status, raw, 201)
	status, raw = f.call(t, "GET", path+"?page_id="+f.pageID+"&revision=1", f.token, "")
	staticArtifactStatus(t, status, raw, 200)
	if !bytes.Contains(raw, []byte("Žluťoučký")) {
		t.Fatal("historical Czech source lost")
	}
	status, raw = f.call(t, "POST", path+"/restore", f.token, f.mutation("", "restore", 2, 1))
	staticArtifactStatus(t, status, raw, 201)
	status, raw = f.call(t, "GET", path+"?page_id="+f.pageID, f.token, "")
	staticArtifactStatus(t, status, raw, 200)
	var saved struct {
		PageID string `json:"page_id"`
		HTML   string `json:"html"`
		Head   int64  `json:"head_rev"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.PageID != f.pageID || saved.HTML != "<h1>Žluťoučký kůň</h1>" || saved.Head != 3 {
		t.Fatalf("restore identity/source/head: %+v", saved)
	}
	reader := seedRoleMemberToken(t, f.router.db, f.ws, "static-reader", "VIEWER", "static-artifact-reader-fixture")
	if _, err := f.router.db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','static-reader','read',?)`, f.pageID, f.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.db.Exec(`UPDATE cli_tokens SET scopes='["pages:read"]' WHERE user_id='static-reader'`); err != nil {
		t.Fatal(err)
	}
	status, raw = f.call(t, "GET", path+"?page_id="+f.pageID, reader, "")
	staticArtifactStatus(t, status, raw, 200)
	var advisory struct {
		CanWrite bool `json:"can_write"`
	}
	if err := json.Unmarshal(raw, &advisory); err != nil {
		t.Fatal(err)
	}
	if advisory.CanWrite {
		t.Fatal("read-only token advertised writes")
	}
	status, raw = f.call(t, "POST", path+"/revisions", reader, f.mutation("forbidden", "reader-write", 3, 0))
	staticArtifactStatus(t, status, raw, 403)
	status, raw = f.call(t, "PATCH", "/api/v1/pages/report", f.token, `{"panels":[]}`)
	staticArtifactStatus(t, status, raw, 409)
	status, raw = f.call(t, "DELETE", "/api/v1/pages/report", f.token, "")
	staticArtifactStatus(t, status, raw, 204)
	status, raw = f.call(t, "POST", "/api/v1/pages", f.token, `{"slug":"report","name":"Replacement","panels":[],"allow_empty":true}`)
	staticArtifactStatus(t, status, raw, 201)
	var replacement struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.ID == f.pageID {
		t.Fatal("Page identity reused")
	}
	status, raw = f.call(t, "POST", path+"/revisions", f.token, first)
	staticArtifactStatus(t, status, raw, 409)
	status, raw = f.call(t, "GET", path+"?page_id="+f.pageID, f.token, "")
	staticArtifactStatus(t, status, raw, 409)
	var count int
	if err := f.router.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?`, replacement.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale mutation published into replacement: %d %v", count, err)
	}
}

func TestStaticArtifactHTTPStrictMutationEnvelope(t *testing.T) {
	f := newStaticArtifactHTTPFixture(t)
	path := "/api/v1/pages/report/artifact/revisions"
	base := f.mutation("<p>safe</p>", "strict", 0, 0)
	var nullHTML map[string]any
	if err := json.Unmarshal([]byte(base), &nullHTML); err != nil {
		t.Fatal(err)
	}
	nullHTML["html"] = nil
	nullBody, err := json.Marshal(nullHTML)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		strings.Replace(base, `"page_id":`, `"PAGE_ID":`, 1),
		strings.TrimSuffix(base, "}") + `,"page_id":"other"}`,
		strings.Replace(base, `"base_rev":0`, `"base_rev":null`, 1),
		string(nullBody),
		strings.TrimSuffix(base, "}") + `,"unknown":true}`,
		base + `{}`,
	} {
		status, body := f.call(t, "POST", path, f.token, raw)
		staticArtifactStatus(t, status, body, 400)
	}
	var n int
	if err := f.router.db.QueryRow(`SELECT COUNT(*) FROM page_artifacts WHERE page_id=?`, f.pageID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rejected envelope changed head: %d %v", n, err)
	}
	status, body := f.call(t, "POST", path, f.token, base)
	staticArtifactStatus(t, status, body, 201)
}
