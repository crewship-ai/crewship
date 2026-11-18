package api

import (
	"net/http"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper"
)

func TestKeeperRunAdmissionRejectsUnavailableAndForeignRuns(t *testing.T) {
	for _, kind := range []string{"access", "execute"} {
		for _, scenario := range []string{"missing", "foreign-agent", "foreign-crew", "completed", "failed", "cancelled", "timeout", "recovered_stop"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				db := setupTestDB(t)
				ws, crew, agent, credential := seedKeeperFixture(t, db)
				writer := newKeeperTestJournal(t, db)
				const run = "keeper-admission-fixture"
				if scenario != "missing" {
					recordedAgent, recordedCrew := agent, crew
					if scenario == "foreign-agent" {
						recordedAgent = "another-agent"
					}
					if scenario == "foreign-crew" {
						recordedCrew = "another-crew"
					}
					if scenario != "recovered_stop" {
						if _, err := writer.Emit(t.Context(), journal.Entry{WorkspaceID: ws, CrewID: recordedCrew, AgentID: recordedAgent, TraceID: run, Type: journal.EntryRunStarted, ActorType: journal.ActorAgent, ActorID: recordedAgent, Summary: "fixture start"}); err != nil {
							t.Fatal(err)
						}
					}
					switch scenario {
					case "completed", "failed", "cancelled", "timeout", "recovered_stop":
						if _, err := writer.Emit(t.Context(), journal.Entry{WorkspaceID: ws, CrewID: crew, AgentID: agent, TraceID: run, Type: journal.EntryType("run." + scenario), ActorType: journal.ActorAgent, ActorID: agent, Summary: "fixture terminal", Payload: map[string]any{"start_recorded": scenario != "recovered_stop"}}); err != nil {
							t.Fatal(err)
						}
					}
					if err := writer.Flush(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				ctr := &mockContainerExec{output: "fixture", exitCode: 0, execID: "fixture-exec"}
				h := NewKeeperHandler(db, "synthetic-internal", &mockEvaluator{resp: keeper.GatekeeperResponse{Decision: "ALLOW", Reason: "fixture", RiskScore: 1}}, keeperEventsLogger()).WithSecrets(&mockSecretGetter{secrets: map[string]string{credential: "synthetic-fixture-value"}}).WithContainer(ctr)
				var code int
				if kind == "access" {
					code = doKeeperRequest(h, keeperRequestBody{WorkspaceID: ws, RequestingCrewID: crew, RequestingAgentID: agent, RequestingRunID: run, CredentialID: credential, Intent: "fixture"}).Code
				} else {
					code = doKeeperExecute(h, keeperExecuteBody{WorkspaceID: ws, RequestingCrewID: crew, RequestingAgentID: agent, RequestingRunID: run, CredentialID: credential, Intent: "fixture", Command: "true", ContainerID: "test-container"}).Code
				}
				if code != http.StatusForbidden {
					t.Fatalf("invalid run returned %d", code)
				}
				var n int
				if err := db.QueryRow(`SELECT COUNT(*) FROM keeper_requests`).Scan(&n); err != nil || n != 0 {
					t.Fatalf("invalid run persisted request: %d %v", n, err)
				}
			})
		}
	}
}
