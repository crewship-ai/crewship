package slides

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

func TestVersionsOrderRestoreReceiptsAndHistory(t *testing.T) {
	store, db, key, original := mutationFixture(t)
	order := append([]string{}, original.Deck.SlideOrder...)
	order[0], order[1] = order[1], order[0]
	reorder := ReorderRequest{PageID: key.PageID, SchemaVersion: original.SchemaVersion, BaseRev: 1, MutationID: "order", IfOrderVersion: 1, SlideOrder: order}
	first, err := store.Reorder(t.Context(), key, reorder, initAuthority)
	if err != nil || first.Revision != 2 || first.OrderVersion != 2 {
		t.Fatalf("reorder %#v %v", first, err)
	}
	current := mutationState(t, store, key)
	expected := original.Deck
	expected.SlideOrder = order
	expectedHash, _ := DeckHash(expected)
	if current.ContentHash != expectedHash || !reflect.DeepEqual(ObjectIDs(current.Deck), ObjectIDs(original.Deck)) {
		t.Fatal("reorder changed content or identities")
	}
	restore := RestoreRequest{PageID: key.PageID, SchemaVersion: current.SchemaVersion, BaseRev: 2, MutationID: "restore", IfOrderVersion: 2, Revision: 1}
	restored, err := store.Restore(t.Context(), key, restore, initAuthority)
	if err != nil || restored.Revision != 3 || restored.OrderVersion != 3 || restored.ContentHash != original.ContentHash {
		t.Fatalf("append restore %#v %v", restored, err)
	}
	store.options.MaxRevisions = 3
	other := func(context.Context, *sql.Tx) (string, error) { return "user:other", nil }
	for _, kind := range []string{"reorder", "restore"} {
		t.Run(kind+" receipt", func(t *testing.T) {
			call := func(changed bool, authority Authorize) (Receipt, error) {
				if kind == "reorder" {
					request := reorder
					if changed {
						request.SlideOrder = original.Deck.SlideOrder
					}
					return store.Reorder(t.Context(), key, request, authority)
				}
				request := restore
				if changed {
					request.Revision = 2
				}
				return store.Restore(t.Context(), key, request, authority)
			}
			replay, e := call(false, initAuthority)
			want := first
			if kind == "restore" {
				want = restored
			}
			if e != nil || !replay.Duplicate || replay.Revision != want.Revision || replay.ContentHash != want.ContentHash {
				t.Fatalf("exact retry at quota %#v %v", replay, e)
			}
			if _, e = call(true, initAuthority); !errors.Is(e, ErrDuplicate) {
				t.Fatalf("changed retry %v", e)
			}
			if _, e = call(false, other); !errors.Is(e, ErrDuplicate) {
				t.Fatalf("actor retry %v", e)
			}
		})
	}
	store.options.MaxRevisions = 1000
	for _, request := range []ReorderRequest{
		{PageID: key.PageID, SchemaVersion: 1, BaseRev: 2, MutationID: "stale-rev", IfOrderVersion: 3, SlideOrder: order},
		{PageID: key.PageID, SchemaVersion: 1, BaseRev: 3, MutationID: "stale-order", IfOrderVersion: 2, SlideOrder: order},
		{PageID: key.PageID, SchemaVersion: 2, BaseRev: 3, MutationID: "wrong-schema", IfOrderVersion: 3, SlideOrder: order},
	} {
		if _, err = store.Reorder(t.Context(), key, request, initAuthority); !errors.Is(err, ErrConflict) {
			t.Fatalf("reorder fence %v", err)
		}
	}
	invalid := ReorderRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 3, MutationID: "invalid-order", IfOrderVersion: 3, SlideOrder: []string{order[0], order[0]}}
	if _, err = store.Reorder(t.Context(), key, invalid, initAuthority); err == nil {
		t.Fatal("invalid permutation accepted")
	}
	for _, request := range []RestoreRequest{
		{PageID: key.PageID, SchemaVersion: 1, BaseRev: 2, MutationID: "restore-stale-rev", IfOrderVersion: 3, Revision: 1},
		{PageID: key.PageID, SchemaVersion: 1, BaseRev: 3, MutationID: "restore-stale-order", IfOrderVersion: 2, Revision: 1},
	} {
		if _, err = store.Restore(t.Context(), key, request, initAuthority); !errors.Is(err, ErrConflict) {
			t.Fatalf("restore fence %v", err)
		}
	}
	history, err := store.History(t.Context(), key, initAuthority)
	if err != nil || len(history) != 3 {
		t.Fatalf("history %#v %v", history, err)
	}
	for i, row := range history {
		if row.Revision != int64(3-i) || row.ParentRevision != int64(2-i) || row.Actor != "user:slides-owner" {
			t.Fatalf("lineage %#v", row)
		}
	}
	if history[0].Kind != "restore" || history[1].Kind != "reorder" || history[0].ContentHash != original.ContentHash {
		t.Fatal("history lost exact revisions")
	}
	store.options.MaxRevisions = 1
	bounded, err := store.History(t.Context(), key, initAuthority)
	if err != nil || len(bounded) != 1 || bounded[0].Revision != 3 {
		t.Fatalf("bounded history %#v %v", bounded, err)
	}
	if _, err = store.Restore(t.Context(), key, RestoreRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 3, MutationID: "over-quota", IfOrderVersion: 3, Revision: 1}, initAuthority); !errors.Is(err, ErrQuota) {
		t.Fatalf("quota %v", err)
	}
	for _, table := range []string{"slides_revisions", "slides_receipts"} {
		countRows(t, db, table, key.PageID, 3)
	}
	var seq int
	var name, path string
	if err = db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again := New(reopened.DB, Options{})
	saved, err := again.Read(t.Context(), key, 0, initAuthority)
	if err != nil || saved.Revision != 3 || saved.ContentHash != original.ContentHash {
		t.Fatalf("reopen %#v %v", saved, err)
	}
	history, err = again.History(t.Context(), key, initAuthority)
	if err != nil || len(history) != 3 || history[0].Revision != 3 {
		t.Fatalf("reopened history %#v %v", history, err)
	}
}

func TestVersionsLiveAuthorityRollbackAndNoHistoryDisclosure(t *testing.T) {
	store, db, key, state := mutationFixture(t)
	denied := errors.New("current access revoked")
	order := append([]string{}, state.Deck.SlideOrder...)
	order[0], order[1] = order[1], order[0]
	for _, kind := range []string{"reorder", "restore", "history"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			auth := func(context.Context, *sql.Tx) (string, error) {
				calls++
				if calls == 2 {
					return "", denied
				}
				return "user:slides-owner", nil
			}
			var err error
			switch kind {
			case "reorder":
				_, err = store.Reorder(t.Context(), key, ReorderRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 1, MutationID: "revoked-order", IfOrderVersion: 1, SlideOrder: order}, auth)
			case "restore":
				_, err = store.Restore(t.Context(), key, RestoreRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 1, MutationID: "revoked-restore", IfOrderVersion: 1, Revision: 1}, auth)
			case "history":
				var rows []Revision
				rows, err = store.History(t.Context(), key, auth)
				if len(rows) != 0 {
					t.Fatal("failed authorization returned history metadata")
				}
			}
			if !errors.Is(err, denied) || calls != 2 {
				t.Fatalf("live authority calls%d %v", calls, err)
			}
			if current := mutationState(t, store, key); current.Revision != 1 || current.ContentHash != state.ContentHash {
				t.Fatal("revoked operation changed head")
			}
			for _, table := range []string{"slides_revisions", "slides_receipts"} {
				countRows(t, db, table, key.PageID, 1)
			}
		})
	}
	immediate := func(context.Context, *sql.Tx) (string, error) { return "", denied }
	if rows, err := store.History(t.Context(), key, immediate); !errors.Is(err, denied) || len(rows) != 0 {
		t.Fatalf("denied history %#v %v", rows, err)
	}
}

func TestVersionsRestoreReservedIdentityAndRequiresRealImageHook(t *testing.T) {
	store, db, key, original := mutationFixture(t)
	target := textMutation(t, original, "choose", "unused").Operations[0]
	remove := PatchRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 1, MutationID: "delete", Operations: []Operation{{Op: "delete_element", SlideID: target.SlideID, ElementID: target.ElementID, IfHash: target.IfHash}}}
	if _, err := store.Patch(t.Context(), key, remove, initAuthority); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Restore(t.Context(), key, RestoreRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 2, MutationID: "revive", IfOrderVersion: 1, Revision: 1}, initAuthority)
	if err != nil || restored.Revision != 3 || restored.ContentHash != original.ContentHash {
		t.Fatalf("reserved identity restore %#v %v", restored, err)
	}
	countRows(t, db, "slides_object_ids", key.PageID, len(ObjectIDs(original.Deck)))
	if !reflect.DeepEqual(ObjectIDs(mutationState(t, store, key).Deck), ObjectIDs(original.Deck)) {
		t.Fatal("restore invented identities")
	}
	// Retained metadata intentionally has no asset pins. Missing real image hook
	// must reject restoration before writing; this is not an admitted asset fixture.
	image := original.Deck
	image.SchemaVersion = ImageSchemaVersion
	image.Slides = append([]Slide{}, image.Slides...)
	image.Slides[0].Elements = append(append([]Element{}, image.Slides[0].Elements...), Element{ID: "retained_image", Type: "image", X: 1, Y: 1, Width: 10, Height: 10, Image: &Image{AssetID: "absent_asset", ExpectedSHA256: strings.Repeat("a", 64), Alt: "Image"}})
	canonical, err := Canonical(image)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := DeckHash(image)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE slides_revisions SET schema_version=?,canonical_json=?,content_hash=? WHERE workspace_id=? AND page_id=? AND rev=1", ImageSchemaVersion, canonical, hash, key.WorkspaceID, key.PageID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Restore(t.Context(), key, RestoreRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 3, MutationID: "unsafe-image", IfOrderVersion: 1, Revision: 1}, initAuthority); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing image attachment hook %v", err)
	}
	if current := mutationState(t, store, key); current.Revision != 3 || current.ContentHash != original.ContentHash {
		t.Fatal("unadmitted image restore changed head")
	}
	for _, table := range []string{"slides_revisions", "slides_receipts"} {
		countRows(t, db, table, key.PageID, 3)
	}
}
