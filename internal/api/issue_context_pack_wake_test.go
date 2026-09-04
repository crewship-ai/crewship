package api

// TestDispatchMention_SecondWake_ReceivesCheckpointAndDelta is the
// end-to-end proof B5's accept line asks for: "at least one test observes
// what an agent actually receives on wake (the pack), not just the rows."
// (PRD-ISSUES-AND-ROUTINES-2026 §11.1/§17, work package B5, #2345).
//
// Driven through the real DispatchMention path (not a direct call into
// assembleContextPack) so what is asserted is exactly what
// insertCappedAssignment wrote into assignments.task — the same column the
// adapter reads to build the actual model prompt. A prior run's checkpoint
// and an intervening structural event are seeded directly (setupMentionFixture
// wires no orchestrator, so a real run fails near-instantly and cannot be
// used to produce a realistic checkpoint — the same reasoning
// issue_session_followups_test.go gives for seeding rather than racing a
// background goroutine).

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/missionactivity"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

func TestDispatchMention_SecondWake_ReceivesCheckpointAndDelta(t *testing.T) {
	f := setupMentionFixture(t)

	// First mention: opens the session.
	f.comment(t, "please take a look "+mentionToken("lead", f.target))
	if n := f.assignments(t); n != 1 {
		t.Fatalf("assignments after first mention = %d, want 1", n)
	}

	var sessionID string
	if err := f.db.QueryRow(
		`SELECT id FROM issue_agent_sessions WHERE mission_id = ? AND agent_id = ?`,
		f.missionID, f.target,
	).Scan(&sessionID); err != nil {
		t.Fatalf("read session id: %v", err)
	}

	// Simulate what a REAL completed run would have left behind: a
	// checkpoint recording finished work, and a structural event (a status
	// change some OTHER actor made) that happened after that run — the
	// exact shape §11.1 item 4's "delta since last look" exists for.
	const doneText = "Implemented the OAuth refresh flow and its tests"
	if err := writeSessionCheckpoint(context.Background(), f.db, f.wsID, sessionID, "run-1",
		orchestrator.CheckpointData{Done: doneText, NextStep: "add the CLI docs", Confidence: "high", Parsed: true}); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
	const eventDetail = "priority raised to urgent while you were away"
	if _, err := missionactivity.Emit(context.Background(), f.db, missionactivity.Entry{
		ID: "evt_wake_test", MissionID: f.missionID, ActorType: "user", ActorID: f.userID,
		Action: "priority_changed", Details: eventDetail,
	}); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	var lastConsumedBefore int
	if err := f.db.QueryRow(`SELECT last_consumed_seq FROM issue_agent_sessions WHERE id = ?`, sessionID).Scan(&lastConsumedBefore); err != nil {
		t.Fatalf("read last_consumed_seq before: %v", err)
	}

	// Second mention: the real wake this test is about.
	f.comment(t, "any update? "+mentionToken("lead", f.target))
	if n := f.assignments(t); n != 2 {
		t.Fatalf("assignments after second mention = %d, want 2", n)
	}

	var task, compaction string
	var tokens int
	var newSessionID string
	if err := f.db.QueryRow(`
		SELECT task, COALESCE(context_pack_compaction, ''), COALESCE(context_pack_tokens, 0), COALESCE(session_id, '')
		  FROM assignments WHERE mission_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`,
		f.missionID).Scan(&task, &compaction, &tokens, &newSessionID); err != nil {
		t.Fatalf("read second assignment: %v", err)
	}

	// This IS what the agent receives — assignments.task is the brief the
	// run was dispatched with.
	if !strings.Contains(task, doneText) {
		t.Fatalf("the run's brief does not contain the prior checkpoint's DONE work:\n%s", task)
	}
	if !strings.Contains(task, eventDetail) {
		t.Fatalf("the run's brief does not contain the intervening event:\n%s", task)
	}
	if !strings.Contains(task, `<untrusted source="context_pack"`) {
		t.Fatalf("the context pack is not fenced through untrusted.Wrap:\n%s", task)
	}
	if !strings.Contains(task, "---CHECKPOINT---") {
		t.Fatalf("the brief does not instruct the agent to end with a checkpoint block:\n%s", task)
	}

	if compaction == "" {
		t.Error("context_pack_compaction was not recorded on the second run")
	}
	if tokens <= 0 {
		t.Error("context_pack_tokens was not recorded on the second run")
	}
	if newSessionID != sessionID {
		t.Fatalf("second run's session_id = %q, want the SAME session %q", newSessionID, sessionID)
	}

	// The cursor advanced past what was actually shown.
	var lastConsumedAfter int
	if err := f.db.QueryRow(`SELECT last_consumed_seq FROM issue_agent_sessions WHERE id = ?`, sessionID).Scan(&lastConsumedAfter); err != nil {
		t.Fatalf("read last_consumed_seq after: %v", err)
	}
	if lastConsumedAfter <= lastConsumedBefore {
		t.Fatalf("last_consumed_seq did not advance: before=%d after=%d", lastConsumedBefore, lastConsumedAfter)
	}
}
