package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

type keeperEndRunDuringJudge struct {
	t                           *testing.T
	writer                      *journal.Writer
	workspace, crew, agent, run string
}

func (e *keeperEndRunDuringJudge) Evaluate(ctx context.Context, _ gatekeeper.EvalRequest) (keeper.GatekeeperResponse, error) {
	e.t.Helper()
	if _, err := e.writer.Emit(ctx, journal.Entry{WorkspaceID: e.workspace, CrewID: e.crew, AgentID: e.agent, TraceID: e.run, Type: journal.EntryRunCompleted, ActorType: journal.ActorAgent, ActorID: e.agent, Summary: "fixture ends while judge evaluates"}); err != nil {
		e.t.Fatal(err)
	}
	if err := e.writer.Flush(ctx); err != nil {
		e.t.Fatal(err)
	}
	return keeper.GatekeeperResponse{Decision: "ALLOW", Reason: "fixture allowed", RiskScore: 1}, nil
}

func TestKeeperExecuteRejectsRunEndedDuringJudgement(t *testing.T) {
	db := setupTestDB(t)
	ws, crew, agent, credential := seedKeeperFixture(t, db)
	writer := newKeeperTestJournal(t, db)
	const run = "keeper-ends-during-judge"
	if _, err := writer.Emit(t.Context(), journal.Entry{WorkspaceID: ws, CrewID: crew, AgentID: agent, TraceID: run, Type: journal.EntryRunStarted, ActorType: journal.ActorAgent, ActorID: agent, Summary: "fixture starts"}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctr := &mockContainerExec{output: "fixture", exitCode: 0, execID: "fixture-exec"}
	h := NewKeeperHandler(db, "synthetic-internal", &keeperEndRunDuringJudge{t: t, writer: writer, workspace: ws, crew: crew, agent: agent, run: run}, keeperEventsLogger()).WithSecrets(&mockSecretGetter{secrets: map[string]string{credential: "synthetic-fixture-value"}}).WithContainer(ctr)
	body, _ := json.Marshal(map[string]string{"workspace_id": ws, "requesting_crew_id": crew, "requesting_agent_id": agent, "requesting_run_id": run, "credential_id": credential, "intent": "fixture", "command": "true", "container_id": "test-container"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/execute", strings.NewReader(string(body)))
	out := httptest.NewRecorder()
	h.HandleExecute(out, req)
	if out.Code != http.StatusForbidden || ctr.lastExecContainerID != "" {
		t.Fatalf("ended run executed: status=%d executed=%v", out.Code, ctr.lastExecContainerID != "")
	}
}
