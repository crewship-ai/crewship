package backup

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/crewship-ai/crewship/internal/slides"
)

// Physical recovery runs offline against the staged database. Source principals
// are capabilities, not history: remove their complete delegation graph before
// fencing retained request histories and before promoting the recovered files.
func fencePhysicalSelectedRequests(ctx context.Context, db *sql.DB) (bool, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='slides_requests')`).Scan(&exists); err != nil || !exists {
		return false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT workspace_id,page_id,id FROM slides_requests ORDER BY workspace_id,page_id,request_seq`)
	if err != nil {
		return false, err
	}
	groups := map[slides.Key][]string{}
	count := 0
	for rows.Next() {
		var key slides.Key
		var id string
		if err = rows.Scan(&key.WorkspaceID, &key.PageID, &id); err != nil {
			rows.Close()
			return false, err
		}
		count++
		if count > 128*slides.MaxSelectedRetainedRequests {
			rows.Close()
			return false, fmt.Errorf("backup: selected request instance quota exceeded")
		}
		groups[key] = append(groups[key], id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	// Include the retained origin marker even when its original request was
	// deleted, plus legacy rows identified by their immutable original run.
	for _, table := range []string{"page_artifact_run_delegations", "artifact_run_principals"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE run_id IN (SELECT run_id FROM artifact_run_principals WHERE slides_request_origin IS NOT NULL UNION SELECT original_run_id FROM slides_requests WHERE original_run_id IS NOT NULL UNION SELECT run_id FROM slides_requests WHERE run_id IS NOT NULL)`); err != nil {
			return false, err
		}
	}
	for key, ids := range groups {
		if err = slides.FenceSelectedRestorePageTx(ctx, tx, key, ids); err != nil {
			return false, fmt.Errorf("backup: fence selected requests: %w", err)
		}
	}
	return true, tx.Commit()
}
