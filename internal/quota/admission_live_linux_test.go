//go:build linux && quota_live

package quota

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func liveAdmissionRoot(t *testing.T) string {
	t.Helper()
	if os.Getenv("CREWSHIP_LIVE_QUOTA_BACKEND") != "1" || os.Geteuid() != 0 {
		t.Fatal("requires explicitly isolated root-owned quota test guest")
	}
	dir, err := os.MkdirTemp("/tmp", "qad-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

func TestLiveCatalogRejectsUnsafeOwnershipAndCompetingHelper(t *testing.T) {
	for _, mode := range []string{"writable parent", "foreign root", "public root", "images file", "foreign mounts", "catalog symlink", "root symlink"} {
		t.Run(mode, func(t *testing.T) {
			parent := liveAdmissionRoot(t)
			root := filepath.Join(parent, "catalog")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "writable parent":
				must(os.Chmod(parent, 0777))
			case "foreign root":
				must(os.Chown(root, 1000, 1000))
			case "public root":
				must(os.Chmod(root, 0755))
			case "images file":
				must(os.WriteFile(filepath.Join(root, "images"), []byte("keep"), 0600))
			case "foreign mounts":
				must(os.Mkdir(filepath.Join(root, "mounts"), 0700))
				must(os.Chown(filepath.Join(root, "mounts"), 1000, 1000))
			case "catalog symlink":
				must(os.WriteFile(filepath.Join(parent, "canary"), []byte("keep"), 0600))
				must(os.Symlink(filepath.Join(parent, "canary"), filepath.Join(root, "catalog.lock")))
			case "root symlink":
				alias := filepath.Join(parent, "alias")
				must(os.Symlink(root, alias))
				root = alias
			}
			b, err := NewBackend(root, 128<<20, 0)
			if b != nil {
				b.Close()
				t.Fatal("unsafe catalog admitted")
			}
			if err == nil {
				t.Fatal("unsafe catalog accepted without error")
			}
			if mode == "catalog symlink" {
				got, err := os.ReadFile(filepath.Join(parent, "canary"))
				if err != nil || string(got) != "keep" {
					t.Fatalf("lock symlink modified unrelated bytes: %q %v", got, err)
				}
			}
		})
	}
	root := liveAdmissionRoot(t)
	b, err := NewBackend(root, 128<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	other, err := NewBackend(root, 128<<20, 0)
	if other != nil {
		other.Close()
		t.Fatal("competing helper acquired catalog")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("competing helper: %v", err)
	}
}

func TestLiveSocketAdmissionPreservesExistingEndpoints(t *testing.T) {
	for _, mode := range []string{"regular file", "active socket", "foreign socket", "stale socket", "unsafe parent", "missing parent", "bad namespace"} {
		t.Run(mode, func(t *testing.T) {
			root := liveAdmissionRoot(t)
			b, err := NewBackend(root, 128<<20, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			front := liveAdmissionRoot(t)
			socket := filepath.Join(front, "helper.sock")
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			namespace := ""
			switch mode {
			case "regular file":
				must(os.WriteFile(socket, []byte("keep"), 0600))
			case "active socket", "foreign socket", "stale socket":
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
				must(err)
				listener.SetUnlinkOnClose(false)
				if mode == "stale socket" {
					must(listener.Close())
				} else {
					defer listener.Close()
				}
				if mode == "foreign socket" {
					must(os.Chown(socket, 1000, 1000))
				}
			case "unsafe parent":
				must(os.Chmod(front, 0777))
			case "missing parent":
				socket = filepath.Join(front, "absent", "helper.sock")
			case "bad namespace":
				namespace = "../foreign"
			}
			readyErr := errors.New("readiness refused")
			ready := false
			err = ServeNamespace(t.Context(), socket, 0, b, namespace, func() error { ready = true; return readyErr })
			if mode == "stale socket" {
				if !ready || !errors.Is(err, readyErr) {
					t.Fatalf("stale socket did not reach bounded readiness: %v", err)
				}
				if _, err := os.Lstat(socket); !os.IsNotExist(err) {
					t.Fatalf("failed readiness leaked socket: %v", err)
				}
			} else if ready || err == nil {
				t.Fatalf("unsafe endpoint reached readiness: %v", err)
			}
			if mode == "regular file" {
				got, err := os.ReadFile(socket)
				if err != nil || string(got) != "keep" {
					t.Fatalf("existing endpoint overwritten: %q %v", got, err)
				}
			}
		})
	}
}
