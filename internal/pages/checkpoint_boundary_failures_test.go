package pages

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCheckpointBoundaryValidationPreservesExistingRoots(t *testing.T) {
	ctx := t.Context()
	store := &ProjectStore{Directory: t.TempDir()}
	commit, err := store.Checkpoint(ctx, "ws", "page", "", "{}", "actor", 1, testSourceProject())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.gitPath(ctx, "ws", "page", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetCheckpointBoundaries(ctx, "ws", "page", []string{commit, commit}); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(repo, "shallow"))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := runProjectGit(ctx, repo, nil, "rev-parse", commit+":project/package.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, ids := range map[string][]string{
		"empty":    nil,
		"invalid":  {"--help"},
		"missing":  {strings.Repeat("0", 40)},
		"blob":     {strings.TrimSpace(string(blob))},
		"too many": make([]string, 4097),
	} {
		t.Run(name, func(t *testing.T) {
			if err := store.SetCheckpointBoundaries(ctx, "ws", "page", ids); err == nil {
				t.Fatal("accepted invalid root")
			}
			got, err := os.ReadFile(filepath.Join(repo, "shallow"))
			if err != nil || string(got) != string(want) {
				t.Fatalf("changed original roots: %q, %v", got, err)
			}
		})
	}
	if got, err := store.CheckpointBoundaries(ctx, "ws", "page"); err != nil || !reflect.DeepEqual(got, []string{commit}) {
		t.Fatalf("roots %v: %v", got, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.SetCheckpointBoundaries(canceled, "ws", "page", []string{commit}); err == nil {
		t.Fatal("canceled operation succeeded")
	}
	if got, err := os.ReadFile(filepath.Join(repo, "shallow")); err != nil || string(got) != string(want) {
		t.Fatalf("cancellation changed roots: %q, %v", got, err)
	}
}

func TestCheckpointBoundaryStorageFailures(t *testing.T) {
	for _, kind := range []string{"invalid contents", "oversize", "too many", "directory", "escaping symlink", "locked directory", "destination directory"} {
		t.Run(kind, func(t *testing.T) {
			store := &ProjectStore{Directory: t.TempDir()}
			ctx := t.Context()
			commit, err := store.Checkpoint(ctx, "ws", "page", "", "{}", "actor", 1, testSourceProject())
			if err != nil {
				t.Fatal(err)
			}
			repo, err := store.gitPath(ctx, "ws", "page", false)
			if err != nil {
				t.Fatal(err)
			}
			shallow := filepath.Join(repo, "shallow")
			switch kind {
			case "invalid contents":
				err = os.WriteFile(shallow, []byte("not-a-commit\n"), 0600)
			case "oversize":
				err = os.WriteFile(shallow, []byte(strings.Repeat("x", 4096*41+1)), 0600)
			case "too many":
				err = os.WriteFile(shallow, []byte(strings.Repeat("a ", 4097)), 0600)
			case "directory", "destination directory":
				err = os.Mkdir(shallow, 0700)
			case "escaping symlink":
				outside := filepath.Join(t.TempDir(), "shallow")
				if err = os.WriteFile(outside, []byte(commit+"\n"), 0600); err == nil {
					err = os.Symlink(outside, shallow)
				}
			case "locked directory":
				err = os.Mkdir(filepath.Join(repo, "shallow.lock"), 0700)
				if err == nil {
					err = os.WriteFile(filepath.Join(repo, "shallow.lock", "owned"), []byte("preserve"), 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "locked directory" || kind == "destination directory" {
				if err := store.SetCheckpointBoundaries(ctx, "ws", "page", []string{commit}); err == nil {
					t.Fatal("ignored storage failure")
				}
				if kind == "locked directory" {
					if got, err := os.ReadFile(filepath.Join(repo, "shallow.lock", "owned")); err != nil || string(got) != "preserve" {
						t.Fatalf("removed occupied lock: %q %v", got, err)
					}
				}
			} else if ids, err := store.CheckpointBoundaries(ctx, "ws", "page"); err == nil || ids != nil {
				t.Fatalf("accepted corrupt roots: %v %v", ids, err)
			}
		})
	}
	store := &ProjectStore{Directory: t.TempDir()}
	if _, err := store.CheckpointBoundaries(t.Context(), "missing", "page"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing repository: %v", err)
	}
	if err := store.SetCheckpointBoundaries(t.Context(), "missing", "page", []string{strings.Repeat("0", 40)}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing repository write: %v", err)
	}
}
