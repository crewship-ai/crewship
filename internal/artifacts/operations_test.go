package artifacts

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/documents"
)

func TestRegisteredTypedDocumentRetainsAuthorityAndMutationGuard(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(filepath.Join(t.TempDir(), "typed.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = database.Migrate(ctx, db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('owner','typed@example.test')`,
		`INSERT INTO users(id,email) VALUES('successor','typed-successor@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('ws','Workspace','ws')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('page','ws','page','Page','owner','{}')`,
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	store := documents.New(db.DB, documents.Options{})
	operations, ok := NewRegistry(EnabledTypes{"doc": true}, NewDocumentAdapter(store, true)).Documents()
	if !ok {
		t.Fatal("typed operations unavailable")
	}
	key := documents.Key{WorkspaceID: "ws", PageID: "page"}
	denied := errors.New("live authorization denied")
	authorize := func(ctx context.Context, tx *sql.Tx) (string, error) {
		var owner string
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(owner_user_id,'') FROM pages WHERE id=? AND workspace_id=?", key.PageID, key.WorkspaceID).Scan(&owner); err != nil {
			return "", err
		}
		if owner != "owner" {
			return "", denied
		}
		return "user:" + owner, nil
	}
	create := CreateDocument{ID: "create", Fingerprint: "original-create", Schema: 3, Snapshot: []byte("initial"), Projection: []byte(`[]`)}
	state, err := operations.Create(ctx, key, create, authorize)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Acquire(ctx, key, "engine", authorize)
	if err != nil {
		t.Fatal(err)
	}
	generation := state.Generation
	guardDenied := errors.New("original mutation scope denied")
	request := documents.Commit{Lease: lease, ID: "patch", Fingerprint: "original-patch", IfRev: 0, IfGeneration: &generation, Schema: 3, Update: []byte("update"), CandidateSnapshot: []byte("changed"), Projection: []byte(`[]`), Kind: "human", Guard: func(context.Context, *sql.Tx, string, documents.State) error { return guardDenied }}
	if _, err = operations.Apply(ctx, key, request, authorize); !errors.Is(err, guardDenied) {
		t.Fatalf("typed interface lost guard: %v", err)
	}
	request.Guard = nil
	receipt, err := operations.Apply(ctx, key, request, authorize)
	if err != nil || receipt.Rev != 1 {
		t.Fatalf("apply %#v %v", receipt, err)
	}
	if retry, err := operations.Apply(ctx, key, request, authorize); err != nil || retry != receipt {
		t.Fatalf("exact retry %#v %v", retry, err)
	}
	if _, err = db.Exec(`UPDATE pages SET owner_user_id='successor' WHERE id='page'`); err != nil {
		t.Fatal(err)
	}
	if _, err = operations.Apply(ctx, key, request, authorize); !errors.Is(err, denied) {
		t.Fatalf("retry bypassed live SQL authority: %v", err)
	}
	if _, err = operations.Read(ctx, key, authorize); !errors.Is(err, denied) {
		t.Fatalf("read bypassed live SQL authority: %v", err)
	}
}

func TestDocumentExportExplicitlyUnsupportedWithoutAuthorityOrBytes(t *testing.T) {
	called := false
	authorize := func(context.Context, *sql.Tx) (string, error) { called = true; return "", errors.New("must not run") }
	for _, adapter := range []*DocumentAdapter{nil, NewDocumentAdapter(nil, false)} {
		var operations DocumentOperations = adapter
		if _, err := operations.Export(t.Context(), documents.Key{}, UnsupportedExportRequest{}, authorize); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("export did not fail closed: %v", err)
		}
	}
	if called {
		t.Fatal("unsupported export entered transaction authority")
	}
}
