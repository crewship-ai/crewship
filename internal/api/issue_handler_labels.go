package api

import (
	"net/http"
	"time"
)

// ── 6. ListLabels — GET /api/v1/labels ──────────────────────────────────────

func (h *IssueHandler) ListLabels(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, name, color, label_group FROM labels WHERE workspace_id = ? ORDER BY name ASC`,
		wsID)
	if err != nil {
		internalError(w, r, h.logger, "list labels", err)
		return
	}
	defer rows.Close()

	result := []labelResponse{}
	for rows.Next() {
		var lbl labelResponse
		if err := rows.Scan(&lbl.ID, &lbl.Name, &lbl.Color, &lbl.LabelGroup); err != nil {
			internalError(w, r, h.logger, "scan label", err)
			return
		}
		result = append(result, lbl)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "rows iteration (labels)", err)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// ── 7. CreateLabel — POST /api/v1/labels ────────────────────────────────────

func (h *IssueHandler) CreateLabel(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Name       string  `json:"name"`
		Color      string  `json:"color"`
		LabelGroup *string `json:"label_group"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Name == "" {
		writeProblem(w, r, http.StatusBadRequest, "name is required")
		return
	}
	if req.Color == "" {
		writeProblem(w, r, http.StatusBadRequest, "color is required")
		return
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO labels (id, workspace_id, name, color, label_group, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, wsID, req.Name, req.Color, req.LabelGroup, now)
	if err != nil {
		internalError(w, r, h.logger, "create label", err)
		return
	}

	resp := labelResponse{
		ID:         id,
		Name:       req.Name,
		Color:      req.Color,
		LabelGroup: req.LabelGroup,
	}
	writeJSON(w, http.StatusCreated, resp)
}

// ── 8. UpdateLabel — PATCH /api/v1/labels/{labelId} ─────────────────────────

func (h *IssueHandler) UpdateLabel(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	labelID := r.PathValue("labelId")
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Name       *string `json:"name"`
		Color      *string `json:"color"`
		LabelGroup *string `json:"label_group"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	ub := newUpdate()
	if req.Name != nil {
		ub.Set("name", *req.Name)
	}
	if req.Color != nil {
		ub.Set("color", *req.Color)
	}
	if req.LabelGroup != nil {
		ub.Set("label_group", *req.LabelGroup)
	}

	if ub.Empty() {
		writeProblem(w, r, http.StatusBadRequest, "No fields to update")
		return
	}

	query, args := ub.Build("labels", "id = ? AND workspace_id = ?", labelID, wsID)
	res, err := h.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		internalError(w, r, h.logger, "update label", err)
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		internalError(w, r, h.logger, "update label rows affected", err)
		return
	}
	if affected == 0 {
		writeProblem(w, r, http.StatusNotFound, "Label not found")
		return
	}

	var lbl labelResponse
	err = h.db.QueryRowContext(r.Context(),
		`SELECT id, name, color, label_group FROM labels WHERE id = ?`, labelID).
		Scan(&lbl.ID, &lbl.Name, &lbl.Color, &lbl.LabelGroup)
	if err != nil {
		internalError(w, r, h.logger, "read updated label", err)
		return
	}

	writeJSON(w, http.StatusOK, lbl)
}

// ── 9. DeleteLabel — DELETE /api/v1/labels/{labelId} ────────────────────────

func (h *IssueHandler) DeleteLabel(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}

	labelID := r.PathValue("labelId")
	wsID := WorkspaceIDFromContext(r.Context())

	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM labels WHERE id = ? AND workspace_id = ?`, labelID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "delete label", err)
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		internalError(w, r, h.logger, "delete label rows affected", err)
		return
	}
	if affected == 0 {
		writeProblem(w, r, http.StatusNotFound, "Label not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
