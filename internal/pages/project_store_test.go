package pages

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectStoreDurabilityIsolationAndCorruption(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "sources")
	s := &ProjectStore{Directory: dir}
	p := testSourceProject()
	digest, err := s.Put(ctx, "workspace-a", p)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh instance must read the same data; memory is not the source of truth.
	reopened := &ProjectStore{Directory: dir}
	got, err := reopened.Get(ctx, "workspace-a", digest)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := got.Digest()
	if want != digest {
		t.Fatal("source changed")
	}
	if _, err := reopened.Get(ctx, "workspace-b", digest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cross-workspace read: %v", err)
	}
	if _, err := s.Get(ctx, "workspace-a", "../../etc/passwd"); err == nil {
		t.Fatal("accepted digest traversal")
	}
	ns := fmt.Sprintf("%x", sha256.Sum256([]byte("workspace-a")))
	path := filepath.Join(dir, ns, digest+".yaml")
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "workspace-a", digest); err == nil {
		t.Fatal("served corrupt source")
	}
	if _, err := s.Put(ctx, "workspace-a", p); err == nil {
		t.Fatal("silently replaced corrupt source")
	}
}

func TestProjectStoreBoundsAndSymlinks(t *testing.T) {
	ctx := context.Background()
	s := &ProjectStore{Directory: t.TempDir()}
	p := testSourceProject()
	digest, err := s.Put(ctx, "ws", p)
	if err != nil {
		t.Fatal(err)
	}
	ns := fmt.Sprintf("%x", sha256.Sum256([]byte("ws")))
	dir := filepath.Join(s.Directory, ns)
	for i := 0; i < 255; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("orphan-%d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Put(ctx, "ws", p); err != nil {
		t.Fatal("deduplicated save should not consume quota", err)
	}
	p.Files[0].Content += "changed"
	if _, err := s.Put(ctx, "ws", p); !errors.Is(err, ErrProjectStoreFull) {
		t.Fatalf("quota: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, digest+".yaml")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, digest+".yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "ws", digest); err == nil {
		t.Fatal("followed escaping symlink")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Put(cancelled, "ws", p); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
