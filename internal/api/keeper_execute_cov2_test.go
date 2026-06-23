package api

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
)

// keeper_execute_cov2_test.go — remaining HandleExecute branches:
// credential-lookup DB error, env-var derivation when the assignment
// row has a blank env_var_name, the deny fallbacks (no gatekeeper /
// evaluate error), risk clamping, non-fatal audit write failures, the
// deny broadcaster, and the encoded-variant output scrubbing.
// Helpers prefixed covKE2.

type covKE2ErrEvaluator struct{}

func (covKE2ErrEvaluator) Evaluate(_ context.Context, _ gatekeeper.EvalRequest) (keeper.GatekeeperResponse, error) {
	return keeper.GatekeeperResponse{}, errors.New("evaluator exploded")
}

func covKE2Result(t *testing.T, raw []byte) keeper.ExecuteResult {
	t.Helper()
	var res keeper.ExecuteResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	return res
}

func covKE2Body(wsID, crewID, agentID, credID string) keeperExecuteBody {
	return keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "run a deploy-related command",
		Command:           "gh pr list",
		ContainerID:       "test-container",
	}
}

func TestCovKE2_CredLookupDBError_500(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	execOrFatal(t, db, `ALTER TABLE credentials RENAME TO credentials_broken`)
	h := newKeeperHandler(t, db)
	rr := doKeeperExecute(h, covKE2Body(wsID, crewID, agentID, credID))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovKE2_NoGatekeeper_DenyByDefault(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := newKeeperHandler(t, db) // nil gatekeeper
	rr := doKeeperExecute(h, covKE2Body(wsID, crewID, agentID, credID))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	res := covKE2Result(t, rr.Body.Bytes())
	if res.Decision != keeper.DecisionDeny || res.Reason != "Keeper not configured" {
		t.Errorf("result = %+v, want DENY 'Keeper not configured'", res)
	}
}

func TestCovKE2_EvaluateError_DenyFallback(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := newKeeperHandlerWithGK(t, db, covKE2ErrEvaluator{})
	rr := doKeeperExecute(h, covKE2Body(wsID, crewID, agentID, credID))
	res := covKE2Result(t, rr.Body.Bytes())
	if res.Decision != keeper.DecisionDeny || res.RiskScore != 10 {
		t.Errorf("result = %+v, want DENY risk 10", res)
	}
}

func TestCovKE2_RiskScoreClampedBothEnds(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, 1}, {99, 10}} {
		db := setupTestDB(t)
		wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
		gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
			Decision: string(keeper.DecisionDeny), Reason: "r", RiskScore: tc.in,
		}}
		h := newKeeperHandlerWithGK(t, db, gk)
		rr := doKeeperExecute(h, covKE2Body(wsID, crewID, agentID, credID))
		if res := covKE2Result(t, rr.Body.Bytes()); res.RiskScore != tc.want {
			t.Errorf("risk in=%d → %d, want %d", tc.in, res.RiskScore, tc.want)
		}
	}
}

// TestCovKE2_AuditWriteFailures_NonFatal_DenyBroadcasts — both
// keeper_requests writes are blocked; the deny decision still returns
// 200 and reaches the broadcaster.
func TestCovKE2_AuditWriteFailures_NonFatal_DenyBroadcasts(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	execOrFatal(t, db, `CREATE TRIGGER covke2_block_ins BEFORE INSERT ON keeper_requests
		BEGIN SELECT RAISE(ABORT, 'covke2 forced'); END`)
	execOrFatal(t, db, `CREATE TRIGGER covke2_block_upd BEFORE UPDATE ON keeper_requests
		BEGIN SELECT RAISE(ABORT, 'covke2 forced'); END`)
	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionDeny), Reason: "nope", RiskScore: 8,
	}}
	bc := &covKReqBroadcaster{}
	h := newKeeperHandlerWithGK(t, db, gk).WithBroadcaster(bc)
	rr := doKeeperExecute(h, covKE2Body(wsID, crewID, agentID, credID))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if len(bc.events) != 1 || bc.events[0]["decision"] != string(keeper.DecisionDeny) {
		t.Errorf("broadcast events = %v, want one DENY", bc.events)
	}
}

// TestCovKE2_Allow_BlankEnvVar_DerivedAndVariantsScrubbed — the
// assignment's env_var_name is blank so the name is derived from the
// credential name, and every encoded variant of the secret in the
// output is scrubbed. The post-exec audit UPDATE is also blocked to
// prove it is non-fatal.
func TestCovKE2_Allow_BlankEnvVar_DerivedAndVariantsScrubbed(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	execOrFatal(t, db, `UPDATE agent_credentials SET env_var_name = '' WHERE credential_id = ?`, credID)
	execOrFatal(t, db, `CREATE TRIGGER covke2_block_upd2 BEFORE UPDATE ON keeper_requests
		BEGIN SELECT RAISE(ABORT, 'covke2 forced'); END`)

	const secret = "p@ss+word/1"
	variants := []string{
		secret,
		base64.StdEncoding.EncodeToString([]byte(secret)),
		base64.URLEncoding.EncodeToString([]byte(secret)),
		url.QueryEscape(secret),
		hex.EncodeToString([]byte(secret)),
		reverseString(secret),
	}
	rawOutput := "leak attempt: " + strings.Join(variants, " | ")

	gk := &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "fine", RiskScore: 2,
	}}
	ctr := &mockContainerExec{output: rawOutput, exitCode: 0}
	h := newKeeperHandlerWithGK(t, db, gk).
		WithSecrets(&mockSecretGetter{secrets: map[string]string{credID: secret}}).
		WithContainer(ctr)

	rr := doKeeperExecute(h, covKE2Body(wsID, crewID, agentID, credID))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	res := covKE2Result(t, rr.Body.Bytes())
	if res.Decision != keeper.DecisionAllow {
		t.Fatalf("decision = %v, want ALLOW", res.Decision)
	}
	for i, v := range variants {
		if strings.Contains(res.Output, v) {
			t.Errorf("output leaked variant %d (%q): %s", i, v, res.Output)
		}
	}
}
