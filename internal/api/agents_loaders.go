package api

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Agent-specific batch loaders. Extracted from agents.go so the
// handler file can focus on HTTP surface (Create / Get / Update /
// Delete / List / FleetStatus / Load). Keep pagination helpers in
// the main handler file for now — moving those would ripple through
// crews.go and credentials.go and belongs in its own refactor pass.

// batchCountByAgentID runs a "SELECT agent_id, COUNT(*) ... GROUP BY
// agent_id" query with a placeholder list matching len(ids) and
// returns the id→count map. The caller passes the template with a
// single "%s" where the placeholder list goes.
func batchCountByAgentID(ctx context.Context, db *sql.DB, tmpl string, ids []string) (map[string]int, error) {
	if len(ids) == 0 {
		return map[string]int{}, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	query := fmt.Sprintf(tmpl, placeholders)

	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("batchCountByAgentID: query: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int, len(ids))
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("batchCountByAgentID: scan: %w", err)
		}
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("batchCountByAgentID: iterate: %w", err)
	}
	return out, nil
}
