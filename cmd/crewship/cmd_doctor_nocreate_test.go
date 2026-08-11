//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

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
func TestCheckDBMigrationVersion_WaitsOutABusyDatabase(t *testing.T) {
	dd := tempDataDir(t)
	ctx := context.Background()

	seed, err := database.Open(dd.DatabaseURL())
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if err := database.Migrate(ctx, seed.DB, covLogger()); err != nil {
		t.Fatalf("seed migrate: %v", err)
	}
	// Closed before the lock is taken: locking_mode=EXCLUSIVE cannot be
	// acquired while another connection still has the WAL index open.
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

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
	// is certainly still held when the probe opens.
	go func() {
		time.Sleep(750 * time.Millisecond)
		_ = locker.Close()
	}()
	r := checkDBMigrationVersion(ctx)

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
// The seed connection is deliberately left OPEN for the duration: that is the
// case operators actually hit, doctor run while crewshipd holds the database,
// and it is the one where a read-only open can go wrong. WAL needs the -shm
// segment, and a connection that is read-only by URI still has to be able to
// join it. Closing the writer first would checkpoint the WAL away and quietly
// skip the interesting half of the test.
func TestOpenLocalDBReadOnly_ReadsAnExistingDatabase(t *testing.T) {
	dd := tempDataDir(t)
	ctx := context.Background()

	seed, err := database.Open(dd.DatabaseURL())
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	defer seed.Close()
	if err := database.Migrate(ctx, seed.DB, covLogger()); err != nil {
		t.Fatalf("seed migrate: %v", err)
	}

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
