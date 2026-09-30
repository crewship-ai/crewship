//go:build linux

package quota

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) { clear(p); return len(p), nil }
func TestQuotaBackendRequiresPrivilegedOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("unprivileged admission assertion")
	}
	if _, err := NewBackend(t.TempDir(), 64<<20, 0); !errors.Is(err, ErrDenied) {
		t.Fatalf("unprivileged filesystem backend: %v", err)
	}
}
func TestLiveBoundedFilesystemRecoveryAndAttachment(t *testing.T) {
	if os.Getenv("CREWSHIP_LIVE_QUOTA_BACKEND") != "1" || os.Geteuid() != 0 {
		t.Skip("requires explicit root-owned isolated loopback test; no shared daemon restart")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	b, err := NewBackend(root, 128<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	key := Key{"synthetic-crew", "probe", "data", 1}
	d, err := b.Ensure(key, 64<<20)
	if err != nil {
		b.Close()
		t.Fatalf("loop/mount support unavailable or quota create failed: %v", err)
	}
	t.Cleanup(func() {
		if err := b.Remove(key); err != nil {
			if !errors.Is(err, ErrDenied) {
				t.Error(err)
			} else {
				t.Logf("safe removal refused external namespace alias on this host: %v", err)
			}
			dev, _ := mountedDevice(d.Mount)
			if dev != "" {
				_ = command("/usr/bin/umount", d.Mount)
				_ = command("/usr/sbin/losetup", "-d", dev)
			}
		}
		b.Close()
	})
	if _, err = b.Ensure(key, 128<<20); !errors.Is(err, ErrDenied) {
		t.Fatalf("existing quota silently expanded: %v", err)
	}
	if err = os.WriteFile(filepath.Join(d.Mount, "canary"), []byte("DURABLE_QUOTA_CANARY"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(d.Mount, "overflow"))
	if err != nil {
		t.Fatal(err)
	}
	written, err := io.CopyN(f, zeroes{}, 96<<20)
	f.Close()
	if err == nil || written >= 64<<20 {
		t.Fatalf("physical overflow did not stop: wrote%d err%v", written, err)
	}
	if err = os.Remove(filepath.Join(d.Mount, "overflow")); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Ensure(Key{"synthetic-crew", "probe", "other", 1}, 96<<20); !errors.Is(err, ErrDenied) {
		t.Fatalf("aggregate capacity not reserved: %v", err)
	}
	binding := filepath.Join(root, "test-bind")
	if err = os.Mkdir(binding, 0700); err != nil {
		t.Fatal(err)
	}
	if err = command("/usr/bin/mount", "--bind", d.Mount, binding); err != nil {
		t.Fatal(err)
	}
	if err = b.Remove(key); !errors.Is(err, ErrDenied) {
		_ = command("/usr/bin/umount", binding)
		t.Fatalf("attached quota removed: %v", err)
	}
	if err = command("/usr/bin/umount", binding); err != nil {
		t.Fatal(err)
	}
	if err = b.Protect(key, "owned-docker-volume"); err != nil {
		t.Fatal(err)
	}
	if err = b.Remove(key); !errors.Is(err, ErrDenied) {
		t.Fatalf("protected quota removed before Docker attachment: %v", err)
	}
	b.Close()
	b, err = NewBackend(root, 128<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Verify(key, 64<<20); err != nil {
		t.Fatal(err)
	}
	if err = b.Remove(key); !errors.Is(err, ErrDenied) {
		t.Fatalf("helper recovery lost durable protection: %v", err)
	}
	if err = b.Release(key, "owned-docker-volume"); err != nil {
		t.Fatal(err)
	}
	canary, err := os.ReadFile(filepath.Join(d.Mount, "canary"))
	if err != nil || string(canary) != "DURABLE_QUOTA_CANARY" {
		t.Fatalf("restart lost data: %s %v", canary, err)
	}
	t.Logf("own64MiB fully preallocated ext4 overflow denied after%dbytes; helper recovery preserved data; active bind removal denied", written)
}
