//go:build unix

package consolidate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractPatternsRefusesSymlinkedLearnedFile(t *testing.T) {
	victim := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(victim, []byte("- **Pattern:** outside secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "learned-2026-08-10.md")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatalf("plant learned-file symlink: %v", err)
	}

	if got := extractPatterns(link); len(got) != 0 {
		t.Fatalf("extractPatterns followed symlink and returned %q, want no patterns", got)
	}
}
