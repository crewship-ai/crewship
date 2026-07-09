//go:build !windows

package sidecar

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadRegularNoFollow_RejectsSymlink pins the memory-read symlink fence
// (review #926): the final read must open the target with O_NOFOLLOW so a
// symlink swapped in between the path check and the read can't redirect the
// server into reading an arbitrary file. A regular file still reads fine.
func TestReadRegularNoFollow_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "AGENT.md")
	if err := os.WriteFile(real, []byte("agent memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	got, err := readRegularNoFollow(real)
	if err != nil || string(got) != "agent memory\n" {
		t.Fatalf("regular read: got %q err %v", got, err)
	}

	if _, err := readRegularNoFollow(link); err == nil {
		t.Fatal("symlink final component must be rejected by O_NOFOLLOW, got nil error")
	}
}

// TestReadRegularNoFollow_RejectsNonRegular guards the post-open re-Stat: a
// FIFO/socket/dir that survives the open must still be rejected.
func TestReadRegularNoFollow_RejectsNonRegular(t *testing.T) {
	dir := t.TempDir()
	if _, err := readRegularNoFollow(dir); err == nil {
		t.Fatal("a directory must not read as a regular file")
	}
}
