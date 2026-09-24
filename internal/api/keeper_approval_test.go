package api

// keeper_approval_test.go — #2574.
//
// "An L4 credential can never execute through Keeper": the judge's ALLOW was
// floored to ESCALATE, a human resolved the escalation, and nothing changed
// for the agent — the sidecar had no route to learn the outcome and a retry
// was re-judged, re-floored, re-escalated. Every test below drives the REAL
// gatekeeper (a canned llm.Provider behind gatekeeper.New), because the tier
// floor that produces the ESCALATE lives inside the real evaluator — a
// mocked gatekeeper.Evaluator would skip it and prove nothing about the L4
// path.
//
// The properties, in the order the issue asks for them:
//
//   - reproduction: an approved escalation retried WITHOUT the approval
//     re-escalates (this is the dead end, pinned so the fix's value is
//     legible)
//   - acceptance: an approved L4 execute with the approval actually RUNS;
//     an unapproved one never does
//   - single use: the second presentation is refused, and the retry row an
//     approval creates is itself not spendable (no self-minting chain)
//   - binding: a different agent, credential, command or request type
//     cannot spend somebody else's approval
//   - denial: a DENY resolution is not consumable
//   - TTL: an approval older than keeperApprovalTTL is refused
//   - concurrency: N simultaneous presentations, exactly one wins
//   - durability: consumption survives a handler rebuild over the same DB
//     (the restart model — the marker lives in SQLite, not memory)

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/llm"
)

// countingCannedProvider is an llm.Provider that returns one fixed verdict
// and counts how many times it was asked — the number that must be ZERO on
// an approval-consumed retry (no re-judging is the whole point).
type countingCannedProvider struct {
	content string
	calls   atomic.Int64
}

func (c *countingCannedProvider) Complete(_ context.Context, _ llm.Request) (*llm.Response, error) {
	c.calls.Add(1)
	return &llm.Response{Content: c.content}, nil
}
func (c *countingCannedProvider) Stream(ctx context.Context, r llm.Request, h func(llm.StreamEvent) error) (*llm.Response, error) {
	resp, _ := c.Complete(ctx, r)
	_ = h(llm.StreamEvent{Type: "done", Response: resp})
	return resp, nil
}
func (c *countingCannedProvider) Name() string { return "counting-canned" }

// seedL4Fixture is seedKeeperFixture with a security_level=4 credential: the
// tier whose policy escalates every judge ALLOW.
func seedL4Fixture(t *testing.T, db *sql.DB) (wsID, crewID, agentID, credID string) {
	t.Helper()
	setTestEncryptionKey(t)

	userID := seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)

	crewID = "l4-crew-" + wsID
	execOrFatal(t, db,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'L4 Crew', 'l4-crew')`, crewID, wsID)

	agentID = "l4-agent-" + wsID
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'L4Bot', 'l4-bot')`,
		agentID, crewID, wsID)

	credID = "l4-cred-" + wsID
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, status, created_by)
		 VALUES (?, ?, 'PROD_DB_ADMIN', 'SECRET', 4, 'v1:aW52YWxpZA==', 'ACTIVE', ?)`,
		credID, wsID, userID)

	execOrFatal(t, db,
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		 VALUES (?, ?, ?, 'PROD_DB_ADMIN', 0)`,
		"l4-ac-"+wsID, agentID, credID)
	return
}

// resolveAllow resolves reqID as decision through HandleResolve, as the
// operator surface does.
func resolveDecision(t *testing.T, h *KeeperHandler, wsID, reqID, decision, userID string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.HandleResolve(rr, resolveReq(t, wsID, "ADMIN", userID, reqID,
		map[string]any{"decision": decision, "reason": "change window is declared"}))
	return rr
}

// escalateL4Execute drives one /keeper/execute against the real gatekeeper
// (which floors the judge's ALLOW to ESCALATE at L4) and returns the parsed
// result plus the escalated request id.
func escalateL4Execute(t *testing.T, h *KeeperHandler, wsID, crewID, agentID, credID, command string) (keeper.ExecuteResult, *httptest.ResponseRecorder) {
	t.Helper()
	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "rotate the production database certificates tonight",
		Command:           command,
		ContainerID:       "test-container",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("execute returned %d: %s", w.Code, w.Body.String())
	}
	var res keeper.ExecuteResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return res, w
}

// newL4Handler builds a KeeperHandler with the REAL gatekeeper over a canned
// judge that says ALLOW — so the only thing that can turn it into ESCALATE
// is the L4 tier floor, which is the behaviour under test.
func newL4Handler(t *testing.T, db *sql.DB, judge *countingCannedProvider, spy *spyContainerExec) (*KeeperHandler, *countingCannedProvider) {
	t.Helper()
	if judge == nil {
		judge = &countingCannedProvider{content: `{"decision":"ALLOW","reason":"task context matches","risk":3}`}
	}
	logger := newTestLogger()
	h := NewKeeperHandler(db, "internal-token", gatekeeper.New(judge, "test-judge", logger), logger)
	if spy != nil {
		h = h.WithSecrets(&mockSecretGetter{secrets: map[string]string{}}).
			WithContainer(spy)
	}
	return h, judge
}

// TestL4Escalation_ApprovalIsConsumedAndTheCommandRuns is THE acceptance
// test for #2574: a human-approved L4 escalation, presented on the retry,
// executes exactly once — and the unapproved path never executes at all.
func TestL4Escalation_ApprovalIsConsumedAndTheCommandRuns(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedL4Fixture(t, db)

	execCalled := false
	spy := &spyContainerExec{
		mockContainerExec: &mockContainerExec{output: "ok", exitCode: 0, execID: "exec-1"},
		execCalled:        &execCalled,
	}
	// The secret reaches the inject path through the mock secrets store.
	h, judge := newL4Handler(t, db, nil, spy)
	h = h.WithSecrets(&mockSecretGetter{secrets: map[string]string{credID: "prod-admin-secret"}})

	const cmd = "pg_dump --host prod-db --all"
	first, _ := escalateL4Execute(t, h, wsID, crewID, agentID, credID, cmd)
	if first.Decision != keeper.DecisionEscalate {
		t.Fatalf("the judge said ALLOW on an L4 credential and the tier floor did not escalate: %s", first.Decision)
	}
	if execCalled {
		t.Fatal("an ESCALATE executed the command; escalation must not execute")
	}
	judgeCallsAfterEscalation := judge.calls.Load()

	// A person rules on it.
	rr := resolveDecision(t, h, wsID, first.RequestID, "ALLOW", "user-approver")
	if rr.Code != http.StatusOK {
		t.Fatalf("resolve returned %d: %s", rr.Code, rr.Body.String())
	}

	// The retry presents the approval.
	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "rotate the production database certificates tonight",
		Command:           cmd,
		ContainerID:       "test-container",
		ApprovalRequestID: first.RequestID,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("approved retry returned %d: %s — the approval is still a no-op (#2574)", w.Code, w.Body.String())
	}
	var retry keeper.ExecuteResult
	if err := json.Unmarshal(w.Body.Bytes(), &retry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if retry.Decision != keeper.DecisionAllow {
		t.Fatalf("approved retry decision = %s, want ALLOW — a human approved this exact command", retry.Decision)
	}
	if !execCalled {
		t.Fatal("the approved L4 execute never ran the command — the approval is still a no-op")
	}
	if judge.calls.Load() != judgeCallsAfterEscalation {
		t.Fatalf("the judge was consulted again on an approval-consumed retry (%d → %d) — re-judging re-escalates, which is the defect",
			judgeCallsAfterEscalation, judge.calls.Load())
	}

	// Single use: presenting it again is refused and runs nothing.
	again := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "rotate the production database certificates tonight",
		Command:           cmd,
		ContainerID:       "test-container",
		ApprovalRequestID: first.RequestID,
	})
	if again.Code != http.StatusConflict {
		t.Fatalf("second presentation returned %d, want 409 — single-use is the property the design rests on: %s",
			again.Code, again.Body.String())
	}
	if !execCalled {
		t.Fatal("sanity: exec must have run once by now")
	}
}

// TestL4Escalation_ApprovedButRetriedWithoutApproval_ReEscalates is the
// reproduction: before #2574 the ONLY retry an agent could make was judged
// afresh, so an approval changed nothing. Pinned so the consumed-approval
// path above is legible as the fix rather than an optimisation.
func TestL4Escalation_ApprovedButRetriedWithoutApproval_ReEscalates(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedL4Fixture(t, db)

	execCalled := false
	spy := &spyContainerExec{
		mockContainerExec: &mockContainerExec{output: "ok"},
		execCalled:        &execCalled,
	}
	h, _ := newL4Handler(t, db, nil, spy)

	const cmd = "pg_dump --host prod-db --all"
	first, _ := escalateL4Execute(t, h, wsID, crewID, agentID, credID, cmd)
	if first.Decision != keeper.DecisionEscalate {
		t.Fatalf("want ESCALATE, got %s", first.Decision)
	}
	if rr := resolveDecision(t, h, wsID, first.RequestID, "ALLOW", "user-approver"); rr.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", rr.Code, rr.Body.String())
	}

	// No approval presented: the only possible outcome is a fresh escalation.
	// A fresh handler models the retry arriving after the dedup debounce
	// window (5s) — the in-memory claim is what the window is made of.
	hLater, _ := newL4Handler(t, db, nil, spy)
	second, _ := escalateL4Execute(t, hLater, wsID, crewID, agentID, credID, cmd)
	if second.Decision != keeper.DecisionEscalate {
		t.Fatalf("retry without the approval decision = %s, want ESCALATE — without presenting it, the human's ruling cannot apply",
			second.Decision)
	}
	if second.RequestID == first.RequestID {
		t.Fatal("the retry reused the escalated request's row; it must be its own request")
	}
	if execCalled {
		t.Fatal("nothing should have executed")
	}
}

// TestL4Escalation_DeniedApprovalIsNotConsumable — a DENY resolution is a
// refusal, and presenting it must neither execute nor burn anything.
func TestL4Escalation_DeniedApprovalIsNotConsumable(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedL4Fixture(t, db)

	execCalled := false
	spy := &spyContainerExec{
		mockContainerExec: &mockContainerExec{output: "ok"},
		execCalled:        &execCalled,
	}
	h, _ := newL4Handler(t, db, nil, spy)

	const cmd = "pg_dump --host prod-db --all"
	first, _ := escalateL4Execute(t, h, wsID, crewID, agentID, credID, cmd)
	if rr := resolveDecision(t, h, wsID, first.RequestID, "DENY", "user-approver"); rr.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", rr.Code, rr.Body.String())
	}

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "rotate the production database certificates tonight",
		Command: cmd, ContainerID: "test-container",
		ApprovalRequestID: first.RequestID,
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("a DENIED escalation was presented and got %d, want 403: %s", w.Code, w.Body.String())
	}
	if execCalled {
		t.Fatal("a denied approval executed the command")
	}
}

// TestL4Escalation_UnresolvedEscalationIsNotConsumable — still ESCALATE
// means still waiting for a person; the error must say so rather than read
// like a spent approval.
func TestL4Escalation_UnresolvedEscalationIsNotConsumable(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedL4Fixture(t, db)

	h, _ := newL4Handler(t, db, nil, nil)
	const cmd = "pg_dump --host prod-db --all"
	first, _ := escalateL4Execute(t, h, wsID, crewID, agentID, credID, cmd)

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "rotate the production database certificates tonight",
		Command: cmd, ContainerID: "test-container",
		ApprovalRequestID: first.RequestID,
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 for an unresolved escalation: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "awaiting a human decision") {
		t.Errorf("the message should tell the agent to keep waiting, got: %s", w.Body.String())
	}
}

// TestL4Escalation_ExpiredApprovalIsRefused — the window is decided_at +
// keeperApprovalTTL; older is dead, with a code the agent can distinguish
// from a binding failure.
func TestL4Escalation_ExpiredApprovalIsRefused(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedL4Fixture(t, db)

	h, _ := newL4Handler(t, db, nil, nil)
	const cmd = "pg_dump --host prod-db --all"
	first, _ := escalateL4Execute(t, h, wsID, crewID, agentID, credID, cmd)
	if rr := resolveDecision(t, h, wsID, first.RequestID, "ALLOW", "user-approver"); rr.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", rr.Code, rr.Body.String())
	}

	// Age the approval past the TTL.
	stale := time.Now().UTC().Add(-keeperApprovalTTL - time.Minute).Format(time.RFC3339)
	execOrFatal(t, db, `UPDATE keeper_requests SET decided_at = ? WHERE id = ?`, stale, first.RequestID)

	w := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "rotate the production database certificates tonight",
		Command: cmd, ContainerID: "test-container",
		ApprovalRequestID: first.RequestID,
	})
	if w.Code != http.StatusGone {
		t.Fatalf("an expired approval got %d, want 410: %s", w.Code, w.Body.String())
	}
}

// TestL4Escalation_ApprovalBindsToAgentCredentialAndCommand — the four
// mismatches that must each refuse: another agent, another credential,
// another command, and an approval from the other request type (an approved
// access escalation must not pay for a command that was never judged).
func TestL4Escalation_ApprovalBindsToAgentCredentialAndCommand(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedL4Fixture(t, db)

	// A peer agent holding a second credential — both must exist for the
	// retries to reach the consumption check rather than 404 earlier.
	peerID := "l4-peer-" + wsID
	execOrFatal(t, db,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'L4Peer', 'l4-peer')`,
		peerID, crewID, wsID)
	peerCred := "l4-peer-cred-" + wsID
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, status, created_by)
		 VALUES (?, ?, 'PEER_TOKEN', 'SECRET', 4, 'v1:aW52YWxpZA==', 'ACTIVE',
		   (SELECT created_by FROM credentials WHERE id = ?))`, peerCred, wsID, credID)
	execOrFatal(t, db,
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		 VALUES (?, ?, ?, 'PEER_TOKEN', 0)`, "l4-peer-ac-"+wsID, peerID, peerCred)
	// Both credentials are shared by both agents — the crew-container reality.
	// Without this the mismatch retries die at the assignment JOIN (404) and
	// never reach the binding check this test exists for.
	execOrFatal(t, db,
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		 VALUES (?, ?, ?, 'PROD_DB_ADMIN', 1)`, "l4-peer-ac2-"+wsID, peerID, credID)
	execOrFatal(t, db,
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		 VALUES (?, ?, ?, 'PEER_TOKEN', 1)`, "l4-ac2-"+wsID, agentID, peerCred)

	execCalled := false
	spy := &spyContainerExec{
		mockContainerExec: &mockContainerExec{output: "ok"},
		execCalled:        &execCalled,
	}
	h, _ := newL4Handler(t, db, nil, spy)

	const cmd = "pg_dump --host prod-db --all"
	esc, _ := escalateL4Execute(t, h, wsID, crewID, agentID, credID, cmd)
	if rr := resolveDecision(t, h, wsID, esc.RequestID, "ALLOW", "user-approver"); rr.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", rr.Code, rr.Body.String())
	}

	cases := []struct {
		name string
		body keeperExecuteBody
	}{
		{"peer agent", keeperExecuteBody{
			RequestingAgentID: peerID, RequestingCrewID: crewID, WorkspaceID: wsID,
			CredentialID: credID, Intent: "rotate the production database certificates tonight",
			Command: cmd, ContainerID: "test-container", ApprovalRequestID: esc.RequestID}},
		{"different credential", keeperExecuteBody{
			RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
			CredentialID: peerCred, Intent: "rotate the production database certificates tonight",
			Command: cmd, ContainerID: "test-container", ApprovalRequestID: esc.RequestID}},
		{"different command", keeperExecuteBody{
			RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
			CredentialID: credID, Intent: "rotate the production database certificates tonight",
			Command: "pg_restore --host prod-db", ContainerID: "test-container", ApprovalRequestID: esc.RequestID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doKeeperExecute(h, tc.body)
			if w.Code != http.StatusForbidden {
				t.Fatalf("got %d, want 403 — the approval does not bind to this request: %s", w.Code, w.Body.String())
			}
		})
	}
	if execCalled {
		t.Fatal("a mismatched approval executed something")
	}

	// An approved ACCESS escalation cannot pay for an execute: the command
	// was never judged.
	aw := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "rotate the production database certificates tonight",
	})
	var accessRes keeper.RequestResult
	if err := json.Unmarshal(aw.Body.Bytes(), &accessRes); err != nil {
		t.Fatalf("access unmarshal: %v", err)
	}
	if accessRes.Decision != keeper.DecisionEscalate {
		t.Fatalf("L4 access decision = %s, want ESCALATE", accessRes.Decision)
	}
	if rr := resolveDecision(t, h, wsID, accessRes.RequestID, "ALLOW", "user-approver"); rr.Code != http.StatusOK {
		t.Fatalf("access resolve: %d %s", rr.Code, rr.Body.String())
	}
	xw := doKeeperExecute(h, keeperExecuteBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "rotate the production database certificates tonight",
		Command: cmd, ContainerID: "test-container",
		ApprovalRequestID: accessRes.RequestID,
	})
	if xw.Code != http.StatusForbidden {
		t.Fatalf("an approved access escalation paid for an execute: %d %s", xw.Code, xw.Body.String())
	}
}

// TestL4Escalation_JudgeAllowIsNotAnApproval — a machine ALLOW (possible at
// L3, or L4 on an instance with escalate-from=never) must not be spendable
// as an approval, and neither must the row an approval-consumed retry
// creates: without the resolved_by_user_id / born-consumed markers, every
// ALLOW on the table would mint self-renewing approvals.
func TestL4Escalation_JudgeAllowIsNotAnApproval(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedL4Fixture(t, db)

	// L3 credential: a judge ALLOW is terminal ALLOW, no escalation.
	l3Cred := "l3-cred-" + wsID
	execOrFatal(t, db,
		`INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, status, created_by)
		 VALUES (?, ?, 'STAGING_DB', 'SECRET', 3, 'v1:aW52YWxpZA==', 'ACTIVE',
		   (SELECT created_by FROM credentials WHERE id = ?))`, l3Cred, wsID, credID)
	execOrFatal(t, db,
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority)
		 VALUES (?, ?, ?, 'STAGING_DB', 0)`, "l3-ac-"+wsID, agentID, l3Cred)

	h, _ := newL4Handler(t, db, nil, nil)

	w := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: l3Cred, Intent: "run the staging migration for the orders service tonight",
	})
	var res keeper.RequestResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Decision != keeper.DecisionAllow {
		t.Fatalf("L3 judge-ALLOW decision = %s, want ALLOW (the premise of the test)", res.Decision)
	}

	// Presenting the machine's own ALLOW as an approval: refused.
	aw := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: l3Cred, Intent: "run the staging migration for the orders service tonight",
		ApprovalRequestID: res.RequestID,
	})
	if aw.Code != http.StatusForbidden {
		t.Fatalf("a judge ALLOW was spent as an approval: %d %s", aw.Code, aw.Body.String())
	}
}

// TestL4Escalation_ConcurrentPresentations_ConsumeExactlyOnce — the spend
// is a conditional UPDATE, so however many identical retries race, exactly
// one wins. The access path (no in-process dedup) is where the race is
// observable end-to-end.
func TestL4Escalation_ConcurrentPresentations_ConsumeExactlyOnce(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedL4Fixture(t, db)

	h, _ := newL4Handler(t, db, nil, nil)

	aw := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "rotate the production database certificates tonight",
	})
	var accessRes keeper.RequestResult
	if err := json.Unmarshal(aw.Body.Bytes(), &accessRes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if accessRes.Decision != keeper.DecisionEscalate {
		t.Fatalf("L4 access decision = %s, want ESCALATE", accessRes.Decision)
	}
	if rr := resolveDecision(t, h, wsID, accessRes.RequestID, "ALLOW", "user-approver"); rr.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", rr.Code, rr.Body.String())
	}

	const racers = 8
	start := make(chan struct{})
	codes := make([]int, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rr := httptest.NewRecorder()
			<-start
			raw, merr := json.Marshal(keeperRequestBody{
				RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
				CredentialID: credID, Intent: "rotate the production database certificates tonight",
				ApprovalRequestID: accessRes.RequestID,
			})
			if merr != nil {
				t.Errorf("marshal: %v", merr)
				return
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/request", strings.NewReader(string(raw)))
			req.Header.Set("Content-Type", "application/json")
			h.HandleRequest(rr, req)
			codes[i] = rr.Code
		}(i)
	}
	close(start)
	wg.Wait()

	ok := 0
	for i, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
		default:
			t.Errorf("racer %d got %d, want 200 or 409", i, c)
		}
	}
	if ok != 1 {
		t.Fatalf("%d of %d concurrent presentations were honoured, want exactly 1 — the approval is not single-use",
			ok, racers)
	}
}

// TestL4Escalation_ConsumptionSurvivesAHandlerRebuild — consumption state
// lives in keeper_requests, not memory. A rebuilt handler over the same DB
// (what a restart is, from the approval's point of view) still honours a
// fresh approval once and refuses the one already spent.
func TestL4Escalation_ConsumptionSurvivesAHandlerRebuild(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedL4Fixture(t, db)

	execCalled := false
	spy := &spyContainerExec{
		mockContainerExec: &mockContainerExec{output: "ok"},
		execCalled:        &execCalled,
	}
	h1, _ := newL4Handler(t, db, nil, spy)

	const cmd = "pg_dump --host prod-db --all"
	esc, _ := escalateL4Execute(t, h1, wsID, crewID, agentID, credID, cmd)
	if rr := resolveDecision(t, h1, wsID, esc.RequestID, "ALLOW", "user-approver"); rr.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", rr.Code, rr.Body.String())
	}

	// The restart: a fresh handler (fresh in-memory dedup, same DB).
	h2, _ := newL4Handler(t, db, nil, spy)
	h2 = h2.WithSecrets(&mockSecretGetter{secrets: map[string]string{credID: "prod-admin-secret"}})
	w := doKeeperExecute(h2, keeperExecuteBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "rotate the production database certificates tonight",
		Command: cmd, ContainerID: "test-container",
		ApprovalRequestID: esc.RequestID,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("after a restart the approval was not honoured: %d %s", w.Code, w.Body.String())
	}
	var res keeper.ExecuteResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Decision != keeper.DecisionAllow || !execCalled {
		t.Fatalf("post-restart decision = %s, exec called = %v; want ALLOW and true", res.Decision, execCalled)
	}

	// And the SPENT one is still spent after another rebuild.
	h3, _ := newL4Handler(t, db, nil, spy)
	again := doKeeperExecute(h3, keeperExecuteBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "rotate the production database certificates tonight",
		Command: cmd, ContainerID: "test-container",
		ApprovalRequestID: esc.RequestID,
	})
	if again.Code != http.StatusConflict {
		t.Fatalf("after a restart the spent approval was honoured again: %d %s", again.Code, again.Body.String())
	}
}

// TestL4Escalation_ApprovedAccessRetryIsAllowed — the access path consumes
// the same way: an approved L4 read returns ALLOW to the agent (which is
// what releases the credential via the lease/credstore machinery).
func TestL4Escalation_ApprovedAccessRetryIsAllowed(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedL4Fixture(t, db)

	h, judge := newL4Handler(t, db, nil, nil)
	aw := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "rotate the production database certificates tonight",
	})
	var accessRes keeper.RequestResult
	if err := json.Unmarshal(aw.Body.Bytes(), &accessRes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if accessRes.Decision != keeper.DecisionEscalate {
		t.Fatalf("L4 access decision = %s, want ESCALATE", accessRes.Decision)
	}
	if rr := resolveDecision(t, h, wsID, accessRes.RequestID, "ALLOW", "user-approver"); rr.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", rr.Code, rr.Body.String())
	}
	judgeCalls := judge.calls.Load()

	retry := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID, RequestingCrewID: crewID, WorkspaceID: wsID,
		CredentialID: credID, Intent: "rotate the production database certificates tonight",
		ApprovalRequestID: accessRes.RequestID,
	})
	if retry.Code != http.StatusOK {
		t.Fatalf("approved access retry: %d %s", retry.Code, retry.Body.String())
	}
	var res keeper.RequestResult
	if err := json.Unmarshal(retry.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res.Decision != keeper.DecisionAllow {
		t.Fatalf("approved access retry decision = %s, want ALLOW — the read the human approved never happens", res.Decision)
	}
	if judge.calls.Load() != judgeCalls {
		t.Fatal("the judge ran again on an approval-consumed access retry")
	}
}

// TestKeeperGetRequest_AgentScopedByQuery — the poll route the sidecar
// exposes (#2574) pins the ACTING agent; a row belonging to somebody else
// must be a 404, not a read.
func TestKeeperGetRequest_AgentScopedByQuery(t *testing.T) {
	db := setupTestDB(t)
	_, crewID, agentID, credID := seedL4Fixture(t, db)

	execOrFatal(t, db, `
		INSERT INTO keeper_requests (id, request_type, requesting_agent_id, requesting_crew_id, credential_id, intent, decision)
		VALUES ('kr-poll', 'access', ?, ?, ?, 'rotate the certs', 'ESCALATE')`,
		agentID, crewID, credID)

	h := newKeeperHandler(t, db)

	get := func(query string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/keeper/request/kr-poll"+query, nil)
		req.SetPathValue("requestId", "kr-poll")
		rr := httptest.NewRecorder()
		h.GetRequest(rr, req)
		return rr.Code
	}

	if get("") != http.StatusOK {
		t.Fatal("an internal caller without an agent_id pin can still read (master-token surface)")
	}
	if get("?agent_id="+agentID) != http.StatusOK {
		t.Fatal("the owning agent cannot poll its own request")
	}
	if get("?agent_id=somebody-else") != http.StatusNotFound {
		t.Fatal("a sibling's poll leaked another agent's request — 404, not 200")
	}
}
