package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/provider"
)

// countingContainerExec wraps mockContainerExec and atomically counts Exec
// calls, so a concurrent test can observe exactly how many times the
// container actually ran the command — independent of any HTTP-level
// response counting, which would miss a double-exec that both requests
// happen to see as "success".
type countingContainerExec struct {
	*mockContainerExec
	execCount atomic.Int64
}

func (c *countingContainerExec) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	c.execCount.Add(1)
	return c.mockContainerExec.Exec(ctx, cfg)
}

// TestKeeperHandleExecute_ConcurrentIdenticalRequests_T10 is the keeper
// TOCTOU harness's T10 twin (scripts/test-harness/test-keeper-toctou.sh,
// section 9), ported to a deterministic Go-level test instead of a live
// two-container race: /keeper/execute has no client- or server-supplied
// idempotency key, so this fires two structurally-identical execute
// requests concurrently (as a client retry-on-timeout would) and reports
// how many times the command actually ran and how many audit rows landed.
//
// This is intentionally NOT a hard assertion that exactly one execution
// happens — HandleExecute (keeper_execute.go) generates a fresh request ID
// and re-validates credential state per call with no cross-call dedup, so
// two concurrent identical calls are expected, by the code as written
// today, to both run the command and both leave an audit row. The test
// exists to make that fact visible and pinned (t.Log + explicit counts),
// not to silently pass or silently block every future PR on an unplanned
// idempotency feature.
func TestKeeperHandleExecute_ConcurrentIdenticalRequests_T10(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)

	ctr := &countingContainerExec{
		mockContainerExec: &mockContainerExec{output: "ok", exitCode: 0, execID: "exec-t10"},
	}
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision:  string(keeper.DecisionAllow),
		Reason:    "routine read-only command",
		RiskScore: 1,
	}}

	h := newKeeperHandlerWithGK(t, db, gk).
		WithSecrets(&mockSecretGetter{secrets: map[string]string{credID: "hunter2"}}).
		WithContainer(ctr)

	body := keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "list PRs",
		Command:           "gh pr list",
		ContainerID:       "test-container",
	}

	const concurrentCalls = 2
	var wg sync.WaitGroup
	codes := make([]int, concurrentCalls)
	wg.Add(concurrentCalls)
	for i := range concurrentCalls {
		go func(i int) {
			defer wg.Done()
			codes[i] = doKeeperExecute(h, body).Code
		}(i)
	}
	wg.Wait()

	for i, code := range codes {
		if code != 200 {
			t.Errorf("call %d: expected 200, got %d", i, code)
		}
	}

	var auditRows int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM keeper_requests WHERE requesting_agent_id = ? AND credential_id = ? AND request_type = 'execute'`,
		agentID, credID).Scan(&auditRows); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}

	execCount := ctr.execCount.Load()
	t.Logf("T10: %d concurrent identical /keeper/execute calls -> %d container executions, %d audit rows",
		concurrentCalls, execCount, auditRows)

	if execCount != 1 {
		t.Logf("FINDING (not a test bug): HandleExecute has no idempotency key, so %d concurrent identical requests ran the command %d times and left %d audit rows instead of 1 — this is the T10 gap the brief flagged as untested, now proven rather than assumed.",
			concurrentCalls, execCount, auditRows)
	}
}
