//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

// migratedImages is one fully-migrated database, captured in the two on-disk
// states the tests below stage it in.
type migratedImages struct {
	// live/liveWAL are the main file and its "-wal" copied out from under an
	// OPEN writer: a WAL with the migration frames still in it and no "-shm",
	// which is what a killed crewshipd leaves behind (and what `cp` of a live
	// database produces).
	live, liveWAL []byte
	// closed is the same database after Close checkpointed the WAL into it and
	// unlinked both sidecars — a cleanly stopped crewshipd. journal_mode=WAL
	// persists in the header either way.
	closed []byte
	err    error
}

// migratedFixture runs the migrations ONCE per test binary.
//
// Five fixtures in this file used to call database.Migrate themselves. That is
// ~1 s each without -race and ~30 s each WITH it — ~150 s of the Go Race job's
// 25-minute budget spent rebuilding the same database, since the migration set
// is compile-time constant and so are the bytes it produces. Copying the file
// in reproduces each state exactly: the crash pair is captured the same way
// crashedDatabase captured it (from under a live writer), and every test that
// needs a live writer still opens one, it just does not migrate again.
var migratedFixture = sync.OnceValue(buildMigratedFixture)

func buildMigratedFixture() migratedImages {
	var m migratedImages
	failed := func(format string, a ...any) migratedImages {
		m.err = fmt.Errorf(format, a...)
		return m
	}

	dir, err := os.MkdirTemp("", "crewship-doctor-fixture-")
	if err != nil {
		return failed("temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "crewship.db")
	seed, err := database.Open("file:" + path)
	if err != nil {
		return failed("seed open: %w", err)
	}
	if err := database.Migrate(context.Background(), seed.DB, covLogger()); err != nil {
		seed.Close()
		return failed("seed migrate: %w", err)
	}
	// Read before the close, or the WAL is already gone.
	if m.live, err = os.ReadFile(path); err != nil {
		seed.Close()
		return failed("read live image: %w", err)
	}
	if m.liveWAL, err = os.ReadFile(path + "-wal"); err != nil {
		seed.Close()
		return failed("read live -wal: %w", err)
	}
	if err := seed.Close(); err != nil {
		return failed("seed close: %w", err)
	}
	if m.closed, err = os.ReadFile(path); err != nil {
		return failed("read closed image: %w", err)
	}
	return m
}

// installMigratedDB writes a fully-migrated database into dd, with no sidecars
// — the state a cleanly stopped crewshipd leaves.
func installMigratedDB(t *testing.T, dd *database.DataDir) {
	t.Helper()
	m := migratedFixture()
	if m.err != nil {
		t.Fatalf("build migrated fixture: %v", m.err)
	}
	if err := os.WriteFile(dd.DatabasePath(), m.closed, 0o600); err != nil {
		t.Fatalf("install database: %v", err)
	}
}

// holdDatabaseOpen opens a writer on the installed database and keeps it open
// for the rest of the test — this is crewshipd, and its connection is what
// keeps the "-shm" WAL index on disk.
//
// The query is not decoration: sql.Open is lazy, so a handle that has executed
// nothing has not opened a connection and no -shm exists yet.
func holdDatabaseOpen(t *testing.T, dd *database.DataDir) {
	t.Helper()
	db, err := database.Open(dd.DatabaseURL())
	if err != nil {
		t.Fatalf("hold database open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&n); err != nil {
		t.Fatalf("holder query: %v", err)
	}
	if n == 0 {
		t.Fatal("_migrations is empty — the installed fixture is not a migrated database")
	}
}

// `crewship doctor` must not create the thing it is reporting on.
//
// B-02: run against an empty data dir, doctor printed
//
//	[WARN] db migration version   database file does not exist (crewshipd has never run)
//
// and then left a fully migrated 3 MB crewship.db behind. The WARN comes from
// checkDBMigrationVersion, which stats and creates nothing — correct. The file
// came from the telemetry probes further down the list: all three route through
// openLocalDB, which calls database.Open (creates the file) and then, on a
// fresh install (from == 0), Migrate. So the operator was told the database did
// not exist by one row while another row was busy manufacturing it, and the
// next `crewship start` inherited a database it never provisioned — schema
// applied with no pre-migrate snapshot and no ENCRYPTION_KEY bootstrap.
//
// openLocalDB keeps that create-and-init behaviour for `crewship telemetry
// on`/`off`, which legitimately need somewhere to write consent before the
// first start. The diagnostic path must not get it.
//
// This test pins the invariant at the level that actually matters — the
// filesystem — rather than the wording of any single row: after every
// database-touching doctor probe has run against an empty data dir, there is
// still no database file.
func TestDoctorProbes_DoNotCreateDatabase(t *testing.T) {
	dd := tempDataDir(t)
	t.Setenv("CREWSHIP_SENTRY_DSN", "")
	ctx := context.Background()

	// Every doctor probe that reaches the local database, in the order
	// cmd_doctor.go registers them.
	checkDBMigrationVersion(ctx)
	runCheckTelemetryStatus(ctx)
	runCheckSentryDSNWiring(ctx)
	runCheckDsnReachability(ctx)

	// The WAL/SHM sidecars are listed explicitly: an open that creates only
	// those still means the probe opened for writing, and they would confuse
	// the next `crewship start` just as much as a stray main file.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := dd.DatabasePath() + suffix
		if _, err := os.Stat(path); err == nil {
			t.Errorf("doctor created %s — a diagnostic command must not provision the database", path)
		} else if !os.IsNotExist(err) {
			t.Errorf("stat %s: %v", path, err)
		}
	}

	// Belt and braces: nothing else database-shaped appeared either. Catches a
	// future probe that opens a differently-named file in the same data dir.
	entries, err := os.ReadDir(dd.Root)
	if err != nil {
		t.Fatalf("read data dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".db") {
			t.Errorf("doctor left %s in the data dir", filepath.Join(dd.Root, e.Name()))
		}
	}
}

// emptyDataDir points CREWSHIP_DATA_DIR at a directory that exists and is
// EMPTY, and returns its root.
//
// Deliberately not tempDataDir: that helper resolves through
// database.DefaultDataDir, which provisions output/chats/logs/skills as a side
// effect — i.e. it pre-creates exactly the tree the test below is about, so
// every assertion would pass no matter what doctor does.
func emptyDataDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CREWSHIP_DATA_DIR", root)
	return root
}

// Same invariant as the test above, one layer down: doctor must not provision
// the data-dir TREE either, not just the database file.
//
// The first B-02 fix moved the telemetry probes off openLocalDB, but
// openLocalDBReadOnly still resolved its paths with database.DefaultDataDir —
// a mutating call (DefaultDataDir → NewDataDir → MkdirAll on the root plus
// output, chats, logs and skills). So `crewship doctor` on a box that had never
// run crewshipd printed "database file does not exist (crewshipd has never
// run)" and left ~/.crewship/{output,chats,logs,skills} behind at 0755, the
// same class of side effect B-02 was filed for and a direct contradiction of
// openLocalDBReadOnly's own contract. The original regression test only
// asserted crewship.db was absent, which is why it did not catch this.
//
// Every doctor probe that resolves the data dir is exercised, not just the
// database ones: a diagnostic that reports on a directory has no business
// creating anything inside it, so the assertion is "the directory is still
// empty", full stop.
func TestDoctorProbes_DoNotProvisionTheDataDirTree(t *testing.T) {
	root := emptyDataDir(t)
	t.Setenv("CREWSHIP_SENTRY_DSN", "")
	t.Setenv("NEXTAUTH_SECRET", "")
	ctx := context.Background()

	checkDataDir(false)
	checkDataDirWritable()
	runCheckDataDirPerms()
	checkNextAuthSecret()
	checkDBMigrationVersion(ctx)
	runCheckTelemetryStatus(ctx)
	runCheckSentryDSNWiring(ctx)
	runCheckDsnReachability(ctx)

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read data dir: %v", err)
	}
	if len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("doctor created %v in %s — a diagnostic must not provision the data dir it reports on", names, root)
	}
}

// checkDBMigrationVersion must behave like its three sibling database probes
// when crewshipd is live and holding the database.
//
// It used to open its own handle whose DSN omitted the
// _pragma=busy_timeout(5000) the telemetry probes get from
// openLocalDBReadOnly, and sql.Open is lazy with no Ping. A SQLITE_BUSY from a
// concurrent checkpoint therefore surfaced at row.Scan and was rendered as
//
//	[WARN] db migration version   could not read _migrations: database is locked (5) (SQLITE_BUSY)
//	                              → crewshipd may not have run against this DB yet
//
// telling an operator whose server is demonstrably running that their database
// has never been migrated — the single most misleading thing doctor can say
// about a healthy install, and the one that invites a destructive "fix".
//
// The lock here is held by one connection in locking_mode=EXCLUSIVE, which is
// the cheapest deterministic stand-in for the checkpoint window: in WAL mode an
// ordinary writer does not block readers at all, an exclusive-mode connection
// does, and it does so for as long as we keep it open. Held briefly and then
// released, exactly like the real thing — a probe with the busy timeout waits
// it out and reads the version; one without fails instantly.
//
// The hold is 300ms, not a substantial slice of the 5s busy timeout: what is
// under test is that the probe WAITS rather than answering "never migrated" on
// the first SQLITE_BUSY, and a probe whose DSN drops busy_timeout(5000) fails
// on that lock in single-digit milliseconds. Waiting longer proves nothing
// extra and costs the wall-clock twice over under -race. The `releasing`
// channel is what keeps the shorter hold honest: if the probe ever returns
// while the lock is still held, the database was not actually locked and the
// test would otherwise pass vacuously.
func TestCheckDBMigrationVersion_WaitsOutABusyDatabase(t *testing.T) {
	dd := tempDataDir(t)
	ctx := context.Background()

	// No writer of our own is left open: locking_mode=EXCLUSIVE cannot be
	// acquired while another connection still has the WAL index open.
	installMigratedDB(t, dd)

	locker, err := sql.Open("sqlite", dd.DatabaseURL()+"?_pragma=locking_mode(EXCLUSIVE)&_pragma=busy_timeout(30000)&_txlock=immediate")
	if err != nil {
		t.Fatalf("open locker: %v", err)
	}
	locker.SetMaxOpenConns(1)
	defer locker.Close()
	// A write is what actually takes the file lock; the header write is the
	// smallest one that does not disturb the schema the probe reads.
	if _, err := locker.ExecContext(ctx, "PRAGMA user_version = 7"); err != nil {
		t.Fatalf("take exclusive lock: %v", err)
	}

	// Released well inside openLocalDBReadOnly's 5s busy timeout, so the fixed
	// probe waits and answers; started immediately before the call so the lock
	// is certainly still held when the probe opens. `releasing` closes just
	// BEFORE the Close, so a probe that returns before it closed can only have
	// done so against a database that was still locked.
	releasing := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		close(releasing)
		_ = locker.Close()
	}()
	r := checkDBMigrationVersion(ctx)
	select {
	case <-releasing:
	default:
		t.Fatalf("the probe returned while the database was still locked, so it never waited for anything: %+v", r)
	}

	if strings.Contains(r.hint, "may not have run") || strings.Contains(r.detail, "could not read _migrations") {
		t.Errorf("a busy database was reported as never migrated: %+v", r)
	}
	latest := database.MaxKnownMigrationVersion()
	if r.status != "PASS" || !strings.Contains(r.detail, fmt.Sprintf("v%d (latest)", latest)) {
		t.Errorf("migrated database held busy: got %+v, want PASS v%d (latest)", r, latest)
	}
}

// The no-database case must read as "not configured yet", not as an internal
// error and not as a fabricated answer. checkDBMigrationVersion already WARNs
// "database file does not exist"; the telemetry rows have to agree with it.
//
// Absence of the database is positive evidence for what these rows report:
// consent lives in app_settings, so no file means consent was never recorded —
// "not yet configured" is exactly true, and reachability is correctly skipped
// because telemetry cannot be enabled. No open, no create, same answer.
func TestDoctorTelemetryProbes_MissingDatabaseIsHonest(t *testing.T) {
	tempDataDir(t)
	t.Setenv("CREWSHIP_SENTRY_DSN", "")
	ctx := context.Background()

	if r := runCheckTelemetryStatus(ctx); r.status != "WARN" || !strings.Contains(r.detail, "not yet configured") {
		t.Errorf("telemetry status on a missing DB: got %+v", r)
	}
	if r := runCheckSentryDSNWiring(ctx); r.status != "INFO" || !strings.Contains(r.detail, "not yet configured") {
		t.Errorf("dsn wiring on a missing DB: got %+v", r)
	}
	if r := runCheckDsnReachability(ctx); r.status != "INFO" || !strings.Contains(r.detail, "skipped") {
		t.Errorf("dsn reachability on a missing DB: got %+v", r)
	}
}

// The read-only route must still work on a real database — otherwise the fix
// would trade a phantom database for three probes that permanently report
// "could not open".
//
// A writer connection is deliberately held OPEN for the duration: that is the
// case operators actually hit, doctor run while crewshipd holds the database,
// and it is the one where a read-only open can go wrong. WAL needs the -shm
// segment, and a connection that is read-only by URI still has to be able to
// join it. With no writer open there would be no -shm to join, which is a
// different case (and the one two tests below are about).
func TestOpenLocalDBReadOnly_ReadsAnExistingDatabase(t *testing.T) {
	dd := tempDataDir(t)
	ctx := context.Background()

	installMigratedDB(t, dd)
	holdDatabaseOpen(t, dd)

	db, err := openLocalDBReadOnly(ctx)
	if err != nil {
		t.Fatalf("openLocalDBReadOnly on an existing database: %v", err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM _migrations").Scan(&n); err != nil {
		t.Fatalf("read _migrations through the read-only handle: %v", err)
	}
	if n == 0 {
		t.Error("_migrations is empty — the seed did not migrate")
	}

	// Read-only means read-only: a write must be refused by SQLite rather
	// than silently accepted on a handle doctor holds.
	if _, err := db.ExecContext(ctx, "CREATE TABLE doctor_should_not_write (x INTEGER)"); err == nil {
		t.Error("read-only handle accepted a write")
	}
}

// crashedDatabase stages the state a killed crewshipd leaves behind: a WAL
// database with a stale "-wal" and NO "-shm", and returns the data dir holding
// it (already installed as CREWSHIP_DATA_DIR).
//
// The pair is copied from under a live writer (see migratedFixture), which is
// what produces it honestly: a database closed first would have checkpointed
// the WAL into the main file and unlinked both sidecars, i.e. staged the one
// state the tests below are not about. It also covers the other route to the
// same place — a backup taken with `cp` that picked up the -wal and not the
// -shm.
func crashedDatabase(t *testing.T) *database.DataDir {
	t.Helper()
	m := migratedFixture()
	if m.err != nil {
		t.Fatalf("build migrated fixture: %v", m.err)
	}

	dst := t.TempDir()
	for suffix, image := range map[string][]byte{"": m.live, "-wal": m.liveWAL} {
		if err := os.WriteFile(filepath.Join(dst, "crewship.db"+suffix), image, 0o600); err != nil {
			t.Fatalf("stage crewship.db%s: %v", suffix, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "crewship.db-shm")); !os.IsNotExist(err) {
		t.Fatalf("staged copy has a -shm; the crash state was not reproduced (%v)", err)
	}
	t.Setenv("CREWSHIP_DATA_DIR", dst)
	return &database.DataDir{Root: dst}
}

// unwritableDir drops the write bit on a directory for the rest of the test,
// restoring it first thing afterwards — t.TempDir's own cleanup cannot remove
// a directory it may not write to.
func unwritableDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// The post-crash state is the one an operator runs `doctor` IN, so the
// diagnostic route has to survive it.
//
// A WAL database cannot be read at all — read-only included — without the
// "-shm" WAL index, and a crashed crewshipd leaves a "-wal" with no "-shm". A
// read-only connection is then allowed to rebuild the index only if it can
// create the file, which needs write permission on the DIRECTORY. Here it has
// it, so the open must succeed and doctor must report the migration version
// instead of "could not open DB".
//
// The -shm assertion is the point of the test, not incidental: building it is
// the only reason this case works, so a future "diagnostics create nothing at
// all" tightening that blocked it would silently make doctor useless in
// exactly the state it exists for. It is a rebuildable index over an existing
// database — no schema, no data, recreated by the next crewshipd start — which
// is why it does not offend the B-02 invariant that the tests above pin.
func TestOpenLocalDBReadOnly_StaleWALWithoutIndex(t *testing.T) {
	dd := crashedDatabase(t)
	ctx := context.Background()

	db, err := openLocalDBReadOnly(ctx)
	if err != nil {
		t.Fatalf("read-only open of a crashed WAL database in a writable dir: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM _migrations").Scan(&n); err != nil {
		t.Fatalf("read _migrations after a crash: %v", err)
	}
	if n == 0 {
		t.Error("_migrations is empty — the staged database did not carry the seed's migrations")
	}
	if _, err := os.Stat(dd.DatabasePath() + "-shm"); err != nil {
		t.Errorf("stat -shm after a successful read-only open: %v (the WAL index is what makes this case work)", err)
	}

	// The probe that owns the row must agree, since that is what the operator
	// actually sees.
	if r := checkDBMigrationVersion(ctx); r.status != "PASS" {
		t.Errorf("db migration version after a crash: got %+v, want PASS", r)
	}
}

// The other half of the same rule: with no -shm AND no way to create one, the
// read-only open cannot succeed — SQLite has nowhere to put the WAL index.
//
// That is a real regression against the pre-mode=ro behaviour, where the probe
// opened read-write and this never came up, so it is pinned rather than left to
// be rediscovered. What is testable is that the failure is legible: SQLite says
// only "unable to open database file (14)" (stale -wal) or "attempt to write a
// readonly database (1544)" (no -wal — the database is still WAL-mode, so the
// index is still required), neither of which mentions the directory that is
// actually at fault. Both states are exercised because they produce different
// SQLite errors and must produce the same explanation.
func TestOpenLocalDBReadOnly_UnwritableDirWithoutWALIndex(t *testing.T) {
	// SKIP-WAIVER(#1929): the condition under test IS a directory SQLite may
	// not write, and mode 0500 does not deny root. As euid 0 the read-only open
	// succeeds, no -shm failure ever occurs, and every assertion below inverts.
	// There is no seam to drive instead: openLocalDBReadOnly takes no
	// writability answer as input, it discovers one by attempting the open. The
	// decision walIndexUnbuildable makes on its own IS reachable without mode
	// bits and is covered as root too — see TestWALIndexUnbuildable below.
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}
	ctx := context.Background()

	cases := []struct {
		name  string
		setup func(t *testing.T) *database.DataDir
	}{
		{"stale wal, no shm", crashedDatabase},
		{"no sidecars at all", func(t *testing.T) *database.DataDir {
			dd := tempDataDir(t)
			// The image installed here is the post-Close one: the WAL was
			// checkpointed into the main file and both sidecars unlinked.
			// journal_mode=WAL persists in the header regardless, which is why
			// the index is still needed to read it.
			installMigratedDB(t, dd)
			return dd
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dd := tc.setup(t)
			unwritableDir(t, dd.Root)

			db, err := openLocalDBReadOnly(ctx)
			if err == nil {
				db.Close()
				t.Fatal("read-only open succeeded with no WAL index and an unwritable directory")
			}
			// Not errNoLocalDB: the database is right there. Confusing the two
			// would have doctor say "crewshipd has never run" about a
			// populated database, the same class of lie as B-02.
			if errors.Is(err, errNoLocalDB) {
				t.Errorf("an unreadable database was reported as absent: %v", err)
			}
			for _, want := range []string{"-shm", "not writable", dd.Root} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not mention %q, so the operator cannot act on it: %v", want, err)
				}
			}

			// And the row the operator reads carries the same explanation.
			r := checkDBMigrationVersion(ctx)
			if r.status != "WARN" || !strings.Contains(r.detail, "not writable") {
				t.Errorf("db migration version with an unbuildable WAL index: got %+v", r)
			}
		})
	}
}

// The rescue clause, and the case that must NOT be annotated: where the -shm
// already exists, a read-only open works no matter what the directory's mode
// is, because we only join an index we are not creating.
//
// This is the live-crewshipd case with a hardened data dir, and it is why the
// fix for the test above is "make the directory writable OR run this while the
// server is up" rather than a single instruction. It also pins the order
// walIndexUnbuildable asks its two questions in: -shm first, so that a failure
// with an index present is never mis-blamed on directory permissions.
func TestOpenLocalDBReadOnly_UnwritableDirWithExistingWALIndex(t *testing.T) {
	// SKIP-WAIVER(#1929): this is the rescue clause, so the unwritable
	// directory is the entire premise — the open must succeed *because* the
	// -shm already exists, not because the process could have created one. Root
	// writes through mode 0500, so as euid 0 the test would still pass while
	// proving the opposite of what it claims. A green vacuous pass here is
	// worse than an honest skip, because the row it guards is the advice
	// operators are given for the failure above.
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}
	dd := tempDataDir(t)
	ctx := context.Background()

	// The writer is held open for the duration: this is crewshipd, and its
	// connection is what keeps the -shm on disk.
	installMigratedDB(t, dd)
	holdDatabaseOpen(t, dd)
	if _, err := os.Stat(dd.DatabasePath() + "-shm"); err != nil {
		t.Fatalf("stat -shm while the writer is open: %v", err)
	}
	unwritableDir(t, dd.Root)

	db, err := openLocalDBReadOnly(ctx)
	if err != nil {
		t.Fatalf("read-only open with an existing WAL index in an unwritable dir: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM _migrations").Scan(&n); err != nil {
		t.Fatalf("read _migrations: %v", err)
	}
	if n == 0 {
		t.Error("_migrations is empty — the seed did not migrate")
	}
}

// walIndexUnbuildable decides the wording the two tests above assert, and both
// of them are unreachable as root because mode bits are their premise. Its own
// two questions are not: neither needs a permission the caller might already
// hold, so this is where that decision stays covered at euid 0.
//
// Driven directly rather than through openLocalDBReadOnly because that route
// requires a read-only open to have failed first, and staging that failure is
// exactly the part root cannot be made to reproduce.
func TestWALIndexUnbuildable(t *testing.T) {
	t.Run("existing -shm is never blamed on the directory", func(t *testing.T) {
		// The order that matters: with an index present the failure is
		// something else (corruption, a lock we lost), and answering "true"
		// here would send the operator to chmod a directory that is fine.
		dir := t.TempDir()
		db := filepath.Join(dir, "crewship.db")
		for _, p := range []string{db, db + "-shm"} {
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatalf("stage %s: %v", p, err)
			}
		}
		if walIndexUnbuildable(db) {
			t.Error("an existing -shm was reported as an unbuildable WAL index")
		}
	})

	t.Run("writable directory can build one", func(t *testing.T) {
		dir := t.TempDir()
		db := filepath.Join(dir, "crewship.db")
		if err := os.WriteFile(db, nil, 0o600); err != nil {
			t.Fatalf("stage %s: %v", db, err)
		}
		if walIndexUnbuildable(db) {
			t.Error("a writable data dir was reported as unable to build the WAL index")
		}
		// The writability test is a create-and-remove, and this file's whole
		// subject is diagnostics that leave nothing behind: a probe file that
		// survived would be the same B-02 defect one filename over.
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read data dir: %v", err)
		}
		if len(entries) != 1 || entries[0].Name() != "crewship.db" {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("the writability probe left %v in the data dir", names)
		}
	})

	t.Run("directory that cannot hold a file", func(t *testing.T) {
		// A regular file standing where the data dir should be: both the -shm
		// stat and the probe create fail with ENOTDIR. No permission bits are
		// involved, so this arm — the one that produces the "not writable"
		// wording — is exercised at every euid, root included.
		notDir := filepath.Join(t.TempDir(), "root")
		if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
			t.Fatalf("stage %s: %v", notDir, err)
		}
		if !walIndexUnbuildable(filepath.Join(notDir, "crewship.db")) {
			t.Error("a data dir that cannot hold any file was reported as able to build the WAL index")
		}
	})
}
