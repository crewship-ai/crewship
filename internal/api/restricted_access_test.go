package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/ws"
)

func TestRestrictedAuthorityAcrossAuthenticatedRoutes(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES ('access-crew',?,'Crew','access-crew')`, workspace)
	seedAgentRow(t, db, "access-a", workspace, "access-crew", "A", "access-a", "AGENT")
	seedAgentRow(t, db, "access-b", workspace, "access-crew", "B", "access-b", "AGENT")
	store := access.Store{DB: db}
	tokens := map[string]string{}
	const secret = "resource-authority-route-test-secret-32"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"access-h1", "access-h2"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES (?,?)`, user, user+"@access.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES (?,?,?,'MEMBER')`, user, workspace, user)
		execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility,title,status) VALUES (?,?,'access-a',?,'private',?,'ACTIVE')`, user+"-chat", workspace, user, user+"-private-canary")
		member, e := store.Membership(t.Context(), user, workspace)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = store.Replace(t.Context(), owner, user, workspace, "restricted", member, []access.Right{{Kind: "agent", ID: "access-a", Operation: "chat"}}); e != nil {
			t.Fatal(e)
		}
		session, e := sessions.NewDBStore(db).Create(t.Context(), user, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		tokens[user], e = validator.IssueAccessToken(user, session.ID, user, user+"@access.test")
		if e != nil {
			t.Fatal(e)
		}
	}
	router, err := NewRouter(db, secret, newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	request := func(user, method, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		req.Header.Set("X-Workspace-ID", workspace)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	for _, user := range []string{"access-h1", "access-h2"} {
		other := "access-h1"
		if user == other {
			other = "access-h2"
		}
		rr := request(user, "GET", "/api/v1/agents/access-a/chats?counts=1")
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), user+"-private-canary") || strings.Contains(rr.Body.String(), other+"-private-canary") || rr.Header().Get("X-Total-Count") != "1" {
			t.Fatalf("own list %s: %d %s", user, rr.Code, rr.Body.String())
		}
		for _, path := range []string{
			"/api/v1/agents/access-b/chats", "/api/v1/agents/access-a/runs",
			"/api/v1/agents/access-a/files", "/api/v1/agents/access-a/logs",
			"/api/v1/crews/access-crew/files", "/api/v1/agents/access-a/credentials",
			"/api/v1/chats/" + other + "-chat/messages", "/api/v1/ws-token",
			"/api/v1/projects", "/api/v1/journal",
		} {
			rr = request(user, "GET", path)
			if rr.Code != http.StatusNotFound {
				t.Errorf("%s %s: status=%d body=%s", user, path, rr.Code, rr.Body.String())
			}
		}
		for _, path := range []string{"/api/v1/agents/access-b/chats", "/api/v1/agents/access-a/start", "/api/v1/assignments"} {
			rr = request(user, "POST", path)
			if rr.Code != http.StatusNotFound {
				t.Errorf("dispatch %s: %d %s", path, rr.Code, rr.Body.String())
			}
		}
		rr = request(user, "GET", "/api/v1/workspaces")
		if rr.Code != 200 || strings.Contains(rr.Body.String(), "agent_count") || strings.Contains(rr.Body.String(), "crew_count") || strings.Contains(rr.Body.String(), "member_count") {
			t.Fatalf("workspace projection leaked broad metadata %d %s", rr.Code, rr.Body.String())
		}
		channels := ws.NewDBChannelAuthorizer(db)
		for _, ch := range []string{"session:" + user + "-chat", "workspace:" + workspace, "agent:access-a", "files:access-crew", "journal:" + workspace, "providers:global"} {
			if allowed, e := channels.CanSubscribe(t.Context(), user, ch); e != nil || allowed {
				t.Errorf("unintegrated stream %s: %v %v", ch, allowed, e)
			}
		}
	}
	member, err := store.Membership(t.Context(), "access-h1", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "access-h1", workspace, "restricted", member, nil); err != nil {
		t.Fatal(err)
	}
	if rr := request("access-h1", "GET", "/api/v1/agents/access-a/chats"); rr.Code != 404 {
		t.Fatalf("revoked list: %d %s", rr.Code, rr.Body.String())
	}
	if rr := request("access-h2", "GET", "/api/v1/agents/access-a/chats"); rr.Code != 200 {
		t.Fatalf("other client affected: %d %s", rr.Code, rr.Body.String())
	}
}
