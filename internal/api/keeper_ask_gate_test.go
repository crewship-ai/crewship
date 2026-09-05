package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// TestJudgeCredentialAsk_NilGatekeeper_Unavailable verifies the KeeperHandler
// side reports "do not gate" when no judge is configured — the caller then
// stages the ask as before.
func TestJudgeCredentialAsk_NilGatekeeper_Unavailable(t *testing.T) {
	db := setupTestDB(t)
	h := newKeeperHandler(t, db) // nil gatekeeper
	_, err := h.JudgeCredentialAsk(context.Background(), CredentialAskInput{
		WorkspaceID: "ws", AgentID: "a", CredentialName: "PG_PASSWORD", Purpose: "read the orders table", SecurityLevel: 2,
	})
	if !errors.Is(err, errAskJudgeUnavailable) {
		t.Fatalf("want errAskJudgeUnavailable, got %v", err)
	}
}

// TestJudgeCredentialAsk_UsesAccessEvaluator verifies the handler runs the
// access evaluator and returns its verdict for an ask.
func TestJudgeCredentialAsk_UsesAccessEvaluator(t *testing.T) {
	db := setupTestDB(t)
	h := newKeeperHandlerWithGK(t, db, &mockEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "ok", RiskScore: 2,
	}})
	resp, err := h.JudgeCredentialAsk(context.Background(), CredentialAskInput{
		WorkspaceID: "ws", AgentID: "a", CredentialName: "PG_PASSWORD", Purpose: "read the orders table", SecurityLevel: 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Decision != string(keeper.DecisionAllow) {
		t.Errorf("decision = %q, want ALLOW", resp.Decision)
	}
}

// stubAskJudge is a canned CredentialAskJudge for CreateEscalation tests.
type stubAskJudge struct {
	resp   keeper.GatekeeperResponse
	err    error
	called bool
	last   CredentialAskInput
}

func (s *stubAskJudge) JudgeCredentialAsk(_ context.Context, in CredentialAskInput) (keeper.GatekeeperResponse, error) {
	s.called = true
	s.last = in
	return s.resp, s.err
}

// TestCreateEscalation_Ask_KeeperDeny_StagesNothing verifies the #2392 gate:
// a DENY from the ask judge stages no credential, records no escalation, and
// returns the reason to the agent.
func TestCreateEscalation_Ask_KeeperDeny_StagesNothing(t *testing.T) {
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	judge := &stubAskJudge{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionDeny), Reason: "no work in the conversation needs this credential", RiskScore: 7,
	}}
	h.SetCredentialAskJudge(judge)

	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "need db pw", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "CREDENTIAL",
		"metadata": `{"name":"PG_PASSWORD","type":"SECRET","security_level":3,"purpose":"read the orders table for the weekly report"}`,
	})

	if !judge.called {
		t.Fatal("ask judge was not consulted")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (denied); body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "DENIED") || !strings.Contains(rr.Body.String(), "no work in the conversation") {
		t.Errorf("denial body missing status/reason: %s", rr.Body.String())
	}
	// Nothing staged, nothing recorded.
	var escN, credN int
	h.db.QueryRow(`SELECT COUNT(*) FROM escalations WHERE workspace_id=?`, wsID).Scan(&escN)
	h.db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE workspace_id=?`, wsID).Scan(&credN)
	if escN != 0 {
		t.Errorf("escalations created on DENY = %d, want 0", escN)
	}
	if credN != 0 {
		t.Errorf("credentials staged on DENY = %d, want 0", credN)
	}
	// The judge saw the declared metadata, never a value (an ask has none).
	if judge.last.CredentialName != "PG_PASSWORD" || judge.last.SecurityLevel != 3 ||
		judge.last.Purpose == "" || judge.last.AgentID != agentID {
		t.Errorf("ask judge input wrong: %+v", judge.last)
	}
}

// TestCreateEscalation_Ask_KeeperEscalate_StagesWithNote verifies an ESCALATE
// stages the credential (as an ALLOW would) but attaches the judge's note so
// the human sees why it was flagged.
func TestCreateEscalation_Ask_KeeperEscalate_StagesWithNote(t *testing.T) {
	ensureEncryptionKey(t)
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	h.SetCredentialAskJudge(&stubAskJudge{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionEscalate), Reason: "L3 request with thin corroboration", RiskScore: 5,
	}})

	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "need db pw", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "CREDENTIAL",
		"metadata": `{"name":"PG_PASSWORD","type":"SECRET","security_level":3,"purpose":"read the orders table for the weekly report"}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var credID, ctxVal *string
	if err := h.db.QueryRow(`SELECT credential_id, context FROM escalations WHERE workspace_id=? AND type='CREDENTIAL'`, wsID).
		Scan(&credID, &ctxVal); err != nil {
		t.Fatalf("load escalation: %v", err)
	}
	if credID == nil || *credID == "" {
		t.Fatal("ESCALATE must still stage the credential")
	}
	if ctxVal == nil || !strings.Contains(*ctxVal, "Keeper flagged this request") {
		t.Errorf("ESCALATE note not attached to context: %v", ctxVal)
	}
	status, _, _, _, _, _ := credRow(t, h, *credID)
	if status != "REQUESTED" {
		t.Errorf("staged credential status = %q, want REQUESTED", status)
	}
}

// TestCreateEscalation_Ask_KeeperAllow_StagesNormally verifies an ALLOW stages
// exactly as the pre-#2392 flow did, with no note.
func TestCreateEscalation_Ask_KeeperAllow_StagesNormally(t *testing.T) {
	ensureEncryptionKey(t)
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	h.SetCredentialAskJudge(&stubAskJudge{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), Reason: "matches the task", RiskScore: 2,
	}})

	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "need db pw", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "CREDENTIAL",
		"metadata": `{"name":"PG_PASSWORD","type":"SECRET","security_level":2,"purpose":"read the orders table for the weekly report"}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var credID, ctxVal *string
	h.db.QueryRow(`SELECT credential_id, context FROM escalations WHERE workspace_id=? AND type='CREDENTIAL'`, wsID).Scan(&credID, &ctxVal)
	if credID == nil || *credID == "" {
		t.Fatal("ALLOW must stage the credential")
	}
	if ctxVal != nil && strings.Contains(*ctxVal, "Keeper flagged") {
		t.Errorf("ALLOW must not attach a flag note, got: %v", *ctxVal)
	}
}

// TestCreateEscalation_Ask_NoJudge_StagesAsBefore verifies backward
// compatibility: with no judge wired, an ask stages and routes to a human
// exactly as it did before #2392.
func TestCreateEscalation_Ask_NoJudge_StagesAsBefore(t *testing.T) {
	ensureEncryptionKey(t)
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	// no SetCredentialAskJudge

	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "need db pw", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "CREDENTIAL",
		"metadata": `{"name":"PG_PASSWORD","type":"SECRET","security_level":2,"purpose":"read the orders table"}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var credID *string
	h.db.QueryRow(`SELECT credential_id FROM escalations WHERE workspace_id=? AND type='CREDENTIAL'`, wsID).Scan(&credID)
	if credID == nil || *credID == "" {
		t.Fatal("with no judge, an ask must stage as before")
	}
}

// TestCreateEscalation_Ask_JudgeError_EscalatesToHuman verifies a judge outage
// is an ESCALATE, not a DENY: the ask still reaches a human, with a note.
func TestCreateEscalation_Ask_JudgeError_EscalatesToHuman(t *testing.T) {
	ensureEncryptionKey(t)
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	h.SetCredentialAskJudge(&stubAskJudge{err: context.DeadlineExceeded})

	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "need db pw", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "CREDENTIAL",
		"metadata": `{"name":"PG_PASSWORD","type":"SECRET","security_level":2,"purpose":"read the orders table"}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (escalate on outage); body=%s", rr.Code, rr.Body.String())
	}
	var credID, ctxVal *string
	h.db.QueryRow(`SELECT credential_id, context FROM escalations WHERE workspace_id=? AND type='CREDENTIAL'`, wsID).Scan(&credID, &ctxVal)
	if credID == nil || *credID == "" {
		t.Fatal("a judge outage must still stage the ask for a human")
	}
	if ctxVal == nil || !strings.Contains(*ctxVal, "could not evaluate this request automatically") {
		t.Errorf("outage note not attached: %v", ctxVal)
	}
}

// TestCreateEscalation_Propose_NotJudged verifies a PROPOSE (value present) is
// NOT ask-judged — it is the human approving a generated secret, not a request
// to grant one.
func TestCreateEscalation_Propose_NotJudged(t *testing.T) {
	ensureEncryptionKey(t)
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	judge := &stubAskJudge{resp: keeper.GatekeeperResponse{Decision: string(keeper.DecisionDeny), Reason: "x"}}
	h.SetCredentialAskJudge(judge)

	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "keep this pw", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "CREDENTIAL",
		"metadata": `{"name":"PG_PASSWORD","type":"SECRET","provider":"NONE","value":"generated-pw-123"}`,
	})
	if judge.called {
		t.Error("a PROPOSE (value present) must not be ask-judged")
	}
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
}
