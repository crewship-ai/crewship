package api

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

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
	execOrFatal(t, db, `UPDATE users SET email='OTHER_MEMBER_EMAIL_CANARY@example.test' WHERE id=?`, owner)
	execOrFatal(t, db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,status) VALUES('HIDDEN_ROUTINE_ID',?,'hidden-routine','HIDDEN_ROUTINE_TITLE','{}','hidden','active')`, ws)
	execOrFatal(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('HIDDEN_PAGE_ID',?,'hidden-page','HIDDEN_PAGE_TITLE',?,'{}')`, ws, owner)
	execOrFatal(t, db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('HIDDEN_CREDENTIAL_ID',?,'HIDDEN_CREDENTIAL_TITLE','synthetic-unreadable-cipher','API_KEY','OPENAI',?)`, ws, owner)
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
	// This inventory walk checks response isolation; admit each search after refill.
	// Exhaustion and credential rotation are covered by the limiter tests.
	searchClock := time.Now()
	router.sharedSearch.now = func() time.Time { searchClock = searchClock.Add(time.Minute); return searchClock }
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
	for _, actor := range []string{"project-guest", "second-guest"} {
		walked := 0
		for _, foreignPath := range []bool{false, true} {
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
						if foreignPath {
							return "UNSHARED_ISSUE_CANARY"
						}
						return "shared-issue"
					case "{projectId}":
						if foreignPath {
							return "UNSHARED_PROJECT_CANARY"
						}
						return "shared-project"
					case "{agentId}":
						return "ISSUE_HIDDEN_AGENT_ID"
					case "{credentialId}":
						return "HIDDEN_CREDENTIAL_ID"
					case "{pipelineId}", "{routineId}":
						return "HIDDEN_ROUTINE_ID"
					case "{slug}":
						return "hidden-page"
					default:
						return "probe"
					}
				})
				if !strings.HasPrefix(path, "/") {
					continue
				}
				rr := request(actor, path, 0)
				walked++
				if path == "/api/auth/signin" {
					if rr.Code != 302 {
						t.Errorf("signin redirect status=%d", rr.Code)
					}
				} else {
					switch rr.Code {
					case 200, 204, 400, 401, 403, 404, 405, 422, 503:
					default:
						t.Errorf("unexpected GET status %s %s: %d", actor, path, rr.Code)
					}
				}
				canaries := []string{"OTHER_MEMBER_EMAIL_CANARY", "HIDDEN_ROUTINE_TITLE", "HIDDEN_PAGE_TITLE", "HIDDEN_CREDENTIAL_TITLE", "ISSUE_HIDDEN_CREW_ID", "ISSUE_HIDDEN_AGENT_ID"}
				if actor == "project-guest" {
					canaries = append(canaries, "UNSHARED_PROJECT_CANARY", "UNSHARED_PROJECT_TITLE", "UNSHARED_ISSUE_CANARY", "UNSHARED_ISSUE_TITLE", "UNSHARED_DESCRIPTION_CANARY")
				} else {
					canaries = append(canaries, "shared-issue", "Shared description")
				}
				for _, canary := range canaries {
					if strings.Contains(rr.Body.String(), canary) {
						t.Errorf("GET %s %s leaked %s in response body", actor, path, canary)
					}
				}
			}
		}
		if walked < 200 {
			t.Fatalf("GET inventory walk incomplete for %s: %d", actor, walked)
		}
	}
	execOrFatal(t, db, `INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,identifier,title,description,status,mission_type,project_id) VALUES('second-shared-issue',?,'ISSUE_HIDDEN_CREW_ID','ISSUE_HIDDEN_AGENT_ID','second-trace','SECOND-1','Another shared issue','Brief','TODO','issue','shared-project')`, ws)
	page := request("project-guest", base+"?limit=1", 200)
	if page.Header().Get("X-Total-Count") != "2" {
		t.Fatalf("total count is page length: %s", page.Header().Get("X-Total-Count"))
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
	// The same authenticated Guest loses both direct and bulk projections when
	// the grant issuer is demoted. Stored shares follow current issuer authority.
	replace("project-guest", "shared-project")
	p, err := store.Policy(t.Context(), owner, "project-guest", ws)
	if err != nil {
		t.Fatal(err)
	}
	p.Rights = append(p.Rights, access.Right{Kind: "agent", ID: "ISSUE_HIDDEN_AGENT_ID", Operation: "discover"})
	if _, err = store.Replace(t.Context(), owner, "project-guest", ws, "restricted", p.Membership, p.Rights); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(request("project-guest", "/api/v1/agents", 200).Body.String(), "ISSUE_HIDDEN_AGENT_ID") {
		t.Fatal("authorized agent absent before issuer demotion")
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=? AND workspace_id=?`, owner, ws)
	request("project-guest", base+"/shared-issue", 404)
	for _, path := range []string{base, "/api/v1/agents"} {
		body := request("project-guest", path, 200).Body.String()
		if strings.Contains(body, "shared-issue") || strings.Contains(body, "ISSUE_HIDDEN_AGENT_ID") {
			t.Fatalf("demoted issuer still grants projection %s", path)
		}
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='OWNER' WHERE user_id=? AND workspace_id=?`, owner, ws)
	request("project-guest", base+"/shared-issue", 200)
}
