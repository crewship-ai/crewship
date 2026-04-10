package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// SavedViewHandler implements CRUD endpoints for saved issue views.
type SavedViewHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewSavedViewHandler creates a new SavedViewHandler.
func NewSavedViewHandler(db *sql.DB, logger *slog.Logger) *SavedViewHandler {
	return &SavedViewHandler{db: db, logger: logger}
}

// ── Response type ──────────────────────────────────────────────────────────

type savedViewResponse struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	FiltersJSON string  `json:"filters_json"`
	SortJSON    *string `json:"sort_json"`
	ViewType    string  `json:"view_type"`
	IsDefault   bool    `json:"is_default"`
	Shared      bool    `json:"shared"`
	CreatedAt   string  `json:"created_at"`
}

// ── 1. List — GET /api/v1/saved-views ─────────────────────────────────────

func (h *SavedViewHandler) List(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())

	// Return the user's own views plus shared views in the workspace
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, name, filters_json, sort_json, view_type,
		       is_default, shared, created_at
		FROM saved_views
		WHERE workspace_id = ? AND (user_id = ? OR shared = 1)
		ORDER BY is_default DESC, name ASC`, wsID, user.ID)
	if err != nil {
		h.logger.Error("list saved views", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []savedViewResponse
	for rows.Next() {
		var sv savedViewResponse
		if err := rows.Scan(
			&sv.ID, &sv.Name, &sv.FiltersJSON, &sv.SortJSON,
			&sv.ViewType, &sv.IsDefault, &sv.Shared, &sv.CreatedAt,
		); err != nil {
			h.logger.Error("scan saved view", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		result = append(result, sv)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (saved views)", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if result == nil {
		result = []savedViewResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ── 2. Create — POST /api/v1/saved-views ──────────────────────────────────

func (h *SavedViewHandler) Create(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	user := UserFromContext(r.Context())
	if user == nil {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Name        string  `json:"name"`
		FiltersJSON string  `json:"filters_json"`
		SortJSON    *string `json:"sort_json"`
		ViewType    string  `json:"view_type"`
		Shared      bool    `json:"shared"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Name == "" {
		writeProblem(w, r, http.StatusBadRequest, "name is required")
		return
	}
	if req.FiltersJSON == "" {
		writeProblem(w, r, http.StatusBadRequest, "filters_json is required")
		return
	}
	if req.ViewType == "" {
		req.ViewType = "list"
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO saved_views (id, workspace_id, user_id, name, filters_json,
		    sort_json, view_type, is_default, shared, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		id, wsID, user.ID, req.Name, req.FiltersJSON,
		req.SortJSON, req.ViewType, req.Shared, now)
	if err != nil {
		h.logger.Error("insert saved view", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	resp := savedViewResponse{
		ID:          id,
		Name:        req.Name,
		FiltersJSON: req.FiltersJSON,
		SortJSON:    req.SortJSON,
		ViewType:    req.ViewType,
		IsDefault:   false,
		Shared:      req.Shared,
		CreatedAt:   now,
	}

	writeJSON(w, http.StatusCreated, resp)
}

// ── 3. Update — PATCH /api/v1/saved-views/{viewId} ───────────────────────

func (h *SavedViewHandler) Update(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	user := UserFromContext(r.Context())
	if user == nil {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}

	viewID := r.PathValue("viewId")

	// Verify view exists and belongs to this user
	var ownerID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT user_id FROM saved_views WHERE id = ?`, viewID).Scan(&ownerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Saved view not found")
			return
		}
		h.logger.Error("get saved view for update", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if ownerID != user.ID {
		writeProblem(w, r, http.StatusForbidden, "Only the view owner can update it")
		return
	}

	var req struct {
		Name        *string `json:"name"`
		FiltersJSON *string `json:"filters_json"`
		SortJSON    *string `json:"sort_json"`
		ViewType    *string `json:"view_type"`
		IsDefault   *bool   `json:"is_default"`
		Shared      *bool   `json:"shared"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	ub := newUpdate()

	if req.Name != nil {
		ub.Set("name", *req.Name)
	}
	if req.FiltersJSON != nil {
		ub.Set("filters_json", *req.FiltersJSON)
	}
	if req.SortJSON != nil {
		ub.Set("sort_json", *req.SortJSON)
	}
	if req.ViewType != nil {
		ub.Set("view_type", *req.ViewType)
	}
	if req.IsDefault != nil {
		ub.Set("is_default", *req.IsDefault)
	}
	if req.Shared != nil {
		ub.Set("shared", *req.Shared)
	}

	if ub.Empty() {
		writeProblem(w, r, http.StatusBadRequest, "No fields to update")
		return
	}

	query, args := ub.Build("saved_views", "id = ?", viewID)
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		h.logger.Error("update saved view", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Return updated view
	var sv savedViewResponse
	err = h.db.QueryRowContext(r.Context(), `
		SELECT id, name, filters_json, sort_json, view_type,
		       is_default, shared, created_at
		FROM saved_views WHERE id = ?`, viewID).Scan(
		&sv.ID, &sv.Name, &sv.FiltersJSON, &sv.SortJSON,
		&sv.ViewType, &sv.IsDefault, &sv.Shared, &sv.CreatedAt,
	)
	if err != nil {
		h.logger.Error("read updated saved view", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, sv)
}

// ── 4. Delete — DELETE /api/v1/saved-views/{viewId} ───────────────────────

func (h *SavedViewHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeProblem(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}

	viewID := r.PathValue("viewId")

	// Only the owner can delete
	var ownerID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT user_id FROM saved_views WHERE id = ?`, viewID).Scan(&ownerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Saved view not found")
			return
		}
		h.logger.Error("get saved view for delete", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if ownerID != user.ID {
		writeProblem(w, r, http.StatusForbidden, "Only the view owner can delete it")
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM saved_views WHERE id = ?`, viewID)
	if err != nil {
		h.logger.Error("delete saved view", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("delete saved view rows affected", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if affected == 0 {
		writeProblem(w, r, http.StatusNotFound, "Saved view not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
