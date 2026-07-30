package testutil

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// TestMigratedDB_SchemaMatchesMigrateRunner is the load-bearing test for this
// helper: a copied template must be indistinguishable from a DB the migration
// runner just built. If it ever diverges, every package that swapped
// database.Migrate for MigratedDB is silently testing a different schema.
func TestMigratedDB_SchemaMatchesMigrateRunner(t *testing.T) {
	fromHelper := objectNames(t, MigratedSQLDB(t))

	dir, err := os.MkdirTemp("", "migrate-reference-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ref, err := database.Open("file:" + filepath.Join(dir, "ref.db"))
	if err != nil {
		t.Fatalf("open reference db: %v", err)
	}
	t.Cleanup(func() { _ = ref.Close() })
	if err := database.Migrate(context.Background(), ref.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate reference db: %v", err)
	}
	fromRunner := objectNames(t, ref.DB)

	if len(fromHelper) == 0 {
		t.Fatal("helper DB has no schema objects")
	}
	if len(fromHelper) != len(fromRunner) {
		t.Fatalf("schema object count differs: helper=%d runner=%d", len(fromHelper), len(fromRunner))
	}
	for i := range fromHelper {
		if fromHelper[i] != fromRunner[i] {
			t.Fatalf("schema object %d differs:\nhelper: %s\nrunner: %s", i, fromHelper[i], fromRunner[i])
		}
	}
}

// objectNames returns every table/index/view/trigger as "type|name|sql",
// ordered, so two schemas can be compared element-wise.
func objectNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT type, name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var typ, name, ddl string
		if err := rows.Scan(&typ, &name, &ddl); err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		out = append(out, typ+"|"+name+"|"+ddl)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	return out
}

// TestMigratedDB_MigrationsRecorded checks the _migrations ledger came across
// with the file. A copy that lost it would make a later database.Migrate call
// (some tests run one on top of the fixture) replay everything.
func TestMigratedDB_MigrationsRecorded(t *testing.T) {
	db := MigratedSQLDB(t)
	var count, maxVersion int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(version), 0) FROM _migrations`).Scan(&count, &maxVersion); err != nil {
		t.Fatalf("read _migrations: %v", err)
	}
	if count < 100 {
		t.Fatalf("expected the full migration ledger, got %d rows", count)
	}
	if maxVersion < 100 {
		t.Fatalf("expected a high max migration version, got %d", maxVersion)
	}
}

// TestMigratedDB_ProductionPragmas pins the reason the helper goes through
// database.Open rather than sql.Open: a fixture that runs with foreign keys OFF
// or in rollback-journal mode is a fixture that hides real bugs.
func TestMigratedDB_ProductionPragmas(t *testing.T) {
	db := MigratedSQLDB(t)
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1", fk)
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

// TestMigratedDB_IsolatedPerCall is the correctness guarantee that makes the
// template safe: sharing one migrated file would be faster still and completely
// wrong, so prove each caller gets its own database.
func TestMigratedDB_IsolatedPerCall(t *testing.T) {
	a := MigratedDB(t)
	b := MigratedDB(t)
	if a.Path() == b.Path() {
		t.Fatalf("both calls got the same file: %s", a.Path())
	}
	if _, err := a.DB.Exec(`INSERT INTO users (id, email, full_name) VALUES ('iso-1', 'iso@example.com', 'Iso')`); err != nil {
		t.Fatalf("insert into a: %v", err)
	}
	var n int
	if err := b.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE id = 'iso-1'`).Scan(&n); err != nil {
		t.Fatalf("count in b: %v", err)
	}
	if n != 0 {
		t.Fatalf("row written to a is visible in b (n=%d) — the fixtures are not isolated", n)
	}
}

// TestMigratedDB_TemplateNotMutated guards the read-only contract on the
// template: a test that writes to its copy must not affect later copies.
func TestMigratedDB_TemplateNotMutated(t *testing.T) {
	first := MigratedDB(t)
	if _, err := first.DB.Exec(`INSERT INTO users (id, email, full_name) VALUES ('tpl-1', 'tpl@example.com', 'Tpl')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	second := MigratedSQLDB(t)
	var n int
	if err := second.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("fresh fixture already has %d users — the template was mutated", n)
	}
}

// TestMigratedDBAt_UsesRequestedPath covers the variant callers reach for when
// the file location is part of what they assert.
func TestMigratedDBAt_UsesRequestedPath(t *testing.T) {
	want := filepath.Join(t.TempDir(), "nested", "chosen.db")
	db := MigratedDBAt(t, want)
	if db.Path() != want {
		t.Fatalf("Path() = %q, want %q", db.Path(), want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("stat %s: %v", want, err)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&n); err != nil {
		t.Fatalf("read _migrations: %v", err)
	}
	if n < 100 {
		t.Fatalf("expected a migrated DB at the requested path, got %d migrations", n)
	}
}
