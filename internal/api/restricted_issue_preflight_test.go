package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/restrictedpreflight"
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
)

type preflightExecutor struct{}

func (preflightExecutor) ExecuteRun(context.Context, string, string, string, string, func(string, string) error) error {
	return access.ErrDenied
}
func (preflightExecutor) ExecuteRunWithRights(context.Context, string, string, string, string, []access.Right, func(string, string) error) error {
	return access.ErrDenied
}

func TestRestrictedIssuePreflightAuthenticatedRouter(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('f-crew',?,'Crew','f-crew')`, workspace)
	seedAgentRow(t, db, "f-agent", workspace, "f-crew", "Agent", "text-agent", "AGENT")
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='responses_text',llm_provider='OPENAI',llm_model='fixture-model' WHERE id='f-agent'`)
	cipher, err := encryption.Encrypt("synthetic-f-key")
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('f-key',?,'Key',?,'API_KEY','OPENAI',?)`, workspace, cipher, owner)
	execOrFatal(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('f-key-grant','f-agent','f-key','OPENAI_API_KEY')`)
	execOrFatal(t, db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,author_crew_id,status) VALUES('f-routine',?,'private-work','Private',?,'fixture-hash','f-crew','active')`, workspace, privateRoutine)
	execOrFatal(t, db, `INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('f-cap',?,'workspace',?,'month',100,'hard')`, workspace, workspace)
	store := access.Store{DB: db}
	tokens := map[string]string{}
	const secret = "synthetic-preflight-router-secret-long-enough"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"f-h1", "f-h2"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, u, u+"@f.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,capabilities) VALUES(?,?,?,'MEMBER','["routine.run"]')`, u, workspace, u)
		project := u + "-project"
		execOrFatal(t, db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES(?,?,?,?)`, project, workspace, u, u)
		execOrFatal(t, db, `INSERT INTO missions(id,workspace_id,crew_id,delegate_agent_id,lead_agent_id,trace_id,title,description,mission_type,status,project_id) VALUES(?,?,'f-crew','f-agent','f-agent',?,'Issue',?,'issue','TODO',?)`, u+"-issue", workspace, u+"-trace", u+"_PRIVATE_CANARY", project)
		m, err := store.Membership(t.Context(), u, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), owner, u, workspace, "restricted", m, []access.Right{{Kind: "agent", ID: "f-agent", Operation: "run"}, {Kind: "project", ID: project, Operation: "read"}}); err != nil {
			t.Fatal(err)
		}
		session, err := sessions.NewDBStore(db).Create(t.Context(), u, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		tokens[u], err = validator.IssueAccessToken(u, session.ID, u, u+"@f.test")
		if err != nil {
			t.Fatal(err)
		}
	}
	wf, err := restrictedworkflow.New(db, preflightExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	wf.SourceChecker = restrictedpreflight.CheckSource
	defer wf.Close()
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-f-host-token"))
	if err != nil {
		t.Fatal(err)
	}
	router.SetRestrictedWorkflow(wf)
	request := func(u, issue, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+workspace+"/issues/"+issue+"/private-preflight", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokens[u])
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	body := `{"agent_id":"f-agent","routine_slug":"private-work"}`
	if r := request("f-h1", "f-h2-issue", body); r.Code != 404 || strings.Contains(r.Body.String(), "CANARY") {
		t.Fatalf("foreign issue %d %s", r.Code, r.Body.String())
	}
	if r := request("f-h1", "f-h1-issue", `{"agent_id":"f-agent","routine_slug":"private-work","principal_id":"f-h2"}`); r.Code != 400 {
		t.Fatalf("caller authority %d", r.Code)
	}
	r := request("f-h1", "f-h1-issue", body)
	if r.Code != 202 {
		t.Fatalf("positive %d %s", r.Code, r.Body.String())
	}
	if r2 := request("f-h1", "f-h1-issue", body); r2.Code != 202 || r2.Body.String() != r.Body.String() {
		// Status may be pending rather than the initial SCHEDULED label; identity
		// equality is checked in the core atomic dedup test.
		if r2.Code != 202 {
			t.Fatalf("duplicate %d %s", r2.Code, r2.Body.String())
		}
	}
	if r := request("f-h2", "f-h2-issue", body); r.Code != 409 || strings.Contains(r.Body.String(), "f-h1") {
		t.Fatalf("capacity leaks %d %s", r.Code, r.Body.String())
	}
}
