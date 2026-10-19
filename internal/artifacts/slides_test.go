package artifacts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/slides"
)

var errSlidesAdapterDenied = errors.New("current Slides authority denied")

func slidesAdapterFixture(t *testing.T) (*sql.DB, *slides.Store, slides.Key, slides.InitializeRequest, slides.Authorize) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "slides-adapter.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = database.Migrate(t.Context(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('owner','owner@slides.example'),('successor','successor@slides.example')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('ws','Slides','slides')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('fixture_page','ws','deck','Deck','owner','{}')`,
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile("../../testdata/slides-schema-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Deck slides.Deck }
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	key := slides.Key{WorkspaceID: "ws", PageID: fixture.Deck.DeckID}
	auth := func(ctx context.Context, tx *sql.Tx) (string, error) {
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM pages WHERE workspace_id=? AND id=?`, key.WorkspaceID, key.PageID).Scan(&owner); err != nil {
			return "", err
		}
		if owner != "owner" {
			return "", errSlidesAdapterDenied
		}
		return "user:" + owner, nil
	}
	return db.DB, slides.New(db.DB, slides.Options{}), key, slides.InitializeRequest{PageID: key.PageID, SchemaVersion: 1, MutationID: "create", Deck: fixture.Deck}, auth
}
func slidesAdapterExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestSlidesTypedRegistryCapabilitiesAndFlags(t *testing.T) {
	_, store, _, _, _ := slidesAdapterFixture(t)
	adapter := NewSlidesAdapter(store, true)
	flags := EnabledTypes{"slides": true}
	registry := NewRegistry(flags, nil, WithSlidesAdapter(adapter))
	flags["slides"] = false
	var operations SlideOperations
	var ok bool
	operations, ok = registry.Slides()
	if !ok || operations != adapter {
		t.Fatal("installed typed Slides unavailable")
	}
	for _, c := range registry.Capabilities("slides", 1) {
		want := c.Operation != Snapshot && c.Operation != Export
		if c.Supported != want || c.Available != want {
			t.Fatalf("declaration %#v", c)
		}
	}
	for _, test := range []struct {
		registry *Registry
		want     error
	}{
		{NewRegistry(EnabledTypes{"slides": true}, nil), ErrUnavailable},
		{NewRegistry(EnabledTypes{"slides": true}, nil, WithSlidesAdapter(nil)), ErrUnavailable},
		{NewRegistry(EnabledTypes{"slides": true}, nil, WithSlidesAdapter(NewSlidesAdapter(nil, true))), ErrUnavailable},
		{NewRegistry(nil, nil, WithSlidesAdapter(adapter)), ErrDisabled},
		{NewRegistry(EnabledTypes{"slides": true}, nil, WithSlidesAdapter(NewSlidesAdapter(store, false))), ErrDisabled},
		{nil, ErrUnavailable},
	} {
		if value, ok := test.registry.Slides(); ok || value != nil {
			t.Fatal("unavailable adapter exposed")
		}
		if err := test.registry.Check("slides", 1, Read); !errors.Is(err, test.want) {
			t.Fatalf("availability %v want %v", err, test.want)
		}
	}
	for _, schema := range []int{2, 99} {
		if err := registry.Check("slides", schema, Read); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("schema%d: %v", schema, err)
		}
	}
	// Existing Document constructors and distinct create/read State results remain compatible.
	var document DocumentOperations = NewDocumentAdapter(documents.New(nil, documents.Options{}), true)
	if doc, ok := NewRegistry(EnabledTypes{"doc": true}, document.(*DocumentAdapter)).Documents(); !ok || doc != document {
		t.Fatal("Document contract changed")
	}
}

func TestSlidesAdapterNativeLifecycleAndRevocation(t *testing.T) {
	db, store, key, create, live := slidesAdapterFixture(t)
	adapter := NewSlidesAdapter(store, true)
	operations, _ := NewRegistry(EnabledTypes{"slides": true}, nil, WithSlidesAdapter(adapter)).Slides()
	if value, err := operations.Read(t.Context(), key, live); !errors.Is(err, slides.ErrNotFound) || !reflect.DeepEqual(value, slides.State{}) {
		t.Fatalf("absent read %#v %v", value, err)
	}
	if value, err := operations.Versions(t.Context(), key, live); !errors.Is(err, slides.ErrNotFound) || value != nil {
		t.Fatalf("absent versions %#v %v", value, err)
	}
	calls := 0
	auth := func(ctx context.Context, tx *sql.Tx) (string, error) { calls++; return live(ctx, tx) }
	var first slides.Receipt
	first, err := operations.Create(t.Context(), key, create, auth)
	if err != nil || first.Revision != 1 || calls != 2 {
		t.Fatalf("native create %#v calls%d %v", first, calls, err)
	}
	replay, err := operations.Create(t.Context(), key, create, auth)
	if err != nil || !replay.Duplicate || replay.ContentHash != first.ContentHash || calls != 4 {
		t.Fatalf("native replay %#v calls%d %v", replay, calls, err)
	}
	state, err := operations.Read(t.Context(), key, auth)
	if err != nil || state.Revision != 1 || state.Deck.DeckID != key.PageID {
		t.Fatalf("state %#v %v", state, err)
	}
	hash, _ := slides.SlideHash(state.Deck.Slides[0])
	notes := "Changed notes"
	patch := slides.PatchRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 1, MutationID: "patch", Operations: []slides.Operation{{Op: "set_notes", SlideID: state.Deck.Slides[0].ID, IfHash: hash, Notes: &notes}}}
	receipt, err := operations.Apply(t.Context(), key, patch, auth)
	if err != nil || receipt.Revision != 2 {
		t.Fatalf("patch %#v %v", receipt, err)
	}
	restore := slides.RestoreRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 2, IfOrderVersion: receipt.OrderVersion, Revision: 1, MutationID: "restore"}
	receipt, err = operations.Restore(t.Context(), key, restore, auth)
	if err != nil || receipt.Revision != 3 || receipt.ContentHash != first.ContentHash {
		t.Fatalf("restore %#v %v", receipt, err)
	}
	history, err := operations.Versions(t.Context(), key, auth)
	if err != nil || len(history) != 3 || history[0].Kind != "restore" || history[2].Revision != 1 {
		t.Fatalf("versions %#v %v", history, err)
	}
	slidesAdapterExec(t, db, `UPDATE pages SET owner_user_id='successor' WHERE id=?`, key.PageID)
	for _, run := range []func() (slides.Receipt, error){
		func() (slides.Receipt, error) { return operations.Create(t.Context(), key, create, auth) },
		func() (slides.Receipt, error) { return operations.Apply(t.Context(), key, patch, auth) },
		func() (slides.Receipt, error) { return operations.Restore(t.Context(), key, restore, auth) },
	} {
		if value, err := run(); !errors.Is(err, errSlidesAdapterDenied) || value != (slides.Receipt{}) {
			t.Fatalf("revoked receipt %#v %v", value, err)
		}
	}
	if value, err := operations.Read(t.Context(), key, auth); !errors.Is(err, errSlidesAdapterDenied) || !reflect.DeepEqual(value, slides.State{}) {
		t.Fatalf("revoked state %#v %v", value, err)
	}
	if value, err := operations.Versions(t.Context(), key, auth); !errors.Is(err, errSlidesAdapterDenied) || value != nil {
		t.Fatalf("revoked versions %#v %v", value, err)
	}
}

func TestSlidesAdapterFinalAuthorityFailureReturnsZero(t *testing.T) {
	db, store, key, create, live := slidesAdapterFixture(t)
	adapter := NewSlidesAdapter(store, true)
	denyFinal := func() slides.Authorize {
		calls := 0
		return func(ctx context.Context, tx *sql.Tx) (string, error) {
			calls++
			actor, err := live(ctx, tx)
			if err != nil {
				return "", err
			}
			if calls == 2 {
				return "", errSlidesAdapterDenied
			}
			return actor, nil
		}
	}
	if value, err := adapter.Create(t.Context(), key, create, denyFinal()); !errors.Is(err, errSlidesAdapterDenied) || value != (slides.Receipt{}) {
		t.Fatalf("rolled back create %#v %v", value, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM slides_heads`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("create did not rollback %d %v", count, err)
	}
	if _, err := adapter.Create(t.Context(), key, create, live); err != nil {
		t.Fatal(err)
	}
	if value, err := adapter.Read(t.Context(), key, denyFinal()); !errors.Is(err, errSlidesAdapterDenied) || !reflect.DeepEqual(value, slides.State{}) {
		t.Fatalf("populated state leaked %#v %v", value, err)
	}
	if value, err := adapter.Versions(t.Context(), key, denyFinal()); !errors.Is(err, errSlidesAdapterDenied) || value != nil {
		t.Fatalf("populated metadata leaked %#v %v", value, err)
	}
	hash, _ := slides.SlideHash(create.Deck.Slides[0])
	notes := "Rejected candidate"
	patch := slides.PatchRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 1, MutationID: "final-patch", Operations: []slides.Operation{{Op: "set_notes", SlideID: create.Deck.Slides[0].ID, IfHash: hash, Notes: &notes}}}
	if value, err := adapter.Apply(t.Context(), key, patch, denyFinal()); !errors.Is(err, errSlidesAdapterDenied) || value != (slides.Receipt{}) {
		t.Fatalf("partial patch receipt %#v %v", value, err)
	}
	restore := slides.RestoreRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 1, IfOrderVersion: 1, Revision: 1, MutationID: "final-restore"}
	if value, err := adapter.Restore(t.Context(), key, restore, denyFinal()); !errors.Is(err, errSlidesAdapterDenied) || value != (slides.Receipt{}) {
		t.Fatalf("partial restore receipt %#v %v", value, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM slides_receipts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed mutations did not rollback %d %v", count, err)
	}
	if value, err := adapter.Read(t.Context(), key, nil); err == nil || !reflect.DeepEqual(value, slides.State{}) {
		t.Fatal("nil authority accepted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if value, err := adapter.Read(t.Context(), key, live); err == nil || !reflect.DeepEqual(value, slides.State{}) {
		t.Fatal("failed Store returned state")
	}
	if value, err := adapter.Create(t.Context(), key, create, live); err == nil || value != (slides.Receipt{}) {
		t.Fatal("failed Store returned receipt")
	}
	if value, err := adapter.Versions(t.Context(), key, live); err == nil || value != nil {
		t.Fatal("failed Store returned metadata")
	}
}

func TestSlidesAdapterSchemaFencesAndUnsupportedOperations(t *testing.T) {
	db, store, key, create, live := slidesAdapterFixture(t)
	adapter := NewSlidesAdapter(store, true)
	if _, err := adapter.Create(t.Context(), key, create, live); err != nil {
		t.Fatal(err)
	}
	for _, schema := range []int{2, 99} {
		slidesAdapterExec(t, db, `UPDATE slides_heads SET schema_version=? WHERE page_id=?`, schema, key.PageID)
		slidesAdapterExec(t, db, `UPDATE pages SET owner_user_id='successor' WHERE id=?`, key.PageID)
		if value, err := adapter.Versions(t.Context(), key, live); !errors.Is(err, errSlidesAdapterDenied) || value != nil {
			t.Fatalf("schema fence preceded actor authority %#v %v", value, err)
		}
		slidesAdapterExec(t, db, `UPDATE pages SET owner_user_id='owner' WHERE id=?`, key.PageID)
		if value, err := adapter.Create(t.Context(), key, create, live); !errors.Is(err, ErrUnsupported) || value != (slides.Receipt{}) {
			t.Fatalf("head%d replay %#v %v", schema, value, err)
		}
		if value, err := adapter.Read(t.Context(), key, live); !errors.Is(err, ErrUnsupported) || !reflect.DeepEqual(value, slides.State{}) {
			t.Fatalf("head%d state %#v %v", schema, value, err)
		}
		if value, err := adapter.Versions(t.Context(), key, live); !errors.Is(err, ErrUnsupported) || value != nil {
			t.Fatalf("head%d versions %#v %v", schema, value, err)
		}
	}
	slidesAdapterExec(t, db, `UPDATE slides_heads SET schema_version=1 WHERE page_id=?`, key.PageID)
	slidesAdapterExec(t, db, `UPDATE slides_revisions SET schema_version=2 WHERE page_id=? AND rev=1`, key.PageID)
	restore := slides.RestoreRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 1, IfOrderVersion: 1, Revision: 1, MutationID: "restore-target2"}
	if value, err := adapter.Restore(t.Context(), key, restore, live); !errors.Is(err, ErrUnsupported) || value != (slides.Receipt{}) {
		t.Fatalf("target2 restore %#v %v", value, err)
	}
	if value, err := adapter.Versions(t.Context(), key, live); !errors.Is(err, ErrUnsupported) || value != nil {
		t.Fatalf("mixed-schema history %#v %v", value, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM slides_receipts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unsupported restore mutated receipts %d %v", count, err)
	}
	called := false
	forbidden := func(context.Context, *sql.Tx) (string, error) { called = true; return "", errSlidesAdapterDenied }
	bad := create
	bad.SchemaVersion = 2
	if _, err := adapter.Create(t.Context(), key, bad, forbidden); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("request schema %v", err)
	}
	target := 2
	if _, err := adapter.Apply(t.Context(), key, slides.PatchRequest{SchemaVersion: 1, TargetSchemaVersion: &target}, forbidden); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("upgrade request %v", err)
	}
	for _, value := range []*SlidesAdapter{nil, NewSlidesAdapter(nil, true), adapter} {
		if _, err := value.Snapshot(t.Context(), key, UnsupportedSnapshotRequest{}, forbidden); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("snapshot %v", err)
		}
		if _, err := value.Export(t.Context(), key, UnsupportedExportRequest{}, forbidden); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("export %v", err)
		}
	}
	if called {
		t.Fatal("unsupported request entered authority")
	}
	for _, value := range []*SlidesAdapter{nil, NewSlidesAdapter(nil, true), NewSlidesAdapter(store, false)} {
		if state, err := value.Read(t.Context(), key, live); err == nil || !reflect.DeepEqual(state, slides.State{}) {
			t.Fatal("unavailable read returned state")
		}
	}
}
