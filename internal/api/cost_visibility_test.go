package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/costvis"
	"github.com/crewship-ai/crewship/internal/paymaster"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// #3590 follow-up: the first fix covered only the dedicated Spend endpoints.
// Money also rode along on run lists, run detail, metrics, missions, work
// items, the journal and the agent inbox. This sweep seeds a non-zero cost
// behind every endpoint that carries one and asserts, per role:
//
//   - OWNER / ADMIN receive the amount;
//   - every other role gets 403 on a cost-only endpoint, and on a mixed
//     endpoint either a refusal for its own reasons or the same body with no
//     cost field and no formatted amount anywhere.
//
// The cost field set is costvis.Keys; the role rule is canReadCosts.
func TestCostVisibility_EveryCostCarryingEndpoint(t *testing.T) {
	db, userID, wsID, crewID, agentID := acrSeed(t, "CLAUDE_CODE", "ANTHROPIC", "claude-sonnet-4-6")
	now := time.Now().UTC()
	ts := now.Format("2006-01-02T15:04:05.000Z")

	// Routine with a monthly budget and one run that cost $42.
	execOrFatal(t, db, `INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at, monthly_budget_usd)
		VALUES ('cv-pipe', ?, 'cv-pipe', 'cv-pipe', '{"name":"x","steps":[]}', 'hash', ?, ?, ?, 100)`, wsID, ts, ts, ts)
	execOrFatal(t, db, `INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, mode, started_at,
		step_outputs_json, cost_usd, duration_ms, triggered_via, inputs_json, created_at, updated_at)
		VALUES ('cv-run', ?, 'cv-pipe', 'cv-pipe', 'completed', 'run', ?, '{}', 42, 10, 'manual', '{}', ?, ?)`, wsID, ts, ts, ts)

	// Journal: a cost.incurred entry and the routine's completed entry.
	execOrFatal(t, db, `INSERT INTO journal_entries (id, workspace_id, crew_id, agent_id, ts, entry_type, severity, priority, actor_type, summary, payload, refs)
		VALUES ('cv-cost', ?, ?, ?, ?, 'cost.incurred', 'info', 'normal', 'system', '$42.0000 spent on anthropic/claude', '{"provider":"anthropic","model":"claude","cost_usd":42}', '{}')`,
		wsID, crewID, agentID, ts)
	execOrFatal(t, db, `INSERT INTO journal_entries (id, workspace_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs)
		VALUES ('cv-prun', ?, ?, 'pipeline.run.completed', 'info', 'normal', 'orchestrator', 'cv-run', 'done', '{"pipeline_id":"cv-pipe","pipeline_slug":"cv-pipe","run_id":"cv-run","total_cost_usd":42}', '{}')`,
		wsID, ts)
	// An agent run whose metadata carries the CLI's cost (run list reads it
	// from the run.started payload).
	execOrFatal(t, db, `INSERT INTO journal_entries (id, workspace_id, agent_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs, trace_id)
		VALUES ('cv-ar-s', ?, ?, ?, 'run.started', 'info', 'normal', 'sidecar', ?, 'r', '{"trigger_type":"chat","metadata":{"total_cost_usd":42,"model_usage":{"m":{"costUSD":42}}}}', '{}', 'cv-trace')`,
		wsID, agentID, ts, agentID)
	execOrFatal(t, db, `INSERT INTO journal_entries (id, workspace_id, agent_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs, trace_id)
		VALUES ('cv-ar-t', ?, ?, ?, 'run.completed', 'info', 'normal', 'sidecar', ?, 'r', '{"exit_code":0}', '{}', 'cv-trace')`,
		wsID, agentID, now.Add(time.Second).Format("2006-01-02T15:04:05.000Z"), agentID)

	// Ledger spend this month for the agent inbox, and a workspace budget for
	// the chat run preflight.
	if _, err := paymaster.Record(t.Context(), db, nil, paymaster.Call{Scope: paymaster.Scope{WorkspaceID: wsID, CrewID: crewID, AgentID: agentID}, Provider: "anthropic", Model: "claude-sonnet-4-6", CostUSD: 42}); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, db, `INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('cv-cap',?,'workspace',?,'month',100,'soft')`, wsID, wsID)
	execOrFatal(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('cv-chat',?,?,?,'private')`, wsID, agentID, userID)

	// Mission with an accumulated cost and a task that carries it.
	missionID := seedMissionRow(t, db, "cv-mission", wsID, crewID, "Costed")
	execOrFatal(t, db, `UPDATE missions SET total_estimated_cost = 42, updated_at = ? WHERE id = ?`, ts, missionID)
	execOrFatal(t, db, `INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,status,task_order,depends_on,estimated_cost,created_at,updated_at)
		VALUES('cv-task',?,?,'t','COMPLETED',1,'[]',42,?,?)`, missionID, agentID, ts, ts)

	// Work item with a costed attempt.
	seedWorkItem(t, db, seededWork{ID: "cv-work", WorkspaceID: wsID, State: "succeeded", AgentID: agentID, Generation: 1, Attempts: 1})
	execOrFatal(t, db, `INSERT INTO work_attempts (run_id, work_id, attempt, generation, lease_owner, lease_expires_at, heartbeat_at, started_at, ended_at, cost_usd)
		VALUES ('cv-work-run','cv-work',1,1,'w',?,?,?,?,42)`, ts, ts, ts, ts)

	// Eval run with a total cost.
	execOrFatal(t, db, `INSERT INTO eval_runs (id, workspace_id, kind, mission_id, status, total_cost_usd, created_at)
		VALUES ('cv-eval', ?, 'replay', ?, 'completed', 42, datetime('now'))`, wsID, missionID)

	pipes := NewPipelineHandler(db, newTestLogger(), nil, nil)
	pipes.SetRunStore(pipeline.NewRunStore(db))
	metrics := NewMetricsHandler(db, newTestLogger())
	journalH := NewJournalHandler(db, newTestLogger(), &emitRecorder{})
	missions := NewMissionHandler(db, nil, nil, newTestLogger())
	work := NewWorkItemsHandler(db, newTestLogger())
	evals := NewEvalHandler(db, newTestLogger())
	inbox := NewAgentInboxHandler(db, newTestLogger())
	runs := NewRunHandler(db, newTestLogger())
	agents := NewAgentHandler(db, newTestLogger())
	router := &Router{db: db, logger: newTestLogger()}

	type endpoint struct {
		name     string
		costOnly bool // 403 below ADMIN; otherwise redact
		path     string
		pv       map[string]string
		call     func(http.ResponseWriter, *http.Request)
	}
	endpoints := []endpoint{
		{"metrics timeseries cost_usd (reviewer #1)", true, "/api/v1/metrics/timeseries?metric=cost_usd&window=24h&bucket=1h", nil, metrics.Timeseries},
		{"metrics timeseries cost_usd by model", true, "/api/v1/metrics/timeseries?metric=cost_usd&window=24h&bucket=1h&group_by=model", nil, metrics.Timeseries},
		{"workspace pipeline-runs list (reviewer #2)", false, "/api/v1/workspaces/" + wsID + "/pipeline-runs", nil, pipes.ListWorkspaceRuns},
		{"pipeline-run detail", false, "/api/v1/workspaces/" + wsID + "/pipeline-runs/cv-run", map[string]string{"runId": "cv-run"}, pipes.GetRun},
		{"pipeline-run tree", false, "/api/v1/workspaces/" + wsID + "/pipeline-runs/cv-run/tree", map[string]string{"runId": "cv-run"}, pipes.GetRunTree},
		{"routine run-records", false, "/api/v1/workspaces/" + wsID + "/pipelines/cv-pipe/run-records", map[string]string{"slug": "cv-pipe"}, pipes.ListRunRecords},
		{"routine runs (journal)", false, "/api/v1/workspaces/" + wsID + "/pipelines/cv-pipe/runs", map[string]string{"slug": "cv-pipe"}, pipes.ListRuns},
		{"routine budget", false, "/api/v1/workspaces/" + wsID + "/pipelines/cv-pipe/budget", map[string]string{"slug": "cv-pipe"}, pipes.GetBudget},
		{"routine health", false, "/api/v1/workspaces/" + wsID + "/pipeline-health", nil, pipes.GetHealth},
		{"routine budget summary", false, "/api/v1/workspaces/" + wsID + "/pipelines/budget-summary", nil, pipes.GetBudgetSummary},
		{"journal list", false, "/api/v1/journal", nil, journalH.List},
		{"journal entry", false, "/api/v1/journal/cv-cost", map[string]string{"id": "cv-cost"}, journalH.Get},
		{"runs list", false, "/api/v1/runs", nil, runs.List},
		{"run detail", false, "/api/v1/runs/cv-trace", map[string]string{"id": "cv-trace"}, runs.Get},
		{"agent runs", false, "/api/v1/agents/" + agentID + "/runs", map[string]string{"agentId": agentID}, agents.ListRuns},
		{"agent inbox", false, "/api/v1/agents/" + agentID + "/inbox", map[string]string{"agentId": agentID}, inbox.Handle},
		{"missions list", false, "/api/v1/missions", nil, missions.ListAll},
		{"crew missions list", false, "/api/v1/crews/" + crewID + "/missions", map[string]string{"crewId": crewID}, missions.List},
		{"mission detail", false, "/api/v1/crews/" + crewID + "/missions/" + missionID, map[string]string{"crewId": crewID, "missionId": missionID}, missions.Get},
		{"mission metrics", false, "/api/v1/mission-metrics", nil, missions.Metrics},
		{"work items list", false, "/api/v1/workspaces/" + wsID + "/work-items", map[string]string{"workspaceId": wsID}, work.List},
		{"work item detail", false, "/api/v1/workspaces/" + wsID + "/work-items/cv-work", map[string]string{"workspaceId": wsID, "workItemId": "cv-work"}, work.Get},
		{"eval runs", false, "/api/v1/eval/runs", nil, evals.ListRuns},
		{"eval run", false, "/api/v1/eval/runs/cv-eval", map[string]string{"id": "cv-eval"}, evals.GetRun},
		{"chat run preflight", false, "/api/v1/chats/cv-chat/run-preflight", map[string]string{"chatId": "cv-chat"}, router.chatRunPreflight},
	}
	roles := []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER", "GUEST"}

	for _, ep := range endpoints {
		for _, role := range roles {
			t.Run(ep.name+"/"+role, func(t *testing.T) {
				req := httptest.NewRequest("GET", ep.path, nil)
				for k, v := range ep.pv {
					req.SetPathValue(k, v)
				}
				req = withWorkspaceUser(req, userID, wsID, role)
				rr := httptest.NewRecorder()
				ep.call(rr, req)
				body := rr.Body.String()

				if role == "OWNER" || role == "ADMIN" {
					if rr.Code != http.StatusOK {
						t.Fatalf("%s: status = %d, want 200; body=%s", role, rr.Code, body)
					}
					if !ep.costOnly && !bodyHasCost(t, rr.Body.Bytes()) {
						t.Fatalf("%s: seeded cost missing from response (fixture broken?): %s", role, body)
					}
					return
				}
				if ep.costOnly {
					if rr.Code != http.StatusForbidden {
						t.Fatalf("%s: status = %d, want 403 on a cost-only endpoint; body=%s", role, rr.Code, body)
					}
					return
				}
				if rr.Code == http.StatusForbidden || rr.Code == http.StatusNotFound {
					return // refused for its own reasons; nothing disclosed
				}
				if rr.Code != http.StatusOK {
					t.Fatalf("%s: unexpected status %d; body=%s", role, rr.Code, body)
				}
				if bodyHasCost(t, rr.Body.Bytes()) {
					t.Fatalf("%s received a cost field; must be omitted below ADMIN: %s", role, body)
				}
				if strings.Contains(body, "$42") {
					t.Fatalf("%s received a formatted amount: %s", role, body)
				}
			})
		}
	}
}

// bodyHasCost reports whether any costvis key carries a value anywhere in the
// JSON document.
func bodyHasCost(t *testing.T, raw []byte) bool {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if _, money := costvis.Keys[k]; money && child != nil {
					return true
				}
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range x {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
}
