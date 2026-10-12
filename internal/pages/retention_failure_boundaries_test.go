package pages

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestProjectPrunePreservesUnrecognizedFilesAndRetainedSources(t *testing.T) {
	store := &ProjectStore{Directory: t.TempDir()}
	ctx := t.Context()
	keep, err := store.Put(ctx, "ws", testSourceProject())
	if err != nil {
		t.Fatal(err)
	}
	other := testSourceProject()
	other.Files[0].Content = "old source"
	remove, err := store.Put(ctx, "ws", other)
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.root("ws", false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"operator-notes.txt", ".staging-abandoned"} {
		if err := root.WriteFile(name, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := root.Mkdir(".staging-directory", 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(ctx, "ws", map[string]bool{keep: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "ws", keep); err != nil {
		t.Fatal("deleted retained source", err)
	}
	for _, name := range []string{remove + ".yaml", ".staging-abandoned"} {
		if _, err := root.Stat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unretained %s survives: %v", name, err)
		}
	}
	for _, name := range []string{"operator-notes.txt", ".staging-directory"} {
		if _, err := root.Stat(name); err != nil {
			t.Fatalf("removed unrecognized %s: %v", name, err)
		}
	}
	if err := store.Prune(ctx, "missing", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestProjectPruneRejectsCorruptNamespaces(t *testing.T) {
	for _, kind := range []string{"source entry limit", "git entry limit", "git is file", "unknown repository", "repository is file", "invalid retained commit", "missing retained commit", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			store := &ProjectStore{Directory: t.TempDir()}
			ctx := t.Context()
			root, err := store.root("ws", true)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			retained := map[string][]string{}
			switch kind {
			case "source entry limit":
				for i := 0; i < 1024; i++ {
					if err := root.WriteFile(fmt.Sprintf("note-%d", i), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "git entry limit":
				if err := root.Mkdir("git", 0700); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 4096; i++ {
					if err := root.WriteFile(fmt.Sprintf("git/%d", i), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "git is file":
				err = root.WriteFile("git", nil, 0600)
			case "unknown repository", "repository is file":
				err = root.Mkdir("git", 0700)
				if err == nil {
					if kind == "unknown repository" {
						err = root.Mkdir("git/not-a-hash", 0700)
					} else {
						err = root.WriteFile("git/"+strings.Repeat("a", 64), nil, 0600)
					}
				}
			case "invalid retained commit", "missing retained commit":
				_, err = store.Checkpoint(ctx, "ws", "page", "", "{}", "actor", 1, testSourceProject())
				retained["page"] = []string{"invalid"}
				if kind == "missing retained commit" {
					retained["page"] = []string{strings.Repeat("0", 40)}
				}
			case "canceled":
				err = root.WriteFile("operator-note", nil, 0600)
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Prune(ctx, "ws", nil, retained); err == nil {
				t.Fatal("accepted unsafe retention state")
			}
		})
	}
	if err := (&ProjectStore{}).Prune(t.Context(), "ws", nil, nil); err == nil {
		t.Fatal("accepted unconfigured storage")
	}
}

func TestProjectPruneDeletesRepositoryWithNoRetainedCheckpoints(t *testing.T) {
	store := &ProjectStore{Directory: t.TempDir()}
	ctx := t.Context()
	if _, err := store.Checkpoint(ctx, "ws", "old-page", "", "{}", "actor", 1, testSourceProject()); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(ctx, "ws", nil, map[string][]string{"old-page": nil}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.gitPath(ctx, "ws", "old-page", false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unretained repository survives: %v", err)
	}
}
