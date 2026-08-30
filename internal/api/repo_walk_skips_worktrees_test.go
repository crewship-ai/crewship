package api

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// repoGoFiles returns every non-test .go file under root, repo-relative and
// sorted, for the source-derived guards in this package.
//
// Shared so the walk has one skip list rather than one per guard: what a guard
// must not read is a property of the repository layout, not of the guard.
func repoGoFiles(root string) ([]string, error) {
	var files []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			// `.claude` holds agent worktrees — complete copies of this tree
			// left by parallel sessions. Descending into one re-reports every
			// file in it under a path no allowlist here is keyed by, so a
			// classified loader comes back as an unclassified one (#2188).
			case ".git", ".claude", "node_modules", "web", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// The repository walks that back the source-derived guards must not descend
// into `.claude/`.
//
// `.claude/worktrees/agent-*/` holds complete copies of the tree, left by
// parallel agent sessions working in this clone. A guard that classifies files
// through a map keyed by repo-relative path — notUpstreamDelivery in
// agent_config_provider_test.go is the one that bites — sees
// `.claude/worktrees/agent-x/internal/api/credentials.go`, matches no key, and
// reports a file that was classified years ago as a brand-new unclassified
// credential loader — one bogus finding per classified file per worktree, so
// `go test ./internal/api/...` cannot be made green locally.
//
// CI has no worktrees, so it never sees this. The failure lives exactly where
// the work does (#2188).
//
// Asserted on behaviour, not on the skip list: a test that reads the `case`
// arm back would pass against a walk that skipped `.claude` and descended into
// it anyway. This one builds a tree and asks what came out.
func TestRepoWalk_SkipsAgentWorktrees(t *testing.T) {
	root := t.TempDir()

	mustWrite := func(rel string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte("package x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	// One real source file, and the same file as it appears inside two agent
	// worktrees. Plus a directory the walk already skips, as a control.
	mustWrite("internal/api/credentials.go")
	mustWrite(".claude/worktrees/agent-one/internal/api/credentials.go")
	mustWrite(".claude/worktrees/agent-two/internal/api/credentials.go")
	// A file directly under `.claude`, so the fixture pins the whole directory
	// and not just the worktrees subtree.
	mustWrite(".claude/helper.go")
	mustWrite("vendor/example.com/dep/dep.go")

	got, err := repoGoFiles(root)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	want := []string{"internal/api/credentials.go"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("walk returned %v, want %v", got, want)
		for _, f := range got {
			if f != want[0] {
				t.Errorf("  unexpected: %s — a copy under .claude/ is not a second occurrence "+
					"of the file, and a path-keyed allowlist cannot classify it", f)
			}
		}
	}
}
