package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/services"
)

var dashRegex = regexp.MustCompile(`-+`)

type llmProviderInfo struct {
	provider   string
	envVarName string
}

// resolveLLMProvider maps a user-supplied provider string to its canonical
// (provider, envVarName) pair. Mirrors the multi-CLI registry in
// lib/cli-adapters.ts — Cursor + Factory added with the multi-CLI wave so
// the onboarding wizard's provider step doesn't silently coerce them to
// ANTHROPIC. Unknown values still fall back to ANTHROPIC for backward
// compatibility (pre-multi-CLI behaviour).
func resolveLLMProvider(provider string) llmProviderInfo {
	switch strings.ToUpper(provider) {
	case "OPENAI":
		return llmProviderInfo{provider: "OPENAI", envVarName: "OPENAI_API_KEY"}
	case "GOOGLE":
		return llmProviderInfo{provider: "GOOGLE", envVarName: "GOOGLE_API_KEY"}
	case "CURSOR":
		return llmProviderInfo{provider: "CURSOR", envVarName: "CURSOR_API_KEY"}
	case "FACTORY":
		return llmProviderInfo{provider: "FACTORY", envVarName: "FACTORY_API_KEY"}
	case "ANTHROPIC":
		return llmProviderInfo{provider: "ANTHROPIC", envVarName: "ANTHROPIC_API_KEY"}
	default:
		return llmProviderInfo{provider: "ANTHROPIC", envVarName: "ANTHROPIC_API_KEY"}
	}
}

// OnboardingHandler guides new users through workspace setup (runtime detection, crew creation).
type OnboardingHandler struct {
	db     *sql.DB
	svc    *services.OnboardingService
	logger *slog.Logger
}

// NewOnboardingHandler creates an OnboardingHandler with the given dependencies.
func NewOnboardingHandler(db *sql.DB, svc *services.OnboardingService, logger *slog.Logger) *OnboardingHandler {
	return &OnboardingHandler{db: db, svc: svc, logger: logger}
}

// Status returns whether the current user has completed onboarding.
// GET /api/v1/onboarding/status
func (h *OnboardingHandler) Status(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	var completed bool
	err := h.db.QueryRowContext(r.Context(),
		"SELECT onboarding_completed FROM users WHERE id = ?", user.ID).Scan(&completed)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "User not found"})
		return
	}
	if err != nil {
		h.logger.Error("query onboarding status", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Smart detect: if user already has agents (e.g. provisioned via CLI), treat as completed
	if !completed {
		var agentCount int
		err = h.db.QueryRowContext(r.Context(), `
			SELECT COUNT(*) FROM agents a
			JOIN workspace_members wm ON wm.workspace_id = a.workspace_id
			WHERE wm.user_id = ? AND a.deleted_at IS NULL
		`, user.ID).Scan(&agentCount)
		if err == nil && agentCount > 0 {
			completed = true
			// Persist the flag so we don't re-query next time
			if _, err := h.db.ExecContext(r.Context(),
				"UPDATE users SET onboarding_completed = 1, updated_at = ? WHERE id = ?",
				time.Now().UTC().Format(time.RFC3339), user.ID); err != nil {
				h.logger.Warn("persist onboarding flag", "error", err, "user_id", user.ID)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"completed": completed,
	})
}

// Complete marks onboarding as finished for the current user.
// POST /api/v1/onboarding/complete
func (h *OnboardingHandler) Complete(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		"UPDATE users SET onboarding_completed = 1, updated_at = ? WHERE id = ?",
		time.Now().UTC().Format(time.RFC3339), user.ID)
	if err != nil {
		h.logger.Error("complete onboarding", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	rows, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("complete onboarding rows affected", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if rows == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "User not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "completed"})
}

type onboardingSetupRequest struct {
	WorkspaceName   string `json:"workspace_name"`
	CrewName        string `json:"crew_name"`
	AgentName       string `json:"agent_name"`
	CliAdapter      string `json:"cli_adapter"`
	LlmProvider     string `json:"llm_provider"`
	LlmModel        string `json:"llm_model"`
	CredentialName  string `json:"credential_name"`
	CredentialValue string `json:"credential_value"`
}

var slugRegex = regexp.MustCompile(`[^a-z0-9-]`)

func makeSlug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = slugRegex.ReplaceAllString(s, "-")
	s = dashRegex.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "default"
	}
	return s
}

// Setup handles the onboarding wizard submission (crew creation, agent provisioning, credential setup).
// POST /api/v1/onboarding/setup
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
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No workspace found for user"})
		return
	}
	if err != nil {
		h.logger.Error("find workspace", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	cliAdapter := req.CliAdapter
	if cliAdapter == "" {
		cliAdapter = "CLAUDE_CODE"
	}
	llm := resolveLLMProvider(req.LlmProvider)
	credName := req.CredentialName
	if credName == "" && req.CredentialValue != "" {
		credName = "API Key"
	}

	result, err := h.svc.Setup(r.Context(), services.SetupParams{
		UserID:          user.ID,
		WorkspaceID:     workspaceID,
		WorkspaceName:   req.WorkspaceName,
		CrewName:        req.CrewName,
		CrewSlug:        makeSlug(req.CrewName),
		AgentName:       req.AgentName,
		AgentSlug:       makeSlug(req.AgentName),
		CliAdapter:      cliAdapter,
		LLMProvider:     llm.provider,
		LLMModel:        stringPtr(req.LlmModel),
		EnvVarName:      llm.envVarName,
		CredentialName:  credName,
		CredentialValue: req.CredentialValue,
		Now:             time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		switch {
		case errors.Is(err, services.ErrOnboardingAlreadyCompleted):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Onboarding already completed"})
		case errors.Is(err, services.ErrWorkspaceNotFound):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No workspace found for user"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		}
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
