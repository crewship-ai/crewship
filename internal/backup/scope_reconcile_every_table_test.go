package backup_test

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// TestScopeReconciliation_EveryBackupTableIsClean runs the #2009 scope
// reconciliation over the row-per-table fixture: on a correctly scoped dump it
// must run and report no shortfall. A shortfall here is either a real scope
// filter that leaves workspace rows behind, or a reconciliation rule that would
// fail healthy bundles in production — both need fixing before merge.
func TestScopeReconciliation_EveryBackupTableIsClean(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	seedLiveMission(t, source, workspaceID)
	seedRowPerBackupTable(t, source, workspaceID)

	dump, err := backup.DumpWorkspace(ctx, source, workspaceID)
	if err != nil {
		t.Fatalf("DumpWorkspace: %v", err)
	}
	sr := dump.ScopeReconciliation()
	if sr == nil || !sr.Checked {
		t.Fatalf("reconciliation did not run: %+v", sr)
	}
	// Not vacuous: nearly every exported table has a workspace_id or a
	// foreign key into an exported workspace-owned parent.
	if sr.Tables < 80 {
		t.Fatalf("reconciled only %d tables; the schema-derived paths have stopped matching", sr.Tables)
	}
	for _, s := range sr.Shortfalls {
		t.Errorf("%s: %d of %d schema-reachable workspace rows not exported", s.Table, s.Missing, s.Reachable)
	}
}
