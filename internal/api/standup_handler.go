package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// standupConvEntry holds a single peer conversation for standup formatting.
type standupConvEntry struct {
	question, response, status, createdAt string
	fromName, fromSlug, toName, toSlug    string
	escalated                             int
}

// standupEscEntry holds a single escalation for standup formatting.
type standupEscEntry struct {
	reason, status, createdAt, fromName, fromSlug string
}

// fetchStandupConversations queries peer conversations for a crew since the given time.
func (h *QueryHandler) fetchStandupConversations(ctx context.Context, crewID, since string) ([]standupConvEntry, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT pc.question, pc.response, pc.status, pc.escalated, pc.created_at,
		       from_a.name, from_a.slug, to_a.name, to_a.slug
		FROM peer_conversations pc
		JOIN agents from_a ON from_a.id = pc.from_agent_id
		JOIN agents to_a ON to_a.id = pc.to_agent_id
		WHERE pc.crew_id = ? AND pc.created_at >= ?
		ORDER BY pc.created_at ASC
	`, crewID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var convs []standupConvEntry
	for rows.Next() {
		var c standupConvEntry
		var nullResp sql.NullString
		if err := rows.Scan(&c.question, &nullResp, &c.status, &c.escalated, &c.createdAt,
			&c.fromName, &c.fromSlug, &c.toName, &c.toSlug); err != nil {
			return convs, fmt.Errorf("scanning standup conversation: %w", err)
		}
		c.response = nullResp.String
		convs = append(convs, c)
	}
	if err := rows.Err(); err != nil {
		return convs, fmt.Errorf("iterating standup conversations: %w", err)
	}
	return convs, nil
}

// fetchStandupEscalations queries escalations for a crew since the given time.
func (h *QueryHandler) fetchStandupEscalations(ctx context.Context, crewID, since string) ([]standupEscEntry, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT e.reason, e.status, e.created_at, from_a.name, from_a.slug
		FROM escalations e
		JOIN agents from_a ON from_a.id = e.from_agent_id
		WHERE e.crew_id = ? AND e.created_at >= ?
		ORDER BY e.created_at ASC
	`, crewID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var escs []standupEscEntry
	for rows.Next() {
		var e standupEscEntry
		if err := rows.Scan(&e.reason, &e.status, &e.createdAt, &e.fromName, &e.fromSlug); err != nil {
			return escs, fmt.Errorf("scanning standup escalation: %w", err)
		}
		escs = append(escs, e)
	}
	if err := rows.Err(); err != nil {
		return escs, fmt.Errorf("iterating standup escalations: %w", err)
	}
	return escs, nil
}

// formatStandupTimestamp returns a short HH:MM time or the raw string on parse failure.
func formatStandupTimestamp(raw string) string {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Format("15:04")
	}
	return raw
}

// formatConversations writes the peer interactions section into the builder.
func formatConversations(b *strings.Builder, convs []standupConvEntry) {
	if len(convs) == 0 {
		b.WriteString("No peer interactions in this period.\n\n")
		return
	}

	fmt.Fprintf(b, "Peer interactions (%d):\n\n", len(convs))
	for i, c := range convs {
		ts := formatStandupTimestamp(c.createdAt)
		fmt.Fprintf(b, "%d. %s -> %s: \"%s\"\n", i+1, c.fromName, c.toName, c.question)
		if c.response != "" {
			resp := c.response
			if len(resp) > 200 {
				resp = resp[:200] + "..."
			}
			fmt.Fprintf(b, "   %s: \"%s\"\n", c.toName, resp)
		}
		suffix := ""
		if c.escalated != 0 {
			suffix = ", ESCALATED"
		}
		fmt.Fprintf(b, "   (%s%s)\n\n", ts, suffix)
	}
}

// formatEscalations writes the escalations section into the builder and returns
// the pending and resolved counts.
func formatEscalations(b *strings.Builder, escs []standupEscEntry) (pending, resolved int) {
	for _, e := range escs {
		if e.status == "PENDING" {
			pending++
		} else {
			resolved++
		}
	}

	if len(escs) == 0 {
		return 0, 0
	}

	fmt.Fprintf(b, "Escalations (%d pending, %d resolved):\n", pending, resolved)
	for _, e := range escs {
		ts := formatStandupTimestamp(e.createdAt)
		fmt.Fprintf(b, "- %s [%s]: \"%s\" (%s)\n", e.fromName, e.status, e.reason, ts)
	}
	return pending, resolved
}

// Standup handles GET /api/v1/internal/standup (internal) and GET /api/v1/crews/{crewId}/standup (public).
func (h *QueryHandler) Standup(w http.ResponseWriter, r *http.Request) {
	// Always prefer the path parameter to prevent query-param override bypass
	// (e.g. /crews/A/standup?crew_id=B reading crew B's data).
	crewID := r.PathValue("crewId")
	if crewID == "" {
		// Internal route has no path param — fall back to query param.
		crewID = r.URL.Query().Get("crew_id")
	}
	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crew_id required"})
		return
	}

	// When accessed via the public (authenticated) route, validate that the crew
	// belongs to the caller's workspace to prevent cross-workspace data access.
	if wsID := WorkspaceIDFromContext(r.Context()); wsID != "" {
		if err := crewExists(r.Context(), h.db, crewID, wsID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "crew not found in workspace"})
				return
			}
			h.logger.Error("standup workspace validation", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	since := r.URL.Query().Get("since")
	if since == "" {
		since = time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	} else if t, err := time.Parse(time.RFC3339, since); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "since must be a valid RFC3339 timestamp"})
		return
	} else {
		since = t.UTC().Format(time.RFC3339)
	}

	// Fetch data
	convs, err := h.fetchStandupConversations(r.Context(), crewID, since)
	if err != nil {
		h.logger.Error("standup query conversations", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	escs, err := h.fetchStandupEscalations(r.Context(), crewID, since)
	if err != nil {
		h.logger.Error("standup query escalations", "error", err)
		// Non-fatal: keep any partial results already read before the error
	}

	// Format report
	var b strings.Builder
	b.WriteString("[CREW STANDUP]\n")

	formatConversations(&b, convs)
	pending, resolved := formatEscalations(&b, escs)

	queryCount := len(convs)
	escalationCount := pending + resolved

	fmt.Fprintf(&b, "\nSummary: %d queries", queryCount)
	if escalationCount > 0 {
		fmt.Fprintf(&b, ", %d escalations", escalationCount)
	}
	b.WriteString("\n[END CREW STANDUP]")

	writeJSON(w, http.StatusOK, map[string]string{
		"standup": b.String(),
		"crew_id": crewID,
		"since":   since,
	})
}
