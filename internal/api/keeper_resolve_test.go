package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/eval"
)

// An escalation exists so a PERSON rules on it, and there was no way for a
// person to rule on it. The inbox card said so out loud — "missing on the
// server: a keeper request has no resolve endpoint yet" — and sent the operator
// to a terminal, where `inbox resolve` marked the notification read without
// recording a verdict against the request it notified about.
//
// So the decision the whole tier system defers to a human was the one decision
// the product could not accept.
//
// The security properties this endpoint has to hold, each pinned below:
//
//	roleManage        — OWNER/ADMIN, the same gate the audience is addressed by
//	workspace-scoped  — an admin cannot rule on another tenant's request
//	settled-once      — a decided request cannot be re-decided
//	four-eyes         — the owner of the requesting agent cannot approve it

func resolveReq(t *testing.T, ws, role, userID, reqID string, body map[string]any) *http.Request {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST",
		"/api/v1/admin/keeper/requests/"+reqID+"/resolve", bytes.NewReader(raw))
	r.SetPathValue("requestId", reqID)
	ctx := context.WithValue(r.Context(), ctxRole, role)
	if userID != "" {
		ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: userID})
	}
	if ws != "" {
		ctx = context.WithValue(ctx, ctxWorkspaceID, ws)
	}
	return r.WithContext(ctx)
}

func TestKeeperResolve_RefusesARoleThatCannotManage(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	for _, role := range []string{"MANAGER", "MEMBER", "VIEWER", ""} {
		rr := httptest.NewRecorder()
		h.HandleResolve(rr, resolveReq(t, "ws1", role, "u1", "req1",
			map[string]any{"decision": "ALLOW"}))
		if rr.Code != http.StatusForbidden {
			t.Errorf("role %q got %d, want 403 — the audience is addressed at ADMIN precisely because this is the gate",
				role, rr.Code)
		}
	}
}

func TestKeeperResolve_RequiresAWorkspace(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	rr := httptest.NewRecorder()
	h.HandleResolve(rr, resolveReq(t, "", "ADMIN", "u1", "req1",
		map[string]any{"decision": "ALLOW"}))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 without a workspace", rr.Code)
	}
}

// The decision vocabulary is closed. A typo must not be stored as a verdict —
// this row is what the eval harness later reads as ground truth, and a value
// nothing recognises would be counted as neither approval nor refusal.
func TestKeeperResolve_RefusesAnUnknownDecision(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	for _, d := range []string{"", "MAYBE", "yes", "ESCALATE", "approve"} {
		rr := httptest.NewRecorder()
		h.HandleResolve(rr, resolveReq(t, "ws1", "ADMIN", "u1", "req1",
			map[string]any{"decision": d}))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("decision %q got %d, want 400", d, rr.Code)
		}
	}
}

// Case and surrounding space are normalised, not rejected: "allow" and "ALLOW "
// are the same verdict, and an API that refused one of them would be pedantry
// rather than safety. They reach the lookup — 404 here, since the fixture has no
// such request — which is how we know they were accepted as verdicts.
func TestKeeperResolve_NormalisesCaseAndSpace(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	for _, d := range []string{"allow", "ALLOW ", " deny", "Deny"} {
		rr := httptest.NewRecorder()
		h.HandleResolve(rr, resolveReq(t, "ws1", "ADMIN", "u1", "req1",
			map[string]any{"decision": d}))
		if rr.Code == http.StatusBadRequest {
			t.Errorf("decision %q was rejected as unknown; case and space are not the verdict", d)
		}
	}
}

// The ruling has to reach the place the EVAL reads, and that place is not
// keeper_requests.
//
// eval.LoadCorpus takes a human label from inbox_items: kind='escalation',
// source_id = the keeper request, state='resolved', a named resolved_by_user_id
// and a resolved_action of 'approved'/'denied' (corpus.go, humanInboxSQL). It
// deliberately refuses to read keeper_requests.decision as truth, because that
// column holds the MODEL's verdict — scoring against it measures agreement with
// the predecessor, which is the whole defect P4 exists to remove.
//
// So an endpoint that settles the request without resolving the inbox row would
// leave the operator's ruling invisible to the only consumer that needs it: they
// would rule on twenty escalations and `keeper eval` would still report that no
// human has ruled on anything. Silent, and expensive — it costs a person an
// afternoon before anyone finds out.
//
// Which makes this the acceptance test for the whole exercise: rule through the
// endpoint, and ask the eval whether it can see it.
func TestKeeperResolve_MakesTheRulingVisibleToTheEval(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := newKeeperHandler(t, db)

	// An escalated access request, prompt recorded, exactly as the keeper writes
	// one — LoadCorpus skips rows with an empty ollama_prompt since there would
	// be nothing to replay.
	execOrFatal(t, db, `
		INSERT INTO keeper_requests
			(id, request_type, requesting_agent_id, requesting_crew_id, credential_id,
			 intent, decision, risk_score, ollama_prompt, created_at)
		VALUES ('kr-eval', 'access', ?, ?, ?, 'rotate the production certificates',
		        'ESCALATE', 7, 'PROMPT TEXT', '2026-01-01T00:00:00Z')`,
		agentID, crewID, credID)

	// ...and the inbox item the keeper raises alongside it.
	execOrFatal(t, db, `
		INSERT INTO inbox_items (id, workspace_id, kind, source_id, target_role, title, state)
		VALUES ('ibx-eval', ?, 'escalation', 'kr-eval', 'ADMIN', 'Keeper escalation', 'unread')`,
		wsID)

	rr := httptest.NewRecorder()
	h.HandleResolve(rr, resolveReq(t, wsID, "ADMIN", "user-deciding", "kr-eval",
		map[string]any{"decision": "ALLOW", "reason": "change window is declared"}))
	if rr.Code != http.StatusOK {
		t.Fatalf("resolve returned %d: %s", rr.Code, rr.Body.String())
	}

	rows, err := eval.LoadCorpus(context.Background(), db, 0)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	var got *eval.CorpusRow
	for i := range rows {
		if rows[i].ID == "kr-eval" {
			got = &rows[i]
		}
	}
	if got == nil {
		t.Fatalf("the resolved request is not in the corpus at all (%d rows loaded)", len(rows))
	}
	if !got.LabelSource.IsHuman() {
		t.Errorf("label source is %q, want human — the operator's ruling never reached inbox_items, "+
			"so the eval is scoring the model against itself", got.LabelSource)
	}
	if got.LabelOrigin != eval.OriginInbox {
		t.Errorf("label origin is %q, want %q", got.LabelOrigin, eval.OriginInbox)
	}
	if got.Label != eval.Allow {
		t.Errorf("label is %q, want ALLOW — the verdict the person actually gave", got.Label)
	}
}

// DENY has to travel the same way, and it is the direction with the sharper
// consequence: a refusal that the eval cannot see means a candidate model is
// never penalised for granting what a person refused.
func TestKeeperResolve_ADenialIsAlsoGroundTruth(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := newKeeperHandler(t, db)

	execOrFatal(t, db, `
		INSERT INTO keeper_requests
			(id, request_type, requesting_agent_id, requesting_crew_id, credential_id,
			 intent, decision, risk_score, ollama_prompt, created_at)
		VALUES ('kr-deny', 'access', ?, ?, ?, 'need it', 'ESCALATE', 6, 'PROMPT TEXT',
		        '2026-01-01T00:00:00Z')`,
		agentID, crewID, credID)
	execOrFatal(t, db, `
		INSERT INTO inbox_items (id, workspace_id, kind, source_id, target_role, title, state)
		VALUES ('ibx-deny', ?, 'escalation', 'kr-deny', 'ADMIN', 'Keeper escalation', 'unread')`,
		wsID)

	rr := httptest.NewRecorder()
	h.HandleResolve(rr, resolveReq(t, wsID, "ADMIN", "user-deciding", "kr-deny",
		map[string]any{"decision": "DENY"}))
	if rr.Code != http.StatusOK {
		t.Fatalf("resolve returned %d: %s", rr.Code, rr.Body.String())
	}

	rows, err := eval.LoadCorpus(context.Background(), db, 0)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	for _, r := range rows {
		if r.ID != "kr-deny" {
			continue
		}
		if !r.LabelSource.IsHuman() || r.Label != eval.Deny {
			t.Fatalf("label %q from %q, want DENY from a human", r.Label, r.LabelSource)
		}
		return
	}
	t.Fatal("the denied request is not in the corpus")
}

// The inbox row is a projection of the ruling, so it must not survive the ruling
// failing — and it must not be written twice. Both are checked by resolving once
// and reading the row: state, the deciding user and the action all come from the
// same call that settled the request.
func TestKeeperResolve_StampsWhoDecidedOnTheInboxRow(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := newKeeperHandler(t, db)

	execOrFatal(t, db, `
		INSERT INTO keeper_requests
			(id, request_type, requesting_agent_id, requesting_crew_id, credential_id,
			 intent, decision, ollama_prompt)
		VALUES ('kr-stamp', 'access', ?, ?, ?, 'x', 'ESCALATE', 'PROMPT')`,
		agentID, crewID, credID)
	execOrFatal(t, db, `
		INSERT INTO inbox_items (id, workspace_id, kind, source_id, target_role, title, state)
		VALUES ('ibx-stamp', ?, 'escalation', 'kr-stamp', 'ADMIN', 'Keeper escalation', 'unread')`,
		wsID)

	rr := httptest.NewRecorder()
	h.HandleResolve(rr, resolveReq(t, wsID, "ADMIN", "alice", "kr-stamp",
		map[string]any{"decision": "ALLOW"}))
	if rr.Code != http.StatusOK {
		t.Fatalf("resolve returned %d: %s", rr.Code, rr.Body.String())
	}

	var state, by, action string
	if err := db.QueryRow(`
		SELECT state, COALESCE(resolved_by_user_id,''), COALESCE(resolved_action,'')
		  FROM inbox_items WHERE id = 'ibx-stamp'`).Scan(&state, &by, &action); err != nil {
		t.Fatalf("read inbox row: %v", err)
	}
	if state != "resolved" {
		t.Errorf("inbox state is %q, want resolved — the operator clicked Approve and the item stayed in their inbox", state)
	}
	if by != "alice" {
		t.Errorf("resolved_by_user_id is %q, want alice; the eval requires a NAMED decider", by)
	}
	if action != "approved" {
		t.Errorf("resolved_action is %q, want approved — humanInboxSQL reads only 'approved'/'denied' as verdicts", action)
	}
}

// Settled once. A request whose decision is already terminal has been ACTED ON —
// the credential was delivered or refused — so letting it be re-decided would
// rewrite an audit record about something that already happened. It is also the
// row eval.LoadCorpus reads as ground truth, and a verdict that can be
// overwritten is not a verdict.
//
// Only ESCALATE is awaiting a person, so only ESCALATE is resolvable.
func TestKeeperResolve_RefusesARequestThatIsAlreadySettled(t *testing.T) {
	for _, settled := range []string{"ALLOW", "DENY"} {
		t.Run(settled, func(t *testing.T) {
			db := setupTestDB(t)
			wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
			h := newKeeperHandler(t, db)

			execOrFatal(t, db, `
				INSERT INTO keeper_requests
					(id, request_type, requesting_agent_id, requesting_crew_id, credential_id,
					 intent, decision)
				VALUES ('kr-settled', 'access', ?, ?, ?, 'x', ?)`,
				agentID, crewID, credID, settled)

			rr := httptest.NewRecorder()
			h.HandleResolve(rr, resolveReq(t, wsID, "ADMIN", "u1", "kr-settled",
				map[string]any{"decision": "ALLOW"}))
			if rr.Code != http.StatusConflict {
				t.Fatalf("re-deciding a %s request returned %d, want 409: %s",
					settled, rr.Code, rr.Body.String())
			}

			// And the stored verdict is untouched — a 409 that still wrote would be
			// the same defect with a louder status code.
			var got string
			if err := db.QueryRow(`SELECT decision FROM keeper_requests WHERE id = 'kr-settled'`).
				Scan(&got); err != nil {
				t.Fatalf("read decision: %v", err)
			}
			if got != settled {
				t.Errorf("decision is now %q, want %q — the refusal did not prevent the write", got, settled)
			}
		})
	}
}

// Four-eyes. L4 forces SecondApprover (tier.go), so the owner of the requesting
// agent may not approve what that agent asked for. Approving your own agent's
// production request is the event this control exists to catch, which is why the
// blocked attempt is journaled rather than merely refused.
func TestKeeperResolve_OwnerOfTheRequestingAgentCannotApproveIt(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, _ := seedKeeperFixture(t, db)
	h := newKeeperHandler(t, db)

	// alice is a real user row, distinct from the fixture's own user:
	// agents.created_by_user_id and credentials.created_by are both foreign keys,
	// and a fixture that dodged them would prove the check works on data the schema
	// cannot hold.
	const alice = "user-alice"
	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, 'alice@example.com', 'Alice')`, alice)

	// A critical credential: L4's tier policy sets SecondApprover unconditionally,
	// so this does not depend on the workspace having opted in.
	execOrFatal(t, db, `
		INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, created_by)
		VALUES ('cred-l4', ?, 'PROD_DB_ADMIN', 'SECRET', 4, 'v1:aW52YWxpZA==', ?)`, wsID, alice)
	execOrFatal(t, db, `UPDATE agents SET created_by_user_id = ? WHERE id = ?`, alice, agentID)
	execOrFatal(t, db, `
		INSERT INTO keeper_requests
			(id, request_type, requesting_agent_id, requesting_crew_id, credential_id, intent, decision)
		VALUES ('kr-4eyes', 'access', ?, ?, 'cred-l4', 'migrate the orders table', 'ESCALATE')`,
		agentID, crewID)

	rr := httptest.NewRecorder()
	h.HandleResolve(rr, resolveReq(t, wsID, "ADMIN", alice, "kr-4eyes",
		map[string]any{"decision": "ALLOW"}))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("the agent's own owner approved it: got %d, want 403: %s", rr.Code, rr.Body.String())
	}

	var decision string
	if err := db.QueryRow(`SELECT COALESCE(decision,'') FROM keeper_requests WHERE id = 'kr-4eyes'`).
		Scan(&decision); err != nil {
		t.Fatalf("read decision: %v", err)
	}
	if decision != "ESCALATE" {
		t.Errorf("decision is %q, want it left at ESCALATE — still waiting for somebody else", decision)
	}
}

// DENY is not gated by four-eyes: the rule guards against somebody waving their
// own agent through, and refusing a request cannot be that. Gating it too would
// mean the one person present could not close an obviously bad ask.
func TestKeeperResolve_FourEyesDoesNotBlockADenial(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, _ := seedKeeperFixture(t, db)
	h := newKeeperHandler(t, db)

	const alice = "user-alice"
	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, 'alice@example.com', 'Alice')`, alice)
	execOrFatal(t, db, `
		INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, created_by)
		VALUES ('cred-l4d', ?, 'PROD_PAYMENTS_KEY', 'SECRET', 4, 'v1:aW52YWxpZA==', ?)`, wsID, alice)
	execOrFatal(t, db, `UPDATE agents SET created_by_user_id = ? WHERE id = ?`, alice, agentID)
	execOrFatal(t, db, `
		INSERT INTO keeper_requests
			(id, request_type, requesting_agent_id, requesting_crew_id, credential_id, intent, decision)
		VALUES ('kr-4eyes-deny', 'access', ?, ?, 'cred-l4d', 'send the key to a contractor', 'ESCALATE')`,
		agentID, crewID)

	rr := httptest.NewRecorder()
	h.HandleResolve(rr, resolveReq(t, wsID, "ADMIN", alice, "kr-4eyes-deny",
		map[string]any{"decision": "DENY"}))
	if rr.Code != http.StatusOK {
		t.Fatalf("the owner could not refuse their own agent's request: got %d, want 200: %s",
			rr.Code, rr.Body.String())
	}
}

// A request that does not exist in THIS workspace is a 404, not a 403 and not a
// silent success. Scoping is by workspace and not by id alone, so an admin
// holding a request id from another tenant learns nothing from the response.
func TestKeeperResolve_ScopesToTheCallersWorkspace(t *testing.T) {
	db := setupTestDB(t)
	h := NewKeeperHandler(db, "tok", nil, newTestLogger())

	rr := httptest.NewRecorder()
	h.HandleResolve(rr, resolveReq(t, "ws-mine", "ADMIN", "u1", "req-from-another-tenant",
		map[string]any{"decision": "ALLOW"}))
	if rr.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404 for a request outside the caller's workspace", rr.Code)
	}
}

// The human ruling has to reach the LEDGER too, and on dev2 it did not:
// `keeper history` on a request an operator had just denied showed PENDING →
// ESCALATE and stopped. keeper_requests said DENY, keeper_request_events said
// the request was still waiting for somebody.
//
// #1369 wrote the ledger precisely so the projection and the history could never
// disagree, and this is the one transition where the disagreement matters most:
// keeper_requests is UPDATEd in place, so the ledger is the only record of WHO
// decided and when. Without it the audit trail says a person's verdict was the
// model's.
//
// keeperActorUser exists in keeper_events.go with the comment "an operator
// resolved an escalation" and nothing used it — the slot was cut for this and
// left empty.
func TestKeeperResolve_AppendsTheHumanDecisionToTheLedger(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := newKeeperHandler(t, db)

	execOrFatal(t, db, `
		INSERT INTO keeper_requests
			(id, request_type, requesting_agent_id, requesting_crew_id, credential_id, intent, decision)
		VALUES ('kr-ledger', 'access', ?, ?, ?, 'rotate the certs', 'ESCALATE')`,
		agentID, crewID, credID)
	execOrFatal(t, db, `
		INSERT INTO keeper_request_events
			(id, request_id, workspace_id, seq, state, actor_type, recorded_at)
		VALUES ('ev1', 'kr-ledger', ?, 1, 'PENDING', 'agent', '2026-01-01T00:00:00.000000000Z'),
		       ('ev2', 'kr-ledger', ?, 2, 'ESCALATE', 'keeper', '2026-01-01T00:00:01.000000000Z')`,
		wsID, wsID)

	rr := httptest.NewRecorder()
	h.HandleResolve(rr, resolveReq(t, wsID, "ADMIN", "alice", "kr-ledger",
		map[string]any{"decision": "DENY", "reason": "no change window declared"}))
	if rr.Code != http.StatusOK {
		t.Fatalf("resolve returned %d: %s", rr.Code, rr.Body.String())
	}

	var seq int
	var state, actorType, actorID, reason string
	if err := db.QueryRow(`
		SELECT seq, state, actor_type, COALESCE(actor_id,''), COALESCE(reason,'')
		  FROM keeper_request_events
		 WHERE request_id = 'kr-ledger' ORDER BY seq DESC LIMIT 1`).
		Scan(&seq, &state, &actorType, &actorID, &reason); err != nil {
		t.Fatalf("read the last ledger row: %v", err)
	}
	if seq != 3 {
		t.Fatalf("the last ledger entry is seq %d, want 3 — the human decision was never appended, "+
			"so `keeper history` still says the request is waiting for somebody", seq)
	}
	if state != "DENY" {
		t.Errorf("ledger state is %q, want DENY", state)
	}
	if actorType != keeperActorUser {
		t.Errorf("actor_type is %q, want %q — a person decided this, not the model",
			actorType, keeperActorUser)
	}
	if actorID != "alice" {
		t.Errorf("actor_id is %q, want alice; WHO decided is the point of the entry", actorID)
	}
	if reason != "no change window declared" {
		t.Errorf("reason is %q, want the operator's own words", reason)
	}
}

// The ledger append rides the same transaction as the decision, so a request
// that is refused must leave no trace at all — a history showing a DENY that was
// never applied is worse than one missing an entry.
func TestKeeperResolve_ARefusedRulingLeavesNoLedgerEntry(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, _ := seedKeeperFixture(t, db)
	h := newKeeperHandler(t, db)

	const alice = "user-alice"
	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, 'alice@example.com', 'Alice')`, alice)
	execOrFatal(t, db, `
		INSERT INTO credentials (id, workspace_id, name, type, security_level, encrypted_value, created_by)
		VALUES ('cred-l4x', ?, 'PROD_DB_ADMIN', 'SECRET', 4, 'v1:aW52YWxpZA==', ?)`, wsID, alice)
	execOrFatal(t, db, `UPDATE agents SET created_by_user_id = ? WHERE id = ?`, alice, agentID)
	execOrFatal(t, db, `
		INSERT INTO keeper_requests
			(id, request_type, requesting_agent_id, requesting_crew_id, credential_id, intent, decision)
		VALUES ('kr-refused', 'access', ?, ?, 'cred-l4x', 'migrate orders', 'ESCALATE')`,
		agentID, crewID)

	rr := httptest.NewRecorder()
	h.HandleResolve(rr, resolveReq(t, wsID, "ADMIN", alice, "kr-refused",
		map[string]any{"decision": "ALLOW"}))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("four-eyes did not refuse: %d %s", rr.Code, rr.Body.String())
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM keeper_request_events WHERE request_id = 'kr-refused'`).
		Scan(&n); err != nil {
		t.Fatalf("count ledger rows: %v", err)
	}
	if n != 0 {
		t.Errorf("the refused ruling left %d ledger entries; a history showing a decision "+
			"that was never applied is worse than one missing an entry", n)
	}
}

// Settled-once is checked BEFORE the transaction opens, so the sequential test
// above cannot see the hole CodeRabbit found: two requests can both read
// ESCALATE, both pass the check, and both write — the UPDATE filtered only by
// id. The later one silently overwrites a verdict somebody already gave, and the
// ledger ends up with two terminal entries for one decision.
//
// That is the exact property the endpoint claims: this row is what the eval
// reads as ground truth, and a verdict that can be rewritten is not one.
//
// So the guard has to live in the WRITE, not in a read before it: update only
// while the row is still ESCALATE, and treat "no row changed" as the 409.
func TestKeeperResolve_ConcurrentRulingsSettleExactlyOnce(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := newKeeperHandler(t, db)

	execOrFatal(t, db, `
		INSERT INTO keeper_requests
			(id, request_type, requesting_agent_id, requesting_crew_id, credential_id, intent, decision)
		VALUES ('kr-race', 'access', ?, ?, ?, 'rotate the certs', 'ESCALATE')`,
		agentID, crewID, credID)

	const racers = 6
	start := make(chan struct{})
	codes := make([]int, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Half approve, half refuse: whichever lands first, the other verdict
			// must not overwrite it.
			d := "ALLOW"
			if i%2 == 1 {
				d = "DENY"
			}
			rr := httptest.NewRecorder()
			<-start
			h.HandleResolve(rr, resolveReq(t, wsID, "ADMIN", "alice", "kr-race",
				map[string]any{"decision": d}))
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
		t.Errorf("%d of %d rulings were accepted, want exactly 1 — a settled request was re-decided", ok, racers)
	}

	// One terminal entry in the ledger, matching the one accepted verdict. Two
	// would mean the audit trail records a decision that was overwritten as though
	// it still stood.
	var n int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM keeper_request_events
		 WHERE request_id = 'kr-race' AND state IN ('ALLOW','DENY')`).Scan(&n); err != nil {
		t.Fatalf("count ledger rows: %v", err)
	}
	if n != 1 {
		t.Errorf("the ledger holds %d terminal entries for one decision, want 1", n)
	}
}
