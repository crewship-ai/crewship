package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

type revokeProjectWriter struct {
	*httptest.ResponseRecorder
	after func()
}

func (w *revokeProjectWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	if w.after != nil {
		f := w.after
		w.after = nil
		f()
	}
	return n, err
}

func TestProjectFilesAuthenticatedRoutesAndStreamingRevocation(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	store := access.Store{DB: db}
	tokens := map[string]string{}
	const secret = "synthetic-project-file-router-secret-long-enough"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"pf-h1", "pf-h2"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, u, u+"@pf.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MANAGER')`, u, workspace, u)
		project := u + "-project"
		execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES(?,?,?,?)`, project, workspace, u, u)
		m, err := store.Membership(t.Context(), u, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), owner, u, workspace, "restricted", m, []access.Right{{Kind: "project", ID: project, Operation: "read"}, {Kind: "project", ID: project, Operation: "write"}}); err != nil {
			t.Fatal(err)
		}
		session, err := sessions.NewDBStore(db).Create(t.Context(), u, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		tokens[u], err = validator.IssueAccessToken(u, session.ID, u, u+"@pf.test")
		if err != nil {
			t.Fatal(err)
		}
	}
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-project-file-host-token"))
	if err != nil {
		t.Fatal(err)
	}
	request := func(u, method, url, body string, w http.ResponseWriter) {
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokens[u])
		router.ServeHTTP(w, req)
	}
	url := "/api/v1/workspaces/" + workspace + "/projects/pf-h1-project/files"
	body := `{"name":"docs/canary.txt","expected_revision":0,"content_base64":"` + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("H1_PRIVATE_CANARY", 4096))) + `"}`
	r := httptest.NewRecorder()
	request("pf-h1", "POST", url, body, r)
	if r.Code != 201 {
		t.Fatalf("upload %d %s", r.Code, r.Body.String())
	}
	var version access.ProjectFileVersion
	if err = json.Unmarshal(r.Body.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Body.String(), "uploader") || strings.Contains(r.Body.String(), "principal") {
		t.Fatal("uploader identity exposed")
	}
	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRecorder()
		request("pf-h2", method, url, body, r)
		if r.Code != 404 || strings.Contains(r.Body.String(), "CANARY") {
			t.Fatalf("foreign %s %d %s", method, r.Code, r.Body.String())
		}
	}
	r = httptest.NewRecorder()
	request("pf-h1", "POST", url, `{"name":"x","expected_revision":0,"content_base64":"","host_path":"/srv/foreign"}`, r)
	if r.Code != 400 {
		t.Fatalf("callerpath %d", r.Code)
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id='pf-h1'`)
	r = httptest.NewRecorder()
	request("pf-h1", "POST", url, `{"name":"other.txt","expected_revision":0,"content_base64":""}`, r)
	if r.Code != 403 {
		t.Fatalf("grant bypasses role %d %s", r.Code, r.Body.String())
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='MANAGER' WHERE user_id='pf-h1'`)
	r = httptest.NewRecorder()
	request("pf-h1", "POST", url, `{"file_id":"`+version.FileID+`","name":"docs/canary.txt","expected_revision":99,"content_base64":""}`, r)
	if r.Code != 409 {
		t.Fatalf("stale overwrite %d", r.Code)
	}
	stream := &revokeProjectWriter{ResponseRecorder: httptest.NewRecorder(), after: func() {
		execOrFatal(t, db, `DELETE FROM access_grants WHERE project_id='pf-h1-project' AND operation='read'`)
	}}
	request("pf-h1", "GET", url+"/"+version.ID+"/download", "", stream)
	if stream.Code != 200 || stream.Body.Len() != 16384 {
		t.Fatalf("continued revoked stream %d bytes%d", stream.Code, stream.Body.Len())
	}
	r = httptest.NewRecorder()
	request("pf-h1", "GET", url, "", r)
	if r.Code != 404 {
		t.Fatalf("revoked list %d", r.Code)
	}
}
