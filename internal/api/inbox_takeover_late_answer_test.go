package api

// PRD §9, "Dvě současná rozhodnutí / timeout / Issue takeover": the
// SIMULTANEOUS pairs are covered (TestInboxAnswerAndTakeoverCommitOnlyWinningAction,
// TestIssueWorkConcurrentTransfersHaveOneWinner,
// TestInternalStatusUpdateCannotRacePastHumanTakeover). What none of them
// covers is the SEQUENTIAL case, which is the one that actually happens: a
// person takes the work over, and the answer somebody else was already
// typing arrives a moment later.
//
// A race has a winner by construction — one of the two writes reaches the
// CAS first. A late answer has no race to lose: the card is already
// resolved, and the only thing standing between it and a second, contrary
// effect is the handler refusing it on its own. That is what these pin.

import (
	"encoding/json"
	"testing"
)

// answeredCount reports how many times the answer's side effects landed: the
// comment on the issue and the resumed assignment. Both must stay at zero
// when the answer is late.
func answeredCount(t *testing.T, r *needsHumanRig) (comments, resumed int) {
	t.Helper()
	if err := r.f.db.QueryRow(`SELECT COUNT(*) FROM mission_comments WHERE mission_id=? AND body='Use staging'`,
		r.f.missionID).Scan(&comments); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if err := r.f.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE session_id=? AND id!=?`,
		r.sessionID, r.assignmentID).Scan(&resumed); err != nil {
		t.Fatalf("count resumed assignments: %v", err)
	}
	return comments, resumed
}

// TestInboxTakeover_ThenLateAnswerIsRefused is the sequential order the race
// tests cannot reach.
func TestInboxTakeover_ThenLateAnswerIsRefused(t *testing.T) {
	r := setupNeedsHumanRig(t)

	if rr := r.act(t, r.cardID, `{"action":"take_over"}`); rr.Code != 200 {
		t.Fatalf("take_over: %d %s", rr.Code, rr.Body.String())
	}
	state, action, _, _ := r.card(t)
	if action != "take_over" {
		t.Fatalf("card resolved_action = %q, want take_over", action)
	}

	late := r.act(t, r.cardID, `{"action":"answer","input":"Use staging"}`)
	if late.Code == 200 {
		t.Fatalf("a late answer was accepted after the work was taken over: %s", late.Body.String())
	}
	if late.Code != 409 {
		t.Errorf("late answer status = %d, want 409 — the caller needs to know it lost, not that it broke", late.Code)
	}
	if late.Body.Len() == 0 {
		t.Error("the refusal carries no body, so the client cannot say why")
	}

	// The verdict on the card is unchanged, and the answer left nothing.
	stateAfter, actionAfter, _, _ := r.card(t)
	if stateAfter != state || actionAfter != action {
		t.Errorf("the late answer moved the card: (%q,%q) → (%q,%q)", state, action, stateAfter, actionAfter)
	}
	if comments, resumed := answeredCount(t, r); comments != 0 || resumed != 0 {
		t.Errorf("the refused answer still left effects: comments=%d resumed=%d", comments, resumed)
	}
}

// TestInboxAnswer_ThenLateTakeoverIsRefused is the same instant from the
// other side: the answer landed first, so the takeover is the late one. It
// must not pull the work back after an answer has already been committed and
// the agent resumed on it.
func TestInboxAnswer_ThenLateTakeoverIsRefused(t *testing.T) {
	r := setupNeedsHumanRig(t)

	if rr := r.act(t, r.cardID, `{"action":"answer","input":"Use staging"}`); rr.Code != 200 {
		t.Fatalf("answer: %d %s", rr.Code, rr.Body.String())
	}
	comments, resumed := answeredCount(t, r)
	if comments != 1 || resumed != 1 {
		t.Fatalf("the accepted answer did not take effect: comments=%d resumed=%d", comments, resumed)
	}

	late := r.act(t, r.cardID, `{"action":"take_over"}`)
	if late.Code == 200 {
		t.Fatalf("a late takeover was accepted after the answer committed: %s", late.Body.String())
	}
	if late.Code != 409 {
		t.Errorf("late takeover status = %d, want 409", late.Code)
	}
	_, action, _, _ := r.card(t)
	if action != "answer" {
		t.Errorf("the accepted verdict changed to %q", action)
	}
	if c, s := answeredCount(t, r); c != comments || s != resumed {
		t.Errorf("the refused takeover changed the answer's effects: comments %d→%d resumed %d→%d",
			comments, c, resumed, s)
	}
}

// TestInboxLateAnswer_RepeatedDoesNotAccumulate — a client that retries its
// late answer (a stuck spinner, an impatient double-click) must keep getting
// the same refusal rather than eventually slipping one through.
func TestInboxLateAnswer_RepeatedDoesNotAccumulate(t *testing.T) {
	r := setupNeedsHumanRig(t)
	if rr := r.act(t, r.cardID, `{"action":"take_over"}`); rr.Code != 200 {
		t.Fatalf("take_over: %d %s", rr.Code, rr.Body.String())
	}
	for i := 0; i < 3; i++ {
		if rr := r.act(t, r.cardID, `{"action":"answer","input":"Use staging"}`); rr.Code != 409 {
			t.Fatalf("late answer %d: status = %d, want 409", i+1, rr.Code)
		}
	}
	if comments, resumed := answeredCount(t, r); comments != 0 || resumed != 0 {
		t.Errorf("three refused answers left effects: comments=%d resumed=%d", comments, resumed)
	}
	_, action, _, payload := r.card(t)
	if action != "take_over" {
		t.Errorf("resolved_action = %q after three refusals", action)
	}
	// The card's payload must not have collected the refused attempts.
	raw, _ := json.Marshal(payload)
	if len(raw) > 4096 {
		t.Errorf("card payload grew to %d bytes across refused attempts", len(raw))
	}
}
