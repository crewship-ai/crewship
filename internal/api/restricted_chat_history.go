package api

import (
	"database/sql"
	"net/http"

	"github.com/crewship-ai/crewship/internal/access"
)

func restrictedChatHistory(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
	user := UserFromContext(r.Context())
	if user == nil {
		return false
	}
	store := access.Store{DB: db}
	restricted, err := store.HasRestrictedMembership(r.Context(), user.ID)
	if err != nil {
		replyError(w, http.StatusServiceUnavailable, "history unavailable")
		return true
	}
	if !restricted {
		return false
	}
	chat := r.PathValue("chatId")
	workspace := WorkspaceIDFromContext(r.Context())
	var agent string
	if err = db.QueryRowContext(r.Context(), `SELECT agent_id FROM chats WHERE id=? AND workspace_id=?`, chat, workspace).Scan(&agent); err != nil {
		replyError(w, http.StatusNotFound, "Chat not found")
		return true
	}
	entries, err := store.ContextEntriesForChat(r.Context(), user.ID, workspace, agent, chat)
	if err != nil {
		replyError(w, http.StatusNotFound, "Chat not found")
		return true
	}
	result := []map[string]string{}
	for _, entry := range entries {
		if entry.Kind == access.ContextHistory {
			result = append(result, map[string]string{"id": entry.ID, "role": entry.Role, "content": entry.Content, "ts": entry.CreatedAt})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": result})
	return true
}
