package api

// #2269 follow-up, defects 2 and 3.
//
// dispatchByID (assignments_dispatch_pump.go) reconstructs a QUEUED
// assignment's dispatch entirely from the `assignments` row — it is the ONLY
// input the completion-path pump has once a row has left PENDING. Before
// this fix it silently dropped three things the ORIGINAL dispatch had:
//
//   - MissionID, wrongly re-derived from group_id (defect 2). group_id is
//     NOT always a mission id: Create's /assign door sets group_id=chat_id,
//     which has no mission at all. A requeued /assign row would resurface
//     with a fabricated MissionID pointing at a chat.
//   - AuthorAgentID / CreatedByUserID (defect 2), never read at all — a
//     requeued @mention row lost its creator attribution on re-dispatch.
//   - LeadPlanning, hard-coded to false (defect 3) — a requeued lead's own
//     planning turn would re-run as a plain AGENT, silently dropping its
//     sidecar mid-mission.
//
// The 20260901221102 migration adds mission_id/author_agent_id/
// created_by_user_id/lead_planning columns; insertCappedAssignment and the
// two mission-engine raw INSERTs now persist them; dispatchByID now reads
// them back instead of guessing or hard-coding.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// TestDispatchByID_RequeuedMentionRow_PreservesAuthorAttribution proves a
// row shaped like a requeued @mention dispatch (group_id == mission_id,
// author_agent_id set — DispatchMention's shape) carries its attribution
// through dispatchByID into the run.started journal entry.
func TestDispatchByID_RequeuedMentionRow_PreservesAuthorAttribution(t *testing.T) {
	t.Parallel()
	h, db, crewID, agentIDs, chatID := dispatchPumpRig(t)
	rec := &recordingEmitter{}
	h.SetJournal(rec)

	const aid = "a_mention_requeue"
	authorID := agentIDs[2] // a distinct agent id — the mention's author, not the target
	// mission_id is a real foreign key since #2279, so the mention must point
	// at a mission that exists — the same shape DispatchMention leaves behind.
	const missionID = "m_mention_requeue"
	if _, err := db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, 'test-workspace-id', ?, ?, 'trace-m_mention_requeue', 'M', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		missionID, crewID, agentIDs[0]); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status,
		                         group_id, mission_id, author_agent_id, created_at)
		VALUES (?, 'test-workspace-id', ?, ?, ?, 'do the thing', 'QUEUED', ?, ?, ?, datetime('now'))`,
		aid, chatID, authorID, agentIDs[0], missionID, missionID, authorID); err != nil {
		t.Fatalf("seed mention row: %v", err)
	}

	if err := h.dispatchByID(context.Background(), aid); err != nil {
		t.Fatalf("dispatchByID: %v", err)
	}

	entry := findRunStartedEntry(t, rec, aid)
	if got, _ := entry.Payload["author_agent_id"].(string); got != authorID {
		t.Errorf("run.started author_agent_id = %q, want %q — attribution was lost on re-dispatch", got, authorID)
	}
	if got := entry.MissionID; got != missionID {
		t.Errorf("run.started mission_id = %q, want %q", got, missionID)
	}
}

// TestDispatchByID_RequeuedAssignRow_DoesNotInventMissionIDFromGroupID
// proves a row shaped like a requeued Create (/assign) dispatch — where
// group_id is a CHAT id (Create's own choice, assignments_run.go) and
// mission_id is genuinely NULL — does not resurface with group_id
// masquerading as a mission id.
func TestDispatchByID_RequeuedAssignRow_DoesNotInventMissionIDFromGroupID(t *testing.T) {
	t.Parallel()
	h, db, _, agentIDs, chatID := dispatchPumpRig(t)
	rec := &recordingEmitter{}
	h.SetJournal(rec)

	const aid = "a_assign_requeue"
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status,
		                         group_id, mission_id, created_at)
		VALUES (?, 'test-workspace-id', ?, ?, ?, 'do the thing', 'QUEUED', ?, NULL, datetime('now'))`,
		aid, chatID, agentIDs[1], agentIDs[0], chatID /* group_id = chat_id, Create's own shape */); err != nil {
		t.Fatalf("seed assign row: %v", err)
	}

	if err := h.dispatchByID(context.Background(), aid); err != nil {
		t.Fatalf("dispatchByID: %v", err)
	}

	entry := findRunStartedEntry(t, rec, aid)
	if entry.MissionID != "" {
		t.Errorf("run.started mission_id = %q, want empty — group_id (a chat id here) must not be "+
			"reused as a mission id", entry.MissionID)
	}
}

// TestDispatchByID_RequeuedLeadPlanningRow_PreservesFlag proves a row
// shaped like a requeued lead-planning dispatch (lead_planning=1, the shape
// mission_tasks_planning.go's INSERT now writes) has that flag read back
// rather than hard-coded to false. Observed via the "dispatching queued
// assignment" log line's lead_planning field: with h.orch=nil (this rig's
// determinism device) the run short-circuits before reaching the
// AgentRole/SkipSidecar branch runAssignment derives LeadPlanning into, so
// the row->body reconstruction dispatchByID owns — the actual locus of this
// bug — is what's observable here; runAssignment's own consumption of
// LeadPlanning is pre-existing, unit-tested elsewhere, and unchanged by
// this fix.
func TestDispatchByID_RequeuedLeadPlanningRow_PreservesFlag(t *testing.T) {
	t.Parallel()
	h, db, _, agentIDs, chatID := dispatchPumpRig(t)
	var logBuf bytes.Buffer
	h.logger = slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	const aid = "a_lead_requeue"
	if _, err := db.Exec(`
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status,
		                         lead_planning, created_at)
		VALUES (?, 'test-workspace-id', ?, ?, ?, '[PLANNING] plan it', 'PENDING', 1, datetime('now'))`,
		aid, chatID, agentIDs[0], agentIDs[0]); err != nil {
		t.Fatalf("seed lead-planning row: %v", err)
	}

	if err := h.dispatchByID(context.Background(), aid); err != nil {
		t.Fatalf("dispatchByID: %v", err)
	}

	if logs := logBuf.String(); !strings.Contains(logs, `"lead_planning":true`) {
		t.Fatalf("dispatchByID did not preserve lead_planning=true from the row into the reconstructed "+
			"dispatch; log output:\n%s", logs)
	}
}

// findRunStartedEntry returns the run.started entry whose Refs names
// assignmentID, failing the test if none was emitted.
func findRunStartedEntry(t *testing.T, rec *recordingEmitter, assignmentID string) journal.Entry {
	t.Helper()
	for _, e := range rec.entries {
		if e.Type != journal.EntryRunStarted {
			continue
		}
		if refID, _ := e.Refs["assignment_id"].(string); refID == assignmentID {
			return e
		}
	}
	t.Fatalf("no run.started entry found for assignment %s (got %d entries)", assignmentID, len(rec.entries))
	return journal.Entry{}
}
