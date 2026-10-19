package documents

import (
	"context"
	"database/sql"
)

func (s *Store) Checkpoints(ctx context.Context, k Key, a Authorize) (out []map[string]any, err error) {
	out = []map[string]any{}
	err = s.tx(ctx, k, a, func(tx *sql.Tx, _ string) error {
		if _, e := state(ctx, tx, k); e != nil {
			return e
		}
		rows, e := tx.QueryContext(ctx, `SELECT id,rev,schema_version,actor,created_at FROM document_checkpoints WHERE workspace_id=? AND page_id=? ORDER BY rev DESC,id DESC LIMIT 1000`, k.WorkspaceID, k.PageID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id, actor, created string
			var rev int64
			var schema int
			if e = rows.Scan(&id, &rev, &schema, &actor, &created); e != nil {
				return e
			}
			out = append(out, map[string]any{"id": id, "rev": rev, "schema": schema, "actor": actor, "created_at": created})
		}
		return rows.Err()
	})
	return
}
