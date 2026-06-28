package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
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

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT mc.id, mc.mission_id, mc.author_type, mc.author_id,
		       CASE
		         WHEN mc.author_type = 'user' THEN (SELECT full_name FROM users WHERE id = mc.author_id)
		         WHEN mc.author_type = 'agent' THEN (SELECT name FROM agents WHERE id = mc.author_id)
		         ELSE ''
		       END,
		       mc.body, mc.created_at, mc.updated_at
		FROM mission_comments mc
		WHERE mc.mission_id = ?
		ORDER BY mc.created_at ASC`, missionID)
	if err != nil {
		internalError(w, r, h.logger, "list comments", err)
		return
	}
	defer rows.Close()

	result := []commentResponse{}
	for rows.Next() {
		var c commentResponse
		if err := rows.Scan(&c.ID, &c.MissionID, &c.AuthorType, &c.AuthorID,
			&c.AuthorName, &c.Body, &c.CreatedAt, &c.UpdatedAt); err != nil {
			internalError(w, r, h.logger, "scan comment", err)
			return
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "rows iteration (comments)", err)
		return
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
		Body string `json:"body"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Body == "" {
		writeProblem(w, r, http.StatusBadRequest, "body is required")
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

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at)
		VALUES (?, ?, 'user', ?, ?, ?, ?)`,
		id, missionID, user.ID, req.Body, now, now)
	if err != nil {
		internalError(w, r, h.logger, "create comment", err)
		return
	}

	resp := commentResponse{
		ID:         id,
		MissionID:  missionID,
		AuthorType: "user",
		AuthorID:   user.ID,
		AuthorName: user.Name,
		Body:       req.Body,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	h.broadcastIssueEvent(wsID, "issue.updated", map[string]string{"id": missionID})

	writeJSON(w, http.StatusCreated, resp)
}
