package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
)

// PRD §18 scenario 15, second half (B15, #2389): acting on a NEEDS_HUMAN
// card resumes the run, writes a receipt, and updates the same thread.
// The fixture is the real mention fixture — real AssignmentHandler as the
// dispatcher, so `answer` produces a real assignment attached to the
// session that asked.

type needsHumanRig struct {
	f            *mentionFixture
	ih           *InboxHandler
	sessionID    string
	assignmentID string
	cardID       string
}

func setupNeedsHumanRig(t *testing.T) *needsHumanRig {
	t.Helper()
	f := setupMentionFixture(t)
	sessionID, assignmentID := "sess_b15", "asg_b15"
	seedSession(t, f, sessionID, "active")
	execOrFatal(t, f.db, `UPDATE issue_agent_sessions SET agent_version = 3 WHERE id = ?`, sessionID)
	seedSessionAssignment(t, f, assignmentID, sessionID, "COMPLETED")
	execOrFatal(t, f.db, `UPDATE issue_agent_sessions SET state = 'awaiting_input' WHERE id = ?`, sessionID)
	f.assign.createOutcomeInboxItem(context.Background(), assignmentID, f.wsID, "worker", "blocked: which bucket?")

	var cardID string
	if err := f.db.QueryRow(`SELECT id FROM inbox_items WHERE kind = ? AND source_id = ?`,
		inbox.KindRunNeedsHuman, assignmentID).Scan(&cardID); err != nil {
		t.Fatalf("card not raised: %v", err)
	}
	ih := NewInboxHandler(f.db, newTestLogger(), nil)
	ih.SetMentionDispatcher(f.assign)
	return &needsHumanRig{f: f, ih: ih, sessionID: sessionID, assignmentID: assignmentID, cardID: cardID}
}

func (r *needsHumanRig) act(t *testing.T, cardID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req.SetPathValue("id", cardID)
	req = withWorkspaceUser(req, r.f.userID, r.f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	r.ih.Act(rr, req)
	r.f.assign.WaitDispatches()
	return rr
}

func (r *needsHumanRig) card(t *testing.T) (state, action, by string, payload map[string]any) {
	t.Helper()
	var payloadJSON string
	var act, byp sql.NullString
	if err := r.f.db.QueryRow(`SELECT state, resolved_action, resolved_by_user_id, payload_json FROM inbox_items WHERE id = ?`, r.cardID).
		Scan(&state, &act, &byp, &payloadJSON); err != nil {
		t.Fatalf("read card: %v", err)
	}
	_ = json.Unmarshal([]byte(payloadJSON), &payload)
	return state, act.String, byp.String, payload
}

func TestInboxAct_Answer_ResumesTheSessionThatAsked(t *testing.T) {
	r := setupNeedsHumanRig(t)
	rr := r.act(t, r.cardID, `{"action":"answer","input":"Use the staging bucket."}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		State   string          `json:"state"`
		Action  string          `json:"action"`
		Receipt inboxActReceipt `json:"receipt"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.State != "resolved" || resp.Action != "answer" {
		t.Fatalf("resp = %+v", resp)
	}

	// 1. The answer is a comment on the issue, by the person.
	var n int
	if err := r.f.db.QueryRow(`SELECT COUNT(*) FROM mission_comments WHERE mission_id = ? AND author_type = 'user' AND author_id = ? AND body = 'Use the staging bucket.'`,
		r.f.missionID, r.f.userID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("answer comment rows = %d (err %v), want 1", n, err)
	}
	if resp.Receipt.CommentID == "" {
		t.Error("receipt lacks the comment id")
	}

	// 2. It became a delivery to the SAME agent, and a new run attached
	//    to the SAME session — the run resumed from the card action.
	if resp.Receipt.DeliveryID == "" || resp.Receipt.DispatchState != mentionDispatchDispatched {
		t.Fatalf("receipt delivery = %q state = %q, want a delivery and %q", resp.Receipt.DeliveryID, resp.Receipt.DispatchState, mentionDispatchDispatched)
	}
	if resp.Receipt.RunID == "" || resp.Receipt.RunID == r.assignmentID {
		t.Fatalf("receipt run_id = %q, want a NEW run", resp.Receipt.RunID)
	}
	var runAgent, runSession string
	if err := r.f.db.QueryRow(`SELECT assigned_to_id, COALESCE(session_id,'') FROM assignments WHERE id = ?`, resp.Receipt.RunID).
		Scan(&runAgent, &runSession); err != nil {
		t.Fatalf("new run row: %v", err)
	}
	if runAgent != r.f.target || runSession != r.sessionID {
		t.Fatalf("new run = agent %s session %s, want agent %s session %s", runAgent, runSession, r.f.target, r.sessionID)
	}
	var delAgent, delState string
	if err := r.f.db.QueryRow(`SELECT agent_id, state FROM mission_comment_mentions WHERE id = ?`, resp.Receipt.DeliveryID).Scan(&delAgent, &delState); err != nil {
		t.Fatalf("delivery row: %v", err)
	}
	if delAgent != r.f.target || delState != "consumed" {
		t.Fatalf("delivery = agent %s state %s, want %s consumed", delAgent, delState, r.f.target)
	}
	if state, _ := sessionState(t, r.f, r.sessionID); state == "awaiting_input" {
		t.Fatalf("session still awaiting_input after the answer resumed it")
	}

	// 3. A receipt on the issue's event log, after the mentioned event,
	//    naming who acted and against which agent_version.
	var details, payloadJSON string
	if err := r.f.db.QueryRow(`SELECT details, payload_json FROM mission_activity WHERE mission_id = ? AND action = 'inbox_acted'`, r.f.missionID).
		Scan(&details, &payloadJSON); err != nil {
		t.Fatalf("receipt event: %v", err)
	}
	for _, want := range []string{"answer on NEEDS_HUMAN card " + r.cardID, "agent_version 3", "→ run " + resp.Receipt.RunID} {
		if !strings.Contains(details, want) {
			t.Errorf("receipt details %q lacks %q", details, want)
		}
	}
	if resp.Receipt.Seq == 0 || resp.Receipt.AgentVersion == nil || *resp.Receipt.AgentVersion != 3 {
		t.Errorf("receipt seq/agent_version = %d/%v", resp.Receipt.Seq, resp.Receipt.AgentVersion)
	}

	// 4. The same card, resolved in place with the receipt — no new card.
	state, action, by, payload := r.card(t)
	if state != "resolved" || action != "answer" || by != r.f.userID {
		t.Fatalf("card = (%s, %s, %s)", state, action, by)
	}
	if rec, ok := payload["receipt"].(map[string]any); !ok || rec["run_id"] != resp.Receipt.RunID {
		t.Fatalf("card payload receipt = %v", payload["receipt"])
	}
	if payload["who_can_act"] == nil || payload["context"] == nil {
		t.Error("acting dropped the producer's payload keys")
	}
	if err := r.f.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind = ?`, inbox.KindRunNeedsHuman).Scan(&n); err != nil || n != 1 {
		t.Fatalf("run_needs_human cards = %d, want exactly 1", n)
	}

	// 5. Acting again is refused.
	if rr := r.act(t, r.cardID, `{"action":"dismiss"}`); rr.Code != http.StatusConflict {
		t.Fatalf("second act status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
}

func TestInboxAct_Answer_UndeliverableLeavesCardOpen(t *testing.T) {
	r := setupNeedsHumanRig(t)
	stub := &stubMentionDispatcher{err: &agentHeldError{msg: "agent is held"}}
	r.ih.SetMentionDispatcher(stub)
	rr := r.act(t, r.cardID, `{"action":"answer","input":"go ahead"}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "could not be delivered") {
		t.Errorf("body: %s", rr.Body.String())
	}
	if state, _, _, _ := r.card(t); state == "resolved" {
		t.Fatal("card resolved although nothing will pick the answer up")
	}
	var n int
	if err := r.f.db.QueryRow(`SELECT COUNT(*) FROM mission_comments WHERE mission_id = ? AND body = 'go ahead'`, r.f.missionID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the answer comment should still exist (rows=%d err=%v)", n, err)
	}
	if stub.count() != 1 {
		t.Fatalf("dispatch attempts = %d, want 1", stub.count())
	}
}

func TestInboxAct_TakeOverAndDismiss_SettleSessionAndReceipt(t *testing.T) {
	for _, action := range []string{inboxActTakeOver, inboxActDismiss} {
		t.Run(action, func(t *testing.T) {
			r := setupNeedsHumanRig(t)
			rr := r.act(t, r.cardID, `{"action":"`+action+`"}`)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
			}
			if state, _ := sessionState(t, r.f, r.sessionID); state != "idle" {
				t.Fatalf("session state = %s, want idle", state)
			}
			state, got, by, payload := r.card(t)
			if state != "resolved" || got != action || by != r.f.userID {
				t.Fatalf("card = (%s, %s, %s)", state, got, by)
			}
			if payload["receipt"] == nil {
				t.Fatal("card lacks the receipt")
			}
			var n int
			if err := r.f.db.QueryRow(`SELECT COUNT(*) FROM mission_activity WHERE mission_id = ? AND action = 'inbox_acted' AND details LIKE ?`,
				r.f.missionID, action+" on NEEDS_HUMAN card %").Scan(&n); err != nil || n != 1 {
				t.Fatalf("receipt events = %d (err %v)", n, err)
			}
			if err := r.f.db.QueryRow(`SELECT COUNT(*) FROM mission_comment_mentions WHERE mission_id = ?`, r.f.missionID).Scan(&n); err != nil || n != 0 {
				t.Fatalf("deliveries = %d, want 0 — %s must not wake the agent", n, action)
			}
		})
	}
}

func TestInboxAct_Validation(t *testing.T) {
	r := setupNeedsHumanRig(t)
	for _, tc := range []struct {
		name string
		id   string
		body string
		want int
	}{
		{"unknown action", r.cardID, `{"action":"approve"}`, http.StatusBadRequest},
		{"answer without input", r.cardID, `{"action":"answer","input":"  "}`, http.StatusBadRequest},
		{"invalid json", r.cardID, `{`, http.StatusBadRequest},
		{"unknown card", "ibx_nope", `{"action":"dismiss"}`, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rr := r.act(t, tc.id, tc.body); rr.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
	// A kind with no server-side action is pointed at its source.
	if err := inbox.Insert(context.Background(), r.f.db, newTestLogger(), inbox.Item{
		WorkspaceID: r.f.wsID, Kind: "failed_run", SourceID: "asg_failed", Title: "failed", Priority: "high",
	}); err != nil {
		t.Fatal(err)
	}
	var otherID string
	if err := r.f.db.QueryRow(`SELECT id FROM inbox_items WHERE kind = 'failed_run'`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	if rr := r.act(t, otherID, `{"action":"dismiss"}`); rr.Code != http.StatusConflict {
		t.Fatalf("failed_run act status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
}

// The card B6 raises now offers the kind's three actions.
func TestCreateOutcomeInboxItem_OffersAnswerTakeOverDismiss(t *testing.T) {
	r := setupNeedsHumanRig(t)
	var actionsJSON string
	if err := r.f.db.QueryRow(`SELECT actions_json FROM inbox_items WHERE id = ?`, r.cardID).Scan(&actionsJSON); err != nil {
		t.Fatal(err)
	}
	var actions []inbox.Action
	if err := json.Unmarshal([]byte(actionsJSON), &actions); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, a := range actions {
		got[a.ID] = true
	}
	for _, want := range []string{inboxActAnswer, inboxActTakeOver, inboxActDismiss} {
		if !got[want] {
			t.Errorf("card actions %s lack %q", actionsJSON, want)
		}
	}
}
