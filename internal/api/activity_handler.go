package api

import (
	"context"
	"net/http"
	"sort"
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

// fetchAssignmentActivity queries recent assignments for the workspace.
// pageSize is the upper bound on rows returned (limit + offset worth of rows,
// so the caller can slice a page out of the merged feed).
func (h *QueryHandler) fetchAssignmentActivity(ctx context.Context, workspaceID string, pageSize int) []activityItem {
	rows, err := h.db.QueryContext(ctx, `
		SELECT a.id, a.task, a.status, a.result_summary, a.created_at,
		       by_a.name, by_a.slug, to_a.name, to_a.slug,
		       c.name, c.slug, c.color
		FROM assignments a
		JOIN agents by_a ON by_a.id = a.assigned_by_id
		JOIN agents to_a ON to_a.id = a.assigned_to_id
		JOIN crews c ON c.id = by_a.crew_id
		WHERE a.workspace_id = ?
		ORDER BY a.created_at DESC LIMIT ?
	`, workspaceID, pageSize)
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

// fetchPeerConversationActivity queries recent peer conversations for the workspace.
// pageSize bounds the number of rows returned so the caller can page the merged feed.
func (h *QueryHandler) fetchPeerConversationActivity(ctx context.Context, workspaceID string, pageSize int) []activityItem {
	rows, err := h.db.QueryContext(ctx, `
		SELECT pc.id, pc.question, pc.status, pc.response, pc.created_at,
		       from_a.name, from_a.slug, to_a.name, to_a.slug,
		       c.name, c.slug, c.color
		FROM peer_conversations pc
		JOIN agents from_a ON from_a.id = pc.from_agent_id
		JOIN agents to_a ON to_a.id = pc.to_agent_id
		JOIN crews c ON c.id = pc.crew_id
		WHERE pc.workspace_id = ?
		ORDER BY pc.created_at DESC LIMIT ?
	`, workspaceID, pageSize)
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

// fetchEscalationActivity queries recent escalations for the workspace.
// pageSize bounds the number of rows returned so the caller can page the merged feed.
func (h *QueryHandler) fetchEscalationActivity(ctx context.Context, workspaceID string, pageSize int) []activityItem {
	rows, err := h.db.QueryContext(ctx, `
		SELECT e.id, e.reason, e.status, e.context, e.created_at,
		       from_a.name, from_a.slug,
		       c.name, c.slug, c.color
		FROM escalations e
		JOIN agents from_a ON from_a.id = e.from_agent_id
		JOIN crews c ON c.id = e.crew_id
		WHERE e.workspace_id = ?
		ORDER BY e.created_at DESC LIMIT ?
	`, workspaceID, pageSize)
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
// Returns a unified feed of assignments, peer conversations, and escalations across all crews.
func (h *QueryHandler) ListAllActivity(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())

	limit, offset := parsePagination(r, 30, 100)

	// Each source has to return at least `limit + offset` rows so that after
	// merging + sorting the correct page window is still present even if one
	// activity type dominates the head of the feed.
	pageSize := limit + offset

	items := make([]activityItem, 0, pageSize)
	items = append(items, h.fetchAssignmentActivity(r.Context(), workspaceID, pageSize)...)
	items = append(items, h.fetchPeerConversationActivity(r.Context(), workspaceID, pageSize)...)
	items = append(items, h.fetchEscalationActivity(r.Context(), workspaceID, pageSize)...)

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
