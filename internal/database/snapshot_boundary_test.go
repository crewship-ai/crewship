package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
)

// Hide the optional backup/serialize extensions while retaining a working
// SQLite connection, as with an older driver that cannot make safe snapshots.
type legacySnapshotConn struct{ driver.Conn }

func legacySnapshotDB(t *testing.T) *sql.DB {
	t.Helper()
	connection, err := (&sqlite.Driver{}).Open(filepath.Join(t.TempDir(), "legacy.db"))
	require.NoError(t, err)
	db := sql.OpenDB(&singleConnector{conn: legacySnapshotConn{connection}})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSnapshotRejectsMissingDriverCapabilities(t *testing.T) {
	db := legacySnapshotDB(t)
	_, err := db.Exec(`CREATE TABLE sample(value TEXT); INSERT INTO sample VALUES ('preserved')`)
	require.NoError(t, err)
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	require.ErrorContains(t, SnapshotTo(context.Background(), db, destination), "has no NewBackup method")
	_, err = os.Stat(destination)
	require.True(t, os.IsNotExist(err))
	snapshot, err := SnapshotToMemory(context.Background(), db)
	require.Nil(t, snapshot)
	require.ErrorContains(t, err, "has no NewBackup method")
	image, err := (&MemorySnapshot{DB: db}).Serialize(context.Background())
	require.Nil(t, image)
	require.ErrorContains(t, err, "cannot serialize")
	var value string
	require.NoError(t, db.QueryRow(`SELECT value FROM sample`).Scan(&value))
	require.Equal(t, "preserved", value)
}

func TestSnapshotRefusesCancelledAndClosedSourcesWithoutArtifacts(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "source.db"))
	require.NoError(t, err)
	require.NoError(t, db.Ping())
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot, err := SnapshotToMemory(ctx, db)
	require.Nil(t, snapshot)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, SnapshotTo(ctx, db, destination), context.Canceled)
	require.NoError(t, db.Close())
	snapshot, err = SnapshotToMemory(context.Background(), db)
	require.Nil(t, snapshot)
	require.ErrorContains(t, err, "acquire connection")
	require.ErrorContains(t, SnapshotTo(context.Background(), db, destination), "acquire connection")
	_, err = os.Stat(destination)
	require.True(t, os.IsNotExist(err))
}

func TestSnapshotRefusesUnusableDestinationAndKeepsSource(t *testing.T) {
	db, err := Open("file:" + filepath.Join(t.TempDir(), "source.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE sample(value TEXT); INSERT INTO sample VALUES ('preserved')`)
	require.NoError(t, err)
	loop := filepath.Join(t.TempDir(), "loop")
	require.NoError(t, os.Symlink(loop, loop))
	require.ErrorContains(t, SnapshotTo(context.Background(), db.DB, filepath.Join(loop, "snapshot.db")), "stat snapshot destination")
	destination := filepath.Join(t.TempDir(), "missing", "snapshot.db")
	require.ErrorContains(t, SnapshotTo(context.Background(), db.DB, destination), "open snapshot destination")
	_, err = os.Stat(destination)
	require.True(t, os.IsNotExist(err))
	var value string
	require.NoError(t, db.QueryRow(`SELECT value FROM sample`).Scan(&value))
	require.Equal(t, "preserved", value)
}

func TestPendingMigrationDiagnosticsDistinguishFreshEmptyAndBrokenLedger(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "source.db"))
	require.NoError(t, err)
	defer db.Close()
	from, to, pending, err := PendingMigrations(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, []int{0, 0, 0}, []int{from, to, pending})
	_, err = db.Exec(`CREATE TABLE _migrations(version INTEGER); INSERT INTO _migrations VALUES (123)`)
	require.NoError(t, err)
	usePostDeployRegistry(t, nil)
	from, to, pending, err = PendingMigrations(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, []int{123, 123, 0}, []int{from, to, pending})
	migrations = []migration{{version: 124}, {version: 125}}
	require.Equal(t, 125, MaxKnownMigrationVersion())
	from, to, pending, err = PendingMigrations(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, []int{123, 125, 2}, []int{from, to, pending})
	_, err = db.Exec(`DROP TABLE _migrations; CREATE TABLE _migrations(broken TEXT)`)
	require.NoError(t, err)
	_, _, _, err = PendingMigrations(context.Background(), db)
	require.Error(t, err, "broken ledger must not be reported as up to date")
	require.NoError(t, db.Close())
	_, _, _, err = PendingMigrations(context.Background(), db)
	require.Error(t, err)
}
