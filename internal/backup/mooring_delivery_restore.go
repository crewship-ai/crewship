package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// Ordinary restore also deletes colliding soft-deleted parents. Protect their
// issued delivery evidence before hooks or tombstone purges, in the same SQL
// transaction. A concurrent writer invalidating this read snapshot causes the
// subsequent write upgrade to fail; it cannot silently delete newly issued gates.
func rejectMooringDeliveryRestoreTarget(ctx context.Context, tx *sql.Tx, dump *DBDump) error {
	if err := rejectMooringDeliveryTargetReplacement(ctx, tx, nil); err == nil {
		return nil
	} else if !errors.Is(err, ErrMooringDeliveryTargetReplacement) {
		return err
	}
	workspaces := map[string]bool{}
	if dump.WorkspaceID != "" {
		workspaces[dump.WorkspaceID] = true
	}
	for table, rows := range dump.Tables {
		for _, row := range rows {
			if value := row["workspace_id"]; value != nil {
				if _, ok := value.(string); !ok {
					return fmt.Errorf("backup: invalid Mooring restore workspace identifier")
				}
			}
			if ws, ok := row["workspace_id"].(string); ok && ws != "" {
				workspaces[ws] = true
			}
			if table == "workspaces" {
				if value := row["id"]; value != nil {
					if _, ok := value.(string); !ok {
						return fmt.Errorf("backup: invalid Mooring restore workspace identifier")
					}
				}
				if id, ok := row["id"].(string); ok && id != "" {
					workspaces[id] = true
				}
			}
		}
	}
	// A partial dump can name an existing Page without its workspace row or
	// anchor. Resolve the current target scope rather than trusting source scope.
	if len(dump.Tables["pages"]) > 0 {
		exists, err := tableExistsTx(ctx, tx, "pages")
		if err != nil {
			return err
		}
		if exists {
			for _, row := range dump.Tables["pages"] {
				if value := row["id"]; value != nil {
					if _, ok := value.(string); !ok {
						return fmt.Errorf("backup: invalid Mooring restore Page identifier")
					}
				}
				id, ok := row["id"].(string)
				if !ok || id == "" {
					continue
				}
				var ws string
				err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM pages WHERE id=?`, id).Scan(&ws)
				if err == sql.ErrNoRows {
					continue
				}
				if err != nil {
					return fmt.Errorf("backup: inspect Mooring delivery target: %w", err)
				}
				workspaces[ws] = true
			}
		}
	}
	ids := make([]string, 0, len(workspaces))
	for ws := range workspaces {
		ids = append(ids, ws)
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	// Bound SQL parameters for large instance/partial dumps without changing
	// admission meaning. Every touched workspace must pass the same guard.
	for start := 0; start < len(ids); start += 500 {
		end := min(start+500, len(ids))
		if err := rejectMooringDeliveryTargetReplacement(ctx, tx, ids[start:end]); err != nil {
			return err
		}
	}
	return nil
}
