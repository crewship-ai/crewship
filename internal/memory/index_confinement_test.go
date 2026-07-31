package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestReindexPathConfinement pins the contract Engine.ReindexPath gets from the
// shared path guard, at the call site rather than only in internal/safepath.
// The guard moved from internal/pathsafe.Join to safepath.JoinRel when the two
// path-safety packages were collapsed; these cases are the ones the deleted
// package tested, plus the two deltas the survivor introduces.
func TestReindexPathConfinement(t *testing.T) {
	ctx := context.Background()
	dir, engine := newCorpusEngine(t)

	// A real file still indexes: the collapse must not tighten the happy path.
	writeCorpusFile(t, dir, 0)
	if n, err := engine.ReindexPath(ctx, corpusFileName(0)); err != nil || n == 0 {
		t.Fatalf("ReindexPath(%q) = %d, %v; want chunks and no error", corpusFileName(0), n, err)
	}

	// A nested file too — the guard accepts multi-segment relative paths, which
	// is the whole reason the old package existed.
	if err := os.WriteFile(filepath.Join(dir, "daily", "2026-07-30.md"), []byte("# Day\n\nnotes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ReindexPath(ctx, "daily/2026-07-30.md"); err != nil {
		t.Fatalf("ReindexPath(nested) unexpected error: %v", err)
	}

	bad := []struct {
		name string
		rel  string
	}{
		{"empty", ""},
		{"dot", "."},
		{"parent", ".."},
		{"escape", "../../etc/passwd"},
		{"escape via subdir", "daily/../../etc/passwd"},
		{"absolute", filepath.FromSlash("/etc/passwd")},
		{"NUL smuggling", "note\x00.md"},
		// Delta vs the deleted pathsafe.Join: a backslash was an ordinary
		// filename byte on Linux and this used to be accepted as one weird
		// component. safepath.JoinRel refuses it, so the reindex key and the
		// sidecar write guard agree on what a component is.
		{"backslash component", `a\b.md`},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if n, err := engine.ReindexPath(ctx, tc.rel); err == nil {
				t.Fatalf("ReindexPath(%q) = %d, nil; want rejection", tc.rel, n)
			}
		})
	}
}
