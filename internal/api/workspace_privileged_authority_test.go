package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// Workspace ownership does not confer instance-level runtime authority.
// The fixture uses no Docker operations or real credentials.
func TestWorkspaceOwnerCannotSelfAuthorizeHostPrivileges(t *testing.T) {
	db := setupTestDB(t)
	execOrFatal(t, db, `INSERT INTO users(id,email,instance_role) VALUES ('real-operator','operator@example.test','ADMIN')`)
	user := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, user)
	isAdmin, _, err := instanceAdminStatus(t.Context(), db, user, "test@example.com")
	if err != nil || isAdmin {
		t.Fatalf("fixture must not be an instance admin: %v %v", isAdmin, err)
	}
	const secret = "runtime-authority-test-session-secret-32"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(db).Create(t.Context(), user, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken(user, session.ID, user, "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, secret, newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	for _, tc := range []struct{ method, path, body string }{
		{"PATCH", "/api/v1/workspaces/" + workspace, `{"allow_privileged_credentials":true}`},
		{"POST", "/api/v1/crews", `{"name":"Runtime boundary fixture","slug":"runtime-boundary-fixture","devcontainer_config":"{\"image\":\"debian:bookworm-slim\",\"privileged\":true}"}`},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Workspace-ID", workspace)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Errorf("non-instance-admin %s %s: got %d, want 403", tc.method, tc.path, w.Code)
		}
	}
	var enabled, crews int
	if err := db.QueryRow(`SELECT allow_privileged_credentials FROM workspaces WHERE id=?`, workspace).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Errorf("rejected enable changed workspace flag to %d", enabled)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM crews WHERE workspace_id=?`, workspace).Scan(&crews); err != nil {
		t.Fatal(err)
	}
	if crews != 0 {
		t.Errorf("rejected privileged configuration created %d crews", crews)
	}
	// Positive control: the same workspace role can authorize the operation
	// only after receiving explicit instance authority from the fixture.
	execOrFatal(t, db, `UPDATE users SET instance_role='ADMIN' WHERE id=?`, user)
	r := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+workspace, strings.NewReader(`{"allow_privileged_credentials":true}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Workspace-ID", workspace)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("instance admin cannot enable: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("POST", "/api/v1/crews", strings.NewReader(`{"name":"Approved runtime fixture","slug":"approved-runtime-fixture","devcontainer_config":"{\"image\":\"debian:bookworm-slim\",\"privileged\":true}"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Workspace-ID", workspace)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("instance admin cannot save approved privileged configuration: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		name, scopes string
		want         int
	}{
		{"workspace-only", `["workspace:admin"]`, 403},
		{"instance-and-workspace", `["workspace:admin","instance:admin"]`, 200},
	} {
		cliToken := "crewship_cli_runtime_authority_" + tc.name
		execOrFatal(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,scopes,created_at) VALUES (?,?,?,?,?,datetime('now'))`, tc.name, user, tc.name, sha256Hex(cliToken), tc.scopes)
		r = httptest.NewRequest("PATCH", "/api/v1/workspaces/"+workspace, strings.NewReader(`{"allow_privileged_credentials":true}`))
		r.Header.Set("Authorization", "Bearer "+cliToken)
		r.Header.Set("X-Workspace-ID", workspace)
		r.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s token: got %d want %d", tc.name, w.Code, tc.want)
		}
	}
	// Turning off an unsafe workspace opt-in must remain available to its
	// workspace administrator even after instance authority is removed.
	execOrFatal(t, db, `UPDATE users SET instance_role=NULL WHERE id=?`, user)
	r = httptest.NewRequest("PATCH", "/api/v1/workspaces/"+workspace, strings.NewReader(`{"allow_privileged_credentials":false}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Workspace-ID", workspace)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("workspace owner cannot disable: %d %s", w.Code, w.Body.String())
	}
}
