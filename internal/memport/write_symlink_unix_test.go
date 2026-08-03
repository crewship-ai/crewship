//go:build !windows

package memport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

// Lexical confinement is not confinement, and proving that needs real
// symlinks. Creating one needs privileges on Windows; a build tag makes
// that a compile-time decision instead of a runtime t.Skip, which would
// report the same "ok" as a genuine pass.

func symlinkOrFail(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
}

// Lexical confinement is not confinement. An agent owns its own
// .memory, so it can replace a subdirectory with a link to somewhere
// else — another crew's shared tree, say — and every path stays
// textually inside the root while the bytes land outside it.
func TestApplyRefusesSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	symlinkOrFail(t, elsewhere, filepath.Join(root, "daily"))

	res, err := Apply(context.Background(), root,
		[]Doc{{Tier: memory.TierAgent, RelPath: "daily/2026-08-01.md", Body: []byte("stolen")}},
		memory.WriteConfig{})
	expectRefused(t, res, err, "symlinked parent")
	if _, serr := os.Stat(filepath.Join(elsewhere, "2026-08-01.md")); serr == nil {
		t.Fatal("content landed outside the memory root")
	}
}

// The same swap one level down: the file itself is the link.
func TestApplyRefusesSymlinkedLeaf(t *testing.T) {
	root := t.TempDir()
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "victim.md")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOrFail(t, victim, filepath.Join(root, "AGENT.md"))

	res, err := Apply(context.Background(), root,
		[]Doc{{Tier: memory.TierAgent, RelPath: "AGENT.md", Body: []byte("overwrite")}},
		memory.WriteConfig{})
	expectRefused(t, res, err, "symlinked leaf")
	got, rerr := os.ReadFile(victim)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "original" {
		t.Fatalf("symlink target was overwritten: %q", got)
	}
}
