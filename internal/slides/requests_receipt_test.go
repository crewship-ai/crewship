package slides

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestSelectedReceiptCallbackAtomicRollbackAndOriginalResultReplay(t *testing.T) {
	pin := func(ctx context.Context, tx *sql.Tx, key Key, revision int64, _ Deck) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO selected_test_pins(rev) VALUES(?)`, revision)
		return err
	}
	store, db, key, deck, _ := storeFixture(t, Options{OnRevision: pin})
	if _, err := db.Exec(`CREATE TABLE selected_test_pins(rev INTEGER PRIMARY KEY);CREATE TABLE selected_test_results(id TEXT PRIMARY KEY,rev INTEGER NOT NULL,hash TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	initializeStore(t, store, key, deck)
	anchor := commentAnchorFixture(t, deck, 1)
	createCommentFixture(t, store, key, anchor, "retained_thread")
	slide := deck.Slides[0]
	hash, _ := SlideHash(slide)
	notes := "Selected result"
	request := PatchRequest{PageID: key.PageID, SchemaVersion: deck.SchemaVersion, BaseRev: 1, MutationID: "selected_result", Operations: []Operation{{Op: "set_notes", SlideID: slide.ID, IfHash: hash, Notes: &notes}}}
	rejected := errors.New("result ownership rejected")
	reject := func(ctx context.Context, tx *sql.Tx, key Key, actor string, receipt Receipt) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO selected_test_results(id,rev,hash) VALUES(?,?,?)`, receipt.MutationID, receipt.Revision, receipt.ContentHash); err != nil {
			return err
		}
		return rejected
	}
	if _, err := store.PatchWithReceipt(t.Context(), key, request, storeAuthority, reject); !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	assertCounts := func(pins, results int, revision int64) {
		t.Helper()
		var got int
		if err := db.QueryRow(`SELECT COUNT(*) FROM selected_test_pins`).Scan(&got); err != nil || got != pins {
			t.Fatal("pin atomicity", got, err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM selected_test_results`).Scan(&got); err != nil || got != results {
			t.Fatal("result atomicity", got, err)
		}
		state, err := store.Read(t.Context(), key, 0, storeAuthority)
		if err != nil || state.Revision != revision {
			t.Fatal(state, err)
		}
		thread, err := store.CommentThread(t.Context(), key, "retained_thread", storeAuthority)
		if err != nil || thread.ProjectionRevision != revision {
			t.Fatal("comment projection atomicity", thread, err)
		}
	}
	assertCounts(1, 0, 1)
	bind := func(ctx context.Context, tx *sql.Tx, _ Key, _ string, receipt Receipt) error {
		if receipt.Duplicate {
			var revision int64
			var hash string
			if err := tx.QueryRowContext(ctx, `SELECT rev,hash FROM selected_test_results WHERE id=?`, receipt.MutationID).Scan(&revision, &hash); err != nil {
				return err
			}
			if revision != receipt.Revision || hash != receipt.ContentHash {
				return errors.New("changed original result")
			}
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO selected_test_results(id,rev,hash) VALUES(?,?,?)`, receipt.MutationID, receipt.Revision, receipt.ContentHash)
		return err
	}
	checks := 0
	finalRevoke := func(ctx context.Context, tx *sql.Tx) (string, error) {
		checks++
		if checks == 2 {
			return "", errors.New("credential revoked before commit")
		}
		return storeAuthority(ctx, tx)
	}
	if _, err := store.PatchWithReceipt(t.Context(), key, request, finalRevoke, bind); err == nil {
		t.Fatal("final credential check bypassed")
	}
	assertCounts(1, 0, 1)
	result, err := store.PatchWithReceipt(t.Context(), key, request, storeAuthority, bind)
	if err != nil || result.Revision != 2 || result.Duplicate {
		t.Fatal(result, err)
	}
	assertCounts(2, 1, 2)
	state, err := store.Read(t.Context(), key, 0, storeAuthority)
	if err != nil {
		t.Fatal(err)
	}
	humanNotes := "Later human revision"
	for _, currentSlide := range state.Deck.Slides {
		if currentSlide.ID == slide.ID {
			hash, _ = SlideHash(currentSlide)
		}
	}
	human := PatchRequest{PageID: key.PageID, SchemaVersion: state.SchemaVersion, BaseRev: 2, MutationID: "later_human", Operations: []Operation{{Op: "set_notes", SlideID: slide.ID, IfHash: hash, Notes: &humanNotes}}}
	if _, err = store.Patch(t.Context(), key, human, storeAuthority); err != nil {
		t.Fatal(err)
	}
	replay, err := store.PatchWithReceipt(t.Context(), key, request, storeAuthority, bind)
	if err != nil || !replay.Duplicate || replay.Revision != 2 || replay.ContentHash != result.ContentHash {
		t.Fatal("replay substituted latest checkpoint", replay, err)
	}
	assertCounts(3, 1, 3)
	called := false
	deny := func(context.Context, *sql.Tx) (string, error) { return "", errors.New("grant revoked") }
	callback := func(context.Context, *sql.Tx, Key, string, Receipt) error { called = true; return nil }
	if _, err = store.PatchWithReceipt(t.Context(), key, request, deny, callback); err == nil || called {
		t.Fatal("replay bypassed revoked capability", err, called)
	}
}
