package server

import (
	"context"
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/conversation"
)

// SearchConversationsAcross adapts conversation.Store.SearchAgents to
// api.MultiAgentConversationSearcher, which is what makes POST
// /api/v1/conversations/search answerable WITHOUT an agent_id: ⌘K searches
// everything the caller can see, and the API handler resolves that set from
// the workspace on the request context before calling this.
//
// The agent set is the tenancy boundary and arrives already authorized —
// this layer, like the agent-scoped one, only filters.
func (a *convStoreAdapter) SearchConversationsAcross(ctx context.Context, agentIDs []string, query string, limit int) ([]goapi.ConversationSearchHit, error) {
	hits, err := a.store.SearchAgents(ctx, agentIDs, query, limit)
	if err != nil {
		return nil, err
	}
	return convHitsToAPI(hits), nil
}

// convHitsToAPI is the one wire conversion for both search scopes.
func convHitsToAPI(hits []conversation.SearchHit) []goapi.ConversationSearchHit {
	out := make([]goapi.ConversationSearchHit, len(hits))
	for i, h := range hits {
		out[i] = goapi.ConversationSearchHit{
			ID:          h.ID,
			SessionID:   h.SessionID,
			AgentID:     h.AgentID,
			Role:        string(h.Role),
			Content:     h.Content,
			ToolSummary: h.ToolSummary,
			Timestamp:   h.Timestamp.UTC().Format(time.RFC3339Nano),
		}
	}
	return out
}
