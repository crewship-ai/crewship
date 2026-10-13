package api

import (
	"encoding/json"
	"fmt"
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
		expected := []any{"account_security"}
		if operation == "discover" {
			expected = []any{"chat", "account_security"}
		}
		if got := surfaces("nav-a"); !reflect.DeepEqual(got, expected) {
			t.Fatalf("%s surfaces=%v", operation, got)
		}
		directoryReq := httptest.NewRequest("GET", "/api/v1/agents?workspace_id="+ws, nil)
		directoryReq.Header.Set("Authorization", "Bearer "+tokens["nav-a"])
		directoryReply := httptest.NewRecorder()
		router.ServeHTTP(directoryReply, directoryReq)
		var agents []map[string]any
		if directoryReply.Code != 200 || json.Unmarshal(directoryReply.Body.Bytes(), &agents) != nil {
			t.Fatalf("agent directory=%d %s", directoryReply.Code, directoryReply.Body.String())
		}
		wantCount := 0
		if operation == "discover" {
			wantCount = 1
		}
		if len(agents) != wantCount {
			t.Fatalf("%s must preserve discover-only directory: %v", operation, agents)
		}
		if operation == "chat" {
			chatReq := httptest.NewRequest("GET", "/api/v1/agents/"+agent+"/chats?workspace_id="+ws, nil)
			chatReq.Header.Set("Authorization", "Bearer "+tokens["nav-a"])
			chatReply := httptest.NewRecorder()
			router.ServeHTTP(chatReply, chatReq)
			if chatReply.Code != 200 {
				t.Fatalf("chat-only known agent lost direct chat: %d %s", chatReply.Code, chatReply.Body.String())
			}
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

	checkNavigationProfiles(t, db, router, agent, func() []any { return surfaces("nav-a") })

	// Scale regression: navigation needs existence, not all 50 frozen recipes
	// and 10 action catalogs. Count real SQL statements, not elapsed wall time.
	for i := 1; i < 50; i++ {
		execOrFatal(t, db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,author_crew_id,status) SELECT ?,workspace_id,?,name,definition_json,definition_hash,author_crew_id,status FROM pipelines WHERE id='nav-recipe'`, fmt.Sprintf("nav-recipe-%d", i), fmt.Sprintf("nav-work-%02d", i))
	}
	for i := 1; i < 10; i++ {
		execOrFatal(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) SELECT ?,workspace_id,?,name,owner_user_id,spec_json FROM pages WHERE id='nav-page'`, fmt.Sprintf("nav-page-%d", i), fmt.Sprintf("nav-page-%02d", i))
		execOrFatal(t, db, `INSERT INTO page_panels(id,page_id,panel_id,schema,owner_crew_id,producer_kind,producer_ref,sla_seconds,span) SELECT ?,? ,panel_id,schema,owner_crew_id,producer_kind,producer_ref,sla_seconds,span FROM page_panels WHERE id='nav-panel'`, fmt.Sprintf("nav-panel-%d", i), fmt.Sprintf("nav-page-%d", i))
	}
	checkNavigationScale(t, db, "nav-a", true)

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
	checkNavigationScale(t, db, "nav-a", false)

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

// PR regression: a malformed workspace-local Page must not remove another
// membership's usable workspace directory. Main does not inspect Page catalogs.
func TestMemberNavigationIsolatesWorkspaceCatalogFailure(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	good := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('nav-isolated','nav-isolated@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('nav-bad','Bad','nav-bad')`)
	store := access.Store{DB: db}
	for _, ws := range []string{good, "nav-bad"} {
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,'nav-isolated','VIEWER')`, "isolated-"+ws, ws)
		m, err := store.Membership(t.Context(), "nav-isolated", ws)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), owner, "nav-isolated", ws, "restricted", m, nil); err != nil {
			// The owner also needs membership in the additional synthetic workspace.
			execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('bad-owner','nav-bad',?,'OWNER')`, owner)
			if _, err = store.Replace(t.Context(), owner, "nav-isolated", ws, "restricted", m, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	badCrew := seedTestCrew(t, db, "nav-bad")
	badAgent := seedAgentRow(t, db, "nav-bad-agent", "nav-bad", badCrew, "Bad Agent", "bad-agent", "AGENT")
	execOrFatal(t, db, `UPDATE workspace_members SET role='MANAGER' WHERE workspace_id='nav-bad' AND user_id='nav-isolated'`)
	badMember, err := store.Membership(t.Context(), "nav-isolated", "nav-bad")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "nav-isolated", "nav-bad", "restricted", badMember, []access.Right{{Kind: "agent", ID: badAgent, Operation: "run"}}); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,pages_version,spec_json) VALUES('nav-bad-page','nav-bad','broken','Broken',?,'not-an-integer','{}')`, owner)
	req := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "nav-isolated"}))
	rr := httptest.NewRecorder()
	NewWorkspaceHandler(db, newTestLogger()).List(rr, req)
	if rr.Code != 200 {
		t.Fatalf("one bad workspace hid all memberships: %d %s", rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["id"] != good {
		t.Fatalf("must omit only failing workspace: %v", rows)
	}
	if !reflect.DeepEqual(rows[0]["restricted_surfaces"], []any{"account_security"}) {
		t.Fatalf("healthy projection changed: %v", rows)
	}
}

func TestMemberNavigationGlobalCatalogFailureWithHealthyWorkspace(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO users(id,email) VALUES('nav-global','nav-global@example.test')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,capabilities) VALUES('global-member',?,'nav-global','MEMBER','["routine.run"]')`, ws)
	store := access.Store{DB: db}
	m, err := store.Membership(t.Context(), "nav-global", ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "nav-global", ws, "restricted", m, nil); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('global-healthy','Healthy','aaa-healthy')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('global-healthy','global-healthy','nav-global','VIEWER')`)
	globalAgent := seedAgentRow(t, db, "nav-global-agent", ws, seedTestCrew(t, db, ws), "Global Agent", "global-agent", "AGENT")
	m, err = store.Membership(t.Context(), "nav-global", ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "nav-global", ws, "restricted", m, []access.Right{{Kind: "agent", ID: globalAgent, Operation: "run"}}); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `DROP TABLE pipelines`)
	req := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "nav-global"}))
	rr := httptest.NewRecorder()
	NewWorkspaceHandler(db, newTestLogger()).List(rr, req)
	if rr.Code != 500 {
		t.Fatalf("global SQL store failure was mistaken for workspace-local corruption: %d %s", rr.Code, rr.Body.String())
	}
}
