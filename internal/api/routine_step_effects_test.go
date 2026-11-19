package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/policy"
)

// recordingCrewshipActions forwards every dispatched step and keeps the exact
// request. A resumed step rebuilds the same request (the operation key is
// derived from run, foreach path and step), so replaying a recorded request
// is what the executor sends after a crash that lost the step's output.
type recordingCrewshipActions struct {
	inner pipeline.CrewshipActions
	mu    sync.Mutex
	reqs  []pipeline.CrewshipRequest
	outs  []string
}

func (r *recordingCrewshipActions) Do(ctx context.Context, req pipeline.CrewshipRequest) (string, error) {
	out, err := r.inner.Do(ctx, req)
	if err == nil {
		r.mu.Lock()
		r.reqs = append(r.reqs, req)
		r.outs = append(r.outs, out)
		r.mu.Unlock()
	}
	return out, err
}

type routineStepFixture struct {
	chatIssueFixture
	server  *httptest.Server
	actions pipeline.CrewshipActions
	assign  *AssignmentHandler
}

// newRoutineStepFixture serves the six routine-reachable write routes through
// the same wrapping the production router applies.
func newRoutineStepFixture(t *testing.T, level string) routineStepFixture {
	t.Helper()
	f := newChatIssueFixture(t)
	execOrFatal(t, f.h.db, `UPDATE crews SET autonomy_level=? WHERE id=?`, level, f.crew)
	seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-901", "BACKLOG")
	seedIssue(t, f.h.db, f.workspace, f.crew, f.agent, "ENG-902", "BACKLOG")

	assign := NewAssignmentHandler(f.h.db, nil, nil, "step-host", newTestLogger())
	lock := chatbridge.NewAgentRunLock()
	lock.TryStart("agent-worker")
	assign.SetAgentRunLock(lock)
	t.Cleanup(assign.WaitDispatches)
	// Keep the assignment in the durable queue instead of starting a CLI.
	execOrFatal(t, f.h.db, `UPDATE crews SET max_concurrent_agents=1 WHERE id=?`, f.crew)
	execOrFatal(t, f.h.db, `INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,created_at) VALUES('step-busy',?,?,?,?,'busy','RUNNING',datetime('now'))`, f.workspace, f.chat, f.agent, f.agent)
	queries := NewQueryHandler(f.h.db, nil, nil, "step-host", newTestLogger())

	host := NewInternalHandler(f.h.db, "step-host", newTestLogger())
	auth := func(verb string, h http.HandlerFunc) http.Handler {
		return host.requireInternal(withHostChatOrigin(withRoutineStepEffect(f.h.db, verb, h)))
	}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/internal/issues", auth("issue.create", f.h.Create))
	mux.Handle("PATCH /api/v1/internal/issues/{identifier}", auth("issue.update", f.h.UpdateStatus))
	mux.Handle("POST /api/v1/internal/issues/{identifier}/comments", auth("issue.comment", f.h.CreateComment))
	mux.Handle("POST /api/v1/internal/issues/{identifier}/relations", auth("issue.link", f.h.CreateRelation))
	mux.Handle("POST /api/v1/internal/assignments", auth("assignment.create", assign.Create))
	mux.Handle("POST /api/v1/internal/escalations", auth("escalation.create", queries.CreateEscalation))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	actions := newCrewshipActions(server.URL, "step-host", policy.NewResolver(f.h.db), f.h.db, newTestLogger())
	return routineStepFixture{chatIssueFixture: f, server: server, actions: actions, assign: assign}
}

func (f routineStepFixture) saveRoutine(t *testing.T, slug string, steps []any) *pipeline.Pipeline {
	t.Helper()
	definition, _ := json.Marshal(map[string]any{"name": slug, "steps": steps})
	now := time.Now()
	// Agent-authored, no source run: a scheduled/webhook/manual routine, not a
	// chat-origin one. These had no replay protection before #5332.
	recipe, err := pipeline.NewStore(f.h.db).Save(t.Context(), pipeline.SaveInput{WorkspaceID: f.workspace, Slug: slug, Name: slug, DefinitionJSON: string(definition), Author: pipeline.AuthorMeta{CrewID: f.crew, AgentID: f.agent, Via: pipeline.AuthoredViaAgent}, LastTestRunAt: &now, LastTestRunPassed: true})
	if err != nil {
		t.Fatal(err)
	}
	return recipe
}

func (f routineStepFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.h.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// #5332: a crash after a step's route wrote but before the executor persisted
// the step's output re-runs the step on resume with the same request. Every
// routine-reachable write verb must turn that second call into a replay of the
// first answer, not a second write — for a run with no chat origin.
func TestRoutineStepReplayAfterCrashWritesOnce(t *testing.T) {
	f := newRoutineStepFixture(t, "full")
	recipe := f.saveRoutine(t, "step-once", []any{
		map[string]any{"id": "create", "type": "crewship", "action": "issue.create", "args": map[string]any{"title": "Routine created once"}},
		map[string]any{"id": "update", "type": "crewship", "action": "issue.update", "args": map[string]any{"identifier": "ENG-901", "priority": "high", "comment": "Routine inline progress"}},
		map[string]any{"id": "comment", "type": "crewship", "action": "issue.comment", "args": map[string]any{"identifier": "ENG-901", "body": "Routine comment once"}},
		map[string]any{"id": "link", "type": "crewship", "action": "issue.link", "args": map[string]any{"identifier": "ENG-901", "target_identifier": "ENG-902", "relation_type": "relates_to"}},
		map[string]any{"id": "assign", "type": "crewship", "action": "assignment.create", "args": map[string]any{"target_slug": "worker", "task": "Routine task once", "chat_id": f.chat}},
		map[string]any{"id": "escalate", "type": "crewship", "action": "escalation.create", "args": map[string]any{"from_slug": "lead", "reason": "Routine escalation once", "chat_id": f.chat}},
	})
	rec := &recordingCrewshipActions{inner: f.actions}
	executor := pipeline.NewExecutor(pipeline.NewStore(f.h.db), pipeline.NewResolver(f.h.db), nil, nil).WithRunStore(pipeline.NewRunStore(f.h.db)).WithCrewshipActions(rec)
	res, err := executor.Run(t.Context(), pipeline.RunInput{PipelineID: recipe.ID, WorkspaceID: f.workspace, Mode: pipeline.ModeRun})
	f.assign.WaitDispatches()
	if err != nil || res.Status != "COMPLETED" {
		t.Fatalf("routine: %+v %v", res, err)
	}
	if len(rec.reqs) != 6 {
		t.Fatalf("dispatched %d steps, want 6", len(rec.reqs))
	}
	// The crash: the process died before the terminal write, so the run row is
	// still in flight when the boot scan resumes it.
	execOrFatal(t, f.h.db, `UPDATE pipeline_runs SET status='running' WHERE id=?`, res.RunID)
	for i, req := range rec.reqs {
		out, err := f.actions.Do(t.Context(), req)
		if err != nil {
			t.Errorf("resumed %s: %v", req.Verb, err)
			continue
		}
		if out != rec.outs[i] {
			t.Errorf("resumed %s answered %s, want the first answer %s", req.Verb, out, rec.outs[i])
		}
	}
	f.assign.WaitDispatches()
	for _, tc := range []struct {
		verb, query string
	}{
		{"issue.create", `SELECT COUNT(*) FROM missions WHERE title='Routine created once'`},
		{"issue.update", `SELECT COUNT(*) FROM mission_comments WHERE body='Routine inline progress'`},
		{"issue.comment", `SELECT COUNT(*) FROM mission_comments WHERE body='Routine comment once'`},
		{"issue.link", `SELECT COUNT(*) FROM mission_relations WHERE relation_type='relates_to'`},
		{"assignment.create", `SELECT COUNT(*) FROM assignments WHERE task='Routine task once'`},
		{"escalation.create", `SELECT COUNT(*) FROM escalations WHERE reason='Routine escalation once'`},
	} {
		if n := f.count(t, tc.query); n != 1 {
			t.Errorf("%s: %d records after a resumed step, want exactly 1", tc.verb, n)
		}
	}
}

// A held action's approval stays `approved` after the write. Presented again
// by a resumed step, it must not authorise a second write.
func TestRoutineHeldApprovalAuthorisesOneWrite(t *testing.T) {
	f := newRoutineStepFixture(t, "strict")
	recipe := f.saveRoutine(t, "held-once", []any{
		map[string]any{"id": "create", "type": "crewship", "action": "issue.create", "args": map[string]any{"title": "Approved once"}},
	})
	execOrFatal(t, f.h.db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,pipeline_version,status,started_at) VALUES('held-run',?,?,?,1,'running','2026-11-19')`, f.workspace, recipe.ID, recipe.Slug)
	req := pipeline.CrewshipRequest{
		Verb: "issue.create", Args: map[string]any{"title": "Approved once"}, OperationKey: "routine:held-once-create",
		WorkspaceID: f.workspace, CrewID: f.crew, AgentID: f.agent, RunID: "held-run", StepID: "create",
	}
	stepKey := pipeline.CrewshipApprovalStepKey(req.StepID, pipeline.CrewshipApprovalBinding(req.OperationKey, req.Verb, req.Args))
	execOrFatal(t, f.h.db, `INSERT INTO pipeline_waitpoints(token,workspace_id,pipeline_run_id,step_id,kind,status,decided_by_user_id,timeout_at) VALUES('held-token',?,?,?,'approval','approved',?,'2099-01-01T00:00:00Z')`, f.workspace, "held-run", stepKey, f.user)
	req.ApprovalToken = "held-token"
	first, err := f.actions.Do(t.Context(), req)
	if err != nil {
		t.Fatalf("approved write: %v", err)
	}
	second, err := f.actions.Do(t.Context(), req)
	if err != nil {
		t.Fatalf("resumed approved write: %v", err)
	}
	if first != second {
		t.Fatalf("resumed step answered %s, want the first answer %s", second, first)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM missions WHERE title='Approved once'`); n != 1 {
		t.Fatalf("one approval authorised %d writes", n)
	}
}

// The production router must wrap every route a routine step writes through.
// A step key naming a run of another workspace is refused by the wrapper
// before the handler runs, which only a wrapped route can answer.
func TestRouterWrapsEveryRoutineStepWriteRoute(t *testing.T) {
	db := setupTestDB(t)
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('ws-run-owner','Owner','ws-run-owner')`)
	execOrFatal(t, db, `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('router-recipe','ws-run-owner','router','Router','{}','h')`)
	execOrFatal(t, db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,pipeline_version,status,started_at) VALUES('run-owned','ws-run-owner','router-recipe','router',1,'running','2026-11-19')`)
	const hostToken = "step-effect-router-host"
	router, err := NewRouter(db, "step-effect-router-jwt-secret-padding", newTestLogger(), WithInternalToken(hostToken))
	if err != nil {
		t.Fatal(err)
	}
	routes := map[string][2]string{
		"issue.create":      {"POST", "/api/v1/internal/issues"},
		"issue.update":      {"PATCH", "/api/v1/internal/issues/ENG-1"},
		"issue.comment":     {"POST", "/api/v1/internal/issues/ENG-1/comments"},
		"issue.link":        {"POST", "/api/v1/internal/issues/ENG-1/relations"},
		"assignment.create": {"POST", "/api/v1/internal/assignments"},
		"escalation.create": {"POST", "/api/v1/internal/escalations"},
	}
	if len(routes) != len(routineStepEffectVerbs) {
		t.Fatalf("test covers %d routes, routineStepEffectVerbs lists %d", len(routes), len(routineStepEffectVerbs))
	}
	for verb := range routineStepEffectVerbs {
		route, ok := routes[verb]
		if !ok {
			t.Fatalf("no route under test for %s", verb)
		}
		t.Run(verb, func(t *testing.T) {
			req := httptest.NewRequest(route[0], route[1], jsonBody(map[string]any{"workspace_id": "ws-none"}))
			req.RemoteAddr = "127.0.0.1:1234"
			req.Header.Set("X-Internal-Token", hostToken)
			req.Header.Set("X-Crewship-Pipeline-Run", "run-owned")
			req.Header.Set(routineStepKeyHeader, "routine:none")
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "step_effect_run_unavailable") {
				t.Fatalf("%s %s is not wrapped: %d %s", route[0], route[1], rr.Code, rr.Body.String())
			}
		})
	}
}

// A step key is the host's to send: without a host pipeline run, for a run of
// another workspace, or with a request that differs from the recorded one, it
// is refused and nothing is written.
func TestRoutineStepEffectRefusals(t *testing.T) {
	f := newRoutineStepFixture(t, "full")
	recipe := f.saveRoutine(t, "refusals", []any{map[string]any{"id": "create", "type": "crewship", "action": "issue.create", "args": map[string]any{"title": "x"}}})
	execOrFatal(t, f.h.db, `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,pipeline_version,status,started_at) VALUES('refusal-run',?,?,?,1,'running','2026-11-19')`, f.workspace, recipe.ID, recipe.Slug)
	execOrFatal(t, f.h.db, `INSERT INTO workspaces(id,name,slug) VALUES('step-foreign','Foreign','step-foreign')`)
	var lastBody string
	call := func(run, key, title, workspace string) *http.Response {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"workspace_id": workspace, "crew_id": f.crew, "title": title, "author_agent_id": f.agent, "author_run_id": run})
		req, _ := http.NewRequest("POST", f.server.URL+"/api/v1/internal/issues", strings.NewReader(string(raw)))
		req.Header.Set("X-Internal-Token", "step-host")
		if run != "" {
			req.Header.Set("X-Crewship-Pipeline-Run", run)
		}
		req.Header.Set(routineStepKeyHeader, key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastBody = string(body)
		return resp
	}
	if resp := call("", "routine:k", "No run", f.workspace); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("key without a host run: %d", resp.StatusCode)
	}
	if resp := call("refusal-run", "routine:k", "Foreign", "step-foreign"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("key for another workspace's run: %d", resp.StatusCode)
	}
	if resp := call("refusal-run", strings.Repeat("k", maxRoutineStepKeyLen+1), "Long", f.workspace); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized key: %d", resp.StatusCode)
	}
	first := call("refusal-run", "routine:k", "First", f.workspace)
	if first.StatusCode != http.StatusCreated || first.Header.Get(routineStepReplayedHeader) != "" {
		t.Fatalf("first write: %d replayed=%q", first.StatusCode, first.Header.Get(routineStepReplayedHeader))
	}
	replay := call("refusal-run", "routine:k", "First", f.workspace)
	if replay.StatusCode != http.StatusCreated || replay.Header.Get(routineStepReplayedHeader) != "true" {
		t.Fatalf("replay: %d replayed=%q", replay.StatusCode, replay.Header.Get(routineStepReplayedHeader))
	}
	if resp := call("refusal-run", "routine:k", "Changed", f.workspace); resp.StatusCode != http.StatusConflict {
		t.Fatalf("same key, other request: %d", resp.StatusCode)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM missions WHERE title IN ('No run','Foreign','Long','First','Changed')`); n != 1 {
		t.Fatalf("%d issues written, want 1", n)
	}
	// A run with no row (a nested call_pipeline run, an unsaved draft) is
	// served as before: the key is ignored and nothing is recorded. The issue
	// route itself still refuses an unknown pipeline run.
	if resp := call("nested-run", "routine:k", "Nested", f.workspace); resp.StatusCode != http.StatusForbidden || !strings.Contains(lastBody, "run_authority_unavailable") {
		t.Fatalf("rowless run must reach the route's own check: %d %s", resp.StatusCode, lastBody)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM routine_step_effects WHERE run_id='nested-run'`); n != 0 {
		t.Fatalf("%d step effects recorded for a rowless run", n)
	}
	// The ledger row goes with its run.
	execOrFatal(t, f.h.db, `DELETE FROM pipeline_runs WHERE id='refusal-run'`)
	if n := f.count(t, `SELECT COUNT(*) FROM routine_step_effects WHERE run_id='refusal-run'`); n != 0 {
		t.Fatalf("%d step effects outlived their run", n)
	}
}
