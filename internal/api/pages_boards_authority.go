package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func (h *PageHandler) checkBoardRun(ctx context.Context, rec *pageRecord, a *artifactRunAuthority) error {
	if err := h.checkArtifactRunPrincipal(ctx, a); err != nil {
		return err
	}
	if err := h.checkDocumentRunPageScope(ctx, rec.ID, a); err != nil {
		return err
	}
	if !rec.HasBoard || rec.OwnerUserID != a.HumanUserID || rec.OwnerCrewID != "" {
		return errors.New("board requires explicit current human owner authority")
	}
	var expiry string
	var revoked sql.NullString
	if err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT expires_at,revoked_at FROM page_board_run_delegations WHERE board_page_id=? AND run_id=? AND granted_by_user_id=?`, rec.ID, a.RunID, a.HumanUserID).Scan(&expiry, &revoked); err != nil {
		return errors.New("board run delegation required")
	}
	deadline, err := time.Parse(time.RFC3339Nano, expiry) // tsformat:allow: comparing stored authority expiry; no timestamp write
	if err != nil || !time.Now().Before(deadline) || revoked.Valid {
		return errors.New("board run delegation expired or revoked")
	}
	if !h.agentMayEditSpec(ctx, a.WorkspaceID, rec, a.AgentID) {
		return errors.New("agent page write grant required")
	}
	return nil
}
func (h *PageHandler) GrantBoardRun(w http.ResponseWriter, r *http.Request) {
	if agent, _ := pageGrantCallerIsAgent(r); agent || projectAgentFrom(r.Context()) != nil {
		replyError(w, 401, "human authentication required")
		return
	}
	var req struct {
		RunID string `json:"run_id"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if _, ok := boardDecode(w, r, &req); !ok {
		return
	}
	if !boardMutationValid(req.RunID) {
		replyError(w, 400, "run_id required")
		return
	}
	tx, r, rec, ok := h.boardTransaction(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	if !rec.HasBoard {
		replyError(w, 404, "board not found")
		return
	}
	user := UserFromContext(r.Context()).ID
	if rec.OwnerUserID != user {
		replyError(w, 403, "only the human board owner may delegate a run")
		return
	}
	var a artifactRunAuthority
	var expires string
	if err := tx.QueryRowContext(r.Context(), `SELECT workspace_id,agent_id,crew_id,run_id,human_user_id,chat_id,message_id,expires_at FROM artifact_run_principals WHERE run_id=? AND workspace_id=? AND human_user_id=?`, req.RunID, WorkspaceIDFromContext(r.Context()), user).Scan(&a.WorkspaceID, &a.AgentID, &a.CrewID, &a.RunID, &a.HumanUserID, &a.ChatID, &a.MessageID, &expires); err != nil {
		replyError(w, 403, "verified human chat run required")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO page_board_run_delegations(board_page_id,run_id,granted_by_user_id,granted_at,expires_at) VALUES(?,?,?,?,?) ON CONFLICT(board_page_id,run_id) DO NOTHING`, rec.ID, a.RunID, user, tsformat.Format(time.Now()), expires); err != nil {
		h.boardError(w, err)
		return
	}
	if err := h.checkBoardRun(r.Context(), rec, &a); err != nil {
		replyError(w, 403, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		h.boardError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"run_id": a.RunID, "agent_id": a.AgentID, "expires_at": expires, "scope": "own-elements"})
}
func (h *PageHandler) RevokeBoardRun(w http.ResponseWriter, r *http.Request) {
	if agent, _ := pageGrantCallerIsAgent(r); agent || projectAgentFrom(r.Context()) != nil {
		replyError(w, 401, "human authentication required")
		return
	}
	tx, r, rec, ok := h.boardTransaction(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	if !rec.HasBoard {
		replyError(w, 404, "board not found")
		return
	}
	user := UserFromContext(r.Context()).ID
	if rec.OwnerUserID != user {
		replyError(w, 403, "only the human board owner may revoke delegation")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE page_board_run_delegations SET revoked_at=?,revoked_by_user_id=? WHERE board_page_id=? AND run_id=? AND revoked_at IS NULL`, tsformat.Format(time.Now()), user, rec.ID, r.PathValue("runId")); err != nil {
		h.boardError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		h.boardError(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *PageHandler) GetBoardAgent(w http.ResponseWriter, r *http.Request) {
	if checked, ok := h.artifactAgentRequest(w, r); ok {
		h.GetBoard(w, checked)
	}
}
func (h *PageHandler) PublishBoardAgent(w http.ResponseWriter, r *http.Request) {
	if checked, ok := h.artifactAgentRequest(w, r); ok {
		h.ApplyBoardBatch(w, checked)
	}
}

type boardElementAuthor struct{ UserID, AgentID, RunID string }

func loadBoardAuthors(ctx context.Context, q pageAuthQueryer, pageID string) (map[string]boardElementAuthor, error) {
	rows, err := q.QueryContext(ctx, `SELECT element_id,COALESCE(author_user_id,''),COALESCE(author_agent_id,''),COALESCE(author_run_id,'') FROM page_board_elements WHERE board_page_id=?`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]boardElementAuthor{}
	for rows.Next() {
		var id string
		var author boardElementAuthor
		if err = rows.Scan(&id, &author.UserID, &author.AgentID, &author.RunID); err != nil {
			return nil, err
		}
		result[id] = author
	}
	return result, rows.Err()
}
