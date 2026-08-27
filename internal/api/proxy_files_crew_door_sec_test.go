package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// proxy_files_crew_door_sec_test.go — regression pin for #2142.
//
// #2069 taught AgentFileDownload to refuse the six generated per-agent files
// that hold resolved MCP credentials (isProtectedAgentConfigPath), because
// those files are deliberately mode 0600 inside the container and the file
// API must not turn its container-read fallback into a way to get them back
// over HTTP. CrewFileDownload reads the exact same storage — a path of the
// shape "<crewID>/<agentSlug>/.mcp.json" resolves to the identical
// crewshipd storage key on either door — but never ran that check, so
//
//	GET /agents/{agentId}/files/download?path=<crewID>/<slug>/.mcp.json   → 403
//	GET /crews/{crewId}/files/download?path=<crewID>/<slug>/.mcp.json     → served
//
// for the same bytes, gated by the same "read" role. This drives the
// PRODUCTION Router — real middleware chain, real auth, real route table —
// rather than calling the handler function directly, because the bug is
// that a ROUTE was unguarded; calling CrewFileDownload directly would prove
// only that the Go function does the right thing, not that the HTTP surface
// does. The socket path points nowhere: both doors must answer 403 before
// ever dialing crewshipd, exactly like AgentFileDownload always has, so no
// running daemon is needed to observe the fix (or its absence).
//
// On main (pre-fix) the crew-door subtest below fails: it gets a 404 "File
// not found" (the IPC dial to a nonexistent socket erroring out) instead of
// the 403 the agent door already returns for byte-identical input.

func TestCrewDoorHonoursProtectedAgentConfigDenial(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-door-2142", wsID, "Door", "door-2142")
	agentSlug := "riley"
	agentID := seedAgentRow(t, db, "agent-door-2142", wsID, crewID, "Riley", agentSlug, "AGENT")

	const secret = "test-secret-for-jwt-signing-32chars!!"
	router, err := NewRouter(db, secret, newTestLogger(),
		WithSocketPath("/tmp/crewship-crew-door-2142-route-test.sock"),
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
	token, err := v.IssueAccessToken(userID, sess.ID, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	get := func(t *testing.T, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	protectedRelPaths := []string{
		".mcp.json",
		".cursor/mcp.json",
		".factory/mcp.json",
		".gemini/settings.json",
		"opencode.json",
		".codex/config.toml",
	}

	for _, rel := range protectedRelPaths {
		t.Run("crew door/"+rel, func(t *testing.T) {
			fullPath := crewID + "/" + agentSlug + "/" + rel
			u := "/api/v1/crews/" + crewID + "/files/download?workspace_id=" + wsID +
				"&path=" + url.QueryEscape(fullPath)
			rr := get(t, u)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("crew door GET %s = %d, want 403 (protected agent config); body=%s",
					fullPath, rr.Code, rr.Body.String())
			}
		})

		t.Run("agent door/"+rel, func(t *testing.T) {
			// Same bytes, same role requirement, the door #2069 already fixed.
			// Pinned here too so a change that "fixes" the crew door by
			// deleting the agent door's check would still be caught.
			u := "/api/v1/agents/" + agentID + "/files/download?workspace_id=" + wsID +
				"&path=" + url.QueryEscape(rel)
			rr := get(t, u)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("agent door GET %s = %d, want 403 (protected agent config); body=%s",
					rel, rr.Code, rr.Body.String())
			}
		})
	}
}

// TestCrewDoorProtectedConfigDenial_OtherAgentsNamespace pins the case the
// crew door uniquely has to handle: it has no single agent in context, so a
// path can name ANY agent's generated config, not just one the caller
// already knows about. isProtectedAgentConfigPath alone (built for the
// agent door, which checks against exactly one slug) would say "false" for
// a path under a DIFFERENT agent's namespace — that is correct for the
// agent door (a sibling agent's file is refused for a different reason,
// "not scoped to this agent"), but it would be a hole on the crew door,
// which has no such per-agent scoping at all.
func TestCrewDoorProtectedConfigDenial_OtherAgentsNamespace(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-door-2142b", wsID, "Door", "door-2142b")

	const secret = "test-secret-for-jwt-signing-32chars!!"
	router, err := NewRouter(db, secret, newTestLogger(),
		WithSocketPath("/tmp/crewship-crew-door-2142b-route-test.sock"),
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
	token, err := v.IssueAccessToken(userID, sess.ID, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	fullPath := crewID + "/some-other-agent-nobody-registered/.mcp.json"
	u := "/api/v1/crews/" + crewID + "/files/download?workspace_id=" + wsID +
		"&path=" + url.QueryEscape(fullPath)

	req := httptest.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("GET %s = %d, want 403 (protected agent config, any agent slug); body=%s",
			fullPath, rr.Code, rr.Body.String())
	}
}

// TestCrewDoorStillServesOrdinaryFiles guards against the lazy fix: denying
// EVERYTHING through the crew door would also make the protected-path
// subtests above pass. An ordinary crew file must still reach the IPC call
// (and fail there, for lack of a running daemon in this test — the "no such
// socket" pattern the rest of this package's proxy_files tests use) rather
// than being rejected by the new guard.
func TestCrewDoorStillServesOrdinaryFiles(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-door-2142c", wsID, "Door", "door-2142c")

	const secret = "test-secret-for-jwt-signing-32chars!!"
	router, err := NewRouter(db, secret, newTestLogger(),
		WithSocketPath("/tmp/crewship-crew-door-2142c-route-test.sock"),
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
	token, err := v.IssueAccessToken(userID, sess.ID, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	fullPath := crewID + "/riley/report.md"
	u := "/api/v1/crews/" + crewID + "/files/download?workspace_id=" + wsID +
		"&path=" + url.QueryEscape(fullPath)

	req := httptest.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code == http.StatusForbidden {
		t.Fatalf("GET %s was refused as protected agent config; an ordinary crew file must still reach the "+
			"(here, unreachable) daemon instead — body=%s", fullPath, rr.Body.String())
	}
}
