package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

// TemplateHandler provides CRUD endpoints for agent prompt templates.
type TemplateHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewTemplateHandler creates a TemplateHandler with the given database and logger.
func NewTemplateHandler(db *sql.DB, logger *slog.Logger) *TemplateHandler {
	return &TemplateHandler{db: db, logger: logger}
}

type templateResponse struct {
	ID           string          `json:"id"`
	WorkspaceID  string          `json:"workspace_id"`
	Name         string          `json:"name"`
	Description  *string         `json:"description"`
	TemplateJSON json.RawMessage `json:"template_json"`
	Icon         *string         `json:"icon"`
	Color        *string         `json:"color"`
	IsBuiltin    bool            `json:"is_builtin"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
}

// List handles GET /api/v1/templates
func (h *TemplateHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	// Lazy seed builtin templates for this workspace
	if err := database.SeedBuiltinTemplates(r.Context(), h.db, wsID, h.logger); err != nil {
		h.logger.Warn("seed builtin templates", "error", err)
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, workspace_id, name, description, template_json, icon, color, is_builtin, created_at, updated_at
		FROM workflow_templates
		WHERE workspace_id = ?
		ORDER BY is_builtin DESC, name ASC`, wsID)
	if err != nil {
		h.logger.Error("list templates", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []templateResponse
	for rows.Next() {
		var t templateResponse
		var tmplJSON string
		if err := rows.Scan(&t.ID, &t.WorkspaceID, &t.Name, &t.Description, &tmplJSON,
			&t.Icon, &t.Color, &t.IsBuiltin, &t.CreatedAt, &t.UpdatedAt); err != nil {
			h.logger.Error("scan template", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		t.TemplateJSON = json.RawMessage(tmplJSON)
		result = append(result, t)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (templates)", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if result == nil {
		result = []templateResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// Get handles GET /api/v1/templates/{templateId}
func (h *TemplateHandler) Get(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	templateID := r.PathValue("templateId")

	var t templateResponse
	var tmplJSON string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, workspace_id, name, description, template_json, icon, color, is_builtin, created_at, updated_at
		FROM workflow_templates WHERE id = ? AND workspace_id = ?`, templateID, wsID).Scan(
		&t.ID, &t.WorkspaceID, &t.Name, &t.Description, &tmplJSON,
		&t.Icon, &t.Color, &t.IsBuiltin, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Template not found")
			return
		}
		h.logger.Error("get template", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	t.TemplateJSON = json.RawMessage(tmplJSON)
	writeJSON(w, http.StatusOK, t)
}

// Create handles POST /api/v1/templates
func (h *TemplateHandler) Create(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Name         string          `json:"name"`
		Description  *string         `json:"description"`
		TemplateJSON json.RawMessage `json:"template_json"`
		Icon         *string         `json:"icon"`
		Color        *string         `json:"color"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Name == "" {
		writeProblem(w, r, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.TemplateJSON) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "template_json is required")
		return
	}
	// Validate template_json is valid JSON
	if !json.Valid(req.TemplateJSON) {
		writeProblem(w, r, http.StatusBadRequest, "template_json must be valid JSON")
		return
	}

	id := generateTemplateID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO workflow_templates (id, workspace_id, name, description, template_json, icon, color, is_builtin, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		id, wsID, req.Name, req.Description, string(req.TemplateJSON), req.Icon, req.Color, now, now)
	if err != nil {
		h.logger.Error("create template", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to create template")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// checkTemplateModifiable verifies the template exists in the workspace and is
// not a builtin template. Writes the appropriate error response on failure.
// Returns true if the template is modifiable, false if the handler should return.
func (h *TemplateHandler) checkTemplateModifiable(w http.ResponseWriter, r *http.Request, templateID, wsID, action string) bool {
	var isBuiltin bool
	err := h.db.QueryRowContext(r.Context(),
		`SELECT is_builtin FROM workflow_templates WHERE id = ? AND workspace_id = ?`,
		templateID, wsID).Scan(&isBuiltin)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Template not found")
			return false
		}
		h.logger.Error("check template", "error", err, "action", action)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return false
	}
	if isBuiltin {
		writeProblem(w, r, http.StatusForbidden, "Cannot "+action+" builtin templates")
		return false
	}
	return true
}

// Update handles PATCH /api/v1/templates/{templateId}
func (h *TemplateHandler) Update(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())
	templateID := r.PathValue("templateId")

	var req struct {
		Name         *string          `json:"name"`
		Description  *string          `json:"description"`
		TemplateJSON *json.RawMessage `json:"template_json"`
		Icon         *string          `json:"icon"`
		Color        *string          `json:"color"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	// Check template exists and is not builtin
	if !h.checkTemplateModifiable(w, r, templateID, wsID, "modify") {
		return
	}

	// Build dynamic update
	ub := newUpdate()
	if req.Name != nil {
		ub.Set("name", *req.Name)
	}
	if req.Description != nil {
		ub.Set("description", *req.Description)
	}
	if req.TemplateJSON != nil {
		if !json.Valid(*req.TemplateJSON) {
			writeProblem(w, r, http.StatusBadRequest, "template_json must be valid JSON")
			return
		}
		ub.Set("template_json", string(*req.TemplateJSON))
	}
	if req.Icon != nil {
		ub.Set("icon", *req.Icon)
	}
	if req.Color != nil {
		ub.Set("color", *req.Color)
	}

	query, args := ub.Build("workflow_templates", "id = ? AND workspace_id = ?", templateID, wsID)
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		h.logger.Error("update template", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to update template")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": templateID})
}

// Delete handles DELETE /api/v1/templates/{templateId}
func (h *TemplateHandler) Delete(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())
	templateID := r.PathValue("templateId")

	if !h.checkTemplateModifiable(w, r, templateID, wsID, "delete") {
		return
	}

	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM workflow_templates WHERE id = ? AND workspace_id = ?`,
		templateID, wsID); err != nil {
		h.logger.Error("delete template", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to delete template")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func generateTemplateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("wt_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("wt_%x", b)
}
