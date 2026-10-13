package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/journal"
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
	return err
}

type artifactRunAuthority struct{ WorkspaceID, AgentID, CrewID, RunID, HumanUserID, ChatID, MessageID string }
type artifactRunKey struct{}

// Checked again through pageAuthReader after the publication's SQLite write
// fence. Journal terminal commits and ledger generation changes serialize with it.
func (h *PageHandler) checkArtifactRun(ctx context.Context, pageID string, a *artifactRunAuthority) error {
	q := h.pageAuthReader(ctx)
	var liveHuman int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_run_principals p JOIN workspace_members wm ON wm.workspace_id=p.workspace_id AND wm.user_id=p.human_user_id AND wm.id=p.member_id AND wm.access_revision=p.member_revision JOIN chats c ON c.id=p.chat_id AND c.workspace_id=p.workspace_id AND c.agent_id=p.agent_id AND c.authority_generation=p.chat_generation AND c.authority_revision=p.chat_revision WHERE p.run_id=? AND NOT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=p.human_user_id AND access_mode='restricted')`, a.RunID).Scan(&liveHuman); err != nil || liveHuman != 1 {
		return errors.New("human chat authority changed")
	}

	var expires, delegationExpires string
	var revoked sql.NullString
	err := q.QueryRowContext(ctx, `SELECT p.expires_at,d.expires_at,d.revoked_at FROM artifact_run_principals p JOIN page_artifact_run_delegations d ON d.run_id=p.run_id AND d.page_id=? AND d.granted_by_user_id=p.human_user_id JOIN agents ag ON ag.id=p.agent_id AND ag.workspace_id=p.workspace_id AND ag.crew_id=p.crew_id AND ag.deleted_at IS NULL WHERE p.run_id=? AND p.workspace_id=? AND p.agent_id=? AND p.crew_id=? AND p.human_user_id=?`, pageID, a.RunID, a.WorkspaceID, a.AgentID, a.CrewID, a.HumanUserID).Scan(&expires, &delegationExpires, &revoked)
	if err != nil {
		return errors.New("artifact run delegation missing")
	}
	now := time.Now()
	for _, v := range []string{expires, delegationExpires} {
		t, e := time.Parse(time.RFC3339Nano, v)
		if e != nil || !now.Before(t) {
			return errors.New("artifact run delegation expired")
		}
	}
	if revoked.Valid {
		return errors.New("artifact run delegation revoked")
	}
	var started, ended int
	err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN entry_type='run.started' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop') THEN 1 ELSE 0 END),0) FROM journal_entries WHERE workspace_id=? AND agent_id=? AND trace_id=?`, a.WorkspaceID, a.AgentID, a.RunID).Scan(&started, &ended)
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
	// A delegation is explicit but it does not bypass Pages' agent grant.
	var slug string
	if err = q.QueryRowContext(ctx, `SELECT slug FROM pages WHERE id=? AND workspace_id=?`, pageID, a.WorkspaceID).Scan(&slug); err != nil {
		return err
	}
	rec, err := h.loadPage(ctx, a.WorkspaceID, slug)
	if err != nil {
		return err
	}
	if !h.agentMayEditSpec(ctx, a.WorkspaceID, rec, a.AgentID) {
		return errors.New("agent page write grant required")
	}
	return nil
}

// The same writer fence used by publication orders grants/revocation and live ACL changes.
func (h *PageHandler) fencedArtifactOwner(w http.ResponseWriter, r *http.Request) (*sql.Tx, *http.Request, *pageRecord, bool) {
	rec, ok := h.artifactPage(w, r, true)
	if !ok {
		return nil, r, nil, false
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin artifact delegation", err)
		return nil, r, nil, false
	}
	failed := func() (*sql.Tx, *http.Request, *pageRecord, bool) { tx.Rollback(); return nil, r, nil, false }
	if _, err = tx.ExecContext(r.Context(), `UPDATE pages SET updated_at=updated_at WHERE id=? AND workspace_id=?`, rec.ID, WorkspaceIDFromContext(r.Context())); err != nil {
		replyInternalError(w, h.logger, "fence artifact delegation", err)
		return failed()
	}
	var role string
	if err = tx.QueryRowContext(r.Context(), `SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID).Scan(&role); err != nil {
		replyError(w, 403, "workspace membership required")
		return failed()
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	ctx = context.WithValue(ctx, ctxRole, role)
	r = r.WithContext(ctx)
	rec, ok = h.artifactPage(w, r, true)
	if !ok {
		return failed()
	}
	if !h.isPageOwner(ctx, WorkspaceIDFromContext(ctx), UserFromContext(ctx).ID, rec) {
		replyError(w, 403, "only the human page owner may manage publication delegation")
		return failed()
	}
	return tx, r, rec, true
}
func (h *PageHandler) GrantArtifactRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RunID string `json:"run_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.RunID == "" {
		replyError(w, 400, "run_id required")
		return
	}
	tx, r, rec, ok := h.fencedArtifactOwner(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	var a artifactRunAuthority
	var expires string
	err := tx.QueryRowContext(r.Context(), `SELECT workspace_id,agent_id,crew_id,run_id,human_user_id,chat_id,message_id,expires_at FROM artifact_run_principals WHERE run_id=? AND workspace_id=? AND human_user_id=?`, body.RunID, WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID).Scan(&a.WorkspaceID, &a.AgentID, &a.CrewID, &a.RunID, &a.HumanUserID, &a.ChatID, &a.MessageID, &expires)
	if err != nil {
		replyError(w, 403, "verified human chat run required")
		return
	}
	if !h.agentMayEditSpec(r.Context(), a.WorkspaceID, rec, a.AgentID) {
		replyError(w, 403, "grant this agent page write access first")
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO page_artifact_run_delegations(page_id,run_id,granted_by_user_id,expires_at,granted_at) VALUES(?,?,?,?,?) ON CONFLICT(page_id,run_id) DO NOTHING`, rec.ID, a.RunID, a.HumanUserID, expires, tsformat.Format(time.Now()))
	if err != nil {
		replyInternalError(w, h.logger, "grant artifact run", err)
		return
	}
	if err = h.checkArtifactRun(r.Context(), rec.ID, &a); err != nil {
		replyError(w, 403, err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit artifact delegation", err)
		return
	}
	writeJSON(w, 201, map[string]any{"run_id": a.RunID, "expires_at": expires})
}
func (h *PageHandler) RevokeArtifactRun(w http.ResponseWriter, r *http.Request) {
	tx, r, rec, ok := h.fencedArtifactOwner(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	_, err := tx.ExecContext(r.Context(), `UPDATE page_artifact_run_delegations SET revoked_at=?,revoked_by_user_id=? WHERE page_id=? AND run_id=?`, tsformat.Format(time.Now().UTC()), UserFromContext(r.Context()).ID, rec.ID, r.PathValue("runId"))
	if err != nil {
		replyInternalError(w, h.logger, "revoke artifact run", err)
		return
	}
	if err = tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit artifact revocation", err)
		return
	}
	w.WriteHeader(204)
}
func (h *PageHandler) artifactAgentRequest(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	ws, crew := InternalTokenWorkspaceFromContext(r.Context()), InternalTokenCrewFromContext(r.Context())
	fields := strings.Fields(r.Header.Get("Authorization"))
	if ws == "" || crew == "" || h.artifactMasterToken == "" || len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		replyError(w, 403, "crew transport and run bearer required")
		return r, false
	}
	tw, agent, run, valid := internaltoken.ValidateAgentRunToken(internaltoken.DeriveAgentRunKey(h.artifactMasterToken, ws, crew), fields[1])
	if !valid || tw != ws {
		replyError(w, 403, "invalid run capability")
		return r, false
	}
	a := &artifactRunAuthority{WorkspaceID: ws, CrewID: crew, AgentID: agent, RunID: run}
	var role string
	err := h.db.QueryRowContext(r.Context(), `SELECT p.human_user_id,p.chat_id,p.message_id,m.role FROM artifact_run_principals p JOIN workspace_members m ON m.workspace_id=p.workspace_id AND m.user_id=p.human_user_id WHERE p.run_id=? AND p.workspace_id=? AND p.agent_id=? AND p.crew_id=?`, run, ws, agent, crew).Scan(&a.HumanUserID, &a.ChatID, &a.MessageID, &role)
	if err != nil {
		replyError(w, 403, "no_human_principal")
		return r, false
	}
	ctx := context.WithValue(r.Context(), artifactRunKey{}, a)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: a.HumanUserID})
	ctx = context.WithValue(ctx, ctxWorkspaceID, ws)
	ctx = context.WithValue(ctx, ctxRole, role)
	return r.WithContext(ctx), true
}

func (h *PageHandler) PublishArtifactAgent(w http.ResponseWriter, r *http.Request) {
	if checked, ok := h.artifactAgentRequest(w, r); ok {
		h.writeArtifact(w, checked, false)
	}
}
func (h *PageHandler) GetArtifactAgent(w http.ResponseWriter, r *http.Request) {
	if checked, ok := h.artifactAgentRequest(w, r); ok {
		h.GetArtifact(w, checked)
	}
}

func artifactAgentID(r *http.Request) string {
	if a, ok := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority); ok {
		return a.AgentID
	}
	return ""
}
func artifactRunID(r *http.Request) string {
	if a, ok := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority); ok {
		return a.RunID
	}
	return ""
}

func artifactActorType(r *http.Request) journal.ActorType {
	if artifactAgentID(r) != "" {
		return journal.ActorAgent
	}
	return journal.ActorUser
}
func artifactActorID(r *http.Request, human string) string {
	if id := artifactAgentID(r); id != "" {
		return id
	}
	return human
}
