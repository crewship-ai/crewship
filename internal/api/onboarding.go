package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

type OnboardingHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewOnboardingHandler(db *sql.DB, logger *slog.Logger) *OnboardingHandler {
	return &OnboardingHandler{db: db, logger: logger}
}

func (h *OnboardingHandler) Status(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	var completed bool
	err := h.db.QueryRowContext(r.Context(),
		"SELECT onboarding_completed FROM users WHERE id = ?", user.ID).Scan(&completed)
	if err != nil {
		h.logger.Error("query onboarding status", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"completed": completed,
	})
}

func (h *OnboardingHandler) Complete(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	_, err := h.db.ExecContext(r.Context(),
		"UPDATE users SET onboarding_completed = 1, updated_at = ? WHERE id = ?",
		time.Now().UTC().Format(time.RFC3339), user.ID)
	if err != nil {
		h.logger.Error("complete onboarding", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "completed"})
}

type onboardingSetupRequest struct {
	WorkspaceName string `json:"workspace_name"`
	CrewName      string `json:"crew_name"`
	AgentName     string `json:"agent_name"`
	CliAdapter    string `json:"cli_adapter"`
	LlmProvider   string `json:"llm_provider"`
	LlmModel      string `json:"llm_model"`
	CredentialName  string `json:"credential_name"`
	CredentialValue string `json:"credential_value"`
}

var slugRegex = regexp.MustCompile(`[^a-z0-9-]`)

func makeSlug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = slugRegex.ReplaceAllString(s, "-")
	s = regexp.MustCompile(`-+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "default"
	}
	return s
}

func (h *OnboardingHandler) Setup(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	var req onboardingSetupRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if req.CrewName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crew_name is required"})
		return
	}
	if req.AgentName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_name is required"})
		return
	}

	// Get user's first workspace
	var workspaceID string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT wm.workspace_id FROM workspace_members wm
		WHERE wm.user_id = ? ORDER BY wm.created_at ASC LIMIT 1
	`, user.ID).Scan(&workspaceID)
	if err != nil {
		h.logger.Error("find workspace", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "No workspace found"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer tx.Rollback()

	// Update workspace name if provided
	if req.WorkspaceName != "" {
		_, err = tx.ExecContext(r.Context(),
			"UPDATE workspaces SET name = ?, updated_at = ? WHERE id = ?",
			req.WorkspaceName, now, workspaceID)
		if err != nil {
			h.logger.Error("update workspace name", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	// Create crew
	crewID := generateCUID()
	crewSlug := makeSlug(req.CrewName)
	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, crewID, workspaceID, req.CrewName, crewSlug, now, now)
	if err != nil {
		h.logger.Error("insert crew", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create crew"})
		return
	}

	// Add user as crew member
	crewMemberID := generateCUID()
	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO crew_members (id, crew_id, user_id, created_at)
		VALUES (?, ?, ?, ?)
	`, crewMemberID, crewID, user.ID, now)
	if err != nil {
		h.logger.Error("insert crew member", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Create agent
	cliAdapter := req.CliAdapter
	if cliAdapter == "" {
		cliAdapter = "CLAUDE_CODE"
	}
	agentID := generateCUID()
	agentSlug := makeSlug(req.AgentName)
	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO agents (id, crew_id, workspace_id, name, slug, cli_adapter, llm_provider, llm_model, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, agentID, crewID, workspaceID, req.AgentName, agentSlug, cliAdapter,
		nullableString(req.LlmProvider), nullableString(req.LlmModel), now, now)
	if err != nil {
		h.logger.Error("insert agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create agent"})
		return
	}

	// Create credential if provided
	var credentialID string
	if req.CredentialValue != "" {
		credentialID = generateCUID()
		credName := req.CredentialName
		if credName == "" {
			credName = "API Key"
		}

		provider := "ANTHROPIC"
		if strings.EqualFold(req.LlmProvider, "OPENAI") {
			provider = "OPENAI"
		} else if strings.EqualFold(req.LlmProvider, "GOOGLE") {
			provider = "GOOGLE"
		}

		encryptedValue, encErr := encryption.Encrypt(req.CredentialValue)
		if encErr != nil {
			h.logger.Error("encrypt credential", "error", encErr)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to encrypt credential"})
			return
		}

		_, err = tx.ExecContext(r.Context(), `
			INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'AI_CLI_TOKEN', ?, 'WORKSPACE', ?, ?, ?)
		`, credentialID, workspaceID, credName, encryptedValue, provider, user.ID, now, now)
		if err != nil {
			h.logger.Error("insert credential", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create credential"})
			return
		}

		// Assign credential to agent
		assignmentID := generateCUID()
		envVarName := "ANTHROPIC_API_KEY"
		if strings.EqualFold(req.LlmProvider, "OPENAI") {
			envVarName = "OPENAI_API_KEY"
		} else if strings.EqualFold(req.LlmProvider, "GOOGLE") {
			envVarName = "GOOGLE_API_KEY"
		}

		_, err = tx.ExecContext(r.Context(), `
			INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
			VALUES (?, ?, ?, ?, 0, ?)
		`, assignmentID, agentID, credentialID, envVarName, now)
		if err != nil {
			h.logger.Error("assign credential", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to assign credential"})
			return
		}
	}

	// Mark onboarding complete
	_, err = tx.ExecContext(r.Context(),
		"UPDATE users SET onboarding_completed = 1, updated_at = ? WHERE id = ?",
		now, user.ID)
	if err != nil {
		h.logger.Error("mark onboarding complete", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"workspace_id":  workspaceID,
		"crew_id":       crewID,
		"agent_id":      agentID,
		"credential_id": credentialID,
	})
}

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
