//go:build linux

package stagedstart

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestInitializeRejectsInvalidRoots(t *testing.T) {
	for _, roots := range [][]string{nil, {"tree:/tmp/invalid"}, {"bad:/mnt/init/invalid"}, {"tree:/mnt/init/../escape"}} {
		if err := Initialize(roots); err == nil {
			t.Fatalf("invalid roots accepted: %v", roots)
		}
	}
}

// Run this test as root to exercise the real offline helper contract. It uses
// actual directory FDs, FIFO/socket inodes, hardlinks and external symlinks.
func TestInitializeRealOwnershipAndSpecialFiles(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("positive ownership fixture requires root; invalid-root tests remain runnable")
	}
	madeParent := false
	if err := os.Mkdir("/mnt/init", 0755); err == nil {
		madeParent = true
	} else if !os.IsExist(err) {
		t.Fatal(err)
	}
	if madeParent {
		defer os.Remove("/mnt/init")
	}
	root, err := os.MkdirTemp("/mnt/init", "crewship-init-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	crew := filepath.Join(root, "crew")
	memory := filepath.Join(crew, "shared", ".memory")
	tree := filepath.Join(root, "tree")
	volume := filepath.Join(root, "volume")
	for _, dir := range []string{memory, tree, volume} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	original := filepath.Join(memory, "retained")
	hardlink := filepath.Join(memory, "hardlink")
	fifo := filepath.Join(memory, "fifo")
	socket := filepath.Join(memory, "socket")
	if err := os.WriteFile(original, []byte("retained content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, hardlink); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0644); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside content"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(memory, "external-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(volume, "untouched")
	if err := os.WriteFile(nested, []byte("named volume data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Initialize([]string{"crew:" + crew, "tree:" + tree, "volume:" + volume}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path     string
		uid, gid uint32
	}{{crew, 1001, 1001}, {tree, 1001, 1001}, {volume, 1001, 1001}, {memory, 1001, 1002}, {original, 1001, 1002}, {hardlink, 1001, 1002}, {fifo, 1001, 1002}, {socket, 1001, 1002}, {link, 1001, 1002}, {outside, 0, 0}, {nested, 0, 0}} {
		info, err := os.Lstat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		st := info.Sys().(*syscall.Stat_t)
		if st.Uid != tc.uid || st.Gid != tc.gid {
			t.Errorf("%s ownership=%d:%d want %d:%d", tc.path, st.Uid, st.Gid, tc.uid, tc.gid)
		}
	}
	info, err := os.Stat(memory)
	if err != nil || info.Mode()&os.ModeSetgid == 0 || info.Mode().Perm() != 0775 {
		t.Fatalf("memory contract: %v %v", info, err)
	}
	info, err = os.Stat(original)
	if err != nil || info.Mode().Perm()&0060 != 0060 || info.Sys().(*syscall.Stat_t).Nlink != 2 {
		t.Fatalf("hardlink/memory permissions: %v %v", info, err)
	}
	if raw, err := os.ReadFile(original); err != nil || string(raw) != "retained content" {
		t.Fatalf("retained content lost: %v", err)
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "outside content" {
		t.Fatalf("symlink followed: %v", err)
	}
	if err := Initialize([]string{"tree:" + link}); err == nil {
		t.Fatal("symlink root admitted")
	}
}
