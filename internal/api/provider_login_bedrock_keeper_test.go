package api

import (
	"context"
	"database/sql"
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
	for _, retag := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-evaluation", true: "during-evaluation"}[retag], func(t *testing.T) {
			db := setupTestDB(t)
			ws, crew, agent, id := seedKeeperFixture(t, db)
			// Invalid ciphertext makes an accidental live-decrypt path fail differently;
			// the panic getter also proves neither cache path is reached.
			if !retag {
				execOrFatal(t, db, `UPDATE credentials SET provider='AWS_BEDROCK',type='PROVIDER_LOGIN',encrypted_value='must-not-be-decrypted' WHERE id=?`, id)
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
