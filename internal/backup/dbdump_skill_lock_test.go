package backup

import (
	"context"
	"database/sql"
	"testing"
)

// #5045: only an instance administrator can lock a skill, and the catalog row
// is shared by every workspace. A workspace bundle carries the rows its agents
// hold, so restoring one must not bring back a lock: otherwise a workspace
// owner could delete a skill an administrator had unlocked, restore an older
// bundle and freeze it for every workspace again.
func TestRestoreDump_SkillLockIsNotRestored(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, db, "sklock")
	row := map[string]any{
		"id": "sk_restored", "name": "restored", "slug": "restored", "display_name": "Restored",
		"source": "CUSTOM", "lifecycle_state": "active", "content": "# body",
		"locked": float64(1), "locked_by": "u_admin", "locked_at": "2026-11-06T00:00:00Z",
	}
	if _, err := RestoreDumpTxHooks(ctx, db, &DBDump{
		WorkspaceID: wsID,
		Tables:      map[string][]map[string]any{"skills": {row}},
	}, nil); err != nil {
		t.Fatalf("restore: %v", err)
	}
	var locked int
	var by, at sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT locked, locked_by, locked_at FROM skills WHERE id = 'sk_restored'`).Scan(&locked, &by, &at); err != nil {
		t.Fatalf("restored skill: %v", err)
	}
	if locked != 0 || by.Valid || at.Valid {
		t.Fatalf("restored lock = %d, %v, %v; want unlocked with no holder", locked, by, at)
	}
}
