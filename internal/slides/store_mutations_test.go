package slides

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

func mutationFixture(t *testing.T) (*Store, *database.DB, Key, State) {
	t.Helper()
	db, deck := initStoreFixture(t)
	store := New(db.DB, Options{})
	key := Key{"ws-a", deck.DeckID}
	if _, err := store.Initialize(t.Context(), key, initRequest(deck, "initialize"), initAuthority); err != nil {
		t.Fatal(err)
	}
	return store, db, key, mutationState(t, store, key)
}
func mutationState(t *testing.T, store *Store, key Key) State {
	t.Helper()
	state, err := store.Read(t.Context(), key, 0, initAuthority)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
func textMutation(t *testing.T, state State, id, value string) PatchRequest {
	t.Helper()
	for _, slide := range state.Deck.Slides {
		for _, element := range slide.Elements {
			if element.Type == "text" {
				hash, err := ElementHash(element)
				if err != nil {
					t.Fatal(err)
				}
				text := *element.Text
				text.Value = value
				element.Text = &text
				return PatchRequest{PageID: state.Deck.DeckID, SchemaVersion: state.SchemaVersion, BaseRev: state.Revision, MutationID: id, Operations: []Operation{{Op: "set_element", SlideID: slide.ID, ElementID: element.ID, IfHash: hash, Element: &element}}}
			}
		}
	}
	t.Fatal("fixture has no text")
	return PatchRequest{}
}

func TestPatchDurableReceiptsQuotaAndHistoricalIDs(t *testing.T) {
	store, db, key, state := mutationFixture(t)
	request := textMutation(t, state, "edit", "Uloženo česky\nDruhý řádek")
	first, err := store.Patch(t.Context(), key, request, initAuthority)
	if err != nil || first.Revision != 2 || first.OrderVersion != 1 {
		t.Fatalf("patch %#v %v", first, err)
	}
	replay, err := store.Patch(t.Context(), key, request, initAuthority)
	if err != nil || !replay.Duplicate || replay.Revision != 2 || replay.ContentHash != first.ContentHash {
		t.Fatalf("replay %#v %v", replay, err)
	}
	changed := request
	changed.Operations = append([]Operation{}, request.Operations...)
	changed.Operations[0].IfHash = strings.Repeat("a", 64)
	if _, err = store.Patch(t.Context(), key, changed, initAuthority); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("changed retry %v", err)
	}
	otherActor := func(context.Context, *sql.Tx) (string, error) { return "user:other", nil }
	if _, err = store.Patch(t.Context(), key, request, otherActor); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("actor retry %v", err)
	}
	stale := request
	stale.MutationID = "stale"
	if _, err = store.Patch(t.Context(), key, stale, initAuthority); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision %v", err)
	}
	state = mutationState(t, store, key)
	store.options.MaxRevisions = 2
	if _, err = store.Patch(t.Context(), key, textMutation(t, state, "capacity", "new"), initAuthority); !errors.Is(err, ErrQuota) {
		t.Fatalf("retained quota %v", err)
	}
	if replay, err = store.Patch(t.Context(), key, request, initAuthority); err != nil || !replay.Duplicate {
		t.Fatalf("confirmed retry at capacity %#v %v", replay, err)
	}
	store.options.MaxRevisions = 1000
	target := textMutation(t, state, "choose", "unused").Operations[0]
	deleted := PatchRequest{PageID: key.PageID, SchemaVersion: state.SchemaVersion, BaseRev: state.Revision, MutationID: "delete", Operations: []Operation{{Op: "delete_element", SlideID: target.SlideID, ElementID: target.ElementID, IfHash: target.IfHash}}}
	if _, err = store.Patch(t.Context(), key, deleted, initAuthority); err != nil {
		t.Fatal(err)
	}
	state = mutationState(t, store, key)
	var slideHash string
	for _, slide := range state.Deck.Slides {
		if slide.ID == target.SlideID {
			slideHash, _ = SlideHash(slide)
		}
	}
	reuse := PatchRequest{PageID: key.PageID, SchemaVersion: state.SchemaVersion, BaseRev: state.Revision, MutationID: "reuse", Operations: []Operation{{Op: "insert_element", SlideID: target.SlideID, IfHash: slideHash, Element: target.Element}}}
	if _, err = store.Patch(t.Context(), key, reuse, initAuthority); !errors.Is(err, ErrConflict) {
		t.Fatalf("historical identity reused %v", err)
	}
	countRows(t, db, "slides_revisions", key.PageID, 3)
	countRows(t, db, "slides_receipts", key.PageID, 3)
	var retained int
	if err = db.QueryRow("SELECT count(*) FROM slides_object_ids WHERE page_id=? AND object_id=?", key.PageID, target.ElementID).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("lost reserved identity %d %v", retained, err)
	}
}

func TestPatchRejectedCandidateAndOrderAreAtomic(t *testing.T) {
	store, db, key, state := mutationFixture(t)
	request := textMutation(t, state, "invalid-batch", "candidate")
	request.Operations = append(request.Operations, Operation{Op: "delete_element", SlideID: request.Operations[0].SlideID, ElementID: request.Operations[0].ElementID, IfHash: request.Operations[0].IfHash})
	if _, err := store.Patch(t.Context(), key, request, initAuthority); !errors.Is(err, ErrConflict) {
		t.Fatalf("partial batch %v", err)
	}
	order := state.OrderVersion + 1
	request = textMutation(t, state, "wrong-order", "candidate")
	request.IfOrderVersion = &order
	if _, err := store.Patch(t.Context(), key, request, initAuthority); !errors.Is(err, ErrConflict) {
		t.Fatalf("order fence %v", err)
	}
	version := ImageSchemaVersion
	slide := state.Deck.Slides[0]
	hash, _ := SlideHash(slide)
	image := Element{ID: "image_one", Type: "image", X: 4, Y: 5, Width: 20, Height: 30, Image: &Image{AssetID: "asset_one", ExpectedSHA256: strings.Repeat("a", 64), Alt: "Private image"}}
	upgrade := PatchRequest{PageID: key.PageID, SchemaVersion: state.SchemaVersion, BaseRev: state.Revision, MutationID: "image", TargetSchemaVersion: &version, IfContentHash: state.ContentHash, Operations: []Operation{{Op: "insert_element", SlideID: slide.ID, IfHash: hash, Element: &image}}}
	if _, err := store.Patch(t.Context(), key, upgrade, initAuthority); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing asset guard %v", err)
	}
	if current := mutationState(t, store, key); current.Revision != state.Revision || current.ContentHash != state.ContentHash {
		t.Fatal("rejected request changed head")
	}
	for _, table := range []string{"slides_revisions", "slides_receipts"} {
		countRows(t, db, table, key.PageID, 1)
	}
	countRows(t, db, "slides_object_ids", key.PageID, len(ObjectIDs(state.Deck)))
}

func TestPatchReceiptGuardAndLiveAuthorityRollBack(t *testing.T) {
	store, db, key, state := mutationFixture(t)
	denied := errors.New("current permission revoked")
	request := textMutation(t, state, "guarded", "candidate")
	allowed := true
	authorize := func(context.Context, *sql.Tx) (string, error) {
		if !allowed {
			return "", denied
		}
		return "user:slides-owner", nil
	}
	guardCalls := 0
	guard := func(ctx context.Context, tx *sql.Tx, got Key, actor string, receipt Receipt) error {
		guardCalls++
		if got != key || actor != "user:slides-owner" || receipt.Revision != 2 {
			t.Fatal("guard lost exact receipt authority")
		}
		var inserted int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM slides_revisions WHERE page_id=? AND rev=?", key.PageID, receipt.Revision).Scan(&inserted); err != nil || inserted != 1 {
			t.Fatalf("guard outside writer tx %d %v", inserted, err)
		}
		allowed = false
		return nil
	}
	if _, err := store.PatchWithReceipt(t.Context(), key, request, authorize, guard); !errors.Is(err, denied) || guardCalls != 1 {
		t.Fatalf("final authority %d %v", guardCalls, err)
	}
	for _, table := range []string{"slides_revisions", "slides_receipts"} {
		countRows(t, db, table, key.PageID, 1)
	}
	allowed = true
	if _, err := store.Patch(t.Context(), key, request, authorize); err != nil {
		t.Fatal(err)
	}
	rejectReceipt := func(context.Context, *sql.Tx, Key, string, Receipt) error { guardCalls++; return denied }
	if _, err := store.PatchWithReceipt(t.Context(), key, request, authorize, rejectReceipt); !errors.Is(err, denied) {
		t.Fatalf("replay guard bypassed %v", err)
	}
	allowed = false
	before := guardCalls
	if _, err := store.PatchWithReceipt(t.Context(), key, request, authorize, rejectReceipt); !errors.Is(err, denied) || guardCalls != before {
		t.Fatalf("revoked replay reached guard %d %v", guardCalls, err)
	}
	for _, table := range []string{"slides_revisions", "slides_receipts"} {
		countRows(t, db, table, key.PageID, 2)
	}
}

func TestPatchConcurrentOriginalRevisionCAS(t *testing.T) {
	store, _, key, state := mutationFixture(t)
	requests := []PatchRequest{textMutation(t, state, "concurrent-a", "first"), textMutation(t, state, "concurrent-b", "second")}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, request := range requests {
		wait.Add(1)
		go func(request PatchRequest) {
			defer wait.Done()
			_, err := store.Patch(t.Context(), key, request, initAuthority)
			results <- err
		}(request)
	}
	wait.Wait()
	close(results)
	committed, conflicts := 0, 0
	for err := range results {
		if err == nil {
			committed++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if committed != 1 || conflicts != 1 || mutationState(t, store, key).Revision != 2 {
		t.Fatalf("CAS committed%d conflicts%d", committed, conflicts)
	}
}
