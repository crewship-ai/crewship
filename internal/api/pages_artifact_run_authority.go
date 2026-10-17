package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Only trusted human-chat admission can create this immutable binding. Neither
// crew IPC nor general run metadata can nominate a human principal.
func captureArtifactChatPrincipal(r *http.Request, db *sql.DB, run, ws, agent, crew, chat, message string, receipt *chataudience.Receipt) error {
	if InternalTokenWorkspaceFromContext(r.Context()) != "" || InternalTokenCrewFromContext(r.Context()) != "" || receipt == nil || message == "" {
		return errors.New("host human admission required")
	}
	current, err := chataudience.CaptureTrusted(r.Context(), db, chat, receipt.UserID, receipt)
	if err != nil {
		return err
	}
	if current == nil || current.WorkspaceID != ws || current.AgentID != agent {
		return errors.New("human admission changed")
	}
	var n int
	if err = db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM conversation_messages WHERE id=? AND session_id=? AND agent_id=? AND author_user_id=? AND role='user'`, message, chat, agent, receipt.UserID).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return errors.New("human message missing")
	}
	_, err = db.ExecContext(r.Context(), `INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id) DO NOTHING`, run, ws, agent, crew, receipt.UserID, chat, message, receipt.MemberID, receipt.MemberRevision, receipt.ChatGeneration, receipt.ChatRevision, tsformat.Format(time.Now().UTC().Add(time.Hour)))
	if err != nil {
		return err
	}
	// An exact retry keeps its original expiry. A run cannot silently reuse
	// a different human, message or captured authority epoch.
	err = db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM artifact_run_principals WHERE run_id=? AND workspace_id=? AND agent_id=? AND crew_id=? AND human_user_id=? AND chat_id=? AND message_id=? AND member_id=? AND member_revision=? AND chat_generation=? AND chat_revision=?`, run, ws, agent, crew, receipt.UserID, chat, message, receipt.MemberID, receipt.MemberRevision, receipt.ChatGeneration, receipt.ChatRevision).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("immutable artifact principal binding changed")
	}
	return nil
}

type artifactRunAuthority struct{ WorkspaceID, AgentID, CrewID, RunID, HumanUserID, ChatID, MessageID, RequestID string }
type artifactRunKey struct{}

// Checked again through pageAuthReader after the publication's SQLite write
// fence. Journal terminal commits and ledger generation changes serialize with it.
func (h *PageHandler) checkArtifactRunPrincipal(ctx context.Context, a *artifactRunAuthority) error {
	q := h.pageAuthReader(ctx)
	var liveHuman int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_run_principals p JOIN users human ON human.id=p.human_user_id AND human.suspended_at IS NULL JOIN workspace_members wm ON wm.workspace_id=p.workspace_id AND wm.user_id=p.human_user_id AND wm.id=p.member_id AND wm.access_revision=p.member_revision JOIN chats c ON c.id=p.chat_id AND c.workspace_id=p.workspace_id AND c.agent_id=p.agent_id AND c.authority_generation=p.chat_generation AND c.authority_revision=p.chat_revision WHERE p.run_id=? AND NOT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=p.human_user_id AND access_mode='restricted')`, a.RunID).Scan(&liveHuman); err != nil || liveHuman != 1 {
		return errors.New("human chat authority changed")
	}

	var expiry string
	if err := q.QueryRowContext(ctx, `SELECT p.expires_at FROM artifact_run_principals p JOIN workspaces w ON w.id=p.workspace_id AND w.deleted_at IS NULL JOIN crews cr ON cr.id=p.crew_id AND cr.workspace_id=p.workspace_id AND cr.deleted_at IS NULL JOIN agents ag ON ag.id=p.agent_id AND ag.workspace_id=p.workspace_id AND ag.crew_id=p.crew_id AND ag.deleted_at IS NULL AND ag.expired_at IS NULL WHERE p.run_id=? AND p.workspace_id=? AND p.agent_id=? AND p.crew_id=? AND p.human_user_id=?`, a.RunID, a.WorkspaceID, a.AgentID, a.CrewID, a.HumanUserID).Scan(&expiry); err != nil {
		return errors.New("artifact principal missing")
	}
	limit, e := time.Parse(time.RFC3339Nano, expiry) // tsformat:allow: parsing a stored expiry for time comparison; no timestamp is written
	if e != nil || !time.Now().Before(limit) {
		return errors.New("artifact principal expired")
	}
	var started, ended int
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN entry_type='run.started' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop') THEN 1 ELSE 0 END),0) FROM journal_entries WHERE workspace_id=? AND agent_id=? AND trace_id=?`, a.WorkspaceID, a.AgentID, a.RunID).Scan(&started, &ended)
	if err != nil || started == 0 || ended != 0 {
		return errors.New("artifact run is not live")
	}
	var state, itemWS, itemAgent, itemCrew, source, ledgerHuman string
	var deadline sql.NullString
	var generation, attemptGeneration int64
	var endedAt sql.NullString
	err = q.QueryRowContext(ctx, `SELECT w.state,w.generation,at.generation,at.ended_at,w.workspace_id,w.agent_id,w.crew_id,w.source,w.authorized_by_user_id,w.deadline_at FROM work_attempts at JOIN work_items w ON w.id=at.work_id WHERE at.run_id=?`, a.RunID).Scan(&state, &generation, &attemptGeneration, &endedAt, &itemWS, &itemAgent, &itemCrew, &source, &ledgerHuman, &deadline)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && deadline.Valid {
		limit, e := time.Parse(time.RFC3339Nano, deadline.String)
		if e != nil || !time.Now().Before(limit) {
			return errors.New("artifact work deadline expired")
		}
	}
	if err == nil && ((source != "chat" && source != "assignment") || (ledgerHuman != "" && ledgerHuman != a.HumanUserID) || itemWS != a.WorkspaceID || itemAgent != a.AgentID || itemCrew != a.CrewID || !memoryRunStateIsLive(state) || generation != attemptGeneration || endedAt.Valid) {
		return errors.New("artifact work attempt is not current")
	}
	return nil
}
