//go:build !windows

package conversation

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/logging"
)

func TestStoreAppendRefusesExternalSymlinks(t *testing.T) {
	for _, shape := range []string{"directory", "file"} {
		t.Run(shape, func(t *testing.T) {
			base, outside := t.TempDir(), t.TempDir()
			const session = "safe-session"
			const original = "outside must remain unchanged\n"
			external := filepath.Join(outside, session+".jsonl")
			if err := os.WriteFile(external, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			link, target := filepath.Join(base, "conversations"), outside
			if shape == "file" {
				if err := os.Mkdir(link, 0750); err != nil {
					t.Fatal(err)
				}
				link, target = filepath.Join(link, session+".jsonl"), external
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			store := NewStore(base, logging.New("error", "json", nil))
			t.Cleanup(store.Close)
			if err := store.Append(context.Background(), session, Message{ID: "blocked", Role: RoleUser, Content: "must not escape"}); err == nil {
				t.Error("append followed an external transcript symlink")
			}
			got, err := os.ReadFile(external)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != original {
				t.Error("append changed a file outside the store root")
			}
			if len(store.files) != 0 {
				t.Error("refused append cached a transcript file")
			}
		})
	}
}

func TestStoreAppendRootCreatesBaseAndRetainsCachedWrites(t *testing.T) {
	base := filepath.Join(t.TempDir(), "new", "store")
	store := NewStore(base, logging.New("error", "json", nil))
	t.Cleanup(store.Close)
	for _, id := range []string{"first", "second"} {
		if err := store.Append(context.Background(), "safe-session", Message{ID: id, Role: RoleUser, Content: id}); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.files) != 1 {
		t.Fatalf("cached transcript count=%d", len(store.files))
	}
	got, err := store.Read(context.Background(), "safe-session", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "first" || got[1].ID != "second" {
		t.Fatalf("messages=%v", got)
	}
}
