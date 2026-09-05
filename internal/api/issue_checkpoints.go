package api

// issue_checkpoints.go — agent_session_checkpoints read/write path
// (PRD-ISSUES-AND-ROUTINES-2026 §9.5, work package B5, #2345).
//
// writeSessionCheckpoint is called from finishAssignment (assignments_run.go)
// for every session-bearing run, regardless of terminal status — §11.3 says
// "a checkpoint at the end of every run", not only successful ones, and
// Parsed=false (orchestrator.ParseCheckpoint's own field) is exactly how a
// run that never emitted the block is told apart from one that did.
//
// latestCheckpointFor is the read side assembleContextPack (issue_context_pack.go)
// uses for §11.1 item 3 ("Latest checkpoint"), and ListCheckpoints is the
// human/CLI read path (GET .../sessions/{sessionId}/checkpoints), mirroring
// ListSessions' shape (issue_sessions.go).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// checkpointScrubber redacts secrets from checkpoint text before it is
// persisted — §16.1's "Scrub before persist" rule, named explicitly for
// "checkpoint bodies" alongside mission_activity.payload_json. Built once,
// same reasoning as judgeScrubber (keeper.go): the pattern set is fixed.
var checkpointScrubber = scrubber.New()

// writeSessionCheckpoint scrubs, marshals and inserts one
// agent_session_checkpoints row for a session-bearing run that just
// finished. Best-effort: called from finishAssignment's post-completion
// side effects, which must never fail (or retry-loop) the completion this
// is a side effect of — same contract as consumeDeliveriesForRun and
// dispatchQueuedFollowUpsForSession right next to it.
func writeSessionCheckpoint(ctx context.Context, db *sql.DB, workspaceID, sessionID, runID string, cp orchestrator.CheckpointData) error {
	if sessionID == "" {
		return nil
	}
	cp.Done = checkpointScrubber.Scrub(cp.Done)
	cp.Plan = checkpointScrubber.Scrub(cp.Plan)
	cp.Facts = checkpointScrubber.Scrub(cp.Facts)
	cp.Blockers = checkpointScrubber.Scrub(cp.Blockers)
	cp.NextStep = checkpointScrubber.Scrub(cp.NextStep)

	payload, err := json.Marshal(cp)
	if err != nil {
		return err
	}

	// seq_at_write: the mission's current mission_activity high-water mark,
	// via the session's own mission_id — purely informational bookkeeping
	// (§9.5 names the column but does not tie it to last_consumed_seq; the
	// context pack's own advance of that cursor happens at DISPATCH time,
	// in issue_context_pack.go, not here).
	var seqAtWrite int
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(a.seq), 0)
		  FROM mission_activity a
		  JOIN issue_agent_sessions s ON s.mission_id = a.mission_id
		 WHERE s.id = ?`, sessionID).Scan(&seqAtWrite); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	var runVal any
	if runID != "" {
		runVal = runID
	}
	var workspaceVal any
	if workspaceID != "" {
		workspaceVal = workspaceID
	} else {
		// The trigger requires NEW.workspace_id to equal the session's own
		// workspace_id (20260904213700's consistency trigger) — resolve it
		// from the session row rather than insert a value the caller left
		// blank, which would abort every call whose caller forgot to thread
		// workspaceID through.
		if err := db.QueryRowContext(ctx,
			`SELECT workspace_id FROM issue_agent_sessions WHERE id = ?`, sessionID,
		).Scan(&workspaceVal); err != nil {
			return err
		}
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO agent_session_checkpoints
		    (id, workspace_id, session_id, run_id, seq_at_write, checkpoint_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		generateCUID(), workspaceVal, sessionID, runVal, seqAtWrite, string(payload),
		time.Now().UTC().Format(time.RFC3339))
	return err
}

// recordSessionCheckpoint is finishAssignment's (assignments_run.go) call
// into the write path above: read the finished assignment's session_id (if
// any), parse the §9.5 checkpoint block out of its result text, and store
// it. Best-effort throughout — a read/parse/write failure here logs and
// returns, exactly like dispatchQueuedFollowUpsForSession right next to it
// in the same completion path, and for the same reason: this is a side
// effect of the completion that just landed, not a precondition for it.
func (h *AssignmentHandler) recordSessionCheckpoint(ctx context.Context, assignmentID, workspaceID, result string) {
	var sessionID sql.NullString
	if err := h.db.QueryRowContext(ctx,
		`SELECT session_id FROM assignments WHERE id = ?`, assignmentID,
	).Scan(&sessionID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			h.logger.Warn("record session checkpoint: read finished assignment",
				"error", err, "assignment_id", assignmentID)
		}
		return
	}
	if !sessionID.Valid || sessionID.String == "" {
		return
	}
	cp := orchestrator.ParseCheckpoint(result)
	if err := writeSessionCheckpoint(ctx, h.db, workspaceID, sessionID.String, assignmentID, cp); err != nil {
		h.logger.Warn("record session checkpoint: write",
			"error", err, "assignment_id", assignmentID, "session_id", sessionID.String)
		return
	}
	// B11 (§14.2, #2368): `issue.checkpoint.written` — unlike the two
	// session-state broadcasts, this write is NOT inside the same
	// transaction as anything else in this request (writeSessionCheckpoint
	// is its own committed statement), so there is no premature-visibility
	// risk in announcing it immediately after success.
	broadcastIssueCheckpointWritten(ctx, h.db, h.hub, workspaceID, sessionID.String)
}

// latestCheckpointFor returns the most recent checkpoint for a session, or
// ok=false when the session has none yet (a first-ever wake, or every prior
// run predates B5). created_at DESC — see the migration's index comment for
// why that, not seq_at_write, is the tie-breaker.
func latestCheckpointFor(ctx context.Context, db *sql.DB, sessionID string) (orchestrator.CheckpointData, bool, error) {
	if sessionID == "" {
		return orchestrator.CheckpointData{}, false, nil
	}
	var raw string
	err := db.QueryRowContext(ctx, `
		SELECT checkpoint_json FROM agent_session_checkpoints
		 WHERE session_id = ?
		 ORDER BY created_at DESC, rowid DESC
		 LIMIT 1`, sessionID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return orchestrator.CheckpointData{}, false, nil
	}
	if err != nil {
		return orchestrator.CheckpointData{}, false, err
	}
	var cp orchestrator.CheckpointData
	if err := json.Unmarshal([]byte(raw), &cp); err != nil {
		return orchestrator.CheckpointData{}, false, err
	}
	return cp, true, nil
}

// checkpointDTO is one row of a session's checkpoint history — the CLI's
// `issue checkpoints` command reads this.
type checkpointDTO struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	RunID      string `json:"run_id,omitempty"`
	SeqAtWrite int    `json:"seq_at_write"`
	Done       string `json:"done,omitempty"`
	Plan       string `json:"plan,omitempty"`
	Facts      string `json:"facts,omitempty"`
	Blockers   string `json:"blockers,omitempty"`
	NextStep   string `json:"next_step,omitempty"`
	Confidence string `json:"confidence,omitempty"`
	Parsed     bool   `json:"parsed"`
	CreatedAt  string `json:"created_at"`
}

// ListCheckpoints GET
// /api/v1/crews/{crewId}/issues/{identifier}/sessions/{sessionId}/checkpoints
//
// Every checkpoint this session has ever written, newest-first — the
// session's own resumable history, not just its latest cursor (ListSessions
// already reports last_consumed_seq; this is "what did it think it had
// done, and when").
func (h *IssueHandler) ListCheckpoints(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "read") {
		return
	}
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	sessionID := r.PathValue("sessionId")
	wsID := WorkspaceIDFromContext(r.Context())

	missionID, err := h.resolveMissionID(r.Context(), ident, crewID, wsID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "issue checkpoints: resolve mission", err)
		return
	}

	// The session must actually belong to this issue (and workspace) — a
	// session id from a different mission must 404, not leak another
	// issue's checkpoints through a guessed id.
	var sessionMissionID string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT mission_id FROM issue_agent_sessions WHERE id = ? AND workspace_id = ?`,
		sessionID, wsID).Scan(&sessionMissionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Session not found")
			return
		}
		internalError(w, r, h.logger, "issue checkpoints: resolve session", err)
		return
	}
	if sessionMissionID != missionID {
		writeProblem(w, r, http.StatusNotFound, "Session not found")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, session_id, COALESCE(run_id, ''), seq_at_write, checkpoint_json, created_at
		FROM agent_session_checkpoints
		WHERE session_id = ?
		ORDER BY created_at DESC, rowid DESC`, sessionID)
	if err != nil {
		internalError(w, r, h.logger, "issue checkpoints: query", err)
		return
	}
	defer rows.Close()

	out := []checkpointDTO{}
	for rows.Next() {
		var dto checkpointDTO
		var raw string
		if err := rows.Scan(&dto.ID, &dto.SessionID, &dto.RunID, &dto.SeqAtWrite, &raw, &dto.CreatedAt); err != nil {
			internalError(w, r, h.logger, "issue checkpoints: scan", err)
			return
		}
		var cp orchestrator.CheckpointData
		if err := json.Unmarshal([]byte(raw), &cp); err == nil {
			dto.Done = cp.Done
			dto.Plan = cp.Plan
			dto.Facts = cp.Facts
			dto.Blockers = cp.Blockers
			dto.NextStep = cp.NextStep
			dto.Confidence = cp.Confidence
			dto.Parsed = cp.Parsed
		}
		out = append(out, dto)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "issue checkpoints: rows", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
