package api

// Two clocks, not one.
//
//	deadline_at         — the AGENT's wait window. When it passes the long poll
//	                      ends and the agent continues with an explicit warning.
//	                      The question stays PENDING and answerable.
//	answer_deadline_at  — the HUMAN's answerability window. When it passes the
//	                      row goes EXPIRED and a staged credential is disposed of.
//
// The regression these tests pin: the branch wrote ONE deadline (300 s) and
// used it for both, so an operator who walked away to fetch an API key came
// back to a 409 and a secret nobody could ever activate or reject.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ─── rig ──────────────────────────────────────────────────────────────────

// seedTwoClockEscalation inserts one PENDING escalation with both clocks set
// explicitly, so a case can put the agent's window in the past and the human's
// in the future — the exact shape the regression got wrong.
func seedTwoClockEscalation(t *testing.T, h *QueryHandler, id, wsID, crewID, agentID, escType string,
	agentDeadline, answerDeadline *time.Time, credentialID interface{}) {
	t.Helper()
	var ad, hd interface{}
	if agentDeadline != nil {
		ad = agentDeadline.UTC().Format(time.RFC3339)
	}
	if answerDeadline != nil {
		hd = answerDeadline.UTC().Format(time.RFC3339)
	}
	execOrFatal(t, h.db, `INSERT INTO escalations
		(id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status,
		 deadline_at, answer_deadline_at, credential_id, created_at)
		VALUES (?, ?, ?, 'tc-chat', ?, 'need the API key', ?, 'PENDING', ?, ?, ?, ?)`,
		id, wsID, crewID, agentID, escType, ad, hd, credentialID,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
}

// escResolve drives PATCH /api/v1/escalations/{id}/resolve as a MANAGER.
func escResolve(h *QueryHandler, userID, wsID, escID, resolution, action string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("PATCH", "/api/v1/escalations/"+escID+"/resolve",
			jsonBody(map[string]string{"resolution": resolution, "action": action})),
		userID, wsID, "MANAGER")
	req.SetPathValue("escalationId", escID)
	rr := httptest.NewRecorder()
	h.ResolveEscalation(rr, req)
	return rr
}

// stageCredentialEscalation raises a real CREDENTIAL escalation carrying a
// proposal, so the staged PENDING_APPROVAL credential is created by the
// production path rather than by hand.
func stageCredentialEscalation(t *testing.T, h *QueryHandler, wsID, crewID, name string) (escID, credID string) {
	t.Helper()
	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "need " + name, "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "CREDENTIAL",
		"metadata": `{"name":"` + name + `","type":"SECRET","provider":"NONE","value":"s3cret-` + name + `"}`, //gitleaks:allow — test fixture
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create credential escalation: status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		EscalationID string `json:"escalation_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	var cid *string
	if err := h.db.QueryRow(`SELECT credential_id FROM escalations WHERE id = ?`, body.EscalationID).Scan(&cid); err != nil {
		t.Fatalf("load credential_id: %v", err)
	}
	if cid == nil || *cid == "" {
		t.Fatalf("escalation %s staged no credential — the fixture cannot test disposal", body.EscalationID)
	}
	return body.EscalationID, *cid
}

// expireByHumanDeadline drags the human clock into the past and runs the
// sweeper, which is what a week of silence looks like in one line.
func expireByHumanDeadline(t *testing.T, h *QueryHandler, escID, wsID string) {
	t.Helper()
	execOrFatal(t, h.db, `UPDATE escalations SET answer_deadline_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), escID)
	if _, err := h.sweepExpiredEscalations(context.Background(), wsID); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if s := escStatus(t, h, escID); s != escalationStatusExpired {
		t.Fatalf("escalation status = %q after its answer deadline passed, want EXPIRED", s)
	}
}

// ─── FINDING 1: the human's clock is not the agent's ──────────────────────

// The operator walks off to fetch the key and comes back after the agent's
// poll gave up. The question was never answered by anyone, so it must still be
// answerable — a 409 here is the system telling a human that a decision they
// are in the middle of making has already been made.
func TestLateHumanAnswerAfterAgentGaveUp(t *testing.T) {
	h, _, userID, wsID, crewID, agentID := escLifecycleRig(t)
	agentGone := time.Now().UTC().Add(-time.Minute)
	humanHasTime := time.Now().UTC().Add(72 * time.Hour)
	seedTwoClockEscalation(t, h, "tc-late", wsID, crewID, agentID, "TEXT", &agentGone, &humanHasTime, nil)

	// The agent's poll ends. It is told to continue, and told why.
	code, body := escWaitCall(t, h, "tc-late", 5*time.Second)
	if code != http.StatusOK {
		t.Fatalf("wait status = %d, want 200; body=%s", code, body)
	}
	var wait map[string]interface{}
	if err := json.Unmarshal([]byte(body), &wait); err != nil {
		t.Fatalf("decode wait %q: %v", body, err)
	}
	if wait["status"] != escalationWireUnanswered {
		t.Errorf("wait status = %v, want %q — the agent gave up, the question did not expire",
			wait["status"], escalationWireUnanswered)
	}
	if w, _ := wait["warning"].(string); w == "" {
		t.Error("the agent was handed no warning — continuing without an answer silently is the original defect")
	}
	if wait["agent_action"] != escalationOutcomeContinuedWithWarning {
		t.Errorf("agent_action = %v, want %q", wait["agent_action"], escalationOutcomeContinuedWithWarning)
	}

	// THE ASSERTION. The row is still a question.
	if s := escStatus(t, h, "tc-late"); s != escalationStatusPending {
		t.Fatalf("row status = %q after the agent's window closed, want PENDING — "+
			"the agent's give-up is not the human's deadline", s)
	}
	// …and the database records that the agent stopped waiting, which is the
	// half of the branch's fix worth keeping.
	var goneAt *string
	if err := h.db.QueryRow(`SELECT agent_gave_up_at FROM escalations WHERE id = 'tc-late'`).Scan(&goneAt); err != nil {
		t.Fatalf("load agent_gave_up_at: %v", err)
	}
	if goneAt == nil || *goneAt == "" {
		t.Error("agent_gave_up_at is unset — nothing records that the agent stopped waiting")
	}

	// Seven minutes later, the human clicks Approve.
	rr := escResolve(h, userID, wsID, "tc-late", "yes, here it is", "approve")
	if rr.Code != http.StatusOK {
		t.Fatalf("late resolve status = %d, want 200; body=%s\n"+
			"a question nobody ever answered must not answer 409", rr.Code, rr.Body.String())
	}
	var res map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode resolve: %v", err)
	}
	// The answer landed, and the operator is told honestly that the run which
	// asked is not going to hear it.
	if waiting, ok := res["agent_still_waiting"].(bool); !ok || waiting {
		t.Errorf("agent_still_waiting = %v, want false — the operator must know the asking run already continued", res["agent_still_waiting"])
	}
	if s := escStatus(t, h, "tc-late"); s != escalationStatusResolved {
		t.Errorf("row status = %q, want RESOLVED", s)
	}
}

// The human's clock does eventually run out — an unanswered question a week
// old is a stale record, not an open one.
func TestHumanAnswerDeadlineStillExpires(t *testing.T) {
	h, _, userID, wsID, crewID, agentID := escLifecycleRig(t)
	past := time.Now().UTC().Add(-time.Minute)
	seedTwoClockEscalation(t, h, "tc-stale", wsID, crewID, agentID, "TEXT", &past, &past, nil)

	if _, err := h.sweepExpiredEscalations(context.Background(), wsID); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if s := escStatus(t, h, "tc-stale"); s != escalationStatusExpired {
		t.Fatalf("status = %q, want EXPIRED once the ANSWER deadline passed", s)
	}
	if rr := escResolve(h, userID, wsID, "tc-stale", "too late", "approve"); rr.Code != http.StatusConflict {
		t.Errorf("resolve after the answer deadline = %d, want 409", rr.Code)
	}
}

// A row past its agent window but with NO answer deadline (a legacy row, or one
// raised before the human clock existed) must never be swept: "no deadline" and
// "the epoch" are different claims.
func TestSweepIgnoresRowsWithNoAnswerDeadline(t *testing.T) {
	h, _, _, wsID, crewID, agentID := escLifecycleRig(t)
	past := time.Now().UTC().Add(-time.Hour)
	seedTwoClockEscalation(t, h, "tc-legacy", wsID, crewID, agentID, "TEXT", &past, nil, nil)

	n, err := h.sweepExpiredEscalations(context.Background(), wsID)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 {
		t.Errorf("sweep expired %d rows, want 0 — the agent's deadline is not grounds to expire", n)
	}
	if s := escStatus(t, h, "tc-legacy"); s != escalationStatusPending {
		t.Errorf("status = %q, want PENDING", s)
	}
}

// ─── FINDING 2: a dead proposal is disposed of, not stranded ──────────────

// An expired CREDENTIAL escalation must dispose of its staged secret exactly
// the way a rejection does, and must not jam the name forever.
func TestExpiredCredentialEscalationDisposesStagedCredential(t *testing.T) {
	ensureEncryptionKey(t)
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	escID, credID := stageCredentialEscalation(t, h, wsID, crewID, "STRIPE_API_KEY")

	expireByHumanDeadline(t, h, escID, wsID)

	status, _, _, _, _, deleted := credRow(t, h, credID)
	if status != "REJECTED" || !deleted {
		t.Fatalf("staged credential after expiry: status=%q deleted=%v, want REJECTED + deleted_at set — "+
			"the only route that could activate or reject it is now 409, so an undisposed row is unreachable forever",
			status, deleted)
	}

	// And the name is free again: a later proposal must stage, not conflict.
	_, res := h.createPendingCredential(context.Background(), wsID, agentID,
		credentialProposal{Name: "STRIPE_API_KEY", Type: "SECRET", Provider: "NONE", Value: "v2"})
	if res != pendingCredStaged {
		t.Fatalf("re-proposing the name after expiry = %v, want pendingCredStaged — "+
			"one unanswered question must not jam auto-staging for that name forever", res)
	}
}

// A cancellation is a human withdrawing the question. The secret they declined
// to consider must go the same way.
func TestCancelledCredentialEscalationDisposesStagedCredential(t *testing.T) {
	ensureEncryptionKey(t)
	h, userID, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	escID, credID := stageCredentialEscalation(t, h, wsID, crewID, "PG_PASSWORD")

	rr := escCancel(h, userID, wsID, escID, "we rotated it by hand")
	if rr.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	status, _, _, _, _, deleted := credRow(t, h, credID)
	if status != "REJECTED" || !deleted {
		t.Fatalf("staged credential after cancel: status=%q deleted=%v, want REJECTED + deleted_at set", status, deleted)
	}
	_, res := h.createPendingCredential(context.Background(), wsID, agentID,
		credentialProposal{Name: "PG_PASSWORD", Type: "SECRET", Provider: "NONE", Value: "v2"})
	if res != pendingCredStaged {
		t.Fatalf("re-proposing the name after cancel = %v, want pendingCredStaged", res)
	}
}

// Belt and braces for the probe itself: a PENDING_APPROVAL row whose escalation
// is no longer answerable is a dead proposal, and the live-name probe must not
// count it. This covers the rows the buggy branch already stranded, which no
// forward-looking disposal can reach.
func TestNameProbeIgnoresStrandedProposal(t *testing.T) {
	ensureEncryptionKey(t)
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	escID, credID := stageCredentialEscalation(t, h, wsID, crewID, "GH_TOKEN")

	// Strand it exactly as the regression did: flip the escalation terminal
	// without touching the credential.
	execOrFatal(t, h.db, `UPDATE escalations SET status = 'EXPIRED' WHERE id = ?`, escID)

	_, res := h.createPendingCredential(context.Background(), wsID, agentID,
		credentialProposal{Name: "GH_TOKEN", Type: "SECRET", Provider: "NONE", Value: "v2"})
	if res != pendingCredStaged {
		t.Fatalf("re-proposing over a stranded proposal = %v, want pendingCredStaged", res)
	}
	// Retirement soft-deletes it, and the same-name cleanup that follows in the
	// same pass hard-deletes it — so the unreachable secret is gone, not merely
	// hidden. Either shape is acceptable; still being live is not.
	var live int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM credentials WHERE id = ? AND deleted_at IS NULL AND status = 'PENDING_APPROVAL'`,
		credID).Scan(&live); err != nil {
		t.Fatalf("count stranded proposal: %v", err)
	}
	if live != 0 {
		t.Errorf("the stranded proposal is still live after retirement — it can never be approved or rejected")
	}
}

// A proposal whose escalation is STILL PENDING is live and must still conflict:
// staging two rows under one name is what the UNIQUE constraint forbids, and
// the second one would have no way to be told apart from the first.
func TestNameProbeStillConflictsWhileTheQuestionIsOpen(t *testing.T) {
	ensureEncryptionKey(t)
	h, _, wsID, crewID, agentID := covEscFixture(t)
	seedChat(t, h, "covesc-chat", agentID, wsID)
	stageCredentialEscalation(t, h, wsID, crewID, "OPENAI_KEY")

	_, res := h.createPendingCredential(context.Background(), wsID, agentID,
		credentialProposal{Name: "OPENAI_KEY", Type: "SECRET", Provider: "NONE", Value: "v2"})
	if res != pendingCredNameConflict {
		t.Fatalf("re-proposing while the first question is open = %v, want pendingCredNameConflict", res)
	}
}

// ─── the agent's clock, for the case it was designed for ──────────────────

// Unchanged: the agent waits the server's window, gives up, and continues with
// a warning. What the create publishes is the AGENT's window — the sidecar
// bounds its poll on it and must not be handed the human's week.
func TestAgentWaitWindowIsUnchanged(t *testing.T) {
	h, _, wsID, crewID, leadID, _ := newQueryHandler(t)
	chatID := generateCUID()
	execOrFatal(t, h.db, `INSERT INTO chats(id,agent_id,workspace_id,mode,status) VALUES (?, ?, ?, 'CHAT', 'ACTIVE')`,
		chatID, leadID, wsID)

	req := httptest.NewRequest("POST", "/", jsonBody(map[string]string{
		"from_slug": "lead", "reason": "need a decision",
		"crew_id": crewID, "workspace_id": wsID, "chat_id": chatID,
	}))
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		EscalationID   string `json:"escalation_id"`
		DeadlineAt     string `json:"deadline_at"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := int(escalationAgentWait.Seconds()); body.TimeoutSeconds != want {
		t.Errorf("timeout_seconds = %d, want the AGENT's window %d", body.TimeoutSeconds, want)
	}
	// The two clocks are both written, and they are not the same clock.
	var agentDL, answerDL string
	if err := h.db.QueryRow(`SELECT COALESCE(deadline_at,''), COALESCE(answer_deadline_at,'')
		FROM escalations WHERE id = ?`, body.EscalationID).Scan(&agentDL, &answerDL); err != nil {
		t.Fatalf("load deadlines: %v", err)
	}
	if agentDL != body.DeadlineAt {
		t.Errorf("published deadline_at %q != stored %q", body.DeadlineAt, agentDL)
	}
	if answerDL == "" {
		t.Fatal("answer_deadline_at was not written — the human has no clock at all")
	}
	a, err := time.Parse(time.RFC3339, agentDL)
	if err != nil {
		t.Fatalf("parse agent deadline: %v", err)
	}
	hum, err := time.Parse(time.RFC3339, answerDL)
	if err != nil {
		t.Fatalf("parse answer deadline: %v", err)
	}
	if !hum.After(a.Add(time.Hour)) {
		t.Errorf("answer_deadline_at %s is not meaningfully later than deadline_at %s — "+
			"collapsing the two is the regression", answerDL, agentDL)
	}
}
