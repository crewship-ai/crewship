package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// TestRestrictedCatalogsNameMissingRuntimeOnlyToRestrictedMembers pins #2877:
// with no private runtime installed, a restricted member's catalog and run
// endpoints say so with a machine code, while every caller that is not a
// restricted member of the workspace still gets the same opaque refusal it
// got before, carrying no code.
func TestRestrictedCatalogsNameMissingRuntimeOnlyToRestrictedMembers(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	store := access.Store{DB: db}
	const secret = "restricted-runtime-missing-test-secret-32"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{"rt-restricted", "rt-trusted"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES (?,?)`, user, user+"@rt.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,capabilities) VALUES (?,?,?,'MEMBER','["routine.run"]')`, user, workspace, user)
		session, e := sessions.NewDBStore(db).Create(t.Context(), user, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		if tokens[user], e = validator.IssueAccessToken(user, session.ID, user, user+"@rt.test"); e != nil {
			t.Fatal(e)
		}
	}
	member, err := store.Membership(t.Context(), "rt-restricted", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "rt-restricted", workspace, "restricted", member, nil); err != nil {
		t.Fatal(err)
	}
	// No SetRestrictedWorkflow: this server has no private runtime.
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
	code := func(rr *httptest.ResponseRecorder) string {
		t.Helper()
		var body struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		return body.Code
	}

	for _, tc := range []struct {
		name, method, path string
	}{
		{"routine catalog", http.MethodGet, "/api/v1/workspaces/" + workspace + "/restricted-routines"},
		{"page catalog", http.MethodGet, "/api/v1/workspaces/" + workspace + "/restricted-pages"},
		{"routine run", http.MethodPost, "/api/v1/workspaces/" + workspace + "/pipelines/any-routine/run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := request("rt-restricted", tc.method, tc.path)
			if rr.Code != http.StatusServiceUnavailable || code(rr) != "restricted_runtime_unavailable" {
				t.Fatalf("restricted member without runtime: %d %s, want 503 restricted_runtime_unavailable", rr.Code, rr.Body.String())
			}
		})
	}

	// The catalogs are restricted-only projections: a trusted member is refused
	// exactly as before, and nothing tells them whether the runtime exists.
	for _, path := range []string{
		"/api/v1/workspaces/" + workspace + "/restricted-routines",
		"/api/v1/workspaces/" + workspace + "/restricted-pages",
	} {
		rr := request("rt-trusted", http.MethodGet, path)
		if rr.Code != http.StatusNotFound || code(rr) != "" {
			t.Fatalf("trusted member %s: %d %s, want opaque 404", path, rr.Code, rr.Body.String())
		}
	}
}
