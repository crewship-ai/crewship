package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMutate_RootSurvivesParentSwap(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		name := "ordinary write"
		if recovery {
			name = "inline recovery"
		}
		t.Run(name, func(t *testing.T) {
			f := newMutateFixture(t)
			parent := filepath.Join(f.dir, "agent")
			f.path = filepath.Join(parent, "AGENT.md")
			req := f.req("seed", OpAppend, "seed\n")
			req.StorageRoot = f.dir
			f.mustMutate(t, req)
			want := "seed\nnext\n"
			if recovery {
				pending := f.req("pending", OpAppend, "promised\n")
				pending.StorageRoot = f.dir
				pending.testHook = crashAt("after_intent")
				if _, err := f.mutate(t, pending); !errors.Is(err, errSimulatedCrash) {
					t.Fatalf("pending intent: %v", err)
				}
				want = "seed\npromised\nnext\n"
			}
			outside := t.TempDir()
			outsideFile := filepath.Join(outside, "AGENT.md")
			if err := os.WriteFile(outsideFile, []byte("outside-private\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			saved := parent + "-original"
			req = f.req("next", OpAppend, "next\n")
			req.StorageRoot = f.dir
			// Authorize runs after the parent and lock are opened, and before both
			// recovery and base reads. Swap exactly there, never by scheduler luck.
			req.Authorize = func(context.Context) error {
				if err := os.Rename(parent, saved); err != nil {
					return err
				}
				return os.Symlink(outside, parent)
			}
			result := f.mustMutate(t, req)
			if recovery && result.Recovered != 1 {
				t.Fatalf("recovered %d, want 1", result.Recovered)
			}
			got, err := os.ReadFile(filepath.Join(saved, "AGENT.md"))
			if err != nil || string(got) != want {
				t.Fatalf("pinned file = %q, want %q: %v", got, want, err)
			}
			got, err = os.ReadFile(outsideFile)
			if err != nil || string(got) != "outside-private\n" {
				t.Fatalf("outside file = %q: %v", got, err)
			}
			if _, err := os.Lstat(outsideFile + ".lock"); !os.IsNotExist(err) {
				t.Fatalf("outside lock exists: %v", err)
			}
		})
	}
}
