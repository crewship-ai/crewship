package api

// issue_session_realtime.go — the §14.2 "New:" realtime types B4/B5/B6 left
// unwired (work package B11, #2368): `issue.session.state`,
// `issue.checkpoint.written`, and `run.outcome`.
//
// Before this file, a session's pending->active->idle/error/awaiting_input
// walk (issue_session_state.go, B4) and a run's outcome (assignments_run.go's
// finishAssignment, B6) were DB writes only — nothing told an open board.
// The session panel and the outcome column were correct on next reload and
// silent until then, which is exactly the "shipped surface that never
// repaints" F32/F43 name. §10.1's own reconciliation sweepers
// (ReconcileExpiredEphemeralSessions, ReconcileStaleActiveSessions) are
// DELIBERATELY left as DB-only best-effort self-heals here — they already
// document themselves as "the board catches up within one tick" and adding
// a broadcast to a bulk UPDATE would mean a second query per affected row
// for marginal benefit on a path that is, by design, the rare backstop
// rather than the common case.

import (
	"context"
	"database/sql"

	"github.com/crewship-ai/crewship/internal/ws"
)

// broadcastIssueSessionState announces a session's own state transition on
// the workspace channel — `issue.session.state`, part of golden scenario's
// "the board moves without refresh for ... session state" (§17 B11 accept
// line). Best-effort and silent on any lookup failure, matching every
// other post-write broadcast in this package: the state transition itself
// already landed by the time this runs, and a missed nudge degrades to
// "stale until reload", not a correctness bug.
func broadcastIssueSessionState(ctx context.Context, db *sql.DB, hub *ws.Hub, workspaceID, sessionID, state string) {
	if hub == nil || sessionID == "" {
		return
	}
	var missionID, agentID string
	var identifier sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT s.mission_id, s.agent_id, m.identifier
		  FROM issue_agent_sessions s
		  JOIN missions m ON m.id = s.mission_id
		 WHERE s.id = ?`, sessionID).Scan(&missionID, &agentID, &identifier); err != nil {
		return
	}
	broadcastWorkspaceEvent(hub, workspaceID, "issue.session.state", map[string]string{
		"mission_id": missionID, "identifier": identifier.String,
		"session_id": sessionID, "agent_id": agentID, "state": state,
	})
}

// broadcastIssueCheckpointWritten announces `issue.checkpoint.written` —
// §11.1's "unread delta" gains a new anchor point every time a session
// checkpoints, and a board panel watching the session can refresh its
// "last checkpoint" line without polling.
func broadcastIssueCheckpointWritten(ctx context.Context, db *sql.DB, hub *ws.Hub, workspaceID, sessionID string) {
	if hub == nil || sessionID == "" {
		return
	}
	var missionID, agentID string
	var identifier sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT s.mission_id, s.agent_id, m.identifier
		  FROM issue_agent_sessions s
		  JOIN missions m ON m.id = s.mission_id
		 WHERE s.id = ?`, sessionID).Scan(&missionID, &agentID, &identifier); err != nil {
		return
	}
	broadcastWorkspaceEvent(hub, workspaceID, "issue.checkpoint.written", map[string]string{
		"mission_id": missionID, "identifier": identifier.String,
		"session_id": sessionID, "agent_id": agentID,
	})
}

// broadcastRunOutcome announces `run.outcome` — the §9.6 routing decision,
// the last of the five signals golden scenario's accept line names ("the
// board moves without refresh for create, status change, comment, session
// state and outcome"). A no-op for a run with no mission_id (a root
// /assign not attributed to any issue) — there is no board to repaint.
func broadcastRunOutcome(ctx context.Context, db *sql.DB, hub *ws.Hub, workspaceID, assignmentID, status, outcome string) {
	if hub == nil {
		return
	}
	var missionID sql.NullString
	var identifier sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT a.mission_id, m.identifier
		  FROM assignments a
		  LEFT JOIN missions m ON m.id = a.mission_id
		 WHERE a.id = ?`, assignmentID).Scan(&missionID, &identifier); err != nil {
		return
	}
	if !missionID.Valid || missionID.String == "" {
		return
	}
	broadcastWorkspaceEvent(hub, workspaceID, "run.outcome", map[string]string{
		"mission_id": missionID.String, "identifier": identifier.String,
		"assignment_id": assignmentID, "status": status, "outcome": outcome,
	})
}
