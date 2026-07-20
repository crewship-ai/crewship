package api

import (
	"os"
	"path/filepath"
	"testing"
)

// setupTestDB's temp directory is deliberately not t.TempDir() — see the
// comment on the helper. This pins the behavioural difference that change
// exists for: a leftover in the test DB's directory must NOT fail the test
// that happens to be cleaning up.
//
// The real-world trigger is a race (a detached background worker re-touching
// the WAL inside RemoveAll's readdir→rmdir window), which cannot be
// reproduced deterministically. So this reproduces the *consequence*
// deterministically instead: an unremovable entry in the directory, which
// makes RemoveAll fail for certain. Under t.TempDir() that fails the test;
// under the helper it must not.
func TestSetupTestDBCleanupDoesNotFailTestOnLeftovers(t *testing.T) {
	var planted string

	passed := t.Run("inner", func(t *testing.T) {
		db := setupTestDB(t)

		// Recover the directory the helper chose, via the DB's own file path.
		var seq int
		var name, file string
		if err := db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &file); err != nil {
			t.Fatalf("read database_list: %v", err)
		}
		if file == "" {
			t.Fatal("database_list returned an empty file path")
		}
		dir := filepath.Dir(file)

		// Plant a directory RemoveAll cannot descend into or unlink: 0500
		// leaves it readable/traversable but not writable, so removing its
		// child fails with EACCES.
		planted = filepath.Join(dir, "undeletable")
		if err := os.Mkdir(planted, 0o700); err != nil {
			t.Fatalf("plant dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(planted, "child"), []byte("x"), 0o600); err != nil {
			t.Fatalf("plant child: %v", err)
		}
		if err := os.Chmod(planted, 0o500); err != nil {
			t.Fatalf("chmod plant: %v", err)
		}
	})

	// Undo the trap so the outer test doesn't leak it either.
	if planted != "" {
		_ = os.Chmod(planted, 0o700)
		_ = os.RemoveAll(filepath.Dir(planted))
	}

	if !passed {
		t.Error("a leftover in the test DB directory failed the test; " +
			"cleanup must be best-effort and only log what survived")
	}
}

// Guards the other half of the contract: the helper still removes the
// directory when nothing is in the way, so switching off t.TempDir() did not
// turn cleanup into a no-op that leaks a temp dir per test across a
// 1800-call-site package.
func TestSetupTestDBCleanupRemovesDirectoryOnSuccess(t *testing.T) {
	var dir string

	t.Run("inner", func(t *testing.T) {
		db := setupTestDB(t)
		var seq int
		var name, file string
		if err := db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &file); err != nil {
			t.Fatalf("read database_list: %v", err)
		}
		dir = filepath.Dir(file)
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("temp dir missing while the test is running: %v", err)
		}
	})

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp dir %s survived cleanup (err=%v); the helper must still remove it", dir, err)
	}
}
