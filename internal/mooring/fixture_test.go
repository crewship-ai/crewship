package mooring

import (
	"crypto/sha256"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Reuse only the fully migrated immutable image, never its connections. The
// ordinary MigratedDB helper intentionally chooses synchronous=NORMAL, which
// is unsuitable for these durability/fencing tests. Each private copy is opened
// through production defaults so FULL applies to every pooled connection.
func openFullMooringFixture(t *testing.T, path string) *database.DB {
	t.Helper()
	path = testutil.ActualEvidenceDBPath(t, path)
	source, err := os.Open(testutil.MigratedTemplatePath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatal(err)
	}
	if err = destination.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMooringFixtureIsolationAndEveryConnectionFull(t *testing.T) {
	template := testutil.MigratedTemplatePath(t)
	before, err := os.ReadFile(template)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := sha256.Sum256(before)
	left := openFullMooringFixture(t, filepath.Join(t.TempDir(), "left.db"))
	right := openFullMooringFixture(t, filepath.Join(t.TempDir(), "right.db"))
	if _, err = left.Exec(`INSERT INTO users(id,email) VALUES('fixture-isolation','fixture@example.test')`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = right.QueryRow(`SELECT count(*) FROM users WHERE id='fixture-isolation'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("shared fixture state", count, err)
	}
	// Hold several connections simultaneously so each assertion exercises a new
	// pooled connection, rather than repeatedly querying a single FULL handle.
	connections := []*sql.Conn{}
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for range 3 {
		conn, err := left.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
		var synchronous, foreignKeys int
		var journal string
		if err = conn.QueryRowContext(t.Context(), `PRAGMA synchronous`).Scan(&synchronous); err != nil || synchronous != 2 {
			t.Fatal("FULL durability lost", synchronous, err)
		}
		if err = conn.QueryRowContext(t.Context(), `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
			t.Fatal("foreign keys lost", foreignKeys, err)
		}
		if err = conn.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&journal); err != nil || journal != "wal" {
			t.Fatal("production WAL lost", journal, err)
		}
	}
	// A file-backed schema at head includes both owner and provenance contracts.
	for _, table := range []string{"mooring_session_owners", "mooring_collab_target_effects"} {
		if err = right.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatal("incomplete migrated fixture", table, count, err)
		}
	}
	after, err := os.ReadFile(template)
	if err != nil || sha256.Sum256(after) != beforeHash {
		t.Fatal("test mutated shared immutable template", err)
	}
}
