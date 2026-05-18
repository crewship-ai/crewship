package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// workspace.go — Engine accessor + Reindex pass-through.
//
// NewWorkspaceMemory / Search / GetContext / Close are covered by
// sibling tests; this fills the two zero-coverage helpers: the
// Engine() escape hatch (callers needing HybridSearch reach through
// it) and the Reindex pass-through (post-write FTS refresh).
// ---------------------------------------------------------------------------

func newWorkspaceMemoryForTest(t *testing.T) *WorkspaceMemory {
	t.Helper()
	wm, err := NewWorkspaceMemory(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspaceMemory: %v", err)
	}
	t.Cleanup(func() { _ = wm.Close() })
	return wm
}

// ---- Engine ----

func TestWorkspaceMemory_Engine_ReturnsInternalHandle(t *testing.T) {
	// Engine() is the escape hatch for low-level access (HybridSearch,
	// custom reindex). Source guarantees it returns the underlying
	// *Engine — pin that callers get the actual handle, not a copy or
	// a wrapper.
	wm := newWorkspaceMemoryForTest(t)
	got := wm.Engine()
	if got == nil {
		t.Fatal("Engine() = nil")
	}
	if got != wm.engine {
		t.Errorf("Engine() = %p, want %p (same internal handle)", got, wm.engine)
	}
}

func TestWorkspaceMemory_Engine_NilReceiverReturnsNil(t *testing.T) {
	// Source guard: nil receiver returns nil. Pin so callers can
	// safely chain `wm.Engine() == nil` checks without a defensive
	// `if wm != nil` wrapper everywhere.
	var wm *WorkspaceMemory
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Engine() on nil receiver panicked: %v", r)
		}
	}()
	if got := wm.Engine(); got != nil {
		t.Errorf("Engine() on nil receiver = %v, want nil", got)
	}
}

func TestWorkspaceMemory_Engine_StableAcrossCalls(t *testing.T) {
	// The escape hatch must return the SAME *Engine on every call —
	// callers may stash the pointer; a regression that allocated a
	// new Engine per call would silently produce divergent state.
	wm := newWorkspaceMemoryForTest(t)
	first := wm.Engine()
	second := wm.Engine()
	if first != second {
		t.Errorf("Engine returned different pointers on repeat calls: %p vs %p", first, second)
	}
}

// ---- Reindex ----

func TestWorkspaceMemory_Reindex_RefreshesIndexAfterFileAdded(t *testing.T) {
	// After a write to the on-disk memory dir, search returns nothing
	// until Reindex runs. Pin the pass-through by:
	//   1. Verify a search for "needle" comes up empty initially.
	//   2. Write a markdown file containing "needle".
	//   3. Call Reindex.
	//   4. Verify the search now finds it.
	path := t.TempDir()
	wm, err := NewWorkspaceMemory(path)
	if err != nil {
		t.Fatalf("NewWorkspaceMemory: %v", err)
	}
	t.Cleanup(func() { _ = wm.Close() })

	// Empty workspace — no hits.
	results, err := wm.Search("needle", 10)
	if err != nil {
		t.Fatalf("initial search: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("got %d initial hits, want 0", len(results))
	}

	// Write the file.
	mdPath := filepath.Join(path, "topics", "test.md")
	if err := os.MkdirAll(filepath.Dir(mdPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(mdPath, []byte("# Topic\nfindme: the needle in the haystack."), 0o644); err != nil {
		t.Fatalf("write md: %v", err)
	}

	// Reindex picks up the new file.
	if err := wm.Reindex(); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	results, err = wm.Search("needle", 10)
	if err != nil {
		t.Fatalf("post-reindex search: %v", err)
	}
	if len(results) == 0 {
		t.Errorf("post-reindex search returned no hits; Reindex did not pick up the new file")
	}
}

func TestWorkspaceMemory_Reindex_IsIdempotent(t *testing.T) {
	// Reindex twice in a row must succeed both times — the underlying
	// engine.Reindex rebuilds the FTS5 index; doing it twice should
	// produce identical state, not duplicate rows or errors.
	wm := newWorkspaceMemoryForTest(t)
	for i := 0; i < 3; i++ {
		if err := wm.Reindex(); err != nil {
			t.Errorf("Reindex #%d: %v", i+1, err)
		}
	}
}

func TestWorkspaceMemory_Reindex_EmptyWorkspace(t *testing.T) {
	// An empty workspace (no markdown files at all) must Reindex
	// cleanly — pins that the rebuild handles "no rows to index"
	// without errors. NewWorkspaceMemory does one initial reindex;
	// a second one must also pass.
	wm := newWorkspaceMemoryForTest(t)
	if err := wm.Reindex(); err != nil {
		t.Errorf("Reindex on empty workspace: %v", err)
	}
}
