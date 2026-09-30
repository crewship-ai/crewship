package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func TestProjectInputOptionsExactHumansCurrentSourceAndNativeProfile(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('picker-crew',?,'Picker','picker')`, workspace)
	seedAgentRow(t, db, "picker-agent", workspace, "picker-crew", "Native", "native", "AGENT")
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='native_api_key' WHERE id='picker-agent'`)
	store := access.Store{DB: db}
	const secret = "synthetic-project-picker-session-secret"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	versions := map[string]access.ProjectFileVersion{}
	for _, human := range []string{"picker-h1", "picker-h2"} {
		project := human + "-project"
		execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES(?,?,?,?)`, project, workspace, human+" project", human)
		version, e := store.PutProjectFile(t.Context(), owner, workspace, project, access.ProjectFileWrite{Name: human + "_FILE_CANARY.txt"}, []byte("SOURCE_BYTES_"+human))
		if e != nil {
			t.Fatal(e)
		}
		versions[human] = version
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, human, human+"@picker.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, human, workspace, human)
		execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,'picker-agent',?,'private')`, human+"-chat", workspace, human)
		member, e := store.Membership(t.Context(), human, workspace)
		if e != nil {
			t.Fatal(e)
		}
		_, e = store.Replace(t.Context(), owner, human, workspace, "restricted", member, []access.Right{{Kind: "agent", ID: "picker-agent", Operation: "chat"}, {Kind: "project", ID: project, Operation: "read"}})
		if e != nil {
			t.Fatal(e)
		}
		session, e := sessions.NewDBStore(db).Create(t.Context(), human, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		tokens[human], e = validator.IssueAccessToken(human, session.ID, human, human+"@picker.test")
		if e != nil {
			t.Fatal(e)
		}
	}
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-picker-host-token"))
	if err != nil {
		t.Fatal(err)
	}
	request := func(human, chat, search string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/chats/"+chat+"/project-input-options?workspace_id="+workspace+"&search="+search, nil)
		req.Header.Set("Authorization", "Bearer "+tokens[human])
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	assertOwn := func(human string) {
		t.Helper()
		rec := request(human, human+"-chat", "")
		if rec.Code != 200 {
			t.Fatalf("own options %d %s", rec.Code, rec.Body.String())
		}
		var response ProjectInputOptionsResponse
		if json.Unmarshal(rec.Body.Bytes(), &response) != nil || len(response.Files) != 1 || response.Files[0].ID != versions[human].ID {
			t.Fatalf("wrong options %s", rec.Body.String())
		}
		other := "picker-h2"
		if human == other {
			other = "picker-h1"
		}
		if strings.Contains(rec.Body.String(), other+"_FILE_CANARY") || strings.Contains(rec.Body.String(), "SOURCE_BYTES_") || strings.Contains(rec.Body.String(), "created_by") {
			t.Fatal("foreign bytes or uploader metadata exposed")
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("source options cacheable")
		}
	}
	assertOwn("picker-h1")
	assertOwn("picker-h2")
	if rec := request("picker-h2", "picker-h1-chat", ""); rec.Code != 404 || strings.Contains(rec.Body.String(), "FILE_CANARY") {
		t.Fatalf("foreign chat options %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("picker-h1", "picker-h1-chat", "picker-h2"); rec.Code != 200 || strings.Contains(rec.Body.String(), "FILE_CANARY") {
		t.Fatal("search widened authority")
	}
	var attempts int
	if db.QueryRow(`SELECT COUNT(*) FROM access_attempts`).Scan(&attempts) != nil || attempts != 0 {
		t.Fatal("options read admitted a runtime attempt")
	}
	execOrFatal(t, db, `UPDATE chats SET origin='ROUTINE' WHERE id='picker-h1-chat'`)
	if rec := request("picker-h1", "picker-h1-chat", ""); rec.Code != 404 {
		t.Fatal("read-only routine transcript offered direct inputs")
	}
	execOrFatal(t, db, `UPDATE chats SET origin='CHAT' WHERE id='picker-h1-chat'`)
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='responses_text' WHERE id='picker-agent'`)
	if rec := request("picker-h1", "picker-h1-chat", ""); rec.Code != 404 {
		t.Fatalf("text profile got options %d", rec.Code)
	}
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='native_api_key' WHERE id='picker-agent'`)
	execOrFatal(t, db, `DELETE FROM access_grants WHERE project_id='picker-h1-project' AND operation='read'`)
	if rec := request("picker-h1", "picker-h1-chat", ""); rec.Code != 200 || strings.Contains(rec.Body.String(), "FILE_CANARY") {
		t.Fatal("revoked project source still listed")
	}
	assertOwn("picker-h2")
	execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES('picker-deleted-project',?,'Delete project','delete-project')`, workspace)
	deleted, e := store.PutProjectFile(t.Context(), owner, workspace, "picker-deleted-project", access.ProjectFileWrite{Name: "DELETED_FILE_CANARY.txt"}, []byte("synthetic deleted bytes"))
	if e != nil {
		t.Fatal(e)
	}
	member, e := store.Membership(t.Context(), "picker-h2", workspace)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Replace(t.Context(), owner, "picker-h2", workspace, "restricted", member, []access.Right{{Kind: "agent", ID: "picker-agent", Operation: "chat"}, {Kind: "project", ID: "picker-h2-project", Operation: "read"}, {Kind: "project", ID: deleted.ProjectID, Operation: "read"}}); e != nil {
		t.Fatal(e)
	}
	if rec := request("picker-h2", "picker-h2-chat", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "DELETED_FILE_CANARY") {
		t.Fatal("delete fixture was never readable")
	}
	execOrFatal(t, db, `DELETE FROM projects WHERE id='picker-deleted-project'`)
	if rec := request("picker-h2", "picker-h2-chat", ""); rec.Code != 200 || strings.Contains(rec.Body.String(), "DELETED_FILE_CANARY") {
		t.Fatal("deleted project still selectable")
	}
	assertOwn("picker-h2")
	v := versions["picker-h2"]
	if err = store.RetireProjectFile(t.Context(), owner, workspace, v.ProjectID, v.FileID, v.Revision); err != nil {
		t.Fatal(err)
	}
	if rec := request("picker-h2", "picker-h2-chat", ""); rec.Code != 200 || strings.Contains(rec.Body.String(), "FILE_CANARY") {
		t.Fatal("retired version still selectable")
	}
	execOrFatal(t, db, `UPDATE agents SET deleted_at=datetime('now') WHERE id='picker-agent'`)
	if rec := request("picker-h1", "picker-h1-chat", ""); rec.Code != 404 {
		t.Fatalf("deleted agent options %d", rec.Code)
	}
}
