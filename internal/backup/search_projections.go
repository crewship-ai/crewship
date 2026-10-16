package backup

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// Derived text is excluded from workspace/fork bundles. Existing target cache
// rows for restored Pages must also be discarded in the same restore transaction,
// including Replace paths which preserve Page IDs and grants. Physical instance
// recovery independently clears every cache row offline through runtimeResets.
func clearRestoredSearchProjections(ctx context.Context, tx *sql.Tx, dump *DBDump) error {
	var present bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='page_search_projections')`).Scan(&present); err != nil {
		return err
	}
	if !present {
		return nil
	} // Older schemas have no derived cache to retain.
	type key struct{ workspace, page string }
	keys := map[key]bool{}
	for _, table := range []string{"pages", "documents", "slides_heads"} {
		for _, row := range dump.Tables[table] {
			field := "page_id"
			if table == "pages" {
				field = "id"
			}
			page, pok := row[field].(string)
			workspace, wok := row["workspace_id"].(string)
			if !pok || !wok || page == "" || workspace == "" {
				return fmt.Errorf("invalid restored native search Page identity")
			}
			keys[key{workspace, page}] = true
		}
	}
	ordered := make([]key, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].workspace == ordered[j].workspace {
			return ordered[i].page < ordered[j].page
		}
		return ordered[i].workspace < ordered[j].workspace
	})
	for _, key := range ordered {
		if _, err := tx.ExecContext(ctx, `DELETE FROM page_search_projections WHERE workspace_id=? AND page_id=?`, key.workspace, key.page); err != nil {
			return err
		}
	}
	return nil
}
