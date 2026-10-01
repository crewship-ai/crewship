package backup_test

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// Source-installation observations and mount references must stay local:
// importing them would misrepresent the target's scan or cleanup authority.
func TestResourceCleanupDiagnostics_ExcludedFromWorkspaceDump(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	for _, statement := range []string{
		`INSERT INTO resource_cleanup_status(instance_id,crew_id,state,complete) VALUES('source-instance','deleted-crew','observed_clear',1)`,
		`INSERT INTO resource_cleanup_mounts(instance_id,crew_id,container_id,mounts_json,observed_at) VALUES('source-instance','deleted-crew','source-container','[{"type":"volume","name":"source-volume"}]','2026-09-30T00:00:00Z')`,
		`INSERT INTO resource_cleanup_scans(instance_id,observed_at,complete) VALUES('source-instance','2026-09-30T00:00:00Z',1)`,
		`INSERT INTO resource_cleanup_installation(id,db_nonce) VALUES(1,'source-nonce')`,
	} {
		if _, err := source.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed source diagnostics: %v", err)
		}
	}
	dump, err := backup.DumpWorkspace(ctx, source, workspaceID)
	if err != nil {
		t.Fatalf("dump workspace: %v", err)
	}
	for _, table := range []string{"resource_cleanup_status", "resource_cleanup_mounts", "resource_cleanup_scans", "resource_cleanup_installation"} {
		var count int
		if err := source.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("source %s fixture missing: count=%d err=%v", table, count, err)
		}
		if _, denied := backup.NonBackedUpTables[table]; !denied {
			t.Errorf("%s lacks an explicit installation-local exclusion", table)
		}
		if _, present := dump.Tables[table]; present {
			t.Errorf("workspace dump exports installation-local %s", table)
		}
	}
}
