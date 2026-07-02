package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// isoMillisNow returns the current UTC time in the fixed-width
// millisecond-ISO form ("2026-07-02T10:00:00.000Z"). This matches
// SQLite's strftime('%Y-%m-%dT%H:%M:%fZ','now') and, unlike
// RFC3339Nano (which strips trailing zeros), stays lexically
// comparable against conversation_messages.ts and
// chats.last_activity_at — the unread computation is a pure string
// comparison in SQL.
func isoMillisNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// chatReplyInboxSourceID is the inbox_items.source_id dedupe key for the
// "your agent replied" notification of one (chat, user) pair. Shared
// between the chatnotify writer and MarkChatRead (which clears it) — keep
// in sync with internal/chatnotify.
func chatReplyInboxSourceID(chatID, userID string) string {
	return "chat_reply_" + chatID + "_" + userID
}

// ListChats returns all chat sessions for a given agent, ordered by most
// recent activity. Each row carries the requesting user's unread_count
// (messages not authored by them appended after their read cursor) and
// last_activity_at so the Sessions sidebar can order + badge sessions.
// GET /api/v1/agents/{agentId}/chats
func (h *AgentHandler) ListChats(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	var userID string
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}

	// last_activity_at falls back to started_at (normalised through
	// strftime so legacy space-separated rows compare with ISO rows).
	//
	// unread_count counts messages the user hasn't seen and didn't
	// write: the author guard treats authorless user-role rows
	// (legacy, scheduler-injected) as "mine" so they never inflate the
	// badge; assistant/system rows always count. No cursor row means
	// "never opened" — everything counts (migration v130 backfills a
	// cursor for pre-existing chats so upgrades ship quiet).
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT c.id, c.agent_id, c.workspace_id, c.title, c.mode, c.status,
			c.message_count, c.started_at, c.ended_at, c.created_at, c.origin,
			COALESCE(c.last_activity_at,
				strftime('%Y-%m-%dT%H:%M:%fZ', c.started_at),
				c.started_at) AS last_activity_at,
			(SELECT COUNT(*) FROM conversation_messages m
			 WHERE m.session_id = c.id
			   AND NOT (m.role = 'user' AND (m.author_user_id IS NULL OR m.author_user_id = ?))
			   AND m.ts > COALESCE((SELECT rc.last_read_at FROM chat_read_cursors rc
			                        WHERE rc.chat_id = c.id AND rc.user_id = ?), '')
			) AS unread_count
		FROM chats c
		WHERE c.agent_id = ? AND c.workspace_id = ?
		ORDER BY last_activity_at DESC
		LIMIT 100
	`, userID, userID, agentID, workspaceID)
	if err != nil {
		h.logger.Error("list agent chats", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	type chatResponse struct {
		ID             string  `json:"id"`
		AgentID        string  `json:"agent_id"`
		WorkspaceID    string  `json:"workspace_id"`
		Title          *string `json:"title"`
		Mode           string  `json:"mode"`
		Status         string  `json:"status"`
		MessageCount   int     `json:"message_count"`
		StartedAt      string  `json:"started_at"`
		EndedAt        *string `json:"ended_at"`
		CreatedAt      string  `json:"created_at"`
		Origin         *string `json:"origin"`
		LastActivityAt string  `json:"last_activity_at"`
		UnreadCount    int     `json:"unread_count"`
	}

	var result []chatResponse
	for rows.Next() {
		var c chatResponse
		if err := rows.Scan(&c.ID, &c.AgentID, &c.WorkspaceID, &c.Title,
			&c.Mode, &c.Status, &c.MessageCount,
			&c.StartedAt, &c.EndedAt, &c.CreatedAt, &c.Origin,
			&c.LastActivityAt, &c.UnreadCount); err != nil {
			h.logger.Error("scan chat", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (chats)", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if result == nil {
		result = []chatResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// CreateChat starts a new chat session with the specified agent.
// POST /api/v1/agents/{agentId}/chats
func (h *AgentHandler) CreateChat(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	userID := UserFromContext(r.Context()).ID

	var body struct {
		SessionID string `json:"session_id"`
		// Origin distinguishes how the session was started: "UI" (chat
		// page in the browser), "CLI" (`crewship run`), "WEBHOOK",
		// "CRON", "AGENT" (agent-to-agent assignment). The
		// SessionsSidebar renders a colored chip per origin. Unknown
		// or empty values are stored as NULL → no chip shown.
		Origin string `json:"origin"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	chatID := body.SessionID
	if chatID == "" {
		chatID = generateCUID()
	}

	// Whitelist allowed origin values; anything else becomes NULL so a
	// rogue caller can't shove arbitrary text into a UI-rendered chip.
	var origin sql.NullString
	switch body.Origin {
	case "UI", "CLI", "WEBHOOK", "CRON", "AGENT":
		origin = sql.NullString{String: body.Origin, Valid: true}
	}

	// Check agent exists
	found, err := agentExists(r.Context(), h.db, agentID, workspaceID)
	if err != nil {
		h.logger.Error("check agent exists", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	// Atomic upsert: insert only if agent is still active (prevents TOCTOU with soft-delete)
	_, err = h.db.ExecContext(r.Context(),
		`INSERT OR IGNORE INTO chats (id, agent_id, workspace_id, created_by, status, origin, last_activity_at)
		 SELECT ?, ?, ?, ?, 'ACTIVE', ?, ?
		 WHERE EXISTS (SELECT 1 FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL)`,
		chatID, agentID, workspaceID, userID, origin, isoMillisNow(), agentID, workspaceID)
	if err != nil {
		h.logger.Error("create chat", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Check outcome: either inserted, already existed (IGNORE), or agent was deleted
	var ownerAgentID string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT agent_id FROM chats WHERE id = ?", chatID).Scan(&ownerAgentID); err != nil {
		if err == sql.ErrNoRows {
			// No row: agent was deleted between preflight and INSERT (WHERE EXISTS failed)
			replyError(w, http.StatusNotFound, "Agent not found")
			return
		}
		h.logger.Error("verify chat owner", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if ownerAgentID != agentID {
		replyError(w, http.StatusConflict, "Chat belongs to a different agent")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": chatID})
}

// MarkChatRead advances the caller's read cursor on a chat to "now",
// zeroing its unread_count in ListChats, and clears the paired "agent
// replied" inbox notification for this (user, chat) so the bell badge
// and the sidebar badge stay in lockstep.
// PUT /api/v1/agents/{agentId}/chats/{chatId}/read
func (h *AgentHandler) MarkChatRead(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	chatID := r.PathValue("chatId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	if workspaceID == "" || user == nil {
		replyError(w, http.StatusUnauthorized, "auth required")
		return
	}

	// Scope check: the chat must belong to this agent AND this
	// workspace. A cross-tenant or mis-nested id 404s (don't leak
	// existence, don't write a cursor).
	var one int
	err := h.db.QueryRowContext(r.Context(),
		`SELECT 1 FROM chats WHERE id = ? AND agent_id = ? AND workspace_id = ?`,
		chatID, agentID, workspaceID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Chat not found")
		return
	}
	if err != nil {
		h.logger.Error("mark chat read lookup", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	now := isoMillisNow()
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO chat_read_cursors (user_id, chat_id, last_read_at)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id, chat_id) DO UPDATE SET last_read_at = excluded.last_read_at`,
		user.ID, chatID, now); err != nil {
		h.logger.Error("mark chat read upsert", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Clear the reply notification this cursor supersedes. Best-effort:
	// the cursor is the durable state; a failed inbox flip only leaves a
	// stale bell item the user can dismiss manually.
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE inbox_items
		SET state = 'read',
		    read_at = COALESCE(read_at, ?),
		    read_by_user_id = COALESCE(read_by_user_id, ?),
		    updated_at = ?
		WHERE workspace_id = ? AND kind = 'message' AND source_id = ? AND state = 'unread'`,
		now, user.ID, now, workspaceID, chatReplyInboxSourceID(chatID, user.ID))
	if err != nil {
		h.logger.Warn("mark chat read: inbox clear", "error", err, "chat_id", chatID)
	} else if n, _ := res.RowsAffected(); n > 0 {
		broadcastWorkspaceEvent(h.hub, workspaceID, "inbox.updated", map[string]string{
			"source": "chat_read",
			"chat":   chatID,
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"chat_id":      chatID,
		"last_read_at": now,
	})
}

// ListRuns returns all execution runs for a given agent, ordered by most recent first.
// GET /api/v1/agents/{agentId}/runs
//
// Reads from journal_entries (unified-journal Phase E). Up to 100 most
// recent runs scoped to the workspace + agent_id.
func (h *AgentHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	aggregated, _, err := journal.ListRuns(r.Context(), h.db, journal.RunsQuery{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		Limit:       100,
	})
	if err != nil {
		h.logger.Error("list agent runs", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Per-agent endpoint doesn't enrich with crew/agent names — caller
	// already knows the agent context. Convert directly.
	result := make([]runResponse, 0, len(aggregated))
	for _, ar := range aggregated {
		resp := runResponse{
			ID:           ar.ID,
			AgentID:      ar.AgentID,
			WorkspaceID:  ar.WorkspaceID,
			TriggerType:  ar.TriggerType,
			Status:       string(ar.Status),
			ErrorMessage: stringPtrOrNil(ar.ErrorMessage),
			ExitCode:     ar.ExitCode,
			CreatedAt:    formatRFC3339(ar.CreatedAt),
		}
		if ar.ChatID != "" {
			c := ar.ChatID
			resp.ChatID = &c
		}
		if ar.TriggeredBy != "" {
			t := ar.TriggeredBy
			resp.TriggeredBy = &t
		}
		if !ar.StartedAt.IsZero() {
			s := formatRFC3339(ar.StartedAt)
			resp.StartedAt = &s
		}
		if ar.FinishedAt != nil && !ar.FinishedAt.IsZero() {
			f := formatRFC3339(*ar.FinishedAt)
			resp.FinishedAt = &f
		}
		if ar.Metadata != nil {
			if b, jerr := json.Marshal(ar.Metadata); jerr == nil {
				resp.Metadata = b
			}
		}
		result = append(result, resp)
	}
	writeJSON(w, http.StatusOK, result)
}
