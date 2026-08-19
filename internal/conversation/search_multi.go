package conversation

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// searchDefaultLimit is the initial slice capacity and the limit used
	// when the caller asks for none — a constant, never caller-controlled.
	searchDefaultLimit = 20
	// searchMaxLimit is the hard ceiling on rows returned.
	searchMaxLimit = 100
)

// SearchAgents runs the BM25 keyword search across a SET of agents and
// returns up to limit hits, best match first.
//
// It is the one implementation behind both scopes: Search is this function
// with a one-element set. The set is ALWAYS applied — an empty set is an
// error, not "search everything" — because it is the isolation boundary,
// and the caller (the API handler) has already resolved it from the
// workspace on the request context. The query is wrapped via fts5Phrase so
// FTS5 operators in user input are matched as literal text.
func (s *Store) SearchAgents(ctx context.Context, agentIDs []string, query string, limit int) ([]SearchHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.db == nil {
		return nil, fmt.Errorf("conversation search mirror not configured")
	}

	ids := make([]any, 0, len(agentIDs))
	for _, id := range agentIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			ids = append(ids, trimmed)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("agent_id is required")
	}

	phrase := fts5Phrase(query)
	if phrase == "" {
		return nil, fmt.Errorf("query is required")
	}
	if limit <= 0 || limit > searchMaxLimit {
		limit = searchDefaultLimit
	}

	// JOIN the external-content FTS shadow on rowid and rank by bm25().
	// agent_id lives only on the base table, so the bare reference stays
	// unambiguous. ORDER BY bm25(fts) ASC puts the best (lowest) score
	// first. The agent set is bound as parameters — never interpolated —
	// so an id can never carry SQL of its own.
	args := make([]any, 0, len(ids)+2)
	args = append(args, ids...)
	args = append(args, phrase, limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT cm.id, cm.session_id, cm.agent_id, cm.role, cm.content, cm.tool_summary, cm.ts
		FROM conversation_messages cm
		JOIN conversation_messages_fts fts ON fts.rowid = cm.rowid
		WHERE cm.agent_id IN (`+placeholders(len(ids))+`) AND conversation_messages_fts MATCH ?
		ORDER BY bm25(conversation_messages_fts) ASC, cm.ts DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	// Preallocate with a fixed capacity (NOT the caller-influenced limit) so
	// the allocation size never depends on untrusted input; the slice grows
	// to at most `limit` rows, which the SQL LIMIT already bounds.
	out := make([]SearchHit, 0, searchDefaultLimit)
	for rows.Next() {
		var (
			h     SearchHit
			role  string
			tsStr string
		)
		if err := rows.Scan(&h.ID, &h.SessionID, &h.AgentID, &role, &h.Content, &h.ToolSummary, &tsStr); err != nil {
			return nil, fmt.Errorf("scan hit: %w", err)
		}
		h.Role = Role(role)
		if t, perr := time.Parse("2006-01-02T15:04:05.000Z", tsStr); perr == nil {
			h.Timestamp = t
		} else if t, perr := time.Parse(time.RFC3339Nano, tsStr); perr == nil {
			h.Timestamp = t
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// placeholders renders `?, ?, …` for an IN list of n bound parameters.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
