package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

// ConversationSearchHit is the wire shape returned by the conversation
// search endpoint. It mirrors conversation.SearchHit without importing the
// conversation package into api (the server adapter converts between them).
type ConversationSearchHit struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	AgentID     string `json:"agent_id"`
	Role        string `json:"role"`
	Content     string `json:"content"`
	ToolSummary string `json:"tool_summary,omitempty"`
	Timestamp   string `json:"ts"`
}

// ConversationSearcher runs an agent-scoped BM25 search over conversation
// history. *conversation.Store satisfies it via a server-side adapter. The
// agentID is the tenancy boundary — callers MUST pass a workspace-verified
// agent id; the searcher itself only filters, it does not authorize.
type ConversationSearcher interface {
	SearchConversations(ctx context.Context, agentID, query string, limit int) ([]ConversationSearchHit, error)
}

// ConversationHandler backs POST /api/v1/conversations/search.
type ConversationHandler struct {
	db       *sql.DB
	searcher ConversationSearcher
}

// NewConversationHandler builds the handler. A nil searcher makes Search
// return 503 (feature not configured) rather than panicking — same
// graceful-degradation contract the rest of the optional surfaces use.
func NewConversationHandler(db *sql.DB, s ConversationSearcher) *ConversationHandler {
	return &ConversationHandler{db: db, searcher: s}
}

type conversationSearchRequest struct {
	AgentID string `json:"agent_id"`
	Query   string `json:"query"`
	Limit   int    `json:"limit"`
}

// Search handles POST /api/v1/conversations/search. Body:
//
//	{"agent_id": "...", "query": "deploy pipeline", "limit": 20}
//
// The agent must belong to the caller's workspace (agentExists gate) — this
// is the authorization boundary that turns the searcher's "filter by
// agent_id" into a real tenancy guarantee. Returns the ranked hits as JSON.
func (h *ConversationHandler) Search(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	if h.searcher == nil {
		replyError(w, http.StatusServiceUnavailable, "conversation search not configured")
		return
	}

	var req conversationSearchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.AgentID) == "" {
		replyError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		replyError(w, http.StatusBadRequest, "query is required")
		return
	}

	// Authorization: the requested agent must live in the caller's
	// workspace. Without this, a caller could pass any agent_id and read
	// its conversation history cross-tenant — the searcher only filters,
	// it does not check ownership.
	ok, err := agentExists(r.Context(), h.db, req.AgentID, workspaceID)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "agent lookup failed")
		return
	}
	if !ok {
		replyError(w, http.StatusNotFound, "agent not found")
		return
	}

	hits, err := h.searcher.SearchConversations(r.Context(), req.AgentID, req.Query, req.Limit)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	if hits == nil {
		hits = []ConversationSearchHit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hits":  hits,
		"query": req.Query,
		"count": len(hits),
	})
}
