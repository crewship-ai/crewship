package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/crewship-ai/crewship/internal/writerlease"
)

func TestResolveDataDirInspectsWithoutCreatingState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent", "data")
	t.Setenv("CREWSHIP_DATA_DIR", "  "+root+"  ")
	resolved, err := ResolveDefaultDataDir()
	require.NoError(t, err)
	require.Equal(t, root, resolved.Root)
	_, err = os.Stat(filepath.Dir(root))
	require.True(t, os.IsNotExist(err), "diagnostics must not provision even the parent")
	provisioned, err := DefaultDataDir()
	require.NoError(t, err)
	require.Equal(t, resolved.Root, provisioned.Root)
	for _, sub := range []string{"output", "chats", "logs", "skills"} {
		info, err := os.Stat(filepath.Join(root, sub))
		require.NoError(t, err)
		require.True(t, info.IsDir())
	}
	require.NoError(t, checkDataDirUsable(root))
}

func TestResolveDataDirResolvesRelativeOverrides(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("CREWSHIP_DATA_DIR", "local/state")
	data, err := ResolveDefaultDataDir()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "local", "state"), data.Root)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestResolveDataDirReportsUnusableLocations(t *testing.T) {
	t.Run("existing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-directory")
		require.NoError(t, os.WriteFile(path, []byte("preserve"), 0600))
		t.Setenv("CREWSHIP_DATA_DIR", path)
		data, err := ResolveDefaultDataDir()
		require.Nil(t, data)
		require.ErrorContains(t, err, "not a directory")
		contents, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "preserve", string(contents))
	})
	t.Run("non-directory ancestor", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-directory")
		require.NoError(t, os.WriteFile(path, []byte("preserve"), 0600))
		t.Setenv("CREWSHIP_DATA_DIR", filepath.Join(path, "child"))
		data, err := ResolveDefaultDataDir()
		require.Nil(t, data)
		require.Error(t, err)
	})
	t.Run("readonly ancestor", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.Chmod(root, 0500))
		t.Cleanup(func() { require.NoError(t, os.Chmod(root, 0700)) })
		t.Setenv("CREWSHIP_DATA_DIR", filepath.Join(root, "child"))
		data, err := ResolveDefaultDataDir()
		require.Nil(t, data)
		require.ErrorContains(t, err, "not writable")
	})
	t.Run("symlink cycle", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cycle")
		require.NoError(t, os.Symlink(path, path))
		t.Setenv("CREWSHIP_DATA_DIR", path)
		data, err := ResolveDefaultDataDir()
		require.Nil(t, data)
		require.ErrorContains(t, err, "stat ")
	})
}

func TestExclusiveWriterAndQueryOnlyReadersShareDataSafely(t *testing.T) {
	path := "file:" + filepath.Join(t.TempDir(), "owned.db")
	writer, err := Open(path, WithExclusiveWriter())
	require.NoError(t, err)
	t.Cleanup(func() { _ = writer.Close() })
	require.NoError(t, writer.VerifyWriterLease())
	_, err = writer.Exec(`CREATE TABLE records (value TEXT); INSERT INTO records VALUES ('saved')`)
	require.NoError(t, err)
	competing, err := Open(path, WithExclusiveWriter())
	require.Nil(t, competing)
	require.ErrorIs(t, err, writerlease.ErrHeld)
	reader, err := Open(path, WithQueryOnly())
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })
	require.ErrorIs(t, reader.VerifyWriterLease(), writerlease.ErrLost)
	// Query-only must apply to every pooled connection, not just the first one.
	for i := 0; i < 3; i++ {
		conn, err := reader.Conn(context.Background())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		var value string
		require.NoError(t, conn.QueryRowContext(context.Background(), `SELECT value FROM records`).Scan(&value))
		require.Equal(t, "saved", value)
		_, err = conn.ExecContext(context.Background(), `INSERT INTO records VALUES ('forbidden')`)
		require.ErrorContains(t, err, "readonly")
	}
	require.NoError(t, writer.Close())
	require.ErrorIs(t, writer.VerifyWriterLease(), writerlease.ErrLost)
	next, err := Open(path, WithExclusiveWriter())
	require.NoError(t, err)
	require.NoError(t, next.VerifyWriterLease())
	require.NoError(t, next.Close())
}

func TestExclusiveWriterRejectsEphemeralDatabases(t *testing.T) {
	for _, dsn := range []string{":memory:", "file:ephemeral?mode=memory&cache=shared"} {
		db, err := Open(dsn, WithExclusiveWriter())
		require.Nil(t, db)
		require.ErrorContains(t, err, "persistent local database")
	}
}
