package database

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func usePostDeployRegistry(t *testing.T, entries []migration) {
	t.Helper()
	original, originalErr := migrations, migrationRegistryErr
	migrations, migrationRegistryErr = entries, nil
	t.Cleanup(func() { migrations, migrationRegistryErr = original, originalErr })
}

func TestPublicPostDeployRunnerResumesAndSkipsAppliedEntries(t *testing.T) {
	db, pending := postDeployFixture(t, 12, convergingBackfill)
	applied := migration{version: pending.version - 1, name: "already_applied", sql: "THIS MUST NOT RUN", postDeploy: true}
	normal := migration{version: pending.version - 2, name: "boot_only", sql: "THIS MUST NOT RUN"}
	usePostDeployRegistry(t, []migration{normal, applied, pending})
	_, err := db.Exec(`INSERT INTO _migrations(version,name) VALUES (?,?)`, applied.version, applied.name)
	require.NoError(t, err)
	before, err := PostDeployPending(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, []PostDeployStatus{{Version: applied.version, Name: applied.name, Applied: true}, {Version: pending.version, Name: pending.name, Applied: false}}, before)
	require.NoError(t, RunPostDeployMigrations(context.Background(), db, pdLogger()))
	require.Equal(t, 12, countFilled(t, db))
	after, err := PostDeployPending(context.Background(), db)
	require.NoError(t, err)
	require.Len(t, after, 2)
	for _, migration := range after {
		require.True(t, migration.Applied)
	}
	require.NoError(t, RunPostDeployMigrations(context.Background(), db, pdLogger()), "a second invocation must not reapply recorded migrations")
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&count))
	require.Equal(t, 2, count)
}

func TestPublicPostDeployRunnerReportsBrokenRegistryAndLedger(t *testing.T) {
	t.Run("registry", func(t *testing.T) {
		usePostDeployRegistry(t, nil)
		migrationRegistryErr = errors.New("invalid embedded SQL")
		require.ErrorContains(t, RunPostDeployMigrations(context.Background(), nil, pdLogger()), "invalid embedded SQL")
	})
	t.Run("ledger", func(t *testing.T) {
		db, pending := postDeployFixture(t, 1, convergingBackfill)
		usePostDeployRegistry(t, []migration{pending})
		_, err := db.Exec(`DROP TABLE _migrations`)
		require.NoError(t, err)
		statuses, err := PostDeployPending(context.Background(), db)
		require.Nil(t, statuses)
		require.ErrorContains(t, err, "check post-deploy migration")
		require.ErrorContains(t, RunPostDeployMigrations(context.Background(), db, pdLogger()), "check post-deploy migration")
		require.Equal(t, 0, countFilled(t, db))
	})
	t.Run("empty registry", func(t *testing.T) {
		usePostDeployRegistry(t, nil)
		require.NoError(t, RunPostDeployMigrations(context.Background(), nil, pdLogger()))
	})
}

func TestPublicPostDeployRunnerRollsBackFailedBatchAndStopsLaterMigrations(t *testing.T) {
	db, pending := postDeployFixture(t, 3, convergingBackfill)
	later := migration{version: pending.version + 1, name: "later", sql: `UPDATE widgets SET label='must-not-run'`, postDeploy: true}
	usePostDeployRegistry(t, []migration{pending, later})
	_, err := db.Exec(`CREATE TRIGGER refuse_widget BEFORE UPDATE ON widgets WHEN NEW.id=1 BEGIN SELECT RAISE(FAIL,'fixture refuses second row'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, RunPostDeployMigrations(context.Background(), db, pdLogger()), "fixture refuses second row")
	require.Equal(t, 0, countFilled(t, db), "earlier row changes in the failing statement must roll back with its transaction")
	var nulls, recorded int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM widgets WHERE label IS NULL`).Scan(&nulls))
	require.Equal(t, 3, nulls)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&recorded))
	require.Zero(t, recorded)
}

func TestPublicPostDeployRunnerRetriesUnrecordedCommittedBatches(t *testing.T) {
	db, pending := postDeployFixture(t, 7, convergingBackfill)
	usePostDeployRegistry(t, []migration{pending})
	_, err := db.Exec(`CREATE TRIGGER refuse_ledger BEFORE INSERT ON _migrations BEGIN SELECT RAISE(ABORT,'ledger unavailable'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, RunPostDeployMigrations(context.Background(), db, pdLogger()), "record: ")
	require.Equal(t, 7, countFilled(t, db), "committed backfill must survive failure to record completion")
	statuses, err := PostDeployPending(context.Background(), db)
	require.NoError(t, err)
	require.False(t, statuses[0].Applied)
	_, err = db.Exec(`DROP TRIGGER refuse_ledger`)
	require.NoError(t, err)
	require.NoError(t, RunPostDeployMigrations(context.Background(), db, pdLogger()))
	statuses, err = PostDeployPending(context.Background(), db)
	require.NoError(t, err)
	require.True(t, statuses[0].Applied)
	require.Equal(t, 7, countFilled(t, db))
}

func TestPostDeployLongBackfillReportsProgressAndCompletion(t *testing.T) {
	db, pending := postDeployFixture(t, 501, `UPDATE widgets SET label='filled' WHERE id IN (SELECT id FROM widgets WHERE label IS NULL LIMIT 25)`)
	usePostDeployRegistry(t, []migration{pending})
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, nil))
	require.NoError(t, RunPostDeployMigrations(context.Background(), db, logger))
	require.Equal(t, 501, countFilled(t, db))
	require.Contains(t, log.String(), "post-deployment migration progress")
	require.Contains(t, log.String(), "passes=20 rows=500")
	require.Contains(t, log.String(), "post-deployment migration complete")
}
