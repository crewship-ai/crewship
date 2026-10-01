//go:build linux && quota_live

package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
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
	d, err := b.Ensure(context.Background(), source, MinBytes, Owner{UID: 1001, GID: 1002})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := b.Remove(context.Background(), source); err != nil {
			t.Error(err)
		}
		if err := b.Remove(context.Background(), target); err != nil {
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
	if err = b.Protect(context.Background(), source, "synthetic-docker-alias"); err != nil {
		t.Fatal(err)
	}
	var image bytes.Buffer
	if err = b.Export(context.Background(), source, MinBytes, &image); !errors.Is(err, ErrDenied) || image.Len() != 0 {
		t.Fatal("protected image exported", err, image.Len())
	}
	if err = b.Release(context.Background(), source, "synthetic-docker-alias"); err != nil {
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

func TestLiveQuotaSnapshotProtocolBindsNamespaceAndRejectsPartialImport(t *testing.T) {
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
	if err = b.BindNamespace("snapshot-protocol-fixture"); err != nil {
		b.Close()
		t.Fatal(err)
	}
	source := Key{"protocol-source", "database", "data", 1}
	target := Key{"protocol-target", "database", "data", 1}
	partial := Key{"protocol-partial", "database", "data", 1}
	if _, err = b.Ensure(context.Background(), source, MinBytes, Owner{UID: 1001, GID: 1002}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	socket := filepath.Join(root, "snapshot.sock")
	go func() {
		done <- ServeNamespace(ctx, socket, 0, b, "snapshot-protocol-fixture", func() error { close(ready); return nil })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		for _, key := range []Key{source, target, partial} {
			if err := b.Remove(context.Background(), key); err != nil {
				t.Error(err)
			}
		}
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	client := Client{Socket: socket, Namespace: "snapshot-protocol-fixture"}
	var image bytes.Buffer
	if err = client.Export(context.Background(), source, MinBytes, &image); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Import(context.Background(), target, MinBytes, bytes.NewReader(image.Bytes())); err != nil {
		t.Fatal(err)
	}
	// A stalled authenticated image stream must not monopolize the helper.
	stalled, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer stalled.Close()
	_ = stalled.SetDeadline(time.Now().Add(5 * time.Second))
	if err = json.NewEncoder(stalled).Encode(Request{Namespace: client.Namespace, Operation: "import", Key: partial, Bytes: MinBytes}); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(2 * time.Second)
	active := false
	for time.Now().Before(until) {
		b.mu.Lock()
		active = b.activeSnapshots[partial.id()]
		b.mu.Unlock()
		if active {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !active {
		t.Fatal("streaming import never admitted")
	}
	controlCtx, cancelControl := context.WithTimeout(context.Background(), time.Second)
	err = client.Release(controlCtx, Key{"unrelated", "database", "data", 1}, "crewship-quota-unrelated")
	cancelControl()
	if err != nil {
		t.Fatalf("stream blocked unrelated RPC: %v", err)
	}
	if err = b.Close(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed backend during active transfer: %v", err)
	}
	_ = stalled.Close()
	until = time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		b.mu.Lock()
		active = b.activeSnapshots[partial.id()]
		b.mu.Unlock()
		if !active {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if active {
		t.Fatal("disconnected import retained ownership")
	}
	wrong := Client{Socket: socket, Namespace: "wrong-instance"}
	if err = wrong.Export(context.Background(), source, MinBytes, io.Discard); err == nil {
		t.Fatal("foreign namespace exported source image")
	}
	if _, err = client.Import(context.Background(), partial, MinBytes, bytes.NewReader(image.Bytes()[:1024])); err == nil {
		t.Fatal("partial image published")
	}
	// The server observes the closed stream before handling this next request.
	// A complete retry at the same unpublished key proves no resumable metadata
	// or silently mounted partial image survived the interrupted transfer.
	if _, err = client.Import(context.Background(), partial, MinBytes, bytes.NewReader(image.Bytes())); err != nil {
		t.Fatal(err)
	}
}
