package api

// File: captain.go — Captain (built-in workspace AI assistant) feature.
//
// DEPRECATED (2026-04-16): Captain is no longer actively developed. The
// feature was started but never completed. In the 2026 architecture,
// workspace-level AI should be delivered via the MCP gateway pattern (see
// CRE-48) or as user-created agents. Existing code is retained for backward
// compatibility and will not receive new features. Do not build on it.
//
// See docs/guides/captain.mdx for migration notes.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/llm"
)

// MissionStarter can start a mission that is already inserted in PLANNING/IN_PROGRESS status.
// *orchestrator.MissionEngine satisfies this interface.
type MissionStarter interface {
	StartMission(ctx context.Context, missionID string) error
	ApproveTask(ctx context.Context, taskID, userID string, approved bool, notes string) error
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

	// Opportunistic sweep: remove stale entries from other users to bound map growth.
	// Only sweep when map is large enough to warrant it (amortised O(1) per call).
	if len(captainRateLimiter.windows) > 100 {
		cutoff := now.Add(-captainRateWindow)
		for uid, w := range captainRateLimiter.windows {
			if uid == userID {
				continue
			}
			if len(w) > 0 && w[len(w)-1].Before(cutoff) {
				delete(captainRateLimiter.windows, uid)
			}
		}
	}

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
	captainRateLimiter.windows[userID] = append(valid, now)
	return true
}

// CaptainHandler powers the Captain AI assistant that helps users manage their workspace via natural language.
//
// Deprecated: Captain (built-in workspace AI assistant) is no longer actively
// developed. Use the MCP gateway pattern or user-created agents instead.
// See docs/guides/captain.mdx.
type CaptainHandler struct {
	db            *sql.DB
	logger        *slog.Logger
	provider      llm.Provider
	missionEngine MissionStarter
	journal       journal.Emitter
}

// SetJournal wires the emitter so Captain's on-demand LLM construction
// can wrap providers with the paymaster+lookout+telemetry stack. Called
// by the router; tests can leave the nil default in which case the
// middleware wrap falls back to a no-op emitter and behaves transparently.
func (h *CaptainHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		j = noopEmitter{}
	}
	h.journal = j
}

// NewCaptainHandler creates a CaptainHandler with the given database and logger.
//
// Deprecated: Captain feature is deprecated. See [CaptainHandler].
func NewCaptainHandler(db *sql.DB, logger *slog.Logger) *CaptainHandler {
	return &CaptainHandler{db: db, logger: logger}
}

// SetProvider attaches the LLM provider used for Captain conversations.
//
// Deprecated: Captain feature is deprecated. See [CaptainHandler].
func (h *CaptainHandler) SetProvider(p llm.Provider) {
	h.provider = p
}

// SetMissionEngine attaches the mission engine for Captain to start missions on behalf of users.
//
// Deprecated: Captain feature is deprecated. See [CaptainHandler].
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
		return nil, fmt.Errorf("load captain history: %w", err)
	}
	var msgs []llm.Message
	if err := json.Unmarshal([]byte(msgsJSON), &msgs); err != nil {
		return nil, fmt.Errorf("load captain history: unmarshal: %w", err)
	}
	return msgs, nil
}

func (h *CaptainHandler) saveHistory(ctx context.Context, chatID, wsID, userID string, msgs []llm.Message) error {
	data, err := json.Marshal(msgs)
	if err != nil {
		return fmt.Errorf("save captain history: marshal: %w", err)
	}
	_, err = h.db.ExecContext(ctx, `
		INSERT INTO captain_chats (id, workspace_id, user_id, messages_json)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			messages_json = excluded.messages_json,
			updated_at = datetime('now')
	`, chatID, wsID, userID, string(data))
	if err != nil {
		return fmt.Errorf("save captain history: %w", err)
	}
	return nil
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
//
// Deprecated: Captain feature is deprecated. See [CaptainHandler].

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
