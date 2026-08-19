package api

import (
	"fmt"
	"net/http"
	"strings"
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
		// Same reasoning as project create: a duplicate name in this
		// workspace is a conflict the caller can act on, and the seed's
		// idempotent re-run path keys off the 409.
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			writeProblem(w, r, http.StatusConflict,
				fmt.Sprintf("A label named %q already exists in this workspace", req.Name))
			return
		}
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

	// NOT newUpdate(): that builder always emits "updated_at = ?" as its first
	// clause, and the labels table has no updated_at column — it carries only
	// created_at. Every PATCH therefore died on "no such column: updated_at"
	// and renaming or recolouring a label was a hard 500 for every caller.
	// Found by the cross-workspace fence matrix, which could not test this
	// route's tenancy at all while the statement never executed.
	var sets []string
	var args []any
	if req.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *req.Name)
	}
	if req.Color != nil {
		sets = append(sets, "color = ?")
		args = append(args, *req.Color)
	}
	if req.LabelGroup != nil {
		sets = append(sets, "label_group = ?")
		args = append(args, *req.LabelGroup)
	}

	if len(sets) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "No fields to update")
		return
	}

	// The workspace_id predicate is the fence: a label id from another tenant
	// affects zero rows and falls through to the 404 below.
	// #nosec G202 -- sets is built from the fixed column list above, never from input.
	query := "UPDATE labels SET " + strings.Join(sets, ", ") + " WHERE id = ? AND workspace_id = ?"
	args = append(args, labelID, wsID)
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

	found, err := deleteByID(r.Context(), h.db, "labels", labelID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "delete label", err)
		return
	}
	if !found {
		writeProblem(w, r, http.StatusNotFound, "Label not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
