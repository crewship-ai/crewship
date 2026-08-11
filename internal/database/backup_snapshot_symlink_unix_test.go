//go:build unix

package database

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSnapshotDatabase_RefusesSymlinkedDestination is the symlink half of the
// "never write over something that is already there" guarantee that
// TestSnapshotDatabase_RefusesExistingDestination covers for plain files.
//
// It matters more here than in the plain-file case. The pre-migration
// destination is guessable — "<db>.pre-migrate-v<from>-to-v<to>-<UTC>.bak"
// beside the database — so anyone who can create an entry in the data
// directory can pick where the snapshot lands. A snapshot is a page-for-page
// copy of the whole database: credentials, tokens, session material. Following
// a link therefore both leaks the database to the link's target and, via
// SnapshotBeforeMigrate's chmod, re-permissions a file we never chose.
//
// The dangling case is the one that a Stat-based guard misses: Stat resolves
// the link, gets ErrNotExist, and reports the destination as free.
func TestSnapshotDatabase_RefusesSymlinkedDestination(t *testing.T) {
	tests := []struct {
		name string
		// plantTarget writes the link's target before the snapshot runs, and
		// returns the content that must still be there afterwards. A nil
		// return means the target must not exist at all when we are done.
		plantTarget func(t *testing.T, target string) []byte
	}{
		{
			// The dangling link: os.Stat returns ErrNotExist for it, so a
			// Stat-based guard sees a free destination and hands the path
			// to SQLite, which creates the target and fills it.
			name:        "dangling symlink",
			plantTarget: func(t *testing.T, target string) []byte { return nil },
		},
		{
			// The link points at a file that already exists — an operator's
			// key file, another database. Refusing is the same rule as for a
			// plain existing destination; only the indirection differs.
			name: "symlink to an existing file",
			plantTarget: func(t *testing.T, target string) []byte {
				content := []byte("sentinel: not a snapshot destination\n")
				if err := os.WriteFile(target, content, 0o600); err != nil {
					t.Fatalf("plant target: %v", err)
				}
				return content
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			db := newHotJournalDB(t, filepath.Join(dir, "crewship.db"))
			defer db.Close()

			// The target lives in a different directory so the assertions
			// below cannot be satisfied by accident by anything the snapshot
			// writes next to the database.
			target := filepath.Join(t.TempDir(), "outside-target")
			want := tc.plantTarget(t, target)

			dstPath := filepath.Join(dir, "crewship.db.pre-migrate-v1-to-v2-planted.bak")
			if err := os.Symlink(target, dstPath); err != nil {
				t.Fatalf("plant destination symlink: %v", err)
			}

			err := snapshotDatabase(ctx, db.DB, dstPath)
			if err == nil {
				t.Fatal("snapshot accepted a symlinked destination")
			}
			if !strings.Contains(err.Error(), "already exists") {
				t.Errorf("error = %v, want it to mention the destination already exists", err)
			}

			if want == nil {
				// Nothing may have been created through the link. Lstat, not
				// Stat: we are asking about the target path itself.
				if _, err := os.Lstat(target); !os.IsNotExist(err) {
					t.Fatalf("snapshot wrote through a dangling symlink: Lstat(%s) = %v", target, err)
				}
			} else {
				got, err := os.ReadFile(target)
				if err != nil {
					t.Fatalf("read target: %v", err)
				}
				if string(got) != string(want) {
					t.Errorf("symlink target was overwritten: got %q, want %q", got, want)
				}
			}

			// The refusal happens before anything is opened, so the link is
			// left exactly as found — removeSnapshotArtifacts must not have
			// run and unlinked someone else's entry either.
			info, err := os.Lstat(dstPath)
			if err != nil {
				t.Fatalf("planted symlink disappeared: %v", err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("destination is no longer a symlink: mode = %v", info.Mode())
			}
			// Sidecars would mean SQLite got as far as opening the destination.
			for _, suffix := range []string{"-journal", "-wal", "-shm"} {
				if _, err := os.Lstat(dstPath + suffix); err == nil {
					t.Errorf("refused snapshot left %s behind", dstPath+suffix)
				}
			}
		})
	}
}
