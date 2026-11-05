package consolidate

import (
	"os"
	"path/filepath"
	"strings"
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
	var published []byte
	go func() {
		content, err := mergeIntoCanonical(base, canonical, time.Now().UTC(), "rule\n")
		published = content
		done <- err
	}()
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
	if err := <-done; err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !strings.Contains(string(published), "rule") {
		t.Fatalf("the merge did not return what it published: %q", published)
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "victim\n" {
		t.Fatalf("the merge reached another crew's file: %q", got)
	}
}

// Both writers of one learned file must exclude each other through the pinned
// directory, even when the parent path has moved since they pinned it.
func TestRootedWritersShareTheLockAfterAParentSwap(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "crew-a", "topics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	first := memory.NewRootFileLock(root, "learned.md.lock")
	if err := first.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		second := memory.NewRootFileLock(root, "learned.md.lock")
		if err := second.Lock(); err == nil {
			close(acquired)
			_ = second.Unlock()
		}
	}()
	select {
	case <-acquired:
		t.Fatal("a second pinned writer took the lock while the first held it")
	case <-time.After(150 * time.Millisecond):
	}
	if err := first.Unlock(); err != nil {
		t.Fatal(err)
	}
	<-acquired
}
