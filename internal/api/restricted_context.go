package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/access"
)

func (r *Router) restrictedContext(w http.ResponseWriter, req *http.Request) {
	user := UserFromContext(req.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	workspace, chat := WorkspaceIDFromContext(req.Context()), req.PathValue("chatId")
	var agent string
	if err := r.db.QueryRowContext(req.Context(), `SELECT agent_id FROM chats WHERE id=? AND workspace_id=?`, chat, workspace).Scan(&agent); err != nil {
		replyError(w, http.StatusNotFound, "Context not found")
		return
	}
	store := access.Store{DB: r.db}
	w.Header().Set("Cache-Control", "no-store")
	if req.Method == http.MethodPost {
		var body struct {
			Content string `json:"content"`
		}
		req.Body = http.MaxBytesReader(w, req.Body, 32768)
		decoder := json.NewDecoder(req.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || strings.TrimSpace(body.Content) == "" || len(body.Content) > 8192 {
			replyError(w, http.StatusBadRequest, "Invalid memory note")
			return
		}
		note, err := store.SaveNote(req.Context(), user.ID, workspace, agent, chat, body.Content)
		if err != nil {
			replyError(w, http.StatusNotFound, "Context not found")
			return
		}
		writeJSON(w, http.StatusCreated, note)
		return
	}
	if req.Method == http.MethodDelete {
		if err := store.DeleteNote(req.Context(), user.ID, workspace, agent, chat, req.PathValue("entryId")); err != nil {
			replyError(w, http.StatusNotFound, "Memory not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	query := strings.ToLower(req.URL.Query().Get("search"))
	kind := req.URL.Query().Get("kind")
	if len(query) > 256 || kind != "" && kind != "history" && kind != "memory" && kind != "summary" {
		replyError(w, http.StatusBadRequest, "Invalid context query")
		return
	}
	entries, err := store.ContextEntriesForChat(req.Context(), user.ID, workspace, agent, chat)
	if err != nil {
		replyError(w, http.StatusNotFound, "Context not found")
		return
	}
	result := []access.ContextEntry{}
	for _, entry := range entries {
		if (kind == "" || string(entry.Kind) == kind) && strings.Contains(strings.ToLower(entry.Content), query) {
			result = append(result, entry)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": result, "limit": 256})
}
