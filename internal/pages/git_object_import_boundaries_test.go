package pages

import (
	"errors"
	"strings"
	"testing"
)

func TestGitArchiveRejectsInvalidReferencesAndStopsOnVisitorFailure(t *testing.T) {
	store := &ProjectStore{Directory: t.TempDir()}
	ctx := t.Context()
	commit, err := store.Checkpoint(ctx, "ws", "page", "", "{}", "actor", 1, testSourceProject())
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("archive destination unavailable")
	calls := 0
	visitor := func(string, string, []byte) error { calls++; return sentinel }
	if err := store.VisitGitObjects(ctx, "ws", "page", nil, visitor); err != nil || calls != 0 {
		t.Fatalf("empty archive: %v calls %d", err, calls)
	}
	if err := store.VisitGitObjects(ctx, "ws", "page", []string{commit}, visitor); !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("visitor failure: %v calls %d", err, calls)
	}
	for _, id := range []string{"--all", strings.Repeat("0", 40)} {
		if err := store.VisitGitObjects(ctx, "ws", "page", []string{id}, visitor); err == nil {
			t.Fatal("accepted invalid archive checkpoint")
		}
		if err := store.PinCheckpoint(ctx, "ws", "page", id); err == nil {
			t.Fatal("pinned invalid checkpoint")
		}
	}
	if calls != 1 {
		t.Fatalf("invalid checkpoint invoked visitor: %d", calls)
	}
	if err := store.VisitGitObjects(ctx, "missing", "page", []string{commit}, visitor); err == nil {
		t.Fatal("archived missing namespace")
	}
	if err := store.PinCheckpoint(ctx, "missing", "page", commit); err == nil {
		t.Fatal("pinned missing namespace")
	}
	if err := store.PinCheckpoint(ctx, "ws", "page", commit); err != nil {
		t.Fatal(err)
	}
	repo, err := store.gitPath(ctx, "ws", "page", false)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := runProjectGit(ctx, repo, nil, "rev-parse", commit+":project/package.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PinCheckpoint(ctx, "ws", "page", strings.TrimSpace(string(blob))); err == nil {
		t.Fatal("pinned a blob as a checkpoint")
	}
	pinned, err := runProjectGit(ctx, repo, nil, "rev-parse", "refs/checkpoints/"+commit)
	if err != nil || strings.TrimSpace(string(pinned)) != commit {
		t.Fatalf("valid checkpoint pin lost: %s %v", pinned, err)
	}
}
