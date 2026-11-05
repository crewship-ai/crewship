package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateFileRootedStaysInThePinnedParent(t *testing.T) {
	base := t.TempDir()
	mine := filepath.Join(base, "crew-a", "topics")
	theirs := filepath.Join(base, "crew-b", "topics")
	for _, dir := range []string{mine, theirs} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(mine, "learned.md")
	victim := filepath.Join(theirs, "learned.md")
	if err := os.WriteFile(victim, []byte("victim\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	holder := NewFileLock(target + ".lock")
	if err := holder.Lock(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- UpdateFileRooted(base, target, func(prior []byte, _ bool) ([]byte, error) {
			return append(prior, []byte("appended\n")...), nil
		})
	}()
	time.Sleep(100 * time.Millisecond)
	moved := mine + ".moved"
	if err := os.Rename(mine, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(theirs, mine); err != nil {
		t.Fatal(err)
	}
	if err := holder.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "victim\n" {
		t.Fatalf("the swapped parent redirected the update: %q", got)
	}
}

func TestUpdateFileRootedRefusesSymlinkedComponents(t *testing.T) {
	base := t.TempDir()
	theirs := filepath.Join(base, "crew-b")
	if err := os.MkdirAll(theirs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(theirs, filepath.Join(base, "crew-a")); err != nil {
		t.Fatal(err)
	}
	err := UpdateFileRooted(base, filepath.Join(base, "crew-a", "learned.md"), func([]byte, bool) ([]byte, error) {
		t.Fatal("update must not run")
		return nil, nil
	})
	if err == nil {
		t.Fatal("a symlinked parent must be refused")
	}
	outside := filepath.Join(t.TempDir(), "learned.md")
	if err := UpdateFileRooted(base, outside, func([]byte, bool) ([]byte, error) { return nil, nil }); err == nil {
		t.Fatal("a path outside the root must be refused")
	}
}
