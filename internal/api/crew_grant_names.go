package api

import (
	"context"
	"database/sql"

	"github.com/crewship-ai/crewship/internal/access"
)

// One snapshot query for every explicitly granted crew; never expand the roster
// or read an ungranted crew's name. The caller's final revision fence still applies.
func liveCrewGrantNames(ctx context.Context, db *sql.DB, workspace string, p access.Policy) (map[access.Right]string, error) {
	names := map[access.Right]string{}
	hasCrew := false
	for _, right := range p.Rights {
		if right.Kind == "crew" {
			hasCrew = true
			break
		}
	}
	if !hasCrew {
		return names, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT c.id,c.name,g.operation FROM live_access_grants g JOIN crews c ON c.id=g.crew_id AND c.workspace_id=? AND c.deleted_at IS NULL WHERE g.member_id=? AND g.resource_kind='crew'`, workspace, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, operation string
		if err = rows.Scan(&id, &name, &operation); err != nil {
			return nil, err
		}
		names[access.Right{Kind: "crew", ID: id, Operation: operation}] = name
	}
	return names, rows.Err()
}
