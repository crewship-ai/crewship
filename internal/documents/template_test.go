package documents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// Document fixtures need schema head, not repeated migration execution. Reuse
// the database package's production-migrated VACUUM template pattern: one
// process-owned snapshot, then a separate database file for every fixture.
// Fixture users, Pages, Documents, receipts and engine boot identities are
// created only after copying, never placed in the shared template.

var (
	// templateDir is created in TestMain before any test runs and removed
	// after the last one. Empty means TestMain has not run — that cannot
	// happen for a test in this package, but templateBuild checks anyway
	// rather than write into the working directory.
	templateDir string

	templateOnce sync.Once
	templatePath string
	templateErr  error
)

// TestMain owns the per-process template directory. It is a plain temp dir,
// not a t.TempDir, because the template outlives every individual test and is
// shared across parallel ones; t.TempDir would be torn down with whichever
// test happened to build it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "crewship-documents-template-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "document tests: create template dir: %v\n", err)
		os.Exit(1)
	}
	templateDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// migratedTemplatePath returns the read-only template file, building it on
// first use. The build is guarded by sync.Once so parallel tests share one
// chain run and every later caller waits for it rather than racing it. Built
// lazily, not in TestMain, so `-run` on a test that never touches a database
// does not pay for the chain.
//
// The template is produced exactly the way production does it — Open (WAL,
// foreign keys on, synchronous FULL) followed by Migrate — and then VACUUM
// INTO a second file. That second file is the template: a self-contained
// single file with no -wal/-shm sidecars, so a byte copy of it is a complete,
// consistent database. Nothing ever opens the template itself again; it is
// chmod 0400 to make that a filesystem guarantee rather than a convention.
func migratedTemplatePath() (string, error) {
	templateOnce.Do(func() {
		templatePath, templateErr = buildMigratedTemplate()
	})
	return templatePath, templateErr
}

func buildMigratedTemplate() (string, error) {
	if templateDir == "" {
		return "", fmt.Errorf("migrated template: TestMain has not set the template directory")
	}
	work := filepath.Join(templateDir, "work.db")
	out := filepath.Join(templateDir, "migrated.db")

	db, err := database.Open("file:" + work)
	if err != nil {
		return "", fmt.Errorf("migrated template: open: %w", err)
	}
	defer db.Close()

	ctx := context.Background()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(ctx, db.DB, silent); err != nil {
		return "", fmt.Errorf("migrated template: migrate: %w", err)
	}
	// VACUUM INTO writes a consistent snapshot of the migrated database into a
	// fresh file. It refuses to run inside a transaction and wants a single
	// connection, so take one explicitly rather than trust the pool.
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("migrated template: conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "VACUUM INTO ?", out); err != nil {
		return "", fmt.Errorf("migrated template: vacuum into: %w", err)
	}
	if err := os.Chmod(out, 0400); err != nil {
		return "", fmt.Errorf("migrated template: chmod: %w", err)
	}
	return out, nil
}

// copyMigratedTemplate writes a fresh copy of the template to dst, creating
// the parent directory. dst must not exist: a copy over a live database would
// corrupt whatever had it open, and a test that reuses a path is a test with
// a bug. The copy is 0600 like every database Open creates.
func copyMigratedTemplate(dst string) error {
	src, err := migratedTemplatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return fmt.Errorf("copy migrated template: mkdir: %w", err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("copy migrated template: open source: %w", err)
	}
	defer in.Close()
	outF, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("copy migrated template: create %s: %w", dst, err)
	}
	if _, err := io.Copy(outF, in); err != nil {
		outF.Close()
		return fmt.Errorf("copy migrated template: copy: %w", err)
	}
	if err := outF.Close(); err != nil {
		return fmt.Errorf("copy migrated template: close: %w", err)
	}
	return nil
}

// openMigratedTestDB returns a fresh database at schema head, opened through
// Open with the given options, closed when the test ends. It is what a test
// should reach for instead of Open + Migrate.
func openMigratedTestDB(t *testing.T, opts ...database.Option) *database.DB {
	t.Helper()
	return openMigratedTestDBAt(t, filepath.Join(t.TempDir(), "migrated.db"), opts...)
}

// openMigratedTestDBAt is openMigratedTestDB for a test that needs the file
// at a path it chose — one it will snapshot, restore over, or reopen later.
// path must not exist yet.
func openMigratedTestDBAt(t *testing.T, path string, opts ...database.Option) *database.DB {
	t.Helper()
	if err := copyMigratedTemplate(path); err != nil {
		t.Fatalf("%v", err)
	}
	db, err := database.Open("file:"+path, opts...)
	if err != nil {
		t.Fatalf("open migrated copy: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// The fixture must retain the entire current production schema and ledger.
func TestDocumentsFixtureMatchesFreshMigrationAndReopens(t *testing.T) {
	_, db, path := fixture(t, Options{})
	fresh := openTest(t, filepath.Join(t.TempDir(), "fresh.db"))
	t.Cleanup(func() { fresh.Close() })
	if err := database.Migrate(ctx, fresh.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	schema := func(db *sql.DB) []string {
		rows, err := db.QueryContext(ctx, `SELECT type,name,tbl_name,COALESCE(sql,'') FROM sqlite_master ORDER BY type,name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var result []string
		for rows.Next() {
			var kind, name, table, ddl string
			if err := rows.Scan(&kind, &name, &table, &ddl); err != nil {
				t.Fatal(err)
			}
			result = append(result, kind+" "+name+" "+table+" "+ddl)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	want, got := schema(fresh.DB), schema(db.DB)
	if len(want) == 0 || !reflect.DeepEqual(want, got) {
		t.Fatal("fixture schema differs from a fresh full production migration")
	}
	ledger := func(db *sql.DB) []string {
		rows, err := db.QueryContext(ctx, `SELECT CAST(version AS TEXT),name FROM _migrations ORDER BY version`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var result []string
		for rows.Next() {
			var version, name string
			if err := rows.Scan(&version, &name); err != nil {
				t.Fatal(err)
			}
			result = append(result, version+" "+name)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	expected := ledger(fresh.DB)
	if len(expected) == 0 || !reflect.DeepEqual(expected, ledger(db.DB)) {
		t.Fatal("fixture migration ledger differs from the current production chain")
	}
	if err := database.Migrate(ctx, db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if !reflect.DeepEqual(expected, ledger(db.DB)) {
		t.Fatal("second migration changed ledger")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTest(t, path)
	t.Cleanup(func() { reopened.Close() })
	var mode string
	var fk, synchronous int
	if err := reopened.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("reopened journal mode %q: %v", mode, err)
	}
	if err := reopened.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("reopened foreign keys %d: %v", fk, err)
	}
	if err := reopened.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatalf("reopened synchronous %d: %v", synchronous, err)
	}
	state, err := New(reopened.DB, Options{}).Read(ctx, key, allow)
	if err != nil || string(state.Snapshot) != "baseline" || state.HeadRev != 0 {
		t.Fatalf("reopened document state: %+v %v", state, err)
	}
	var receipts int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM document_receipts WHERE workspace_id=? AND page_id=? AND id='__init'`, key.WorkspaceID, key.PageID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("reopened initial receipt %d: %v", receipts, err)
	}
}

func TestDocumentsFixtureIsolatesRowsReceiptsAndWriters(t *testing.T) {
	a, dbA, pathA := fixture(t, Options{})
	b, dbB, pathB := fixture(t, Options{})
	if pathA == pathB {
		t.Fatal("fixtures share a database path")
	}
	leaseA, leaseB := acquire(t, a), acquire(t, b)
	if leaseA.Writer == leaseB.Writer {
		t.Fatal("fixture copied a live writer identity")
	}
	if _, err := dbA.Exec(`UPDATE users SET email='copy-a@example.test' WHERE id='document-owner'`); err != nil {
		t.Fatal(err)
	}
	var email string
	if err := dbB.QueryRow(`SELECT email FROM users WHERE id='document-owner'`).Scan(&email); err != nil || email != "document-owner@example.test" {
		t.Fatalf("user rows not isolated: %q %v", email, err)
	}
	if _, err := a.Commit(ctx, key, commit(leaseA, "fixture-copy-mutation", 0), allow); err != nil {
		t.Fatal(err)
	}
	state, err := b.Read(ctx, key, allow)
	if err != nil || state.Generation != 0 || state.HeadRev != 0 || string(state.Snapshot) != "baseline" {
		t.Fatalf("document mutation leaked: %+v %v", state, err)
	}
	var n int
	if err := dbB.QueryRow(`SELECT COUNT(*) FROM document_receipts WHERE id='fixture-copy-mutation'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("receipt leaked: %d %v", n, err)
	}
}

// Only the migration result may be shared. Every live fixture opens its own
// writable copy and creates its own users, receipts, writers and boot state.
func TestDocumentsTemplateSnapshotIsReadOnlyAndCopiesAreExclusive(t *testing.T) {
	src, err := migratedTemplatePath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0400 {
		t.Fatalf("template mode %o, want0400", info.Mode().Perm())
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(src + suffix); !os.IsNotExist(err) {
			t.Fatalf("template sidecar %s: %v", suffix, err)
		}
	}
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	copied := openMigratedTestDB(t)
	copiedInfo, err := os.Stat(copied.Path())
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(info, copiedInfo) || copiedInfo.Mode().Perm() != 0600 {
		t.Fatal("copy shares template inode or lacks private writable permissions")
	}
	if err := copyMigratedTemplate(copied.Path()); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing database replacement was not refused: %v", err)
	}
	for _, query := range []string{`SELECT COUNT(*) FROM users WHERE id='document-owner'`, `SELECT COUNT(*) FROM workspaces WHERE id='workspace-a'`, `SELECT COUNT(*) FROM documents`, `SELECT COUNT(*) FROM document_receipts`} {
		var n int
		if err := copied.QueryRow(query).Scan(&n); err != nil || n != 0 {
			t.Fatalf("template contains fixture state: count%d %v", n, err)
		}
	}
	var boot string
	var seq int
	if err := copied.QueryRow(`SELECT boot_id,seq FROM document_writer_fence WHERE id=1`).Scan(&boot, &seq); err != nil || boot != "" || seq != 0 {
		t.Fatalf("template contains engine boot/fence state: boot=%q seq=%d %v", boot, seq, err)
	}
	// Initialization writes only the live fixture copy at this storage stage.
	fixture(t, Options{})
	after, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("live fixture writes changed the migrated template")
	}
}
