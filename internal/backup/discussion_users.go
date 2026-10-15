package backup

import (
	"context"
	"database/sql"
	"fmt"
)

// Discussion attribution survives membership removal. Carry only identities
// referenced by comments in the exported workspace, including historical edits.
// Optional columns are probed for backups of older schemas.
func includeDiscussionUsers(ctx context.Context, tx *sql.Tx, workspace, where string, args []any) (string, []any, error) {
	for _, family := range []string{"page_artifact_", "page_board_"} {
		for _, ref := range []struct {
			table   string
			columns []string
		}{
			{"threads", []string{"created_by_user_id", "resolved_by_user_id"}},
			{"comments", []string{"author_user_id", "edited_by_user_id", "deleted_by_user_id"}},
			{"comment_history", []string{"actor_user_id"}},
			{"comment_reactions", []string{"user_id"}},
			{"comment_mutations", []string{"actor_user_id"}},
		} {
			table := family + ref.table
			for _, column := range ref.columns {
				exists, err := tableHasColumn(ctx, tx, table, column)
				if err != nil {
					return "", nil, fmt.Errorf("backup: probe discussion attribution: %w", err)
				}
				if !exists {
					continue
				}
				filter, scopedArgs, _ := workspaceFilterSQL(table, workspace)
				where = "(" + where + ") OR id IN (SELECT " + column + " FROM " + table + " WHERE (" + filter + ") AND " + column + " IS NOT NULL)"
				args = append(args, scopedArgs...)
			}
		}
	}
	return where, args, nil
}
