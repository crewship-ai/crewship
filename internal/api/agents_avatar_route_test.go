package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// Router-level coverage for the persisted-avatar URL (#1307).
//
// Everything in agents_avatar_test.go drives the handlers directly with
// withWorkspaceUser, which injects the workspace into the context — so the
// suite stayed green while the URL the API actually hands to clients was
// unusable. The serve route sits behind RequireWorkspace, which takes the
// workspace from ?workspace_id= → {workspaceId} → X-Workspace-ID; the path
// carries no workspace segment and an <img src> cannot set a header, so a
// URL without the query param 400s for every browser that follows it.
//
// The only way to catch that is to fetch the emitted string through the real
// router, exactly as a client would. That is what this file does.

// avatarRouteEnv builds a real Router over a seeded workspace plus one agent,
// and returns the router, the agent id and a bearer token for the owner.
func avatarRouteEnv(t *testing.T) (*Router, string, string, string) {
	t.Helper()

	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedAgentForStatus(t, &AgentHandler{db: db}, "ag-route-av", wsID, "", "IDLE", false)

	const secret = "test-secret-for-jwt-signing-32chars!!"
	r, err := NewRouter(db, secret, newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	v, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatalf("auth.NewJWTValidator: %v", err)
	}
	sess, err := sessions.NewDBStore(db).Create(t.Context(), userID, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tok, err := v.IssueAccessToken(userID, sess.ID, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}
	return r, "ag-route-av", wsID, tok
}

// TestAgentAvatarURL_IsFetchableThroughRouter is the regression test for
// #1307: the URL returned in avatar_url must be usable as-is. It stores an
// avatar, takes the string the API emitted, and GETs exactly that through the
// real router with nothing but the session cookie an <img> would carry.
func TestAgentAvatarURL_IsFetchableThroughRouter(t *testing.T) {
	r, agentID, wsID, tok := avatarRouteEnv(t)

	body, err := json.Marshal(map[string]string{"svg": testAvatarSVG})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	put := httptest.NewRequest(http.MethodPut,
		"/api/v1/agents/"+agentID+"/avatar?workspace_id="+wsID, strings.NewReader(string(body)))
	put.Header.Set("Authorization", "Bearer "+tok)
	putRR := httptest.NewRecorder()
	r.ServeHTTP(putRR, put)
	if putRR.Code != http.StatusOK {
		t.Fatalf("PUT avatar = %d, want 200; body: %s", putRR.Code, putRR.Body.String())
	}

	var stored struct {
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.Unmarshal(putRR.Body.Bytes(), &stored); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if stored.AvatarURL == "" {
		t.Fatal("PUT returned an empty avatar_url")
	}

	// No X-Workspace-ID header on purpose — an <img src> cannot send one, so
	// the URL has to carry the workspace itself.
	get := httptest.NewRequest(http.MethodGet, stored.AvatarURL, nil)
	get.Header.Set("Authorization", "Bearer "+tok)
	getRR := httptest.NewRecorder()
	r.ServeHTTP(getRR, get)

	if getRR.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 — the avatar_url the API emits must be fetchable as-is (#1307); body: %s",
			stored.AvatarURL, getRR.Code, getRR.Body.String())
	}
	if got := getRR.Body.String(); got != testAvatarSVG {
		t.Errorf("served body = %q, want the stored SVG", got)
	}
	if ct := getRR.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", ct)
	}
}

// TestAgentAvatarURL_ListAndGetEmitFetchableURLs covers the other two places
// the URL reaches a client: the roster list and the single-agent read. Both
// build it through agentAvatarURL, so both must produce something the browser
// can follow.
func TestAgentAvatarURL_ListAndGetEmitFetchableURLs(t *testing.T) {
	r, agentID, wsID, tok := avatarRouteEnv(t)

	body, _ := json.Marshal(map[string]string{"svg": testAvatarSVG})
	put := httptest.NewRequest(http.MethodPut,
		"/api/v1/agents/"+agentID+"/avatar?workspace_id="+wsID, strings.NewReader(string(body)))
	put.Header.Set("Authorization", "Bearer "+tok)
	putRR := httptest.NewRecorder()
	r.ServeHTTP(putRR, put)
	if putRR.Code != http.StatusOK {
		t.Fatalf("PUT avatar = %d, want 200; body: %s", putRR.Code, putRR.Body.String())
	}

	fetch := func(t *testing.T, url string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200; body: %s", url, rr.Code, rr.Body.String())
		}
		return rr.Body.String()
	}

	var single struct {
		AvatarURL *string `json:"avatar_url"`
	}
	if err := json.Unmarshal([]byte(fetch(t, "/api/v1/agents/"+agentID+"?workspace_id="+wsID)), &single); err != nil {
		t.Fatalf("decode agent: %v", err)
	}
	if single.AvatarURL == nil {
		t.Fatal("GET agent returned a null avatar_url after a successful PUT")
	}
	if got := fetch(t, *single.AvatarURL); got != testAvatarSVG {
		t.Errorf("avatar_url from GET agent served %q, want the stored SVG", got)
	}

	var list []struct {
		ID        string  `json:"id"`
		AvatarURL *string `json:"avatar_url"`
	}
	if err := json.Unmarshal([]byte(fetch(t, "/api/v1/agents?workspace_id="+wsID)), &list); err != nil {
		t.Fatalf("decode agent list: %v", err)
	}
	var listed *string
	for _, a := range list {
		if a.ID == agentID {
			listed = a.AvatarURL
		}
	}
	if listed == nil {
		t.Fatal("agent list returned no avatar_url for the seeded agent")
	}
	if got := fetch(t, *listed); got != testAvatarSVG {
		t.Errorf("avatar_url from the agent list served %q, want the stored SVG", got)
	}
}
