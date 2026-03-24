package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

// errTemplateNotFound is returned by deployCrewTemplate when the slug doesn't exist.
var errTemplateNotFound = errors.New("template not found")

// errCrewSlugConflict is returned by deployCrewTemplate when the crew slug is already taken.
var errCrewSlugConflict = errors.New("crew slug already exists")

type deployCrewResult struct {
	CrewID     string   `json:"crew_id"`
	CrewName   string   `json:"crew_name"`
	CrewSlug   string   `json:"crew_slug"`
	AgentCount int      `json:"agent_count"`
	AgentIDs   []string `json:"agent_ids"`
}

// deployCrewTemplate is a package-level helper shared by CrewTemplateHandler and captain tool executors.
// crewSlugInput may be empty — if so, it is derived from crewName via slugify.
func deployCrewTemplate(ctx context.Context, db *sql.DB, wsID, templateSlug, crewName, crewSlugInput string) (*deployCrewResult, error) {
	crewSlug := crewSlugInput
	if crewSlug == "" {
		crewSlug = slugify(crewName)
	} else {
		crewSlug = slugify(crewSlug)
	}
	if crewSlug == "" {
		return nil, fmt.Errorf("%w: crew_slug must contain only lowercase letters, numbers, and hyphens", errCrewSlugConflict)
	}

	var agentsJSON string
	var icon, color *string
	err := db.QueryRowContext(ctx, `
		SELECT agents_json, icon, color FROM crew_templates
		WHERE slug = ? AND (is_builtin = 1 OR workspace_id = ?)`, templateSlug, wsID).Scan(&agentsJSON, &icon, &color)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errTemplateNotFound
		}
		return nil, fmt.Errorf("load template: %w", err)
	}

	var agents []database.CrewTemplateAgent
	if err := json.Unmarshal([]byte(agentsJSON), &agents); err != nil {
		return nil, fmt.Errorf("parse template agents: %w", err)
	}

	var existing int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM crews WHERE slug = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewSlug, wsID).Scan(&existing); err != nil {
		return nil, fmt.Errorf("check slug uniqueness: %w", err)
	}
	if existing > 0 {
		return nil, fmt.Errorf("%w: '%s'", errCrewSlugConflict, crewSlug)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	crewID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err = tx.ExecContext(ctx, `
		INSERT INTO crews (id, workspace_id, name, slug, icon, color, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		crewID, wsID, crewName, crewSlug, icon, color, now, now); err != nil {
		return nil, fmt.Errorf("create crew: %w", err)
	}

	var agentIDs []string
	for _, a := range agents {
		agentID := generateCUID()
		agentIDs = append(agentIDs, agentID)
		webhookSecret := generateWebhookSecret()
		agentSlug := a.Slug + "-" + crewSlug

		if _, err = tx.ExecContext(ctx, `
			INSERT INTO agents (id, workspace_id, crew_id, name, slug, role_title, agent_role,
				cli_adapter, llm_provider, llm_model, tool_profile, system_prompt,
				timeout_seconds, memory_enabled, webhook_secret, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			agentID, wsID, crewID, a.Name, agentSlug, a.RoleTitle, a.AgentRole,
			a.CLIAdapter, a.LLMProvider, a.LLMModel, a.ToolProfile, a.SystemPrompt,
			1800, true, webhookSecret, now, now); err != nil {
			return nil, fmt.Errorf("create agent %s: %w", a.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	// Auto-assign workspace AI credentials to all new agents (best-effort, after commit).
	for _, agentID := range agentIDs {
		autoAssignCredentials(ctx, db, wsID, agentID, now)
	}

	return &deployCrewResult{
		CrewID:     crewID,
		CrewName:   crewName,
		CrewSlug:   crewSlug,
		AgentCount: len(agentIDs),
		AgentIDs:   agentIDs,
	}, nil
}

// autoAssignCredentials assigns all workspace-scoped AI credentials (API_KEY, AI_CLI_TOKEN)
// from Anthropic to the given agent. Errors are silently ignored since this is a best-effort
// convenience — the agent can still be manually assigned credentials later.
func autoAssignCredentials(ctx context.Context, db *sql.DB, wsID, agentID, now string) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, name FROM credentials
		WHERE workspace_id = ? AND type IN ('API_KEY', 'AI_CLI_TOKEN')
		  AND provider = 'ANTHROPIC' AND deleted_at IS NULL
		ORDER BY created_at ASC`, wsID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var credID, credName string
		if err := rows.Scan(&credID, &credName); err != nil {
			continue
		}
		_, _ = db.ExecContext(ctx, `
			INSERT OR IGNORE INTO agent_credentials (agent_id, credential_id, env_var_name, created_at)
			VALUES (?, ?, ?, ?)`, agentID, credID, credName, now)
	}
}

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

	seedCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := database.SeedBuiltinCrewTemplates(seedCtx, h.db, h.logger); err != nil {
		h.logger.Warn("seed crew templates", "error", err)
	}

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
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate crew templates", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if result == nil {
		result = []crewTemplateResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// Get handles GET /api/v1/crew-templates/{slug}
func (h *CrewTemplateHandler) Get(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")

	var t crewTemplateResponse
	var agentsJSON string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, name, slug, description, icon, color, category, agents_json, is_builtin, created_at
		FROM crew_templates WHERE slug = ? AND (is_builtin = 1 OR workspace_id = ?)`, slug, wsID).Scan(
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
	body.CrewName = strings.TrimSpace(body.CrewName)
	if body.CrewName == "" {
		writeProblem(w, r, http.StatusBadRequest, "crew_name is required")
		return
	}

	result, err := deployCrewTemplate(r.Context(), h.db, wsID, slug, body.CrewName, body.CrewSlug)
	if err != nil {
		if errors.Is(err, errTemplateNotFound) {
			writeProblem(w, r, http.StatusNotFound, "Template not found")
			return
		}
		if errors.Is(err, errCrewSlugConflict) {
			writeProblem(w, r, http.StatusConflict, err.Error())
			return
		}
		h.logger.Error("deploy crew template", "template", slug, "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to deploy template")
		return
	}

	h.logger.Info("crew template deployed",
		"template", slug,
		"crew_id", result.CrewID,
		"crew_name", result.CrewName,
		"agents", result.AgentCount,
	)

	writeJSON(w, http.StatusCreated, result)
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
	// Collapse consecutive hyphens and trim leading/trailing hyphens (matches frontend)
	result := string(out)
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return strings.Trim(result, "-")
}

func generateWebhookSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return fmt.Sprintf("%x", b)
}
