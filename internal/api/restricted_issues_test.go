package api

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestGuestProjectIssuesAndResponseBodyLeakWalk(t *testing.T) {
	db := setupTestDB(t)
	owner, ws := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('project-guest','project-guest@example.test'),('second-guest','second-guest@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('guest-membership',?,'project-guest','VIEWER'),('second-membership',?,'second-guest','VIEWER')`, ws, ws)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('ISSUE_HIDDEN_CREW_ID',?,'Crew','issue-crew')`, ws)
	seedAgentRow(t, db, "ISSUE_HIDDEN_AGENT_ID", ws, "ISSUE_HIDDEN_CREW_ID", "Worker", "issue-agent", "AGENT")
	execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES('shared-project',?,'Shared project','shared-project'),('UNSHARED_PROJECT_CANARY',?,'UNSHARED_PROJECT_TITLE','private-project')`, ws, ws)
	for _, row := range []struct{ id, project, title, description string }{
		{"shared-issue", "shared-project", "Shared issue", "Shared description"},
		{"UNSHARED_ISSUE_CANARY", "UNSHARED_PROJECT_CANARY", "UNSHARED_ISSUE_TITLE", "UNSHARED_DESCRIPTION_CANARY"},
	} {
		execOrFatal(t, db, `INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,identifier,title,description,status,mission_type,project_id) VALUES(?,?,'ISSUE_HIDDEN_CREW_ID','ISSUE_HIDDEN_AGENT_ID',?,?,?,?,'TODO','issue',?)`, row.id, ws, row.id+"-trace", row.id, row.title, row.description, row.project)
	}
	store := access.Store{DB: db}
	replace := func(user, project string) {
		t.Helper()
		m, err := store.Membership(t.Context(), user, ws)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.Replace(t.Context(), owner, user, ws, "restricted", m, []access.Right{{Kind: "project", ID: project, Operation: "list"}, {Kind: "project", ID: project, Operation: "read"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	replace("project-guest", "shared-project")
	replace("second-guest", "UNSHARED_PROJECT_CANARY")
	router, err := NewRouter(db, "guest-project-leak-test-secret-long-enough", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	tokens := map[string]string{"project-guest": mintTokenFor(t, db, "project-guest", "project-guest-"+strings.Repeat("0", 40)), "second-guest": mintTokenFor(t, db, "second-guest", "second-guest-"+strings.Repeat("0", 40))}
	request := func(user, path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		req.Header.Set("X-Workspace-ID", ws)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if want != 0 && rr.Code != want {
			t.Fatalf("%s %s = %d want %d: %s", user, path, rr.Code, want, rr.Body.String())
		}
		return rr
	}
	base := "/api/v1/workspaces/" + ws + "/restricted-issues"
	list := request("project-guest", base, 200)
	var items []restrictedIssue
	if err = json.Unmarshal(list.Body.Bytes(), &items); err != nil || len(items) != 1 || items[0].ID != "shared-issue" || items[0].Description != nil {
		t.Fatalf("list projection=%+v %v", items, err)
	}
	detail := request("project-guest", base+"/shared-issue", 200)
	if !strings.Contains(detail.Body.String(), "Shared description") {
		t.Fatal("authorized detail missing description")
	}
	request("project-guest", base+"/UNSHARED_ISSUE_CANARY", 404)
	request("project-guest", base+"?project_id=UNSHARED_PROJECT_CANARY", 404)
	request("second-guest", base+"/shared-issue", 404)
	// The second client's catalog is independently filtered.
	second := request("second-guest", base, 200)
	if strings.Contains(second.Body.String(), "shared-issue") {
		t.Fatal("second client received first client's issue")
	}
	placeholders := regexp.MustCompile(`\{([^}]+)\}`)
	walked := 0
	for _, pattern := range registeredPatterns(router) {
		method, path := splitPattern(pattern)
		if method != "GET" {
			continue
		}
		path = placeholders.ReplaceAllStringFunc(path, func(value string) string {
			switch value {
			case "{workspaceId}":
				return ws
			case "{issueId}", "{identifier}", "{missionId}":
				return "shared-issue"
			case "{projectId}":
				return "shared-project"
			default:
				return "probe"
			}
		})
		if !strings.HasPrefix(path, "/") {
			continue
		}
		rr := request("project-guest", path, 0)
		walked++
		for _, canary := range []string{"UNSHARED_PROJECT_CANARY", "UNSHARED_PROJECT_TITLE", "UNSHARED_ISSUE_CANARY", "UNSHARED_ISSUE_TITLE", "UNSHARED_DESCRIPTION_CANARY", "ISSUE_HIDDEN_CREW_ID", "ISSUE_HIDDEN_AGENT_ID"} {
			if strings.Contains(rr.Body.String(), canary) {
				t.Errorf("GET %s leaked %s in response body", path, canary)
			}
		}
	}
	if walked < 100 {
		t.Fatalf("GET inventory walk incomplete: %d routes", walked)
	}
	// Revocation affects the same bearer identity immediately.
	m, err := store.Membership(t.Context(), "project-guest", ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "project-guest", ws, "restricted", m, nil); err != nil {
		t.Fatal(err)
	}
	request("project-guest", base+"/shared-issue", 404)
	list = request("project-guest", base, 200)
	if strings.Contains(list.Body.String(), "shared-issue") {
		t.Fatal("revoked issue remained in catalog")
	}
}
