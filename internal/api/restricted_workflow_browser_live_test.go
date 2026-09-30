//go:build linux && restrictedruntime_live

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
)

// Browser acceptance requires a independently built production export. Only
// owned migrated fixtures, real isolated workers and synthetic TLS are used.
func TestLiveRestrictedWorkflowBrowser(t *testing.T) {
	repo, image := os.Getenv("CREWSHIP_RESTRICTED_BROWSER_REPO"), os.Getenv("CREWSHIP_RESTRICTED_PRODUCTION_IMAGE")
	if repo == "" || image == "" {
		t.Skip("production export and owned pinned text image required")
	}
	if _, err := os.Stat(filepath.Join(repo, "out", "index.html")); err != nil {
		t.Fatal("production export required", err)
	}
	setTestEncryptionKey(t)
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('browser-crew',?,'Crew','browser-crew')`, workspace)
	seedAgentRow(t, db, "browser-agent", workspace, "browser-crew", "Text", "browser-agent", "AGENT")
	execOrFatal(t, db, `UPDATE agents SET restricted_execution_profile='responses_text',llm_provider='OPENAI',llm_model='gpt-5-mini' WHERE id='browser-agent'`)
	cipher, err := encryption.Encrypt("synthetic-workflow-browser-key")
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('browser-key',?,'Key',?,'API_KEY','OPENAI',?)`, workspace, cipher, owner)
	execOrFatal(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('browser-grant','browser-agent','browser-key','OPENAI_API_KEY')`)
	execOrFatal(t, db, `INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('browser-cap',?,'workspace',?,'month',100,'hard')`, workspace, workspace)
	store := access.Store{DB: db}
	const secret = "synthetic-workflow-browser-jwt-secret"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, actor := range []string{"browser-h1", "browser-h2"} {
		role := "MEMBER"
		if actor == "browser-h1" {
			role = "MANAGER"
		}
		execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, actor, actor+"@browser.test")
		execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,capabilities) VALUES(?,?,?,?,'["routine.run"]')`, actor, workspace, actor, role)
		member, e := store.Membership(t.Context(), actor, workspace)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = store.Replace(t.Context(), owner, actor, workspace, "restricted", member, []access.Right{{Kind: "agent", ID: "browser-agent", Operation: "run"}, {Kind: "agent", ID: "browser-agent", Operation: "discover"}}); e != nil {
			t.Fatal(e)
		}
		session, e := sessions.NewDBStore(db).Create(t.Context(), actor, "test", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		tokens[actor], e = validator.IssueAccessToken(actor, session.ID, actor, actor+"@browser.test")
		if e != nil {
			t.Fatal(e)
		}
	}
	execOrFatal(t, db, `INSERT INTO crew_members(crew_id,user_id,role) VALUES('browser-crew','browser-h1','MANAGER')`)
	declaration := `{"dsl_version":"1.0","name":"private-work","inputs":[{"name":"task","type":"string","required":true},{"name":"fixed","type":"string"}],"steps":[{"id":"answer","type":"agent_run","agent_slug":"browser-agent","prompt":"{{ inputs.task }}"}]}`
	execOrFatal(t, db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,author_crew_id,status) VALUES('browser-routine',?,'private-work','Allowed routine',?,'fixture','browser-crew','active')`, workspace, declaration)
	execOrFatal(t, db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,status) VALUES('browser-denied',?,'denied','DENIED_ROUTINE_CANARY','{"dsl_version":"1.0","name":"denied","steps":[{"id":"work","type":"agent_run","agent_slug":"ungranted-agent","prompt":"HIDDEN_PROMPT"}]}','fixture','active')`, workspace)
	spec := `{"apiVersion":"crewship/v1","kind":"Page","metadata":{"slug":"allowed-page"},"spec":{"panels":[{"id":"panel","schema":"status.v1","owner":"crew/browser-crew","producer":"script/status.sh","sla_seconds":30,"span":8,"actions":[{"id":"work","kind":"call","label":"Allowed work","routine":"private-work","params":{"fixed":"FIXED_PRIVATE_CANARY"},"inputs":[{"name":"task","type":"text","required":true}],"confirm":{"title":"Confirm declared work","body":"Run the allowed Page action?"}}]}]}}`
	for _, page := range []struct{ id, slug, name, spec string }{{"browser-page", "allowed-page", "Allowed Page", spec}, {"browser-denied-page", "denied-page", "DENIED_PAGE_CANARY", strings.Replace(spec, `"routine":"private-work"`, `"routine":"denied"`, 1)}} {
		execOrFatal(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,?,?,?)`, page.id, workspace, page.slug, page.name, owner, page.spec)
		execOrFatal(t, db, `INSERT INTO page_panels(id,page_id,panel_id,schema,owner_crew_id,producer_kind,producer_ref,sla_seconds,span) VALUES(?,?,'panel','status.v1','browser-crew','script','status.sh',30,8)`, page.id+"-panel", page.id)
	}
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Input string `json:"input"`
			Model string `json:"model"`
		}
		if json.NewDecoder(req.Body).Decode(&body) != nil || body.Model != "gpt-5-mini" || req.Header.Get("Authorization") != "Bearer synthetic-workflow-browser-key" {
			t.Error("unapproved frozen browser provider request")
			w.WriteHeader(403)
			return
		}
		actor := "browser-h1"
		if strings.Contains(body.Input, "BROWSER_ROUTINE_browser-h2") {
			actor = "browser-h2"
		}
		foreign := "browser-h2"
		if actor == foreign {
			foreign = "browser-h1"
		}
		if strings.Contains(body.Input, "BROWSER_ROUTINE_"+foreign) || strings.Contains(body.Input, "RESULT_"+foreign) {
			t.Error("foreign workflow context reached provider")
		}
		var pending int
		if e := db.QueryRowContext(req.Context(), `SELECT count(*) FROM restricted_cost_reservations WHERE state='pending'`).Scan(&pending); e != nil || pending < 1 {
			t.Error("browser provider call preceded durable debit")
		}
		calls.Add(1)
		result := "ROUTINE_RESULT_" + actor
		if strings.Contains(body.Input, "BROWSER_PAGE_") {
			result = "PAGE_RESULT_" + actor
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", result)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-5-mini\",\"usage\":{\"input_tokens\":20,\"output_tokens\":5,\"total_tokens\":25,\"input_tokens_details\":{\"cached_tokens\":0}}}}\n\n")
	}))
	defer upstream.Close()
	authority := restricteddispatch.Authority{Store: store}
	manager, err := restrictedruntime.New(filepath.Join(t.TempDir(), "runtime"), restrictedruntime.Docker{Image: image}, authority, authority, restrictedruntime.Limits{MemoryBytes: 128 << 20, NanoCPUs: 500000000, PIDs: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err = manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = restrictedruntime.InstallAcceptanceTLS(manager, upstream); err != nil {
		t.Fatal(err)
	}
	runner := &restricteddispatch.TextRunner{Authority: authority, Manager: manager, MaxOutputTokens: 128}
	service, err := restrictedworkflow.New(db, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err = service.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-browser-host-token"), WithRestrictedTextRunner(runner))
	if err != nil {
		t.Fatal(err)
	}
	router.SetRestrictedWorkflow(service)
	mux := http.NewServeMux()
	mux.Handle("/api/", router)
	mux.Handle("/ws", router)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	server := httptest.NewServer(mux)
	defer server.Close()
	fixture, err := json.Marshal(map[string]any{"server": server.URL, "tokens": tokens, "workspace": workspace})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "workflow-browser-fixture.json")
	if err = os.WriteFile(path, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "node", filepath.Join(repo, "scripts/acceptance-restricted-workflow-browser.mjs"))
	command.Env = append(os.Environ(), "CREWSHIP_WORKFLOW_BROWSER_FIXTURE="+path)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = command.Run(); err != nil {
		for _, table := range []string{"restricted_workflow_jobs", "restricted_cost_reservations"} {
			rows, diagErr := db.QueryContext(context.WithoutCancel(t.Context()), "SELECT state,count(*) FROM "+table+" GROUP BY state")
			if diagErr != nil {
				t.Logf("workflow browser diagnostic table=%s query_failed", table)
				continue
			}
			for rows.Next() {
				var state string
				var count int
				if rows.Scan(&state, &count) == nil {
					t.Logf("workflow browser diagnostic table=%s state=%s count=%d", table, state, count)
				}
			}
			rows.Close()
		}
		t.Logf("workflow browser diagnostic upstream_calls=%d", calls.Load())
		t.Fatal("restricted production workflow browser", err)
	}
	var known int
	if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM restricted_cost_reservations WHERE state='known'`).Scan(&known); err != nil || known != 3 || calls.Load() != 3 {
		t.Fatal("browser calls/accounting mismatch", calls.Load(), known, err)
	}
}
