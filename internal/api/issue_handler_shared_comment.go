package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Publishing to a Guest audience is an explicit, atomic operation. The
// ordinary private-comment and agent-mention path keeps its existing behavior.
func (h *IssueHandler) createSharedStaffComment(w http.ResponseWriter, req *http.Request, missionID, body string) {
	user := UserFromContext(req.Context())
	workspace := WorkspaceIDFromContext(req.Context())
	if user == nil {
		writeProblem(w, req, 401, "Authentication required")
		return
	}
	tx, err := h.db.BeginTx(req.Context(), nil)
	if err != nil {
		internalError(w, req, h.logger, "begin shared comment", err)
		return
	}
	defer tx.Rollback()
	var project sql.NullString
	err = tx.QueryRowContext(req.Context(), `SELECT m.project_id FROM missions m JOIN workspace_members wm ON wm.workspace_id=m.workspace_id JOIN users u ON u.id=wm.user_id JOIN workspaces ws ON ws.id=wm.workspace_id
 WHERE m.id=? AND m.workspace_id=? AND m.mission_type='issue' AND wm.user_id=? AND wm.role IN ('OWNER','ADMIN','MANAGER') AND wm.access_mode='trusted' AND u.suspended_at IS NULL AND ws.deleted_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM workspace_members other WHERE other.user_id=wm.user_id AND other.access_mode='restricted')`, missionID, workspace, user.ID).Scan(&project)
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, req, 404, "Issue unavailable")
		return
	}
	if err != nil {
		internalError(w, req, h.logger, "authorize shared comment", err)
		return
	}
	if !project.Valid || project.String == "" {
		writeProblem(w, req, 400, "Only project issues can have comments shared with Guests")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	comment := commentResponse{ID: generateCUID(), MissionID: missionID, AuthorType: "user", AuthorID: user.ID, AuthorName: user.Name, Body: body, CreatedAt: now, UpdatedAt: now, GuestVisible: true}
	_, err = tx.ExecContext(req.Context(), `INSERT INTO mission_comments(id,mission_id,author_type,author_id,body,created_at,updated_at,guest_visible,guest_project_id) VALUES(?,?,'user',?,?,?,?,1,?)`, comment.ID, missionID, user.ID, body, now, now, project.String)
	if err != nil {
		internalError(w, req, h.logger, "publish shared comment", err)
		return
	}
	notify, err := appendProjectIssueAudit(req.Context(), tx, h.journal, journal.Entry{WorkspaceID: workspace, MissionID: missionID, Type: journal.EntryMissionComment, ActorType: journal.ActorUser, ActorID: user.ID, Summary: "Staff published a comment to project Guests", Payload: map[string]any{"project_id": project.String, "issue_id": missionID, "comment_id": comment.ID, "guest_visible": true}})
	if err != nil {
		internalError(w, req, h.logger, "audit shared comment", err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(w, req, h.logger, "commit shared comment", err)
		return
	}
	if notify != nil {
		notify()
	}
	h.broadcastIssueEvent(workspace, "issue.updated", map[string]string{"id": missionID})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, comment)
}
