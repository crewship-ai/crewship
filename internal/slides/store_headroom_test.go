package slides

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/storageheadroom"
)

func slidesAccounting(t *testing.T, db *database.DB) (workspace, instance int64) {
	t.Helper()
	if err := db.QueryRow(`SELECT coalesce((SELECT used_bytes+reserved_bytes FROM artifact_storage_workspaces WHERE workspace_id='ws-a'),0),(SELECT used_bytes+reserved_bytes FROM artifact_storage_global WHERE id=1)`).Scan(&workspace, &instance); err != nil {
		t.Fatal(err)
	}
	return workspace, instance
}

// B9 (#4826): a Slides write below the free-disk reserve is refused with the
// shared storage-full sentinel before any row is written, an exact replay of
// confirmed work still answers, and the same request saves once space returns.
func TestSlidesWriteRefusesBelowFreeDiskReserve(t *testing.T) {
	db, deck := initStoreFixture(t)
	ctx := t.Context()
	store := New(db.DB, Options{})
	key := Key{"ws-a", deck.DeckID}
	confirmed := initRequest(deck, "confirmed")
	first, err := store.Initialize(ctx, key, confirmed, initAuthority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE artifact_storage_global SET min_free_bytes=? WHERE id=1`, int64(1)<<62); err != nil {
		t.Fatal(err)
	}
	workspace, instance := slidesAccounting(t, db)
	spare := deck
	spare.DeckID = "spare_page"
	spareKey := Key{"ws-a", spare.DeckID}
	request := initRequest(spare, "low-disk")
	if _, err = store.Initialize(ctx, spareKey, request, initAuthority); !errors.Is(err, storageheadroom.ErrInsufficient) {
		t.Fatalf("Slides write admitted below the free-disk reserve: %v", err)
	}
	for _, table := range []string{"slides_heads", "slides_revisions", "slides_receipts", "slides_object_ids"} {
		countRows(t, db, table, spare.DeckID, 0)
	}
	if w, i := slidesAccounting(t, db); w != workspace || i != instance {
		t.Fatalf("refused write moved accounting: workspace %d->%d instance %d->%d", workspace, w, instance, i)
	}
	replay, err := store.Initialize(ctx, key, confirmed, initAuthority)
	if err != nil || !replay.Duplicate || replay.Revision != first.Revision {
		t.Fatalf("confirmed replay refused below reserve: %#v %v", replay, err)
	}
	if state, err := store.Read(ctx, key, 0, initAuthority); err != nil || state.ContentHash != first.ContentHash {
		t.Fatalf("confirmed deck unreadable below reserve: %v", err)
	}
	if _, err = db.Exec(`UPDATE artifact_storage_global SET min_free_bytes=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if saved, err := store.Initialize(ctx, spareKey, request, initAuthority); err != nil || saved.Revision != 1 || saved.Duplicate {
		t.Fatalf("retained request did not save after space returned: %#v %v", saved, err)
	}
}
