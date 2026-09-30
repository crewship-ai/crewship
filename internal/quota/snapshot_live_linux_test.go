//go:build linux && quota_live

package quota

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type snapshotZeroBytes struct{}

func (snapshotZeroBytes) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestLiveQuotaOfflineSnapshotPreservesDataOwnerAndCapacity(t *testing.T) {
	if os.Geteuid() != 0 || os.Getenv("CREWSHIP_LIVE_QUOTA_BACKEND") != "1" {
		t.Fatal("requires explicitly selected owned root quota fixture")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	b, err := NewBackend(root, 128<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.BindNamespace("snapshot-fixture"); err != nil {
		t.Fatal(err)
	}
	source := Key{"source-crew", "database", "data", 7}
	target := Key{"restored-crew", "database", "data", 7}
	d, err := b.Ensure(source, MinBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := b.Remove(source); err != nil {
			t.Error(err)
		}
		if err := b.Remove(target); err != nil {
			t.Error(err)
		}
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	path := filepath.Join(d.Mount, "owned-canary")
	if err = os.WriteFile(path, []byte("CONSISTENT_PRIVATE_SERVICE_DATA"), 0640); err != nil {
		t.Fatal(err)
	}
	if err = os.Chown(path, 1001, 1002); err != nil {
		t.Fatal(err)
	}
	if err = b.Protect(source, "synthetic-docker-alias"); err != nil {
		t.Fatal(err)
	}
	var image bytes.Buffer
	if err = b.Export(context.Background(), source, MinBytes, &image); !errors.Is(err, ErrDenied) || image.Len() != 0 {
		t.Fatal("protected image exported", err, image.Len())
	}
	if err = b.Release(source, "synthetic-docker-alias"); err != nil {
		t.Fatal(err)
	}
	if err = b.Export(context.Background(), source, MinBytes, &image); err != nil {
		t.Fatal(err)
	}
	if image.Len() != int(MinBytes) {
		t.Fatal("wrong offline image length", image.Len())
	}
	restored, err := b.Import(context.Background(), target, MinBytes, bytes.NewReader(image.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(restored.Mount, "owned-canary")
	data, err := os.ReadFile(canary)
	if err != nil || string(data) != "CONSISTENT_PRIVATE_SERVICE_DATA" {
		t.Fatal("snapshot data lost", err)
	}
	info, err := os.Stat(canary)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatal("mode lost", err)
	}
	owner, err := os.Open(canary)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := owner.Stat()
	owner.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotOwnerMatches(stat, 1001, 1002) {
		t.Fatal("numeric owner lost")
	}
	if _, err = b.Import(context.Background(), target, MinBytes, bytes.NewReader(image.Bytes())); !errors.Is(err, ErrDenied) {
		t.Fatal("existing generation overwritten", err)
	}
	overflow, err := os.Create(filepath.Join(restored.Mount, "overflow"))
	if err != nil {
		t.Fatal(err)
	}
	n, copyErr := io.CopyN(overflow, snapshotZeroBytes{}, 40<<20)
	overflow.Close()
	if copyErr == nil || n >= MinBytes {
		t.Fatal("restored physical quota escaped", n, copyErr)
	}
	if err = os.Remove(filepath.Join(restored.Mount, "overflow")); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Import(context.Background(), Key{"invalid", "database", "data", 1}, MinBytes, io.LimitReader(snapshotZeroBytes{}, MinBytes)); err == nil {
		t.Fatal("invalid ext4 image published")
	}
}

func snapshotOwnerMatches(info os.FileInfo, uid, gid uint32) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uid && stat.Gid == gid
}
