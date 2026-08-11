//go:build unix

package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func swapOpenedDirectoryPath(t *testing.T, root, outside string) func(string) {
	t.Helper()
	return func(seg string) {
		if seg != "a" {
			return
		}
		if err := os.Rename(filepath.Join(root, "a"), filepath.Join(root, "a-original")); err != nil {
			t.Fatalf("rename validated parent: %v", err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "a")); err != nil {
			t.Fatalf("replace validated parent: %v", err)
		}
	}
}

func TestEnsureDirNoFollow_RefusesInRootSymlinkSwapDuringOpen(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "redirect"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	err := ensureDirNoFollow(root, filepath.Join(root, "a", "b"), ensureDirHooks{
		beforeOpen: func(seg string) {
			if seg != "a" {
				return
			}
			if err := os.Rename(filepath.Join(root, "a"), filepath.Join(root, "a-original")); err != nil {
				t.Fatalf("rename checked component: %v", err)
			}
			if err := os.Symlink("redirect", filepath.Join(root, "a")); err != nil {
				t.Fatalf("replace checked component: %v", err)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("EnsureDirNoFollow in-root swap error = %v, want symlink refusal", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(root, "redirect"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("in-root replacement directory changed: %v", entries)
	}
}

func assertAnchoredChild(t *testing.T, root, outside string) {
	t.Helper()
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("replacement directory changed: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(root, "a-original", "b")); err != nil {
		t.Fatalf("anchored child missing: %v", err)
	}
}

func TestEnsureDirNoFollow_StaysAnchoredAfterParentSwap(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := ensureDirNoFollow(root, filepath.Join(root, "a", "b"), ensureDirHooks{
		afterSegment: swapOpenedDirectoryPath(t, root, outside),
	})
	if err != nil {
		t.Errorf("EnsureDirNoFollow after pathname replacement: %v", err)
	}
	assertAnchoredChild(t, root, outside)
}

func TestEnsureDirNoFollow_ReadsThroughOpenedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	victim := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := ensureDirNoFollow(root, filepath.Join(root, "a", "b"), ensureDirHooks{
		afterSegment: func(seg string) {
			if seg != "a" {
				return
			}
			swapOpenedDirectoryPath(t, root, outside)(seg)
			if err := os.Symlink(victim, filepath.Join(outside, "b")); err != nil {
				t.Fatalf("plant replacement child: %v", err)
			}
		},
	})
	if err != nil {
		t.Fatalf("EnsureDirNoFollow after replacement child: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "a-original", "b")); err != nil {
		t.Fatalf("anchored child missing: %v", err)
	}
	entries, err := os.ReadDir(victim)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink target changed: %v", entries)
	}
}

func TestEnsureDirNoFollow_RechecksMkdirRaceThroughOpenedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	victim := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := ensureDirNoFollow(root, filepath.Join(root, "a", "b"), ensureDirHooks{
		afterSegment: swapOpenedDirectoryPath(t, root, outside),
		beforeMkdir: func(seg string) {
			if seg != "b" {
				return
			}
			if err := os.Mkdir(filepath.Join(root, "a-original", "b"), 0o755); err != nil {
				t.Fatalf("win mkdir race: %v", err)
			}
			if err := os.Symlink(victim, filepath.Join(outside, "b")); err != nil {
				t.Fatalf("plant replacement child: %v", err)
			}
		},
	})
	if err != nil {
		t.Fatalf("EnsureDirNoFollow after mkdir race: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "a-original", "b")); err != nil {
		t.Fatalf("anchored child missing: %v", err)
	}
	info, err := os.Lstat(filepath.Join(outside, "b"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("replacement child changed: info=%v err=%v", info, err)
	}
	entries, err := os.ReadDir(victim)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink target changed: %v", entries)
	}
}
