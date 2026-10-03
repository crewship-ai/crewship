package api

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

// restricted_route_table_test.go pins the restricted-member allowlist
// (restrictedRequest) against the router's REAL route table (#2861).
//
// Every pattern the mux registers is replayed through restrictedRequest as a
// restricted member holding exactly one grant (agent rt-a / chat). The set of
// patterns that pass must equal restrictedAllowedPatterns below, byte for
// byte. A route that silently becomes reachable — or stops being reachable —
// for restricted members fails this test, whatever the shape of the code that
// implements the allowlist.
//
// Changing the allowlist on purpose means editing this golden list in the
// same commit, which makes the permission change visible in review.

// restrictedAllowedPatterns is the sorted golden list of registered patterns
// a restricted member may reach. The two agent-chat patterns are listed
// because the request targets the granted agent; restrictedAgentCheckedPatterns
// covers the denial for every other agent.
var restrictedAllowedPatterns = []string{
	"DELETE /api/v1/auth/cli-tokens/{tokenId}",
	"DELETE /api/v1/chats/{chatId}/restricted-memory/{entryId}",
	"DELETE /api/v1/workspaces/{workspaceId}/projects/{projectId}/files/{fileId}",
	"GET /api/v1/agents",
	"GET /api/v1/agents/{agentId}",
	"GET /api/v1/agents/{agentId}/chats",
	"GET /api/v1/agents/{agentId}/run-profile",
	"GET /api/v1/auth/cli-token/validate",
	"GET /api/v1/auth/cli-tokens",
	"GET /api/v1/auth/sessions",
	"GET /api/v1/chats/{chatId}/execution-profile",
	"GET /api/v1/chats/{chatId}/messages",
	"GET /api/v1/chats/{chatId}/project-input-options",
	"GET /api/v1/chats/{chatId}/restricted-attempts",
	"GET /api/v1/chats/{chatId}/restricted-context",
	"GET /api/v1/chats/{chatId}/restricted-files",
	"GET /api/v1/chats/{chatId}/restricted-files/{fileId}/download",
	"GET /api/v1/pages/{slug}/application/actions/{pendingId}",
	"GET /api/v1/workspaces",
	"GET /api/v1/workspaces/{workspaceId}/projects/{projectId}/files",
	"GET /api/v1/workspaces/{workspaceId}/projects/{projectId}/files/{versionId}/download",
	"GET /api/v1/workspaces/{workspaceId}/restricted-pages",
	"GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs",
	"GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs/{runId}",
	"GET /api/v1/workspaces/{workspaceId}/restricted-routines",
	"POST /api/v1/agents/{agentId}/chats",
	"POST /api/v1/agents/{agentId}/restricted-cli-chats",
	"POST /api/v1/auth/sessions/{id}/revoke",
	"POST /api/v1/chats/{chatId}/restricted-cli-run",
	"POST /api/v1/chats/{chatId}/restricted-memory",
	"POST /api/v1/chats/{chatId}/restricted-run",
	"POST /api/v1/pages/{slug}/application/actions/{panelId}/{actionId}",
	"POST /api/v1/pages/{slug}/panels/{panelId}/actions/{actionId}",
	"POST /api/v1/users/me/password",
	"POST /api/v1/workspaces/{workspaceId}/issues/{issueId}/private-preflight",
	"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run",
	"POST /api/v1/workspaces/{workspaceId}/projects/{projectId}/files",
}

// restrictedAgentCheckedPatterns are the allowlisted patterns that are not a
// blanket pass: the middleware itself requires a current agent/chat grant on
// {agentId} in the agent's own workspace.
var restrictedAgentCheckedPatterns = []string{
	"GET /api/v1/agents/{agentId}/chats",
	"POST /api/v1/agents/{agentId}/chats",
}

type restrictedRouteFixture struct {
	router *Router
	user   string
	member string
}

// newRestrictedRouteFixture seeds one restricted member (granted chat on
// rt-a only), one full member, a sibling agent rt-b in the same workspace, a
// soft-deleted agent rt-gone, and an agent rt-foreign in a workspace the
// restricted member does not belong to.
func newRestrictedRouteFixture(t *testing.T) restrictedRouteFixture {
	t.Helper()
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES ('rt-crew',?,'Crew','rt-crew')`, workspace)
	seedAgentRow(t, db, "rt-a", workspace, "rt-crew", "A", "rt-a", "AGENT")
	seedAgentRow(t, db, "rt-b", workspace, "rt-crew", "B", "rt-b", "AGENT")
	seedAgentRow(t, db, "rt-gone", workspace, "rt-crew", "Gone", "rt-gone", "AGENT")
	execOrFatal(t, db, `UPDATE agents SET deleted_at=CURRENT_TIMESTAMP WHERE id='rt-gone'`)

	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES ('rt-other-ws','Other','rt-other-ws')`)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES ('rt-other-crew','rt-other-ws','Crew','rt-other-crew')`)
	seedAgentRow(t, db, "rt-foreign", "rt-other-ws", "rt-other-crew", "F", "rt-foreign", "AGENT")

	store := access.Store{DB: db}
	for _, u := range []string{"rt-restricted", "rt-member"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES (?,?)`, u, u+"@access.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES (?,?,?,'MEMBER')`, u, workspace, u)
	}
	m, err := store.Membership(t.Context(), "rt-restricted", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "rt-restricted", workspace, "restricted", m, []access.Right{{Kind: "agent", ID: "rt-a", Operation: "chat"}}); err != nil {
		t.Fatal(err)
	}

	router, err := NewRouter(db, "restricted-route-table-test-secret-32b", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	return restrictedRouteFixture{router: router, user: "rt-restricted", member: "rt-member"}
}

// registeredPatterns rebuilds every pattern string exactly as it was handed
// to the mux — which is what http.Request.Pattern carries at request time.
func registeredPatterns(r *Router) []string {
	var out []string
	for path, methods := range r.mux.Routes() {
		for _, m := range methods {
			if m == "*" {
				out = append(out, path)
				continue
			}
			out = append(out, m+" "+path)
		}
	}
	sort.Strings(out)
	return out
}

// restrictedDecision replays one matched pattern through the middleware's
// allowlist and reports whether it let the request through.
func (f restrictedRouteFixture) restrictedDecision(t *testing.T, user, pattern, agentID string) (bool, int) {
	t.Helper()
	method, _ := splitPattern(pattern)
	if method == "" {
		method = http.MethodGet
	}
	req := httptest.NewRequest(method, "/restricted-route-table-probe", nil)
	req.Pattern = pattern
	req.SetPathValue("agentId", agentID)
	rr := httptest.NewRecorder()
	ok := f.router.authMw.restrictedRequest(rr, req, user)
	return ok, rr.Code
}

func TestRestrictedRouteTableSnapshot(t *testing.T) {
	f := newRestrictedRouteFixture(t)
	patterns := registeredPatterns(f.router)
	if len(patterns) < 100 {
		t.Fatalf("route enumeration looks broken: only %d patterns", len(patterns))
	}

	var allowed []string
	for _, p := range patterns {
		ok, code := f.restrictedDecision(t, f.user, p, "rt-a")
		if ok {
			allowed = append(allowed, p)
			continue
		}
		if code != http.StatusNotFound {
			t.Errorf("denied %q answered %d, want 404 (deny must not disclose)", p, code)
		}
	}
	sort.Strings(allowed)

	want := map[string]bool{}
	for _, p := range restrictedAllowedPatterns {
		want[p] = true
	}
	got := map[string]bool{}
	for _, p := range allowed {
		got[p] = true
		if !want[p] {
			t.Errorf("newly ALLOWED for restricted members: %q", p)
		}
	}
	for _, p := range restrictedAllowedPatterns {
		if !got[p] {
			t.Errorf("no longer allowed for restricted members: %q", p)
		}
	}
	if !sort.StringsAreSorted(restrictedAllowedPatterns) {
		t.Error("restrictedAllowedPatterns must stay sorted")
	}
	if t.Failed() {
		t.Logf("current allowed set:\n\t%q", strings.Join(allowed, "\",\n\t\""))
	}

	// A full (non-restricted) member is not subject to the allowlist at all.
	for _, p := range patterns {
		if ok, code := f.restrictedDecision(t, f.member, p, "rt-b"); !ok {
			t.Errorf("full member denied %q (%d)", p, code)
		}
	}
}

// TestRestrictedAllowlistPatternsAreRegistered catches typos and dead
// entries: restrictedRoutes must equal the golden list, and every
// allowlisted pattern must be a real registration, and a
// request shaped like it must resolve on the mux to exactly that pattern
// string (the value restrictedRequest switches on).
func TestRestrictedAllowlistPatternsAreRegistered(t *testing.T) {
	f := newRestrictedRouteFixture(t)
	registered := map[string]bool{}
	for _, p := range registeredPatterns(f.router) {
		registered[p] = true
	}
	// The declared table must be exactly the golden list, and only the
	// grant-checked entries may carry a check.
	golden := map[string]bool{}
	for _, p := range restrictedAllowedPatterns {
		golden[p] = true
	}
	checked := map[string]bool{}
	for _, p := range restrictedAgentCheckedPatterns {
		checked[p] = true
	}
	for p, rule := range restrictedRoutes {
		if !golden[p] {
			t.Errorf("restrictedRoutes entry %q is missing from the golden list", p)
		}
		if (rule.check != nil) != checked[p] {
			t.Errorf("restrictedRoutes entry %q: has check=%v, want %v", p, rule.check != nil, checked[p])
		}
	}
	if len(restrictedRoutes) != len(restrictedAllowedPatterns) {
		t.Errorf("restrictedRoutes has %d entries, golden list %d", len(restrictedRoutes), len(restrictedAllowedPatterns))
	}

	for _, p := range restrictedAllowedPatterns {
		if !registered[p] {
			t.Errorf("allowlisted pattern %q is not registered on the mux", p)
			continue
		}
		method, path := splitPattern(p)
		req := httptest.NewRequest(method, pathParamSub.ReplaceAllString(path, "probe"), nil)
		if _, matched := f.router.mux.Handler(req); matched != p {
			t.Errorf("request for %q resolves to pattern %q", p, matched)
		}
	}
}

// TestRestrictedAgentCheckedRoutes pins the two allowlist entries that carry
// their own grant check: only the granted agent passes; a sibling agent, a
// soft-deleted agent, an unknown agent, and an agent in a foreign workspace
// all deny with 404.
func TestRestrictedAgentCheckedRoutes(t *testing.T) {
	f := newRestrictedRouteFixture(t)
	cases := []struct {
		name  string
		agent string
		allow bool
	}{
		{"granted agent", "rt-a", true},
		{"sibling agent without grant", "rt-b", false},
		{"soft-deleted agent", "rt-gone", false},
		{"unknown agent", "rt-missing", false},
		{"agent in foreign workspace", "rt-foreign", false},
		{"empty agent id", "", false},
	}
	for _, p := range restrictedAgentCheckedPatterns {
		for _, c := range cases {
			t.Run(p+"/"+c.name, func(t *testing.T) {
				ok, code := f.restrictedDecision(t, f.user, p, c.agent)
				if ok != c.allow {
					t.Fatalf("allow=%v, want %v (code %d)", ok, c.allow, code)
				}
				if !ok && code != http.StatusNotFound {
					t.Fatalf("deny answered %d, want 404", code)
				}
			})
		}
	}
	// Same path, wrong method: no blanket pass for unregistered method shapes.
	for _, p := range []string{"PUT /api/v1/agents/{agentId}/chats", "DELETE /api/v1/agents/{agentId}/chats", "GET /api/v1/agents/{agentId}/chats/", "GET /api/v1/agents/{agentId}/chat"} {
		if ok, _ := f.restrictedDecision(t, f.user, p, "rt-a"); ok {
			t.Errorf("unexpected allow for %q", p)
		}
	}
}
