//go:build linux

package quota

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A fresh ext4 root is root:root 0755 with a lost+found directory. Images
// that run as a non-root user get EACCES, and postgres initdb refuses a
// non-empty data directory. New volumes are formatted for the image's user
// and handed over empty.
func TestEnsurePreparesFreshVolumeRoot(t *testing.T) {
	cases := []struct {
		name      string
		owner     Owner
		wantOwner string // root_owner= value, "" for none
	}{
		{name: "root image", owner: Owner{}},
		{name: "numeric user", owner: Owner{UID: 1001, Set: true}, wantOwner: "root_owner=1001:0"},
		{name: "numeric user and group", owner: Owner{UID: 999, GID: 998, Set: true}, wantOwner: "root_owner=999:998"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUnitBackend(t)
			u.attach = func(_ context.Context, d Descriptor) error {
				// What mkfs.ext4 leaves on a fresh filesystem.
				return os.Mkdir(filepath.Join(d.Mount, "lost+found"), 0700)
			}
			d, err := u.Ensure(t.Context(), Key{"crew", "database", "data", 1}, unitSize, tc.owner)
			if err != nil {
				t.Fatal(err)
			}
			mkfs := u.ran("mkfs.ext4")
			if len(mkfs) != 1 {
				t.Fatalf("mkfs runs: %v", mkfs)
			}
			extended := mkfs[0][slices.Index(mkfs[0], "-E")+1]
			hasOwner := strings.Contains(extended, "root_owner=")
			if tc.wantOwner == "" && hasOwner || tc.wantOwner != "" && !strings.Contains(extended, tc.wantOwner) {
				t.Fatalf("mkfs -E %q, want owner %q", extended, tc.wantOwner)
			}
			if _, err := os.Lstat(filepath.Join(d.Mount, "lost+found")); !os.IsNotExist(err) {
				t.Fatalf("fresh volume handed over with lost+found: %v", err)
			}
		})
	}
}

// An existing generation keeps its contents: lost+found there may hold
// fsck-recovered data.
func TestEnsureLeavesExistingVolumeRootAlone(t *testing.T) {
	u := newUnitBackend(t)
	k := Key{"crew", "database", "data", 1}
	u.writeEntry(t, k, unitSize, true, true)
	_, _, mount := u.paths(k)
	if err := os.MkdirAll(filepath.Join(mount, "lost+found"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Ensure(t.Context(), k, unitSize, Owner{UID: 1001, Set: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(mount, "lost+found")); err != nil {
		t.Fatal("existing volume contents touched")
	}
	if len(u.ran("mkfs.ext4")) != 0 {
		t.Fatal("existing volume reformatted")
	}
}

// The metadata is durable before the first attach. When that attach fails
// (a busy loop device), the retry takes the existing-entry path; it must
// still hand the fresh volume over without lost+found, or initdb refuses
// it forever.
func TestEnsurePreparesFreshRootAfterFailedFirstAttach(t *testing.T) {
	u := newUnitBackend(t)
	k := Key{"crew", "database", "data", 1}
	attempts := 0
	u.attach = func(_ context.Context, d Descriptor) error {
		attempts++
		if attempts == 1 {
			return errors.New("loop device busy")
		}
		if err := os.Mkdir(filepath.Join(d.Mount, "lost+found"), 0700); err != nil && !os.IsExist(err) {
			return err
		}
		return nil
	}
	if _, err := u.Ensure(t.Context(), k, unitSize, Owner{}); err == nil {
		t.Fatal("first Ensure succeeded despite the failed attach")
	}
	d, err := u.Ensure(t.Context(), k, unitSize, Owner{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(d.Mount, "lost+found")); !os.IsNotExist(err) {
		t.Fatalf("retried fresh volume handed over with lost+found: %v", err)
	}
	// Once prepared, later attaches never touch the root again.
	if err := os.Mkdir(filepath.Join(d.Mount, "lost+found"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Ensure(t.Context(), k, unitSize, Owner{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(d.Mount, "lost+found")); err != nil {
		t.Fatal("prepared volume root touched again")
	}
}
