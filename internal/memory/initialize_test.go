package memory

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestInitializeFileConcurrentAndIdempotent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "AGENT.md")
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := InitializeFile(context.Background(), root, path, []byte("complete initial body"))
			if err != nil {
				t.Error(err)
			}
			if ok {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("created %d times", created.Load())
	}
	if err := os.WriteFile(path, []byte("operator edit"), 0664); err != nil {
		t.Fatal(err)
	}
	ok, err := InitializeFile(context.Background(), root, path, []byte("overwrite attempt"))
	if err != nil || ok {
		t.Fatalf("retry: %v %v", ok, err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "operator edit" {
		t.Fatalf("operator edit lost: %s %v", b, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("leftover temporary files: %v", entries)
	}
}

func TestInitializeFileConfinesPaths(t *testing.T) {
	for _, kind := range []string{"outside", "leaf-symlink", "parent-symlink", "in-root-parent-symlink"} {
		t.Run(kind, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			sentinel := filepath.Join(outside, "AGENT.md")
			if err := os.WriteFile(sentinel, []byte("private"), 0600); err != nil {
				t.Fatal(err)
			}
			target := sentinel
			switch kind {
			case "leaf-symlink":
				target = filepath.Join(root, "AGENT.md")
				if err := os.Symlink(sentinel, target); err != nil {
					t.Skip(err)
				}
			case "parent-symlink":
				link := filepath.Join(root, "agent")
				if err := os.Symlink(outside, link); err != nil {
					t.Skip(err)
				}
				target = filepath.Join(link, "AGENT.md")
			case "in-root-parent-symlink":
				sibling := filepath.Join(root, "other-crew")
				if err := os.Mkdir(sibling, 0755); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(root, "agent")
				if err := os.Symlink(sibling, link); err != nil {
					t.Skip(err)
				}
				target = filepath.Join(link, "AGENT.md")
			}
			if _, err := InitializeFile(context.Background(), root, target, []byte("attacker")); err == nil {
				t.Fatal("unconfined write allowed")
			}
			b, _ := os.ReadFile(sentinel)
			if string(b) != "private" {
				t.Fatal("outside file changed")
			}
		})
	}
}
