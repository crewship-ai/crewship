package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
)

// errNoWorkspaceSearch marks the "searcher cannot do workspace scope" exit.
// It never reaches the client — the 503 body does — it only tells Search
// that a reply has already been written.
var errNoWorkspaceSearch = errors.New("workspace-wide conversation search not configured")

// ConversationSearchHit is the wire shape returned by the conversation
// search endpoint. It mirrors conversation.SearchHit without importing the
// conversation package into api (the server adapter converts between them).
type ConversationSearchHit struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	// AgentSlug and AgentName are filled in by the handler, not the
	// searcher: the FTS mirror stores only agent_id, and every caller of a
	// workspace-wide result needs to say WHICH agent said it and to link to
	// /chat/<agent_slug>?session=<session_id> (the URL chatnotify already
	// builds). Resolving them here costs one query the handler has already
	// run for the tenancy check.
	AgentSlug   string `json:"agent_slug,omitempty"`
	AgentName   string `json:"agent_name,omitempty"`
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

// MultiAgentConversationSearcher is the optional widening of
// ConversationSearcher that backs a workspace-wide search: one ranked query
// over a SET of agents, rather than N agent-scoped queries merged by a
// caller that can no longer see the BM25 scores.
//
// The set is still the tenancy boundary and is still the caller's
// responsibility — the handler passes exactly the agents it has just read
// out of the caller's workspace. A searcher that does not implement this
// interface simply cannot answer a workspace-scoped query, and the handler
// says so (503) instead of quietly narrowing the scope.
type MultiAgentConversationSearcher interface {
	SearchConversationsAcross(ctx context.Context, agentIDs []string, query string, limit int) ([]ConversationSearchHit, error)
}

// maxWorkspaceSearchAgents caps how many agents one workspace-scoped query
// may span. It exists for the SQL layer, not for the product: the agent ids
// become bound parameters in an IN (...) list and SQLite's default variable
// ceiling is 999. A workspace past this cap searches its most recently
// created agents; that is a limit worth having on the record rather than a
// query that fails outright at agent 1000.
const maxWorkspaceSearchAgents = 400

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

// conversationAgent is one agent's searchable identity: the id the mirror
// stores, plus the slug and name every caller of a hit needs to display and
// link to it.
type conversationAgent struct {
	ID   string
	Slug string
	Name string
}

// workspaceSearchAgents reads the agents a workspace-scoped search may span.
// It is the tenancy boundary for that scope: the ids it returns are the ONLY
// ids handed to the searcher, and they come from the workspace on the
// request context — never from the request body.
//
// Ids come back sorted so the query the searcher runs is deterministic;
// the cap picks the most recently created agents, because a workspace at 400
// agents is far likelier to be looking for the ones it just made.
func workspaceSearchAgents(ctx context.Context, db *sql.DB, workspaceID string) ([]conversationAgent, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, slug, name FROM agents
		 WHERE workspace_id = ? AND deleted_at IS NULL
		 ORDER BY created_at DESC, id ASC
		 LIMIT ?`, workspaceID, maxWorkspaceSearchAgents)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []conversationAgent
	for rows.Next() {
		var a conversationAgent
		if err := rows.Scan(&a.ID, &a.Slug, &a.Name); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Search handles POST /api/v1/conversations/search. Body:
//
//	{"agent_id": "...", "query": "deploy pipeline", "limit": 20}
//
// agent_id is OPTIONAL. With it, the search is scoped to that one agent,
// which must belong to the caller's workspace — the authorization boundary
// that turns the searcher's "filter by agent_id" into a real tenancy
// guarantee. Without it, the search spans every agent in the
// caller's workspace, which is what ⌘K asks for: the user is searching
// everything they can see, and has no agent in mind to name.
//
// Both scopes derive the workspace from the request context, so a body can
// only ever NARROW what the caller may already read, never widen it.
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
	agentID := strings.TrimSpace(req.AgentID)
	if strings.TrimSpace(req.Query) == "" {
		replyError(w, http.StatusBadRequest, "query is required")
		return
	}

	var (
		hits  []ConversationSearchHit
		scope string
		known map[string]conversationAgent
		err   error
	)
	if agentID != "" {
		hits, known, err = h.searchOneAgent(w, r, agentID, workspaceID, req)
		scope = "agent"
	} else {
		hits, known, err = h.searchWorkspace(w, r, workspaceID, req)
		scope = "workspace"
	}
	if err != nil {
		return // the helper already replied
	}

	// Defence in depth. The searcher was handed only in-workspace agent ids,
	// so a hit for anything else is a bug in the searcher, not a query the
	// caller is entitled to see — drop it rather than render it.
	out := make([]ConversationSearchHit, 0, len(hits))
	for _, hit := range hits {
		agent, ok := known[hit.AgentID]
		if !ok {
			continue
		}
		hit.AgentSlug = agent.Slug
		hit.AgentName = agent.Name
		out = append(out, hit)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"hits":  out,
		"query": req.Query,
		"count": len(out),
		"scope": scope,
	})
}

// searchOneAgent runs the original agent-scoped search. It replies on error
// and returns a non-nil error so the caller stops.
func (h *ConversationHandler) searchOneAgent(
	w http.ResponseWriter, r *http.Request, agentID, workspaceID string, req conversationSearchRequest,
) ([]ConversationSearchHit, map[string]conversationAgent, error) {
	// Authorization: the requested agent must live in the caller's
	// workspace. Without this, a caller could pass any agent_id and read
	// its conversation history cross-tenant — the searcher only filters,
	// it does not check ownership.
	var agent conversationAgent
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, slug, name FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		agentID, workspaceID).Scan(&agent.ID, &agent.Slug, &agent.Name)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "agent not found")
		return nil, nil, err
	}
	if err != nil {
		replyError(w, http.StatusInternalServerError, "agent lookup failed")
		return nil, nil, err
	}

	hits, err := h.searcher.SearchConversations(r.Context(), agentID, req.Query, req.Limit)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return nil, nil, err
	}
	return hits, map[string]conversationAgent{agent.ID: agent}, nil
}

// searchWorkspace runs the workspace-scoped search across every agent the
// caller's workspace owns.
func (h *ConversationHandler) searchWorkspace(
	w http.ResponseWriter, r *http.Request, workspaceID string, req conversationSearchRequest,
) ([]ConversationSearchHit, map[string]conversationAgent, error) {
	multi, ok := h.searcher.(MultiAgentConversationSearcher)
	if !ok {
		// Honest 503 rather than a silent narrowing: a searcher that can
		// only do one agent cannot answer "everything I can see".
		replyError(w, http.StatusServiceUnavailable, "workspace-wide conversation search not configured")
		return nil, nil, errNoWorkspaceSearch
	}

	agents, err := workspaceSearchAgents(r.Context(), h.db, workspaceID)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "agent lookup failed")
		return nil, nil, err
	}
	known := make(map[string]conversationAgent, len(agents))
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		known[a.ID] = a
		ids = append(ids, a.ID)
	}
	if len(ids) == 0 {
		// An empty workspace is a legitimate state, not a failure: answer
		// "no matches" without troubling the searcher.
		return nil, known, nil
	}

	hits, err := multi.SearchConversationsAcross(r.Context(), ids, req.Query, req.Limit)
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return nil, nil, err
	}
	return hits, known, nil
}
