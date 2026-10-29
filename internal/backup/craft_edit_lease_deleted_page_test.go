package backup_test

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestCraftEditLeaseDeletedPageCannotEraseWorkspaceRestoreFence(t *testing.T) {
	f, _, scope, _, _ := editLeaseRecoveryFixture(t)
	dump, err := backup.DumpWorkspace(t.Context(), f.db, scope.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`DELETE FROM pages WHERE id=? AND workspace_id=?`, scope.ArtifactID, scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	// Page deletion may clear its live holder, but must not erase evidence that
	// this target issued a generation to a still-live process. Restoring an old
	// Page ID and workspace authority epoch must never bypass target protection.
	if err = backup.RestoreDump(t.Context(), f.db, dump); !errors.Is(err, backup.ErrMooringDeliveryTargetReplacement) {
		t.Fatalf("Page deletion erased issued target fence: %v", err)
	}
	var count int
	if err = f.db.QueryRow(`SELECT count(*) FROM pages WHERE id=?`, scope.ArtifactID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refused restore changed deleted Page: count=%d err=%v", count, err)
	}
}
