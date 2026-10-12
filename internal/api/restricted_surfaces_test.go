package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// restrictedSharedPatterns are allowlisted routes that serve several surfaces
// (or a surface's optional parts) rather than defining one. Naming them here is
// what keeps the next allowlist entry from silently appearing in no surface.
var restrictedSharedPatterns = map[string]bool{
	"GET /api/v1/workspaces":                                                               true,
	"GET /api/v1/agents/{agentId}":                                                         true,
	"GET /api/v1/agents/{agentId}/run-profile":                                             true,
	"POST /api/v1/agents/{agentId}/restricted-cli-chats":                                   true,
	"GET /api/v1/auth/cli-token/validate":                                                  true,
	"GET /api/v1/chats/{chatId}/project-input-options":                                     true,
	"GET /api/v1/chats/{chatId}/restricted-context":                                        true,
	"POST /api/v1/chats/{chatId}/restricted-memory":                                        true,
	"DELETE /api/v1/chats/{chatId}/restricted-memory/{entryId}":                            true,
	"GET /api/v1/chats/{chatId}/restricted-files":                                          true,
	"GET /api/v1/chats/{chatId}/restricted-files/{fileId}/download":                        true,
	"POST /api/v1/chats/{chatId}/restricted-run":                                           true,
	"POST /api/v1/chats/{chatId}/restricted-cli-run":                                       true,
	"GET /api/v1/chats/{chatId}/restricted-attempts":                                       true,
	"GET /api/v1/workspaces/{workspaceId}/projects/{projectId}/files":                      true,
	"GET /api/v1/workspaces/{workspaceId}/projects/{projectId}/files/{versionId}/download": true,
	"POST /api/v1/workspaces/{workspaceId}/projects/{projectId}/files":                     true,
	"DELETE /api/v1/workspaces/{workspaceId}/projects/{projectId}/files/{fileId}":          true,
	"POST /api/v1/workspaces/{workspaceId}/issues/{issueId}/private-preflight":             true,
	"GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs":                         true,
	"GET /api/v1/pages/{slug}/application/actions/{pendingId}":                             true,
}

// TestRestrictedSurfacesAreDerivedFromTheAllowlist holds the shell's surfaces
// and restrictedRoutes — the sole allowlist authority — in step both ways.
func TestRestrictedSurfacesAreDerivedFromTheAllowlist(t *testing.T) {
	claimed := map[string]bool{}
	for _, s := range restrictedSurfaces {
		for _, p := range s.Patterns {
			if _, ok := restrictedRoutes[p]; !ok {
				t.Errorf("surface %s needs %q, which restrictedRoutes does not allow", s.Name, p)
			}
			claimed[p] = true
		}
	}
	for p := range restrictedRoutes {
		if !claimed[p] && !restrictedSharedPatterns[p] {
			t.Errorf("allowlisted %q belongs to no surface and is not named shared; add it to a surface or restrictedSharedPatterns", p)
		}
	}
	for p := range restrictedSharedPatterns {
		if _, ok := restrictedRoutes[p]; !ok {
			t.Errorf("restrictedSharedPatterns names %q, which is not allowlisted", p)
		}
	}
	want := []string{"chat", "routines", "pages", "account_security"}
	if got := allowedRestrictedSurfaces(); !reflect.DeepEqual(got, want) {
		t.Fatalf("allowed surfaces = %v, want %v", got, want)
	}

	// A surface loses its place the moment one of its routes leaves the
	// allowlist.
	saved := restrictedRoutes["GET /api/v1/workspaces/{workspaceId}/restricted-pages"]
	delete(restrictedRoutes, "GET /api/v1/workspaces/{workspaceId}/restricted-pages")
	defer func() { restrictedRoutes["GET /api/v1/workspaces/{workspaceId}/restricted-pages"] = saved }()
	if got := allowedRestrictedSurfaces(); !reflect.DeepEqual(got, []string{"chat", "routines", "account_security"}) {
		t.Fatalf("surfaces without the Pages catalog = %v", got)
	}
}

// TestRestrictedWorkspaceRowsCarryTheSessionSurfaces pins the API contract
// the shell reads: a restricted account's workspace rows say the session is
// restricted and which surfaces it may open; a trusted account's rows do not.
func TestRestrictedWorkspaceRowsCarryTheSessionSurfaces(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	store := access.Store{DB: db}
	const secret = "restricted-surfaces-test-secret-32-chars"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{"rs-restricted", "rs-trusted"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES (?,?)`, user, user+"@rs.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES (?,?,?,'MEMBER')`, user, workspace, user)
		session, e := sessions.NewDBStore(db).Create(t.Context(), user, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		if tokens[user], e = validator.IssueAccessToken(user, session.ID, user, user+"@rs.test"); e != nil {
			t.Fatal(e)
		}
	}
	member, err := store.Membership(t.Context(), "rs-restricted", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "rs-restricted", workspace, "restricted", member, nil); err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, secret, newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	list := func(user string) []map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil)
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: GET /workspaces = %d %s", user, rr.Code, rr.Body.String())
		}
		var rows []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
			t.Fatalf("%s: %v %s", user, err, rr.Body.String())
		}
		return rows
	}

	rows := list("rs-restricted")
	if len(rows) != 1 || rows[0]["restricted_session"] != true {
		t.Fatalf("restricted rows = %+v, want restricted_session true", rows)
	}
	got, _ := rows[0]["restricted_surfaces"].([]any)
	want := []any{"chat", "routines", "pages", "account_security"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restricted_surfaces = %v, want %v", got, want)
	}

	for _, row := range list("rs-trusted") {
		if _, ok := row["restricted_session"]; ok {
			t.Fatalf("trusted row carries restricted_session: %+v", row)
		}
		if _, ok := row["restricted_surfaces"]; ok {
			t.Fatalf("trusted row carries restricted_surfaces: %+v", row)
		}
	}
}
