package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPrivateWorkingPath(t *testing.T) {
	for _, tc := range []struct {
		path    string
		private bool
	}{
		{".claude/settings.json", true},
		{".codex/session.json", true},
		{"internal-docs", true},
		{"internal-docs/research.md", true},
		{"public/design/prototype.html", true},
		{"docs/prd/reports/instance-go.txt", true},
		{"docs/prd/reports/README.md", false},
		{"docs/prd/reports/codex-review-work-regressions-2026-09-11.go.txt", false},
		{"internal/orchestrator/testdata/.claude/settings.json", false},
		{"docs/security/audit.mdx", false},
		{"docs/specs/restricted-context.md", false},
		{"public/brand/logo.svg", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if got := privateWorkingPath(tc.path); got != tc.private {
				t.Fatalf("privateWorkingPath(%q) = %v, want %v", tc.path, got, tc.private)
			}
		})
	}
}

func TestPrivateWorkingFilesUsesIndexNotLocalIgnoredFiles(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.Mkdir(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".claude/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := noPrivateWorkingFiles(root); len(got) != 0 {
		t.Fatalf("ignored local configuration blocked: %v", got)
	}
	run("add", "-f", ".claude/settings.json")
	if got := noPrivateWorkingFiles(root); len(got) != 1 {
		t.Fatalf("force-added configuration not blocked: %v", got)
	}
	if err := os.Remove(filepath.Join(root, ".claude", "settings.json")); err != nil {
		t.Fatal(err)
	}
	if got := noPrivateWorkingFiles(root); len(got) != 1 {
		t.Fatalf("staged content escaped through working-tree deletion: %v", got)
	}
	run("rm", "--cached", ".claude/settings.json")
	if got := noPrivateWorkingFiles(root); len(got) != 0 {
		t.Fatalf("removed index entry still blocked: %v", got)
	}
}

func TestPrivateWorkingFilesFailsClosedWithoutGitIndex(t *testing.T) {
	if got := noPrivateWorkingFiles(t.TempDir()); len(got) != 1 {
		t.Fatalf("missing Git index should fail: %v", got)
	}
}
