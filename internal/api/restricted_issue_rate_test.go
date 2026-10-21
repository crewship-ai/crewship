package api

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func TestSharedIssueSearchBudgetSharedBySessionAndCLITokens(t *testing.T) {
	db := setupTestDB(t)
	owner, ws := seedInviter(t, db)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('search-rate-user','search-rate@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('search-rate-member',?,'search-rate-user','VIEWER')`, ws)
	execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES('search-rate-project',?,'Shared','shared')`, ws)
	store := access.Store{DB: db}
	member, err := store.Membership(t.Context(), "search-rate-user", ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "search-rate-user", ws, "restricted", member, []access.Right{{Kind: "project", ID: "search-rate-project", Operation: "list"}}); err != nil {
		t.Fatal(err)
	}
	const secret = "synthetic-shared-search-budget-secret"
	router, err := NewRouter(db, secret, newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	now := time.Now()
	router.sharedSearch.now = func() time.Time { return now }
	cli := mintTokenFor(t, db, "search-rate-user", "search-budget-"+strings.Repeat("a", 40))
	cli2 := "crewship_cli_search-budget-" + strings.Repeat("b", 40)
	execOrFatal(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash) VALUES('search-rate-token2','search-rate-user','second',?)`, sha256Hex(cli2))
	session, err := sessions.NewDBStore(db).Create(t.Context(), "search-rate-user", "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	jwt, err := validator.IssueAccessToken("search-rate-user", session.ID, "Guest", "search-rate@example.test")
	if err != nil {
		t.Fatal(err)
	}
	credentials := []string{cli, cli2, jwt}
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest("GET", "/api/v1/workspaces/"+ws+"/restricted-issues?q=term"+strconv.Itoa(i), nil)
		req.Header.Set("Authorization", "Bearer "+credentials[i%len(credentials)])
		req.Header.Set("X-Workspace-ID", ws)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		want := 200
		if i == 5 {
			want = 429
		}
		if rr.Code != want {
			t.Fatalf("authenticated search %d: got %d, want %d: %s", i+1, rr.Code, want, rr.Body.String())
		}
		if i == 5 && (rr.Header().Get("Retry-After") == "" || rr.Header().Get("Cache-Control") != "no-store") {
			t.Fatalf("missing retry guidance: %v", rr.Header())
		}
	}
}
