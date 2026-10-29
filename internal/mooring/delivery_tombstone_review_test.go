package mooring

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestDeliveryReviewInPlaceWorkspaceTombstoneRestoreKeepsIssuedExpiry(t *testing.T) {
	store, db, _, owner := deliveryFixture(t)
	deliveryOpen(t, store, owner, "human-a", "live-before-tombstone-restore")
	dump, err := backup.DumpWorkspace(t.Context(), db, "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE workspaces SET deleted_at='2026-10-23T00:00:00Z' WHERE id='w'`); err != nil {
		t.Fatal(err)
	}
	// Ordinary exact-ID merge restore, no --replace path or pre-insert hook.
	if err = backup.RestoreDump(t.Context(), db, dump); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "delivery") {
			return
		}
		t.Fatalf("unexpected unrelated restore refusal: %v", err)
	}
	proof := deliveryRevoke(t, store, db, owner, "human-a", true)
	if proof.NoIssuedGates {
		t.Fatal("ordinary tombstone restore forgot an unexpired gate and prepared a false empty-set proof")
	}
}
