package memory

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWorkspaceMemoryRegistry_LazyInit(t *testing.T) {
	dir := t.TempDir()
	r := NewWorkspaceMemoryRegistry(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer r.Close()

	wm := r.For("ws_a")
	if wm == nil {
		t.Fatalf("expected non-nil WorkspaceMemory on first For")
	}
	// Directory should now exist on disk.
	if _, err := wm.Search("anything", 5); err != nil {
		t.Errorf("freshly-created WorkspaceMemory should accept search, got %v", err)
	}
}

func TestWorkspaceMemoryRegistry_CachesAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	r := NewWorkspaceMemoryRegistry(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer r.Close()

	a1 := r.For("ws_a")
	a2 := r.For("ws_a")
	if a1 != a2 {
		t.Errorf("repeat For(same id) returned different pointers — cache miss")
	}
	b := r.For("ws_b")
	if b == a1 {
		t.Errorf("different workspaces share a WorkspaceMemory")
	}
}

func TestWorkspaceMemoryRegistry_ConcurrentInit_OneCreate(t *testing.T) {
	dir := t.TempDir()
	r := NewWorkspaceMemoryRegistry(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer r.Close()

	// 32 goroutines race on the same workspace id; the double-checked
	// lock pattern must collapse them to a single underlying engine.
	var wg sync.WaitGroup
	results := make([]*WorkspaceMemory, 32)
	for i := range results {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = r.For("ws_race")
		}()
	}
	wg.Wait()
	first := results[0]
	if first == nil {
		t.Fatalf("nil result from concurrent init")
	}
	for i, got := range results {
		if got != first {
			t.Errorf("goroutine %d got a different WorkspaceMemory pointer", i)
		}
	}
}

func TestWorkspaceMemoryRegistry_EmptyID_Nil(t *testing.T) {
	dir := t.TempDir()
	r := NewWorkspaceMemoryRegistry(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer r.Close()
	if got := r.For(""); got != nil {
		t.Errorf("empty workspace_id must return nil")
	}
}

func TestWorkspaceMemoryRegistry_NilRegistry_Safe(t *testing.T) {
	// Receiver-on-nil is safe so a callsite that builds the registry
	// conditionally (test harness, dry-run) doesn't have to guard
	// every For() call.
	var r *WorkspaceMemoryRegistry
	if got := r.For("anything"); got != nil {
		t.Errorf("nil registry must return nil on For")
	}
	if err := r.Close(); err != nil {
		t.Errorf("nil registry Close returned %v, want nil", err)
	}
}

func TestWorkspaceMemoryRegistry_FailingPath_CachedNil(t *testing.T) {
	// Use a path that NewWorkspaceMemory will fail to initialise on:
	// a parent path whose own parent exists but the in-between segment
	// is a regular file rather than a directory.
	dir := t.TempDir()
	jam := filepath.Join(dir, "not-a-dir")
	if err := writeStub(jam); err != nil {
		t.Fatalf("seed jam file: %v", err)
	}
	root := filepath.Join(jam, "underneath")
	r := NewWorkspaceMemoryRegistry(root, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer r.Close()

	if got := r.For("ws_fail"); got != nil {
		t.Errorf("init under a non-directory path should return nil, got %v", got)
	}
	// Second call must NOT re-attempt — registry caches the failure.
	if got := r.For("ws_fail"); got != nil {
		t.Errorf("second For after init failure should still be nil")
	}
}

func writeStub(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	return f.Close()
}
