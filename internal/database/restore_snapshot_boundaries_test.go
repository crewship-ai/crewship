package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func restoreBoundaryFixture(t *testing.T) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crewship.db")
	db, err := Open("file:" + path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE records(value TEXT); INSERT INTO records VALUES ('before')`)
	require.NoError(t, err)
	snapshot := path + ".pre-migrate-v1-to-v2-20261003T000000Z.bak"
	require.NoError(t, SnapshotTo(context.Background(), db.DB, snapshot))
	_, err = db.Exec(`INSERT INTO records VALUES ('after')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return path, snapshot
}

func TestRestoreSnapshotDoesNotFollowPredictableStagingSymlink(t *testing.T) {
	live, snapshot := restoreBoundaryFixture(t)
	unrelated := filepath.Join(t.TempDir(), "unrelated")
	require.NoError(t, os.WriteFile(unrelated, []byte("preserve unrelated file"), 0600))
	require.NoError(t, os.Symlink(unrelated, live+".restore-tmp"))
	require.NoError(t, RestoreSnapshot(live, snapshot))
	data, err := os.ReadFile(unrelated)
	require.NoError(t, err)
	require.True(t, string(data) == "preserve unrelated file", "restore overwrote the unrelated symlink target")
	info, err := os.Lstat(live)
	require.NoError(t, err)
	require.Zero(t, info.Mode()&os.ModeSymlink, "restored database must be a regular file")
	restored, err := sql.Open("sqlite", live)
	require.NoError(t, err)
	defer restored.Close()
	var count int
	require.NoError(t, restored.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestRestoreSnapshotPreservesExistingRollbackCopies(t *testing.T) {
	live, snapshot := restoreBoundaryFixture(t)
	// Cover a clock tick during the operation without relying on a mocked clock.
	var existing []string
	for offset := -2; offset <= 2; offset++ {
		name := live + ".before-restore-" + time.Now().UTC().Add(time.Duration(offset)*time.Second).Format("20060102T150405Z")
		require.NoError(t, os.WriteFile(name, []byte("earlier rollback"), 0600))
		existing = append(existing, name)
	}
	require.NoError(t, RestoreSnapshot(live, snapshot))
	for _, name := range existing {
		content, err := os.ReadFile(name)
		require.NoError(t, err)
		require.True(t, string(content) == "earlier rollback", "existing rollback %s was overwritten", name)
	}
	copies, err := filepath.Glob(live + ".before-restore-*")
	require.NoError(t, err)
	require.Len(t, copies, len(existing)+1, "each restore must retain its own rollback copy")
}

func TestRestoreSnapshotSidecarFailurePreservesLiveDatabase(t *testing.T) {
	live, snapshot := restoreBoundaryFixture(t)
	before, err := os.ReadFile(live)
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(live+"-wal", 0700))
	require.NoError(t, os.WriteFile(filepath.Join(live+"-wal", "owned-sentinel"), []byte("keep"), 0600))
	require.ErrorContains(t, RestoreSnapshot(live, snapshot), "remove ")
	after, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, before, after)
	staged, err := filepath.Glob(live + ".restore-tmp*")
	require.NoError(t, err)
	require.Empty(t, staged, "failed restore must remove its own staged copy")
}

func TestRestoreSnapshotRejectsMalformedSnapshotBeforeChangingLiveFile(t *testing.T) {
	live, snapshot := restoreBoundaryFixture(t)
	before, err := os.ReadFile(live)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(snapshot, []byte("not a database"), 0600))
	require.ErrorContains(t, RestoreSnapshot(live, snapshot), "not a SQLite database")
	after, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, before, after)
	copies, err := filepath.Glob(live + ".before-restore-*")
	require.NoError(t, err)
	require.Empty(t, copies)
}
