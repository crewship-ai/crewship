package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// Transaction-boundary fault injection, not an external SQLite writer: access
// loses run authority at its ALLOW update, execute at its pre-exec lease update.
func TestKeeperRunAdmissionAndLeaseMutationAreAtomic(t *testing.T) {
	for _, kind := range []string{"access", "execute"} {
		t.Run(kind, func(t *testing.T) {
			db := setupTestDB(t)
			ws, crew, agent, credential := seedKeeperFixture(t, db)
			setSecurityLevel(t, db, credential, int(keeper.SecurityLevelL3))
			enableAutoLease(t, db, ws, 900)
			const run = "keeper-write-boundary"
			execOrFatal(t, db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,payload,trace_id) VALUES('admission-start',?,?,?,'run.started','orchestrator','fixture start','{}',?)`, ws, crew, agent, run)
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
			transition := fmt.Sprintf(`INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,payload,trace_id) VALUES('admission-terminal',%s,%s,%s,'run.completed','orchestrator','fixture end at authorization write','{}',%s);`, quote(ws), quote(crew), quote(agent), quote(run))
			trigger := "CREATE TRIGGER fixture_end_before_authorization "
			if kind == "access" {
				trigger += "BEFORE UPDATE OF decision ON keeper_requests WHEN NEW.decision='ALLOW' BEGIN " + transition + " END"
			} else {
				trigger += "BEFORE UPDATE OF expires_at ON agent_credentials WHEN NEW.expires_at IS NOT NULL BEGIN " + transition + " END"
			}
			execOrFatal(t, db, trigger)
			ctr := &mockContainerExec{output: "fixture", exitCode: 0, execID: "fixture-exec"}
			h := NewKeeperHandler(db, "synthetic-internal", &mockEvaluator{resp: keeper.GatekeeperResponse{Decision: "ALLOW", Reason: "fixture", RiskScore: 1}}, keeperEventsLogger()).WithSecrets(&mockSecretGetter{secrets: map[string]string{credential: "synthetic-fixture-value"}}).WithContainer(ctr)
			var code int
			if kind == "access" {
				code = doKeeperRequest(h, keeperRequestBody{WorkspaceID: ws, RequestingCrewID: crew, RequestingAgentID: agent, RequestingRunID: run, CredentialID: credential, Intent: "fixture"}).Code
			} else {
				code = doKeeperExecute(h, keeperExecuteBody{WorkspaceID: ws, RequestingCrewID: crew, RequestingAgentID: agent, RequestingRunID: run, CredentialID: credential, Intent: "fixture", Command: "true", ContainerID: "test-container"}).Code
			}
			if code != http.StatusForbidden {
				t.Errorf("lost authority returned %d", code)
			}
			var expiry, decision string
			if err := db.QueryRow(`SELECT COALESCE(expires_at,'') FROM agent_credentials WHERE agent_id=? AND credential_id=?`, agent, credential).Scan(&expiry); err != nil {
				t.Fatal(err)
			}
			if expiry != "" {
				t.Errorf("lost authority committed lease %q", expiry)
			}
			if err := db.QueryRow(`SELECT decision FROM keeper_requests ORDER BY created_at DESC LIMIT 1`).Scan(&decision); err != nil || decision != "DENY" {
				t.Errorf("lost authority lacks durable refusal: %q %v", decision, err)
			}
			if ctr.lastExecContainerID != "" {
				t.Error("lost authority executed command")
			}
			var allows int
			if err := db.QueryRow(`SELECT COUNT(*) FROM keeper_request_events WHERE state='ALLOW'`).Scan(&allows); err != nil || allows != 0 {
				t.Errorf("uncommitted ALLOW leaked to ledger: %d %v", allows, err)
			}
		})
	}
}
