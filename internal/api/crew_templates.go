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
	"github.com/crewship-ai/crewship/internal/journal"
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

// deployCrewTemplate is a package-level helper shared by CrewTemplateHandler and
// (deprecated) Captain tool executors. CrewTemplateHandler is the primary caller;
// the Captain executor is retained for backward compat only (Captain deprecated 2026-04-16).
// crewSlugInput may be empty — if so, it is derived from crewName via slugify.
//
// Pass a journal.Emitter (callers' h.journal — already defaulted to noopEmitter
// when nothing is wired up) so the template/Captain auto-assign trail lands in
// the canonical event stream alongside server logs.
func deployCrewTemplate(ctx context.Context, db *sql.DB, logger *slog.Logger, j journal.Emitter, wsID, templateSlug, crewName, crewSlugInput string) (*deployCrewResult, error) {
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
				cli_adapter, llm_provider, llm_model, tool_profile, system_prompt_legacy,
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
		autoAssignCredentials(ctx, db, logger, j, wsID, agentID, now)
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
// from Anthropic to the given agent. Best-effort: failures land both in the server log
// (logger.Warn — operators tail this for live debugging) AND in the canonical journal
// stream as `credential.auto_assign_failed` / `credential.auto_assign_empty` entries
// (so the workspace timeline shows why a freshly-deployed agent "runs but says nothing").
// Callers can still finish the parent operation and surface 201.
//
// j is required and called without nil-checks per project convention — pass
// noopEmitter{} in tests when journaling is irrelevant. Pass nil logger only in tests.
func autoAssignCredentials(ctx context.Context, db *sql.DB, logger *slog.Logger, j journal.Emitter, wsID, agentID, now string) {
	emitFailure := func(reason, credID string, err error) {
		payload := map[string]any{
			"workspace_id": wsID,
			"agent_id":     agentID,
			"reason":       reason,
		}
		if credID != "" {
			payload["credential_id"] = credID
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		if _, emitErr := j.Emit(ctx, journal.Entry{
			WorkspaceID: wsID,
			AgentID:     agentID,
			Type:        journal.EntryCredentialAutoAssignFailed,
			Severity:    journal.SeverityWarn,
			ActorType:   journal.ActorSystem,
			Summary:     fmt.Sprintf("auto-assign credential failed (%s)", reason),
			Payload:     payload,
		}); emitErr != nil && logger != nil {
			logger.Warn("autoAssignCredentials: journal emit failed",
				"workspace_id", wsID, "agent_id", agentID, "error", emitErr)
		}
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, name FROM credentials
		WHERE workspace_id = ? AND type IN ('API_KEY', 'AI_CLI_TOKEN')
		  AND provider = 'ANTHROPIC' AND deleted_at IS NULL
		ORDER BY created_at ASC`, wsID)
	if err != nil {
		if logger != nil {
			logger.Warn("autoAssignCredentials: list query failed",
				"workspace_id", wsID, "agent_id", agentID, "error", err)
		}
		emitFailure("list_query", "", err)
		return
	}
	defer rows.Close()
	// credentialsFound flips on the first successful Scan, so it stays
	// false ONLY when the workspace has zero matching credential rows.
	// We deliberately don't track "assigned count" for the empty-event
	// gate: if rows existed but every insert failed, those are already
	// covered by per-row credential.auto_assign_failed entries — firing
	// the "empty" entry there too would misreport "no credentials"
	// when really there were credentials we just couldn't link.
	credentialsFound := false
	for rows.Next() {
		var credID, credName string
		if err := rows.Scan(&credID, &credName); err != nil {
			if logger != nil {
				logger.Warn("autoAssignCredentials: scan failed",
					"workspace_id", wsID, "agent_id", agentID, "error", err)
			}
			emitFailure("scan", "", err)
			continue
		}
		credentialsFound = true
		if _, err := db.ExecContext(ctx, `
			INSERT OR IGNORE INTO agent_credentials (agent_id, credential_id, env_var_name, created_at)
			VALUES (?, ?, ?, ?)`, agentID, credID, credName, now); err != nil {
			if logger != nil {
				logger.Warn("autoAssignCredentials: insert failed",
					"workspace_id", wsID, "agent_id", agentID,
					"credential_id", credID, "error", err)
			}
			emitFailure("insert", credID, err)
			continue
		}
	}
	// Surface late cursor failures (network blip, conn drop mid-iteration)
	// — without this, partial assignments could be reported as success.
	if err := rows.Err(); err != nil {
		if logger != nil {
			logger.Warn("autoAssignCredentials: row iteration error",
				"workspace_id", wsID, "agent_id", agentID, "error", err)
		}
		emitFailure("row_iteration", "", err)
	}
	if !credentialsFound {
		// Most common cause of "agent created but chat returns empty":
		// no Anthropic creds in the workspace yet. Log so operators
		// know to add a credential, and surface in the journal so it
		// shows up alongside the agent.created entry in the timeline.
		if logger != nil {
			logger.Warn("autoAssignCredentials: no Anthropic credentials available — agent will need manual assignment to chat",
				"workspace_id", wsID, "agent_id", agentID)
		}
		if _, err := j.Emit(ctx, journal.Entry{
			WorkspaceID: wsID,
			AgentID:     agentID,
			Type:        journal.EntryCredentialAutoAssignEmpty,
			Severity:    journal.SeverityWarn,
			ActorType:   journal.ActorSystem,
			Summary:     "no Anthropic credentials available — agent will need manual assignment",
			Payload: map[string]any{
				"workspace_id": wsID,
				"agent_id":     agentID,
			},
		}); err != nil && logger != nil {
			logger.Warn("autoAssignCredentials: journal emit failed",
				"workspace_id", wsID, "agent_id", agentID, "error", err)
		}
	}
}

// CrewTemplateHandler provides endpoints for listing and applying crew templates.
type CrewTemplateHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	journal journal.Emitter
}

// NewCrewTemplateHandler creates a CrewTemplateHandler with the given database and logger.
// Journal emitter defaults to noopEmitter — call SetJournal to wire up the real one.
func NewCrewTemplateHandler(db *sql.DB, logger *slog.Logger) *CrewTemplateHandler {
	return &CrewTemplateHandler{db: db, logger: logger, journal: noopEmitter{}}
}

// SetJournal attaches the canonical event-stream emitter; credential auto-assign
// failures from this handler land in `journal_entries` so the workspace timeline
// reflects them. Defaults to noopEmitter when not called (e.g. tests).
func (h *CrewTemplateHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

type crewTemplateResponse struct {
	ID          string                       `json:"id"`
	Name        string                       `json:"name"`
	Slug        string                       `json:"slug"`
	Description *string                      `json:"description"`
	Icon        *string                      `json:"icon"`
	Color       *string                      `json:"color"`
	Category    string                       `json:"category"`
	Agents      []database.CrewTemplateAgent `json:"agents"`
	IsBuiltin   bool                         `json:"is_builtin"`
	CreatedAt   string                       `json:"created_at"`
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

	result, err := deployCrewTemplate(r.Context(), h.db, h.logger, h.journal, wsID, slug, body.CrewName, body.CrewSlug)
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
