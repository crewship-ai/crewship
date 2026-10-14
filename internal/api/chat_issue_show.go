package api

import (
	"database/sql"
	"net/http"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

// ClassifyRun resolves host authority for sidecar routing. It is not approval
// to mutate: supported chat writes still recheck authority in their own Tx.
func (h *InternalIssueHandler) ClassifyRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		RunID       string `json:"author_run_id"`
	}
	if err := readJSON(r, &req); err != nil {
		replyError(w, 400, "invalid_json")
		return
	}
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) {
		return
	}
	var crewID string
	if err := h.db.QueryRowContext(r.Context(), `SELECT COALESCE(crew_id,'') FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, req.AgentID, req.WorkspaceID).Scan(&crewID); err != nil {
		replyError(w, 404, "agent_unavailable")
		return
	}
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &crewID) {
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeChatIssueError(w, err)
		return
	}
	defer tx.Rollback()
	a, err := runauthority.Resolve(r.Context(), tx, req.RunID, req.AgentID, req.WorkspaceID)
	if err != nil {
		writeChatIssueError(w, err)
		return
	}
	mode := "autonomous"
	if a.HumanAuthority != nil {
		mode = "human"
	}
	writeJSON(w, http.StatusOK, map[string]string{"mode": mode})
}

// Show creates a durable reference without modifying the issue. Its chat and
// triggering message come exclusively from the active run's host authority.
func (h *InternalIssueHandler) Show(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkspaceID  string `json:"workspace_id"`
		AgentID      string `json:"agent_id"`
		RunID        string `json:"author_run_id"`
		OperationKey string `json:"operation_key"`
	}
	if err := readJSON(r, &req); err != nil {
		replyError(w, 400, "invalid_json")
		return
	}
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) {
		return
	}
	ident := r.PathValue("identifier")
	var id, crewID string
	err := h.db.QueryRowContext(r.Context(), `SELECT id,crew_id FROM missions WHERE identifier=? AND workspace_id=? AND mission_type='issue'`, ident, req.WorkspaceID).Scan(&id, &crewID)
	if err != nil {
		replyError(w, 404, "issue_unavailable")
		return
	}
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &crewID) {
		return
	}
	if req.RunID == "" {
		writeChatIssueError(w, runauthority.ErrUnavailable)
		return
	}
	if h.checkChatIssueReplay(w, r, req.WorkspaceID, req.AgentID, req.RunID, req.OperationKey, "shown", id, req) {
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeChatIssueError(w, err)
		return
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(r.Context(), `SELECT 1 FROM missions m JOIN agents a ON a.crew_id=m.crew_id AND a.workspace_id=m.workspace_id WHERE m.id=? AND m.workspace_id=? AND a.id=? AND a.deleted_at IS NULL`, id, req.WorkspaceID, req.AgentID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			replyError(w, 404, "issue_unavailable")
		} else {
			writeChatIssueError(w, err)
		}
		return
	}
	op, replay, err := h.beginChatIssueOperation(r.Context(), tx, req.WorkspaceID, req.AgentID, req.RunID, req.OperationKey, "shown", id, req)
	if err != nil {
		writeChatIssueError(w, err)
		return
	}
	if op == nil || op.authority.HumanAuthority == nil {
		writeChatIssueError(w, runauthority.ErrUnavailable)
		return
	}
	if replay {
		writeChatIssueReplay(w, op.operation)
		return
	}
	result := map[string]string{"id": id, "identifier": ident, "operation_id": op.operation.ID}
	if err := completeChatIssueOperation(r.Context(), tx, op, id, result); err != nil {
		writeChatIssueError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		writeChatIssueError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
