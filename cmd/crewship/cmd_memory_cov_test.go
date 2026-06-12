package main

// Coverage tests for cmd_memory.go — local FTS5 memory inspection. The
// memory engine is real (SQLite in a temp dir); no server involved.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveMemoryPaths(t *testing.T) {
	base := t.TempDir()

	t.Run("agent appends .memory", func(t *testing.T) {
		paths, err := resolveMemoryPaths(base, "agent")
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) != 1 || paths[0].scope != "agent" || paths[0].path != filepath.Join(base, ".memory") {
			t.Errorf("got %+v", paths)
		}
	})

	t.Run("agent keeps explicit .memory", func(t *testing.T) {
		p := filepath.Join(base, ".memory")
		paths, err := resolveMemoryPaths(p, "agent")
		if err != nil {
			t.Fatal(err)
		}
		if paths[0].path != p {
			t.Errorf("path doubled: %q", paths[0].path)
		}
	})

	t.Run("crew resolves shared/.memory", func(t *testing.T) {
		paths, err := resolveMemoryPaths(base, "crew")
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) != 1 || paths[0].scope != "crew" || paths[0].path != filepath.Join(base, "shared", ".memory") {
			t.Errorf("got %+v", paths)
		}
	})

	t.Run("workspace uses base as-is", func(t *testing.T) {
		paths, err := resolveMemoryPaths(base, "workspace")
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) != 1 || paths[0].path != base {
			t.Errorf("got %+v", paths)
		}
	})

	t.Run("all without crew dir yields agent only", func(t *testing.T) {
		paths, err := resolveMemoryPaths(base, "all")
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) != 1 || paths[0].scope != "agent" {
			t.Errorf("got %+v", paths)
		}
	})

	t.Run("all with crew dir yields agent and crew", func(t *testing.T) {
		withCrew := t.TempDir()
		if err := os.MkdirAll(filepath.Join(withCrew, "shared", ".memory"), 0o755); err != nil {
			t.Fatal(err)
		}
		paths, err := resolveMemoryPaths(withCrew, "all")
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) != 2 || paths[0].scope != "agent" || paths[1].scope != "crew" {
			t.Errorf("got %+v", paths)
		}
	})

	t.Run("unknown scope errors", func(t *testing.T) {
		if _, err := resolveMemoryPaths(base, "galaxy"); err == nil || !strings.Contains(err.Error(), `unknown scope "galaxy"`) {
			t.Errorf("got %v", err)
		}
	})
}

func TestEnsureMemorySubdirAndDirExists(t *testing.T) {
	if got := ensureMemorySubdir("/a/b"); got != filepath.Join("/a/b", ".memory") {
		t.Errorf("ensureMemorySubdir: %q", got)
	}
	if got := ensureMemorySubdir("/a/b/.memory"); got != "/a/b/.memory" {
		t.Errorf("ensureMemorySubdir idempotency: %q", got)
	}

	dir := t.TempDir()
	if !dirExists(dir) {
		t.Error("dirExists(temp dir) = false")
	}
	if dirExists(filepath.Join(dir, "missing")) {
		t.Error("dirExists(missing) = true")
	}
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if dirExists(file) {
		t.Error("dirExists(regular file) = true")
	}
}

// covSetMemoryFlags sets the shared --path/--scope flags on a memory
// subcommand and restores defaults at cleanup.
func covSetMemoryFlags(t *testing.T, cmdIdx int, path, scope string) {
	t.Helper()
	cmds := []struct {
		name string
		set  func(flag, val string) error
	}{
		{"search", memorySearchCmd.Flags().Set},
		{"status", memoryStatusCmd.Flags().Set},
		{"reindex", memoryReindexCmd.Flags().Set},
	}
	c := cmds[cmdIdx]
	if err := c.set("path", path); err != nil {
		t.Fatalf("set --path on %s: %v", c.name, err)
	}
	if err := c.set("scope", scope); err != nil {
		t.Fatalf("set --scope on %s: %v", c.name, err)
	}
	t.Cleanup(func() {
		_ = c.set("path", "")
		_ = c.set("scope", "agent")
	})
}

func TestMemoryCmds_PathRequired(t *testing.T) {
	saveCLIState(t)
	covSetMemoryFlags(t, 0, "", "agent")
	if err := memorySearchCmd.RunE(memorySearchCmd, []string{"q"}); err == nil || !strings.Contains(err.Error(), "--path is required") {
		t.Errorf("search: %v", err)
	}
	covSetMemoryFlags(t, 1, "", "agent")
	if err := memoryStatusCmd.RunE(memoryStatusCmd, nil); err == nil || !strings.Contains(err.Error(), "--path is required") {
		t.Errorf("status: %v", err)
	}
	covSetMemoryFlags(t, 2, "", "agent")
	if err := memoryReindexCmd.RunE(memoryReindexCmd, nil); err == nil || !strings.Contains(err.Error(), "--path is required") {
		t.Errorf("reindex: %v", err)
	}
}

func TestMemoryCmds_BadScope(t *testing.T) {
	saveCLIState(t)
	covSetMemoryFlags(t, 0, t.TempDir(), "bogus")
	if err := memorySearchCmd.RunE(memorySearchCmd, []string{"q"}); err == nil || !strings.Contains(err.Error(), "unknown scope") {
		t.Errorf("search bad scope: %v", err)
	}
}

func TestMemoryReindexStatusSearch_EndToEnd(t *testing.T) {
	saveCLIState(t)
	base := t.TempDir()
	memDir := filepath.Join(base, ".memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "note.md"), []byte("# Notes\nThe kraken sleeps beneath the waves.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// reindex builds the FTS index from the markdown file.
	covSetMemoryFlags(t, 2, base, "agent")
	out, err := covCaptureStdoutCli7(t, func() error {
		return memoryReindexCmd.RunE(memoryReindexCmd, nil)
	})
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if !strings.Contains(out, "[agent] reindexed") {
		t.Errorf("reindex output = %q", out)
	}

	// status reads the freshly built index.
	covSetMemoryFlags(t, 1, base, "agent")
	out, err = covCaptureStdoutCli7(t, func() error {
		return memoryStatusCmd.RunE(memoryStatusCmd, nil)
	})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "Files:   1") {
		t.Errorf("status should report 1 indexed file, got %q", out)
	}

	// search finds the indexed content (table format).
	covSetMemoryFlags(t, 0, base, "agent")
	if err := memorySearchCmd.Flags().Set("format", "table"); err != nil {
		t.Fatal(err)
	}
	out, err = covCaptureStdoutCli7(t, func() error {
		return memorySearchCmd.RunE(memorySearchCmd, []string{"kraken"})
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "[agent]") || !strings.Contains(out, "note.md") {
		t.Errorf("search output = %q", out)
	}

	// JSON format path.
	if err := memorySearchCmd.Flags().Set("format", "json"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memorySearchCmd.Flags().Set("format", "table") })
	out, err = covCaptureStdoutCli7(t, func() error {
		return memorySearchCmd.RunE(memorySearchCmd, []string{"kraken"})
	})
	if err != nil {
		t.Fatalf("search json: %v", err)
	}
	if !strings.Contains(out, `"source": "agent"`) {
		t.Errorf("json search output = %q", out)
	}

	// A query with no hits prints the empty-state message.
	out, err = covCaptureStdoutCli7(t, func() error {
		return memorySearchCmd.RunE(memorySearchCmd, []string{"zebrasaurus"})
	})
	if err != nil {
		t.Fatalf("search no hits: %v", err)
	}
	if !strings.Contains(out, "No results found.") {
		t.Errorf("expected empty-state output, got %q", out)
	}
}

func TestMemoryReindex_AllTargetsFailing(t *testing.T) {
	saveCLIState(t)
	// Point at a path whose .memory subdir does not exist — SQLite cannot
	// create index.sqlite inside a missing directory, so every reindex
	// target fails and the command must report it.
	base := filepath.Join(t.TempDir(), "missing-root")
	covSetMemoryFlags(t, 2, base, "agent")

	out, err := covCaptureStdoutCli7(t, func() error {
		return memoryReindexCmd.RunE(memoryReindexCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "all reindex operations failed") {
		t.Fatalf("expected all-failed error, got %v (out=%q)", err, out)
	}
}

func TestMemoryStatus_NotInitialized(t *testing.T) {
	saveCLIState(t)
	base := filepath.Join(t.TempDir(), "missing-root")
	covSetMemoryFlags(t, 1, base, "agent")

	out, err := covCaptureStdoutCli7(t, func() error {
		return memoryStatusCmd.RunE(memoryStatusCmd, nil)
	})
	if err != nil {
		t.Fatalf("status should not error on missing index: %v", err)
	}
	if !strings.Contains(out, "not initialized") {
		t.Errorf("expected 'not initialized', got %q", out)
	}
}

func TestMemorySearch_MissingIndexIsSkipped(t *testing.T) {
	saveCLIState(t)
	base := filepath.Join(t.TempDir(), "missing-root")
	covSetMemoryFlags(t, 0, base, "agent")

	out, err := covCaptureStdoutCli7(t, func() error {
		return memorySearchCmd.RunE(memorySearchCmd, []string{"anything"})
	})
	if err != nil {
		t.Fatalf("search on missing index should be a soft skip: %v", err)
	}
	if !strings.Contains(out, "No results found.") {
		t.Errorf("expected empty-state output, got %q", out)
	}
}

// ─── additional error paths ──────────────────────────────────────────────

func TestMemorySearch_VerboseSkipLogging(t *testing.T) {
	saveCLIState(t)
	origVerbose := flagVerbose
	flagVerbose = true
	t.Cleanup(func() { flagVerbose = origVerbose })

	base := filepath.Join(t.TempDir(), "missing-root")
	covSetMemoryFlags(t, 0, base, "agent")

	out, err := covCaptureStdoutCli7(t, func() error {
		return memorySearchCmd.RunE(memorySearchCmd, []string{"anything"})
	})
	if err != nil {
		t.Fatalf("verbose search on missing index: %v", err)
	}
	if !strings.Contains(out, "No results found.") {
		t.Errorf("expected empty-state output, got %q", out)
	}
}
