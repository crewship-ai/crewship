package api

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// routineProjection is the small, workspace-scoped slice needed to render
// schedule and calendar rows. Loading it in bounded batches avoids one SQL
// round trip per schedule while staying below SQLite's bind-variable limit.
type routineProjection struct {
	Slug        string
	Name        string
	Status      string
	HeadVersion int
}

const routineProjectionBatchSize = 500

func loadRoutineProjections(ctx context.Context, db *sql.DB, workspaceID string, ids []string) (map[string]routineProjection, error) {
	unique := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id != "" {
			unique[id] = struct{}{}
		}
	}
	out := make(map[string]routineProjection, len(unique))
	if len(unique) == 0 {
		return out, nil
	}
	if db == nil || workspaceID == "" {
		return nil, fmt.Errorf("routine projection requires a database and workspace")
	}
	batch := make([]string, 0, routineProjectionBatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, 0, len(batch)+1)
		args = append(args, workspaceID)
		for _, id := range batch {
			args = append(args, id)
		}
		rows, err := db.QueryContext(ctx,
			`SELECT id, slug, name, COALESCE(status, 'active'), COALESCE(head_version, 0)
			   FROM pipelines
			  WHERE workspace_id = ? AND deleted_at IS NULL AND id IN (`+placeholders+")",
			args...)
		if err != nil {
			return fmt.Errorf("load routine projections: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var projection routineProjection
			if err := rows.Scan(&id, &projection.Slug, &projection.Name, &projection.Status, &projection.HeadVersion); err != nil {
				return fmt.Errorf("scan routine projection: %w", err)
			}
			out[id] = projection
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate routine projections: %w", err)
		}
		batch = batch[:0]
		return nil
	}
	for id := range unique {
		batch = append(batch, id)
		if len(batch) == routineProjectionBatchSize {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}
