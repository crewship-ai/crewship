package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTimestampLintCommandAuditsRealGitChanges(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "lint-tsformat")
	args := []string{"build", "-o", binary}
	coverage := os.Getenv("CREWSHIP_TEST_TSFORMAT_COVERAGE_DIR")
	if coverage != "" {
		if !filepath.IsAbs(coverage) {
			t.Fatal("coverage output must be absolute")
		}
		if err := os.MkdirAll(coverage, 0700); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-cover", "-coverpkg=github.com/crewship-ai/crewship/scripts/lint-tsformat")
	}
	args = append(args, ".")
	build := exec.Command("go", args...)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build linter: %v\n%s", err, out)
	}
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "test")
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/store/store.go", "package store\nfunc save() { db.ExecContext(ctx, query, tsformat.Format(now)) }\n")
	write("internal/deleted.go", "package internal\n")
	write("internal/README.md", "not source\n")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-qm", "base")
	runGit(t, root, "branch", "base")
	runGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	run := func(dir string, wantCode int, want string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(binary, argv...)
		cmd.Dir = dir
		cmd.Env = os.Environ()
		if coverage != "" {
			cmd.Env = append(cmd.Env, "GOCOVERDIR="+coverage)
		}
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatal(err)
			}
			code = exit.ExitCode()
		}
		if code != wantCode || !strings.Contains(string(out), want) {
			t.Fatalf("lint %v code %d want %d: %v\n%s", argv, code, wantCode, err, out)
		}
		return string(out)
	}
	run(root, 0, "ok (full-tree, 2 file(s) scanned)", "--full")
	run(root, 0, "ok (diff-scoped, 0 file(s) scanned)")
	run(root, 0, "ok (diff-scoped, 0 file(s) scanned)", "base")
	run(root, 2, "git merge-base absent-ref HEAD", "absent-ref")
	run(t.TempDir(), 2, "lint-tsformat:", "--full")
	if err := os.Remove(filepath.Join(root, "internal/deleted.go")); err != nil {
		t.Fatal(err)
	}
	write("internal/README.md", "changed but still not source\n")
	run(root, 0, "ok (diff-scoped, 0 file(s) scanned)", "base")
	bad := "package store\nfunc save() {\n stamp := now.Format(time.RFC3339Nano)\n db.ExecContext(ctx, query, stamp)\n stamp2 := now.Format(time.RFC3339Nano)\n db.QueryContext(ctx, query, stamp2)\n}\n"
	write("internal/store/store.go", bad)
	write("internal/aaa.go", bad)
	runGit(t, root, "add", ".")
	out := run(root, 1, "proximity violation(s)", "base")
	if !strings.Contains(out, "convert to tsformat.Format") || strings.Index(out, "internal/aaa.go:") > strings.Index(out, "internal/store/store.go:") {
		t.Fatalf("diagnostics are not actionable and sorted:\n%s", out)
	}
	run(root, 1, "proximity violation(s)", "--full")
}

func TestTimestampLintGitFailuresRemainErrors(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if _, err := changedGoFiles("absent"); err == nil {
		t.Fatal("missing repository appears unchanged")
	}
	if _, err := addedLinesFromDiff("absent", "internal/file.go"); err == nil {
		t.Fatal("missing diff appears empty")
	}
	if _, err := mergeBase("absent"); err == nil {
		t.Fatal("missing merge base accepted")
	}
	if _, err := allGoFiles(); err == nil {
		t.Fatal("missing source tree appears clean")
	}
}
