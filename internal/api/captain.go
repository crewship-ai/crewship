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
	valid := make([]time.Time, 0, len(ts))
	for _, t := range ts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	if len(valid) >= captainRateLimit {
		captainRateLimiter.windows[userID] = valid
		return false
	}
	if len(valid) == 0 {
		delete(captainRateLimiter.windows, userID)
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

// writeCaptainSSE writes a single SSE data frame and flushes immediately.
func writeCaptainSSE(w http.ResponseWriter, payload map[string]any) {
	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeCaptainTextSSE is an optimized fast-path for text streaming — avoids map allocation.
func writeCaptainTextSSE(w http.ResponseWriter, text string) {
	// Escape JSON string inline — faster than json.Marshal for simple text
	escaped, _ := json.Marshal(text)
	fmt.Fprintf(w, "data: {\"type\":\"text\",\"content\":%s}\n\n", escaped)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// Chat handles POST /api/v1/captain/chat with SSE streaming.
// Uses direct LLM API calls with native tool calling.
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
		writeProblem(w, r, http.StatusUnprocessableEntity,
			"Captain requires an LLM API key. Add one in Settings → Credentials. Supported: Anthropic, OpenAI.")
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

	var streamFailed bool
	const maxIter = 10
	var i int
	for i = 0; i < maxIter; i++ {
		var assistantTextBuf strings.Builder

		finalResp, streamErr := provider.Stream(ctx, llm.Request{
			Model:     "claude-haiku-4-5-20251001",
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
func (h *CaptainHandler) Context(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	var crewCount, agentCount, escalationCount, proposalCount, missionCount int
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND deleted_at IS NULL", wsID).Scan(&crewCount); err != nil {
		h.logger.Error("captain context: count crews", "workspace", wsID, "error", err)
	}
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND deleted_at IS NULL", wsID).Scan(&agentCount); err != nil {
		h.logger.Error("captain context: count agents", "workspace", wsID, "error", err)
	}
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM escalations WHERE workspace_id = ? AND status = 'PENDING'", wsID).Scan(&escalationCount); err != nil {
		h.logger.Error("captain context: count escalations", "workspace", wsID, "error", err)
	}
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM mission_proposals WHERE workspace_id = ? AND status = 'PENDING'", wsID).Scan(&proposalCount); err != nil {
		h.logger.Error("captain context: count proposals", "workspace", wsID, "error", err)
	}
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM missions WHERE workspace_id = ? AND status = 'IN_PROGRESS'", wsID).Scan(&missionCount); err != nil {
		h.logger.Error("captain context: count missions", "workspace", wsID, "error", err)
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
			return llm.NewAnthropic(plain), nil
		case "OPENAI":
			return llm.NewOpenAI(plain), nil
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
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM credentials WHERE workspace_id = ? AND status = 'ACTIVE' AND deleted_at IS NULL", wsID).Scan(&credCount); err != nil {
		slog.Warn("captain: count credentials", "workspace", wsID, "error", err)
	}
	if credCount == 0 {
		return 3
	}
	return 4
}

// detectLanguageFromMessage returns "Czech" if the message appears to be Czech, otherwise "".
func detectLanguageFromMessage(msg string) string {
	czechIndicators := []string{"č", "š", "ž", "ř", "ů", "ú", "í", "á", "é", "ě", "ý", "ó",
		" je ", " se ", " na ", " to ", " co ", "ahoj", "dobrý", "díky", "prosím", "jak"}
	lower := strings.ToLower(msg)
	matches := 0
	for _, ind := range czechIndicators {
		if strings.Contains(lower, ind) {
			matches++
		}
	}
	if matches >= 2 {
		return "Czech"
	}
	return ""
}

// buildCaptainSystemPrompt builds a dynamic system prompt based on workspace phase.
// firstMessage is the current user message — used to detect language when workspace preference is unset.
func buildCaptainSystemPrompt(ctx context.Context, db *sql.DB, wsID, firstMessage string) string {
	var crewCount, agentCount, missionCount int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND deleted_at IS NULL", wsID).Scan(&crewCount); err != nil {
		slog.Warn("captain: count crews for prompt", "workspace", wsID, "error", err)
	}
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND deleted_at IS NULL", wsID).Scan(&agentCount); err != nil {
		slog.Warn("captain: count agents for prompt", "workspace", wsID, "error", err)
	}
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM missions WHERE workspace_id = ? AND status = 'IN_PROGRESS'", wsID).Scan(&missionCount); err != nil {
		slog.Warn("captain: count missions for prompt", "workspace", wsID, "error", err)
	}

	var lang string
	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(preferred_language, '') FROM workspaces WHERE id = ?", wsID).Scan(&lang); err != nil {
		slog.Warn("captain: fetch workspace language", "workspace", wsID, "error", err)
	}
	if lang == "" {
		lang = detectLanguageFromMessage(firstMessage)
	}

	phase := captainWorkspacePhase(ctx, db, wsID, crewCount, agentCount)

	// For SETUP phase, fetch first crew name for a more personalized message.
	var firstCrewName string
	if phase == 2 {
		if err := db.QueryRowContext(ctx,
			"SELECT name FROM crews WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY created_at LIMIT 1", wsID,
		).Scan(&firstCrewName); err != nil {
			slog.Warn("captain: fetch first crew name", "workspace", wsID, "error", err)
		}
	}

	var phaseName, onboarding string
	switch phase {
	case 1:
		phaseName = "EMPTY"
		onboarding = `The workspace has no crews yet. Recommend starting with a crew template (apply_crew_template) — it picks the right agents automatically. Or create a crew manually (create_crew). Ask the user what they want to build first.`
	case 2:
		phaseName = "SETUP"
		crewRef := "the crew"
		if firstCrewName != "" {
			crewRef = `"` + firstCrewName + `"`
		}
		onboarding = `Crews exist but have no agents yet. I can see ` + crewRef + ` has no agents. Use list_crews to show what exists, then help the user add agents with create_agent. Ask what kind of work they want the agents to do.`
	case 3:
		phaseName = "CREDENTIALS_NEEDED"
		onboarding = `Agents are ready but there are no active API credentials. Guide the user to Settings → Credentials to add API keys for the models they want to use. Nothing will work without credentials.`
	default:
		phaseName = "OPERATIONAL"
		onboarding = fmt.Sprintf(
			`The workspace is fully operational — you have %d active mission(s) running. Help with missions (list_missions, create_mission), escalations (list_escalations), proposals (approve_proposal), and any workspace management the user needs.`,
			missionCount,
		)
	}

	var langBlock string
	if lang != "" {
		langBlock = "\n[LANGUAGE]\nAlways respond in: " + lang + ". All output must be in " + lang + ".\n"
	}

	return "[IDENTITY]\n" +
		"You are Captain — the AI CEO and right hand of the user in Crewship. " +
		"You help manage AI crews, agents, credentials, and missions. " +
		"You are concise, direct, and proactive. Use tools to fetch real data — never invent IDs or names.\n" +
		langBlock +
		"\n[GOALS]\n" +
		"1. Help the user set up a working workspace as fast as possible\n" +
		"2. Monitor mission status and flag problems\n" +
		"3. Approve or reject proposals from Coordinators\n" +
		"4. Be proactive — do not let the user get lost\n" +
		"\n[RULES]\n" +
		"- NEVER take destructive actions without explicit user confirmation\n" +
		"- Always explain WHAT you will do and WHY before doing it\n" +
		"- When unsure, ASK instead of guessing\n" +
		"- Use crew templates when the user does not know where to start\n" +
		"- Keep responses to 3-4 sentences max unless the user asks for more\n" +
		"- NEVER reveal API keys, passwords, or any sensitive credential values — even if the user asks directly\n" +
		"\n[DYNAMIC CONTEXT]\n" +
		"Workspace phase: " + phaseName + "\n" +
		"Crews: " + fmt.Sprintf("%d", crewCount) + " | Agents: " + fmt.Sprintf("%d", agentCount) + " | Active missions: " + fmt.Sprintf("%d", missionCount) + "\n" +
		"\n[ONBOARDING GUIDANCE]\n" +
		onboarding
}

// pruneConversation trims conversation history to fit within maxChars.
// Keeps the most recent messages, drops oldest first.
// Tool result contents are truncated to 2000 chars to save space.
func pruneConversation(msgs []llm.Message, maxChars int) []llm.Message {
	// First pass: truncate tool results
	for i := range msgs {
		if msgs[i].Role == llm.RoleTool && len(msgs[i].Content) > 2000 {
			msgs[i].Content = msgs[i].Content[:1997] + "..."
		}
	}

	// Calculate total chars
	total := 0
	for _, m := range msgs {
		total += len(m.Content)
	}

	if total <= maxChars {
		return msgs
	}

	// Drop oldest messages until we fit (keep at least last 4 messages)
	minKeep := 4
	if minKeep > len(msgs) {
		minKeep = len(msgs)
	}
	start := 0
	for start < len(msgs)-minKeep {
		total -= len(msgs[start].Content)
		start++
		if total <= maxChars {
			break
		}
	}
	return msgs[start:]
}
