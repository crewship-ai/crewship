package consolidate

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

// A parent swapped while the merge waits on the canonical lock must not send
// the merge into another crew's learned file.
func TestMergeIntoCanonicalIgnoresAParentSwappedDuringTheLock(t *testing.T) {
	base := t.TempDir()
	mine := filepath.Join(base, "crew-a", "topics")
	theirs := filepath.Join(base, "crew-b", "topics")
	for _, dir := range []string{mine, theirs} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	canonical := filepath.Join(mine, "learned-2026-11-05.md")
	victim := filepath.Join(theirs, filepath.Base(canonical))
	if err := os.WriteFile(victim, []byte("victim\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	holder := memory.NewFileLock(canonical + ".lock")
	if err := holder.Lock(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- mergeIntoCanonical(base, canonical, time.Now().UTC(), "rule\n") }()
	time.Sleep(100 * time.Millisecond)
	if err := os.Rename(mine, mine+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(theirs, mine); err != nil {
		t.Fatal(err)
	}
	if err := holder.Unlock(); err != nil {
		t.Fatal(err)
	}
	<-done
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "victim\n" {
		t.Fatalf("the merge reached another crew's file: %q", got)
	}
}
