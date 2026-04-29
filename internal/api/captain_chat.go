package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/llm"
)

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
	}
	if err := readJSON(r, &body); err != nil || strings.TrimSpace(body.Message) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message is required"})
		return
	}

	// Always derive chatID server-side to prevent cross-user history access.
	chatID := captainChatID(wsID, user.ID)

	msgs, err := h.loadHistory(r.Context(), chatID)
	if err != nil {
		h.logger.Error("captain: load history", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	systemPrompt := buildCaptainSystemPrompt(r.Context(), h.db, wsID, body.Message)
	h.chatViaDirect(w, r, systemPrompt, body.Message, msgs, chatID, wsID, user.ID)
}

// chatViaDirect handles Captain chat via direct Anthropic API calls with tool loop.
func (h *CaptainHandler) chatViaDirect(
	w http.ResponseWriter, r *http.Request,
	systemPrompt, userMessage string,
	msgs []llm.Message,
	chatID, wsID, userID string,
) {
	provider, err := h.getProvider(r, wsID)
	if err != nil {
		h.logger.Warn("captain: no LLM provider", "workspace", wsID, "error", err)
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "Captain requires an LLM API key. Add one in Settings → Credentials. Supported: Anthropic, OpenAI.",
		})
		return
	}

	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: strings.TrimSpace(userMessage)})

	// Prune conversation to fit within context window (~200K tokens for Haiku 4.5).
	// Keep system prompt + recent messages within 80% of context (640K chars ≈ 160K tokens).
	msgs = pruneConversation(msgs, 640000)

	// Switch to SSE — headers must be set before any write.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Disable write timeout for SSE streaming (default 15s is too short for LLM calls).
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{}) // zero = no deadline

	// Overall timeout for the SSE connection to prevent indefinite hangs.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	role := RoleFromContext(r.Context())

	// Select model based on the resolved provider.
	model := "claude-haiku-4-5-20251001"
	if provider.Name() == "openai" {
		model = "gpt-4o-mini"
	}

	var streamFailed bool
	const maxIter = 10
	var i int
	for i = 0; i < maxIter; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var assistantTextBuf strings.Builder

		finalResp, streamErr := provider.Stream(ctx, llm.Request{
			Model:     model,
			System:    systemPrompt,
			Messages:  msgs,
			Tools:     CaptainTools,
			MaxTokens: 4096,
		}, func(ev llm.StreamEvent) error {
			if ev.Type == "text" {
				assistantTextBuf.WriteString(ev.Content)
				writeCaptainTextSSE(w, ev.Content)
			}
			return nil
		})

		if streamErr != nil {
			h.logger.Error("captain: stream error", "error", streamErr)
			writeCaptainSSE(w, map[string]any{"type": "error", "content": "LLM error: " + streamErr.Error()})
			streamFailed = true
			break
		}

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

		for _, tc := range finalResp.ToolCalls {
			WriteAuditLog(ctx, h.db, "captain_tool_call", "CAPTAIN", tc.Name,
				userID, wsID, map[string]interface{}{"tool_id": tc.ID})

			writeCaptainSSE(w, map[string]any{"type": "tool_call", "id": tc.ID, "name": tc.Name})

			result, toolErr := h.executeTool(ctx, wsID, userID, role, tc)
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

	// Re-prune before persisting — tool results appended during the loop may exceed the limit.
	msgs = pruneConversation(msgs, 640000)
	if err := h.saveHistory(ctx, chatID, wsID, userID, msgs); err != nil {
		h.logger.Error("captain: save history", "error", err)
	}

	if !streamFailed {
		if i == maxIter {
			writeCaptainSSE(w, map[string]any{"type": "warning", "content": "Maximum tool iterations reached. Some actions may be incomplete."})
		}
		writeCaptainSSE(w, map[string]any{"type": "done"})
	}
}

// Context handles GET /api/v1/captain/context
//
// Deprecated: Captain feature is deprecated. See [CaptainHandler].
func (h *CaptainHandler) Context(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	var crewCount, agentCount, escalationCount, proposalCount, missionCount int
	err := h.db.QueryRowContext(r.Context(), `
		SELECT
			(SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND deleted_at IS NULL),
			(SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND deleted_at IS NULL),
			(SELECT COUNT(*) FROM escalations WHERE workspace_id = ? AND status = 'PENDING'),
			(SELECT COUNT(*) FROM mission_proposals WHERE workspace_id = ? AND status = 'PENDING'),
			(SELECT COUNT(*) FROM missions WHERE workspace_id = ? AND status = 'IN_PROGRESS')`,
		wsID, wsID, wsID, wsID, wsID).Scan(&crewCount, &agentCount, &escalationCount, &proposalCount, &missionCount)
	if err != nil {
		h.logger.Error("captain context: count workspace stats", "workspace", wsID, "error", err)
	}

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
//
// Deprecated: Captain feature is deprecated. See [CaptainHandler].
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
//
// Deprecated: Captain feature is deprecated. See [CaptainHandler].
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

	// Try providers in priority order: Anthropic → OpenAI → Ollama
	type credRow struct {
		Provider       string
		EncryptedValue string
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT provider, encrypted_value FROM credentials
		WHERE workspace_id = ?
		  AND type = 'API_KEY'
		  AND status = 'ACTIVE'
		  AND deleted_at IS NULL
		  AND provider IN ('ANTHROPIC', 'OPENAI')
		ORDER BY
			CASE provider
				WHEN 'ANTHROPIC' THEN 1
				WHEN 'OPENAI' THEN 2
			END,
			created_at ASC`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var foundAny bool
	var decryptFailed bool
	for rows.Next() {
		var c credRow
		if err := rows.Scan(&c.Provider, &c.EncryptedValue); err != nil {
			h.logger.Warn("captain: credential scan failed", "workspace", wsID, "error", err)
			foundAny = true
			decryptFailed = true
			continue
		}
		foundAny = true
		plain, err := encryption.Decrypt(c.EncryptedValue)
		if err != nil {
			h.logger.Warn("captain: credential decrypt failed", "workspace", wsID, "provider", c.Provider, "error", err)
			decryptFailed = true
			continue
		}
		switch c.Provider {
		case "ANTHROPIC":
			return llm.Middleware(llm.NewAnthropic(plain), h.journal, h.db), nil
		case "OPENAI":
			return llm.Middleware(llm.NewOpenAI(plain), h.journal, h.db), nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("captain: credential query iteration: %w", err)
	}

	if !foundAny {
		return nil, errors.New("no API credentials found")
	}
	if decryptFailed {
		return nil, errors.New("credential decryption failed")
	}
	return nil, errors.New("no usable API credentials found")
}
