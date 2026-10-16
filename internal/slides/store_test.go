package slides

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

func storeAuthority(context.Context, *sql.Tx) (string, error) { return "user:slides-owner", nil }
func storeFixture(t *testing.T, options Options) (*Store, *database.DB, Key, Deck, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "slides.db")
	db, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if err = database.Migrate(t.Context(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`INSERT INTO users(id,email) VALUES('slides-owner','slides-owner@example.test')`, `INSERT INTO workspaces(id,name,slug) VALUES('slides-workspace','Slides','slides-workspace')`, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('fixture_page','slides-workspace','slides','Slides','slides-owner','{}')`} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	deck, _, _ := modelFixture(t)
	key := Key{"slides-workspace", "fixture_page"}
	return New(db.DB, options), db, key, deck, path
}
func initializeStore(t *testing.T, store *Store, key Key, deck Deck) Receipt {
	t.Helper()
	result, err := store.Initialize(t.Context(), key, InitializeRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 0, MutationID: "initialize_one", Deck: deck}, storeAuthority)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func textPatch(t *testing.T, state State, id, value string) PatchRequest {
	t.Helper()
	for _, slide := range state.Deck.Slides {
		for _, element := range slide.Elements {
			if element.Type == "text" {
				hash, _ := ElementHash(element)
				text := *element.Text
				text.Value = value
				element.Text = &text
				return PatchRequest{PageID: state.Deck.DeckID, SchemaVersion: 1, BaseRev: state.Revision, MutationID: id, Operations: []Operation{{Op: "set_element", SlideID: slide.ID, ElementID: element.ID, IfHash: hash, Element: &element}}}
			}
		}
	}
	t.Fatal("fixture text missing")
	return PatchRequest{}
}
func currentState(t *testing.T, store *Store, key Key) State {
	t.Helper()
	state, err := store.Read(t.Context(), key, 0, storeAuthority)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
func assertRows(t *testing.T, db *database.DB, table string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != want {
		t.Fatalf("%s rows=%d want%d: %v", table, count, want, err)
	}
}
func TestStoreDurableCASReceiptsReorderAndRestore(t *testing.T) {
	store, db, key, deck, path := storeFixture(t, Options{})
	init := initializeStore(t, store, key, deck)
	if init.Revision != 1 || init.OrderVersion != 1 {
		t.Fatal(init)
	}
	state := currentState(t, store, key)
	request := textPatch(t, state, "text_change", "Uloženo česky\nDruhý řádek")
	patch, err := store.Patch(t.Context(), key, request, storeAuthority)
	if err != nil || patch.Revision != 2 || patch.OrderVersion != 1 {
		t.Fatalf("patch %#v: %v", patch, err)
	}
	replay, err := store.Patch(t.Context(), key, request, storeAuthority)
	if err != nil || !replay.Duplicate || replay.Revision != 2 {
		t.Fatalf("replay %#v: %v", replay, err)
	}
	different := request
	different.Operations = append([]Operation{}, request.Operations...)
	different.Operations[0].IfHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err = store.Patch(t.Context(), key, different, storeAuthority); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("changed retry: %v", err)
	}
	stale := request
	stale.MutationID = "stale"
	if _, err = store.Patch(t.Context(), key, stale, storeAuthority); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	state = currentState(t, store, key)
	beforeIDs := ObjectIDs(state.Deck)
	order := append([]string{}, state.Deck.SlideOrder...)
	order[0], order[1] = order[1], order[0]
	reordered, err := store.Reorder(t.Context(), key, ReorderRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 2, MutationID: "reorder", IfOrderVersion: 1, SlideOrder: order}, storeAuthority)
	if err != nil || reordered.Revision != 3 || reordered.OrderVersion != 2 {
		t.Fatalf("reorder %#v %v", reordered, err)
	}
	state = currentState(t, store, key)
	for id, kind := range beforeIDs {
		if ObjectIDs(state.Deck)[id] != kind {
			t.Fatal("reorder changed identity")
		}
	}
	if _, err = store.Reorder(t.Context(), key, ReorderRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 3, MutationID: "stale_order", IfOrderVersion: 1, SlideOrder: deck.SlideOrder}, storeAuthority); !errors.Is(err, ErrConflict) {
		t.Fatalf("separate order precondition: %v", err)
	}
	restored, err := store.Restore(t.Context(), key, RestoreRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 3, MutationID: "restore", IfOrderVersion: 2, Revision: 1}, storeAuthority)
	if err != nil || restored.Revision != 4 || restored.OrderVersion != 3 {
		t.Fatalf("restore %#v %v", restored, err)
	}
	final := currentState(t, store, key)
	originalHash, _ := DeckHash(deck)
	if final.ContentHash != originalHash {
		t.Fatal("restore did not restore original canonical bytes")
	}
	history, err := store.History(t.Context(), key, storeAuthority)
	if err != nil || len(history) != 4 || history[0].ParentRevision != 3 || history[0].Actor != "user:slides-owner" {
		t.Fatalf("history %#v %v", history, err)
	}
	assertRows(t, db, "slides_receipts", 4)
	db.Close()
	reopened, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again := New(reopened.DB, Options{})
	reloaded := currentState(t, again, key)
	if reloaded.Revision != 4 || reloaded.ContentHash != final.ContentHash {
		t.Fatal("SQLite close/reopen lost durable head")
	}
	history, err = again.History(t.Context(), key, storeAuthority)
	if err != nil || len(history) != 4 {
		t.Fatal("reopen lost history", err)
	}
	start := time.Now()
	for i := 0; i < 10; i++ {
		currentState(t, again, key)
	}
	t.Logf("diagnostic: 10 serialized authenticated fixture reads in %s; not an API latency guarantee", time.Since(start))
}
func TestStoreRejectsHistoricalIdentityReuseButAllowsExplicitRestore(t *testing.T) {
	store, db, key, deck, _ := storeFixture(t, Options{})
	initializeStore(t, store, key, deck)
	state := currentState(t, store, key)
	request := textPatch(t, state, "choose", "changed")
	op := request.Operations[0]
	original := *op.Element
	request.MutationID = "delete"
	request.Operations = []Operation{{Op: "delete_element", SlideID: op.SlideID, ElementID: op.ElementID, IfHash: op.IfHash}}
	if _, err := store.Patch(t.Context(), key, request, storeAuthority); err != nil {
		t.Fatal(err)
	}
	state = currentState(t, store, key)
	var slideHash string
	for _, slide := range state.Deck.Slides {
		if slide.ID == op.SlideID {
			slideHash, _ = SlideHash(slide)
		}
	}
	reuse := PatchRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 2, MutationID: "reuse", Operations: []Operation{{Op: "insert_element", SlideID: op.SlideID, IfHash: slideHash, Element: &original}}}
	if _, err := store.Patch(t.Context(), key, reuse, storeAuthority); !errors.Is(err, ErrConflict) {
		t.Fatalf("historical ID reused: %v", err)
	}
	assertRows(t, db, "slides_revisions", 2)
	if _, err := store.Restore(t.Context(), key, RestoreRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 2, MutationID: "revive_original", IfOrderVersion: 1, Revision: 1}, storeAuthority); err != nil {
		t.Fatal("explicit original identity restore", err)
	}
	if ObjectIDs(currentState(t, store, key).Deck)[op.ElementID] != "element" {
		t.Fatal("restore lost original identity")
	}
}
func TestStoreConcurrentWholeRevisionCAS(t *testing.T) {
	store, _, key, deck, _ := storeFixture(t, Options{})
	initializeStore(t, store, key, deck)
	state := currentState(t, store, key)
	requests := []PatchRequest{textPatch(t, state, "concurrent_a", "first"), textPatch(t, state, "concurrent_b", "second")}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, request := range requests {
		wait.Add(1)
		go func(request PatchRequest) {
			defer wait.Done()
			_, err := store.Patch(context.Background(), key, request, storeAuthority)
			results <- err
		}(request)
	}
	wait.Wait()
	close(results)
	ok, conflicts := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != 1 || currentState(t, store, key).Revision != 2 {
		t.Fatal("CAS did not serialize", ok, conflicts)
	}
}
func TestStoreLiveAuthorityRecheckedForReplayAndBeforeCommit(t *testing.T) {
	store, db, key, deck, _ := storeFixture(t, Options{})
	initializeStore(t, store, key, deck)
	state := currentState(t, store, key)
	request := textPatch(t, state, "expires", "should rollback")
	allowed := true
	deny := errors.New("principal expired")
	authorize := func(context.Context, *sql.Tx) (string, error) {
		if !allowed {
			return "", deny
		}
		return "user:slides-owner", nil
	}
	store.options.OnRevision = func(ctx context.Context, tx *sql.Tx, key Key, revision int64, deck Deck) error {
		var found int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM slides_revisions WHERE workspace_id=? AND page_id=? AND rev=?`, key.WorkspaceID, key.PageID, revision).Scan(&found); err != nil {
			return err
		}
		if found != 1 {
			return errors.New("hook must see inserted immutable revision")
		}
		allowed = false
		return nil
	}
	if _, err := store.Patch(t.Context(), key, request, authorize); !errors.Is(err, deny) {
		t.Fatalf("expiry before commit ignored: %v", err)
	}
	assertRows(t, db, "slides_revisions", 1)
	assertRows(t, db, "slides_receipts", 1)
	if currentState(t, store, key).Revision != 1 {
		t.Fatal("unauthorized mutation persisted")
	}
	store.options.OnRevision = nil
	if _, err := store.Initialize(t.Context(), key, InitializeRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 0, MutationID: "initialize_one", Deck: deck}, authorize); !errors.Is(err, deny) {
		t.Fatalf("revoked replay returned old receipt: %v", err)
	}
}
func TestStoreQuotaHookAndForkReceiptRejectionsAreAtomic(t *testing.T) {
	store, db, key, deck, _ := storeFixture(t, Options{MaxRevisions: 1})
	initializeStore(t, store, key, deck)
	request := textPatch(t, currentState(t, store, key), "quota", "overflow")
	if _, err := store.Patch(t.Context(), key, request, storeAuthority); !errors.Is(err, ErrQuota) {
		t.Fatal("quota bypass", err)
	}
	assertRows(t, db, "slides_revisions", 1)
	assertRows(t, db, "slides_receipts", 1)
	store.options.MaxRevisions = 1000
	reject := errors.New("asset guard rejected")
	store.options.OnRevision = func(context.Context, *sql.Tx, Key, int64, Deck) error { return reject }
	request.MutationID = "hook"
	if _, err := store.Patch(t.Context(), key, request, storeAuthority); !errors.Is(err, reject) {
		t.Fatal(err)
	}
	assertRows(t, db, "slides_revisions", 1)
	assertRows(t, db, "slides_receipts", 1)
	store.options.OnRevision = nil
	if _, err := db.Exec(`DELETE FROM slides_receipts`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Initialize(t.Context(), key, InitializeRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 0, MutationID: "initialize_one", Deck: deck}, storeAuthority); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("inert fork historical ID must409, not replay/SQL500: %v", err)
	}
}

// A new OS process reads and commits against the persisted database, rather
// than sharing the first Store, database connection or in-memory candidate.
func TestStoreSeparateProcessRestart(t *testing.T) {
	if path := os.Getenv("CREWSHIP_SLIDES_RESTART_TEST_DB"); path != "" {
		db, err := database.Open("file:" + path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		store := New(db.DB, Options{})
		key := Key{"slides-workspace", "fixture_page"}
		state, err := store.Read(t.Context(), key, 0, storeAuthority)
		if err != nil || state.Revision != 1 {
			t.Fatalf("restart read: %#v %v", state, err)
		}
		if _, err := store.Patch(t.Context(), key, textPatch(t, state, "from_new_process", "Confirmed after process restart"), storeAuthority); err != nil {
			t.Fatal(err)
		}
		return
	}
	store, db, key, deck, path := storeFixture(t, Options{})
	initializeStore(t, store, key, deck)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	childContext, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(childContext, os.Args[0], "-test.run=^TestStoreSeparateProcessRestart$", "-test.v")
	command.Env = append(os.Environ(), "CREWSHIP_SLIDES_RESTART_TEST_DB="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("new process: %v\n%s", err, output)
	}
	reopened, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state, err := New(reopened.DB, Options{}).Read(t.Context(), key, 0, storeAuthority)
	if err != nil || state.Revision != 2 {
		t.Fatalf("restarted process commit not durable: %#v %v", state, err)
	}
}
