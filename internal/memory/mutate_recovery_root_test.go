package memory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverPendingUnderRoot_UsesPinnedStorageBoundary(t *testing.T) {
	t.Run("recovers an in-root pending write", func(t *testing.T) {
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("seed", OpReplace, "base\n"))
		req := f.req("crash", OpReplace, "target\n")
		req.ExpectedRevision = 1
		req.testHook = crashAt("after_intent")
		if _, err := f.mutate(t, req); !errors.Is(err, errSimulatedCrash) {
			t.Fatalf("create pending intent: %v", err)
		}
		recovered, conflicted, err := RecoverPendingUnderRoot(t.Context(), f.db, f.blobRoot, f.dir)
		if err != nil || recovered != 1 || conflicted != 0 || f.onDisk(t) != "target\n" {
			t.Fatalf("rooted recovery = %d, %d, %v, file %q", recovered, conflicted, err, f.onDisk(t))
		}
	})

	t.Run("rejects a path outside the current storage root", func(t *testing.T) {
		f := newMutateFixture(t)
		f.mustMutate(t, f.req("seed", OpReplace, "base\n"))
		req := f.req("crash", OpReplace, "target\n")
		req.ExpectedRevision = 1
		req.testHook = crashAt("after_intent")
		if _, err := f.mutate(t, req); !errors.Is(err, errSimulatedCrash) {
			t.Fatalf("create pending intent: %v", err)
		}
		if _, _, err := RecoverPendingUnderRoot(t.Context(), f.db, f.blobRoot, t.TempDir()); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("out-of-root recovery = %v, want permission error", err)
		}
		if got := f.onDisk(t); got != "base\n" {
			t.Fatalf("out-of-root recovery wrote file: %q", got)
		}
		assertPending(t, f, 1)
	})

	t.Run("rejects a symlinked parent", func(t *testing.T) {
		f := newMutateFixture(t)
		outside := t.TempDir()
		link := filepath.Join(f.dir, "agent")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		f.path = filepath.Join(link, "AGENT.md")
		f.mustMutate(t, f.req("seed", OpReplace, "base\n"))
		req := f.req("crash", OpReplace, "target\n")
		req.ExpectedRevision = 1
		req.testHook = crashAt("after_intent")
		if _, err := f.mutate(t, req); !errors.Is(err, errSimulatedCrash) {
			t.Fatalf("create pending intent: %v", err)
		}
		if _, _, err := RecoverPendingUnderRoot(t.Context(), f.db, f.blobRoot, f.dir); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("symlinked recovery = %v, want permission error", err)
		}
		if got := f.onDisk(t); got != "base\n" {
			t.Fatalf("symlinked recovery wrote outside file: %q", got)
		}
		assertPending(t, f, 1)
	})

	t.Run("does not fall back to an absolute path when the parent vanished", func(t *testing.T) {
		f := newMutateFixture(t)
		parent := filepath.Join(f.dir, "agent")
		f.path = filepath.Join(parent, "AGENT.md")
		req := f.req("crash", OpReplace, "target\n")
		req.testHook = crashAt("after_intent")
		if _, err := f.mutate(t, req); !errors.Is(err, errSimulatedCrash) {
			t.Fatalf("create pending intent: %v", err)
		}
		if err := os.RemoveAll(parent); err != nil {
			t.Fatal(err)
		}
		if _, _, err := RecoverPendingUnderRoot(t.Context(), f.db, f.blobRoot, f.dir); err == nil {
			t.Fatal("missing parent silently fell back to an absolute write")
		}
		if _, err := os.Stat(parent); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recovery recreated missing parent: %v", err)
		}
		assertPending(t, f, 1)
	})
}
