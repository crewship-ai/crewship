package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
)

// activityItem represents a single entry in the unified activity feed.
type activityItem struct {
	ID        string  `json:"id"`
	Type      string  `json:"type"`
	Status    string  `json:"status"`
	Summary   string  `json:"summary"`
	Detail    *string `json:"detail"`
	FromName  string  `json:"from_name"`
	FromSlug  string  `json:"from_slug"`
	ToName    *string `json:"to_name"`
	ToSlug    *string `json:"to_slug"`
	CrewName  string  `json:"crew_name"`
	CrewSlug  string  `json:"crew_slug"`
	CrewColor *string `json:"crew_color"`
	CreatedAt string  `json:"created_at"`
}

// activityFilter narrows the unified feed to a specific agent, crew, or
// workspace scope. Zero values mean "no filter". All filters AND together.
type activityFilter struct {
	WorkspaceID string
	AgentID     string
	CrewID      string
}

// buildWhere appends to `base` (which already contains `workspace_id = ?`)
// the optional agent_id / crew_id clauses for the given column names.
// Returning a ready-to-use WHERE fragment + args slice keeps the three
// per-source fetch helpers consistent without duplicating the branching.
func (f activityFilter) buildWhere(base string, args []any, agentCols []string, crewCol string) (string, []any) {
	var b strings.Builder
	b.WriteString(base)
	if f.AgentID != "" && len(agentCols) > 0 {
		b.WriteString(" AND (")
		for i, col := range agentCols {
			if i > 0 {
				b.WriteString(" OR ")
			}
			b.WriteString(col)
			b.WriteString(" = ?")
			args = append(args, f.AgentID)
		}
		b.WriteString(")")
	}
	if f.CrewID != "" && crewCol != "" {
		b.WriteString(" AND ")
		b.WriteString(crewCol)
		b.WriteString(" = ?")
		args = append(args, f.CrewID)
	}
	return b.String(), args
}

// fetchAssignmentActivity queries recent assignments for the given filter.
// pageSize is the upper bound on rows returned (limit + offset worth of rows,
// so the caller can slice a page out of the merged feed).
func (h *QueryHandler) fetchAssignmentActivity(ctx context.Context, f activityFilter, pageSize int) []activityItem {
	args := []any{f.WorkspaceID}
	where, args := f.buildWhere(
		"a.workspace_id = ?",
		args,
		[]string{"a.assigned_by_id", "a.assigned_to_id"},
		"by_a.crew_id",
	)
	args = append(args, pageSize)
	rows, err := h.db.QueryContext(ctx, `
		SELECT a.id, a.task, a.status, a.result_summary, a.created_at,
		       by_a.name, by_a.slug, to_a.name, to_a.slug,
		       c.name, c.slug, c.color
		FROM assignments a
		JOIN agents by_a ON by_a.id = a.assigned_by_id
		JOIN agents to_a ON to_a.id = a.assigned_to_id
		JOIN crews c ON c.id = by_a.crew_id
		WHERE `+where+`
		ORDER BY a.created_at DESC LIMIT ?
	`, args...)
	if err != nil {
		h.logger.Error("list activity: assignments", "error", err)
		return nil
	}
	defer rows.Close()

	var items []activityItem
	for rows.Next() {
		var item activityItem
		var resultSummary *string
		if err := rows.Scan(
			&item.ID, &item.Summary, &item.Status, &resultSummary, &item.CreatedAt,
			&item.FromName, &item.FromSlug, &item.ToName, &item.ToSlug,
			&item.CrewName, &item.CrewSlug, &item.CrewColor,
		); err != nil {
			h.logger.Error("scan activity: assignment", "error", err)
			continue
		}
		item.Type = "assignment"
		item.Detail = resultSummary
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration: assignments", "error", err)
	}
	return items
}

// fetchPeerConversationActivity queries recent peer conversations for the filter.
// pageSize bounds the number of rows returned so the caller can page the merged feed.
func (h *QueryHandler) fetchPeerConversationActivity(ctx context.Context, f activityFilter, pageSize int) []activityItem {
	args := []any{f.WorkspaceID}
	where, args := f.buildWhere(
		"pc.workspace_id = ?",
		args,
		[]string{"pc.from_agent_id", "pc.to_agent_id"},
		"pc.crew_id",
	)
	args = append(args, pageSize)
	rows, err := h.db.QueryContext(ctx, `
		SELECT pc.id, pc.question, pc.status, pc.response, pc.created_at,
		       from_a.name, from_a.slug, to_a.name, to_a.slug,
		       c.name, c.slug, c.color
		FROM peer_conversations pc
		JOIN agents from_a ON from_a.id = pc.from_agent_id
		JOIN agents to_a ON to_a.id = pc.to_agent_id
		JOIN crews c ON c.id = pc.crew_id
		WHERE `+where+`
		ORDER BY pc.created_at DESC LIMIT ?
	`, args...)
	if err != nil {
		h.logger.Error("list activity: peer_conversations", "error", err)
		return nil
	}
	defer rows.Close()

	var items []activityItem
	for rows.Next() {
		var item activityItem
		if err := rows.Scan(
			&item.ID, &item.Summary, &item.Status, &item.Detail, &item.CreatedAt,
			&item.FromName, &item.FromSlug, &item.ToName, &item.ToSlug,
			&item.CrewName, &item.CrewSlug, &item.CrewColor,
		); err != nil {
			h.logger.Error("scan activity: peer_conversation", "error", err)
			continue
		}
		item.Type = "peer_conversation"
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration: peer_conversations", "error", err)
	}
	return items
}

// fetchEscalationActivity queries recent escalations for the filter.
// pageSize bounds the number of rows returned so the caller can page the merged feed.
func (h *QueryHandler) fetchEscalationActivity(ctx context.Context, f activityFilter, pageSize int) []activityItem {
	args := []any{f.WorkspaceID}
	where, args := f.buildWhere(
		"e.workspace_id = ?",
		args,
		[]string{"e.from_agent_id"},
		"e.crew_id",
	)
	args = append(args, pageSize)
	rows, err := h.db.QueryContext(ctx, `
		SELECT e.id, e.reason, e.status, e.context, e.created_at,
		       from_a.name, from_a.slug,
		       c.name, c.slug, c.color
		FROM escalations e
		JOIN agents from_a ON from_a.id = e.from_agent_id
		JOIN crews c ON c.id = e.crew_id
		WHERE `+where+`
		ORDER BY e.created_at DESC LIMIT ?
	`, args...)
	if err != nil {
		h.logger.Error("list activity: escalations", "error", err)
		return nil
	}
	defer rows.Close()

	var items []activityItem
	for rows.Next() {
		var item activityItem
		if err := rows.Scan(
			&item.ID, &item.Summary, &item.Status, &item.Detail, &item.CreatedAt,
			&item.FromName, &item.FromSlug,
			&item.CrewName, &item.CrewSlug, &item.CrewColor,
		); err != nil {
			h.logger.Error("scan activity: escalation", "error", err)
			continue
		}
		item.Type = "escalation"
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration: escalations", "error", err)
	}
	return items
}

// ListAllActivity handles GET /api/v1/activity.
// Returns a unified feed of assignments, peer conversations, and escalations.
// Optional query params `agent_id` and `crew_id` narrow the feed server-side
// so /crews can ask for an agent- or crew-scoped timeline without pulling the
// whole workspace and filtering in the browser.
func (h *QueryHandler) ListAllActivity(w http.ResponseWriter, r *http.Request) {
	filter := activityFilter{
		WorkspaceID: WorkspaceIDFromContext(r.Context()),
		AgentID:     r.URL.Query().Get("agent_id"),
		CrewID:      r.URL.Query().Get("crew_id"),
	}

	limit, offset := parsePagination(r, 30, 100)

	// Each source has to return at least `limit + offset` rows so that after
	// merging + sorting the correct page window is still present even if one
	// activity type dominates the head of the feed.
	pageSize := limit + offset

	items := make([]activityItem, 0, pageSize)
	items = append(items, h.fetchAssignmentActivity(r.Context(), filter, pageSize)...)
	items = append(items, h.fetchPeerConversationActivity(r.Context(), filter, pageSize)...)
	items = append(items, h.fetchEscalationActivity(r.Context(), filter, pageSize)...)

	// Sort all items by created_at DESC
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt > items[j].CreatedAt
	})

	// Slice the requested page out of the merged feed.
	if offset >= len(items) {
		items = []activityItem{}
	} else {
		end := offset + limit
		if end > len(items) {
			end = len(items)
		}
		items = items[offset:end]
	}

	writeJSON(w, http.StatusOK, items)
}
