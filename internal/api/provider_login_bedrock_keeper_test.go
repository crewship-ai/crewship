package api

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

type bedrockNoSecretGetter struct{}

func (bedrockNoSecretGetter) Get(string) (string, bool) {
	panic("Bedrock account must never reach Keeper secret retrieval")
}

type bedrockRetagEvaluator struct {
	db *sql.DB
	id string
}

func (e bedrockRetagEvaluator) Evaluate(ctx context.Context, _ gatekeeper.EvalRequest) (keeper.GatekeeperResponse, error) {
	_, err := e.db.ExecContext(ctx, `UPDATE credentials SET provider='AWS_BEDROCK' WHERE id=?`, e.id)
	return keeper.GatekeeperResponse{Decision: string(keeper.DecisionAllow), Reason: "allowed", RiskScore: 1}, err
}

func TestBedrockKeeperExecuteCannotExportModelAccount(t *testing.T) {
	for _, scenario := range []string{"before-evaluation", "during-evaluation", "other-model-provider"} {
		t.Run(scenario, func(t *testing.T) {
			retag := scenario == "during-evaluation"
			db := setupTestDB(t)
			ws, crew, agent, id := seedKeeperFixture(t, db)
			// Invalid ciphertext makes an accidental live-decrypt path fail differently;
			// the panic getter also proves neither cache path is reached.
			if !retag {
				execOrFatal(t, db, `UPDATE credentials SET provider='AWS_BEDROCK',type='PROVIDER_LOGIN',encrypted_value='must-not-be-decrypted' WHERE id=?`, id)
			}
			if scenario == "other-model-provider" {
				execOrFatal(t, db, `UPDATE credentials SET provider='ANTHROPIC' WHERE id=?`, id)
				execOrFatal(t, db, `UPDATE agents SET llm_provider='AWS_BEDROCK' WHERE id=?`, agent)
			}
			ctr := &mockContainerExec{output: "unexpected", exitCode: 0, execID: "unexpected"}
			var eval gatekeeper.Evaluator = &mockEvaluator{resp: keeper.GatekeeperResponse{Decision: string(keeper.DecisionAllow), Reason: "allow", RiskScore: 1}}
			if retag {
				eval = bedrockRetagEvaluator{db: db, id: id}
			}
			h := newKeeperHandlerWithGK(t, db, eval).WithSecrets(bedrockNoSecretGetter{}).WithContainer(ctr)
			w := doKeeperExecute(h, keeperExecuteBody{RequestingAgentID: agent, RequestingCrewID: crew, WorkspaceID: ws, CredentialID: id, Intent: "read repository state", Command: "git status", EnvVar: "AWS_BEARER_TOKEN_BEDROCK", ContainerID: "test-container"})
			if w.Code != 404 || ctr.lastExecContainerID != "" || len(ctr.lastExecEnv) > 0 {
				t.Fatalf("Bedrock account escaped through Keeper: status=%d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestBedrockAgentCanUseSeparateAWSInfrastructureThroughKeeper(t *testing.T) {
	db := setupTestDB(t)
	ws, crew, agent, id := seedKeeperFixture(t, db)
	execOrFatal(t, db, `UPDATE agents SET llm_provider='AWS_BEDROCK' WHERE id=?`, agent)
	execOrFatal(t, db, `UPDATE credentials SET provider='AWS' WHERE id=?`, id)
	ctr := &mockContainerExec{output: "infrastructure action completed", exitCode: 0, execID: "aws-tool"}
	evaluator := &mockEvaluator{resp: keeper.GatekeeperResponse{Decision: string(keeper.DecisionAllow), Reason: "approved infrastructure action", RiskScore: 1}}
	h := newKeeperHandlerWithGK(t, db, evaluator).WithSecrets(&mockSecretGetter{secrets: map[string]string{id: "infrastructure-only-secret"}}).WithContainer(ctr)
	w := doKeeperExecute(h, keeperExecuteBody{RequestingAgentID: agent, RequestingCrewID: crew, WorkspaceID: ws, CredentialID: id, Intent: "read infrastructure identity", Command: "aws sts get-caller-identity", EnvVar: "AWS_ACCESS_KEY_ID", ContainerID: "test-container"})
	if w.Code != 200 || !strings.Contains(strings.Join(ctr.lastExecEnv, "\n"), "AWS_ACCESS_KEY_ID=infrastructure-only-secret") {
		t.Fatalf("separate infrastructure credential withheld: %d %s env=%v", w.Code, w.Body.String(), ctr.lastExecEnv)
	}
	if strings.Contains(w.Body.String(), "infrastructure-only-secret") {
		t.Fatal("tool result disclosed credential")
	}
}
