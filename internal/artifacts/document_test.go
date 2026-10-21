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

func TestDocumentAdapterLifecycleAndLiveAuthority(t *testing.T) {
	ctx := t.Context()
	db, err := database.Open(filepath.Join(t.TempDir(), "adapter.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = database.Migrate(ctx, db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('owner','owner@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('ws','Workspace','ws')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('page','ws','page','Page','owner','{}')`,
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	store := documents.New(db.DB, documents.Options{})
	adapter := NewDocumentAdapter(store, true)
	key := documents.Key{WorkspaceID: "ws", PageID: "page"}
	denied := errors.New("revoked live permission")
	revoked := false
	checks := 0
	authorize := func(ctx context.Context, tx *sql.Tx) (string, error) {
		checks++
		// A real transactional permission read: this callback must never be cached.
		var owner string
		if err := tx.QueryRowContext(ctx, "SELECT owner_user_id FROM pages WHERE id=? AND workspace_id=?", key.PageID, key.WorkspaceID).Scan(&owner); err != nil {
			return "", err
		}
		if revoked {
			return "", denied
		}
		return "user:" + owner, nil
	}
	create := CreateDocument{ID: "create", Fingerprint: "create-fingerprint", Schema: documents.LatestSchema, Snapshot: []byte("baseline"), Projection: []byte(`[{"id":"one","hash":"h1"}]`)}
	if _, err = NewDocumentAdapter(store, false).Create(ctx, key, create, authorize); !errors.Is(err, ErrDisabled) || checks != 0 {
		t.Fatalf("disabled touched authority: %d %v", checks, err)
	}
	state, err := adapter.Create(ctx, key, create, authorize)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Acquire(ctx, key, "engine", authorize)
	if err != nil {
		t.Fatal(err)
	}
	generation := state.Generation
	snapshot := SnapshotDocument{Lease: lease, ID: "checkpoint", Fingerprint: "snapshot-fingerprint", Snapshot: []byte("baseline"), IfRev: 0, Generation: &generation}
	guardedSnapshot := snapshot
	guardedSnapshot.Guard = func(context.Context, *sql.Tx, string, documents.State) error { return denied }
	if _, err = adapter.Snapshot(ctx, key, guardedSnapshot, authorize); !errors.Is(err, denied) {
		t.Fatalf("snapshot guard discarded: %v", err)
	}
	if _, err = adapter.Snapshot(ctx, key, snapshot, authorize); err != nil {
		t.Fatal(err)
	}
	versions, err := adapter.Versions(ctx, key, authorize)
	if err != nil || len(versions) != 1 || versions[0]["id"] != "checkpoint" {
		t.Fatalf("versions %#v %v", versions, err)
	}
	commit := documents.Commit{Lease: lease, ID: "edit", Fingerprint: "edit-fingerprint", IfRev: 0, IfGeneration: &generation, Schema: documents.LatestSchema, Update: []byte("update"), CandidateSnapshot: []byte("edited"), Projection: create.Projection, Kind: "human"}
	guardedCommit := commit
	guardedCommit.Guard = func(context.Context, *sql.Tx, string, documents.State) error { return denied }
	if _, err = adapter.Apply(ctx, key, guardedCommit, authorize); !errors.Is(err, denied) {
		t.Fatalf("commit guard discarded: %v", err)
	}
	receipt, err := adapter.Apply(ctx, key, commit, authorize)
	if err != nil || receipt.Rev != 1 {
		t.Fatalf("receipt %#v %v", receipt, err)
	}
	restore := RestoreDocument{Lease: lease, Checkpoint: "checkpoint", ID: "restore", Fingerprint: "restore-fingerprint", IfRev: 1, Generation: &generation}
	restored, err := adapter.Restore(ctx, key, restore, authorize)
	if err != nil || restored.Rev != 2 {
		t.Fatalf("restore %#v %v", restored, err)
	}
	if retry, err := adapter.Restore(ctx, key, restore, authorize); err != nil || retry != restored {
		t.Fatalf("retry %#v %v", retry, err)
	}
	changedRestore := restore
	changedRestore.Fingerprint = "different-request"
	if _, err = adapter.Restore(ctx, key, changedRestore, authorize); !errors.Is(err, documents.ErrDuplicate) {
		t.Fatalf("restore fingerprint discarded: %v", err)
	}
	state, err = adapter.Read(ctx, key, authorize)
	if err != nil || state.HeadRev != 2 || string(state.Snapshot) != "baseline" {
		t.Fatalf("state %#v %v", state, err)
	}
	revoked = true
	operations := []struct {
		name string
		run  func() error
	}{
		{"create retry", func() error { _, e := adapter.Create(ctx, key, create, authorize); return e }},
		{"read", func() error { _, e := adapter.Read(ctx, key, authorize); return e }},
		{"apply retry", func() error { _, e := adapter.Apply(ctx, key, commit, authorize); return e }},
		{"snapshot retry", func() error { _, e := adapter.Snapshot(ctx, key, snapshot, authorize); return e }},
		{"versions", func() error { _, e := adapter.Versions(ctx, key, authorize); return e }},
		{"restore retry", func() error { _, e := adapter.Restore(ctx, key, restore, authorize); return e }},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			before := checks
			if err := op.run(); !errors.Is(err, denied) || checks != before+1 {
				t.Fatalf("authority calls %d -> %d error %v", before, checks, err)
			}
		})
	}
	revoked = false
	if _, err = adapter.Read(ctx, key, nil); err == nil {
		t.Fatal("missing Go authority accepted")
	}
	if _, err = db.Exec("UPDATE documents SET schema_version=99 WHERE page_id='page'"); err != nil {
		t.Fatal(err)
	}
	if state, err = adapter.Create(ctx, key, create, authorize); !errors.Is(err, ErrUnsupported) || state.Snapshot != nil || state.Projection != nil {
		t.Fatalf("initialize retry leaked future state: %#v %v", state, err)
	}
	if state, err = adapter.Read(ctx, key, authorize); !errors.Is(err, ErrUnsupported) || state.Snapshot != nil {
		t.Fatalf("unknown schema leaked state: %#v %v", state, err)
	}
}
