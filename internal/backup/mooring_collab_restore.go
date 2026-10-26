package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Collaboration fingerprints bind original workspace/artifact scope. Rewriting
// IDs or merging two histories needs a qualified import/provenance protocol;
// portable transport alone cannot turn historical Saved into new-scope Saved.
var ErrMooringCollaborationRemap = errors.New("backup: Mooring collaboration identity remapping/history merge is not qualified; restore exact IDs into an empty artifact destination")

func hasMooringCollaborationState(dump *DBDump) bool {
	if dump == nil {
		return false
	}
	for table, rows := range dump.Tables {
		if strings.HasPrefix(table, "mooring_collab_") && len(rows) > 0 {
			return true
		}
	}
	return false
}
func rejectMooringCollaborationRemap(dump *DBDump) error {
	if hasMooringCollaborationState(dump) {
		return ErrMooringCollaborationRemap
	}
	return nil
}

type collabRestoreQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func rejectMooringCollaborationCollision(ctx context.Context, db collabRestoreQuerier, dump *DBDump) error {
	if !hasMooringCollaborationState(dump) {
		return nil
	}
	pages := map[string]bool{}
	for table, rows := range dump.Tables {
		if !strings.HasPrefix(table, "mooring_collab_") {
			continue
		}
		for _, row := range rows {
			page, ok := row["page_id"].(string)
			if !ok || page == "" {
				return fmt.Errorf("backup: malformed Mooring collaboration scope")
			}
			pages[page] = true
		}
	}
	for page := range pages {
		var existing int
		err := db.QueryRowContext(ctx, `SELECT 1 FROM pages WHERE id=?`, page).Scan(&existing)
		if err == nil {
			return ErrMooringCollaborationRemap
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}
