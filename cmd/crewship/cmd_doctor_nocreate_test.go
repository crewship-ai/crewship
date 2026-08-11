//go:build !clionly

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
