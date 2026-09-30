package restrictedpreflight

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
	"github.com/crewship-ai/crewship/internal/testutil"
)

type executor struct {
	runner *restricteddispatch.TextRunner
	calls  int
	before func()
	rights []access.Right
}

func (e *executor) ExecuteRun(context.Context, string, string, string, string, func(string, string) error) error {
	return errors.New("unclassified execution forbidden")
}
func (e *executor) ExecuteRunWithRights(ctx context.Context, u, w, c, p string, r []access.Right, emit func(string, string) error) error {
	e.calls++
	e.rights = append([]access.Right(nil), r...)
	if e.before != nil {
		e.before()
	}
	if err := emit("text", "private answer"); err != nil {
		return err
	}
	return emit("done", "")
}

type preflightTextSession struct{ done chan struct{} }

func (s *preflightTextSession) Output(context.Context) (string, error) {
	return "{\"type\":\"text\",\"text\":\"private answer\"}\n{\"type\":\"done\"}\n", nil
}
func (s *preflightTextSession) Done() <-chan struct{} { return s.done }
func (s *preflightTextSession) Stop(string)           {}
func (e *executor) ExecuteWorkflowRun(ctx context.Context, request restricteddispatch.DelegatedRunRequest, emit func(string, string) error) (restricteddispatch.RunProof, error) {
	e.calls++
	e.rights = nil
	for _, right := range request.Rights {
		if right.Kind == "project" {
			e.rights = append(e.rights, right)
		}
	}
	if e.before != nil {
		e.before()
	}
	return e.runner.ExecuteWorkflowRun(ctx, request, emit)
}
func fixture(t *testing.T) (*Service, *executor) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("31", 32))
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v1")
	db := testutil.MigratedSQLDB(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,email) VALUES('owner','owner@f.test'),('h1','h1@f.test'),('h2','h2@f.test')`)
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('w','Workspace','preflight')`)
	exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role,capabilities) VALUES('mo','w','owner','OWNER','[]'),('m1','w','h1','MEMBER','["routine.run"]'),('m2','w','h2','MEMBER','["routine.run"]')`)
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew','w','Crew','crew')`)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,llm_provider,llm_model,restricted_execution_profile) VALUES('agent','w','crew','Worker','worker','AGENT','OPENAI','fixture-model','responses_text')`)
	cipher, err := encryption.Encrypt("synthetic-key")
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('key','w','Key',?,'API_KEY','OPENAI','owner')`, cipher)
	exec(`INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('grant','agent','key','OPENAI_API_KEY')`)
	definition := `{"dsl_version":"1.0","name":"issue-work","inputs":[{"name":"task","type":"string","required":true}],"steps":[{"id":"first","type":"agent_run","agent_slug":"worker","prompt":"{{ inputs.task }}"}]}`
	h := sha256.Sum256([]byte(definition))
	exec(`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,author_crew_id,status) VALUES('routine','w','issue-work','Issue work',?,?,'crew','active')`, definition, hex.EncodeToString(h[:]))
	exec(`INSERT INTO projects(id,workspace_id,name,slug) VALUES('p1','w','One','one'),('p2','w','Two','two')`)
	for _, v := range []struct{ id, project, brief string }{{"i1", "p1", "H1 private canary"}, {"i2", "p2", "H2 private canary"}, {"projectless", "", "projectless canary"}} {
		var project any = v.project
		if v.project == "" {
			project = nil
		}
		exec(`INSERT INTO missions(id,workspace_id,crew_id,delegate_agent_id,lead_agent_id,trace_id,title,description,mission_type,status,project_id) VALUES(?,'w','crew','agent','agent',?,'Issue',?,'issue','TODO',?)`, v.id, v.id+"-trace", v.brief, project)
	}
	exec(`INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('cap','w','workspace','w','month',100,'hard')`)
	store := access.Store{DB: db}
	for _, user := range []string{"h1", "h2"} {
		m, err := store.Membership(t.Context(), user, "w")
		if err != nil {
			t.Fatal(err)
		}
		project := "p1"
		if user == "h2" {
			project = "p2"
		}
		_, err = store.Replace(t.Context(), "owner", user, "w", "restricted", m, []access.Right{{Kind: "agent", ID: "agent", Operation: "run"}, {Kind: "project", ID: project, Operation: "read"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	e := &executor{runner: &restricteddispatch.TextRunner{Authority: restricteddispatch.Authority{Store: store}, MaxOutputTokens: 128}}
	e.runner.StartSession = func(context.Context, string) (restricteddispatch.TextSession, error) {
		done := make(chan struct{})
		close(done)
		return &preflightTextSession{done}, nil
	}
	wf, err := restrictedworkflow.New(db, e)
	if err != nil {
		t.Fatal(err)
	}
	wf.SourceChecker = CheckSource
	t.Cleanup(func() { _ = wf.Close() })
	return &Service{DB: db, Workflow: wf}, e
}
func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPrivateIssueClaimDeduplicatesAndDoesNotLeak(t *testing.T) {
	s, e := fixture(t)
	for _, issue := range []string{"i2", "projectless", "missing"} {
		if _, err := s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", issue, "issue-work"); !errors.Is(err, ErrNoWork) {
			t.Fatalf("%s: %v", issue, err)
		}
	}
	if count(t, s.DB, "chats") != 0 || e.calls != 0 {
		t.Fatal("no-work created a wake")
	}
	r, err := s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work")
	if err != nil || r2.ID != r.ID {
		t.Fatalf("duplicate %#v %v", r2, err)
	}
	if count(t, s.DB, "restricted_workflow_jobs") != 1 || count(t, s.DB, "restricted_preflight_reservations") != 1 || e.calls != 0 {
		t.Fatal("duplicate enqueued/ran")
	}
	if count(t, s.DB, "chats") != 1 {
		t.Fatal("duplicate created ghost chats")
	}
	if _, err = s.Workflow.ReceiptForActor(t.Context(), "h2", "w", r.ID); err == nil {
		t.Fatal("foreign receipt exposed")
	}
	if _, err = s.ClaimAssignedIssue(t.Context(), "h2", "w", "agent", "i2", "issue-work"); !errors.Is(err, ErrBusy) {
		t.Fatalf("same agent capacity: %v", err)
	}
	if _, err = s.Workflow.DispatchNext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.calls != 1 || len(e.rights) != 1 || e.rights[0].ID != "p1" {
		t.Fatal("source right not forwarded")
	}
	if _, err = s.ClaimAssignedIssue(t.Context(), "h2", "w", "agent", "i2", "issue-work"); err != nil {
		t.Fatal(err)
	}
	var inputs string
	if err = s.DB.QueryRow(`SELECT inputs_json FROM restricted_workflow_jobs WHERE principal_id='h2'`).Scan(&inputs); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(inputs, "H1") || !strings.Contains(inputs, "H2") {
		t.Fatal("private source mixed")
	}
	if count(t, s.DB, "journal_entries") != 0 {
		t.Fatal("private claim entered shared journal")
	}
}

func TestConcurrentClaimsHaveOneDurableOwner(t *testing.T) {
	s, _ := fixture(t)
	type result struct {
		r   restrictedworkflow.Receipt
		err error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work")
			results <- result{r, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var id string
	success := 0
	for v := range results {
		if v.err != nil {
			continue
		}
		success++
		if id != "" && v.r.ID != id {
			t.Fatal("two private owners")
		}
		id = v.r.ID
	}
	if success == 0 || count(t, s.DB, "restricted_workflow_jobs") != 1 || count(t, s.DB, "restricted_preflight_reservations") != 1 {
		t.Fatal("claim not atomic")
	}
}

func TestSourceMutationRevokesDispatchAndDelivery(t *testing.T) {
	for _, q := range []string{`UPDATE missions SET project_id='p2' WHERE id='i1'`, `UPDATE missions SET description='changed' WHERE id='i1'`, `UPDATE issue_work SET revision=revision+1 WHERE mission_id='i1'`, `UPDATE projects SET status='paused' WHERE id='p1'`, `DELETE FROM access_grants WHERE project_id='p1'`, `DELETE FROM missions WHERE id='i1'`} {
		t.Run(q, func(t *testing.T) {
			s, e := fixture(t)
			r, err := s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.DB.Exec(q); err != nil {
				t.Fatal(err)
			}
			_, _ = s.Workflow.DispatchNext(t.Context())
			if e.calls != 0 {
				t.Fatal("revoked source dispatched")
			}
			if _, err = s.Workflow.ReceiptForActor(t.Context(), "h1", "w", r.ID); err == nil {
				t.Fatal("revoked receipt exposed")
			}
		})
	}
	s, e := fixture(t)
	r, err := s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work")
	if err != nil {
		t.Fatal(err)
	}
	e.before = func() {
		if _, err := s.DB.Exec(`UPDATE missions SET description='revoked during run' WHERE id='i1'`); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = s.Workflow.DispatchNext(t.Context())
	if _, err = s.Workflow.Result(t.Context(), "h1", "w", r.ID); err == nil {
		t.Fatal("output delivered after source change")
	}
	if _, err = s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work"); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("uncertain replay: %v", err)
	}
}

func TestPrivateIssueFencesLegacyWritersAndAllowsTerminalHistory(t *testing.T) {
	s, _ := fixture(t)
	if _, err := s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by) VALUES('legacy-chat','w','agent','h1')`); err != nil {
		t.Fatal(err)
	}
	assignment := `INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,mission_id,assigned_to_id,task,status) VALUES(?,'w','legacy-chat','agent','i1','agent','work',?)`
	if _, err := s.DB.Exec(assignment, "active", "PENDING"); err == nil {
		t.Fatal("legacy active assignment bypass")
	}
	if _, err := s.DB.Exec(assignment, "terminal", "COMPLETED"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE assignments SET status='RUNNING' WHERE id='terminal'`); err == nil {
		t.Fatal("legacy historical assignment restarted")
	}
	execution := `INSERT INTO issue_executions(id,mission_id,work_revision,brief_revision,reviewer_agent_id,stage,created_at,updated_at) VALUES(?,'i1',0,0,'agent',?,'2026-09-30','2026-09-30')`
	if _, err := s.DB.Exec(execution, "active-execution", "working"); err == nil {
		t.Fatal("legacy execution bypass")
	}
	if _, err := s.DB.Exec(execution, "terminal-execution", "accepted"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE issue_executions SET stage='working' WHERE id='terminal-execution'`); err == nil {
		t.Fatal("historical execution restarted")
	}
	var origin string
	if err := s.DB.QueryRow(`SELECT origin_attempt_id FROM restricted_preflight_reservations`).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	// Removing a binding cannot strip its source authority dependency.
	if _, err := s.DB.Exec(`DELETE FROM restricted_preflight_reservations WHERE origin_attempt_id=?`, origin); err != nil {
		t.Fatal(err)
	}
	var revoked sql.NullString
	if err := s.DB.QueryRow(`SELECT revoked_at FROM access_attempts WHERE id=?`, origin).Scan(&revoked); err != nil || !revoked.Valid {
		t.Fatalf("removed binding preserves authority: %v", err)
	}
}

func TestLegacyBusyAndBudgetPreventPaidWake(t *testing.T) {
	s, e := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by) VALUES('legacy-chat','w','agent','h1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,mission_id,assigned_to_id,task,status) VALUES('legacy','w','legacy-chat','agent','i1','agent','work','PENDING')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work"); !errors.Is(err, ErrBusy) {
		t.Fatalf("legacy busy %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE assignments SET status='COMPLETED' WHERE id='legacy'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE budget_limits SET limit_usd=0 WHERE id='cap'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work"); err == nil {
		t.Fatal("exhausted budget passed")
	}
	if e.calls != 0 || count(t, s.DB, "restricted_workflow_jobs") != 0 || count(t, s.DB, "cost_ledger") != 0 {
		t.Fatal("preflight caused paid wake")
	}
}

func TestCompletedRevisionCanRunAgainButRecoveryNeverReplays(t *testing.T) {
	s, e := fixture(t)
	r, err := s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Workflow.DispatchNext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`UPDATE missions SET description='new authorized brief' WHERE id='i1'`); err != nil {
		t.Fatal(err)
	}
	r2, err := s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work")
	if err != nil || r2.ID == r.ID {
		t.Fatalf("new revision %v %#v", err, r2)
	}
	if _, err = s.DB.Exec(`UPDATE restricted_workflow_jobs SET state='running' WHERE id=?`, r2.ID); err != nil {
		t.Fatal(err)
	}
	recovered, err := restrictedworkflow.New(s.DB, e)
	if err != nil {
		t.Fatal(err)
	}
	recovered.SourceChecker = CheckSource
	if err = recovered.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	s.Workflow = recovered
	if _, err = s.ClaimAssignedIssue(t.Context(), "h1", "w", "agent", "i1", "issue-work"); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("recovery replay %v", err)
	}
	if e.calls != 1 {
		t.Fatal("recovery repeated effects")
	}
}
