package pages

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectGitRawFilesHistoryAndIsolation(t *testing.T) {
	ctx := context.Background()
	store := &ProjectStore{Directory: t.TempDir()}
	p := testSourceProject()
	// A hostile global config must never invoke a filter/hook or redirect objects.
	poison := filepath.Join(t.TempDir(), "config")
	marker := filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(poison, []byte("[filter \"evil\"]\n clean = touch "+marker+"\n[core]\n hooksPath = /nonexistent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", poison)
	t.Setenv("GIT_OBJECT_DIRECTORY", t.TempDir())
	p.Files = append(p.Files, ProjectFile{Path: ".gitattributes", Encoding: "utf8", Content: "* filter=evil\n"})
	first, err := store.Checkpoint(ctx, "ws", "page", "", `{"version":1}`, "actor", 1, p)
	if err != nil {
		t.Fatal(err)
	}
	p.Files[0].Content = "updated\n"
	second, err := store.Checkpoint(ctx, "ws", "page", first, `{"version":2}`, "actor", 2, p)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.gitPath(ctx, "ws", "page", false)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := runProjectGit(ctx, repo, nil, "rev-parse", second+"^")
	if err != nil || strings.TrimSpace(string(parent)) != first {
		t.Fatalf("parent lost %s: %v", parent, err)
	}
	raw, err := runProjectGit(ctx, repo, nil, "cat-file", "blob", first+":project/public/logo.bin")
	if err != nil || string(raw) != string([]byte{0, 255, 17}) {
		t.Fatalf("binary changed %v", err)
	}
	restored, spec, err := store.ReadCheckpoint(ctx, "ws", "page", first)
	if err != nil || spec != `{"version":1}` || restored.Files[0].Content == "updated\n" {
		t.Fatalf("restore failed %s %v", spec, err)
	}
	if _, _, err := store.ReadCheckpoint(ctx, "other", "page", first); err == nil {
		t.Fatal("cross-workspace read")
	}
	if _, _, err := store.ReadCheckpoint(ctx, "ws", "other", first); err == nil {
		t.Fatal("cross-Page read")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("executed imported filter")
	}
	if _, err := runProjectGit(ctx, repo, nil, "fsck", "--full", "--no-reflogs"); err != nil {
		t.Fatal(err)
	}
}
func TestProjectGitRejectsInvalidParentAndQuota(t *testing.T) {
	store := &ProjectStore{Directory: t.TempDir()}
	ctx := context.Background()
	p := testSourceProject()
	if _, err := store.Checkpoint(ctx, "ws", "page", "--help", "{}", "actor", 1, p); err == nil {
		t.Fatal("accepted Git argument")
	}
	repo, err := store.gitPath(ctx, "ws", "page", true)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(repo, "quota-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(MaxProjectGitBytes); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := store.Checkpoint(ctx, "ws", "page", "", "{}", "actor", 1, p); err != ErrProjectGitFull {
		t.Fatalf("quota: %v", err)
	}
}

func TestProjectCompactionDropsUnretainedAncestorsWithoutChangingReceipts(t *testing.T) {
	ctx := context.Background()
	store := &ProjectStore{Directory: t.TempDir()}
	p := testSourceProject()
	first, err := store.Checkpoint(ctx, "ws", "page", "", "{}", "actor", 1, p)
	if err != nil {
		t.Fatal(err)
	}
	p.Files[0].Content = "new snapshot"
	last, err := store.Checkpoint(ctx, "ws", "page", first, "{}", "actor", 2, p)
	if err != nil {
		t.Fatal(err)
	}
	release, err := store.Lease(ctx, "ws", true)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Prune(ctx, "ws", map[string]bool{}, map[string][]string{"page": {last}})
	release()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Compact(ctx, "ws"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ReadCheckpoint(ctx, "ws", "page", first); err == nil {
		t.Fatal("unretained ancestor still consumes quota")
	}
	restored, _, err := store.ReadCheckpoint(ctx, "ws", "page", last)
	if err != nil {
		t.Fatal("retained receipt changed", err)
	}
	want, _ := p.Digest()
	got, _ := restored.Digest()
	if got != want {
		t.Fatal("retained source digest changed")
	}
	repo, err := store.gitPath(ctx, "ws", "page", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runProjectGit(ctx, repo, nil, "fsck", "--full"); err != nil {
		t.Fatal(err)
	}
	staged := &ProjectStore{Directory: t.TempDir()}
	if err := store.VisitGitObjects(ctx, "ws", "page", []string{last}, func(id, kind string, data []byte) error {
		return staged.ImportGitObject(ctx, "restored", "page", id, kind, data)
	}); err != nil {
		t.Fatal(err)
	}
	if err := staged.SetCheckpointBoundaries(ctx, "restored", "page", []string{last}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := staged.ReadCheckpoint(ctx, "restored", "page", last); err != nil {
		t.Fatal("shallow checkpoint restore failed", err)
	}
}

func TestProjectGitTreeOrderingMatchesGit(t *testing.T) {
	ctx := context.Background()
	store := &ProjectStore{Directory: t.TempDir()}
	p := testSourceProject()
	for _, name := range []string{"a/file.txt", "a.txt", "a0.txt", "a-foo"} {
		p.Files = append(p.Files, ProjectFile{Path: name, Encoding: "utf8", Content: name})
	}
	commit, err := store.Checkpoint(ctx, "ws", "page", "", "{}", "actor", 1, p)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.gitPath(ctx, "ws", "page", false)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runProjectGit(ctx, repo, nil, "fsck", "--full"); err != nil {
		t.Fatalf("invalid raw tree %s: %v", out, err)
	}
	if _, _, err := store.ReadCheckpoint(ctx, "ws", "page", commit); err != nil {
		t.Fatal(err)
	}
}

// Keep a maximum-file source in the verification toolbox: checkpoint cost must
// not grow by one Git process per source file.
func BenchmarkProjectCheckpointMaximumFiles(b *testing.B) {
	store := &ProjectStore{Directory: b.TempDir()}
	p := testSourceProject()
	for len(p.Files) < MaxProjectFiles {
		p.Files = append(p.Files, ProjectFile{Path: fmt.Sprintf("src/fixtures/file-%03d.ts", len(p.Files)), Encoding: "utf8", Content: fmt.Sprintf("export const value = %d;", len(p.Files))})
	}
	ctx := context.Background()
	var parent string
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		commit, err := store.Checkpoint(ctx, "benchmark", "page", parent, "{}", "benchmark", int64(i+1), p)
		if err != nil {
			b.Fatal(err)
		}
		parent = commit
	}
}
