package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// The avatar tests that shipped with the feature all call the handler
// directly with withWorkspaceUser, which injects the workspace into the
// request context by hand. That skips the router — and the router is
// where wsCtx lives. So a URL that no client could ever fetch passed
// every test in the suite.
//
// These drive the production Router instead: they take the exact
// avatar_url string the API hands out and fetch it, which is the only
// thing a browser will ever do with it.

// avatarRouteFixture seeds a workspace with one agent that has a stored
// avatar, and returns everything needed to drive the router as that
// agent's owner.
func avatarRouteFixture(t *testing.T) (r http.Handler, token, wsID, agentID string) {
	t.Helper()

	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-av", wsID, "C", "c-avatar")
	agentID = "agent-av"
	seedAgentRow(t, db, agentID, wsID, "crew-av", "A", "a-avatar", "AGENT")

	const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"></svg>`
	if _, err := db.Exec(
		`UPDATE agents SET avatar_svg = ?, avatar_svg_hash = ? WHERE id = ?`,
		svg, agentAvatarHash(svg), agentID,
	); err != nil {
		t.Fatalf("seed avatar: %v", err)
	}

	const secret = "test-secret-for-jwt-signing-32chars!!"
	router, err := NewRouter(db, secret, newTestLogger(),
		WithSocketPath("/tmp/crewship-avatar-route-test.sock"),
		WithInternalToken("internal-test-token"),
		WithInternalBaseURL("http://127.0.0.1:0"),
	)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	v, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatalf("NewJWTValidator: %v", err)
	}
	sess, err := sessions.NewDBStore(db).Create(context.Background(), userID, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token, err = v.IssueAccessToken(userID, sess.ID, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	return router, token, wsID, agentID
}

// TestAvatarURL_IsFetchableThroughRouter is the regression test for the
// bug where avatar_url omitted workspace_id: the route is registered
// behind wsCtx, which requires the workspace from query, path or the
// X-Workspace-ID header. The path carries no {workspaceId} and an <img>
// tag cannot set a header, so every stored avatar 400'd and the client
// silently fell back to generating one — the feature looked fine and did
// nothing.
func TestAvatarURL_IsFetchableThroughRouter(t *testing.T) {
	router, token, wsID, agentID := avatarRouteFixture(t)

	// Ask the API for the agent, exactly as the dashboard does, and take
	// the avatar_url it hands back. Constructing the URL by hand here
	// would test the test, not the contract.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agentID+"?workspace_id="+wsID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET agent: got %d want 200; body=%s", rr.Code, rr.Body.String())
	}

	var agent struct {
		AvatarURL *string `json:"avatar_url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &agent); err != nil {
		t.Fatalf("decode agent: %v", err)
	}
	if agent.AvatarURL == nil || *agent.AvatarURL == "" {
		t.Fatal("agent has a stored avatar but avatar_url is empty")
	}

	// Fetch that URL with nothing but the session credential — no extra
	// query params, no headers a browser would not send.
	imgReq := httptest.NewRequest(http.MethodGet, *agent.AvatarURL, nil)
	imgReq.Header.Set("Authorization", "Bearer "+token)
	imgRR := httptest.NewRecorder()
	router.ServeHTTP(imgRR, imgReq)

	if imgRR.Code != http.StatusOK {
		t.Fatalf("fetching avatar_url %q: got %d want 200; body=%s",
			*agent.AvatarURL, imgRR.Code, imgRR.Body.String())
	}
	if ct := imgRR.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type: got %q want image/svg+xml", ct)
	}
	if !strings.Contains(imgRR.Body.String(), "<svg") {
		t.Errorf("body is not the stored SVG: %q", imgRR.Body.String())
	}
}

// TestAvatarURL_StillScopedToWorkspace guards the fix from being applied
// the lazy way. Dropping wsCtx from the route would also make the URL
// fetchable — and would let any authenticated user read any workspace's
// avatars, because ServeAvatar uses the workspace as its tenant scope
// (SELECT ... WHERE id = ? AND workspace_id = ?).
func TestAvatarURL_StillScopedToWorkspace(t *testing.T) {
	router, token, _, agentID := avatarRouteFixture(t)

	// A syntactically valid workspace the caller is not a member of.
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/agents/"+agentID+"/avatar?v=deadbeef&workspace_id=ws-someone-else", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code == http.StatusOK {
		t.Fatalf("avatar served for a foreign workspace: got 200, body=%s", rr.Body.String())
	}
}

// TestAvatarURL_CarriesWorkspace pins the URL shape itself, so a future
// refactor cannot quietly drop the parameter and reintroduce the bug in
// a place the round-trip test above would not reach (inbox, skills, and
// the agent list all build this URL too).
func TestAvatarURL_CarriesWorkspace(t *testing.T) {
	u := agentAvatarURL("agent-1", "abc123", "ws-42")
	if u == nil {
		t.Fatal("agentAvatarURL returned nil for a stored hash")
	}
	for _, want := range []string{"/api/v1/agents/agent-1/avatar", "v=abc123", "workspace_id=ws-42"} {
		if !strings.Contains(*u, want) {
			t.Errorf("avatar URL %q is missing %q", *u, want)
		}
	}

	if got := agentAvatarURL("agent-1", "", "ws-42"); got != nil {
		t.Errorf("no stored hash must yield nil, got %q", *got)
	}

	// An unknown workspace must not produce a half-built URL that 400s
	// at the middleware; returning nil makes the client generate from
	// the seed, which is the documented fallback.
	if got := agentAvatarURL("agent-1", "abc123", ""); got != nil {
		t.Errorf("empty workspace must yield nil rather than an unfetchable URL, got %q", *got)
	}
}

// TestAvatarURL_ListAndGetEmitFetchableURLs closes the remaining gap: the
// round-trip above proves GET /agents/{id} hands out a working URL, but
// four call sites build this string and the agent LIST is the one a
// roster actually renders. This drives the full lifecycle — PUT the SVG,
// then fetch the URL as emitted by both the single-agent and the list
// endpoint — so a regression in either builder surfaces here.
//
// Adapted from PR #1315, which fixed the same bug independently and
// carried this case; keeping it rather than losing it with the duplicate.
func TestAvatarURL_ListAndGetEmitFetchableURLs(t *testing.T) {
	router, token, wsID, agentID := avatarRouteFixture(t)

	fetch := func(t *testing.T, url string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
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
		t.Fatal("GET agent returned a null avatar_url for an agent with a stored render")
	}
	if got := fetch(t, *single.AvatarURL); !strings.Contains(got, "<svg") {
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
	if got := fetch(t, *listed); !strings.Contains(got, "<svg") {
		t.Errorf("avatar_url from the agent list served %q, want the stored SVG", got)
	}
}
