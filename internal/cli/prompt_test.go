package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildPrompt_PositionalOnly(t *testing.T) {
	got, err := BuildPrompt(PromptOptions{
		Positional:  []string{"create", "a", "REST", "API"},
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "create a REST API" {
		t.Errorf("got %q, want %q", got, "create a REST API")
	}
}

func TestBuildPrompt_FlagOverridesPositional(t *testing.T) {
	got, err := BuildPrompt(PromptOptions{
		Positional:  []string{"ignored"},
		PromptFlag:  "use this instead",
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "use this instead" {
		t.Errorf("got %q", got)
	}
}

func TestBuildPrompt_AtFile(t *testing.T) {
	got, err := BuildPrompt(PromptOptions{
		PromptFlag:  "@task.txt",
		readFile:    func(p string) ([]byte, error) { return []byte("file body\n"), nil },
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "file body" {
		t.Errorf("got %q", got)
	}
}

func TestBuildPrompt_AtFile_Error(t *testing.T) {
	_, err := BuildPrompt(PromptOptions{
		PromptFlag:  "@missing.txt",
		readFile:    func(p string) ([]byte, error) { return nil, errors.New("nope") },
		isStdinPipe: func() bool { return false },
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildPrompt_AtDashStdin(t *testing.T) {
	got, err := BuildPrompt(PromptOptions{
		PromptFlag:  "@-",
		readStdin:   func() ([]byte, error) { return []byte("from stdin\n"), nil },
		isStdinPipe: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "from stdin" {
		t.Errorf("got %q", got)
	}
}

func TestBuildPrompt_AutoStdinAppends(t *testing.T) {
	got, err := BuildPrompt(PromptOptions{
		Positional:  []string{"review", "this"},
		AutoStdin:   true,
		readStdin:   func() ([]byte, error) { return []byte("diff line\nanother\n"), nil },
		isStdinPipe: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.HasPrefix(got, "review this\n\n--- stdin ---\n") {
		t.Errorf("expected stdin appended, got: %q", got)
	}
	if !strings.Contains(got, "diff line") {
		t.Errorf("missing stdin content: %q", got)
	}
}

func TestBuildPrompt_AutoStdinSkippedWhenNotPipe(t *testing.T) {
	got, err := BuildPrompt(PromptOptions{
		Positional:  []string{"hello"},
		AutoStdin:   true,
		readStdin:   func() ([]byte, error) { t.Fatal("stdin should not be read"); return nil, nil },
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestBuildPrompt_AtDashSuppressesAutoStdin(t *testing.T) {
	calls := 0
	got, err := BuildPrompt(PromptOptions{
		PromptFlag: "@-",
		AutoStdin:  true,
		readStdin: func() ([]byte, error) {
			calls++
			return []byte("once"), nil
		},
		isStdinPipe: func() bool { return true },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if calls != 1 {
		t.Errorf("stdin should be read exactly once, got %d", calls)
	}
	if got != "once" {
		t.Errorf("got %q", got)
	}
}

func TestBuildPrompt_GitDiff(t *testing.T) {
	got, err := BuildPrompt(PromptOptions{
		Positional:  []string{"review"},
		WithGitDiff: true,
		runCmd: func(name string, args ...string) ([]byte, error) {
			if name != "git" || len(args) < 1 || args[0] != "diff" {
				t.Fatalf("unexpected cmd: %s %v", name, args)
			}
			return []byte("diff --git ...\n+changes\n"), nil
		},
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "--- git diff ---") {
		t.Errorf("missing label: %q", got)
	}
	if !strings.Contains(got, "+changes") {
		t.Errorf("missing content: %q", got)
	}
}

func TestBuildPrompt_WithFile(t *testing.T) {
	got, err := BuildPrompt(PromptOptions{
		Positional: []string{"explain"},
		WithFiles:  []string{"/tmp/foo.go"},
		readFile: func(p string) ([]byte, error) {
			if p != "/tmp/foo.go" {
				t.Fatalf("unexpected path: %s", p)
			}
			return []byte("package foo\n"), nil
		},
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "--- file: /tmp/foo.go ---") {
		t.Errorf("missing label: %q", got)
	}
	if !strings.Contains(got, "package foo") {
		t.Errorf("missing content: %q", got)
	}
}

func TestBuildPrompt_WithCmd(t *testing.T) {
	got, err := BuildPrompt(PromptOptions{
		Positional: []string{"investigate"},
		WithCmds:   []string{"ps aux"},
		runCmd: func(name string, args ...string) ([]byte, error) {
			if name != "sh" || len(args) != 2 || args[0] != "-c" || args[1] != "ps aux" {
				t.Fatalf("unexpected cmd: %s %v", name, args)
			}
			return []byte("PID 1 init\n"), nil
		},
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "--- $ ps aux ---") {
		t.Errorf("missing label: %q", got)
	}
}

func TestBuildPrompt_CapBytes(t *testing.T) {
	big := strings.Repeat("x", 10_000)
	got, err := BuildPrompt(PromptOptions{
		Positional:      []string{"review"},
		WithFiles:       []string{"big.txt"},
		MaxContextBytes: 100,
		readFile:        func(p string) ([]byte, error) { return []byte(big), nil },
		isStdinPipe:     func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "(truncated)") {
		t.Errorf("expected truncation marker: %q", got[:200])
	}
}

func TestBuildPrompt_EmptyContextSkipped(t *testing.T) {
	got, err := BuildPrompt(PromptOptions{
		Positional:  []string{"hi"},
		WithGitDiff: true,
		runCmd:      func(name string, args ...string) ([]byte, error) { return []byte(""), nil },
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "hi" {
		t.Errorf("expected empty diff to be skipped, got %q", got)
	}
}

func TestBuildPrompt_MultipleContexts(t *testing.T) {
	got, err := BuildPrompt(PromptOptions{
		Positional:    []string{"review"},
		WithGitDiff:   true,
		WithGitStatus: true,
		WithFiles:     []string{"a.txt"},
		runCmd: func(name string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "diff" {
				return []byte("diff body"), nil
			}
			return []byte("status body"), nil
		},
		readFile:    func(p string) ([]byte, error) { return []byte("file body"), nil },
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, want := range []string{"--- git diff ---", "--- git status ---", "--- file: a.txt ---", "diff body", "status body", "file body"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in result", want)
		}
	}
}

func TestBuildPrompt_OnlyContextNoBase(t *testing.T) {
	got, err := BuildPrompt(PromptOptions{
		WithGitDiff: true,
		runCmd:      func(name string, args ...string) ([]byte, error) { return []byte("just a diff"), nil },
		isStdinPipe: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.HasPrefix(got, "--- git diff ---") {
		t.Errorf("expected to start with label, got: %q", got)
	}
}
