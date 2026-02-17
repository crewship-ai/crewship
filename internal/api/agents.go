package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"time"
)

type AgentHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewAgentHandler(db *sql.DB, logger *slog.Logger) *AgentHandler {
	return &AgentHandler{db: db, logger: logger}
}

type agentResponse struct {
	ID              string  `json:"id"`
	CrewID          *string `json:"crew_id"`
	WorkspaceID     string  `json:"workspace_id"`
	Name            string  `json:"name"`
	Slug            string  `json:"slug"`
	Description     *string `json:"description"`
	RoleTitle       *string `json:"role_title"`
	AgentRole       string  `json:"agent_role"`
	Status          string  `json:"status"`
	CLIAdapter      string  `json:"cli_adapter"`
	LLMProvider     *string `json:"llm_provider"`
	LLMModel        *string `json:"llm_model"`
	SystemPrompt    *string `json:"system_prompt"`
	Temperature     float64 `json:"temperature"`
	MaxTokens       *int    `json:"max_tokens"`
	TimeoutSeconds  int     `json:"timeout_seconds"`
	ToolProfile     string  `json:"tool_profile"`
	MemoryEnabled   bool    `json:"memory_enabled"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

func (h *AgentHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id is required"})
		return
	}

	crewID := r.URL.Query().Get("crew_id")

	var rows *sql.Rows
	var err error

	if crewID != "" {
		rows, err = h.db.QueryContext(r.Context(), `
			SELECT id, crew_id, workspace_id, name, slug, description, role_title,
				agent_role, status, cli_adapter, llm_provider, llm_model,
				system_prompt, temperature, max_tokens, timeout_seconds,
				tool_profile, memory_enabled, created_at, updated_at
			FROM agents
			WHERE workspace_id = ? AND crew_id = ? AND deleted_at IS NULL
			ORDER BY created_at DESC
		`, workspaceID, crewID)
	} else {
		rows, err = h.db.QueryContext(r.Context(), `
			SELECT id, crew_id, workspace_id, name, slug, description, role_title,
				agent_role, status, cli_adapter, llm_provider, llm_model,
				system_prompt, temperature, max_tokens, timeout_seconds,
				tool_profile, memory_enabled, created_at, updated_at
			FROM agents
			WHERE workspace_id = ? AND deleted_at IS NULL
			ORDER BY created_at DESC
		`, workspaceID)
	}

	if err != nil {
		h.logger.Error("list agents", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []agentResponse
	for rows.Next() {
		var a agentResponse
		var memEnabled int
		if err := rows.Scan(&a.ID, &a.CrewID, &a.WorkspaceID, &a.Name, &a.Slug,
			&a.Description, &a.RoleTitle, &a.AgentRole, &a.Status, &a.CLIAdapter,
			&a.LLMProvider, &a.LLMModel, &a.SystemPrompt, &a.Temperature,
			&a.MaxTokens, &a.TimeoutSeconds, &a.ToolProfile, &memEnabled,
			&a.CreatedAt, &a.UpdatedAt); err != nil {
			h.logger.Error("scan agent", "error", err)
			continue
		}
		a.MemoryEnabled = memEnabled == 1
		result = append(result, a)
	}

	if result == nil {
		result = []agentResponse{}
	}

	writeJSON(w, http.StatusOK, result)
}

type createAgentRequest struct {
	Name           string  `json:"name"`
	Slug           string  `json:"slug"`
	CrewID         *string `json:"crew_id"`
	Description    *string `json:"description"`
	RoleTitle      *string `json:"role_title"`
	AgentRole      string  `json:"agent_role"`
	CLIAdapter     string  `json:"cli_adapter"`
	LLMProvider    *string `json:"llm_provider"`
	LLMModel       *string `json:"llm_model"`
	SystemPrompt   *string `json:"system_prompt"`
	Temperature    float64 `json:"temperature"`
	MaxTokens      *int    `json:"max_tokens"`
	TimeoutSeconds int     `json:"timeout_seconds"`
	ToolProfile    string  `json:"tool_profile"`
	MemoryEnabled  bool    `json:"memory_enabled"`
}

func (h *AgentHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var req createAgentRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if req.Name == "" || len(req.Name) < 2 || len(req.Name) > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be 2-100 characters"})
		return
	}
	if req.Slug == "" || len(req.Slug) < 2 || len(req.Slug) > 50 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug must be 2-50 characters"})
		return
	}

	if req.AgentRole == "" {
		req.AgentRole = "AGENT"
	}
	if req.CLIAdapter == "" {
		req.CLIAdapter = "CLAUDE_CODE"
	}
	if req.ToolProfile == "" {
		req.ToolProfile = "CODING"
	}
	if req.Temperature == 0 {
		req.Temperature = 0.7
	}
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = 1800
	}

	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE workspace_id = ? AND slug = ?", workspaceID, req.Slug).Scan(&existingID)
	if err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Agent slug already taken in this workspace"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	agentID := generateCUID()
	memEnabled := 0
	if req.MemoryEnabled {
		memEnabled = 1
	}

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO agents (id, crew_id, workspace_id, name, slug, description, role_title,
			agent_role, status, cli_adapter, llm_provider, llm_model, system_prompt,
			temperature, max_tokens, timeout_seconds, tool_profile, memory_enabled,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'IDLE', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID, req.CrewID, workspaceID, req.Name, req.Slug, req.Description, req.RoleTitle,
		req.AgentRole, req.CLIAdapter, req.LLMProvider, req.LLMModel, req.SystemPrompt,
		req.Temperature, req.MaxTokens, req.TimeoutSeconds, req.ToolProfile, memEnabled,
		now, now)
	if err != nil {
		h.logger.Error("insert agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusCreated, agentResponse{
		ID:             agentID,
		CrewID:         req.CrewID,
		WorkspaceID:    workspaceID,
		Name:           req.Name,
		Slug:           req.Slug,
		Description:    req.Description,
		RoleTitle:      req.RoleTitle,
		AgentRole:      req.AgentRole,
		Status:         "IDLE",
		CLIAdapter:     req.CLIAdapter,
		LLMProvider:    req.LLMProvider,
		LLMModel:       req.LLMModel,
		SystemPrompt:   req.SystemPrompt,
		Temperature:    req.Temperature,
		MaxTokens:      req.MaxTokens,
		TimeoutSeconds: req.TimeoutSeconds,
		ToolProfile:    req.ToolProfile,
		MemoryEnabled:  req.MemoryEnabled,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
}

func (h *AgentHandler) Get(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	if agentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agentId is required"})
		return
	}

	workspaceID := WorkspaceIDFromContext(r.Context())

	var a agentResponse
	var memEnabled int
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, crew_id, workspace_id, name, slug, description, role_title,
			agent_role, status, cli_adapter, llm_provider, llm_model,
			system_prompt, temperature, max_tokens, timeout_seconds,
			tool_profile, memory_enabled, created_at, updated_at
		FROM agents
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL
	`, agentID, workspaceID).Scan(&a.ID, &a.CrewID, &a.WorkspaceID, &a.Name, &a.Slug,
		&a.Description, &a.RoleTitle, &a.AgentRole, &a.Status, &a.CLIAdapter,
		&a.LLMProvider, &a.LLMModel, &a.SystemPrompt, &a.Temperature,
		&a.MaxTokens, &a.TimeoutSeconds, &a.ToolProfile, &memEnabled,
		&a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("get agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	a.MemoryEnabled = memEnabled == 1

	writeJSON(w, http.StatusOK, a)
}
