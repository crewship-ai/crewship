package api

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/chataudience"
)

// Agent diagnostics have no reliable per-line conversation identity. Qualify
// every protected context after IPC loading instead of guessing which copied
// prompt, result or metadata belongs to which human. The shared predicate also
// includes durable classification after source consents/results are removed.
func (h *ProxyHandler) allowPrivateArtifactDiagnostics(w http.ResponseWriter, r *http.Request, agent, workspace string) bool {
	const maxContexts = 1000
	rows, err := h.db.QueryContext(r.Context(), `SELECT c.id FROM chats c
 WHERE c.agent_id=? AND c.workspace_id=? AND NOT (`+chataudience.NoPrivateArtifactContextSQL+`)
 ORDER BY c.id LIMIT ?`, agent, workspace, maxContexts+1)
	if err != nil {
		replyInternalError(w, h.logger, "classify agent diagnostics", err)
		return false
	}
	var chats []string
	for rows.Next() {
		var chat string
		if err = rows.Scan(&chat); err != nil {
			break
		}
		chats = append(chats, chat)
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		replyInternalError(w, h.logger, "load agent diagnostic contexts", err)
		return false
	}
	// Close the reader before any OWNER audit INSERT, including with one DB
	// connection. Overflow is a denial, never a truncated authority check.
	if len(chats) > maxContexts {
		replyError(w, http.StatusNotFound, "Agent not found")
		return false
	}
	if len(chats) == 0 {
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusNotFound, "Agent not found")
		return false
	}
	for _, chat := range chats {
		allowed, err := chataudience.CanReadArtifactConversation(r.Context(), h.db, chat, user.ID, workspace, false)
		if err != nil {
			replyInternalError(w, h.logger, "authorize private agent diagnostics", err)
			return false
		}
		if !allowed {
			replyError(w, http.StatusNotFound, "Agent not found")
			return false
		}
	}
	return true
}
