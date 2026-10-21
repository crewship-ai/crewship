package slides

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func notesPatch(t *testing.T, state State, id, notes string) PatchRequest {
	t.Helper()
	hash, err := SlideHash(state.Deck.Slides[0])
	if err != nil {
		t.Fatal(err)
	}
	return PatchRequest{PageID: state.Deck.DeckID, SchemaVersion: 1, BaseRev: state.Revision, MutationID: id, Operations: []Operation{{Op: "set_notes", SlideID: state.Deck.Slides[0].ID, IfHash: hash, Notes: &notes}}}
}

func TestStorePatchPersistsAndReplaysOriginalRevision(t *testing.T) {
	db, deck := initStoreFixture(t)
	store := New(db.DB, Options{})
	key := Key{"ws-a", deck.DeckID}
	ctx := t.Context()
	if _, err := store.Initialize(ctx, key, initRequest(deck, "initialize"), initAuthority); err != nil {
		t.Fatal(err)
	}
	first, _ := store.Read(ctx, key, 0, initAuthority)
	request := notesPatch(t, first, "patch-one", "Changed notes")
	calls := 0
	authority := func(context.Context, *sql.Tx) (string, error) { calls++; return "user:slides-owner", nil }
	result, err := store.Patch(ctx, key, request, authority)
	if err != nil || result.Revision != 2 || result.OrderVersion != 1 || calls != 2 {
		t.Fatalf("patch %#v calls=%d err=%v", result, calls, err)
	}
	saved, err := store.Read(ctx, key, 0, initAuthority)
	if err != nil || saved.Deck.Slides[0].Notes != "Changed notes" || saved.ContentHash == first.ContentHash {
		t.Fatalf("saved %#v %v", saved, err)
	}
	next := notesPatch(t, saved, "patch-two", "Later notes")
	if _, err = store.Patch(ctx, key, next, initAuthority); err != nil {
		t.Fatal(err)
	}
	replay, err := store.Patch(ctx, key, request, authority)
	if err != nil || !replay.Duplicate || replay.Revision != 2 || replay.ContentHash != result.ContentHash || calls != 4 {
		t.Fatalf("original replay %#v %v", replay, err)
	}
	original, err := store.Read(ctx, key, replay.Revision, initAuthority)
	if err != nil || original.Deck.Slides[0].Notes != "Changed notes" {
		t.Fatalf("original revision %#v %v", original, err)
	}
	changed := request
	changed.Operations = append([]Operation(nil), request.Operations...)
	note := "Altered payload"
	changed.Operations[0].Notes = &note
	if _, err = store.Patch(ctx, key, changed, initAuthority); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("changed replay %v", err)
	}
	other := func(context.Context, *sql.Tx) (string, error) { return "user:other", nil }
	if _, err = store.Patch(ctx, key, request, other); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("actor replay %v", err)
	}
	stale := request
	stale.MutationID = "stale"
	if _, err = store.Patch(ctx, key, stale, initAuthority); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale base %v", err)
	}
	current, _ := store.Read(ctx, key, 0, initAuthority)
	badHash := notesPatch(t, current, "bad-hash", "No")
	badHash.Operations[0].IfHash = first.ContentHash
	if _, err = store.Patch(ctx, key, badHash, initAuthority); !errors.Is(err, ErrConflict) {
		t.Fatalf("bad hash %v", err)
	}
	order := int64(99)
	badOrder := notesPatch(t, current, "bad-order", "No")
	badOrder.IfOrderVersion = &order
	if _, err = store.Patch(ctx, key, badOrder, initAuthority); !errors.Is(err, ErrConflict) {
		t.Fatalf("bad order %v", err)
	}
	for _, table := range []string{"slides_revisions", "slides_receipts"} {
		countRows(t, db, table, key.PageID, 3)
	}
}

func TestStorePatchRollbackAndHistoricalIdentity(t *testing.T) {
	db, deck := initStoreFixture(t)
	ctx := t.Context()
	key := Key{"ws-a", deck.DeckID}
	store := New(db.DB, Options{})
	if _, err := store.Initialize(ctx, key, initRequest(deck, "initialize"), initAuthority); err != nil {
		t.Fatal(err)
	}
	initial, _ := store.Read(ctx, key, 0, initAuthority)
	denied := errors.New("revoked before commit")
	calls := 0
	finalDenied := func(context.Context, *sql.Tx) (string, error) {
		calls++
		if calls == 2 {
			return "", denied
		}
		return "user:slides-owner", nil
	}
	request := notesPatch(t, initial, "rollback", "No")
	if _, err := store.Patch(ctx, key, request, finalDenied); !errors.Is(err, denied) {
		t.Fatalf("final authority %v", err)
	}
	guard := func(context.Context, *sql.Tx, Key, int64, Deck) error { return denied }
	if _, err := New(db.DB, Options{OnRevision: guard}).Patch(ctx, key, request, initAuthority); !errors.Is(err, denied) {
		t.Fatalf("guard %v", err)
	}
	if _, err := New(db.DB, Options{MaxRevisions: 1}).Patch(ctx, key, request, initAuthority); !errors.Is(err, ErrQuota) {
		t.Fatalf("quota %v", err)
	}
	for _, table := range []string{"slides_revisions", "slides_receipts"} {
		countRows(t, db, table, key.PageID, 1)
	}
	target := deck.Slides[0].Elements[0]
	hash, _ := ElementHash(target)
	deletion := PatchRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 1, MutationID: "delete-element", Operations: []Operation{{Op: "delete_element", SlideID: deck.Slides[0].ID, ElementID: target.ID, IfHash: hash}}}
	if _, err := store.Patch(ctx, key, deletion, initAuthority); err != nil {
		t.Fatal(err)
	}
	current, _ := store.Read(ctx, key, 0, initAuthority)
	slideHash, _ := SlideHash(current.Deck.Slides[0])
	reuse := PatchRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 2, MutationID: "reuse-element", Operations: []Operation{{Op: "insert_element", SlideID: deck.Slides[0].ID, IfHash: slideHash, Element: &target}}}
	if _, err := store.Patch(ctx, key, reuse, initAuthority); !errors.Is(err, ErrConflict) {
		t.Fatalf("historical ID reuse %v", err)
	}
	countRows(t, db, "slides_object_ids", key.PageID, len(ObjectIDs(deck)))
	for _, table := range []string{"slides_revisions", "slides_receipts"} {
		countRows(t, db, table, key.PageID, 2)
	}
	currentAfter, _ := store.Read(ctx, key, 0, initAuthority)
	if currentAfter.ContentHash != current.ContentHash {
		t.Fatal("failed reuse changed head")
	}
}
