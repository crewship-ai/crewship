package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/policy"
)

func TestAssignmentDirectDSLRetainsHumanOriginAtRealHandler(t *testing.T) {
	f := newChatIssueFixture(t)
	execOrFatal(t, f.h.db, `UPDATE crews SET autonomy_level='full' WHERE id=?`, f.crew)
	handler := NewAssignmentHandler(f.h.db, nil, nil, "dsl-host-token", newTestLogger())
	lock := chatbridge.NewAgentRunLock()
	lock.TryStart("agent-worker")
	handler.SetAgentRunLock(lock)
	t.Cleanup(handler.WaitDispatches)
	// Keep recovery in the actual queue instead of executing a CLI in this test.
	execOrFatal(t, f.h.db, `UPDATE crews SET max_concurrent_agents=1 WHERE id=?`, f.crew)
	execOrFatal(t, f.h.db, `INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,created_at) VALUES('dsl-busy',?,?,?,?,'busy','RUNNING',datetime('now'))`, f.workspace, f.chat, f.agent, f.agent)
	host := NewInternalHandler(f.h.db, "dsl-host-token", newTestLogger())
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/internal/assignments", host.requireInternal(withHostChatOrigin(http.HandlerFunc(handler.Create))))
	server := httptest.NewServer(mux)
	defer server.Close()
	store := pipeline.NewStore(f.h.db)
	now := time.Now()
	definition := `{"name":"Delegate","steps":[{"id":"assign","type":"crewship","action":"assignment.create","args":{"target_slug":"worker","task":"Delegated DSL task","chat_id":"` + f.chat + `","source_run_id":"forged-source","chat_origin_id":"forged-origin"}}]}`
	recipe, err := store.Save(t.Context(), pipeline.SaveInput{WorkspaceID: f.workspace, Slug: "dsl-delegate", Name: "Delegate", DefinitionJSON: definition, Author: pipeline.AuthorMeta{CrewID: f.crew, AgentID: f.agent, Via: pipeline.AuthoredViaAgent}, SourceRunID: f.run, SourceAgentID: f.agent, LastTestRunAt: &now, LastTestRunPassed: true})
	if err != nil {
		t.Fatal(err)
	}
	var expected string
	if err := f.h.db.QueryRow(`SELECT chat_origin_id FROM pipelines WHERE id=?`, recipe.ID).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, f.h.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,trace_id,entry_type,actor_type,summary,payload) VALUES('dsl-parent-ended',?,?,?,'run.completed','orchestrator','done','{}')`, f.workspace, f.agent, f.run)
	actions := newCrewshipActions(server.URL, "dsl-host-token", policy.NewResolver(f.h.db), f.h.db, newTestLogger())
	executor := pipeline.NewExecutor(store, pipeline.NewResolver(f.h.db), nil, nil).WithRunStore(pipeline.NewRunStore(f.h.db)).WithCrewshipActions(actions)
	result, err := executor.Run(t.Context(), pipeline.RunInput{PipelineID: recipe.ID, WorkspaceID: f.workspace, Mode: pipeline.ModeRun})
	handler.WaitDispatches()
	if err != nil || result.Status != "COMPLETED" {
		t.Fatalf("DSL delegation %+v %v", result, err)
	}
	var origin, status string
	if err := f.h.db.QueryRow(`SELECT COALESCE(chat_origin_id,''),status FROM assignments WHERE task='Delegated DSL task'`).Scan(&origin, &status); err != nil {
		t.Fatal(err)
	}
	if expected == "" || origin != expected || status != "QUEUED" {
		t.Fatalf("DSL child lost authority origin=%q expected=%q status=%s", origin, expected, status)
	}
}
