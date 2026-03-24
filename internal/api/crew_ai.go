package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/llm"
)

var errNoActiveAnthropicCredential = errors.New("no active Anthropic credential in workspace")

type CrewAIHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewCrewAIHandler(db *sql.DB, logger *slog.Logger) *CrewAIHandler {
	return &CrewAIHandler{db: db, logger: logger}
}

type AISuggestedAgent struct {
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	RoleTitle    string `json:"role_title"`
	AgentRole    string `json:"agent_role"`
	SystemPrompt string `json:"system_prompt"`
}

type AISuggestResponse struct {
	CrewName    string             `json:"crew_name"`
	CrewSlug    string             `json:"crew_slug"`
	Description string             `json:"description"`
	Agents      []AISuggestedAgent `json:"agents"`
}

const crewDesignerSystemPrompt = `You are a crew designer for Crewship, an AI agent orchestration platform.
Given a user's description of what they want their crew to do, return a JSON object describing a crew of 3-5 specialized agents.

Rules:
- Return ONLY valid JSON. No markdown fences, no explanation.
- Exactly one agent must have agent_role="LEAD" — this is the coordinator.
- All other agents have agent_role="AGENT".
- Each slug must be lowercase, hyphenated, unique within the crew, max 30 chars.
- Each system_prompt should be 2-4 focused sentences describing what that agent does, its tools, and its working style.
- crew_slug must be lowercase, hyphenated, derived from crew_name.

JSON schema (return exactly this structure):
{
  "crew_name": "string",
  "crew_slug": "string",
  "description": "string (one sentence)",
  "agents": [
    {
      "name": "string",
      "slug": "string",
      "role_title": "string",
      "agent_role": "LEAD" | "AGENT",
      "system_prompt": "string"
    }
  ]
}`

// Suggest handles POST /api/v1/crew-ai-suggest
// Calls Anthropic API with workspace's API key to generate a crew definition.
func (h *CrewAIHandler) Suggest(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	var body struct {
		Description string `json:"description"`
	}
	if err := readJSON(r, &body); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	body.Description = strings.TrimSpace(body.Description)
	if len(body.Description) < 10 {
		writeProblem(w, r, http.StatusBadRequest, "description must be at least 10 characters")
		return
	}
	if len(body.Description) > 2000 {
		writeProblem(w, r, http.StatusBadRequest, "description must be at most 2000 characters")
		return
	}

	provider, err := h.getLLMProvider(r.Context(), wsID)
	if err != nil {
		if errors.Is(err, errNoActiveAnthropicCredential) {
			h.logger.Warn("no LLM credential for crew AI suggest", "workspace", wsID)
			writeProblem(w, r, http.StatusUnprocessableEntity,
				"No Anthropic API key found. Add a credential of type API_KEY / provider ANTHROPIC in Settings → Credentials. Note: Claude Code OAuth tokens cannot be used here — a plain API key (sk-ant-api*) is required.")
			return
		}
		h.logger.Error("load LLM provider for crew AI suggest", "workspace", wsID, "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to load AI provider")
		return
	}

	suggestion, err := h.suggest(r.Context(), provider, body.Description)
	if err != nil {
		h.logger.Error("crew AI suggest", "error", err)
		writeProblem(w, r, http.StatusBadGateway, "AI suggestion failed")
		return
	}

	writeJSON(w, http.StatusOK, suggestion)
}

// getLLMProvider fetches the first active Anthropic API_KEY for the workspace
// and returns an llm.Provider ready to use.
func (h *CrewAIHandler) getLLMProvider(ctx context.Context, wsID string) (llm.Provider, error) {
	var encryptedValue string
	err := h.db.QueryRowContext(ctx, `
		SELECT encrypted_value FROM credentials
		WHERE workspace_id = ?
		  AND provider = 'ANTHROPIC'
		  AND type = 'API_KEY'
		  AND status = 'ACTIVE'
		  AND deleted_at IS NULL
		ORDER BY created_at ASC
		LIMIT 1`, wsID).Scan(&encryptedValue)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errNoActiveAnthropicCredential
		}
		return nil, fmt.Errorf("query credential: %w", err)
	}
	plain, err := encryption.Decrypt(encryptedValue)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	return llm.NewAnthropic(plain), nil
}

// suggest calls the LLM provider to generate a crew definition from a description.
func (h *CrewAIHandler) suggest(ctx context.Context, provider llm.Provider, description string) (*AISuggestResponse, error) {
	resp, err := provider.Complete(ctx, llm.Request{
		Model:     "claude-3-5-haiku-20241022",
		System:    crewDesignerSystemPrompt,
		MaxTokens: 2048,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: description}},
	})
	if err != nil {
		return nil, fmt.Errorf("provider complete: %w", err)
	}

	rawJSON := strings.TrimSpace(resp.Content)
	if rawJSON == "" {
		return nil, fmt.Errorf("empty response from AI")
	}

	rawJSON = stripMarkdownFences(rawJSON)

	var suggestion AISuggestResponse
	if err := json.Unmarshal([]byte(rawJSON), &suggestion); err != nil {
		return nil, fmt.Errorf("AI returned invalid JSON: %w", err)
	}

	if err := validateSuggestion(&suggestion); err != nil {
		return nil, fmt.Errorf("AI response validation: %w", err)
	}

	return &suggestion, nil
}

func stripMarkdownFences(s string) string {
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func validateSuggestion(s *AISuggestResponse) error {
	if s.CrewName == "" {
		return fmt.Errorf("missing crew_name")
	}
	if len(s.Agents) < 2 || len(s.Agents) > 6 {
		return fmt.Errorf("expected 2-6 agents, got %d", len(s.Agents))
	}
	leads := 0
	for i := range s.Agents {
		if s.Agents[i].AgentRole == "LEAD" {
			leads++
		}
		if s.Agents[i].Name == "" || s.Agents[i].SystemPrompt == "" {
			return fmt.Errorf("agent missing required fields")
		}
		// Normalize slug; fall back to name-derived slug if empty or invalid
		s.Agents[i].Slug = slugify(s.Agents[i].Slug)
		if s.Agents[i].Slug == "" {
			s.Agents[i].Slug = slugify(s.Agents[i].Name)
		}
		if s.Agents[i].Slug == "" {
			return fmt.Errorf("agent %q has invalid slug", s.Agents[i].Name)
		}
	}
	if leads != 1 {
		return fmt.Errorf("expected exactly 1 LEAD agent, got %d", leads)
	}
	// Normalize crew_slug
	s.CrewSlug = slugify(s.CrewSlug)
	if s.CrewSlug == "" {
		s.CrewSlug = slugify(s.CrewName)
	}
	return nil
}
