package testutil

// migrateddb.go provides a *fully-migrated* SQLite database for tests, built
// once per test process and handed out as a private per-test copy.
//
// Why this exists
// ---------------
// The repo carries 155 migrations. Running the whole chain costs a few hundred
// milliseconds; a package with a few hundred DB-backed tests therefore spends
// minutes doing nothing but replaying schema it already replayed. internal/api
// hit that wall first (its Go CI job started brushing the 15-minute cap) and
// grew a local fix inside router_test.go: migrate once into a template file,
// then copy that file per test. A copy is ~1 ms.
//
// That fix lived in exactly one file while 50 other test files still ran the
// migration chain per test. This file is that pattern promoted to normal
// exported code so every package can use it — the helper cannot be a
// `_test.go` file, because Go does not export test-only symbols across
// packages.
//
// What the callers get
// --------------------
//   - identical schema to database.Migrate, because the template *is* built by
//     database.Migrate;
//   - full isolation: every call copies the template to its own file and opens
//     its own connection pool, so tests cannot see each other's rows;
//   - production open semantics: the DB is opened through database.Open, so
//     foreign_keys(ON), WAL, busy_timeout and the connection cap are exactly
//     what the server runs with. Opening test DBs any other way would mean
//     testing against a database that behaves differently from production —
//     a worse outcome than slow tests.
//
// When NOT to use this
// --------------------
//   - Tests of the migration runner itself (internal/database/migrate_*_test.go).
//     They must observe a real, incremental migration run; a pre-migrated
//     template would defeat the point.
//   - Tests that need an *unmigrated* or partially-migrated DB (schema-skew
//     guards, backup-before-migrate paths).
//   - Tests that only need three tables. NewMemDBWithSchema in dbfixture.go is
//     cheaper and states its dependencies explicitly.

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
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

var (
	migratedTemplateOnce sync.Once
	migratedTemplatePath string
	migratedTemplateErr  error
)

// testSynchronous is the one production pragma the test fixture deliberately
// does not inherit.
//
// database.Open defaults to synchronous=FULL, because an accepted piece of work
// has to survive an OS crash and NORMAL in WAL mode does not promise that. FULL
// costs ~21 ms of fsync per acceptance commit on this hardware under the
// daemon's managed-WAL configuration; measured across the test suite that is ~2%
// on a read-heavy package and 3.1x on a write-heavy one (internal/work: 4.3s ->
// 13.4s), and the per-package timeout in CI is 12 minutes. Paying that on every
// fixture buys nothing real: no unit test survives an OS crash either way, and
// T14's power-loss evidence needs a separate harness by design.
//
// What keeps this from becoming a silent divergence is that the production
// setting is asserted directly — see TestOpen_DefaultsToFullSynchronous and
// TestOpen_SynchronousAppliesToEveryPooledConnection in internal/database, plus
// the boot-wiring assertion on the server's own handle. A test that needs real
// FULL semantics opens its own handle with database.Open and says so.
var testSynchronous = database.WithSynchronous(database.SynchronousNormal)

// buildMigratedTemplate migrates one SQLite file and leaves it on disk as the
// template every later call copies.
//
// The template directory is intentionally not registered for cleanup with any
// single test: the template outlives every individual test in the process, so
// there is no test whose lifetime it could be tied to. Go's test binary has no
// "process exit" hook that is safe here either (TestMain belongs to the package
// under test, not to this helper).
//
// So it leaks, and the size is worth stating accurately rather than waving at.
// A migrated template measures 2,879,488 bytes (2.75 MiB). One copy is kept per
// test binary, and 23 packages use this helper, so a full `go test ./...`
// leaves ~63 MiB in os.TempDir() for the OS to reclaim. Before this helper
// existed the same leak was one template (internal/api's), so this is 23x more
// of an existing habit, not a new kind of problem — but it is not "a handful of
// KiB" either, and someone running the suite in a loop will notice.
//
// The migration run itself is no longer per binary: migrateddb_shared.go keys
// one template on the source tree and every test process copies it. What still
// leaks per process is that private copy, kept on purpose so a test that writes
// to MigratedTemplatePath cannot corrupt a sibling process.
func buildMigratedTemplate() {
	// Shared, content-addressed template first (see migrateddb_shared.go):
	// one migration run per source tree instead of one per test binary. Any
	// failure there — no module root, no flock, an unwritable temp dir — falls
	// back to the private per-process build below, which is what this helper
	// always did, so the shared path can only make a run faster, never fail it.
	if path, err := sharedMigratedTemplate(); err == nil {
		migratedTemplatePath = path
		return
	}
	dir, err := os.MkdirTemp("", "crewship-migrated-template-")
	if err != nil {
		migratedTemplateErr = err
		return
	}
	path := filepath.Join(dir, "template.db")
	if err := migrateTemplateAt(path); err != nil {
		migratedTemplateErr = err
		return
	}
	migratedTemplatePath = path
}

// migrateTemplateAt runs the full migration chain into a new SQLite file at
// path and folds its WAL back into the main file, so the result is one
// self-contained file that a plain copy reproduces exactly.
func migrateTemplateAt(path string) error {
	db, err := database.Open("file:"+path, testSynchronous)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
		db.Close()
		return err
	}
	// Fold the WAL back into the main file so a plain file copy carries the
	// complete schema — no -wal/-shm sidecars to copy alongside it.
	//
	// QueryRow, not Exec. `PRAGMA wal_checkpoint` reports failure as a RESULT
	// ROW — (busy, log, checkpointed) — not as an error, and Exec discards
	// rows. A busy checkpoint through Exec therefore returns nil while leaving
	// pages in the -wal that the copy does not carry, and every fixture in the
	// process would silently get a truncated schema. This build is
	// single-threaded so busy is not realistically reachable, and
	// TestMigratedDB_SchemaMatchesMigrateRunner would catch it if it were, but
	// a check that costs one line should not be left to a downstream assertion.
	var busy, walPages, checkpointed int
	if err := db.DB.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").
		Scan(&busy, &walPages, &checkpointed); err != nil {
		db.Close()
		return fmt.Errorf("checkpoint template wal: %w", err)
	}
	if busy != 0 {
		db.Close()
		return fmt.Errorf(
			"checkpoint template wal: busy (log=%d pages, checkpointed=%d) — "+
				"template would be missing schema still held in the -wal",
			walPages, checkpointed)
	}
	if err := db.Close(); err != nil {
		return err
	}
	// Close leaves empty -wal/-shm sidecars behind; the template is the main
	// file alone, so drop them rather than let a copy-by-directory pick them up.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove template %s: %w", suffix, err)
		}
	}
	return nil
}

// MigratedTemplatePath returns the path of the process-wide migrated template
// file, building it on first use. Callers that want their own copy should use
// MigratedDB / MigratedDBAt instead; this is for the rare test that needs the
// raw file (e.g. to hand a pre-migrated DB path to a subprocess or to a
// component that opens the file itself).
//
// The returned file must be treated as read-only. Writing to it corrupts every
// later MigratedDB call in the same process.
func MigratedTemplatePath(t testing.TB) string {
	t.Helper()
	path, err := templatePath()
	if err != nil {
		t.Fatalf("testutil: build migrated template: %v", err)
	}
	return path
}

// MigratedTemplate is MigratedTemplatePath for callers without a testing.TB —
// a sync.Once-built fixture that derives its own images from the template
// (cmd/crewship's doctor and ledger fixtures are the live examples). The same
// read-only rule applies to the returned file.
func MigratedTemplate() (string, error) {
	return templatePath()
}

// MigratedDB returns a fully-migrated SQLite database that belongs to this test
// alone. It is opened with database.Open, so it carries the production pragmas,
// and it is closed (and its files removed) when the test finishes.
//
// Use the *database.DB return value where a caller needs Path() or the wrapper
// type; reach for .DB when a plain *sql.DB is wanted, or call MigratedSQLDB.
func MigratedDB(t testing.TB) *database.DB {
	t.Helper()
	if p, ok := takePrefetched(); ok {
		// Same two cleanups, in the same LIFO order, as the synchronous path
		// below: quiesce the handle first, then remove its directory.
		registerTestDirCleanup(t, p.dir)
		t.Cleanup(func() { quiesce(p.db, p.path) })
		return p.db
	}
	return MigratedDBAt(t, filepath.Join(migratedTestDir(t), "test.db"))
}

// MigratedSQLDB is MigratedDB for the common case where the caller only wants
// the *sql.DB handle.
func MigratedSQLDB(t testing.TB) *sql.DB {
	t.Helper()
	return MigratedDB(t).DB
}

// MigratedDBAt copies the migrated template to path and opens it. Use when the
// test cares where the file lives — a data-dir layout assertion, a backup test
// that inspects the directory, a config that names the DB file.
//
// Prefer MigratedDB when the location is irrelevant: it places the file in a
// directory whose cleanup contract is friendlier to background workers (see
// migratedTestDir).
func MigratedDBAt(t testing.TB, path string) *database.DB {
	t.Helper()
	if _, err := templatePath(); err != nil {
		t.Fatalf("testutil: build migrated template: %v", err)
	}
	db, err := openMigratedCopy(path)
	if err != nil {
		t.Fatalf("testutil: %v", err)
	}
	t.Cleanup(func() { quiesce(db, path) })
	return db
}

// NewMigratedDB is the *testing.T-free core of MigratedDB, for the handful of
// helpers that build a fixture without a test handle (internal/server's
// package-level newTestServer() is the live example — it returns a *Server with
// no t to hang cleanup on). Returns the DB and the teardown func the caller must
// arrange to run; prefer MigratedDB whenever a testing.TB is in scope, since it
// registers teardown for you.
func NewMigratedDB() (*database.DB, func(), error) {
	if _, err := templatePath(); err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "crewship-testdb-")
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "test.db")
	db, err := openMigratedCopy(path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, err
	}
	return db, func() {
		quiesce(db, path)
		_ = os.RemoveAll(dir)
	}, nil
}

func openMigratedCopy(path string) (*database.DB, error) {
	template, err := templatePath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	if err := copyFileForTest(template, path); err != nil {
		return nil, fmt.Errorf("copy migrated template: %w", err)
	}
	db, err := database.Open("file:"+path, testSynchronous)
	if err != nil {
		return nil, fmt.Errorf("open migrated db: %w", err)
	}
	return db, nil
}

func templatePath() (string, error) {
	migratedTemplateOnce.Do(func() {
		if path, ok := inheritedTemplate(); ok {
			migratedTemplatePath = path
			return
		}
		buildMigratedTemplate()
	})
	if migratedTemplateErr != nil {
		return "", migratedTemplateErr
	}
	return migratedTemplatePath, nil
}

// migratedTestDir returns a per-test directory for DB files, deliberately NOT
// t.TempDir().
//
// t.TempDir()'s cleanup contract is "if RemoveAll fails, fail the test". That
// is the wrong contract for DB fixtures, and internal/api learned it the hard
// way: handlers there spawn detached background workers on purpose (a
// context.WithoutCancel goroutine so a terminal run-status write survives the
// request context), so a straggler can re-touch the WAL inside RemoveAll's
// readdir→rmdir window. On 2026-07-20 that failed three unrelated tests with
// "TempDir RemoveAll cleanup: unlinkat …: directory not empty"; each passed
// standalone. Whichever test is unlucky gets blamed, so the signal is not
// merely noisy, it is misattributed.
//
// Most packages using this helper have no detached workers, so for them
// t.TempDir() would be harmless — but "harmless here" is not a property a
// shared helper can rely on: the moment such a package grows an async write
// path, the failure reappears somewhere else and looks unrelated to the change
// that caused it. The helper therefore keeps the safe contract everywhere:
// quiesce properly (see quiesce below), and if something still survives, log
// what it was instead of failing a bystander. The OS reclaims the directory
// regardless.
func migratedTestDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "crewship-testdb-")
	if err != nil {
		t.Fatalf("testutil: create temp dir: %v", err)
	}
	registerTestDirCleanup(t, dir)
	return dir
}

// registerTestDirCleanup is migratedTestDir's cleanup contract, shared with
// the prefetched path so both remove their directory the same way.
func registerTestDirCleanup(t testing.TB, dir string) {
	t.Helper()
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			leftovers, _ := os.ReadDir(dir)
			names := make([]string, 0, len(leftovers))
			for _, e := range leftovers {
				names = append(names, e.Name())
			}
			t.Logf("testutil: test DB temp dir not fully removed (non-fatal): %v; survivors: %v", err, names)
		}
	})
}

// quiesce shuts a test DB down deterministically.
//
// db.Close() prevents any NEW query from starting and waits for in-flight ones,
// so once it returns no straggling goroutine can create a file. We fold the WAL
// back with a TRUNCATE checkpoint first (so close leaves clean, empty sidecars)
// and then best-effort unlink any -wal/-shm that linger, with a short bounded
// retry to cover a worker that grew the WAL during the close.
//
// Without this, two intermittent failures wander between whichever test happens
// to be tearing down while a neighbour's detached worker is still flushing:
// "sql: database is closed" bursts, and RemoveAll's "directory not empty".
func quiesce(db *database.DB, path string) {
	// Best-effort: a checkpoint can return SQLITE_BUSY if a straggler still
	// holds the writer; the explicit unlink below is the backstop.
	_, _ = db.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	_ = db.Close()
	for _, suffix := range []string{"-wal", "-shm"} {
		for attempt := 0; attempt < 20; attempt++ {
			if err := os.Remove(path + suffix); err == nil || os.IsNotExist(err) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func copyFileForTest(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
