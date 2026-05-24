package testutil

import (
	"testing"
)

// TestNewMemDB_OpensWithForeignKeys pins the foreign-keys-ON default.
// A future refactor that drops the pragma would let FK-cascade bugs
// pass silently in tests that consume the helper.
func TestNewMemDB_OpensWithForeignKeys(t *testing.T) {
	t.Parallel()
	db := NewMemDB(t)
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("query pragma: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

// TestNewMemDB_AutoCleanup confirms the t.Cleanup wiring closes the
// db when the test ends. We can't directly observe the cleanup, but
// we can confirm the helper is callable repeatedly without leaking
// (the inner subtest finishes, its cleanup fires).
func TestNewMemDB_AutoCleanup(t *testing.T) {
	t.Parallel()
	for i := 0; i < 5; i++ {
		t.Run("", func(t *testing.T) {
			db := NewMemDB(t)
			if err := db.Ping(); err != nil {
				t.Fatalf("ping: %v", err)
			}
		})
	}
}

// TestNewMemDBWithSchema_LoadsStatements pins the order-preserving
// per-statement contract — schema is loaded one CREATE at a time so
// a failure points at the specific bad SQL.
func TestNewMemDBWithSchema_LoadsStatements(t *testing.T) {
	t.Parallel()
	db := NewMemDBWithSchema(t,
		`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT NOT NULL)`,
		`CREATE TABLE posts (id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id))`,
		`CREATE INDEX idx_posts_user ON posts(user_id)`,
	)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table', 'index') AND name IN ('users', 'posts', 'idx_posts_user')`).Scan(&count); err != nil {
		t.Fatalf("inspect schema: %v", err)
	}
	if count != 3 {
		t.Errorf("schema objects = %d, want 3 (2 tables + 1 index)", count)
	}
}

// TestNewMemDBWithSchema_SkipsBlankStatements documents the
// whitespace-tolerant contract — callers can pass “ or `   ` in
// the variadic slice without it tripping the executor. Useful when
// statements are built from a template + some conditional blocks
// that may render empty.
func TestNewMemDBWithSchema_SkipsBlankStatements(t *testing.T) {
	t.Parallel()
	db := NewMemDBWithSchema(t,
		`CREATE TABLE a (id TEXT PRIMARY KEY)`,
		"",
		"   \n\t",
		`CREATE TABLE b (id TEXT PRIMARY KEY)`,
	)
	for _, table := range []string{"a", "b"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s not present", table)
		}
	}
}

// TestSeedRow_HappyPath confirms a successful INSERT survives the
// helper's Fatal guard (no spurious Fatal call when err is nil).
func TestSeedRow_HappyPath(t *testing.T) {
	t.Parallel()
	db := NewMemDBWithSchema(t,
		`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT NOT NULL)`,
	)
	SeedRow(t, db, `INSERT INTO users (id, email) VALUES (?, ?)`, "u1", "alice@example.com")
	SeedRow(t, db, `INSERT INTO users (id, email) VALUES (?, ?)`, "u2", "bob@example.com")

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("rows = %d, want 2", count)
	}
}

// TestForeignKeyEnforcement is the regression guard the foreign-keys-
// ON pragma exists for. Without it, the INSERT below would succeed
// (orphan post pointing at a non-existent user) and silently mask a
// production bug.
func TestForeignKeyEnforcement(t *testing.T) {
	t.Parallel()
	db := NewMemDBWithSchema(t,
		`CREATE TABLE users (id TEXT PRIMARY KEY)`,
		`CREATE TABLE posts (id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id))`,
	)
	_, err := db.Exec(`INSERT INTO posts (id, user_id) VALUES ('p1', 'nonexistent-user')`)
	if err == nil {
		t.Fatal("orphan INSERT succeeded — foreign_keys pragma not active")
	}
}

// TestMustExec_AliasOfSeedRow pins the alias contract — MustExec is
// just a synonym for callers whose code reads better as "exec this
// DDL" rather than "seed a row".
func TestMustExec_AliasOfSeedRow(t *testing.T) {
	t.Parallel()
	db := NewMemDBWithSchema(t,
		`CREATE TABLE x (id TEXT PRIMARY KEY)`,
	)
	MustExec(t, db, `INSERT INTO x VALUES (?)`, "x1")
	var got string
	if err := db.QueryRow(`SELECT id FROM x WHERE id = ?`, "x1").Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != "x1" {
		t.Errorf("got %q, want x1", got)
	}
}
