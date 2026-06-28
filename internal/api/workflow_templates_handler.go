package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// WorkflowTemplateHandler implements CRUD for workflow templates — the
// reusable issue-lifecycle definitions (backlog / in_progress / done / …)
// users attach to crews. Workspace-scoped; user-created rows always have
// is_builtin=0. The single source of truth for the underlying schema lives
// in migration v19 (internal/database/migrate_consts_v16_v25.go).
type WorkflowTemplateHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
}

// NewWorkflowTemplateHandler wires a handler. `hub` may be nil in tests; the
// broadcast helper is hub-nil-safe.
func NewWorkflowTemplateHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *WorkflowTemplateHandler {
	return &WorkflowTemplateHandler{db: db, hub: hub, logger: logger}
}

// ── Response + stage types ─────────────────────────────────────────────────

// workflowTemplateResponse is the JSON shape returned by every verb on this
// handler. Optional columns surface as nullable pointers so a missing icon/
// color round-trips as JSON null instead of an empty string (which would
// otherwise change the manifest's planning diff).
type workflowTemplateResponse struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	TemplateJSON string  `json:"template_json"`
	Icon         *string `json:"icon"`
	Color        *string `json:"color"`
	IsBuiltin    bool    `json:"is_builtin"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// workflowStage mirrors the per-stage shape declared inside template_json.
// Parsed for validation only — we re-serialise the original JSON when
// persisting so we don't drop fields a future schema rev might add.
type workflowStage struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Position int    `json:"position"`
	Color    string `json:"color,omitempty"`
}

// stageHexColorRE accepts standard 6-digit hex (with optional 3-digit short
// form) so the validator can reject typos like "blue" before they reach the
// frontend renderer. Mirrors the convention used by Label/Project.
var stageHexColorRE = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// validStageTypes is the closed enum the SPEC-2 manifest validator also
// enforces. Keeping the same set both server-side and client-side means
// `crewship apply` can give the user the same error a manual API caller
// would see.
var validStageTypes = map[string]bool{
	"open":      true,
	"started":   true,
	"completed": true,
	"cancelled": true,
}

// validateTemplateJSON parses the raw template_json TEXT, enforces shape
// rules, and returns the canonical re-serialised JSON (so the row we store
// is deterministic regardless of caller whitespace).
//
// Rules — kept in lockstep with SPEC-2 §8 "Validation":
//   - parses as a non-empty array
//   - every stage has name + type + position
//   - stage.type is one of {open, started, completed, cancelled}
//   - names are unique within the template (case-sensitive — matches DB)
//   - positions are unique within the template
//   - exactly one stage with type=open
//   - at least one stage with type=completed
//   - stage.color (if set) matches hex pattern
func validateTemplateJSON(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("template_json is required")
	}
	var stages []workflowStage
	if err := json.Unmarshal([]byte(raw), &stages); err != nil {
		return "", errors.New("template_json must be a JSON array of stages")
	}
	if len(stages) == 0 {
		return "", errors.New("template_json must contain at least one stage")
	}

	names := make(map[string]struct{}, len(stages))
	positions := make(map[int]struct{}, len(stages))
	openCount, completedCount := 0, 0

	for i, st := range stages {
		if st.Name == "" {
			return "", errors.New("stage name is required")
		}
		if st.Type == "" {
			return "", errors.New("stage type is required")
		}
		if !validStageTypes[st.Type] {
			return "", errors.New("stage type must be one of: open, started, completed, cancelled")
		}
		// Position 0 is treated as "missing" — the canonical templates in
		// the spec start at 1. Anything <=0 makes ORDER BY position
		// ambiguous so we reject it up front.
		if st.Position <= 0 {
			return "", errors.New("stage position must be a positive integer")
		}
		if _, dup := names[st.Name]; dup {
			return "", errors.New("stage names must be unique within a template")
		}
		names[st.Name] = struct{}{}

		if _, dup := positions[st.Position]; dup {
			return "", errors.New("stage positions must be unique within a template")
		}
		positions[st.Position] = struct{}{}

		if st.Color != "" && !stageHexColorRE.MatchString(st.Color) {
			return "", errors.New("stage color must be a hex string like #3B82F6")
		}
		switch st.Type {
		case "open":
			openCount++
		case "completed":
			completedCount++
		}
		_ = i
	}
	if openCount != 1 {
		return "", errors.New("template must declare exactly one stage with type=open")
	}
	if completedCount < 1 {
		return "", errors.New("template must declare at least one stage with type=completed")
	}

	// Re-serialise to canonicalise whitespace + key order. Callers that
	// later diff template_json (manifest apply, primarily) get a stable
	// byte string instead of whatever the user pasted.
	canon, err := json.Marshal(stages)
	if err != nil {
		return "", errors.New("could not normalise template_json")
	}
	return string(canon), nil
}

// scanWorkflowTemplate hydrates the response struct from a row that
// projects every selectable column in the canonical order used by both
// List and Get.
func scanWorkflowTemplate(row interface {
	Scan(dest ...any) error
}) (workflowTemplateResponse, error) {
	var wt workflowTemplateResponse
	err := row.Scan(
		&wt.ID,
		&wt.Name,
		&wt.Description,
		&wt.TemplateJSON,
		&wt.Icon,
		&wt.Color,
		&wt.IsBuiltin,
		&wt.CreatedAt,
		&wt.UpdatedAt,
	)
	return wt, err
}

const workflowTemplateColumns = `id, name, description, template_json, icon, color, is_builtin, created_at, updated_at`

// ── List ───────────────────────────────────────────────────────────────────

// List returns every workflow template visible to the calling workspace,
// ordered by creation time. Built-in and user-created rows share the same
// shape so the frontend can render them in one list.
// GET /api/v1/workflow-templates
func (h *WorkflowTemplateHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT `+workflowTemplateColumns+`
		FROM workflow_templates
		WHERE workspace_id = ?
		ORDER BY is_builtin DESC, created_at ASC`, wsID)
	if err != nil {
		internalError(w, r, h.logger, "list workflow templates", err)
		return
	}
	defer rows.Close()

	out := []workflowTemplateResponse{}
	for rows.Next() {
		wt, err := scanWorkflowTemplate(rows)
		if err != nil {
			internalError(w, r, h.logger, "scan workflow template", err)
			return
		}
		out = append(out, wt)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "workflow templates iteration", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Create ─────────────────────────────────────────────────────────────────

// Create inserts a new template owned by the caller's workspace. is_builtin
// is server-controlled — callers cannot mark their own rows as built-in.
// Returns 409 if a template with the same name already exists in the
// workspace (the underlying UNIQUE index).
// POST /api/v1/workflow-templates
func (h *WorkflowTemplateHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Name         string  `json:"name"`
		Description  *string `json:"description"`
		TemplateJSON string  `json:"template_json"`
		Icon         *string `json:"icon"`
		Color        *string `json:"color"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeProblem(w, r, http.StatusBadRequest, "name is required")
		return
	}

	canonical, err := validateTemplateJSON(req.TemplateJSON)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, err.Error())
		return
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO workflow_templates
		    (id, workspace_id, name, description, template_json, icon, color,
		     is_builtin, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		id, wsID, req.Name, req.Description, canonical, req.Icon, req.Color, now, now)
	if err != nil {
		// SQLite surfaces the UNIQUE(workspace_id, name) collision through
		// the generic constraint-failed message. Translate to 409 so the
		// CLI can render a friendly hint instead of "Internal server error".
		if isUniqueConstraintErr(err) {
			writeProblem(w, r, http.StatusConflict, "A workflow template with this name already exists")
			return
		}
		internalError(w, r, h.logger, "insert workflow template", err)
		return
	}

	resp := workflowTemplateResponse{
		ID:           id,
		Name:         req.Name,
		Description:  req.Description,
		TemplateJSON: canonical,
		Icon:         req.Icon,
		Color:        req.Color,
		IsBuiltin:    false,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	broadcastWorkspaceEvent(h.hub, wsID, "workflow_template.created", map[string]string{"id": id})
	writeJSON(w, http.StatusCreated, resp)
}

// ── Get ────────────────────────────────────────────────────────────────────

// Get returns one template by ID, scoped to the caller's workspace. Rows in
// a different workspace surface as 404 — never as 403 — to avoid leaking the
// existence of templates the caller cannot see.
// GET /api/v1/workflow-templates/{id}
func (h *WorkflowTemplateHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	wsID := WorkspaceIDFromContext(r.Context())

	row := h.db.QueryRowContext(r.Context(),
		`SELECT `+workflowTemplateColumns+`
		FROM workflow_templates
		WHERE id = ? AND workspace_id = ?`, id, wsID)

	wt, err := scanWorkflowTemplate(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Workflow template not found")
			return
		}
		internalError(w, r, h.logger, "get workflow template", err)
		return
	}
	writeJSON(w, http.StatusOK, wt)
}

// ── Update ─────────────────────────────────────────────────────────────────

// Update PATCHes the mutable subset (name/description/template_json/icon/color)
// of a template. is_builtin and created_at are immutable; updated_at is set
// to "now" by updateBuilder. When template_json is supplied we re-validate
// the stage shape just like Create — there is no "partial stage edit" path.
// PATCH /api/v1/workflow-templates/{id}
func (h *WorkflowTemplateHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	id := r.PathValue("id")
	wsID := WorkspaceIDFromContext(r.Context())

	// Verify existence + workspace scope first so we return 404 (not 400)
	// when the row doesn't belong to the caller.
	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM workflow_templates WHERE id = ? AND workspace_id = ?`,
		id, wsID).Scan(&existingID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Workflow template not found")
			return
		}
		internalError(w, r, h.logger, "get workflow template for update", err)
		return
	}

	var req struct {
		Name         *string `json:"name"`
		Description  *string `json:"description"`
		TemplateJSON *string `json:"template_json"`
		Icon         *string `json:"icon"`
		Color        *string `json:"color"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	ub := newUpdate()

	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			writeProblem(w, r, http.StatusBadRequest, "name cannot be empty")
			return
		}
		ub.Set("name", *req.Name)
	}
	if req.Description != nil {
		if *req.Description == "" {
			ub.SetNull("description")
		} else {
			ub.Set("description", *req.Description)
		}
	}
	if req.TemplateJSON != nil {
		canonical, err := validateTemplateJSON(*req.TemplateJSON)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, err.Error())
			return
		}
		ub.Set("template_json", canonical)
	}
	if req.Icon != nil {
		if *req.Icon == "" {
			ub.SetNull("icon")
		} else {
			ub.Set("icon", *req.Icon)
		}
	}
	if req.Color != nil {
		if *req.Color == "" {
			ub.SetNull("color")
		} else {
			if !stageHexColorRE.MatchString(*req.Color) {
				writeProblem(w, r, http.StatusBadRequest, "color must be a hex string like #3B82F6")
				return
			}
			ub.Set("color", *req.Color)
		}
	}

	if ub.Empty() {
		writeProblem(w, r, http.StatusBadRequest, "No fields to update")
		return
	}

	query, args := ub.Build("workflow_templates", "id = ? AND workspace_id = ?", id, wsID)
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		if isUniqueConstraintErr(err) {
			writeProblem(w, r, http.StatusConflict, "A workflow template with this name already exists")
			return
		}
		internalError(w, r, h.logger, "update workflow template", err)
		return
	}

	broadcastWorkspaceEvent(h.hub, wsID, "workflow_template.updated", map[string]string{"id": id})

	// Return the freshly-updated row so the caller doesn't have to re-GET.
	row := h.db.QueryRowContext(r.Context(),
		`SELECT `+workflowTemplateColumns+`
		FROM workflow_templates
		WHERE id = ?`, id)
	wt, err := scanWorkflowTemplate(row)
	if err != nil {
		internalError(w, r, h.logger, "read updated workflow template", err)
		return
	}
	writeJSON(w, http.StatusOK, wt)
}

// ── Delete ─────────────────────────────────────────────────────────────────

// Delete removes a template by ID. 404 if the row doesn't exist or belongs
// to another workspace. Soft-delete is intentionally NOT used here —
// templates are reference data, and crews that referenced this template
// will gracefully fall back to the default lifecycle at runtime.
// DELETE /api/v1/workflow-templates/{id}
func (h *WorkflowTemplateHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	id := r.PathValue("id")
	wsID := WorkspaceIDFromContext(r.Context())

	found, err := deleteByID(r.Context(), h.db, "workflow_templates", id, wsID)
	if err != nil {
		internalError(w, r, h.logger, "delete workflow template", err)
		return
	}
	if !found {
		writeProblem(w, r, http.StatusNotFound, "Workflow template not found")
		return
	}

	broadcastWorkspaceEvent(h.hub, wsID, "workflow_template.deleted", map[string]string{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

// isUniqueConstraintErr reports whether `err` looks like a SQLite UNIQUE
// constraint violation. modernc.org/sqlite surfaces this as a plain text
// message ("UNIQUE constraint failed: …") rather than a typed sentinel, so
// substring match is the canonical approach used elsewhere in this package.
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
