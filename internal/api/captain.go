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
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/llm"
)

// MissionStarter can start a mission that is already inserted in PLANNING/IN_PROGRESS status.
// *orchestrator.MissionEngine satisfies this interface.
type MissionStarter interface {
	StartMission(ctx context.Context, missionID string) error
}

// captainRateLimiter is a per-user sliding window rate limiter (20 req/min).
var captainRateLimiter = struct {
	mu      sync.Mutex
	windows map[string][]time.Time
}{windows: make(map[string][]time.Time)}

const (
	captainRateLimit  = 20
	captainRateWindow = time.Minute
)

func captainCheckRateLimit(userID string) bool {
	now := time.Now()
	captainRateLimiter.mu.Lock()
	defer captainRateLimiter.mu.Unlock()

	ts := captainRateLimiter.windows[userID]
	cutoff := now.Add(-captainRateWindow)
	valid := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	if len(valid) >= captainRateLimit {
		captainRateLimiter.windows[userID] = valid
		return false
	}
	captainRateLimiter.windows[userID] = append(valid, now)
	return true
}

type CaptainHandler struct {
	db            *sql.DB
	logger        *slog.Logger
	provider      llm.Provider
	missionEngine MissionStarter
}

func NewCaptainHandler(db *sql.DB, logger *slog.Logger) *CaptainHandler {
	return &CaptainHandler{db: db, logger: logger}
}

func (h *CaptainHandler) SetProvider(p llm.Provider) {
	h.provider = p
}

func (h *CaptainHandler) SetMissionEngine(ms MissionStarter) {
	h.missionEngine = ms
}

func captainChatID(wsID, userID string) string {
	return "cap_" + wsID + "_" + userID
}

func (h *CaptainHandler) loadHistory(ctx context.Context, chatID string) ([]llm.Message, error) {
	var msgsJSON string
	err := h.db.QueryRowContext(ctx, "SELECT messages_json FROM captain_chats WHERE id = ?", chatID).Scan(&msgsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var msgs []llm.Message
	if err := json.Unmarshal([]byte(msgsJSON), &msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}

func (h *CaptainHandler) saveHistory(ctx context.Context, chatID, wsID, userID string, msgs []llm.Message) error {
	data, err := json.Marshal(msgs)
	if err != nil {
		return err
	}
	_, err = h.db.ExecContext(ctx, `
		INSERT INTO captain_chats (id, workspace_id, user_id, messages_json)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			messages_json = excluded.messages_json,
			updated_at = datetime('now')
	`, chatID, wsID, userID, string(data))
	return err
}

// writeCaptainSSE writes a single SSE data frame and flushes.
func writeCaptainSSE(w http.ResponseWriter, payload map[string]any) {
	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// Chat handles POST /api/v1/captain/chat with SSE streaming and tool execution loop.
func (h *CaptainHandler) Chat(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	if !captainCheckRateLimit(user.ID) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Rate limit exceeded: 20 messages per minute"})
		return
	}

	var body struct {
		Message string `json:"message"`
		ChatID  string `json:"chat_id,omitempty"`
	}
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Message) == "" {
		writeProblem(w, r, http.StatusBadRequest, "message is required")
		return
	}

	chatID := body.ChatID
	if chatID == "" {
		chatID = captainChatID(wsID, user.ID)
	}

	msgs, err := h.loadHistory(r.Context(), chatID)
	if err != nil {
		h.logger.Error("captain: load history", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	provider, err := h.getProvider(r, wsID)
	if err != nil {
		h.logger.Warn("captain: no LLM provider", "workspace", wsID, "error", err)
		writeProblem(w, r, http.StatusUnprocessableEntity,
			"No LLM provider configured. Add an Anthropic API key in Settings → Credentials.")
		return
	}

	systemPrompt := buildCaptainSystemPrompt(r.Context(), h.db, wsID)
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: strings.TrimSpace(body.Message)})

	// Switch to SSE — headers must be set before any write.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	role := RoleFromContext(r.Context())

	const maxIter = 10
	for i := 0; i < maxIter; i++ {
		var assistantTextBuf strings.Builder

		finalResp, streamErr := provider.Stream(r.Context(), llm.Request{
			Model:     "claude-3-5-haiku-20241022",
			System:    systemPrompt,
			Messages:  msgs,
			Tools:     CaptainTools,
			MaxTokens: 4096,
		}, func(ev llm.StreamEvent) error {
			if ev.Type == "text" {
				assistantTextBuf.WriteString(ev.Content)
				writeCaptainSSE(w, map[string]any{"type": "text", "content": ev.Content})
			}
			return nil
		})

		if streamErr != nil {
			h.logger.Error("captain: stream error", "error", streamErr)
			writeCaptainSSE(w, map[string]any{"type": "error", "content": "LLM error: " + streamErr.Error()})
			break
		}

		// Append assistant message (text + tool calls if any).
		assistantMsg := llm.Message{
			Role:    llm.RoleAssistant,
			Content: assistantTextBuf.String(),
		}
		if len(finalResp.ToolCalls) > 0 {
			assistantMsg.ToolCalls = finalResp.ToolCalls
		}
		msgs = append(msgs, assistantMsg)

		if finalResp.StopReason != llm.StopToolUse || len(finalResp.ToolCalls) == 0 {
			break
		}

		// Execute each tool and feed results back.
		for _, tc := range finalResp.ToolCalls {
			WriteAuditLog(r.Context(), h.db, "captain_tool_call", "CAPTAIN", tc.Name,
				user.ID, wsID, map[string]interface{}{"tool_id": tc.ID})

			writeCaptainSSE(w, map[string]any{"type": "tool_call", "id": tc.ID, "name": tc.Name})

			result, toolErr := h.executeTool(r.Context(), wsID, user.ID, role, tc)
			if toolErr != nil {
				result = "Error: " + toolErr.Error()
			}

			writeCaptainSSE(w, map[string]any{"type": "tool_result", "id": tc.ID, "name": tc.Name, "content": result})

			msgs = append(msgs, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				Content:    result,
			})
		}
	}

	if err := h.saveHistory(r.Context(), chatID, wsID, user.ID, msgs); err != nil {
		h.logger.Error("captain: save history", "error", err)
	}

	writeCaptainSSE(w, map[string]any{"type": "done"})
}

// Context handles GET /api/v1/captain/context
func (h *CaptainHandler) Context(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	var crewCount, agentCount, escalationCount, proposalCount, missionCount int
	h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND deleted_at IS NULL", wsID).Scan(&crewCount)
	h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND deleted_at IS NULL", wsID).Scan(&agentCount)
	h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM escalations WHERE workspace_id = ? AND status = 'PENDING'", wsID).Scan(&escalationCount)
	h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM mission_proposals WHERE workspace_id = ? AND status = 'PENDING'", wsID).Scan(&proposalCount)
	h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM missions WHERE workspace_id = ? AND status = 'IN_PROGRESS'", wsID).Scan(&missionCount)

	phase := captainWorkspacePhase(r.Context(), h.db, wsID, crewCount, agentCount)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"workspace_id":        wsID,
		"crews":               crewCount,
		"agents":              agentCount,
		"pending_escalations": escalationCount,
		"pending_proposals":   proposalCount,
		"active_missions":     missionCount,
		"onboarding_phase":    phase,
	})
}

// History handles GET /api/v1/captain/history
func (h *CaptainHandler) History(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	chatID := captainChatID(wsID, user.ID)
	msgs, err := h.loadHistory(r.Context(), chatID)
	if err != nil {
		h.logger.Error("captain: load history", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if msgs == nil {
		msgs = []llm.Message{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"messages": msgs})
}

// ClearHistory handles DELETE /api/v1/captain/history
func (h *CaptainHandler) ClearHistory(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	chatID := captainChatID(wsID, user.ID)
	if _, err := h.db.ExecContext(r.Context(), "DELETE FROM captain_chats WHERE id = ?", chatID); err != nil {
		h.logger.Error("captain: clear history", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CaptainHandler) getProvider(r *http.Request, wsID string) (llm.Provider, error) {
	if h.provider != nil {
		return h.provider, nil
	}
	var encryptedValue string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT encrypted_value FROM credentials
		WHERE workspace_id = ?
		  AND provider = 'ANTHROPIC'
		  AND type = 'API_KEY'
		  AND status = 'ACTIVE'
		  AND deleted_at IS NULL
		ORDER BY created_at ASC
		LIMIT 1`, wsID).Scan(&encryptedValue)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("no active Anthropic credential in workspace")
	}
	if err != nil {
		return nil, err
	}
	plain, err := encryption.Decrypt(encryptedValue)
	if err != nil {
		return nil, err
	}
	return llm.NewAnthropic(plain), nil
}

func (h *CaptainHandler) executeTool(ctx context.Context, wsID, userID, role string, tc llm.ToolCall) (string, error) {
	exec, ok := captainToolExecutors[tc.Name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", tc.Name)
	}
	var input map[string]any
	if tc.Input != "" {
		if err := json.Unmarshal([]byte(tc.Input), &input); err != nil {
			return "", fmt.Errorf("invalid tool input: %w", err)
		}
	}
	if input == nil {
		input = map[string]any{}
	}
	return exec(ctx, h, wsID, userID, role, input)
}

// captainWorkspacePhase returns 1-4 based on workspace state.
func captainWorkspacePhase(ctx context.Context, db *sql.DB, wsID string, crewCount, agentCount int) int {
	if crewCount == 0 {
		return 1
	}
	if agentCount == 0 {
		return 2
	}
	var credCount int
	db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM credentials WHERE workspace_id = ? AND status = 'ACTIVE' AND deleted_at IS NULL", wsID).Scan(&credCount)
	if credCount == 0 {
		return 3
	}
	return 4
}

// buildCaptainSystemPrompt builds a dynamic system prompt based on workspace phase.
func buildCaptainSystemPrompt(ctx context.Context, db *sql.DB, wsID string) string {
	var crewCount, agentCount int
	db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND deleted_at IS NULL", wsID).Scan(&crewCount)
	db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND deleted_at IS NULL", wsID).Scan(&agentCount)

	phase := captainWorkspacePhase(ctx, db, wsID, crewCount, agentCount)
	base := "You are Captain, the Crewship workspace assistant. You help users manage their AI crews, agents, credentials, and missions. Be concise and actionable. Use tools to fetch real data — never make up IDs or names.\n\n"

	switch phase {
	case 1:
		return base + "WORKSPACE PHASE: EMPTY — No crews yet. Suggest creating a crew via a template (apply_crew_template) or creating a custom crew (create_crew). List available templates if asked."
	case 2:
		return base + "WORKSPACE PHASE: SETUP — Crews exist but no agents. Help the user add agents using create_agent. List existing crews with list_crews first."
	case 3:
		return base + "WORKSPACE PHASE: CREDENTIALS NEEDED — Agents exist but no active credentials. Guide the user to add API credentials in Settings → Credentials."
	default:
		return base + "WORKSPACE PHASE: OPERATIONAL — Workspace is fully set up. Help with missions (list_missions, create_mission), escalations (list_escalations), proposals (approve_proposal), and general workspace management."
	}
}
