package api

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/mentions"
)

// ── 10. ListComments — GET /api/v1/crews/{crewId}/issues/{identifier}/comments

func (h *IssueHandler) ListComments(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	// Resolve identifier to mission_id
	missionID, err := h.resolveMissionID(r.Context(), ident, crewID, wsID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "resolve issue for comments", err)
		return
	}

	// Opt-in cursor pagination keeps legacy CLI consumers compatible.
	// The browser always opts in and reads at most 100 comments per request.
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize < 0 || pageSize > 100 {
		pageSize = 100
	}
	predicate := ""
	args := []any{missionID}
	if before := r.URL.Query().Get("before_id"); before != "" && pageSize > 0 {
		var created string
		if err := h.db.QueryRowContext(r.Context(), `SELECT created_at FROM mission_comments WHERE id=? AND mission_id=?`, before, missionID).Scan(&created); err != nil {
			writeProblem(w, r, 400, "Comment cursor is no longer available; refresh the conversation")
			return
		}
		predicate = " AND (mc.created_at < ? OR (mc.created_at = ? AND mc.id < ?))"
		args = append(args, created, created, before)
	}
	order := " ORDER BY mc.created_at ASC,mc.id ASC"
	if pageSize > 0 {
		order = " ORDER BY mc.created_at DESC,mc.id DESC LIMIT ?"
		args = append(args, pageSize+1)
	}
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT mc.id, mc.mission_id, mc.author_type, mc.author_id,
		       COALESCE(CASE
		         WHEN mc.author_type = 'user' THEN (SELECT full_name FROM users WHERE id = mc.author_id)
		         WHEN mc.author_type = 'agent' THEN (SELECT name FROM agents WHERE id = mc.author_id)
		         ELSE ''
		       END, ''),
		       mc.body, mc.created_at, mc.updated_at,
		       COALESCE(mc.guest_visible=1 AND mc.guest_project_id=(SELECT project_id FROM missions WHERE id=mc.mission_id),0)
		FROM mission_comments mc
		WHERE mc.mission_id = ?`+predicate+order, args...)
	if err != nil {
		internalError(w, r, h.logger, "list comments", err)
		return
	}
	defer rows.Close()

	result := []commentResponse{}
	for rows.Next() {
		var c commentResponse
		if err := rows.Scan(&c.ID, &c.MissionID, &c.AuthorType, &c.AuthorID,
			&c.AuthorName, &c.Body, &c.CreatedAt, &c.UpdatedAt, &c.GuestVisible); err != nil {
			internalError(w, r, h.logger, "scan comment", err)
			return
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "rows iteration (comments)", err)
		return
	}

	if pageSize > 0 {
		more := len(result) > pageSize
		if more {
			result = result[:pageSize]
		}
		slices.Reverse(result)
		w.Header().Set("X-Has-More", strconv.FormatBool(more))
	}
	writeJSON(w, http.StatusOK, result)
}

// ── 11. CreateComment — POST /api/v1/crews/{crewId}/issues/{identifier}/comments

func (h *IssueHandler) CreateComment(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())

	var req struct {
		Body              string `json:"body"`
		GuestVisible      bool   `json:"guest_visible"`
		ExpectedProjectID string `json:"expected_project_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Body == "" {
		writeProblem(w, r, http.StatusBadRequest, "body is required")
		return
	}
	if req.GuestVisible && len(mentions.ExtractAgentIDs(req.Body)) > 0 {
		writeProblem(w, r, 400, "Remove agent mentions before sharing this comment with Guests; use a private comment to request agent work")
		return
	}
	if req.GuestVisible && len(req.Body) > 32768 {
		writeProblem(w, r, 400, "Shared comments must contain at most 32768 bytes")
		return
	}

	// Resolve identifier to mission_id
	missionID, err := h.resolveMissionID(r.Context(), ident, crewID, wsID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "resolve issue for comment creation", err)
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, r, h.logger, "begin comment", err)
		return
	}
	defer tx.Rollback()
	var exists bool
	if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM missions WHERE id=? AND workspace_id=? AND crew_id=?)`, missionID, wsID, crewID).Scan(&exists); err != nil {
		internalError(w, r, h.logger, "recheck comment issue", err)
		return
	}
	if !exists {
		writeProblem(w, r, 404, "Issue not found")
		return
	}
	if req.GuestVisible {
		if req.ExpectedProjectID == "" {
			writeProblem(w, r, 400, "expected_project_id is required when sharing a comment")
			return
		}
		var project string
		if err = tx.QueryRowContext(r.Context(), `SELECT COALESCE(project_id,'') FROM missions WHERE id=? AND workspace_id=?`, missionID, wsID).Scan(&project); err != nil {
			internalError(w, r, h.logger, "check shared comment audience", err)
			return
		}
		if project == "" {
			writeProblem(w, r, 400, "Only project issues can have comments shared with Guests")
			return
		}
		if project != req.ExpectedProjectID {
			writeProblem(w, r, 409, "The issue project changed. Review the audience before sharing your draft.")
			return
		}
	}
	op, handled := h.beginHumanIssueOperation(w, r, tx, "commented", missionID, req)
	if handled {
		return
	}

	originID, err := captureHumanIssueOrigin(r.Context(), tx, r, missionID)
	if err != nil {
		writeChatIssueError(w, err)
		return
	}
	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at, chat_origin_id, guest_visible, guest_project_id)
		VALUES (?, ?, 'user', ?, ?, ?, ?, NULLIF(?,''), ?, CASE WHEN ? THEN (SELECT project_id FROM missions WHERE id=?) ELSE NULL END)`,
		id, missionID, user.ID, req.Body, now, now, originID, req.GuestVisible, req.GuestVisible, missionID)
	if err != nil {
		internalError(w, r, h.logger, "create comment", err)
		return
	}

	resp := commentResponse{
		GuestVisible: req.GuestVisible,
		ID:           id,
		MissionID:    missionID,
		AuthorType:   "user",
		AuthorID:     user.ID,
		AuthorName:   user.Name,
		Body:         req.Body,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	var issueTitle, issueCrewID string
	if err = tx.QueryRowContext(r.Context(), `SELECT title,COALESCE(crew_id,'') FROM missions WHERE id=? AND workspace_id=?`, missionID, wsID).Scan(&issueTitle, &issueCrewID); err != nil {
		internalError(w, r, h.logger, "load mention issue", err)
		return
	}
	if _, err = persistIssueActivityTx(r.Context(), tx, issueEvent{MissionID: missionID, ActorType: "user", ActorID: user.ID, Action: actionCommented, Details: "Comment added"}); err != nil {
		internalError(w, r, h.logger, "persist comment audit", err)
		return
	}
	if err = persistCommentMentionsTx(r.Context(), tx, mentionContext{WorkspaceID: wsID, MissionID: missionID, Identifier: ident, IssueTitle: issueTitle, IssueCrewID: issueCrewID, CommentID: id, CommentBody: req.Body, AuthorType: "user", AuthorID: user.ID, AuthorName: user.Name}); err != nil {
		internalError(w, r, h.logger, "persist comment mentions", err)
		return
	}
	if err = completeHumanIssueOperation(r, tx, op, missionID, resp); err != nil {
		internalError(w, r, h.logger, "complete comment operation", err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(w, r, h.logger, "commit comment", err)
		return
	}
	if op != nil {
		w.Header().Set("Operation-ID", op.ID)
	}

	h.drainCommittedIssueMutation(wsID)

	h.broadcastIssueEvent(wsID, "issue.updated", map[string]string{"id": missionID})

	writeJSON(w, http.StatusCreated, resp)
}
