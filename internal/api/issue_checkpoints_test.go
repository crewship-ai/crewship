package api

// Tests for issue_checkpoints.go — the agent_session_checkpoints read/write
// path (PRD-ISSUES-AND-ROUTINES-2026 §9.5, work package B5, #2345).

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

func TestWriteSessionCheckpoint_AndLatestCheckpointFor_RoundTrip(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "TODO")
	_ = userID

	sessionID, err := resolveOrCreateIssueAgentSessionTx(context.Background(), db, wsID, missionID, leadID)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	cp := orchestrator.CheckpointData{
		Done:       "implemented the parser",
		Plan:       "wire it into dispatch",
		NextStep:   "add the acceptance test",
		Confidence: "high",
		Parsed:     true,
	}
	if err := writeSessionCheckpoint(context.Background(), db, wsID, sessionID, "run-1", cp); err != nil {
		t.Fatalf("writeSessionCheckpoint: %v", err)
	}

	got, ok, err := latestCheckpointFor(context.Background(), db, sessionID)
	if err != nil {
		t.Fatalf("latestCheckpointFor: %v", err)
	}
	if !ok {
		t.Fatal("expected a checkpoint, found none")
	}
	if got.Done != cp.Done || got.NextStep != cp.NextStep || got.Confidence != cp.Confidence {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, cp)
	}
	if !got.Parsed {
		t.Error("expected Parsed=true to round-trip")
	}
}

func TestLatestCheckpointFor_NoCheckpoints_ReturnsNotFound(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "TODO")
	sessionID, err := resolveOrCreateIssueAgentSessionTx(context.Background(), db, wsID, missionID, leadID)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	_, ok, err := latestCheckpointFor(context.Background(), db, sessionID)
	if err != nil {
		t.Fatalf("latestCheckpointFor: %v", err)
	}
	if ok {
		t.Fatal("expected no checkpoint for a fresh session")
	}
}

func TestLatestCheckpointFor_MultipleWrites_ReturnsNewest(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "TODO")
	sessionID, err := resolveOrCreateIssueAgentSessionTx(context.Background(), db, wsID, missionID, leadID)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	if err := writeSessionCheckpoint(context.Background(), db, wsID, sessionID, "run-1",
		orchestrator.CheckpointData{Done: "first pass", NextStep: "x", Parsed: true}); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if err := writeSessionCheckpoint(context.Background(), db, wsID, sessionID, "run-2",
		orchestrator.CheckpointData{Done: "second pass, superseding the first", NextStep: "y", Parsed: true}); err != nil {
		t.Fatalf("write 2: %v", err)
	}

	got, ok, err := latestCheckpointFor(context.Background(), db, sessionID)
	if err != nil || !ok {
		t.Fatalf("latestCheckpointFor: ok=%v err=%v", ok, err)
	}
	if got.Done != "second pass, superseding the first" {
		t.Errorf("Done = %q, want the SECOND (newest) write", got.Done)
	}
}

// TestWriteSessionCheckpoint_ScrubsSecrets pins §16.1's "scrub before
// persist" rule, named explicitly for checkpoint bodies alongside
// mission_activity.payload_json.
func TestWriteSessionCheckpoint_ScrubsSecrets(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "TODO")
	sessionID, err := resolveOrCreateIssueAgentSessionTx(context.Background(), db, wsID, missionID, leadID)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	const secret = "ghp_16C7e42F292c6912E7710c838347Ae178B4a" //gitleaks:allow — fabricated, shaped like the real thing so the scrubber engages
	cp := orchestrator.CheckpointData{
		Done:     "stored the token " + secret + " in the credential vault",
		NextStep: "done",
		Parsed:   true,
	}
	if err := writeSessionCheckpoint(context.Background(), db, wsID, sessionID, "run-1", cp); err != nil {
		t.Fatalf("writeSessionCheckpoint: %v", err)
	}

	var raw string
	if err := db.QueryRow(`SELECT checkpoint_json FROM agent_session_checkpoints WHERE session_id = ?`, sessionID).Scan(&raw); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	if strings.Contains(raw, secret) {
		t.Fatalf("checkpoint_json still contains the raw secret: %s", raw)
	}
}

func TestRecordSessionCheckpoint_NoSession_IsNoOp(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, workerID := seedIssueFixtures(t, db)
	missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "TODO")
	_ = userID

	if err := ensureMissionChat(context.Background(), db, missionID, wsID, leadID, "Test issue"); err != nil {
		t.Fatalf("ensure mission chat: %v", err)
	}
	assignmentID := generateCUID()
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at, mission_id)
		VALUES (?, ?, ?, ?, ?, 'do the thing', 'COMPLETED', datetime('now'), ?)`,
		assignmentID, wsID, missionID, leadID, workerID, missionID); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	assign := NewAssignmentHandler(db, nil, nil, "token", newTestLogger())
	assign.recordSessionCheckpoint(context.Background(), assignmentID, wsID, "no checkpoint block here")

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_session_checkpoints`).Scan(&n); err != nil {
		t.Fatalf("count checkpoints: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected no checkpoint written for a session-less assignment, got %d", n)
	}
}

func TestRecordSessionCheckpoint_ParsesBlockFromResult_AndRecordsUnparsedWhenMissing(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID, leadID, workerID := seedIssueFixtures(t, db)
	missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "TODO")
	sessionID, err := resolveOrCreateIssueAgentSessionTx(context.Background(), db, wsID, missionID, leadID)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	assign := NewAssignmentHandler(db, nil, nil, "token", newTestLogger())
	if err := ensureMissionChat(context.Background(), db, missionID, wsID, leadID, "Test issue"); err != nil {
		t.Fatalf("ensure mission chat: %v", err)
	}

	// Run 1: a well-formed checkpoint block.
	a1 := generateCUID()
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at, mission_id, session_id)
		VALUES (?, ?, ?, ?, ?, 'do the thing', 'COMPLETED', datetime('now'), ?, ?)`,
		a1, wsID, missionID, leadID, workerID, missionID, sessionID); err != nil {
		t.Fatalf("seed assignment 1: %v", err)
	}
	result1 := "Finished up.\n\n---CHECKPOINT---\ndone: shipped the feature\nnext_step: none\nconfidence: high\n---END CHECKPOINT---"
	assign.recordSessionCheckpoint(context.Background(), a1, wsID, result1)

	cp, ok, err := latestCheckpointFor(context.Background(), db, sessionID)
	if err != nil || !ok {
		t.Fatalf("latestCheckpointFor after run 1: ok=%v err=%v", ok, err)
	}
	if !cp.Parsed || cp.Done != "shipped the feature" {
		t.Fatalf("expected a parsed checkpoint from run 1, got %+v", cp)
	}

	// Run 2: no checkpoint block at all — §11.3 says this must be recorded
	// (Parsed=false), not silently skipped or left as run 1's stale state.
	a2 := generateCUID()
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at, mission_id, session_id)
		VALUES (?, ?, ?, ?, ?, 'do more', 'FAILED', datetime('now'), ?, ?)`,
		a2, wsID, missionID, leadID, workerID, missionID, sessionID); err != nil {
		t.Fatalf("seed assignment 2: %v", err)
	}
	assign.recordSessionCheckpoint(context.Background(), a2, wsID, "the run crashed with no structured output")

	cp2, ok, err := latestCheckpointFor(context.Background(), db, sessionID)
	if err != nil || !ok {
		t.Fatalf("latestCheckpointFor after run 2: ok=%v err=%v", ok, err)
	}
	if cp2.Parsed {
		t.Fatalf("expected the NEWEST checkpoint (run 2) to be Parsed=false, got %+v", cp2)
	}
}

func TestListCheckpoints_HTTP_NewestFirst_AndScopedToItsOwnIssue(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "TODO")
	otherMissionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-2", "TODO")

	sessionID, err := resolveOrCreateIssueAgentSessionTx(context.Background(), db, wsID, missionID, leadID)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := writeSessionCheckpoint(context.Background(), db, wsID, sessionID, "run-1",
		orchestrator.CheckpointData{Done: "older", NextStep: "x", Parsed: true}); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if err := writeSessionCheckpoint(context.Background(), db, wsID, sessionID, "run-2",
		orchestrator.CheckpointData{Done: "newer", NextStep: "y", Parsed: true}); err != nil {
		t.Fatalf("write 2: %v", err)
	}

	issues := NewIssueHandler(db, nil, nil, newTestLogger())

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/issues/ENG-1/sessions/"+sessionID+"/checkpoints", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-1")
	req.SetPathValue("sessionId", sessionID)
	rr := httptest.NewRecorder()
	issues.ListCheckpoints(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var got []checkpointDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Done != "newer" || got[1].Done != "older" {
		t.Fatalf("order = [%q, %q], want [newer, older]", got[0].Done, got[1].Done)
	}

	// A session id that belongs to a DIFFERENT issue must 404, not leak
	// this session's checkpoints through a mismatched identifier.
	req2 := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/issues/ENG-2/sessions/"+sessionID+"/checkpoints", nil),
		userID, wsID, "OWNER",
	)
	req2.SetPathValue("crewId", crewID)
	req2.SetPathValue("identifier", "ENG-2")
	req2.SetPathValue("sessionId", sessionID)
	rr2 := httptest.NewRecorder()
	issues.ListCheckpoints(rr2, req2)
	if rr2.Code != 404 {
		t.Fatalf("cross-issue session lookup status = %d, want 404", rr2.Code)
	}
	_ = otherMissionID
}
