//go:build linux

package restricteddispatch

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/paymaster"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func missionAccountingFixture(t *testing.T, issue bool) (Authority, []string, []access.Attempt) {
	t.Helper()
	a := providerFixture(t)
	ctx := t.Context()
	for _, q := range []string{
		`UPDATE agents SET llm_model='gpt-5-mini',restricted_execution_profile='responses_text' WHERE id='a'`,
		`INSERT INTO projects(id,workspace_id,name,slug) VALUES('project','w','Project','budget-project')`,
		`INSERT INTO missions(id,workspace_id,crew_id,delegate_agent_id,lead_agent_id,trace_id,title,mission_type,status,project_id) VALUES('issue','w','crew','a','a','issue-budget-trace','Issue','issue','TODO','project')`,
		`INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash,status) VALUES('recipe','w','Recipe','budget-recipe','{}','fixture','active')`,
		`INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('workspace-cap','w','workspace','w','month',100,'hard'),('mission-cap','w','mission','issue','month',.006,'hard')`,
	} {
		if _, err := a.Store.DB.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	rights := []access.Right{{Kind: "agent", ID: "a", Operation: "run"}, {Kind: "agent", ID: "a", Operation: "delegate"}, {Kind: "project", ID: "project", Operation: "read"}}
	setRights(t, a, "h1", rights)
	originHandle, origin, err := a.Store.Admit(ctx, "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.AppendContext(ctx, originHandle, access.ContextUser, "classified issue source"); err != nil {
		t.Fatal(err)
	}
	if err = a.Store.CompleteAttempt(ctx, originHandle); err != nil {
		t.Fatal(err)
	}
	cipher, err := encryption.Encrypt(originHandle)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	facet := ""
	if issue {
		facet = "issue"
	}
	tx, err := a.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if issue {
		_, err = tx.ExecContext(ctx, `INSERT INTO restricted_preflight_reservations(origin_attempt_id,workflow_id,workspace_id,principal_id,member_id,member_revision,agent_id,issue_id,project_id,source_hash,authority_hash,recipe_hash,work_revision,brief_revision)
 SELECT ?,'job','w','h1',?,?,'a','issue','project',?,?,'fixture',revision,brief_revision FROM issue_work WHERE mission_id='issue'`, origin.ID, origin.Member, origin.Revision, strings.Repeat("a", 64), strings.Repeat("b", 64))
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO restricted_workflow_jobs(id,workspace_id,principal_id,member_id,member_revision,pipeline_id,recipe_hash,recipe_json,agent_id,execution_profile,chat_id,origin_attempt_id,origin_handle_ciphertext,source_kind,inputs_json,created_at,fire_at,expires_at,source_facet) VALUES('job','w','h1',?,?,'recipe','fixture','{}','a','responses_text','c1',?,?,'manual','{}',?,?,?,?)`, origin.Member, origin.Revision, origin.ID, cipher, tsformat.Format(now), tsformat.Format(now), tsformat.Format(now.Add(time.Hour)), facet)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := a.ProviderDelegationHash(ctx, "h1", "w", "a")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.FreezeWorkflowProvider(ctx, tx, "job", "a", hash, 1000); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.DB.ExecContext(ctx, `UPDATE restricted_workflow_jobs SET state='running' WHERE id='job'`); err != nil {
		t.Fatal(err)
	}
	parentHandle, parent, err := a.Store.Admit(ctx, "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.BuildContext(ctx, parent, "orchestrate classified source"); err != nil {
		t.Fatal(err)
	}
	if err = a.BindProviderDelegation(ctx, parentHandle, "job", "a", hash, 1000); err != nil {
		t.Fatal(err)
	}
	var handles []string
	var attempts []access.Attempt
	for range 2 {
		h, attempt, err := a.PrepareDelegatedResponses(ctx, "h1", "w", "a", "c1", parentHandle, rights, 1000, func(context.Context, string, access.Attempt) ([]string, error) { return []string{"/bin/true"}, nil })
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, h)
		attempts = append(attempts, attempt)
	}
	return a, handles, attempts
}

func TestIssueMissionAccountingConcurrentLeaves(t *testing.T) {
	a, handles, _ := missionAccountingFixture(t, true)
	var sequence int
	var databaseName, databasePath string
	if err := a.Store.DB.QueryRowContext(t.Context(), `PRAGMA database_list`).Scan(&sequence, &databaseName, &databasePath); err != nil {
		t.Fatal(err)
	}
	second, err := sql.Open("sqlite", "file:"+databasePath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	authorities := []Authority{a, {Store: access.Store{DB: second}}}
	start := make(chan struct{})
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for index, handle := range handles {
		wg.Add(1)
		go func(authority Authority, h string) {
			defer wg.Done()
			<-start
			id, err := authority.BrokerReserve(t.Context(), h, "key", "gpt-5-mini", 1000, 1000)
			results <- result{id, err}
		}(authorities[index], handle)
	}
	close(start)
	wg.Wait()
	close(results)
	var accepted []string
	for result := range results {
		if result.err == nil {
			accepted = append(accepted, result.id)
			continue
		}
		var denied *paymaster.BudgetExceededError
		if !errors.As(result.err, &denied) || len(denied.Statuses) != 1 || denied.Statuses[0].Budget.ScopeKind != "mission" || denied.Statuses[0].Budget.ScopeID != "issue" {
			t.Fatalf("unexpected concurrent denial: %v", result.err)
		}
	}
	if len(accepted) != 1 {
		t.Fatalf("mission cap admitted %d concurrent leaves", len(accepted))
	}
	var mission string
	var total float64
	if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT mission_id,SUM(cost_usd) FROM cost_ledger GROUP BY mission_id`).Scan(&mission, &total); err != nil || mission != "issue" || total > .006 {
		t.Fatalf("wrong mission debit %q %.8f %v", mission, total, err)
	}
	settled := false
	for _, h := range handles {
		// Only the owning opaque handle can settle; unknown usage keeps its full debit.
		if err := a.BrokerSettle(t.Context(), h, accepted[0], restrictedruntime.BrokerUsage{}); err == nil {
			settled = true
			break
		}
	}
	if !settled {
		t.Fatal("accepted reservation lost its payer")
	}

	for _, h := range handles {
		if _, err := a.BrokerReserve(t.Context(), h, "key", "gpt-5-mini", 1000, 1000); err == nil {
			t.Fatal("unknown usage reset mission cap")
		}
	}
}

func TestIssueMissionAccountingRejectsMalformedBinding(t *testing.T) {
	a, handles, attempts := missionAccountingFixture(t, true)
	// Even bypassing triggers to simulate a damaged restore cannot select an
	// unrelated issue or erase the financial scope of an issue-bound leaf.
	if _, err := a.Store.DB.ExecContext(t.Context(), `DROP TRIGGER restricted_preflight_immutable; UPDATE restricted_preflight_reservations SET principal_id='h2' WHERE workflow_id='job'`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.accountingMission(t.Context(), attempts[0]); err == nil {
		t.Fatal("mismatched issue principal accepted")
	}
	if _, err := a.BrokerReserve(t.Context(), handles[0], "key", "gpt-5-mini", 1000, 1000); err == nil {
		t.Fatal("malformed issue binding admitted provider accounting")
	}
	var count int
	if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM cost_ledger`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("malformed binding produced debit %d %v", count, err)
	}

}

func TestManualWorkflowAccountingLeavesMissionUnselected(t *testing.T) {
	a, handles, attempts := missionAccountingFixture(t, false)
	if mission, err := a.accountingMission(t.Context(), attempts[0]); err != nil || mission != "" {
		t.Fatalf("manual mission %q %v", mission, err)
	}
	for _, h := range handles {
		if _, err := a.BrokerReserve(t.Context(), h, "key", "gpt-5-mini", 1000, 1000); err != nil {
			t.Fatal("unrelated mission cap blocked manual workflow", err)
		}
	}
	var count int
	if err := a.Store.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM cost_ledger WHERE mission_id IS NOT NULL AND mission_id<>''`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("manual selected mission: %d %v", count, err)
	}
}
