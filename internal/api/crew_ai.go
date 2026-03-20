package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

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

	cred, err := h.getAnthropicCred(r.Context(), wsID)
	if err != nil {
		h.logger.Warn("no anthropic API key for crew AI suggest", "workspace", wsID, "error", err)
		writeProblem(w, r, http.StatusUnprocessableEntity,
			"No Anthropic API key found. Add a credential of type API_KEY / provider ANTHROPIC in Settings → Credentials. Note: Claude Code OAuth tokens cannot be used here — a plain API key (sk-ant-api*) is required.")
		return
	}

	suggestion, err := h.callAnthropic(r.Context(), cred, body.Description)
	if err != nil {
		h.logger.Error("anthropic crew suggest", "error", err)
		writeProblem(w, r, http.StatusBadGateway, "AI suggestion failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, suggestion)
}

type anthropicCred struct {
	plain string
}

// getAnthropicCred fetches and decrypts the first active Anthropic API_KEY for the workspace.
// Note: AI_CLI_TOKEN (OAuth sk-ant-oat*) cannot be used for direct Messages API calls —
// Anthropic does not support OAuth for their REST API. Only API_KEY works here.
func (h *CrewAIHandler) getAnthropicCred(ctx context.Context, wsID string) (*anthropicCred, error) {
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
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no active Anthropic credential in workspace")
		}
		return nil, fmt.Errorf("query credential: %w", err)
	}
	plain, err := encryption.Decrypt(encryptedValue)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	return &anthropicCred{plain: plain}, nil
}

// callAnthropic sends the user description to Claude and parses the JSON response.
func (h *CrewAIHandler) callAnthropic(ctx context.Context, cred *anthropicCred, description string) (*AISuggestResponse, error) {
	reqBody, err := json.Marshal(map[string]interface{}{
		"model":      "claude-3-5-haiku-20241022",
		"max_tokens": 2048,
		"system":     crewDesignerSystemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": description},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-api-key", cred.plain)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("invalid Anthropic API key — update the credential in Settings → Credentials")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("Anthropic rate limit exceeded, try again in a moment")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Anthropic API returned %d", resp.StatusCode)
	}

	// Parse Anthropic Messages API response
	var anthropicResp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBytes, &anthropicResp); err != nil {
		return nil, fmt.Errorf("parse anthropic response: %w", err)
	}

	var rawJSON string
	for _, block := range anthropicResp.Content {
		if block.Type == "text" {
			rawJSON = strings.TrimSpace(block.Text)
			break
		}
	}
	if rawJSON == "" {
		return nil, fmt.Errorf("empty response from AI")
	}

	// Strip any accidental markdown code fences
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
	for _, a := range s.Agents {
		if a.AgentRole == "LEAD" {
			leads++
		}
		if a.Name == "" || a.Slug == "" || a.SystemPrompt == "" {
			return fmt.Errorf("agent missing required fields")
		}
	}
	if leads != 1 {
		return fmt.Errorf("expected exactly 1 LEAD agent, got %d", leads)
	}
	// Auto-fill crew_slug if AI forgot it
	if s.CrewSlug == "" {
		s.CrewSlug = slugify(s.CrewName)
	}
	return nil
}
