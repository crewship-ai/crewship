package database

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The migrated template — why every test no longer migrates from scratch.
//
// Almost every test in this package wants "a fresh database at schema head".
// Until #2551 each one built it by running the whole migration chain, so the
// package's wall time was (number of tests) × (number of migrations) and grew
// with every migration anyone added: 523 s on main, past CI's 12-minute
// -timeout after two more. That is a budget problem, not a test problem — no
// test cared about the chain itself, only about the schema it produces.
//
// Now the chain runs ONCE per test binary, into a template file under a
// per-process directory that TestMain owns. A test that wants a fresh database
// copies that file (openMigratedTestDB / migratedTestDBPath) and pays for a
// copy of a few megabytes instead of a hundred-odd DDL transactions.
//
// Invalidation is by construction: the template lives only as long as the
// test process that built it, so it is always the product of the migrations
// compiled into the binary being run. There is nothing on disk to go stale
// between runs, and nothing to clear when a migration is added or edited.
//
// Tests that need an OLDER schema — applyRegistry up to a version, a raw
// fixture of a legacy table, MigrateSkipping — do not use the template; they
// keep building their own starting point, because the thing under test there
// is the transition, not the destination. Tests that re-run one migration by
// deleting its ledger row and calling Migrate again work fine on a copy: the
// copy is at head, and Migrate replays only the row they removed.

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
	dir, err := os.MkdirTemp("", "crewship-db-template-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "database tests: create template dir: %v\n", err)
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

	db, err := Open("file:" + work)
	if err != nil {
		return "", fmt.Errorf("migrated template: open: %w", err)
	}
	defer db.Close()

	ctx := context.Background()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(ctx, db.DB, silent); err != nil {
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

// migratedTestDBPath returns the path of a fresh, not-yet-opened copy of the
// migrated template inside the test's own temp dir. For tests that open the
// file with their own DSN (a raw sql.Open with foreign keys off, a custom busy
// timeout) — everything else wants openMigratedTestDB.
func migratedTestDBPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "migrated.db")
	if err := copyMigratedTemplate(path); err != nil {
		t.Fatalf("%v", err)
	}
	return path
}

// openMigratedTestDB returns a fresh database at schema head, opened through
// Open with the given options, closed when the test ends. It is what a test
// should reach for instead of Open + Migrate.
func openMigratedTestDB(t *testing.T, opts ...Option) *DB {
	t.Helper()
	return openMigratedTestDBAt(t, filepath.Join(t.TempDir(), "migrated.db"), opts...)
}

// openMigratedTestDBAt is openMigratedTestDB for a test that needs the file
// at a path it chose — one it will snapshot, restore over, or reopen later.
// path must not exist yet.
func openMigratedTestDBAt(t *testing.T, path string, opts ...Option) *DB {
	t.Helper()
	if err := copyMigratedTemplate(path); err != nil {
		t.Fatalf("%v", err)
	}
	db, err := Open("file:"+path, opts...)
	if err != nil {
		t.Fatalf("open migrated copy: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// openMigratedTestSQL is openMigratedTestDB for tests that hold a plain
// *sql.DB.
func openMigratedTestSQL(t *testing.T) *sql.DB {
	t.Helper()
	return openMigratedTestDB(t).DB
}

// TestMigratedTemplate_MatchesAFreshMigration is the proof the rest of the
// package rests on: a copy of the template is indistinguishable, in schema and
// ledger, from a database migrated from scratch in this process. It runs the
// chain once itself, on purpose — that one slow test is what lets the other
// three hundred be fast.
func TestMigratedTemplate_MatchesAFreshMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	fresh, err := Open("file:" + filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("open fresh: %v", err)
	}
	t.Cleanup(func() { fresh.Close() })
	if err := Migrate(ctx, fresh.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate fresh: %v", err)
	}

	copyDB := openMigratedTestDB(t)

	schema := func(db *sql.DB) []string {
		rows, err := db.QueryContext(ctx, `SELECT type, name, tbl_name, COALESCE(sql, '')
			FROM sqlite_master ORDER BY type, name`)
		if err != nil {
			t.Fatalf("read sqlite_master: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var typ, name, tbl, ddl string
			if err := rows.Scan(&typ, &name, &tbl, &ddl); err != nil {
				t.Fatalf("scan sqlite_master: %v", err)
			}
			out = append(out, typ+" "+name+" "+tbl+" "+ddl)
		}
		return out
	}
	want, got := schema(fresh.DB), schema(copyDB.DB)
	if len(want) != len(got) {
		t.Fatalf("template schema has %d objects, fresh migration has %d", len(got), len(want))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("schema object %d differs:\n fresh:    %s\n template: %s", i, want[i], got[i])
		}
	}
	if len(want) == 0 {
		t.Fatal("fresh migration produced no schema objects — the comparison proves nothing")
	}

	ledger := func(db *sql.DB) (n, maxV int) {
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(version), 0) FROM _migrations`).Scan(&n, &maxV); err != nil {
			t.Fatalf("read _migrations: %v", err)
		}
		return n, maxV
	}
	wantN, wantMax := ledger(fresh.DB)
	gotN, gotMax := ledger(copyDB.DB)
	if wantN != gotN || wantMax != gotMax {
		t.Fatalf("ledger differs: fresh %d rows to v%d, template %d rows to v%d", wantN, wantMax, gotN, gotMax)
	}
	if gotMax != maxKnownMigrationVersion() {
		t.Fatalf("template is at v%d, binary knows up to v%d", gotMax, maxKnownMigrationVersion())
	}

	// A copy opened through Open carries Open's pragmas, not whatever the
	// template file was written with.
	var journalMode string
	if err := copyDB.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("copy journal_mode = %q, want wal", journalMode)
	}
	var fk int
	if err := copyDB.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("copy foreign_keys = %d, want 1", fk)
	}

	// Migrate on a copy is a no-op: nothing pending, nothing to collide with.
	if err := Migrate(ctx, copyDB.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("Migrate on a template copy should be a no-op, got: %v", err)
	}
}

// TestMigratedTemplate_CopiesAreIndependent guards the property that makes
// the template safe under t.Parallel and -shuffle: every caller gets its own
// file, the template itself is never opened for writing, and a write in one
// copy is invisible to another.
func TestMigratedTemplate_CopiesAreIndependent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	src, err := migratedTemplatePath()
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	info, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat template: %v", err)
	}
	if info.Mode().Perm() != 0400 {
		t.Errorf("template mode = %o, want 0400 (nothing may open it for writing)", info.Mode().Perm())
	}
	for _, sidecar := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(src + sidecar); err == nil {
			t.Errorf("template has a %s sidecar — a byte copy would not be a complete database", sidecar)
		}
	}

	a := openMigratedTestDB(t)
	b := openMigratedTestDB(t)
	if a.Path() == b.Path() {
		t.Fatalf("two copies share a path: %s", a.Path())
	}
	if _, err := a.ExecContext(ctx, `INSERT INTO workspaces (id, name, slug) VALUES ('ws-a', 'A', 'a')`); err != nil {
		t.Fatalf("insert into copy a: %v", err)
	}
	var n int
	if err := b.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE id = 'ws-a'`).Scan(&n); err != nil {
		t.Fatalf("count in copy b: %v", err)
	}
	if n != 0 {
		t.Fatalf("a row written to copy a is visible in copy b — the copies are not independent")
	}

	// The template did not change under us.
	after, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat template after writes: %v", err)
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		t.Fatalf("template changed while copies were being written to")
	}
}
