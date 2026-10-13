package api

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/encryption"
)

// Navigation is a member/workspace hint, not the static account route ceiling.
// Reuse one real session while the owner changes grants.
func TestMemberNavigationFollowsGrantsAndWorkspace(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	store := access.Store{DB: db}
	const secret = "member-navigation-test-secret-32-chars"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{"nav-a", "nav-b"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES (?,?)`, user, user+"@example.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES (?,?,?,'VIEWER')`, user, ws, user)
		member, e := store.Membership(t.Context(), user, ws)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = store.Replace(t.Context(), owner, user, ws, "restricted", member, nil); e != nil {
			t.Fatal(e)
		}
		session, e := sessions.NewDBStore(db).Create(t.Context(), user, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		tokens[user], e = validator.IssueAccessToken(user, session.ID, user, user+"@example.test")
		if e != nil {
			t.Fatal(e)
		}
	}
	router, err := NewRouter(db, secret, newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	surfaces := func(user string, selected ...string) []any {
		target := ws
		if len(selected) > 0 {
			target = selected[0]
		}
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("directory=%d %s", rr.Code, rr.Body.String())
		}
		var rows []map[string]any
		if e := json.Unmarshal(rr.Body.Bytes(), &rows); e != nil {
			t.Fatal(e)
		}
		for _, row := range rows {
			if row["id"] == target {
				if row["restricted_session"] != true {
					t.Fatal("account ceiling lost")
				}
				out, _ := row["restricted_surfaces"].([]any)
				return out
			}
		}
		t.Fatal("workspace missing")
		return nil
	}
	for _, user := range []string{"nav-a", "nav-b"} {
		if got := surfaces(user); !reflect.DeepEqual(got, []any{"account_security"}) {
			t.Fatalf("%s none surfaces=%v; want account_security only", user, got)
		}
	}
	agent := seedAgentRow(t, db, "nav-alex", ws, seedTestCrew(t, db, ws), "Alex", "alex", "AGENT")
	replace := func(rights []access.Right) {
		t.Helper()
		member, e := store.Membership(t.Context(), "nav-a", ws)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = store.Replace(t.Context(), owner, "nav-a", ws, "restricted", member, rights); e != nil {
			t.Fatal(e)
		}
	}
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('nav-other','Other','nav-other')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('nav-other-member','nav-other','nav-a','VIEWER')`)
	for _, operation := range []string{"discover", "chat"} {
		replace([]access.Right{{Kind: "agent", ID: agent, Operation: operation}})
		if got := surfaces("nav-a"); !reflect.DeepEqual(got, []any{"chat", "account_security"}) {
			t.Fatalf("%s surfaces=%v", operation, got)
		}
		if got := surfaces("nav-a", "nav-other"); !reflect.DeepEqual(got, []any{"account_security"}) {
			t.Fatalf("workspace switch borrowed grant: %v", got)
		}
		if got := surfaces("nav-b"); !reflect.DeepEqual(got, []any{"account_security"}) {
			t.Fatalf("other member surfaces=%v", got)
		}
	}
	replace(nil)
	if got := surfaces("nav-a"); !reflect.DeepEqual(got, []any{"account_security"}) {
		t.Fatalf("same JWT revoke surfaces=%v", got)
	}

	if runtime.GOOS != "linux" {
		t.Log("Chat/none navigation covered; routine/Page authority checks require the Linux provider authority adapter")
		return
	}

	// Navigation authority survives an uninstalled runtime; the execution
	// endpoint independently reports runtime_missing instead of pretending revoke.
	execOrFatal(t, db, `UPDATE workspace_members SET capabilities='["routine.run"]' WHERE user_id='nav-a'`)
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='responses_text',llm_provider='OPENAI',llm_model='fixture-model' WHERE id=?`, agent)
	cipher, e := encryption.Encrypt("synthetic-nav-provider-key")
	if e != nil {
		t.Fatal(e)
	}
	execOrFatal(t, db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('nav-key',?,'Synthetic',?,'API_KEY','OPENAI',?)`, ws, cipher, owner)
	execOrFatal(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('nav-key-bind',?,'nav-key','OPENAI_API_KEY')`, agent)
	execOrFatal(t, db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,author_crew_id,status) VALUES('nav-recipe',?,'nav-work','Navigation recipe',?,'fixture',?,'active')`, ws, `{"dsl_version":"1.0","name":"nav-work","inputs":[{"name":"task","type":"string","required":true}],"steps":[{"id":"answer","type":"agent_run","agent_slug":"alex","prompt":"{{ inputs.task }}"}]}`, "crew_test_"+ws)
	replace([]access.Right{{Kind: "agent", ID: agent, Operation: "run"}})
	if got := surfaces("nav-a"); !reflect.DeepEqual(got, []any{"routines", "account_security"}) {
		t.Fatalf("authorized navigation disappeared with absent runtime: %v", got)
	}
	req := httptest.NewRequest("GET", "/api/v1/workspaces/"+ws+"/restricted-routines?workspace_id="+ws, nil)
	req.Header.Set("Authorization", "Bearer "+tokens["nav-a"])
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != 503 {
		t.Fatalf("missing runtime catalog=%d %s", rr.Code, rr.Body.String())
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='MANAGER' WHERE id='nav-a'`)
	execOrFatal(t, db, `INSERT INTO crew_members(crew_id,user_id,role) VALUES(?,'nav-a','MANAGER')`, "crew_test_"+ws)
	spec := `{"apiVersion":"crewship/v1","kind":"Page","metadata":{"slug":"nav-page"},"spec":{"panels":[{"id":"panel","schema":"status.v1","owner":"crew/crew-` + ws + `","producer":"script/status.sh","sla_seconds":30,"span":8,"actions":[{"id":"work","kind":"call","label":"Run navigation recipe","routine":"nav-work","inputs":[{"name":"task","type":"text","required":true}]}]}]}}`
	execOrFatal(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('nav-page',?,'nav-page','Navigation Page',?,?)`, ws, owner, spec)
	execOrFatal(t, db, `INSERT INTO page_panels(id,page_id,panel_id,schema,owner_crew_id,producer_kind,producer_ref,sla_seconds,span) VALUES('nav-panel','nav-page','panel','status.v1',?,'script','status.sh',30,8)`, "crew_test_"+ws)
	if got := surfaces("nav-a"); !reflect.DeepEqual(got, []any{"routines", "pages", "account_security"}) {
		t.Fatalf("authorized Page navigation disappeared with absent runtime: %v", got)
	}
	req = httptest.NewRequest("GET", "/api/v1/workspaces/"+ws+"/restricted-pages?workspace_id="+ws, nil)
	req.Header.Set("Authorization", "Bearer "+tokens["nav-a"])
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != 503 {
		t.Fatalf("missing Page runtime=%d %s", rr.Code, rr.Body.String())
	}

	// Main's GET read contract is principal-based; a navigation hint is not
	// permission to execute with a narrowed CLI token.
	const limited = "crewship_cli_11223344556677889900aabbccddeeff"
	execOrFatal(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,scopes,created_at) VALUES('nav-scoped-token','nav-a','Synthetic',?,'["agents:write"]',datetime('now'))`, sha256Hex(limited))
	scoped := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	scoped.Header.Set("Authorization", "Bearer "+limited)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, scoped)
	if rec.Code != 200 {
		t.Fatalf("narrowed token lost existing GET projection: %d %s", rec.Code, rec.Body.String())
	}
	var projected []map[string]any
	if err = json.Unmarshal(rec.Body.Bytes(), &projected); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range projected {
		if row["id"] == ws {
			found = reflect.DeepEqual(row["restricted_surfaces"], []any{"routines", "pages", "account_security"})
		}
	}
	if !found {
		t.Fatalf("narrowed token GET projection=%v", projected)
	}
	for _, path := range []string{"/api/v1/workspaces/" + ws + "/pipelines/nav-work/run", "/api/v1/pages/nav-page/panels/panel/actions/work"} {
		scoped = httptest.NewRequest("POST", path+"?workspace_id="+ws, strings.NewReader(`{"inputs":{"task":"scope-denied"}}`))
		scoped.Header.Set("Authorization", "Bearer "+limited)
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, scoped)
		if rec.Code != 403 {
			t.Fatalf("navigation became scoped execution authority: %s=%d %s", path, rec.Code, rec.Body.String())
		}
	}
	replace(nil)
	if got := surfaces("nav-a"); !reflect.DeepEqual(got, []any{"account_security"}) {
		t.Fatalf("revoked runtime-offline navigation=%v", got)
	}

}

func TestMemberNavigationDirectoryFailureIsNotAnEmptyGrantProjection(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('nav-error','nav-error@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('nav-error',?,'nav-error','VIEWER')`, workspace)
	store := access.Store{DB: db}
	member, err := store.Membership(t.Context(), "nav-error", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "nav-error", workspace, "restricted", member, nil); err != nil {
		t.Fatal(err)
	}
	// Simulate an unavailable policy store in this disposable migrated database.
	execOrFatal(t, db, `DROP TABLE access_grants`)
	req := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "nav-error"}))
	rr := httptest.NewRecorder()
	NewWorkspaceHandler(db, newTestLogger()).List(rr, req)
	if rr.Code != 500 {
		t.Fatalf("policy failure became success/empty projection: %d %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "" && got != "no-store" {
		t.Fatalf("failure cached: %q", got)
	}
}
