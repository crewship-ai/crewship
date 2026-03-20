package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

var seedTemplatesOnce sync.Once

type CrewTemplateHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewCrewTemplateHandler(db *sql.DB, logger *slog.Logger) *CrewTemplateHandler {
	return &CrewTemplateHandler{db: db, logger: logger}
}

type crewTemplateResponse struct {
	ID          string                          `json:"id"`
	Name        string                          `json:"name"`
	Slug        string                          `json:"slug"`
	Description *string                         `json:"description"`
	Icon        *string                         `json:"icon"`
	Color       *string                         `json:"color"`
	Category    string                          `json:"category"`
	Agents      []database.CrewTemplateAgent    `json:"agents"`
	IsBuiltin   bool                            `json:"is_builtin"`
	CreatedAt   string                          `json:"created_at"`
}

// List handles GET /api/v1/crew-templates
func (h *CrewTemplateHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	seedTemplatesOnce.Do(func() {
		if err := database.SeedBuiltinCrewTemplates(r.Context(), h.db, h.logger); err != nil {
			h.logger.Warn("seed crew templates", "error", err)
		}
	})

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, name, slug, description, icon, color, category, agents_json, is_builtin, created_at
		FROM crew_templates
		WHERE is_builtin = 1 OR workspace_id = ?
		ORDER BY is_builtin DESC, category ASC, name ASC`, wsID)
	if err != nil {
		h.logger.Error("list crew templates", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []crewTemplateResponse
	for rows.Next() {
		var t crewTemplateResponse
		var agentsJSON string
		if err := rows.Scan(&t.ID, &t.Name, &t.Slug, &t.Description, &t.Icon, &t.Color,
			&t.Category, &agentsJSON, &t.IsBuiltin, &t.CreatedAt); err != nil {
			h.logger.Error("scan crew template", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		if err := json.Unmarshal([]byte(agentsJSON), &t.Agents); err != nil {
			h.logger.Warn("parse agents_json", "slug", t.Slug, "error", err)
			t.Agents = []database.CrewTemplateAgent{}
		}
		result = append(result, t)
	}
	if result == nil {
		result = []crewTemplateResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// Get handles GET /api/v1/crew-templates/{slug}
func (h *CrewTemplateHandler) Get(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	var t crewTemplateResponse
	var agentsJSON string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, name, slug, description, icon, color, category, agents_json, is_builtin, created_at
		FROM crew_templates WHERE slug = ?`, slug).Scan(
		&t.ID, &t.Name, &t.Slug, &t.Description, &t.Icon, &t.Color,
		&t.Category, &agentsJSON, &t.IsBuiltin, &t.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Template not found")
			return
		}
		h.logger.Error("get crew template", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if err := json.Unmarshal([]byte(agentsJSON), &t.Agents); err != nil {
		t.Agents = []database.CrewTemplateAgent{}
	}
	writeJSON(w, http.StatusOK, t)
}

// Deploy handles POST /api/v1/crew-templates/{slug}/deploy
// Creates a crew + all agents from the template in a single transaction.
func (h *CrewTemplateHandler) Deploy(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	wsID := WorkspaceIDFromContext(r.Context())

	var body struct {
		CrewName string `json:"crew_name"`
		CrewSlug string `json:"crew_slug"`
	}
	if err := readJSON(r, &body); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if body.CrewName == "" {
		writeProblem(w, r, http.StatusBadRequest, "crew_name is required")
		return
	}
	if body.CrewSlug == "" {
		body.CrewSlug = slugify(body.CrewName)
	}

	// Load template
	var agentsJSON string
	var icon, color *string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT agents_json, icon, color FROM crew_templates WHERE slug = ?`, slug).Scan(&agentsJSON, &icon, &color)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Template not found")
			return
		}
		h.logger.Error("load crew template", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	var agents []database.CrewTemplateAgent
	if err := json.Unmarshal([]byte(agentsJSON), &agents); err != nil {
		h.logger.Error("parse template agents", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Invalid template data")
		return
	}

	// Check crew slug uniqueness
	var existing int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM crews WHERE slug = ? AND workspace_id = ? AND deleted_at IS NULL`,
		body.CrewSlug, wsID).Scan(&existing); err != nil {
		h.logger.Error("check slug uniqueness", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if existing > 0 {
		writeProblem(w, r, http.StatusConflict, fmt.Sprintf("Crew with slug '%s' already exists", body.CrewSlug))
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback()

	crewID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO crews (id, workspace_id, name, slug, icon, color, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		crewID, wsID, body.CrewName, body.CrewSlug, icon, color, now, now)
	if err != nil {
		h.logger.Error("create crew from template", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to create crew")
		return
	}

	var agentIDs []string
	for _, a := range agents {
		agentID := generateCUID()
		agentIDs = append(agentIDs, agentID)
		webhookSecret := generateWebhookSecret()
		// Suffix agent slug with crew slug to avoid workspace-wide uniqueness conflicts
		// when the same template is deployed more than once.
		agentSlug := a.Slug + "-" + body.CrewSlug

		_, err = tx.ExecContext(r.Context(), `
			INSERT INTO agents (id, workspace_id, crew_id, name, slug, role_title, agent_role,
				cli_adapter, llm_provider, llm_model, tool_profile, system_prompt,
				timeout_seconds, memory_enabled, webhook_secret, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			agentID, wsID, crewID, a.Name, agentSlug, a.RoleTitle, a.AgentRole,
			a.CLIAdapter, a.LLMProvider, a.LLMModel, a.ToolProfile, a.SystemPrompt,
			1800, true, webhookSecret, now, now)
		if err != nil {
			h.logger.Error("create agent from template", "agent", agentSlug, "error", err)
			writeProblem(w, r, http.StatusInternalServerError, fmt.Sprintf("Failed to create agent %s", a.Name))
			return
		}
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit crew template deploy", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to deploy template")
		return
	}

	h.logger.Info("crew template deployed",
		"template", slug,
		"crew_id", crewID,
		"crew_name", body.CrewName,
		"agents", len(agents),
	)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"crew_id":     crewID,
		"crew_name":   body.CrewName,
		"crew_slug":   body.CrewSlug,
		"agent_count": len(agentIDs),
		"agent_ids":   agentIDs,
	})
}

func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	var out []byte
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			out = append(out, c)
		}
	}
	return string(out)
}

func generateWebhookSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return fmt.Sprintf("%x", b)
}
