//go:build linux

package backup

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// A symlink is copied as a link and never followed: the operator's file and
// directory outside the installation stay untouched and are not duplicated.
func TestForceStageCopiesSymlinksWithoutFollowing(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "data")
	if err := os.MkdirAll(filepath.Join(target, "run"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "operator")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	writeForceFixture(t, filepath.Join(outside, "secret.txt"), "outside")
	links := map[string]string{
		"to-dir":     outside,
		"dangling":   filepath.Join(parent, "nowhere"),
		"relative":   "run",
		"run/nested": "../crewship.db",
		"to-file":    filepath.Join(outside, "secret.txt"),
	}
	for name, dest := range links {
		if err := os.Symlink(dest, filepath.Join(target, name)); err != nil {
			t.Fatal(err)
		}
	}
	s, err := beginForceStage(t.Context(), target)
	if err != nil {
		t.Fatalf("symlinks must not block recovery: %v", err)
	}
	defer s.close()
	for name, dest := range links {
		got, err := os.Readlink(filepath.Join(s.dir, name))
		if err != nil || got != dest {
			t.Errorf("%s: link = %q, %v; want %q", name, got, err, dest)
		}
	}
	// Nothing behind a link was copied into the staged generation.
	if _, err := os.Lstat(filepath.Join(s.dir, "secret.txt")); err == nil {
		t.Error("the link target's content was copied")
	}
	var st, want unix.Stat_t
	if err := unix.Lstat(filepath.Join(target, "to-dir"), &want); err != nil {
		t.Fatal(err)
	}
	if err := unix.Lstat(filepath.Join(s.dir, "to-dir"), &st); err != nil || st.Uid != want.Uid || st.Gid != want.Gid {
		t.Errorf("link owner = %d:%d, %v", st.Uid, st.Gid, err)
	}
	assertForceFixture(t, filepath.Join(outside, "secret.txt"), "outside")
	// The staged tree is still walkable for ownership sync with links in it.
	if err := syncForceTree(t.Context(), s.dir, target, s.mount, want.Uid, want.Gid); err != nil {
		t.Fatalf("sync with links: %v", err)
	}
}

// A socket left by a crashed process is skipped and named; other files copy.
func TestForceStageSkipsSocketsAndNamesThem(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "data")
	if err := os.MkdirAll(filepath.Join(target, "run"), 0700); err != nil {
		t.Fatal(err)
	}
	writeForceFixture(t, filepath.Join(target, "crewship.db"), "db")
	ln, err := net.Listen("unix", filepath.Join(target, "run", "crewship.sock"))
	if err != nil {
		t.Fatalf("cannot create a socket: %v", err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close() // leaves the socket file behind, like a crash
	s, err := beginForceStage(t.Context(), target)
	if err != nil {
		t.Fatalf("a stale socket must not block recovery: %v", err)
	}
	defer s.close()
	if len(s.skipped) != 1 || s.skipped[0] != filepath.Join("run", "crewship.sock") {
		t.Fatalf("skipped = %v", s.skipped)
	}
	if _, err := os.Lstat(filepath.Join(s.dir, "run", "crewship.sock")); err == nil {
		t.Error("socket was copied")
	}
	assertForceFixture(t, filepath.Join(s.dir, "crewship.db"), "db")
}

// Every obstacle is named before anything is copied.
func TestForceStageListsAllObstaclesBeforeCopying(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "data")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(parent, "operator")
	writeForceFixture(t, external, "safe")
	if err := os.Link(external, filepath.Join(target, "hard-a")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(target, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(target, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(external, filepath.Join(target, "sub", "hard-b")); err != nil {
		t.Fatal(err)
	}
	s, err := beginForceStage(t.Context(), target)
	if err == nil {
		s.close()
		t.Fatal("accepted obstacles")
	}
	for _, want := range []string{"hard-a (hard-linked file)", "pipe (special file)", filepath.Join("sub", "hard-b") + " (hard-linked file)", "3 entr"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
	assertForceFixture(t, external, "safe")
	if names, _ := filepath.Glob(filepath.Join(parent, ".crewship-recovery-*")); len(names) != 0 {
		t.Fatalf("scratch left: %v", names)
	}
}
