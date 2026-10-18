package slides

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

func initStoreFixture(t *testing.T) (*database.DB, Deck) {
	t.Helper()
	db, err := database.Open("file:" + filepath.Join(t.TempDir(), "slides.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = database.Migrate(t.Context(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('slides-owner','slides-owner@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('ws-a','A','slides-a'),('ws-b','B','slides-b')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('fixture_page','ws-a','deck','Deck','slides-owner','{}'),('spare_page','ws-a','spare','Spare','slides-owner','{}'),('other_page','ws-b','other','Other','slides-owner','{}')`,
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	deck, _, _ := modelFixture(t)
	return db, deck
}
func initRequest(deck Deck, id string) InitializeRequest {
	return InitializeRequest{PageID: deck.DeckID, SchemaVersion: deck.SchemaVersion, MutationID: id, Deck: deck}
}
func initAuthority(context.Context, *sql.Tx) (string, error) { return "user:slides-owner", nil }
func countRows(t *testing.T, db *database.DB, table, page string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT count(*) FROM "+table+" WHERE page_id=?", page).Scan(&got); err != nil || got != want {
		t.Fatalf("%s rows %d want %d: %v", table, got, want, err)
	}
}

func TestInitializeWorkspaceCapacityAndReceiptIdentity(t *testing.T) {
	db, deck := initStoreFixture(t)
	store := New(db.DB, Options{MaxDecks: 1})
	ctx := t.Context()
	key := Key{"ws-a", deck.DeckID}
	request := initRequest(deck, "create-one")
	calls := 0
	authorize := func(ctx context.Context, tx *sql.Tx) (string, error) {
		calls++
		var owner string
		err := tx.QueryRowContext(ctx, "SELECT owner_user_id FROM pages WHERE workspace_id=? AND id=?", key.WorkspaceID, key.PageID).Scan(&owner)
		return "user:" + owner, err
	}
	first, err := store.Initialize(ctx, key, request, authorize)
	if err != nil || first.Revision != 1 || first.OrderVersion != 1 || calls != 2 {
		t.Fatalf("initialize %#v calls%d %v", first, calls, err)
	}
	countRows(t, db, "slides_revisions", key.PageID, 1)
	countRows(t, db, "slides_receipts", key.PageID, 1)
	countRows(t, db, "slides_object_ids", key.PageID, len(ObjectIDs(deck)))
	// A confirmed retry still succeeds when this workspace is at capacity.
	replay, err := store.Initialize(ctx, key, request, authorize)
	if err != nil || !replay.Duplicate || replay.ContentHash != first.ContentHash || replay.Revision != first.Revision || calls != 4 {
		t.Fatalf("retry %#v calls%d %v", replay, calls, err)
	}
	changed := request
	changed.Deck.Theme.Color = "#334455"
	if _, err = store.Initialize(ctx, key, changed, authorize); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("changed content retry: %v", err)
	}
	otherActor := func(context.Context, *sql.Tx) (string, error) { return "user:other", nil }
	if _, err = store.Initialize(ctx, key, request, otherActor); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("changed actor retry: %v", err)
	}
	spare := deck
	spare.DeckID = "spare_page"
	if _, err = store.Initialize(ctx, Key{"ws-a", spare.DeckID}, initRequest(spare, "create-spare"), initAuthority); !errors.Is(err, ErrQuota) {
		t.Fatalf("workspace capacity not enforced: %v", err)
	}
	countRows(t, db, "slides_heads", spare.DeckID, 0)
	// Another workspace's first deck must not consume A's capacity or vice versa.
	other := deck
	other.DeckID = "other_page"
	if _, err = store.Initialize(ctx, Key{"ws-b", other.DeckID}, initRequest(other, "create-other"), initAuthority); err != nil {
		t.Fatalf("other workspace blocked by global capacity: %v", err)
	}
	for _, revision := range []int64{0, 1} {
		state, err := store.Read(ctx, key, revision, authorize)
		if err != nil || state.ContentHash != first.ContentHash || state.Deck.DeckID != key.PageID {
			t.Fatalf("read revision%d %#v %v", revision, state, err)
		}
	}
	if _, err = store.Read(ctx, Key{"ws-b", key.PageID}, 0, initAuthority); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace content: %v", err)
	}
	denied := errors.New("live permission revoked")
	revoke := func(context.Context, *sql.Tx) (string, error) { return "", denied }
	if _, err = store.Initialize(ctx, key, request, revoke); !errors.Is(err, denied) {
		t.Fatalf("revoked receipt replay: %v", err)
	}
	if _, err = store.Read(ctx, key, 0, revoke); !errors.Is(err, denied) {
		t.Fatalf("revoked read: %v", err)
	}
	countRows(t, db, "slides_revisions", key.PageID, 1)
	countRows(t, db, "slides_receipts", key.PageID, 1)
}

func TestInitializeFinalAuthorityAndAssetGuardRollBack(t *testing.T) {
	db, deck := initStoreFixture(t)
	ctx := t.Context()
	key := Key{"ws-a", deck.DeckID}
	request := initRequest(deck, "create-rollback")
	denied := errors.New("rejected before commit")
	calls := 0
	finalDenied := func(context.Context, *sql.Tx) (string, error) {
		calls++
		if calls == 2 {
			return "", denied
		}
		return "user:slides-owner", nil
	}
	store := New(db.DB, Options{})
	if _, err := store.Initialize(ctx, key, request, finalDenied); !errors.Is(err, denied) || calls != 2 {
		t.Fatalf("final authority calls%d %v", calls, err)
	}
	for _, table := range []string{"slides_heads", "slides_revisions", "slides_receipts", "slides_object_ids"} {
		countRows(t, db, table, key.PageID, 0)
	}
	guard := func(context.Context, *sql.Tx, Key, int64, Deck) error { return denied }
	if _, err := New(db.DB, Options{OnRevision: guard}).Initialize(ctx, key, request, initAuthority); !errors.Is(err, denied) {
		t.Fatalf("asset hook rejection ignored: %v", err)
	}
	for _, table := range []string{"slides_heads", "slides_revisions", "slides_receipts", "slides_object_ids"} {
		countRows(t, db, table, key.PageID, 0)
	}
	imageDeck := deck
	imageDeck.SchemaVersion = ImageSchemaVersion
	imageDeck.Slides = append([]Slide{}, deck.Slides...)
	imageDeck.Slides[0].Elements = append(append([]Element{}, deck.Slides[0].Elements...), Element{ID: "image_one", Type: "image", X: 4, Y: 5, Width: 20, Height: 30, Image: &Image{AssetID: "asset_one", ExpectedSHA256: strings.Repeat("a", 64), Alt: "Private image"}})
	if _, err := store.Initialize(ctx, key, initRequest(imageDeck, "create-image"), initAuthority); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing real asset pin hook accepted: %v", err)
	}
	countRows(t, db, "slides_heads", key.PageID, 0)
	if _, err := store.Initialize(ctx, key, request, nil); err == nil {
		t.Fatal("missing Go authority accepted")
	}
	schema2 := deck
	schema2.SchemaVersion = ImageSchemaVersion
	if _, err := store.Initialize(ctx, key, initRequest(schema2, "create-schema2"), initAuthority); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int64{0, 1} {
		state, err := store.Read(ctx, key, revision, initAuthority)
		if err != nil || state.SchemaVersion != ImageSchemaVersion || state.Deck.SchemaVersion != ImageSchemaVersion {
			t.Fatalf("schema2 revision%d %#v %v", revision, state, err)
		}
	}
	if _, err := db.Exec("UPDATE slides_heads SET schema_version=99 WHERE page_id=?", key.PageID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(ctx, key, 0, initAuthority); !errors.Is(err, ErrSchema) {
		t.Fatalf("unknown stored schema accepted: %v", err)
	}
}
