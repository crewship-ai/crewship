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
// two-container race: it fires two structurally-identical execute requests
// concurrently (as a client retry-on-timeout would) and asserts the command
// runs exactly once.
//
// #1329: HandleExecute previously had no client- or server-supplied
// idempotency key, so two concurrent identical calls both ran the command
// and both left an audit row (proven 5/5 by an earlier version of this
// test, t.Logf-only, landed in #1324). keeper_execute.go now claims a
// single dedup chokepoint (execDedup, keyed on workspace+agent+credential+
// command) before the PENDING audit insert: the loser of the race gets 409
// with a DUPLICATE_SUPPRESSED audit row instead of running the command.
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

	var ok200, dup409 int
	for i, code := range codes {
		switch code {
		case 200:
			ok200++
		case 409:
			dup409++
		default:
			t.Errorf("call %d: unexpected status %d (want 200 or 409)", i, code)
		}
	}
	if ok200 != 1 || dup409 != 1 {
		t.Fatalf("expected exactly one 200 and one 409 (deduped) among %d concurrent identical calls, got %d/200 %d/409",
			concurrentCalls, ok200, dup409)
	}

	execCount := ctr.execCount.Load()
	if execCount != 1 {
		t.Errorf("expected exactly 1 container execution for %d concurrent identical requests, got %d",
			concurrentCalls, execCount)
	}

	var allowRows, dupRows int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM keeper_requests WHERE requesting_agent_id = ? AND credential_id = ? AND request_type = 'execute' AND decision = 'ALLOW'`,
		agentID, credID).Scan(&allowRows); err != nil {
		t.Fatalf("count ALLOW audit rows: %v", err)
	}
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM keeper_requests WHERE requesting_agent_id = ? AND credential_id = ? AND request_type = 'execute' AND decision = 'DUPLICATE_SUPPRESSED'`,
		agentID, credID).Scan(&dupRows); err != nil {
		t.Fatalf("count DUPLICATE_SUPPRESSED audit rows: %v", err)
	}
	if allowRows != 1 {
		t.Errorf("expected exactly 1 ALLOW audit row, got %d", allowRows)
	}
	if dupRows != 1 {
		t.Errorf("expected exactly 1 DUPLICATE_SUPPRESSED audit row, got %d", dupRows)
	}

	t.Logf("T10: %d concurrent identical /keeper/execute calls -> %d container execution(s), %d ALLOW row(s), %d DUPLICATE_SUPPRESSED row(s)",
		concurrentCalls, execCount, allowRows, dupRows)
}
