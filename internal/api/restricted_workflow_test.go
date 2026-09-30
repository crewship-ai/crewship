package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
)

const privateRoutine = `{"dsl_version":"1.0","name":"private-work","inputs":[{"name":"task","type":"string","required":true}],"steps":[{"id":"answer","type":"agent_run","agent_slug":"text-agent","prompt":"{{ inputs.task }}"}]}`

func TestRestrictedOrdinaryRoutineAndDeclaredPageUsePrivateQueue(t *testing.T) {
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('wf-crew',?,'Crew','wf-crew')`, workspace)
	seedAgentRow(t, db, "wf-agent", workspace, "wf-crew", "Agent", "text-agent", "AGENT")
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='responses_text',llm_provider='OPENAI',llm_model='fixture-model' WHERE id='wf-agent'`)
	cipher, err := encryption.Encrypt("synthetic-workflow-key")
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('wf-key',?,'Key',?,'API_KEY','OPENAI',?)`, workspace, cipher, owner)
	execOrFatal(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('wf-key-grant','wf-agent','wf-key','OPENAI_API_KEY')`)
	execOrFatal(t, db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,author_crew_id,status) VALUES('wf-routine',?,'private-work','Private',?,'fixture-hash','wf-crew','active')`, workspace, privateRoutine)
	store := access.Store{DB: db}
	tokens := map[string]string{}
	const secret = "synthetic-workflow-router-secret-long-enough"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"wf-h1", "wf-h2"} {
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, user, user+"@wf.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,capabilities) VALUES(?,?,?,'MEMBER','["routine.run"]')`, user, workspace, user)
		m, err := store.Membership(t.Context(), user, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), owner, user, workspace, "restricted", m, []access.Right{{Kind: "agent", ID: "wf-agent", Operation: "run"}}); err != nil {
			t.Fatal(err)
		}
		session, err := sessions.NewDBStore(db).Create(t.Context(), user, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		tokens[user], err = validator.IssueAccessToken(user, session.ID, user, user+"@wf.test")
		if err != nil {
			t.Fatal(err)
		}
	}
	starts := 0
	runner := &restricteddispatch.TextRunner{Authority: restricteddispatch.Authority{Store: store}, MaxOutputTokens: 128}
	runner.StartSession = func(ctx context.Context, h string) (restricteddispatch.TextSession, error) {
		a, err := store.Resolve(ctx, h)
		if err != nil {
			return nil, err
		}
		starts++
		done := make(chan struct{})
		close(done)
		return &fixtureTextSession{"{\"type\":\"text\",\"text\":\"answer-" + a.Principal + "\"}\n{\"type\":\"done\"}\n", done}, nil
	}
	service, err := restrictedworkflow.New(db, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-wf-host-token"), WithRestrictedTextRunner(runner))
	if err != nil {
		t.Fatal(err)
	}
	router.SetRestrictedWorkflow(service)
	request := func(user, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path+"?workspace_id="+workspace, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	execOrFatal(t, db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,status) VALUES('foreign-routine',?,'foreign-work','FOREIGN_ROUTINE_CANARY','{"dsl_version":"1.0","name":"foreign","steps":[{"id":"work","type":"agent_run","agent_slug":"ungranted-agent","prompt":"HIDDEN_PROMPT_CANARY"}]}','foreign','active')`, workspace)
	catalog := request("wf-h1", http.MethodGet, "/api/v1/workspaces/"+workspace+"/restricted-routines", "")
	if catalog.Code != 200 || catalog.Header().Get("X-Total-Count") != "1" || strings.Contains(catalog.Body.String(), "FOREIGN_ROUTINE_CANARY") || strings.Contains(catalog.Body.String(), "agent_slug") || strings.Contains(catalog.Body.String(), "prompt") {
		t.Fatalf("catalog leaked declaration/denied counts: %d %s", catalog.Code, catalog.Body.String())
	}
	workspaces := request("wf-h1", http.MethodGet, "/api/v1/workspaces", "")
	if workspaces.Code != 200 || !strings.Contains(workspaces.Body.String(), `"currentUserAccessMode":"restricted"`) {
		t.Fatal("automatic restricted directory mode missing", workspaces.Body.String())
	}
	manualPath := "/api/v1/workspaces/" + workspace + "/pipelines/private-work/run"
	jobs := map[string]string{}
	for _, user := range []string{"wf-h1", "wf-h2"} {
		rec := request(user, http.MethodPost, manualPath, `{"inputs":{"task":"`+user+`_PRIVATE_CANARY"}}`)
		if rec.Code != 202 || starts != 0 {
			t.Fatalf("ordinary routine start %d starts=%d body=%s", rec.Code, starts, rec.Body.String())
		}
		var accepted struct {
			ID     string `json:"run_id"`
			ChatID string `json:"chat_id"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &accepted) != nil || accepted.ID == "" {
			t.Fatal("missing durable private identity")
		}
		tamper := request(user, http.MethodPost, "/api/v1/chats/"+accepted.ChatID+"/restricted-cli-run", `{"content":"TAMPER_WORKFLOW_INPUT"}`)
		if tamper.Code != 403 || starts != 0 {
			t.Fatalf("routine context externally executable %d starts%d", tamper.Code, starts)
		}
		jobs[user] = accepted.ID
	}
	for range jobs {
		if worked, err := service.DispatchNext(t.Context()); !worked || err != nil {
			t.Fatalf("dispatch %v %v", worked, err)
		}
	}
	for user, job := range jobs {
		path := "/api/v1/workspaces/" + workspace + "/restricted-routine-runs/" + job
		rec := request(user, http.MethodGet, path, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "answer-"+user) {
			t.Fatalf("own result %d %s", rec.Code, rec.Body.String())
		}
		other := "wf-h1"
		if user == "wf-h1" {
			other = "wf-h2"
		}
		rec = request(other, http.MethodGet, path, "")
		if rec.Code != 404 {
			t.Fatalf("foreign workflow disclosed %d", rec.Code)
		}
		list := request(user, http.MethodGet, "/api/v1/workspaces/"+workspace+"/restricted-routine-runs", "")
		if list.Code != 200 || list.Header().Get("X-Total-Count") != strconv.Itoa(1) || strings.Contains(list.Body.String(), jobs[other]) {
			t.Fatalf("foreign list/count %d %s", list.Code, list.Body.String())
		}
	}
	// Neither execution metadata nor a broad actor can be selected by task JSON.
	for _, body := range []string{`{"inputs":{"task":"x"},"principal":"wf-h2"}`, `{"inputs":{"task":"x"},"metadata":{"handle":"fake"}}`, `{"inputs":{"task":"x"},"tier_override":"smart"}`} {
		if rec := request("wf-h1", http.MethodPost, manualPath, body); rec.Code != 400 {
			t.Fatalf("caller authority fields accepted %d", rec.Code)
		}
	}
	execOrFatal(t, db, `UPDATE workspace_members SET capabilities='[]' WHERE user_id='wf-h2'`)
	if rec := request("wf-h2", http.MethodPost, manualPath, `{"inputs":{"task":"run grant alone"}}`); rec.Code != 404 {
		t.Fatalf("run grant bypassed routine capability %d", rec.Code)
	}
	// A declared Page action retains its own manager/visible-panel floor.
	execOrFatal(t, db, `UPDATE workspace_members SET role='MANAGER' WHERE user_id='wf-h1'`)
	execOrFatal(t, db, `INSERT INTO crew_members(crew_id,user_id,role) VALUES('wf-crew','wf-h1','MANAGER')`)
	spec := `{"apiVersion":"crewship/v1","kind":"Page","metadata":{"slug":"private-page"},"spec":{"name":"Private Page","panels":[{"id":"panel","schema":"status.v1","owner":"crew/wf-crew","producer":"script/status.sh","sla_seconds":30,"span":8,"actions":[{"id":"do-work","kind":"call","label":"Private work","routine":"private-work","params":{"task":"PAGE_PRIVATE_CANARY"}}]}]}}`
	execOrFatal(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('wf-page',?,'private-page','Private',?,?)`, workspace, owner, spec)
	execOrFatal(t, db, `INSERT INTO page_panels(id,page_id,panel_id,schema,owner_crew_id,producer_kind,producer_ref,sla_seconds,span) VALUES('wf-panel','wf-page','panel','status.v1','wf-crew','script','status.sh',30,8)`)
	pageCatalogPath := "/api/v1/workspaces/" + workspace + "/restricted-pages"
	beforeCatalog := map[string]int{}
	for _, table := range []string{"chats", "access_attempts", "restricted_workflow_jobs"} {
		var count int
		if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		beforeCatalog[table] = count
	}
	pageCatalog := request("wf-h1", http.MethodGet, pageCatalogPath, "")
	if pageCatalog.Code != 200 || pageCatalog.Header().Get("X-Total-Count") != "1" || !strings.Contains(pageCatalog.Body.String(), "do-work") {
		t.Fatalf("allowed Page directory %d %s", pageCatalog.Code, pageCatalog.Body.String())
	}
	for _, forbidden := range []string{"PAGE_PRIVATE_CANARY", "producer", "params", "spec_json", "status.sh"} {
		if strings.Contains(pageCatalog.Body.String(), forbidden) {
			t.Fatalf("Page directory exposed %s", forbidden)
		}
	}
	otherCatalog := request("wf-h2", http.MethodGet, pageCatalogPath, "")
	if otherCatalog.Code != 200 || otherCatalog.Header().Get("X-Total-Count") != "0" || strings.Contains(otherCatalog.Body.String(), "private-page") {
		t.Fatalf("Page floor leaked declaration/count %d %s", otherCatalog.Code, otherCatalog.Body.String())
	}
	for table, count := range beforeCatalog {
		var after int
		if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&after); err != nil || after != count {
			t.Fatalf("Page catalog issued %s authority %d != %d %v", table, after, count, err)
		}
	}
	pagePath := "/api/v1/pages/private-page/panels/panel/actions/do-work"
	rec := request("wf-h2", http.MethodPost, pagePath, `{"inputs":{}}`)
	if rec.Code != 403 {
		t.Fatalf("Page operation floor bypassed %d %s", rec.Code, rec.Body.String())
	}
	rec = request("wf-h1", http.MethodPost, pagePath, `{"inputs":{}}`)
	if rec.Code != 202 {
		t.Fatalf("Page private queue %d %s", rec.Code, rec.Body.String())
	}

	var pageReceipt struct {
		ID string `json:"run_id"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &pageReceipt) != nil || pageReceipt.ID == "" {
		t.Fatal("missing Page receipt")
	}
	statusPath := "/api/v1/pages/private-page/application/actions/" + pageReceipt.ID
	if status := request("wf-h1", http.MethodGet, statusPath, ""); status.Code != 200 || !strings.Contains(status.Body.String(), `"pending_status":"pending"`) {
		t.Fatalf("own Page pending status %d %s", status.Code, status.Body.String())
	}
	for _, check := range []struct{ user, path string }{{"wf-h2", statusPath}, {"wf-h1", "/api/v1/pages/guessed-page/application/actions/" + pageReceipt.ID}, {"wf-h1", "/api/v1/pages/private-page/application/actions/" + jobs["wf-h1"]}} {
		if status := request(check.user, http.MethodGet, check.path, ""); status.Code != 404 {
			t.Fatalf("foreign/mismatched Page receipt disclosed %d %s", status.Code, status.Body.String())
		}
	}
	if worked, err := service.DispatchNext(t.Context()); !worked || err != nil {
		t.Fatalf("Page dispatch %v %v", worked, err)
	}
	if status := request("wf-h1", http.MethodGet, statusPath, ""); status.Code != 200 || !strings.Contains(status.Body.String(), "answer-wf-h1") {
		t.Fatalf("own Page completed status %d %s", status.Code, status.Body.String())
	}
	membership, err := store.Membership(t.Context(), "wf-h1", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "wf-h1", workspace, "restricted", membership, nil); err != nil {
		t.Fatal(err)
	}
	if catalog := request("wf-h1", http.MethodGet, pageCatalogPath, ""); catalog.Code != 200 || catalog.Header().Get("X-Total-Count") != "0" {
		t.Fatal("Page directory ignored current target revocation", catalog.Code)
	}
	membership, err = store.Membership(t.Context(), "wf-h1", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), owner, "wf-h1", workspace, "restricted", membership, []access.Right{{Kind: "agent", ID: "wf-agent", Operation: "run"}}); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	execOrFatal(t, db, `INSERT INTO page_project_drafts(page_id,source_digest,revision,spec_json,updated_at) VALUES('wf-page',?,1,?,'2026-09-30T00:00:00Z')`, digest, spec)
	if catalog := request("wf-h1", http.MethodGet, pageCatalogPath, ""); catalog.Code != 200 || catalog.Header().Get("X-Total-Count") != "0" {
		t.Fatal("unpublished Page draft exposed executable actions", catalog.Code)
	}
	execOrFatal(t, db, `INSERT INTO page_project_builds(id,page_id,source_revision,source_digest,state,created_at) VALUES('wf-page-build','wf-page',1,?,'ready','2026-09-30T00:00:00Z')`, digest)
	execOrFatal(t, db, `INSERT INTO page_project_publications(page_id,version,build_id,source_revision,source_digest,git_commit,artifact_digest,spec_json,checks_json,created_at) VALUES('wf-page',1,'wf-page-build',1,?,'synthetic-commit',?,?,'{}','2026-09-30T00:00:00Z')`, digest, digest, spec)
	execOrFatal(t, db, `INSERT INTO page_project_live(page_id,version) VALUES('wf-page',1)`)
	if catalog := request("wf-h1", http.MethodGet, pageCatalogPath, ""); catalog.Code != 200 || catalog.Header().Get("X-Total-Count") != "1" || !strings.Contains(catalog.Body.String(), `"publication":1`) {
		t.Fatal("current publication missing from filtered Page directory", catalog.Code)
	}
	execOrFatal(t, db, `UPDATE pages SET spec_json=? WHERE id='wf-page'`, strings.Replace(spec, "Private work", "Changed work", 1))
	if catalog := request("wf-h1", http.MethodGet, pageCatalogPath, ""); catalog.Code != 200 || catalog.Header().Get("X-Total-Count") != "0" || strings.Contains(catalog.Body.String(), "Changed work") {
		t.Fatal("changed unpublished declaration escaped Page publication fence", catalog.Code)
	}

	var shared int
	for _, table := range []string{"pipeline_runs", "pending_runs"} {
		if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&shared); err != nil || shared != 0 {
			t.Fatalf("private work leaked shared %s %d %v", table, shared, err)
		}
	}
}
