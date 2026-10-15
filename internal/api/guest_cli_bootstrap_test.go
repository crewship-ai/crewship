package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func TestGuestCanBootstrapOwnStandardCLIFromVerifiedSession(t *testing.T) {
	db := setupTestDB(t)
	owner, workspace := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('cli-guest','cli-guest@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('cli-guest-member',?,'cli-guest','VIEWER')`, workspace)
	store := access.Store{DB: db}
	member, err := store.Membership(t.Context(), "cli-guest", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "cli-guest", workspace, "restricted", member, nil); err != nil {
		t.Fatal(err)
	}
	const secret = "guest-cli-bootstrap-real-session-secret"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(db).Create(t.Context(), "cli-guest", "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	jwt, err := validator.IssueAccessToken("cli-guest", session.ID, "Guest", "cli-guest@example.test")
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-internal-token"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	call := func(token, method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", workspace)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("%s %s = %d want %d: %s", method, path, rr.Code, want, rr.Body.String())
		}
		return rr
	}
	created := call(jwt, "POST", "/api/v1/auth/cli-token", `{"name":"Client CLI","user_id":"`+owner+`"}`, 200)
	var result struct{ ID, Token string }
	if err = json.Unmarshal(created.Body.Bytes(), &result); err != nil || !strings.HasPrefix(result.Token, "crewship_cli_") {
		t.Fatal("standard token was not issued", err)
	}
	var principal string
	if err = db.QueryRow(`SELECT user_id FROM cli_tokens WHERE id=?`, result.ID).Scan(&principal); err != nil || principal != "cli-guest" {
		t.Fatalf("token principal %q %v", principal, err)
	}
	// The minted token is useful, but retains the account-wide Guest ceiling.
	call(result.Token, "GET", "/api/v1/workspaces/"+workspace+"/access/me", "", 200)
	call(result.Token, "GET", "/api/v1/workspaces/"+workspace+"/members", "", 404)
	// A bearer token cannot mint a wider successor through this new entrypoint.
	call(result.Token, "POST", "/api/v1/auth/cli-token", `{"name":"Remint"}`, 404)
	call(jwt, "POST", "/api/v1/auth/cli-token", `{"tier":"ADMIN"}`, 403)
	call(jwt, "POST", "/api/v1/auth/cli-token", `{"scopes":["*"]}`, 403)
	call(result.Token, "DELETE", "/api/v1/auth/cli-tokens/"+result.ID, "", 200)
	call(result.Token, "GET", "/api/v1/workspaces/"+workspace+"/access/me", "", 401)
}
