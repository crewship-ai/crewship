package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScopedBearerCannotMintSuccessorCLITokens(t *testing.T) {
	for _, scopes := range []string{`["agents:write"]`, `["workspace:admin"]`, `["agents:*"]`} {
		t.Run(scopes, func(t *testing.T) {
			db := setupTestDB(t)
			owner, workspace := seedInviter(t, db)
			token := mintTokenFor(t, db, owner, strings.Repeat("a", 64))
			execOrFatal(t, db, `UPDATE cli_tokens SET scopes=? WHERE user_id=?`, scopes, owner)
			router, err := NewRouter(db, "scoped-token-mint-fixture-secret-long", newTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			for _, body := range []string{`{"name":"Successor"}`, `{"name":"Successor","scopes":["*"]}`, `{"name":`} {
				req := httptest.NewRequest("POST", "/api/v1/auth/cli-token", strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("X-Workspace-ID", workspace)
				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)
				if rr.Code != 403 {
					t.Fatalf("scoped bearer issued a successor: HTTP %d, want403", rr.Code)
				}
			}
			var count int
			if err = db.QueryRow(`SELECT COUNT(*) FROM cli_tokens WHERE user_id=?`, owner).Scan(&count); err != nil || count != 1 {
				t.Fatalf("unexpected credentials persisted: %d %v", count, err)
			}
		})
	}
}

func TestFullTrustedBearerRetainsOwnTokenCreation(t *testing.T) {
	for _, scopes := range []string{"", `[]`, `["*"]`} {
		t.Run(scopes, func(t *testing.T) {
			db := setupTestDB(t)
			owner, _ := seedInviter(t, db)
			token := mintTokenFor(t, db, owner, strings.Repeat("b", 64))
			if scopes != "" {
				execOrFatal(t, db, `UPDATE cli_tokens SET scopes=? WHERE user_id=?`, scopes, owner)
			}
			router, err := NewRouter(db, "full-token-mint-fixture-secret-long", newTestLogger())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			req := httptest.NewRequest("POST", "/api/v1/auth/cli-token", strings.NewReader(`{"name":"Owned successor","scopes":["agents:write"]}`))
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != 200 {
				t.Fatalf("full trusted credential failed to create own token: HTTP %d", rr.Code)
			}
			var child struct{ Token string }
			if err = json.Unmarshal(rr.Body.Bytes(), &child); err != nil || child.Token == "" {
				t.Fatal("narrow child response unavailable", err)
			}
			request := httptest.NewRequest("POST", "/api/v1/auth/cli-token", strings.NewReader(`{"name":"Unauthorized grandchild"}`))
			request.Header.Set("Authorization", "Bearer "+child.Token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != 403 {
				t.Fatalf("narrow child created successor: HTTP %d", response.Code)
			}
		})
	}
}
