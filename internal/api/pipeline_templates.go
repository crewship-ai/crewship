package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// Routine templates (ODCH-RT-22).
//
//	GET    /workspaces/{ws}/pipeline-templates               member    list (no definitions)
//	GET    /workspaces/{ws}/pipeline-templates/{id}          member    one, with its definition
//	POST   /workspaces/{ws}/pipeline-templates               MANAGER+  create from a definition or from a routine
//	DELETE /workspaces/{ws}/pipeline-templates/{id}          MANAGER+
//	POST   /workspaces/{ws}/pipeline-templates/{id}/use      MANAGER+  save a DRAFT routine from the template
//
// A template is a definition and a name. Using it never publishes anything: it
// saves a draft that is tested and published like a draft written by hand.

const (
	maxTemplateNameLen = 80
	maxTemplateDescLen = 500
	maxTemplates       = 200
)

type routineTemplateResponse struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Category    string          `json:"category,omitempty"`
	StepCount   int             `json:"step_count"`
	Definition  json.RawMessage `json:"definition,omitempty"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

func templateStepCount(def string) int {
	var d struct {
		Steps []json.RawMessage `json:"steps"`
	}
	_ = json.Unmarshal([]byte(def), &d)
	return len(d.Steps)
}

func scanTemplate(row interface{ Scan(...any) error }, withDefinition bool) (*routineTemplateResponse, error) {
	var t routineTemplateResponse
	var desc, cat sql.NullString
	var def string
	if err := row.Scan(&t.ID, &t.Name, &desc, &cat, &def, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.Description, t.Category = desc.String, cat.String
	t.StepCount = templateStepCount(def)
	if withDefinition {
		t.Definition = json.RawMessage(def)
	}
	return &t, nil
}

const templateColumns = `id, name, description, category, definition_json, created_at, updated_at`

func (h *PipelineHandler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(), `SELECT `+templateColumns+` FROM routine_templates WHERE workspace_id = ? ORDER BY lower(name), id`, WorkspaceIDFromContext(r.Context()))
	if err != nil {
		replyInternalError(w, h.logger, "list routine templates", err)
		return
	}
	defer rows.Close()
	out := []routineTemplateResponse{}
	for rows.Next() {
		t, err := scanTemplate(rows, false)
		if err != nil {
			replyInternalError(w, h.logger, "scan routine template", err)
			return
		}
		out = append(out, *t)
	}
	if rows.Err() != nil {
		replyInternalError(w, h.logger, "list routine templates", rows.Err())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *PipelineHandler) GetTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := scanTemplate(h.db.QueryRowContext(r.Context(), `SELECT `+templateColumns+` FROM routine_templates WHERE id = ? AND workspace_id = ?`,
		r.PathValue("id"), WorkspaceIDFromContext(r.Context())), true)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "template not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "get routine template", err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

type createTemplateBody struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Category    string          `json:"category"`
	Definition  json.RawMessage `json:"definition"`
	// FromRoutine copies the definition of an existing routine (its slug)
	// instead of taking one in the body.
	FromRoutine string `json:"from_routine"`
}

func (h *PipelineHandler) CreateTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	var body createTemplateBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len([]rune(body.Name)) > maxTemplateNameLen {
		replyError(w, http.StatusBadRequest, "name is required and must be 80 characters or fewer")
		return
	}
	if len([]rune(body.Description)) > maxTemplateDescLen || len(body.Category) > 40 {
		replyError(w, http.StatusBadRequest, "description must be 500 characters or fewer and category 40")
		return
	}
	if body.FromRoutine != "" && len(body.Definition) > 0 {
		replyError(w, http.StatusBadRequest, "pass definition or from_routine, not both")
		return
	}
	def := string(body.Definition)
	if body.FromRoutine != "" {
		p, err := h.store.GetBySlug(r.Context(), workspaceID, body.FromRoutine)
		if errors.Is(err, pipeline.ErrNotFound) {
			replyError(w, http.StatusNotFound, "routine not found: "+body.FromRoutine)
			return
		}
		if err != nil {
			replyInternalError(w, h.logger, "template from routine", err)
			return
		}
		def = p.DefinitionJSON
	}
	if strings.TrimSpace(def) == "" {
		replyError(w, http.StatusBadRequest, "definition or from_routine is required")
		return
	}
	// A template that could not become a routine is no template: the same
	// parse and validation a save runs, with agent slugs left open (they are
	// resolved against a crew when the template is used).
	dsl, err := pipeline.Parse([]byte(def))
	if err == nil {
		err = pipeline.Validate(dsl, nil, nil)
	}
	if err != nil {
		replyError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	var n int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM routine_templates WHERE workspace_id = ?`, workspaceID).Scan(&n); err == nil && n >= maxTemplates {
		replyError(w, http.StatusConflict, "this workspace has the most templates it can hold (200)")
		return
	}
	var createdBy any
	if u := UserFromContext(r.Context()); u != nil && u.ID != "" {
		createdBy = u.ID
	}
	now := time.Now().UTC().Format(time.RFC3339)
	id := generateCUID()
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO routine_templates (id, workspace_id, name, description, category, definition_json, created_by, created_at, updated_at)
		VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?)`,
		id, workspaceID, body.Name, body.Description, body.Category, def, createdBy, now, now); err != nil {
		if isUniqueViolation(err) {
			replyError(w, http.StatusConflict, "a template named \""+body.Name+"\" already exists")
			return
		}
		replyInternalError(w, h.logger, "create routine template", err)
		return
	}
	t, err := scanTemplate(h.db.QueryRowContext(r.Context(), `SELECT `+templateColumns+` FROM routine_templates WHERE id = ?`, id), true)
	if err != nil {
		replyInternalError(w, h.logger, "load routine template", err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (h *PipelineHandler) DeleteTemplate(w http.ResponseWriter, r *http.Request) {
	res, err := h.db.ExecContext(r.Context(), `DELETE FROM routine_templates WHERE id = ? AND workspace_id = ?`, r.PathValue("id"), WorkspaceIDFromContext(r.Context()))
	if err != nil {
		replyInternalError(w, h.logger, "delete routine template", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		replyError(w, http.StatusNotFound, "template not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var templateSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type useTemplateBody struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	AuthorCrewID string `json:"author_crew_id"`
}

// UseTemplate saves a DRAFT routine from the template. Never a live routine.
func (h *PipelineHandler) UseTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "auth required")
		return
	}
	var body useTemplateBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.Slug = strings.TrimSpace(body.Slug)
	if !templateSlugRE.MatchString(body.Slug) {
		replyError(w, http.StatusBadRequest, "slug is required: lower-case letters, digits and hyphens, up to 63 characters")
		return
	}
	t, err := scanTemplate(h.db.QueryRowContext(r.Context(), `SELECT `+templateColumns+` FROM routine_templates WHERE id = ? AND workspace_id = ?`,
		r.PathValue("id"), workspaceID), true)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "template not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "use routine template", err)
		return
	}
	if body.AuthorCrewID != "" {
		var id string
		if err := h.db.QueryRowContext(r.Context(), `SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, body.AuthorCrewID, workspaceID).Scan(&id); err != nil {
			replyError(w, http.StatusUnprocessableEntity, "author_crew_id does not name a crew of this workspace")
			return
		}
	}
	if _, err := h.store.GetBySlug(r.Context(), workspaceID, body.Slug); err == nil {
		replyError(w, http.StatusConflict, "a routine with the slug \""+body.Slug+"\" already exists")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = t.Name
	}
	doc, err := json.Marshal(map[string]any{
		"slug": body.Slug, "name": name, "description": t.Description,
		"definition": t.Definition, "author_crew_id": body.AuthorCrewID,
	})
	if err != nil {
		replyInternalError(w, h.logger, "use routine template", err)
		return
	}
	draft, err := h.store.SaveDraft(r.Context(), pipeline.Draft{WorkspaceID: workspaceID, Slug: body.Slug, Document: doc, UpdatedBy: user.ID})
	if errors.Is(err, pipeline.ErrDraftConflict) {
		replyError(w, http.StatusConflict, "a draft with the slug \""+body.Slug+"\" already exists")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "save draft from template", err)
		return
	}
	writeJSON(w, http.StatusCreated, draft)
}
