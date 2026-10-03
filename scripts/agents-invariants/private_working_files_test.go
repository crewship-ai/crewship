package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateWorkingPath(t *testing.T) {
	for _, tc := range []struct {
		path    string
		private bool
	}{
		{"CLAUDE.md", true},
		{"CODEX.md", true},
		{"GEMINI.md", true},
		{".cursor", true},
		{".cursor/rules/local.mdc", true},
		{".github/copilot-instructions.md", true},
		{"AGENTS.md", false},
		{".github/workflows/ci.yml", false},
		{"internal/orchestrator/testdata/CODEX.md", false},
		{"docs/guides/CODEX.md", false},
		{".cursor-examples/rule.md", false},
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
	paths := []string{".claude/settings.json", ".codex/session.json", "CLAUDE.md", "CODEX.md", "GEMINI.md", ".cursor/rules/local.mdc", ".github/copilot-instructions.md"}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(strings.Join(paths, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			file := filepath.Join(root, path)
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("private workstation instructions"), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := noPrivateWorkingFiles(root); len(got) != 0 {
				t.Fatalf("ignored local file blocked: %v", got)
			}
			run("add", "-f", path)
			if got := noPrivateWorkingFiles(root); len(got) != 1 {
				t.Fatalf("force-added file not blocked: %v", got)
			}
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if got := noPrivateWorkingFiles(root); len(got) != 1 {
				t.Fatalf("staged content escaped through working-tree deletion: %v", got)
			}
			run("rm", "--cached", path)
			if got := noPrivateWorkingFiles(root); len(got) != 0 {
				t.Fatalf("removed index entry still blocked: %v", got)
			}
		})
	}
}

func TestPrivateWorkingFilesFailsClosedWithoutGitIndex(t *testing.T) {
	if got := noPrivateWorkingFiles(t.TempDir()); len(got) != 1 {
		t.Fatalf("missing Git index should fail: %v", got)
	}
}
