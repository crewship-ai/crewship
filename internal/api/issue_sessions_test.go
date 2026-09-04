package api

// Tests for issue_sessions.go — the B1 accept line's own words: "a mention
// reuses an existing session rather than creating a second"
// (PRD-ISSUES-AND-ROUTINES-2026 §9.2/§17, #2332).

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestMentions_SecondMentionReusesTheSameSession is the B1 accept-line proof.
// Two SEPARATE comments (two separate DispatchMention calls, not one comment
// mentioning the same agent twice — TestMentions_SameAgentTwiceDispatchesOnce
// already covers the per-comment dedup) both mention the same agent on the
// same issue. Before resolveOrCreateIssueAgentSession existed there was no
// concept of a session at all; this pins that the UPSERT keyed on
// UNIQUE(mission_id, agent_id) makes the second mention find the first
// mention's row rather than minting a second one, and that both resulting
// runs carry the SAME assignments.session_id.
func TestMentions_SecondMentionReusesTheSameSession(t *testing.T) {
	f := setupMentionFixture(t)

	f.comment(t, "first "+mentionToken("lead", f.target))
	f.comment(t, "second "+mentionToken("lead", f.target))
	// B4 (§10.1, #2343): the session's state now genuinely moves as its
	// runs claim and finish, so — unlike before B4, when 'pending' was a
	// static value nothing ever touched — the assertions below must wait
	// for both dispatched runs to actually settle rather than racing their
	// background goroutines.
	f.assign.WaitDispatches()

	if n := f.assignments(t); n != 2 {
		t.Fatalf("assignments = %d, want 2 — two separate mentions, two runs", n)
	}

	var sessionCount int
	if err := f.db.QueryRow(
		`SELECT COUNT(*) FROM issue_agent_sessions WHERE mission_id = ? AND agent_id = ?`,
		f.missionID, f.target,
	).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Fatalf("issue_agent_sessions rows for (mission, agent) = %d, want 1 — the second mention created a second session", sessionCount)
	}

	var sessionID string
	if err := f.db.QueryRow(
		`SELECT id FROM issue_agent_sessions WHERE mission_id = ? AND agent_id = ?`,
		f.missionID, f.target,
	).Scan(&sessionID); err != nil {
		t.Fatalf("read session id: %v", err)
	}
	if sessionID == "" {
		t.Fatal("session id is empty")
	}

	rows, err := f.db.Query(`SELECT COALESCE(session_id, '') FROM assignments WHERE mission_id = ? ORDER BY created_at`, f.missionID)
	if err != nil {
		t.Fatalf("query assignments: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			t.Fatalf("scan session_id: %v", err)
		}
		got = append(got, sid)
	}
	if len(got) != 2 {
		t.Fatalf("assignments with a mission_id = %d, want 2", len(got))
	}
	for _, sid := range got {
		if sid != sessionID {
			t.Errorf("assignment.session_id = %q, want %q (the one shared session)", sid, sessionID)
		}
	}

	// The API surface: ListSessions reports exactly this one session.
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/crews/"+f.crewID+"/issues/"+f.ident+"/sessions", nil),
		f.userID, f.wsID, "OWNER",
	)
	req.SetPathValue("crewId", f.crewID)
	req.SetPathValue("identifier", f.ident)
	rr := httptest.NewRecorder()
	f.issues.ListSessions(rr, req)
	if rr.Code != 200 {
		t.Fatalf("ListSessions status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var got2 []issueAgentSessionDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &got2); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	if len(got2) != 1 {
		t.Fatalf("ListSessions returned %d rows, want 1", len(got2))
	}
	if got2[0].ID != sessionID {
		t.Errorf("ListSessions id = %q, want %q", got2[0].ID, sessionID)
	}
	// B4 (§10.1, #2343): this fixture's AssignmentHandler has no
	// orchestrator wired (setupMentionFixture passes nil), so every
	// dispatched run fails immediately with "orchestrator not available"
	// — status=FAILED — and settleSessionForAssignment moves the session
	// 'active' -> 'error' once the SECOND run (the one that actually holds
	// active_run_id last) finishes. Before B4 this column was written once,
	// at creation, and never touched again; asserting 'error' here pins
	// that the claim/settle transitions this file adds actually fire on
	// the ordinary mention path, not just in an isolated unit test.
	if got2[0].State != "error" {
		t.Errorf("state = %q, want error (both runs failed — no orchestrator wired in this fixture)", got2[0].State)
	}
}

// TestResolveOrCreateIssueAgentSession_ConcurrentMentionsProduceOneRow is the
// -race proof that the reuse guarantee holds under real concurrent writers,
// not just sequentially. Mirrors the shape of
// missionactivity's TestEmit_SeqIsMonotonicUnderConcurrentWriters.
func TestResolveOrCreateIssueAgentSession_ConcurrentMentionsProduceOneRow(t *testing.T) {
	f := setupMentionFixture(t)

	const n = 10
	ids := make([]string, n)
	errs := make([]error, n)
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			id, err := resolveOrCreateIssueAgentSession(context.Background(), f.db, f.wsID, f.missionID, f.target)
			ids[i] = id
			errs[i] = err
			done <- i
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}

	first := ""
	for i, err := range errs {
		if err != nil {
			t.Fatalf("resolveOrCreateIssueAgentSession(%d): %v", i, err)
		}
		if ids[i] == "" {
			t.Fatalf("resolveOrCreateIssueAgentSession(%d): empty session id", i)
		}
		if first == "" {
			first = ids[i]
		} else if ids[i] != first {
			t.Errorf("session id %d = %q, want %q (same as every other caller)", i, ids[i], first)
		}
	}

	var count int
	if err := f.db.QueryRow(
		`SELECT COUNT(*) FROM issue_agent_sessions WHERE mission_id = ? AND agent_id = ?`,
		f.missionID, f.target,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("issue_agent_sessions rows = %d, want 1", count)
	}
}

// TestResolveOrCreateIssueAgentSession_FlagOff proves the kill switch:
// disabling issueAgentSessionsFlagKey must make session creation a no-op
// that returns ("", nil), not an error.
func TestResolveOrCreateIssueAgentSession_FlagOff(t *testing.T) {
	f := setupMentionFixture(t)
	f.db.Exec(`UPDATE feature_flags SET enabled = 0 WHERE key = ?`, issueAgentSessionsFlagKey)

	id, err := resolveOrCreateIssueAgentSession(context.Background(), f.db, f.wsID, f.missionID, f.target)
	if err != nil {
		t.Fatalf("resolveOrCreateIssueAgentSession: %v", err)
	}
	if id != "" {
		t.Errorf("session id = %q, want empty when the flag is off", id)
	}
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM issue_agent_sessions`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("issue_agent_sessions rows = %d, want 0 when the flag is off", count)
	}
}
