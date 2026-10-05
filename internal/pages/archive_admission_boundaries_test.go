package pages

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestGitRestoreQuotaCountsOnlyAdditionalStorage(t *testing.T) {
	for _, tc := range []struct {
		name          string
		target, stage int64
		extra         bool
		wantFull      bool
	}{
		{"same object at quota", MaxProjectGitBytes, MaxProjectGitBytes, false, false},
		{"larger replacement", MaxProjectGitBytes, MaxProjectGitBytes + 1, false, true},
		{"shrink does not fund new objects", MaxProjectGitBytes, 1, true, true},
		{"new object below quota", 0, 1, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := &ProjectStore{Directory: t.TempDir()}
			stage := &ProjectStore{Directory: t.TempDir()}
			for store, size := range map[*ProjectStore]int64{target: tc.target, stage: tc.stage} {
				root, err := store.root("ws", true)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				if err := root.Mkdir("git", 0700); err != nil {
					t.Fatal(err)
				}
				if size > 0 {
					f, err := root.Create("git/object")
					if err != nil {
						t.Fatal(err)
					}
					err = f.Truncate(size)
					closeErr := f.Close()
					if err != nil || closeErr != nil {
						t.Fatalf("fixture: %v %v", err, closeErr)
					}
				}
				if store == stage && tc.extra {
					if err := root.WriteFile("git/new", []byte("extra"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := target.CheckGitRestoreQuota(t.Context(), "ws", stage)
			if tc.wantFull {
				if !errors.Is(err, ErrProjectGitFull) {
					t.Fatalf("quota bypass: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGitQuotaRejectsUnsafeTreesAndInterruptedScans(t *testing.T) {
	for _, kind := range []string{"missing git", "git is file", "symlink", "canceled", "oversize", "destination is directory"} {
		t.Run(kind, func(t *testing.T) {
			target := &ProjectStore{Directory: t.TempDir()}
			stage := &ProjectStore{Directory: t.TempDir()}
			root, err := target.root("ws", true)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			staged, err := stage.root("ws", true)
			if err != nil {
				t.Fatal(err)
			}
			defer staged.Close()
			ctx := t.Context()
			if kind == "git is file" {
				err = root.WriteFile("git", []byte("occupied"), 0600)
			} else if kind != "missing git" {
				err = root.Mkdir("git", 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				err = root.Symlink(t.TempDir(), "git/foreign")
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "oversize":
				var f *os.File
				f, err = root.Create("git/large")
				if err == nil {
					err = f.Truncate(MaxProjectGitBytes + 1)
					closeErr := f.Close()
					if err == nil {
						err = closeErr
					}
				}
			case "destination is directory":
				err = root.Mkdir("git/object", 0700)
				if err == nil {
					err = staged.Mkdir("git", 0700)
				}
				if err == nil {
					err = staged.WriteFile("git/object", []byte("object"), 0600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			quotaErr := target.CheckGitQuota(ctx, "ws")
			restoreErr := target.CheckGitRestoreQuota(ctx, "ws", stage)
			if kind == "missing git" {
				if quotaErr != nil || restoreErr != nil {
					t.Fatalf("empty storage: %v %v", quotaErr, restoreErr)
				}
			} else if kind == "destination is directory" {
				if quotaErr != nil || restoreErr == nil {
					t.Fatalf("destination conflict: %v %v", quotaErr, restoreErr)
				}
			} else if quotaErr == nil || restoreErr == nil {
				t.Fatalf("unsafe storage accepted: %v %v", quotaErr, restoreErr)
			}
		})
	}
}

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
